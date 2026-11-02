package instances

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// request sends a request with extra headers through the manager handler.
func request(t *testing.T, m *Manager, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	return rec
}

func TestHandlerRoutingErrors(t *testing.T) {
	m, _ := openTestManager(t)

	t.Run("unknown path", func(t *testing.T) {
		rec := request(t, m, http.MethodGet, "/api/unknown", "", nil)
		assertJSONError(t, rec, http.StatusNotFound)
	})
	t.Run("wrong method on collection", func(t *testing.T) {
		rec := request(t, m, http.MethodDelete, "/api/instances", "", nil)
		assertJSONError(t, rec, http.StatusMethodNotAllowed)
	})
	t.Run("wrong method on item", func(t *testing.T) {
		rec := request(t, m, http.MethodPost, "/api/instances/alpha", "", nil)
		assertJSONError(t, rec, http.StatusMethodNotAllowed)
	})
	t.Run("wrong method on settings", func(t *testing.T) {
		rec := request(t, m, http.MethodDelete, "/api/settings", "", nil)
		assertJSONError(t, rec, http.StatusMethodNotAllowed)
	})
	t.Run("wrong content type", func(t *testing.T) {
		rec := request(t, m, http.MethodPost, "/api/instances", `{"id":"a","dsn":"sqlite:///tmp/x.db"}`, map[string]string{"Content-Type": "text/plain"})
		assertJSONError(t, rec, http.StatusUnsupportedMediaType)
	})
	t.Run("no cache headers", func(t *testing.T) {
		rec := request(t, m, http.MethodGet, "/api/instances", "", nil)
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control = %q", got)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("X-Content-Type-Options = %q", got)
		}
	})
}

func assertJSONError(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, want, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not JSON: %s", rec.Body.String())
	}
	if msg, ok := payload["error"].(string); !ok || msg == "" {
		t.Fatalf("response missing error message: %s", rec.Body.String())
	}
}

func TestOriginGuardOnWrites(t *testing.T) {
	m, _ := openTestManager(t)
	dsn := sqliteFixture(t)
	body := fmt.Sprintf(`{"id":"alpha","dsn":%q}`, dsn)

	rec := request(t, m, http.MethodPost, "/api/instances", body, map[string]string{"Origin": "https://evil.example"})
	assertJSONError(t, rec, http.StatusForbidden)
	if items, _ := m.List(context.Background()); len(items) != 0 {
		t.Fatal("cross-origin write was applied")
	}

	rec = request(t, m, http.MethodPost, "/api/instances", body, map[string]string{"Origin": "http://example.com"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("same-origin create status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = request(t, m, http.MethodPatch, "/api/instances/alpha", `{"name":"x"}`, map[string]string{"Origin": "https://evil.example"})
	assertJSONError(t, rec, http.StatusForbidden)

	rec = request(t, m, http.MethodPut, "/api/settings/default", `{"instance":"alpha"}`, map[string]string{"Origin": "https://evil.example"})
	assertJSONError(t, rec, http.StatusForbidden)

	rec = request(t, m, http.MethodDelete, "/api/instances/alpha", "", map[string]string{"Origin": "null"})
	assertJSONError(t, rec, http.StatusForbidden)

	// Reads are not origin-restricted.
	rec = request(t, m, http.MethodGet, "/api/instances", "", map[string]string{"Origin": "https://other.example"})
	if rec.Code != http.StatusOK {
		t.Fatalf("cross-origin read status=%d", rec.Code)
	}
}

func TestHTTPResponsesNeverContainSecrets(t *testing.T) {
	m, _ := openTestManager(t)
	password := "sup3r-s3cret-pw"
	dsn := "postgres://admin:" + password + "@db.example:5432/app?sslmode=disable"

	rec := request(t, m, http.MethodPost, "/api/instances", fmt.Sprintf(`{"id":"alpha","name":"Alpha","dsn":%q}`, dsn), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	checkNoSecret(t, "create", rec.Body.String(), password)

	rec = request(t, m, http.MethodGet, "/api/instances", "", nil)
	checkNoSecret(t, "list", rec.Body.String(), password)
	checkNoSecret(t, "list", rec.Body.String(), "postgres://")

	rec = request(t, m, http.MethodPatch, "/api/instances/alpha", `{"memo":"changed"}`, nil)
	checkNoSecret(t, "patch", rec.Body.String(), password)

	// A failed test on an unreachable host must not echo credentials either.
	rec = request(t, m, http.MethodPost, "/api/instances/alpha/test", `{}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("test status=%d body=%s", rec.Code, rec.Body.String())
	}
	checkNoSecret(t, "test", rec.Body.String(), password)
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if ok, _ := payload["ok"].(bool); ok {
		t.Fatalf("unreachable instance tested ok: %s", rec.Body.String())
	}
}

func checkNoSecret(t *testing.T, label, body, secret string) {
	t.Helper()
	if strings.Contains(body, secret) {
		t.Fatalf("%s response leaked secret: %s", label, body)
	}
}

func TestConnectionTestSQLite(t *testing.T) {
	m, _ := openTestManager(t)
	dsn := sqliteFixture(t)
	mustCreate(t, m, "alpha", dsn)

	rec := request(t, m, http.MethodPost, "/api/instances/alpha/test", `{}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ok"] != true {
		t.Fatalf("sqlite test failed: %s", rec.Body.String())
	}
	checkNoSecret(t, "test", rec.Body.String(), dsn)

	// Status is recorded and visible in the list view without leaking the DSN.
	listRec := request(t, m, http.MethodGet, "/api/instances", "", nil)
	checkNoSecret(t, "list", listRec.Body.String(), dsn)
	var view struct {
		Instances []map[string]any `json:"instances"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Instances) != 1 || view.Instances[0]["status"] != statusOK {
		t.Fatalf("instance status not recorded: %s", listRec.Body.String())
	}
}

func TestConnectionTestMissingSQLiteFile(t *testing.T) {
	m, _ := openTestManager(t)
	missing := filepath.Join(t.TempDir(), "absent.sqlite")
	mustCreate(t, m, "missing", "sqlite://"+missing)

	rec := request(t, m, http.MethodPost, "/api/instances/missing/test", `{}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ok"] != false {
		t.Fatalf("missing sqlite database tested ok: %s", rec.Body.String())
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("connection test created the database file: %v", err)
	}
}

func TestConnectionTestUnknownInstance(t *testing.T) {
	m, _ := openTestManager(t)
	rec := request(t, m, http.MethodPost, "/api/instances/nope/test", `{}`, nil)
	assertJSONError(t, rec, http.StatusNotFound)
}

// silentServer accepts TCP connections and never responds, simulating a hung server.
func silentServer(t *testing.T) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	return ln.Addr().String(), func() {
		_ = ln.Close()
		wg.Wait()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
	}
}

func TestConnectionTestIsBounded(t *testing.T) {
	m, _ := openTestManager(t)
	m.testTimeout = 300 * time.Millisecond

	addr, stop := silentServer(t)
	defer stop()
	password := "hung-server-pw"
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	dsn := fmt.Sprintf("postgres://user:%s@%s/db?sslmode=disable&connect_timeout=30", password, net.JoinHostPort(host, port))
	mustCreate(t, m, "hung", dsn)

	start := time.Now()
	rec := request(t, m, http.MethodPost, "/api/instances/hung/test", `{}`, nil)
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("connection test not bounded: %s", elapsed)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	checkNoSecret(t, "bounded test", rec.Body.String(), password)
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ok"] != false {
		t.Fatalf("hung server tested ok: %s", rec.Body.String())
	}
	if msg, _ := payload["error"].(string); msg == "" {
		t.Fatalf("missing failure reason: %s", rec.Body.String())
	}
}

func TestConnectionTestRefused(t *testing.T) {
	m, _ := openTestManager(t)
	m.testTimeout = 2 * time.Second
	// Reserve a port, then close it so the connection is refused.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	password := "refused-pw"
	mustCreate(t, m, "refused", fmt.Sprintf("postgres://user:%s@%s/db?sslmode=disable", password, addr))
	rec := request(t, m, http.MethodPost, "/api/instances/refused/test", `{}`, nil)
	checkNoSecret(t, "refused test", rec.Body.String(), password)
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ok"] != false {
		t.Fatalf("refused connection tested ok: %s", rec.Body.String())
	}
}

func TestSanitizeMessage(t *testing.T) {
	dsn := "mysql://root:hunter2@db.internal:3306/app?password=hunter2&timeout=5s"
	cases := []struct {
		name   string
		input  string
		secret []string
		want   []string
	}{
		{
			name:   "url credentials",
			input:  "dial " + dsn + ": connection refused",
			secret: []string{"hunter2"},
			want:   []string{"***", "db.internal"},
		},
		{
			name:   "dsn echo",
			input:  "failed to parse mysql://root:hunter2@db:3306/app",
			secret: []string{"hunter2"},
		},
		{
			name:   "key value",
			input:  "auth failed: password=hunter2 user=root",
			secret: []string{"hunter2"},
			want:   []string{"password=***"},
		},
		{
			name:   "generic",
			input:  "connection refused",
			secret: nil,
			want:   []string{"connection refused"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeMessage(tc.input, dsn)
			for _, s := range tc.secret {
				if strings.Contains(got, s) {
					t.Fatalf("sanitized message still contains %q: %s", s, got)
				}
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Fatalf("sanitized message lost %q: %s", w, got)
				}
			}
		})
	}

	long := strings.Repeat("x", 5000)
	if got := sanitizeMessage(long, ""); len(got) > 400 {
		t.Fatalf("sanitized message too long: %d bytes", len(got))
	}
	if got := sanitizeMessage("   ", ""); got == "" {
		t.Fatal("empty message should fall back to a generic description")
	}
}

func TestStorePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")
	m, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Fatalf("data dir mode = %o, want 700", mode)
	}
	info, err = os.Stat(filepath.Join(dir, storeFileName))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("store file mode = %o, want 600", mode)
	}
}

func TestHandlerAfterClose(t *testing.T) {
	dir := t.TempDir()
	m, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	rec := request(t, m, http.MethodGet, "/api/instances", "", nil)
	assertJSONError(t, rec, http.StatusServiceUnavailable)
}

func TestIDNormalization(t *testing.T) {
	m, _ := openTestManager(t)
	dsn := sqliteFixture(t)

	rec := request(t, m, http.MethodPost, "/api/instances", fmt.Sprintf(`{"id":"  Alpha-1  ","dsn":%q}`, dsn), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := m.Resolve(context.Background(), "alpha-1"); err != nil {
		t.Fatalf("id not normalized: %v", err)
	}
	rec = request(t, m, http.MethodGet, "/api/instances/alpha-1", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET item status=%d body=%s", rec.Code, rec.Body.String())
	}
}
