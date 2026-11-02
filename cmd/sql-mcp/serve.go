package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/longkeyy/sqlx-mcp-server/internal/config"
	sqlmcp "github.com/longkeyy/sqlx-mcp-server/internal/server"
)

const (
	shutdownTimeout   = 10 * time.Second
	readHeaderTimeout = 10 * time.Second
)

// runServe starts the HTTP/SSE server for the statically configured databases.
//
// The listener is bound before any database is opened so an address conflict
// fails fast. An unavailable database is isolated: healthy instances and
// liveness stay up while the failed instance answers 503. With no database at
// all the process still serves /healthz; MCP routes answer 503.
func runServe(ctx context.Context, cfg *config.AppConfig, logger *log.Logger) int {
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		logger.Printf("Server error: cannot listen on %s: %v", cfg.Listen, err)
		return exitRuntime
	}

	apps, _ := buildServeApps(cfg, logger)
	defer sqlmcp.CloseAppServers(apps)

	handler, endStreams := newServeHandler(cfg, apps)
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
	}
	// SSE and Streamable HTTP GET streams only end when their request context
	// does; end them as soon as shutdown starts so draining is prompt.
	srv.RegisterOnShutdown(endStreams)

	logger.Printf("Serving %d of %d static database instance(s) on http://%s (default=%q, auth=%t)",
		len(apps), len(cfg.Databases), listener.Addr(), cfg.DefaultDatabase, cfg.AuthToken != "")
	logger.Printf("Routes: /mcp, /sse, /message, /db/{id}/mcp|sse|message; liveness: /healthz")

	return serveHTTP(ctx, srv, listener, logger, shutdownTimeout)
}

// buildServeApps opens each configured database independently and
// concurrently. It returns the healthy subset plus a redacted failure reason
// per id, so one broken DSN never hides the others. Reasons go to the operator
// log only; HTTP clients get a generic message (see mountUnavailable).
func buildServeApps(cfg *config.AppConfig, logger *log.Logger) (map[string]*sqlmcp.AppServer, map[string]string) {
	apps := make(map[string]*sqlmcp.AppServer, len(cfg.Databases))
	failures := make(map[string]string)

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, database := range cfg.Databases {
		wg.Add(1)
		go func(database config.DatabaseConfig) {
			defer wg.Done()
			built, err := sqlmcp.BuildAppServers([]config.DatabaseConfig{database})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				reason := config.RedactDSN(err.Error(), database.DSN)
				failures[database.ID] = reason
				logger.Printf("Database %q is unavailable: %s", database.ID, reason)
				return
			}
			for id, app := range built {
				apps[id] = app
			}
		}(database)
	}
	wg.Wait()
	return apps, failures
}

// newServeHandler assembles the routing table and returns it with a function
// that ends long-lived streams at shutdown.
//
// /healthz is deliberately outside authentication: liveness probes must work
// without credentials, and it exposes no data. Every MCP route, including the
// legacy default routes, stays behind the optional bearer/query token.
func newServeHandler(cfg *config.AppConfig, apps map[string]*sqlmcp.AppServer) (http.Handler, func()) {
	mcpMux := http.NewServeMux()

	// The legacy routes are bound to the configured default only. When it is
	// missing or failed they answer 503 instead of switching to another
	// instance, so a client never talks to a database it did not select.
	switch app, ok := apps[cfg.DefaultDatabase]; {
	case ok:
		mountInstance(mcpMux, "", app)
	case cfg.DefaultDatabase == "":
		mountUnavailable(mcpMux, "", "no default database instance is configured")
	default:
		mountUnavailable(mcpMux, "", unavailableMessage(cfg.DefaultDatabase))
	}

	for _, database := range cfg.Databases {
		prefix := "/db/" + database.ID
		if app, ok := apps[database.ID]; ok {
			mountInstance(mcpMux, prefix, app)
			continue
		}
		mountUnavailable(mcpMux, prefix, unavailableMessage(database.ID))
	}

	// Unknown instance ids are client errors, never a silent fallback to the
	// default instance.
	mcpMux.HandleFunc("/db/", func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusNotFound, "unknown database instance")
	})

	streams := newStreamShutdown()
	root := http.NewServeMux()
	root.HandleFunc("/healthz", healthzHandler)
	root.Handle("/", sqlmcp.AuthMiddleware(cfg.AuthToken)(streams.wrap(mcpMux)))
	return root, streams.end
}

// mountInstance registers both transports for one database. An empty prefix is
// the legacy default binding (/mcp, /sse, /message); named instances live under
// /db/{id}/... exactly as before.
func mountInstance(mux *http.ServeMux, prefix string, app *sqlmcp.AppServer) {
	mcpPath := prefix + "/mcp"
	ssePath := prefix + "/sse"
	messagePath := prefix + "/message"

	mux.Handle(mcpPath, mcpserver.NewStreamableHTTPServer(
		app.MCPServer(),
		mcpserver.WithEndpointPath(mcpPath),
	))

	sse := mcpserver.NewSSEServer(
		app.MCPServer(),
		mcpserver.WithSSEEndpoint(ssePath),
		mcpserver.WithMessageEndpoint(messagePath),
		mcpserver.WithAppendQueryToMessageEndpoint(),
	)
	mux.Handle(ssePath, sse)
	mux.Handle(messagePath, sse)
}

// mountUnavailable answers the MCP paths of an instance that could not be
// opened. Liveness stays 200; the transports report 503. The message never
// carries driver errors, which can contain hosts, users or file paths.
func mountUnavailable(mux *http.ServeMux, prefix, message string) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusServiceUnavailable, message)
	}
	mux.HandleFunc(prefix+"/mcp", handler)
	mux.HandleFunc(prefix+"/sse", handler)
	mux.HandleFunc(prefix+"/message", handler)
}

func unavailableMessage(id string) string {
	return fmt.Sprintf("database instance %q is unavailable; see the server log", id)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// healthzHandler is a liveness probe: it reports that the process is serving
// HTTP, independent of how many database instances are reachable.
func healthzHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
	}
}

// streamShutdown ends long-lived GET streams (legacy SSE and the Streamable
// HTTP notification stream) when shutdown begins. Those handlers return only
// when their request context is done, so without this http.Server.Shutdown
// would wait out the grace period for every connected client. Other requests
// keep their own context and are allowed to finish.
type streamShutdown struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func newStreamShutdown() *streamShutdown {
	ctx, cancel := context.WithCancel(context.Background())
	return &streamShutdown{ctx: ctx, cancel: cancel}
}

func (s *streamShutdown) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		stop := context.AfterFunc(s.ctx, cancel)
		defer stop()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *streamShutdown) end() {
	s.cancel()
}

// serveHTTP runs the server until ctx is cancelled (SIGINT/SIGTERM) or the
// listener fails, then drains in-flight requests within grace and
// force-closes anything still open.
func serveHTTP(ctx context.Context, srv *http.Server, listener net.Listener, logger *log.Logger, grace time.Duration) int {
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(listener)
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("Server error: %v", err)
			return exitRuntime
		}
		return exitOK
	case <-ctx.Done():
		logger.Printf("Shutdown requested; draining connections (up to %s)", grace)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		// Requests still running after the grace period are cut off so the
		// process can exit; the shutdown itself was requested, so exit 0.
		logger.Printf("Forcing close of remaining connections: %v", err)
		_ = srv.Close()
	}
	<-serveErr
	logger.Printf("Server stopped")
	return exitOK
}
