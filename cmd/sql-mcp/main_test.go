package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// runResult captures the three streams of one in-process lifecycle.
type runResult struct {
	code   int
	stdout string
	stderr string
}

// lockedBuffer is safe to read while a background goroutine still writes to
// it, which the lifecycle tests rely on while polling stderr.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *lockedBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

// runOnce executes run with a finite stdin and returns its captured output.
func runOnce(t *testing.T, args []string, env map[string]string, stdin io.Reader) runResult {
	t.Helper()
	var stdout, stderr lockedBuffer
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	code := run(ctx, args, env, stdin, &syncWriter{w: &stdout}, &syncWriter{w: &stderr})
	return runResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// stdioRequests builds newline-delimited JSON-RPC: initialize, the required
// initialized notification, tools/list and one query against the marker.
func stdioRequests(t *testing.T, query string) io.Reader {
	t.Helper()
	messages := []map[string]any{
		{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "cmd-test", "version": "1"},
		}},
		{"jsonrpc": "2.0", "method": "notifications/initialized"},
		{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}},
		{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{
			"name": "query", "arguments": map[string]any{"sql": query},
		}},
	}
	var input bytes.Buffer
	for _, message := range messages {
		encoded, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		input.Write(encoded)
		input.WriteByte('\n')
	}
	return &input
}

// parseStdioResponses decodes every stdout line and fails if any line is not
// JSON-RPC 2.0, which enforces that logs never share stdout in stdio mode.
func parseStdioResponses(t *testing.T, stdout string) map[float64]map[string]any {
	t.Helper()
	responses := map[float64]map[string]any{}
	scanner := bufio.NewScanner(strings.NewReader(stdout))
	scanner.Buffer(make([]byte, 4096), 8<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var message map[string]any
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("stdout line is not JSON-RPC: %q: %v", line, err)
		}
		if message["jsonrpc"] != "2.0" {
			t.Fatalf("stdout line is not JSON-RPC 2.0: %q", line)
		}
		if id, ok := message["id"].(float64); ok {
			responses[id] = message
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return responses
}

func TestRunHelpExitsZeroWithUsage(t *testing.T) {
	result := runOnce(t, []string{"help"}, nil, nil)
	if result.code != exitOK {
		t.Fatalf("help exit code=%d, want %d (stderr=%s)", result.code, exitOK, result.stderr)
	}
	if !strings.Contains(result.stdout, "sql-mcp serve") || !strings.Contains(result.stdout, "--data-dir") {
		t.Fatalf("usage missing from stdout: %q", result.stdout)
	}
}

func TestRunUsageErrorsExitTwo(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{name: "unknown command", args: []string{"bogus"}},
		{name: "stdio without dsn", args: []string{"stdio"}},
		{name: "unknown default", args: []string{"serve"}, env: map[string]string{testDefaultDatabase: "missing"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := runOnce(t, tc.args, tc.env, nil)
			if result.code != exitUsage {
				t.Fatalf("exit code=%d, want %d (stderr=%s)", result.code, exitUsage, result.stderr)
			}
			if !strings.Contains(result.stderr, "Configuration error") {
				t.Fatalf("expected a configuration error on stderr, got %q", result.stderr)
			}
			if result.stdout != "" {
				t.Fatalf("diagnostics must not reach stdout in error paths: %q", result.stdout)
			}
		})
	}
}

func TestRunDataDirFileIsUsageError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := runOnce(t, []string{"serve", "--dsn", sqliteDSN(t, "x"), "--data-dir", file}, nil, nil)
	if result.code != exitUsage {
		t.Fatalf("exit code=%d, want %d (stderr=%s)", result.code, exitUsage, result.stderr)
	}
	if !strings.Contains(result.stderr, "data directory") && !strings.Contains(result.stderr, "not a directory") {
		t.Fatalf("expected a data dir error, got %q", result.stderr)
	}
}

func TestRunStdioWithStaticDSN(t *testing.T) {
	result := runOnce(t, []string{"stdio", "--dsn", sqliteDSN(t, "stdio-marker")}, nil, stdioRequests(t, "SELECT value FROM marker"))
	if result.code != exitOK {
		t.Fatalf("exit code=%d, want %d (stderr=%s)", result.code, exitOK, result.stderr)
	}

	responses := parseStdioResponses(t, result.stdout)
	if responses[1]["result"] == nil {
		t.Fatalf("initialize response missing: %v", responses[1])
	}
	if responses[2]["result"] == nil {
		t.Fatalf("tools/list response missing: %v", responses[2])
	}
	call, _ := responses[3]["result"].(map[string]any)
	content, _ := call["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("tools/call response missing content: %v", responses[3])
	}
	item, _ := content[0].(map[string]any)
	if !strings.Contains(strings.Join(strings.Fields(strings.TrimSpace(stringOf(item["text"]))), " "), "stdio-marker") {
		t.Fatalf("query did not return the fixture marker: %v", item["text"])
	}
	if !strings.Contains(result.stderr, "stdio") {
		t.Fatalf("expected a stdio notice on stderr, got %q", result.stderr)
	}
}

func stringOf(value any) string {
	text, _ := value.(string)
	return text
}

func TestRunStdioIgnoresListener(t *testing.T) {
	// A LISTEN value that cannot be bound proves stdio never opens a port.
	result := runOnce(t, []string{"stdio", "--dsn", sqliteDSN(t, "ignore-listen")}, map[string]string{
		"LISTEN": "127.0.0.1:not-a-port",
	}, stdioRequests(t, "SELECT 1"))
	if result.code != exitOK {
		t.Fatalf("exit code=%d, want %d (stderr=%s)", result.code, exitOK, result.stderr)
	}
	if strings.Contains(result.stderr, "http://") || strings.Contains(result.stderr, "cannot listen") {
		t.Fatalf("stdio must not start an HTTP listener, got %q", result.stderr)
	}
}

func TestRunStdioStopsOnContextCancel(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	t.Cleanup(func() { stdinWriter.Close() })

	var stdout, stderr lockedBuffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dsn := sqliteDSN(t, "cancel-marker")
	exit := make(chan int, 1)
	go func() {
		exit <- run(ctx, []string{"stdio", "--dsn", dsn}, nil,
			stdinReader, &syncWriter{w: &stdout}, &syncWriter{w: &stderr})
	}()

	waitForText(t, &stderr, "Serving database")
	cancel()
	select {
	case code := <-exit:
		if code != exitOK {
			t.Fatalf("exit code=%d, want %d (stderr=%s)", code, exitOK, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stdio did not stop after the context was cancelled")
	}
	if stdout.Len() != 0 {
		t.Fatalf("cancellation must not write to stdout: %q", stdout.String())
	}
}

func TestRunSuccessfulConfigFailureExitsOne(t *testing.T) {
	// The configuration parses, but opening the database fails at startup.
	result := runOnce(t, []string{"stdio", "--dsn", "bogus://unreachable"}, nil, nil)
	if result.code != exitRuntime {
		t.Fatalf("exit code=%d, want %d (stderr=%s)", result.code, exitRuntime, result.stderr)
	}
	if !strings.Contains(result.stderr, "Startup error") {
		t.Fatalf("expected a startup error, got %q", result.stderr)
	}
}

func TestRunServeServesAndShutsDown(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	dsn := sqliteDSN(t, "serve-marker")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr lockedBuffer
	exit := make(chan int, 1)
	go func() {
		exit <- run(ctx, []string{"serve", "--listen", "127.0.0.1:0", "--dsn", dsn, "--data-dir", dataDir}, nil,
			nil, &syncWriter{w: &stdout}, &syncWriter{w: &stderr})
	}()

	base := waitForServeAddress(t, &stderr)
	waitForHealth(t, base)

	// The static instance answers MCP over the legacy default route.
	resp, err := http.Post(base+"/mcp", "application/json", initializeBody(t))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/mcp status=%d", resp.StatusCode)
	}

	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("data dir was not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("data dir permissions are %o, want owner-only", perm)
	}
	if !strings.Contains(stderr.String(), "persistent instance storage is not part of this release") {
		t.Fatalf("expected the storage notice, got %q", stderr.String())
	}

	cancel()
	select {
	case code := <-exit:
		if code != exitOK {
			t.Fatalf("exit code=%d, want %d (stderr=%s)", code, exitOK, stderr.String())
		}
	case <-time.After(shutdownTimeout + 5*time.Second):
		t.Fatal("serve did not shut down")
	}
	if stdout.Len() != 0 {
		t.Fatalf("serve must not write to stdout: %q", stdout.String())
	}
}

func TestRunServeListenConflictExitsOne(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()

	result := runOnce(t, []string{"serve", "--listen", blocker.Addr().String(), "--dsn", sqliteDSN(t, "conflict")}, nil, nil)
	if result.code != exitRuntime {
		t.Fatalf("exit code=%d, want %d (stderr=%s)", result.code, exitRuntime, result.stderr)
	}
	if !strings.Contains(result.stderr, "cannot listen") {
		t.Fatalf("expected a listen error, got %q", result.stderr)
	}
}

func TestRunLegacyHTTPServesWithNotice(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr lockedBuffer
	dsn := sqliteDSN(t, "legacy-marker")
	exit := make(chan int, 1)
	go func() {
		exit <- run(ctx, []string{"--listen", "127.0.0.1:0", "--dsn", dsn}, nil,
			nil, &syncWriter{w: &stdout}, &syncWriter{w: &stderr})
	}()

	base := waitForServeAddress(t, &stderr)
	waitForHealth(t, base)
	if !strings.Contains(stderr.String(), "Notice:") {
		t.Fatalf("legacy startup should print a migration notice, got %q", stderr.String())
	}

	cancel()
	select {
	case code := <-exit:
		if code != exitOK {
			t.Fatalf("exit code=%d, want %d", code, exitOK)
		}
	case <-time.After(shutdownTimeout + 5*time.Second):
		t.Fatal("legacy serve did not shut down")
	}
}

// waitForServeAddress reads stderr until the startup line reports the bound
// address and returns its base URL.
func waitForServeAddress(t *testing.T, stderr *lockedBuffer) string {
	t.Helper()
	waitForText(t, stderr, "on http://")
	text := stderr.String()
	marker := "on http://"
	start := strings.Index(text, marker)
	if start < 0 {
		t.Fatalf("startup line missing: %q", text)
	}
	rest := text[start+len(marker):]
	end := strings.IndexAny(rest, " \n")
	if end < 0 {
		t.Fatalf("startup line has no address terminator: %q", text)
	}
	return "http://" + rest[:end]
}

func waitForText(t *testing.T, buffer *lockedBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buffer.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; stderr=%q", want, buffer.String())
}
