// Package routing exposes the multiplexed MCP front door for multiple database
// instances.
//
// The router is a standalone http.Handler. It owns the transport mapping
// (/mcp, /db/{id}/mcp, /sse + /message, /db/{id}/sse + /db/{id}/message) and
// resolves the target database from the registry at every tool call. SQL work
// is delegated to the existing internal/server AppServer tools; this package
// only adds instance selection, authorization, pool lifecycle and result
// tagging.
package routing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/longkeyy/sqlx-mcp-server/internal/config"
)

// Registry is the minimal instance catalog the router depends on. The
// production implementation is expected to be the instance manager; tests use
// in-memory doubles. The router never falls back to a default instance: every
// operation names its target explicitly.
type Registry interface {
	List(context.Context) ([]config.DatabaseConfig, error)
	Resolve(context.Context, string) (config.DatabaseConfig, error)
}

// Authorizer is invoked before every tools/call with the request context and
// the MCP tool name. A non-nil error blocks the call before any SQL is
// executed. The context is the one handed to ServeHTTP, so wiring code can
// read authenticated identity from it on both transports.
type Authorizer func(context.Context, string) error

// Option configures a Router.
type Option func(*Router)

// WithAuthorizer installs a per-call authorization hook. Passing nil (or not
// supplying the option at all) leaves the router unrestricted.
func WithAuthorizer(authorize func(context.Context, string) error) Option {
	return func(r *Router) { r.authorize = authorize }
}

// ErrClosed is returned when a router is used after Close.
var ErrClosed = errors.New("routing: router is closed")

// Router is an http.Handler that exposes the multi-instance MCP gateway.
type Router struct {
	registry  Registry
	authorize Authorizer

	unified     *mcpserver.MCPServer
	unifiedHTTP *mcpserver.StreamableHTTPServer
	rootSSE     *mcpserver.SSEServer

	mu       sync.Mutex
	closed   bool
	current  map[string]*instanceEntry
	retired  []*instanceEntry
	building map[string]*poolBuild
	fixed    map[string]*fixedEndpoint
}

// fixedEndpoint bundles the transport handlers for one fixed instance ID.
//
// The MCP server, and therefore the advertised tool schemas, is created once
// per instance ID and does not depend on a live connection. Handlers re-resolve
// the registry on every call, so deleting, reconfiguring or re-adding an
// instance takes effect within already initialized sessions.
type fixedEndpoint struct {
	id         string
	server     *mcpserver.MCPServer
	streamable *mcpserver.StreamableHTTPServer
	sse        *mcpserver.SSEServer
}

// New builds a router over the given registry. It does not connect to any
// database: pools are created lazily on the first operation that targets an
// instance.
func New(registry Registry, options ...Option) (*Router, error) {
	if registry == nil {
		return nil, errors.New("routing: a registry is required")
	}
	r := &Router{
		registry: registry,
		current:  make(map[string]*instanceEntry),
		building: make(map[string]*poolBuild),
		fixed:    make(map[string]*fixedEndpoint),
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		option(r)
	}
	r.unified = r.buildUnifiedServer()
	r.unifiedHTTP = mcpserver.NewStreamableHTTPServer(r.unified)
	r.rootSSE = mcpserver.NewSSEServer(r.unified, mcpserver.WithAppendQueryToMessageEndpoint())
	return r, nil
}

// ServeHTTP dispatches the MCP transports:
//
//	/mcp                unified gateway (tools require an explicit instance)
//	/db/{id}/mcp        fixed instance
//	/sse, /message      unified gateway over the legacy SSE transport
//	/db/{id}/sse        fixed instance over the legacy SSE transport
//	/db/{id}/message    fixed instance message endpoint
//
// Unknown paths are always 404, even after Close. Known paths answer 503 once
// the router has been closed.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	path := req.URL.EscapedPath()

	id, tail, fixed := splitInstancePath(path)
	if !fixed && path != "/mcp" && path != "/sse" && path != "/message" {
		http.NotFound(w, req)
		return
	}
	if fixed && tail != "mcp" && tail != "sse" && tail != "message" {
		http.NotFound(w, req)
		return
	}
	if r.isClosed() {
		http.Error(w, ErrClosed.Error(), http.StatusServiceUnavailable)
		return
	}

	if !fixed {
		switch path {
		case "/mcp":
			r.unifiedHTTP.ServeHTTP(w, req)
		case "/sse":
			r.rootSSE.SSEHandler().ServeHTTP(w, req)
		case "/message":
			r.rootSSE.MessageHandler().ServeHTTP(w, req)
		}
		return
	}

	endpoint, err := r.fixedEndpoint(req.Context(), id)
	if err != nil {
		// An unknown or removed instance has no endpoint at all. Sessions that
		// already exist may stay connected; every tool call re-resolves the
		// registry and fails there if the instance disappeared in the meantime.
		http.NotFound(w, req)
		return
	}
	switch tail {
	case "mcp":
		endpoint.streamable.ServeHTTP(w, req)
	case "sse":
		endpoint.sse.SSEHandler().ServeHTTP(w, req)
	case "message":
		endpoint.sse.MessageHandler().ServeHTTP(w, req)
	}
}

func (r *Router) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// splitInstancePath parses /db/{id}/{tail} out of an escaped URL path.
func splitInstancePath(path string) (id, tail string, ok bool) {
	rest, found := strings.CutPrefix(path, "/db/")
	if !found {
		return "", "", false
	}
	rawID, tail, found := strings.Cut(rest, "/")
	if !found || rawID == "" || tail == "" {
		return "", "", false
	}
	id, err := url.PathUnescape(rawID)
	if err != nil || id == "" || strings.Contains(id, "/") {
		return "", "", false
	}
	return id, tail, true
}

// fixedEndpoint returns the (lazily created) transports for a fixed instance.
// The endpoint is keyed by the canonical ID reported by the registry; its
// creation needs the instance to exist, but not to accept connections.
func (r *Router) fixedEndpoint(ctx context.Context, id string) (*fixedEndpoint, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, ErrClosed
	}
	endpoint := r.fixed[id]
	r.mu.Unlock()
	if endpoint != nil {
		return endpoint, nil
	}

	cfg, err := r.registry.Resolve(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("routing: instance %q: %w", id, err)
	}
	canonical := cfg.ID
	if canonical == "" {
		canonical = id
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	if existing := r.fixed[canonical]; existing != nil {
		return existing, nil
	}
	candidate := r.buildFixedEndpoint(canonical)
	r.fixed[canonical] = candidate
	return candidate, nil
}

func (r *Router) buildFixedEndpoint(id string) *fixedEndpoint {
	server := r.buildFixedServer(id)
	return &fixedEndpoint{
		id:         id,
		server:     server,
		streamable: mcpserver.NewStreamableHTTPServer(server),
		sse: mcpserver.NewSSEServer(
			server,
			mcpserver.WithSSEEndpoint("/db/"+url.PathEscape(id)+"/sse"),
			mcpserver.WithMessageEndpoint("/db/"+url.PathEscape(id)+"/message"),
			mcpserver.WithAppendQueryToMessageEndpoint(),
		),
	}
}

func (e *fixedEndpoint) shutdown() {
	e.sse.CloseSessions()
	_ = e.streamable.Shutdown(context.Background())
}

// Close shuts down transports and closes every pool owned by the router.
// It is safe to call more than once.
func (r *Router) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	entries := make([]*instanceEntry, 0, len(r.current)+len(r.retired))
	for id, entry := range r.current {
		entries = append(entries, entry)
		delete(r.current, id)
	}
	entries = append(entries, r.retired...)
	r.retired = nil
	endpoints := r.fixed
	r.fixed = make(map[string]*fixedEndpoint)
	r.mu.Unlock()

	for _, entry := range entries {
		closeEntryApp(entry)
	}
	r.rootSSE.CloseSessions()
	var errs []error
	if err := r.unifiedHTTP.Shutdown(context.Background()); err != nil {
		errs = append(errs, err)
	}
	for _, endpoint := range endpoints {
		endpoint.shutdown()
	}
	return errors.Join(errs...)
}

// FixedServer exposes the fixed-instance tools for a trusted stdio transport.
// It pins the instance ID, but resolves its current configuration on each call.
// Construct this router without an HTTP token authorizer for direct stdio.
func (r *Router) FixedServer(ctx context.Context, id string) (*mcpserver.MCPServer, error) {
	endpoint, err := r.fixedEndpoint(ctx, id)
	if err != nil {
		return nil, err
	}
	return endpoint.server, nil
}
