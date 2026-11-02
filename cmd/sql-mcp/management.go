package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/longkeyy/sqlx-mcp-server/internal/config"
	"github.com/longkeyy/sqlx-mcp-server/internal/instances"
	"github.com/longkeyy/sqlx-mcp-server/internal/routing"
	"github.com/longkeyy/sqlx-mcp-server/internal/tokens"
	"github.com/longkeyy/sqlx-mcp-server/internal/webui"
	"github.com/mark3labs/mcp-go/mcp"
)

func runManagedServe(ctx context.Context, cfg *config.AppConfig, logger *log.Logger) int {
	manager, err := instances.Open(cfg.DataDir)
	if err != nil {
		logger.Printf("Management store: %v", err)
		return exitRuntime
	}
	defer manager.Close()
	manager.SetExternal(cfg.Databases)
	auth, err := tokens.Open(cfg.DataDir, cfg.AuthToken)
	if err != nil {
		logger.Printf("Token store: %v", err)
		return exitRuntime
	}
	defer auth.Close()
	router, err := routing.New(manager, routing.WithAuthorizer(func(ctx context.Context, tool string) error {
		return auth.Authorize(tokens.TokenFromContext(ctx), tool)
	}))
	if err != nil {
		return exitRuntime
	}
	defer router.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthzHandler)
	mux.Handle("/api/tokens", auth.Handler())
	mux.Handle("/api/tokens/", auth.Handler())
	admin := manager.Handler()
	mux.Handle("/api/settings", admin)
	mux.Handle("/api/settings/", admin)
	mux.Handle("/api/instances", admin)
	mux.Handle("/api/instances/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/instances/"), "/")
		if len(parts) >= 2 && parts[1] == "objects" {
			serveMetadata(w, r, router, parts)
			return
		}
		admin.ServeHTTP(w, r)
	}))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeJSONError(w, 404, "unknown API endpoint") })
	protected := auth.Middleware(router)
	for _, path := range []string{"/mcp", "/sse", "/message", "/db/"} {
		mux.Handle(path, protected)
	}
	mux.Handle("/", webui.Handler())
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		logger.Printf("cannot listen: %v", err)
		return exitRuntime
	}
	streams := newStreamShutdown()
	srv := &http.Server{Handler: streams.wrap(mux), ReadHeaderTimeout: readHeaderTimeout}
	srv.RegisterOnShutdown(streams.end)
	logger.Printf("Serving management UI and MCP on http://%s", listener.Addr())
	return serveHTTP(ctx, srv, listener, logger, shutdownTimeout)
}

func serveMetadata(w http.ResponseWriter, r *http.Request, router *routing.Router, parts []string) {
	if r.Method != http.MethodGet {
		writeJSONError(w, 405, "method not allowed")
		return
	}
	tool := "list_objects"
	args := map[string]any{}
	if len(parts) == 3 && parts[2] == "describe" {
		tool = "describe_object"
	} else if len(parts) != 2 {
		writeJSONError(w, 404, "not found")
		return
	}
	for key, values := range r.URL.Query() {
		if len(values) != 1 {
			writeJSONError(w, 400, "duplicate query parameter")
			return
		}
		value := values[0]
		switch key {
		case "page_size":
			n, err := strconv.Atoi(value)
			if err != nil {
				writeJSONError(w, 400, "invalid page_size")
				return
			}
			args[key] = n
		case "group_shards":
			b, err := strconv.ParseBool(value)
			if err != nil {
				writeJSONError(w, 400, "invalid group_shards")
				return
			}
			args[key] = b
		case "type", "pattern", "cursor", "group_id", "object_name", "object_type":
			args[key] = value
		default:
			writeJSONError(w, 400, "unknown metadata parameter")
			return
		}
	}
	result, err := router.CallMetadata(r.Context(), parts[0], tool, args)
	if err != nil || result == nil {
		writeJSONError(w, 503, "metadata unavailable")
		return
	}
	if result.IsError {
		writeJSONError(w, 400, "metadata request failed; check instance and parameters")
		return
	}
	for _, content := range result.Content {
		if text, ok := content.(mcp.TextContent); ok {
			if !json.Valid([]byte(text.Text)) {
				break
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			fmt.Fprint(w, text.Text)
			return
		}
	}
	writeJSONError(w, 500, "invalid metadata response")
}
