package routing

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/longkeyy/sqlx-mcp-server/internal/config"
)

// ---------------------------------------------------------------------------
// registry test double
// ---------------------------------------------------------------------------

type memRegistry struct {
	mu         sync.RWMutex
	items      map[string]config.DatabaseConfig
	listErr    error
	resolveErr error
}

func newMemRegistry() *memRegistry {
	return &memRegistry{items: map[string]config.DatabaseConfig{}}
}

func (m *memRegistry) List(context.Context) ([]config.DatabaseConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	out := make([]config.DatabaseConfig, 0, len(m.items))
	for _, cfg := range m.items {
		out = append(out, cfg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *memRegistry) Resolve(_ context.Context, id string) (config.DatabaseConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.resolveErr != nil {
		return config.DatabaseConfig{}, m.resolveErr
	}
	cfg, ok := m.items[id]
	if !ok {
		return config.DatabaseConfig{}, fmt.Errorf("unknown instance %s", id)
	}
	return cfg, nil
}

func (m *memRegistry) put(id, dsn string) {
	m.putConfig(config.DatabaseConfig{
		ID:           id,
		Name:         id,
		Memo:         "test " + id,
		DSN:          dsn,
		MaxOpenConns: 2,
		MaxIdleConns: 1,
	})
}

func (m *memRegistry) putConfig(cfg config.DatabaseConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[cfg.ID] = cfg
}

func (m *memRegistry) remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
}

func (m *memRegistry) setListErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listErr = err
}

// ---------------------------------------------------------------------------
// sqlite fixtures
// ---------------------------------------------------------------------------

func sqliteFixture(t *testing.T, statements ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE marker(value TEXT)"); err != nil {
		t.Fatalf("create marker: %v", err)
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture statement %q: %v", statement, err)
		}
	}
	return "sqlite://" + path
}

func markerFixture(t *testing.T, label string) string {
	t.Helper()
	return sqliteFixture(t, fmt.Sprintf("INSERT INTO marker VALUES ('%s')", strings.ReplaceAll(label, "'", "''")))
}

func markerDSN(t *testing.T) string {
	t.Helper()
	return sqliteFixture(t)
}

// ---------------------------------------------------------------------------
// Streamable HTTP JSON-RPC client
// ---------------------------------------------------------------------------

type rpcClient struct {
	t       *testing.T
	url     string
	headers http.Header

	mu      sync.Mutex
	session string
	seq     int
}

// rpcResult is the outcome of a single JSON-RPC exchange. err is set instead
// of failing the test so the client can be used from helper goroutines.
type rpcResult struct {
	response map[string]any
	status   int
	err      error
}

func (c *rpcClient) nextRequest() (int, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	return c.seq, c.session
}

func (c *rpcClient) rememberSession(session string) {
	if session == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = session
}

func (c *rpcClient) post(payload map[string]any, session string) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	request, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-03-26")
	for key, values := range c.headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	if session != "" {
		request.Header.Set("Mcp-Session-Id", session)
	}
	return (&http.Client{Timeout: 15 * time.Second}).Do(request)
}

// exchange performs one JSON-RPC request without touching testing.T, which
// makes it safe to call from goroutines.
func (c *rpcClient) exchange(method string, params any) rpcResult {
	id, session := c.nextRequest()
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	response, err := c.post(payload, session)
	if err != nil {
		return rpcResult{err: fmt.Errorf("%s: %w", method, err)}
	}
	defer response.Body.Close()
	c.rememberSession(response.Header.Get("Mcp-Session-Id"))
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return rpcResult{status: response.StatusCode}
	}
	var decoded map[string]any
	if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		decoded, err = readSSEResponse(response.Body)
	} else {
		err = json.NewDecoder(response.Body).Decode(&decoded)
	}
	if err != nil {
		return rpcResult{status: response.StatusCode, err: fmt.Errorf("%s: decode response: %w", method, err)}
	}
	return rpcResult{response: decoded, status: response.StatusCode}
}

func (c *rpcClient) rpc(t *testing.T, method string, params any) (map[string]any, int) {
	t.Helper()
	result := c.exchange(method, params)
	if result.err != nil {
		t.Fatal(result.err)
	}
	return result.response, result.status
}

func (c *rpcClient) notify(t *testing.T, method string, params any) {
	t.Helper()
	_, session := c.nextRequest()
	payload := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		payload["params"] = params
	}
	response, err := c.post(payload, session)
	if err != nil {
		t.Fatalf("notification %s: %v", method, err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusAccepted && response.StatusCode != http.StatusOK {
		t.Fatalf("notification %s status = %d", method, response.StatusCode)
	}
}

func readSSEResponse(body io.Reader) (map[string]any, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &decoded); err == nil && decoded["id"] != nil {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("no JSON-RPC response in SSE stream (scanner error: %v)", scanner.Err())
}

// tools lists the advertised tool definitions keyed by name.
func (c *rpcClient) tools(t *testing.T) map[string]map[string]any {
	t.Helper()
	response, status := c.rpc(t, "tools/list", map[string]any{})
	if status != http.StatusOK || response["error"] != nil {
		t.Fatalf("tools/list failed: status=%d response=%v", status, response)
	}
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list result is not an object: %v", response["result"])
	}
	tools, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("tools/list tools is not an array: %v", result["tools"])
	}
	index := make(map[string]map[string]any, len(tools))
	for _, item := range tools {
		definition, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("tool definition is not an object: %v", item)
		}
		index[fmt.Sprint(definition["name"])] = definition
	}
	return index
}

// call invokes a tool and returns the decoded payload plus a success flag.
func (c *rpcClient) call(t *testing.T, name string, arguments map[string]any) (map[string]any, bool) {
	t.Helper()
	payload, ok, err := c.tryCall(name, arguments)
	if err != nil {
		t.Fatal(err)
	}
	return payload, ok
}

// tryCall is the goroutine-safe variant of call: protocol problems are
// returned as errors instead of failing the test.
func (c *rpcClient) tryCall(name string, arguments map[string]any) (map[string]any, bool, error) {
	exchanged := c.exchange("tools/call", map[string]any{"name": name, "arguments": arguments})
	if exchanged.err != nil {
		return nil, false, exchanged.err
	}
	response := exchanged.response
	if exchanged.status != http.StatusOK || response["error"] != nil {
		return response, false, nil
	}
	result, ok := response["result"].(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("%s result is not an object: %v", name, response["result"])
	}
	if result["isError"] == true {
		return result, false, nil
	}
	if structured, ok := result["structuredContent"].(map[string]any); ok {
		return structured, true, nil
	}
	content, ok := result["content"].([]any)
	if !ok {
		return nil, false, fmt.Errorf("%s result has no content: %v", name, result)
	}
	for _, item := range content {
		part, ok := item.(map[string]any)
		if !ok || part["type"] != "text" {
			continue
		}
		text := fmt.Sprint(part["text"])
		var decoded map[string]any
		if err := json.Unmarshal([]byte(text), &decoded); err == nil {
			return decoded, true, nil
		}
		if name == toolExecute {
			return map[string]any{"message": text}, true, nil
		}
	}
	return nil, false, fmt.Errorf("%s returned no JSON payload: %v", name, result)
}

func (c *rpcClient) mustCall(t *testing.T, name string, arguments map[string]any) map[string]any {
	t.Helper()
	payload, ok := c.call(t, name, arguments)
	if !ok {
		t.Fatalf("%s unexpectedly failed: %v", name, payload)
	}
	return payload
}

func (c *rpcClient) mustFail(t *testing.T, name string, arguments map[string]any) map[string]any {
	t.Helper()
	payload, ok := c.call(t, name, arguments)
	if ok {
		t.Fatalf("%s unexpectedly succeeded: %v", name, payload)
	}
	return payload
}

// toolErrorText returns the human-readable text of a failed tool result.
func toolErrorText(result map[string]any) string {
	content, ok := result["content"].([]any)
	if !ok {
		return fmt.Sprint(result)
	}
	var parts []string
	for _, item := range content {
		if part, ok := item.(map[string]any); ok && part["type"] == "text" {
			parts = append(parts, fmt.Sprint(part["text"]))
		}
	}
	return strings.Join(parts, "\n")
}

func markerValue(t *testing.T, c *rpcClient, arguments map[string]any) string {
	t.Helper()
	if arguments == nil {
		arguments = map[string]any{}
	}
	arguments["sql"] = "SELECT value FROM marker"
	payload := c.mustCall(t, toolQuery, arguments)
	rows, ok := payload["rows"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected exactly one marker row: %v", payload)
	}
	row, ok := rows[0].([]any)
	if !ok || len(row) == 0 {
		t.Fatalf("unexpected marker row shape: %v", rows[0])
	}
	return fmt.Sprint(row[0])
}

// ---------------------------------------------------------------------------
// legacy SSE client
// ---------------------------------------------------------------------------

type sseEvent struct {
	name string
	data string
}

type sseClient struct {
	t          *testing.T
	base       string
	stream     *http.Response
	cancel     context.CancelFunc
	events     chan sseEvent
	messageURL string
}

// dialSSE opens the SSE stream, waits for the endpoint event and performs the
// MCP initialization handshake over the announced message endpoint.
func dialSSE(t *testing.T, base, ssePath string) *sseClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+ssePath, nil)
	if err != nil {
		cancel()
		t.Fatalf("new SSE request: %v", err)
	}
	request.Header.Set("Accept", "text/event-stream")
	stream, err := (&http.Client{}).Do(request)
	if err != nil {
		cancel()
		t.Fatalf("open SSE stream: %v", err)
	}
	if stream.StatusCode != http.StatusOK {
		cancel()
		stream.Body.Close()
		t.Fatalf("SSE stream status = %d", stream.StatusCode)
	}
	client := &sseClient{t: t, base: base, stream: stream, cancel: cancel, events: make(chan sseEvent, 32)}
	go client.pump()
	t.Cleanup(client.close)

	event := client.next(t, "endpoint")
	endpoint := strings.TrimSpace(event.data)
	if strings.HasPrefix(endpoint, "/") {
		client.messageURL = base + endpoint
	} else {
		client.messageURL = endpoint
	}
	response := client.rpc(t, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "routing-sse", "version": "1"},
	})
	if response["error"] != nil || response["result"] == nil {
		t.Fatalf("SSE initialize failed: %v", response)
	}
	client.post(t, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return client
}

func (c *sseClient) pump() {
	defer close(c.events)
	reader := bufio.NewReader(c.stream.Body)
	var event sseEvent
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if event.name != "" || event.data != "" {
				select {
				case c.events <- event:
				default:
				}
				event = sseEvent{}
			}
		case strings.HasPrefix(line, "event:"):
			event.name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			chunk := strings.TrimPrefix(line, "data:")
			chunk = strings.TrimPrefix(chunk, " ")
			if event.data != "" {
				event.data += "\n"
			}
			event.data += chunk
		}
	}
}

func (c *sseClient) next(t *testing.T, name string) sseEvent {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case event, ok := <-c.events:
			if !ok {
				t.Fatalf("SSE stream closed while waiting for %q", name)
			}
			if name == "" || event.name == name {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for SSE event %q", name)
		}
	}
}

func (c *sseClient) post(t *testing.T, payload map[string]any) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal SSE message: %v", err)
	}
	response, err := http.Post(c.messageURL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post SSE message: %v", err)
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("SSE message status = %d, want 202", response.StatusCode)
	}
}

func (c *sseClient) rpc(t *testing.T, method string, params any) map[string]any {
	t.Helper()
	payload := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		payload["params"] = params
	}
	c.post(t, payload)
	event := c.next(t, "message")
	var decoded map[string]any
	if err := json.Unmarshal([]byte(event.data), &decoded); err != nil {
		t.Fatalf("decode SSE message: %v (%s)", err, event.data)
	}
	return decoded
}

func (c *sseClient) call(t *testing.T, name string, arguments map[string]any) (map[string]any, bool) {
	t.Helper()
	response := c.rpc(t, "tools/call", map[string]any{"name": name, "arguments": arguments})
	if response["error"] != nil {
		return response, false
	}
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s result is not an object: %v", name, response["result"])
	}
	if result["isError"] == true {
		return result, false
	}
	content, ok := result["content"].([]any)
	if !ok {
		t.Fatalf("%s result has no content: %v", name, result)
	}
	for _, item := range content {
		part, ok := item.(map[string]any)
		if !ok || part["type"] != "text" {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(fmt.Sprint(part["text"])), &decoded); err == nil {
			return decoded, true
		}
	}
	t.Fatalf("%s returned no JSON payload: %v", name, result)
	return nil, false
}

func (c *sseClient) close() {
	c.cancel()
	_ = c.stream.Body.Close()
	// Drain so the pump goroutine can exit before the test server shuts down.
	for range c.events {
	}
}

// ---------------------------------------------------------------------------
// router harness
// ---------------------------------------------------------------------------

type harness struct {
	router *Router
	server *httptest.Server
	base   string
}

func newHarness(t *testing.T, registry Registry, options ...Option) *harness {
	t.Helper()
	router, err := New(registry, options...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(func() {
		server.Close()
		if err := router.Close(); err != nil {
			t.Errorf("router.Close: %v", err)
		}
	})
	return &harness{router: router, server: server, base: server.URL}
}

func (h *harness) client(t *testing.T, path string, headers http.Header) *rpcClient {
	t.Helper()
	return newRPCClient(t, h.base, path, headers)
}

// newRPCClient opens an initialized Streamable HTTP MCP session against
// base+path. Tests that wrap the router (to inject request context) use it
// directly instead of going through a harness.
func newRPCClient(t *testing.T, base, path string, headers http.Header) *rpcClient {
	t.Helper()
	c := &rpcClient{t: t, url: base + path, headers: headers}
	return c.initialize()
}

func (c *rpcClient) initialize() *rpcClient {
	c.t.Helper()
	response, status := c.rpc(c.t, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "routing-unit", "version": "1"},
	})
	if status != http.StatusOK || response["error"] != nil || response["result"] == nil {
		c.t.Fatalf("initialize %s failed: status=%d response=%v", c.url, status, response)
	}
	c.notify(c.t, "notifications/initialized", nil)
	return c
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

func TestNewRequiresRegistry(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New(nil) must fail")
	}
}

func TestDiscoveryListsInstancesWithoutSecrets(t *testing.T) {
	registry := newMemRegistry()
	registry.put("beta", markerFixture(t, "beta"))
	alpha := config.DatabaseConfig{
		ID:           "alpha",
		Name:         "Alpha database",
		Memo:         "primary",
		DSN:          markerFixture(t, "alpha"),
		MaxOpenConns: 2,
		MaxIdleConns: 1,
	}
	registry.putConfig(alpha)
	h := newHarness(t, registry)

	payload := h.client(t, "/mcp", nil).mustCall(t, toolListInstances, map[string]any{})
	instances, ok := payload["instances"].([]any)
	if !ok {
		t.Fatalf("instances is not an array: %v", payload)
	}
	if len(instances) != 2 {
		t.Fatalf("want two instances, got %v", instances)
	}
	first, _ := instances[0].(map[string]any)
	second, _ := instances[1].(map[string]any)
	if first["id"] != "alpha" || second["id"] != "beta" {
		t.Fatalf("instances must be sorted by id: %v", instances)
	}
	if first["name"] != "Alpha database" || first["memo"] != "primary" {
		t.Fatalf("descriptor lost metadata: %v", first)
	}
	if first["dialect"] != "SQLite" {
		t.Fatalf("dialect = %v, want SQLite", first["dialect"])
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal discovery: %v", err)
	}
	for _, secret := range []string{alpha.DSN, "sqlite://"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("discovery leaked %q: %s", secret, encoded)
		}
	}
}

func TestDiscoveryFailureIsToolError(t *testing.T) {
	registry := newMemRegistry()
	registry.setListErr(fmt.Errorf("store offline"))
	h := newHarness(t, registry)
	payload := h.client(t, "/mcp", nil).mustFail(t, toolListInstances, map[string]any{})
	if !strings.Contains(toolErrorText(payload), "store offline") {
		t.Fatalf("error text missing cause: %v", payload)
	}
}

func TestEmptyRegistryDiscoveryReturnsEmptyArray(t *testing.T) {
	h := newHarness(t, newMemRegistry())
	payload := h.client(t, "/mcp", nil).mustCall(t, toolListInstances, map[string]any{})
	instances, ok := payload["instances"].([]any)
	if !ok || len(instances) != 0 {
		t.Fatalf("want empty array, got %v", payload["instances"])
	}
}

func TestUnifiedSchemasRequireInstance(t *testing.T) {
	h := newHarness(t, newMemRegistry())
	tools := h.client(t, "/mcp", nil).tools(t)
	for _, name := range []string{toolQuery, toolExecute, toolListObjects, toolDescribeObject} {
		definition, ok := tools[name]
		if !ok {
			t.Fatalf("missing unified tool %q", name)
		}
		schema, ok := definition["inputSchema"].(map[string]any)
		if !ok {
			t.Fatalf("%s inputSchema is not an object", name)
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s has no properties", name)
		}
		property, ok := properties["instance"].(map[string]any)
		if !ok {
			t.Fatalf("%s has no instance property: %v", name, properties)
		}
		if property["type"] != "string" {
			t.Fatalf("%s instance type = %v, want string", name, property["type"])
		}
		required := stringSet(schema["required"])
		if !required["instance"] {
			t.Fatalf("%s does not require instance: %v", name, schema["required"])
		}
		for _, original := range baselineRequired(name) {
			if !required[original] {
				t.Fatalf("%s lost baseline required argument %q", name, original)
			}
		}
	}
	if _, ok := tools[toolListInstances]; !ok {
		t.Fatal("unified endpoint must expose list_instances")
	}
}

func TestFixedSchemasMirrorBaseline(t *testing.T) {
	registry := newMemRegistry()
	registry.put("alpha", markerFixture(t, "alpha"))
	h := newHarness(t, registry)
	client := h.client(t, "/db/alpha/mcp", nil)
	tools := client.tools(t)
	if _, ok := tools[toolListInstances]; ok {
		t.Fatal("fixed endpoint must not expose list_instances")
	}
	if len(tools) != 4 {
		t.Fatalf("fixed endpoint tool count = %d, want 4", len(tools))
	}
	unified := h.client(t, "/mcp", nil).tools(t)
	for _, name := range []string{toolQuery, toolExecute, toolListObjects, toolDescribeObject} {
		fixedSchema := tools[name]["inputSchema"].(map[string]any)
		properties := fixedSchema["properties"].(map[string]any)
		if _, ok := properties["instance"]; ok {
			t.Fatalf("%s fixed schema exposes instance", name)
		}
		unifiedProperties := unified[name]["inputSchema"].(map[string]any)["properties"].(map[string]any)
		delete(unifiedProperties, "instance")
		if !jsonEqual(properties, unifiedProperties) {
			t.Fatalf("%s fixed properties %v do not mirror baseline %v", name, properties, unifiedProperties)
		}
		fixedRequired := stringSet(fixedSchema["required"])
		unifiedRequired := stringSet(unified[name]["inputSchema"].(map[string]any)["required"])
		delete(unifiedRequired, "instance")
		if !sameSet(fixedRequired, unifiedRequired) {
			t.Fatalf("%s fixed required %v != baseline %v", name, fixedRequired, unifiedRequired)
		}
		for _, want := range baselineRequired(name) {
			if !fixedRequired[want] {
				t.Fatalf("%s fixed schema lost required %q", name, want)
			}
		}
	}
}

func TestUnifiedRoutesEveryCallToNamedInstance(t *testing.T) {
	registry := newMemRegistry()
	registry.put("alpha", markerFixture(t, "alpha"))
	registry.put("beta", markerFixture(t, "beta"))
	h := newHarness(t, registry)
	client := h.client(t, "/mcp", nil)

	if got := markerValue(t, client, map[string]any{"instance": "alpha"}); got != "alpha" {
		t.Fatalf("alpha marker = %q", got)
	}
	if got := markerValue(t, client, map[string]any{"instance": "beta"}); got != "beta" {
		t.Fatalf("beta marker = %q", got)
	}
	payload := client.mustCall(t, toolQuery, map[string]any{"instance": "beta", "sql": "SELECT 1 AS one"})
	if payload["instance"] != "beta" {
		t.Fatalf("result must identify the actual instance: %v", payload)
	}
	if payload["dialect"] != "SQLite" {
		t.Fatalf("result lost dialect: %v", payload)
	}
	client.mustCall(t, toolExecute, map[string]any{"instance": "beta", "sql": "CREATE TABLE beta_only (id INTEGER)"})

	objects := client.mustCall(t, toolListObjects, map[string]any{"instance": "beta", "type": "table"})
	if !objectNamesContain(objects, "beta_only") {
		t.Fatalf("beta list_objects lost the new table: %v", objects)
	}
	if objects["instance"] != "beta" {
		t.Fatalf("list_objects result must identify instance: %v", objects)
	}
	alphaObjects := client.mustCall(t, toolListObjects, map[string]any{"instance": "alpha", "type": "table"})
	if objectNamesContain(alphaObjects, "beta_only") {
		t.Fatalf("beta write leaked into alpha: %v", alphaObjects)
	}
	described := client.mustCall(t, toolDescribeObject, map[string]any{"instance": "beta", "object_name": "beta_only"})
	if described["instance"] != "beta" {
		t.Fatalf("describe_object must identify instance: %v", described)
	}
	client.mustFail(t, toolDescribeObject, map[string]any{"instance": "alpha", "object_name": "beta_only"})
}

func TestUnifiedRejectsMissingAndUnknownInstances(t *testing.T) {
	registry := newMemRegistry()
	registry.put("alpha", markerFixture(t, "alpha"))
	h := newHarness(t, registry)
	client := h.client(t, "/mcp", nil)

	missing := client.mustFail(t, toolQuery, map[string]any{"sql": "SELECT 1"})
	if text := toolErrorText(missing); !strings.Contains(text, "instance") {
		t.Fatalf("missing-instance error should name the argument: %v", missing)
	}
	unknown := client.mustFail(t, toolQuery, map[string]any{"instance": "missing", "sql": "SELECT 1"})
	if text := toolErrorText(unknown); !strings.Contains(text, "missing") {
		t.Fatalf("unknown-instance error should name the instance: %v", unknown)
	}
	client.mustFail(t, toolExecute, map[string]any{"instance": "missing", "sql": "SELECT 1"})
	client.mustFail(t, toolListObjects, map[string]any{"instance": "missing"})
	client.mustFail(t, toolDescribeObject, map[string]any{"instance": "missing", "object_name": "x"})
	// A rejected call must not disturb the healthy instance.
	if got := markerValue(t, client, map[string]any{"instance": "alpha"}); got != "alpha" {
		t.Fatalf("alpha marker = %q", got)
	}
}

func TestFixedEndpointBindsPathAndRejectsInstance(t *testing.T) {
	registry := newMemRegistry()
	registry.put("alpha", markerFixture(t, "alpha"))
	registry.put("beta", markerFixture(t, "beta"))
	h := newHarness(t, registry)
	fixed := h.client(t, "/db/alpha/mcp", nil)

	if got := markerValue(t, fixed, nil); got != "alpha" {
		t.Fatalf("fixed marker = %q", got)
	}
	override := fixed.mustFail(t, toolQuery, map[string]any{"instance": "beta", "sql": "SELECT value FROM marker"})
	if !strings.Contains(toolErrorText(override), "instance") {
		t.Fatalf("override rejection should mention instance: %v", override)
	}
	fixed.mustFail(t, toolExecute, map[string]any{"instance": "beta", "sql": "INSERT INTO marker VALUES ('x')"})
	if got := markerValue(t, fixed, nil); got != "alpha" {
		t.Fatalf("path binding was overridden: %q", got)
	}
	// The rejected execute must not have reached beta either.
	unified := h.client(t, "/mcp", nil)
	if got := markerValue(t, unified, map[string]any{"instance": "beta"}); got != "beta" {
		t.Fatalf("rejected fixed call reached beta: %q", got)
	}
}

func TestFixedUnknownInstanceHasNoEndpoint(t *testing.T) {
	h := newHarness(t, newMemRegistry())
	client := &rpcClient{t: t, url: h.base + "/db/missing/mcp"}
	_, status := client.rpc(t, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "routing-unit", "version": "1"},
	})
	if status != http.StatusNotFound {
		t.Fatalf("unknown fixed endpoint status = %d, want 404", status)
	}
}

func TestFixedListInstancesIsUnknownTool(t *testing.T) {
	registry := newMemRegistry()
	registry.put("alpha", markerFixture(t, "alpha"))
	h := newHarness(t, registry)
	fixed := h.client(t, "/db/alpha/mcp", nil)
	response, status := fixed.rpc(t, "tools/call", map[string]any{"name": toolListInstances, "arguments": map[string]any{}})
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if response["error"] == nil {
		t.Fatalf("fixed list_instances must be an unknown-tool error: %v", response)
	}
}

func TestRegistryChangesVisibleAtOperationBoundaries(t *testing.T) {
	registry := newMemRegistry()
	registry.put("gamma", markerFixture(t, "gamma"))
	h := newHarness(t, registry)
	client := h.client(t, "/mcp", nil)
	fixed := h.client(t, "/db/gamma/mcp", nil)

	if got := markerValue(t, client, map[string]any{"instance": "gamma"}); got != "gamma" {
		t.Fatalf("gamma marker = %q", got)
	}
	if got := markerValue(t, fixed, nil); got != "gamma" {
		t.Fatalf("fixed gamma marker = %q", got)
	}

	registry.put("gamma", markerFixture(t, "gamma-updated"))
	if got := markerValue(t, client, map[string]any{"instance": "gamma"}); got != "gamma-updated" {
		t.Fatalf("DSN update not observed: %q", got)
	}
	if got := markerValue(t, fixed, nil); got != "gamma-updated" {
		t.Fatalf("fixed session did not observe DSN update: %q", got)
	}

	registry.remove("gamma")
	fixed.mustFail(t, toolQuery, map[string]any{"sql": "SELECT value FROM marker"})
	client.mustFail(t, toolQuery, map[string]any{"instance": "gamma", "sql": "SELECT 1"})

	h.router.mu.Lock()
	remaining := h.router.current["gamma"]
	h.router.mu.Unlock()
	if remaining != nil {
		t.Fatal("removed instance retained a pool")
	}

	registry.put("gamma", markerFixture(t, "gamma-again"))
	if got := markerValue(t, fixed, nil); got != "gamma-again" {
		t.Fatalf("re-added instance not observed: %q", got)
	}
}

func TestUnavailableInstanceIsIsolated(t *testing.T) {
	registry := newMemRegistry()
	registry.put("alpha", markerFixture(t, "alpha"))
	registry.put("broken", "sqlite://"+filepath.Join(t.TempDir(), "missing-dir", "x.sqlite"))
	h := newHarness(t, registry)
	client := h.client(t, "/mcp", nil)

	client.mustFail(t, toolQuery, map[string]any{"instance": "broken", "sql": "SELECT 1"})
	if got := markerValue(t, client, map[string]any{"instance": "alpha"}); got != "alpha" {
		t.Fatalf("healthy instance impacted by broken neighbour: %q", got)
	}
}

func TestAuthorizerRunsBeforeEveryCall(t *testing.T) {
	registry := newMemRegistry()
	registry.put("alpha", markerFixture(t, "alpha"))
	type authKey struct{}
	var mu sync.Mutex
	var seen []string
	var denyExecute bool
	authorize := func(ctx context.Context, tool string) error {
		if ctx.Value(authKey{}) != "credential" {
			return fmt.Errorf("request authorization context lost")
		}
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, tool)
		if tool == toolExecute && denyExecute {
			return fmt.Errorf("execution disabled")
		}
		return nil
	}
	router, err := New(registry, WithAuthorizer(authorize))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authKey{}, "credential")))
	}))
	t.Cleanup(func() {
		server.Close()
		router.Close()
	})
	base := server.URL

	unified := newRPCClient(t, base, "/mcp", nil)
	fixed := newRPCClient(t, base, "/db/alpha/mcp", nil)
	unified.mustCall(t, toolListInstances, map[string]any{})
	if got := markerValue(t, unified, map[string]any{"instance": "alpha"}); got != "alpha" {
		t.Fatalf("alpha marker = %q", got)
	}
	if got := markerValue(t, fixed, nil); got != "alpha" {
		t.Fatalf("fixed alpha marker = %q", got)
	}
	unified.mustCall(t, toolExecute, map[string]any{"instance": "alpha", "sql": "INSERT INTO marker VALUES ('unified-allowed')"})
	fixed.mustCall(t, toolExecute, map[string]any{"sql": "INSERT INTO marker VALUES ('fixed-allowed')"})

	mu.Lock()
	denyExecute = true
	mu.Unlock()
	unified.mustFail(t, toolExecute, map[string]any{"instance": "alpha", "sql": "INSERT INTO marker VALUES ('unified-denied')"})
	fixed.mustFail(t, toolExecute, map[string]any{"sql": "INSERT INTO marker VALUES ('fixed-denied')"})

	mu.Lock()
	executeCalls := 0
	discoveryCalls := 0
	for _, tool := range seen {
		switch tool {
		case toolExecute:
			executeCalls++
		case toolListInstances:
			discoveryCalls++
		}
	}
	mu.Unlock()
	if executeCalls != 4 {
		t.Fatalf("authorizer saw %d execute calls, want 4", executeCalls)
	}
	if discoveryCalls != 1 {
		t.Fatalf("authorizer saw %d list_instances calls, want 1", discoveryCalls)
	}

	payload := unified.mustCall(t, toolQuery, map[string]any{"instance": "alpha", "sql": "SELECT value FROM marker ORDER BY value"})
	rows, _ := payload["rows"].([]any)
	if len(rows) != 3 {
		t.Fatalf("denied writes left side effects: %v", rows)
	}
	allowed := map[string]bool{}
	for _, row := range rows {
		values, _ := row.([]any)
		if len(values) == 1 {
			allowed[fmt.Sprint(values[0])] = true
		}
	}
	if !allowed["alpha"] || !allowed["unified-allowed"] || !allowed["fixed-allowed"] {
		t.Fatalf("unexpected marker contents: %v", rows)
	}
}

func TestAuthorizerContextLossBlocksCalls(t *testing.T) {
	registry := newMemRegistry()
	registry.put("alpha", markerFixture(t, "alpha"))
	type authKey struct{}
	h := newHarness(t, registry, WithAuthorizer(func(ctx context.Context, _ string) error {
		if ctx.Value(authKey{}) == nil {
			return fmt.Errorf("missing credential")
		}
		return nil
	}))
	client := h.client(t, "/mcp", nil)
	client.mustFail(t, toolListInstances, map[string]any{})
	client.mustFail(t, toolQuery, map[string]any{"instance": "alpha", "sql": "SELECT 1"})
}

func TestSSEGatewayAndFixedInstance(t *testing.T) {
	registry := newMemRegistry()
	registry.put("alpha", markerFixture(t, "alpha"))
	registry.put("beta", markerFixture(t, "beta"))
	type authKey struct{}
	h := newHarness(t, registry, WithAuthorizer(func(ctx context.Context, _ string) error {
		if ctx.Value(authKey{}) != "credential" {
			return fmt.Errorf("missing SSE credentials")
		}
		return nil
	}))
	withCredential := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.router.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authKey{}, "credential")))
	})
	server := httptest.NewServer(withCredential)
	t.Cleanup(server.Close)

	root := dialSSE(t, server.URL, "/sse")
	payload, ok := root.call(t, toolQuery, map[string]any{"instance": "beta", "sql": "SELECT value FROM marker"})
	if !ok {
		t.Fatalf("SSE unified call failed: %v", payload)
	}
	if payload["instance"] != "beta" {
		t.Fatalf("SSE result must identify instance: %v", payload)
	}
	fixed := dialSSE(t, server.URL, "/db/alpha/sse")
	payload, ok = fixed.call(t, toolQuery, map[string]any{"sql": "SELECT value FROM marker"})
	if !ok {
		t.Fatalf("SSE fixed call failed: %v", payload)
	}
	if payload["instance"] != "alpha" {
		t.Fatalf("SSE fixed result must identify instance: %v", payload)
	}
}

func TestSSEAuthorizerSeesRequestContext(t *testing.T) {
	registry := newMemRegistry()
	registry.put("alpha", markerFixture(t, "alpha"))
	type authKey struct{}
	var denials int
	var mu sync.Mutex
	deny := false
	h := newHarness(t, registry, WithAuthorizer(func(ctx context.Context, _ string) error {
		if ctx.Value(authKey{}) != "sse-credential" {
			return fmt.Errorf("context lost")
		}
		mu.Lock()
		defer mu.Unlock()
		if deny {
			denials++
			return fmt.Errorf("revoked")
		}
		return nil
	}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.router.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authKey{}, "sse-credential")))
	}))
	t.Cleanup(server.Close)

	fixed := dialSSE(t, server.URL, "/db/alpha/sse")
	payload, ok := fixed.call(t, toolQuery, map[string]any{"sql": "SELECT value FROM marker"})
	if !ok || payload["instance"] != "alpha" {
		t.Fatalf("initial SSE call failed: %v", payload)
	}
	mu.Lock()
	deny = true
	mu.Unlock()
	if _, ok := fixed.call(t, toolExecute, map[string]any{"sql": "INSERT INTO marker VALUES ('denied')"}); ok {
		t.Fatal("authorizer denial on an existing SSE session was ignored")
	}
	mu.Lock()
	got := denials
	deny = false
	mu.Unlock()
	if got != 1 {
		t.Fatalf("authorizer ran %d times after denial, want 1", got)
	}
	payload, ok = fixed.call(t, toolQuery, map[string]any{"sql": "SELECT value FROM marker"})
	if !ok {
		t.Fatalf("query after execute denial failed: %v", payload)
	}
	if rows, _ := payload["rows"].([]any); len(rows) != 1 {
		t.Fatalf("denied write reached the database: %v", payload)
	}
}

func TestPoolReuseRetireAndForget(t *testing.T) {
	registry := newMemRegistry()
	registry.put("alpha", markerFixture(t, "alpha"))
	h := newHarness(t, registry)
	ctx := context.Background()

	cfg, err := registry.Resolve(ctx, "alpha")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	first, err := h.router.acquire(ctx, cfg)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	h.router.release(first)
	if first.refs != 0 {
		t.Fatalf("refs after release = %d", first.refs)
	}
	again, err := h.router.acquire(ctx, cfg)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if again != first {
		t.Fatal("unchanged configuration must reuse the live pool")
	}

	inFlight, err := h.router.acquire(ctx, cfg)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	updated := cfg
	updated.DSN = markerFixture(t, "alpha-updated")
	registry.putConfig(updated)
	fresh, err := h.router.acquire(ctx, updated)
	if err != nil {
		t.Fatalf("acquire updated: %v", err)
	}
	if fresh == first {
		t.Fatal("DSN change must build a new pool")
	}
	if current := h.router.current["alpha"]; current != fresh {
		t.Fatal("the new pool must become the current target for new calls")
	}
	h.router.release(again)
	if !containsEntry(h.router.retired, first) || first.closed {
		t.Fatal("old pool must stay open while a call is in flight")
	}
	h.router.release(inFlight)
	if containsEntry(h.router.retired, first) || !first.closed {
		t.Fatal("retired pool must close once its last reference is released")
	}
	h.router.release(fresh)
	if fresh.closed {
		t.Fatal("releasing the current pool must keep it open for reuse")
	}

	registry.remove("alpha")
	if err := h.router.forget("alpha"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if _, ok := h.router.current["alpha"]; ok {
		t.Fatal("forget must drop the cached pool")
	}
	if !fresh.closed {
		t.Fatal("forget must close an idle pool")
	}
	if err := h.router.forget("never-seen"); err != nil {
		t.Fatalf("forget of an unknown id: %v", err)
	}
	rebuilt, err := h.router.acquire(ctx, cfg)
	if err != nil {
		t.Fatalf("acquire after forget: %v", err)
	}
	h.router.release(rebuilt)
}

func TestAcquireCancellationAndClose(t *testing.T) {
	registry := newMemRegistry()
	registry.put("alpha", markerFixture(t, "alpha"))
	h := newHarness(t, registry)

	cfg, err := registry.Resolve(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	exists, err := h.router.acquire(context.Background(), cfg)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	h.router.release(exists)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.router.acquire(ctx, cfg); err == nil {
		t.Fatal("acquire with a cancelled context must fail")
	}

	if err := h.router.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := h.router.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := h.router.acquire(context.Background(), cfg); err != ErrClosed {
		t.Fatalf("acquire after Close = %v, want ErrClosed", err)
	}
	if err := h.router.forget("alpha"); !errors.Is(err, ErrClosed) {
		t.Fatalf("forget after Close = %v, want ErrClosed", err)
	}
	response, status := (&rpcClient{t: t, url: h.base + "/mcp"}).rpc(t, "initialize", map[string]any{})
	if status != http.StatusServiceUnavailable {
		t.Fatalf("closed router status = %d, want 503 (%v)", status, response)
	}
	if code := statusOf(h, "/db/alpha/mcp"); code != http.StatusServiceUnavailable {
		t.Fatalf("closed fixed endpoint status = %d, want 503", code)
	}
	if code := statusOf(h, "/healthz"); code != http.StatusNotFound {
		t.Fatalf("unknown path status = %d, want 404", code)
	}
}

func TestServeHTTPRejectsUnknownPaths(t *testing.T) {
	h := newHarness(t, newMemRegistry())
	for _, path := range []string{"/", "/healthz", "/db/", "/db/alpha", "/db/alpha/other", "/mcp/extra"} {
		if code := statusOf(h, path); code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, code)
		}
	}
}

func TestSpliceInstanceField(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "prepends to object preserving big integers",
			input: `{"columns":["v"],"rows":[[9007199254740993]],"row_count":1}`,
			want:  "{\n  \"instance\": \"beta\",\n  \"columns\": [\n    \"v\"\n  ],\n  \"rows\": [\n    [\n      9007199254740993\n    ]\n  ],\n  \"row_count\": 1\n}",
		},
		{
			name:  "replaces existing key in place",
			input: `{"z":1,"instance":"old","n":2}`,
			want:  "{\n  \"z\": 1,\n  \"instance\": \"beta\",\n  \"n\": 2\n}",
		},
		{
			name:  "empty object",
			input: `{}`,
			want:  "{\n  \"instance\": \"beta\"\n}",
		},
		{
			name:  "leaves arrays untouched",
			input: `[1,2]`,
			want:  `[1,2]`,
		},
		{
			name:  "leaves scalars untouched",
			input: `null`,
			want:  `null`,
		},
		{
			name:  "leaves plain text untouched",
			input: `Operation successful.`,
			want:  `Operation successful.`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := spliceInstanceField(tc.input, "beta"); got != tc.want {
				t.Fatalf("spliceInstanceField(%s) =\n%s\nwant\n%s", tc.input, got, tc.want)
			}
		})
	}
	var decoded struct {
		Columns []string            `json:"columns"`
		Rows    [][]json.RawMessage `json:"rows"`
		Count   int                 `json:"row_count"`
		Inst    string              `json:"instance"`
	}
	if err := json.Unmarshal([]byte(spliceInstanceField(`{"columns":["v"],"rows":[[9007199254740993]],"row_count":1}`, "beta")), &decoded); err != nil {
		t.Fatalf("spliced JSON is invalid: %v", err)
	}
	if decoded.Inst != "beta" || decoded.Count != 1 || len(decoded.Rows) != 1 || len(decoded.Rows[0]) != 1 {
		t.Fatalf("spliced payload damaged: %+v", decoded)
	}
	// RawMessage keeps the literal digits: decoding into any would round the
	// value to float64 and hide a precision loss introduced by the splice.
	if got := string(decoded.Rows[0][0]); got != "9007199254740993" {
		t.Fatalf("big integer mangled: %s", got)
	}
}

func TestResultTaggingKeepsStructuredContent(t *testing.T) {
	result := &mcp.CallToolResult{
		Content:           []mcp.Content{mcp.TextContent{Type: mcp.ContentTypeText, Text: `{"value":1}`}},
		StructuredContent: map[string]any{"value": 1.0},
	}
	tagged := tagResult(result, "alpha")
	structured, ok := tagged.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structuredContent lost: %T", tagged.StructuredContent)
	}
	if structured["instance"] != "alpha" || structured["value"] != 1.0 {
		t.Fatalf("structuredContent not tagged: %v", structured)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(tagged.Content[0].(mcp.TextContent).Text), &decoded); err != nil {
		t.Fatalf("text payload is not JSON: %v", err)
	}
	if decoded["instance"] != "alpha" || decoded["value"] != 1.0 {
		t.Fatalf("text payload does not mirror structuredContent: %v", decoded)
	}
}

func TestTagResultAddsInstanceWithoutMutating(t *testing.T) {
	result := &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{Type: mcp.ContentTypeText, Text: `{"value":1}`},
			mcp.TextContent{Type: mcp.ContentTypeText, Text: "Operation successful."},
		},
		StructuredContent: map[string]any{"value": 1},
	}
	tagged := tagResult(result, "alpha")
	if result.Content[0].(mcp.TextContent).Text != `{"value":1}` {
		t.Fatal("tagResult mutated the original result")
	}
	first := tagged.Content[0].(mcp.TextContent).Text
	if !strings.Contains(first, `"instance": "alpha"`) {
		t.Fatalf("text payload not tagged: %s", first)
	}
	if second := tagged.Content[1].(mcp.TextContent).Text; second != "Operation successful." {
		t.Fatalf("non-JSON text was rewritten: %q", second)
	}
	structured, ok := tagged.StructuredContent.(map[string]any)
	if !ok || structured["instance"] != "alpha" {
		t.Fatalf("structuredContent not tagged: %v", tagged.StructuredContent)
	}
	if _, ok := result.StructuredContent.(map[string]any)["instance"]; ok {
		t.Fatal("tagResult mutated the original structuredContent")
	}
	errResult := &mcp.CallToolResult{IsError: true, Content: []mcp.Content{mcp.TextContent{Type: mcp.ContentTypeText, Text: `{"error":1}`}}}
	if got := tagResult(errResult, "alpha"); got != errResult {
		t.Fatal("error results must be returned unchanged")
	}
}

func TestForwardingPreservesBaselineErrors(t *testing.T) {
	registry := newMemRegistry()
	registry.put("alpha", markerFixture(t, "alpha"))
	h := newHarness(t, registry)
	client := h.client(t, "/mcp", nil)

	payload := client.mustFail(t, toolQuery, map[string]any{"instance": "alpha", "sql": "INSERT INTO marker VALUES ('x')"})
	if text := toolErrorText(payload); !strings.Contains(text, "read-oriented") {
		t.Fatalf("baseline validation error not preserved: %q", text)
	}
	if got := markerValue(t, client, map[string]any{"instance": "alpha"}); got != "alpha" {
		t.Fatalf("write leaked through query: %q", got)
	}
	payload = client.mustFail(t, toolQuery, map[string]any{"instance": "alpha", "sql": "SELECT * FROM no_such_table"})
	if text := toolErrorText(payload); !strings.Contains(text, "query execution failed") {
		t.Fatalf("baseline execution error not preserved: %q", text)
	}
	badArgs := client.mustFail(t, toolQuery, map[string]any{"instance": "alpha"})
	if text := toolErrorText(badArgs); !strings.Contains(text, "sql") {
		t.Fatalf("missing sql error not preserved: %q", text)
	}
}

func TestExecuteSuccessTextStillReturned(t *testing.T) {
	registry := newMemRegistry()
	registry.put("alpha", markerFixture(t, "alpha"))
	h := newHarness(t, registry)
	payload := h.client(t, "/mcp", nil).mustCall(t, toolExecute, map[string]any{"instance": "alpha", "sql": "UPDATE marker SET value = value"})
	message, _ := payload["message"].(string)
	if !strings.HasPrefix(message, "Operation successful") {
		t.Fatalf("execute text = %q", message)
	}
}

func TestConcurrentCallsKeepIndependentTargets(t *testing.T) {
	registry := newMemRegistry()
	registry.put("alpha", markerFixture(t, "alpha"))
	registry.put("beta", markerFixture(t, "beta"))
	h := newHarness(t, registry)
	client := h.client(t, "/mcp", nil)

	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for i := 0; i < 16; i++ {
		for _, id := range []string{"alpha", "beta"} {
			id := id
			wg.Add(1)
			go func() {
				defer wg.Done()
				payload, ok, err := client.tryCall(toolQuery, map[string]any{"instance": id, "sql": "SELECT value FROM marker"})
				if err != nil {
					errs <- fmt.Sprintf("%s: %v", id, err)
					return
				}
				if !ok {
					errs <- fmt.Sprintf("%s failed: %v", id, payload)
					return
				}
				if payload["instance"] != id {
					errs <- fmt.Sprintf("%s reported %v", id, payload["instance"])
					return
				}
				rows, _ := payload["rows"].([]any)
				if len(rows) != 1 {
					errs <- fmt.Sprintf("%s returned %v", id, rows)
					return
				}
				row, _ := rows[0].([]any)
				if len(row) != 1 || fmt.Sprint(row[0]) != id {
					errs <- fmt.Sprintf("%s saw wrong database: %v", id, rows)
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for message := range errs {
		t.Error(message)
	}
}

func TestFixedEndpointWorksWithoutConnecting(t *testing.T) {
	registry := newMemRegistry()
	registry.put("broken", "postgres://user:secret@127.0.0.1:1/none?connect_timeout=1")
	h := newHarness(t, registry)
	client := h.client(t, "/db/broken/mcp", nil)
	payload := client.mustFail(t, toolQuery, map[string]any{"sql": "SELECT 1"})
	if text := toolErrorText(payload); text == "" {
		t.Fatal("expected a connection error message")
	}
	if strings.Contains(toolErrorText(payload), "secret") {
		t.Fatalf("error leaked credentials: %s", toolErrorText(payload))
	}
}

func TestFixedEndpointCustomHeadersDoNotAffectRouting(t *testing.T) {
	registry := newMemRegistry()
	registry.put("alpha", markerFixture(t, "alpha"))
	h := newHarness(t, registry)
	headers := http.Header{"X-Request-Id": []string{"unit-test"}}
	client := h.client(t, "/db/alpha/mcp", headers)
	if got := markerValue(t, client, nil); got != "alpha" {
		t.Fatalf("marker = %q", got)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func baselineRequired(tool string) []string {
	switch tool {
	case toolQuery, toolExecute:
		return []string{"sql"}
	case toolDescribeObject:
		return []string{"object_name"}
	default:
		return nil
	}
}

func stringSet(value any) map[string]bool {
	out := map[string]bool{}
	items, _ := value.([]any)
	for _, item := range items {
		out[fmt.Sprint(item)] = true
	}
	return out
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for key := range a {
		if !b[key] {
			return false
		}
	}
	return true
}

func jsonEqual(a, b any) bool {
	left, errLeft := json.Marshal(a)
	right, errRight := json.Marshal(b)
	return errLeft == nil && errRight == nil && bytes.Equal(left, right)
}

func objectNamesContain(payload map[string]any, want string) bool {
	objects, _ := payload["objects"].([]any)
	for _, item := range objects {
		object, _ := item.(map[string]any)
		if object["name"] == want {
			return true
		}
	}
	return false
}

func containsEntry(entries []*instanceEntry, want *instanceEntry) bool {
	for _, entry := range entries {
		if entry == want {
			return true
		}
	}
	return false
}

func errorsIs(err, target error) bool {
	return err == target || errors.Is(err, target)
}

func statusOf(h *harness, path string) int {
	request, err := http.NewRequest(http.MethodGet, h.base+path, nil)
	if err != nil {
		return -1
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return -1
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

func TestMetadataRejectsSQLTools(t *testing.T) {
	r, err := New(newMemRegistry(), WithAuthorizer(func(context.Context, string) error { return fmt.Errorf("denied") }))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, name := range []string{"query", "execute", "list_instances"} {
		if _, err := r.CallMetadata(context.Background(), "alpha", name, nil); err == nil {
			t.Fatalf("allowed %s", name)
		}
	}
}
