package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/longkeyy/sqlx-mcp-server/internal/config"
	sqlmcp "github.com/longkeyy/sqlx-mcp-server/internal/server"
)

const testDefaultDatabase = "DEFAULT_DB"

// sqliteDSN creates a real SQLite database with a marker row and returns a
// DSN for it. Every test gets its own temporary database; nothing is canned.
func sqliteDSN(t *testing.T, marker string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE marker(value TEXT); INSERT INTO marker VALUES (?)", marker); err != nil {
		t.Fatal(err)
	}
	return "sqlite://" + path
}

// brokenSQLiteDSN returns a DSN whose parent directory does not exist, so the
// connection fails at ping time without needing a network service.
func brokenSQLiteDSN(t *testing.T) string {
	t.Helper()
	return "sqlite://" + filepath.Join(t.TempDir(), "missing", "dir", "db.sqlite")
}

// randomToken produces a per-run credential; tests never hardcode secrets.
func randomToken(t *testing.T) string {
	t.Helper()
	return rand.Text()
}

func newTestConfig(t *testing.T, args []string, env map[string]string) *config.AppConfig {
	t.Helper()
	cfg, err := config.ParseArgs(args, env)
	if err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return cfg
}

func newTestServeHandler(t *testing.T, cfg *config.AppConfig) http.Handler {
	t.Helper()
	handler, _, _ := newTestServeHandlerWithApps(t, cfg)
	return handler
}

func newTestServeHandlerWithApps(t *testing.T, cfg *config.AppConfig) (http.Handler, map[string]*sqlmcp.AppServer, map[string]string) {
	t.Helper()
	logger := log.New(io.Discard, "", 0)
	apps, failures := buildServeApps(cfg, logger)
	t.Cleanup(func() { sqlmcp.CloseAppServers(apps) })
	handler, _ := newServeHandler(cfg, apps)
	return handler, apps, failures
}

func doRequest(t *testing.T, handler http.Handler, method, path string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func mcpHeaders(token string) map[string]string {
	headers := map[string]string{
		"Content-Type":         "application/json",
		"Accept":               "application/json, text/event-stream",
		"MCP-Protocol-Version": "2025-03-26",
	}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return headers
}

// mcpClient talks JSON-RPC over the streamable HTTP endpoint of a handler and
// carries the Mcp-Session-Id issued by initialize, like a real client.
type mcpClient struct {
	handler   http.Handler
	path      string
	token     string
	sessionID string
}

func (c *mcpClient) call(t *testing.T, method string, params any) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	headers := mcpHeaders(c.token)
	if c.sessionID != "" {
		headers["Mcp-Session-Id"] = c.sessionID
	}
	recorder := doRequest(t, c.handler, http.MethodPost, c.path, bytes.NewReader(body), headers)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s %s status=%d body=%s", method, c.path, recorder.Code, recorder.Body.String())
	}
	if sessionID := recorder.Header().Get("Mcp-Session-Id"); sessionID != "" {
		c.sessionID = sessionID
	}
	payload := recorder.Body.Bytes()
	if strings.Contains(recorder.Header().Get("Content-Type"), "text/event-stream") {
		payload = firstSSEData(recorder.Body.Bytes())
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("%s: invalid JSON %q: %v", method, payload, err)
	}
	return decoded
}

// firstSSEData returns the payload of the first "data:" line of an SSE body.
func firstSSEData(body []byte) []byte {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			return []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return nil
}

func (c *mcpClient) initialize(t *testing.T) {
	t.Helper()
	response := c.call(t, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "cmd-test", "version": "1"},
	})
	if response["error"] != nil || response["result"] == nil {
		t.Fatalf("initialize failed: %v", response)
	}
	if c.sessionID == "" {
		t.Fatal("initialize did not issue an Mcp-Session-Id")
	}
}

// assertMarker queries the fixture's marker table through the "query" tool and
// checks the returned value, which proves the request reached the right pool.
func assertMarker(t *testing.T, client *mcpClient, want string) {
	t.Helper()
	response := client.call(t, "tools/call", map[string]any{
		"name":      "query",
		"arguments": map[string]any{"sql": "SELECT value FROM marker"},
	})
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("query: missing result: %v", response)
	}
	if result["isError"] == true {
		t.Fatalf("query unexpectedly failed: %v", result)
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("query returned no content: %v", result)
	}
	item, _ := content[0].(map[string]any)
	var payload struct {
		Rows [][]any `json:"rows"`
	}
	if err := json.Unmarshal([]byte(fmt.Sprint(item["text"])), &payload); err != nil {
		t.Fatalf("query payload is not JSON: %v", item["text"])
	}
	if len(payload.Rows) != 1 || len(payload.Rows[0]) != 1 || fmt.Sprint(payload.Rows[0][0]) != want {
		t.Fatalf("expected marker %q, got %v", want, payload.Rows)
	}
}

func initializeBody(t *testing.T) io.Reader {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "cmd-test", "version": "1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(body)
}

func TestServeHealthzWithoutDatabases(t *testing.T) {
	token := randomToken(t)
	cfg := newTestConfig(t, []string{"serve", "--auth-token", token}, nil)
	if len(cfg.Databases) != 0 {
		t.Fatalf("expected no databases, got %+v", cfg.Databases)
	}
	handler := newTestServeHandler(t, cfg)

	// Liveness works without credentials even when auth is enabled.
	resp := doRequest(t, handler, http.MethodGet, "/healthz", nil, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("/healthz status=%d body=%s", resp.Code, resp.Body.String())
	}
	var health map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &health); err != nil || health["status"] != "ok" {
		t.Fatalf("unexpected health payload %q: %v", resp.Body.String(), err)
	}
	if head := doRequest(t, handler, http.MethodHead, "/healthz", nil, nil); head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD /healthz status=%d body=%q", head.Code, head.Body.String())
	}
	if resp := doRequest(t, handler, http.MethodPost, "/healthz", nil, nil); resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz status=%d, want 405", resp.Code)
	}

	// MCP routes need the token and answer 503 with no database configured.
	if resp := doRequest(t, handler, http.MethodPost, "/mcp", initializeBody(t), mcpHeaders("")); resp.Code != http.StatusUnauthorized {
		t.Fatalf("/mcp without token status=%d, want 401", resp.Code)
	}
	resp = doRequest(t, handler, http.MethodPost, "/mcp", initializeBody(t), mcpHeaders(token))
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("/mcp without database status=%d body=%s", resp.Code, resp.Body.String())
	}
	if resp := doRequest(t, handler, http.MethodGet, "/sse", nil, mcpHeaders(token)); resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("/sse without database status=%d", resp.Code)
	}
	if resp := doRequest(t, handler, http.MethodPost, "/db/missing/mcp", initializeBody(t), mcpHeaders(token)); resp.Code != http.StatusNotFound {
		t.Fatalf("unknown instance status=%d, want 404", resp.Code)
	}
}

func TestServeStreamableHTTPWithStaticDSN(t *testing.T) {
	cfg := newTestConfig(t, []string{"serve", "--dsn", sqliteDSN(t, "http-marker")}, nil)
	handler, apps, failures := newTestServeHandlerWithApps(t, cfg)
	if len(apps) != 1 || len(failures) != 0 {
		t.Fatalf("expected one healthy instance, got apps=%d failures=%v", len(apps), failures)
	}

	client := &mcpClient{handler: handler, path: "/mcp"}
	client.initialize(t)
	assertMarker(t, client, "http-marker")

	// The default instance is also reachable under its explicit id.
	named := &mcpClient{handler: handler, path: "/db/default/mcp"}
	named.initialize(t)
	assertMarker(t, named, "http-marker")
}

func TestServeSSEEndpointEvent(t *testing.T) {
	cfg := newTestConfig(t, []string{"serve", "--dsn", sqliteDSN(t, "sse-marker")}, nil)
	handler, _, _ := newTestServeHandlerWithApps(t, cfg)

	// A real server is required: the SSE handler streams until the client
	// disconnects, and a ResponseRecorder cannot be read concurrently.
	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/sse status=%d", resp.StatusCode)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("/sse content type = %q", contentType)
	}

	event := readEndpointEvent(t, resp.Body)
	if !strings.Contains(event, "/message?sessionId=") {
		t.Fatalf("endpoint event should point at the message route, got %q", event)
	}

	// The named instance advertises its own message path.
	namedReq, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/db/default/sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	namedResp, err := http.DefaultClient.Do(namedReq)
	if err != nil {
		t.Fatal(err)
	}
	defer namedResp.Body.Close()
	if namedResp.StatusCode != http.StatusOK {
		t.Fatalf("/db/default/sse status=%d", namedResp.StatusCode)
	}
	namedEvent := readEndpointEvent(t, namedResp.Body)
	if !strings.Contains(namedEvent, "/db/default/message?sessionId=") {
		t.Fatalf("named endpoint event should include the instance prefix, got %q", namedEvent)
	}
}

// readEndpointEvent reads SSE lines until the endpoint event's data line.
func readEndpointEvent(t *testing.T, body io.Reader) string {
	t.Helper()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	deadline := make(chan string, 1)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				deadline <- strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				return
			}
		}
		deadline <- ""
	}()
	select {
	case event := <-deadline:
		if event == "" {
			t.Fatal("stream ended before the endpoint event")
		}
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the endpoint event")
		return ""
	}
}

func TestServeIsolatesUnavailableInstance(t *testing.T) {
	good := sqliteDSN(t, "good-marker")
	broken := brokenSQLiteDSN(t)
	cfg := newTestConfig(t, []string{"serve"}, map[string]string{
		"DB_GOOD_URL":       good,
		"DB_BROKEN_URL":     broken,
		testDefaultDatabase: "good",
	})
	handler, apps, failures := newTestServeHandlerWithApps(t, cfg)
	if apps["good"] == nil {
		t.Fatalf("expected the healthy instance to survive, got %v", apps)
	}
	if failures["broken"] == "" {
		t.Fatalf("expected the broken instance to be recorded, got %v", failures)
	}

	healthy := &mcpClient{handler: handler, path: "/db/good/mcp"}
	healthy.initialize(t)
	assertMarker(t, healthy, "good-marker")

	// The configured default keeps the legacy routes.
	root := &mcpClient{handler: handler, path: "/mcp"}
	root.initialize(t)
	assertMarker(t, root, "good-marker")

	for _, path := range []string{"/db/broken/mcp", "/db/broken/sse", "/db/broken/message"} {
		resp := doRequest(t, handler, http.MethodPost, path, initializeBody(t), mcpHeaders(""))
		if resp.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status=%d, want 503", path, resp.Code)
		}
		var payload map[string]string
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatalf("%s body is not JSON: %s", path, resp.Body.String())
		}
		if payload["error"] != unavailableMessage("broken") {
			t.Fatalf("%s should return the generic message, got %q", path, payload["error"])
		}
		// Driver and filesystem details stay in the operator log.
		for _, leaked := range []string{broken, "missing", "db.sqlite"} {
			if strings.Contains(resp.Body.String(), leaked) {
				t.Fatalf("%s leaks %q: %s", path, leaked, resp.Body.String())
			}
		}
	}
}

func TestServeUnavailableDefaultDoesNotFallBack(t *testing.T) {
	// The default is configured but cannot be opened at startup.
	cfg := newTestConfig(t, []string{"serve"}, map[string]string{
		"DB_NAMED_URL":      sqliteDSN(t, "named-marker"),
		"DB_MISSING_URL":    brokenSQLiteDSN(t),
		testDefaultDatabase: "missing",
	})
	handler, apps, failures := newTestServeHandlerWithApps(t, cfg)
	if apps["named"] == nil {
		t.Fatalf("healthy instance missing: %v", apps)
	}
	if failures["missing"] == "" {
		t.Fatal("expected the default instance to fail")
	}

	resp := doRequest(t, handler, http.MethodPost, "/mcp", initializeBody(t), mcpHeaders(""))
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("default route status=%d, want 503 (no silent fallback)", resp.Code)
	}
	if strings.Contains(resp.Body.String(), "named") {
		t.Fatalf("default route must not fall back to another instance: %s", resp.Body.String())
	}

	// The healthy named instance stays reachable through its own path.
	named := &mcpClient{handler: handler, path: "/db/named/mcp"}
	named.initialize(t)
	assertMarker(t, named, "named-marker")
}

func TestServeDoesNotLeakDSNInFailedInstanceErrors(t *testing.T) {
	password := randomToken(t)
	querySecret := randomToken(t)
	// The bad escape in the path makes url.Parse fail, and its error echoes the
	// whole DSN; nothing touches the network.
	dsn := "postgres://app:" + password + "@db.internal/app%zz?password=" + querySecret
	cfg := newTestConfig(t, []string{"serve"}, map[string]string{
		"DB_BROKEN_URL":     dsn,
		testDefaultDatabase: "broken",
	})

	var logs bytes.Buffer
	logger := log.New(&syncWriter{w: &logs}, "", 0)
	apps, failures := buildServeApps(cfg, logger)
	t.Cleanup(func() { sqlmcp.CloseAppServers(apps) })
	handler, _ := newServeHandler(cfg, apps)

	reason := failures["broken"]
	if reason == "" {
		t.Fatal("expected the instance to fail to build")
	}
	if !strings.Contains(reason, "invalid DSN") {
		t.Fatalf("expected an invalid DSN error, got %q", reason)
	}
	if !strings.Contains(logs.String(), "Database \"broken\" is unavailable") {
		t.Fatalf("operator log should record the failure, got %q", logs.String())
	}

	resp := doRequest(t, handler, http.MethodPost, "/mcp", initializeBody(t), mcpHeaders(""))
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	for _, secret := range []string{password, querySecret} {
		if strings.Contains(reason, secret) {
			t.Fatalf("failure reason leaks a credential: %q", reason)
		}
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("operator log leaks a credential: %q", logs.String())
		}
		if strings.Contains(resp.Body.String(), secret) {
			t.Fatalf("response leaks a credential: %q", resp.Body.String())
		}
	}
}

// syncWriter serializes writes from the parallel build goroutines.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func TestServeRequiresAuthTokenOnMCPRoutes(t *testing.T) {
	token := randomToken(t)
	cfg := newTestConfig(t, []string{"serve", "--dsn", sqliteDSN(t, "auth-marker"), "--auth-token", token}, nil)
	handler := newTestServeHandler(t, cfg)

	if resp := doRequest(t, handler, http.MethodPost, "/mcp", initializeBody(t), mcpHeaders("")); resp.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /mcp status=%d, want 401", resp.Code)
	}
	if resp := doRequest(t, handler, http.MethodPost, "/mcp", initializeBody(t), mcpHeaders(randomToken(t))); resp.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status=%d, want 401", resp.Code)
	}
	authorized := &mcpClient{handler: handler, path: "/mcp", token: token}
	authorized.initialize(t)
	assertMarker(t, authorized, "auth-marker")
}

func TestServeHandlesGracefulShutdown(t *testing.T) {
	port := freePort(t)
	cfg := newTestConfig(t, []string{"serve", "--dsn", sqliteDSN(t, "shutdown-marker"), "--listen", port}, nil)
	apps, _ := buildServeApps(cfg, log.New(io.Discard, "", 0))
	t.Cleanup(func() { sqlmcp.CloseAppServers(apps) })
	handler, endStreams := newServeHandler(cfg, apps)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: readHeaderTimeout}
	srv.RegisterOnShutdown(endStreams)

	listener, err := net.Listen("tcp", port)
	if err != nil {
		t.Fatal(err)
	}
	exit := make(chan int, 1)
	go func() { exit <- serveHTTP(ctx, srv, listener, log.New(io.Discard, "", 0), time.Second) }()

	base := "http://" + listener.Addr().String()
	waitForHealth(t, base)

	// Hold an SSE stream open; shutdown must end it instead of draining for
	// the whole grace period.
	streamResp, err := http.Get(base + "/sse")
	if err != nil {
		t.Fatal(err)
	}
	defer streamResp.Body.Close()
	if streamResp.StatusCode != http.StatusOK {
		t.Fatalf("/sse status=%d", streamResp.StatusCode)
	}

	start := time.Now()
	cancel()
	select {
	case code := <-exit:
		if code != exitOK {
			t.Fatalf("serveHTTP exit code = %d, want %d", code, exitOK)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveHTTP did not shut down promptly")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("shutdown took %s despite the open SSE stream", elapsed)
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	return addr
}

func waitForHealth(t *testing.T, base string) {
	t.Helper()
	client := http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("health endpoint never became ready")
}
