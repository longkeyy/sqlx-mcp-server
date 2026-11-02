package tokens

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func apiRequest(t *testing.T, srv *httptest.Server, method, path string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	decoded := map[string]any{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("%s %s: invalid JSON %q: %v", method, path, data, err)
		}
	}
	return resp, decoded
}

func TestHandlerLifecycle(t *testing.T) {
	manager, _ := openTestManager(t, "")
	srv := httptest.NewServer(manager.Handler())
	defer srv.Close()

	resp, body := apiRequest(t, srv, http.MethodGet, "/api/tokens", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("empty list status = %d, want 200", resp.StatusCode)
	}
	tokens, ok := body["tokens"].([]any)
	if !ok || len(tokens) != 0 {
		t.Fatalf("empty list must return an empty array, got %v", body)
	}
	if got := resp.Header.Get("Content-Type"); got != jsonContentType {
		t.Fatalf("Content-Type = %q, want %q", got, jsonContentType)
	}

	resp, created := apiRequest(t, srv, http.MethodPost, "/api/tokens",
		map[string]any{"name": "  reader  ", "allow_execute": false})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %v", resp.StatusCode, created)
	}
	secret, _ := created["token"].(string)
	if secret == "" {
		t.Fatalf("create response must include one-time plaintext token: %v", created)
	}
	if created["name"] != "reader" {
		t.Fatalf("name must be trimmed, got %v", created["name"])
	}
	if created["allow_execute"] != false {
		t.Fatalf("allow_execute must default to false, got %v", created["allow_execute"])
	}
	if created["created_at"] == "" || created["id"] == "" {
		t.Fatalf("create response missing metadata: %v", created)
	}
	if err := manager.Authorize(secret, "query"); err != nil {
		t.Fatalf("created token rejected: %v", err)
	}

	resp, listed := apiRequest(t, srv, http.MethodGet, "/api/tokens", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want 200", resp.StatusCode)
	}
	raw, _ := json.Marshal(listed)
	if strings.Contains(string(raw), secret) {
		t.Fatalf("listing leaked plaintext token: %s", raw)
	}
	if strings.Contains(string(raw), digest(secret)) {
		t.Fatalf("listing leaked token digest: %s", raw)
	}
	items, _ := listed["tokens"].([]any)
	if len(items) != 1 {
		t.Fatalf("listing = %v, want one token", listed)
	}
	first, _ := items[0].(map[string]any)
	if first["id"] != created["id"] || first["name"] != "reader" {
		t.Fatalf("listed token mismatch: %v", first)
	}

	id, _ := created["id"].(string)
	resp, _ = apiRequest(t, srv, http.MethodDelete, "/api/tokens/"+id, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", resp.StatusCode)
	}
	if err := manager.Authorize(secret, "query"); err == nil {
		t.Fatal("revoked token still authorized")
	}
	resp, _ = apiRequest(t, srv, http.MethodDelete, "/api/tokens/"+id, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", resp.StatusCode)
	}
}

func TestHandlerReturnsNoStoreHeaders(t *testing.T) {
	manager, _ := openTestManager(t, "")
	srv := httptest.NewServer(manager.Handler())
	defer srv.Close()

	resp, _ := apiRequest(t, srv, http.MethodPost, "/api/tokens", map[string]any{"name": "reader"})
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestHandlerAllowsExecuteAndDuplicateNames(t *testing.T) {
	manager, _ := openTestManager(t, "")
	srv := httptest.NewServer(manager.Handler())
	defer srv.Close()

	_, first := apiRequest(t, srv, http.MethodPost, "/api/tokens", map[string]any{"name": "same", "allow_execute": true})
	_, second := apiRequest(t, srv, http.MethodPost, "/api/tokens", map[string]any{"name": "same", "allow_execute": true})
	if first["id"] == second["id"] || first["token"] == second["token"] {
		t.Fatal("each creation must mint a distinct id and secret")
	}
	if first["allow_execute"] != true {
		t.Fatalf("allow_execute not persisted: %v", first)
	}
	secret, _ := second["token"].(string)
	if err := manager.Authorize(secret, "execute"); err != nil {
		t.Fatalf("execute-enabled token denied: %v", err)
	}
}

func TestHandlerValidation(t *testing.T) {
	manager, _ := openTestManager(t, "")
	srv := httptest.NewServer(manager.Handler())
	defer srv.Close()

	cases := []struct {
		name   string
		method string
		path   string
		ctype  string
		body   string
		want   int
		allow  string
	}{
		{name: "empty body", method: http.MethodPost, path: "/api/tokens", body: ``, want: http.StatusBadRequest},
		{name: "missing name", method: http.MethodPost, path: "/api/tokens", body: `{"allow_execute":false}`, want: http.StatusBadRequest},
		{name: "blank name", method: http.MethodPost, path: "/api/tokens", body: `{"name":"   "}`, want: http.StatusBadRequest},
		{name: "long name", method: http.MethodPost, path: "/api/tokens", body: `{"name":"` + strings.Repeat("x", 129) + `"}`, want: http.StatusBadRequest},
		{name: "control chars", method: http.MethodPost, path: "/api/tokens", body: "{\"name\":\"a\\u0000b\"}", want: http.StatusBadRequest},
		{name: "string allow_execute", method: http.MethodPost, path: "/api/tokens", body: `{"name":"a","allow_execute":"yes"}`, want: http.StatusBadRequest},
		{name: "invalid JSON", method: http.MethodPost, path: "/api/tokens", body: `{"name":`, want: http.StatusBadRequest},
		{name: "trailing data", method: http.MethodPost, path: "/api/tokens", body: `{"name":"a"}{"name":"b"}`, want: http.StatusBadRequest},
		{name: "wrong content type", method: http.MethodPost, path: "/api/tokens", ctype: "text/plain", body: `{"name":"a"}`, want: http.StatusUnsupportedMediaType},
		{name: "collection PUT", method: http.MethodPut, path: "/api/tokens", body: `{}`, want: http.StatusMethodNotAllowed, allow: "GET, HEAD, POST"},
		{name: "item GET", method: http.MethodGet, path: "/api/tokens/abc", want: http.StatusMethodNotAllowed, allow: "DELETE"},
		{name: "unknown path", method: http.MethodGet, path: "/api/other", want: http.StatusNotFound},
		{name: "nested path", method: http.MethodDelete, path: "/api/tokens/a/b", want: http.StatusNotFound},
		{name: "missing id", method: http.MethodDelete, path: "/api/tokens/", want: http.StatusMethodNotAllowed},
		{name: "delete unknown", method: http.MethodDelete, path: "/api/tokens/does-not-exist", want: http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, srv.URL+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			if tc.ctype != "" {
				req.Header.Set("Content-Type", tc.ctype)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("do request: %v", err)
			}
			defer resp.Body.Close()
			data, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", resp.StatusCode, tc.want, data)
			}
			if tc.allow != "" && resp.Header.Get("Allow") != tc.allow {
				t.Fatalf("Allow = %q, want %q", resp.Header.Get("Allow"), tc.allow)
			}
			if tc.want >= 400 && !json.Valid(data) {
				t.Fatalf("error body must be JSON, got %q", data)
			}
			if strings.Contains(string(data), "sqlite") || strings.Contains(string(data), "SQL") {
				t.Fatalf("error body leaked storage details: %s", data)
			}
		})
	}
}

func TestHandlerRejectsCrossOriginWrites(t *testing.T) {
	manager, _ := openTestManager(t, "")
	srv := httptest.NewServer(manager.Handler())
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/tokens", strings.NewReader(`{"name":"csrf"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin POST status = %d, want 403", resp.StatusCode)
	}
	if records, err := manager.list(t.Context()); err != nil || len(records) != 0 {
		t.Fatalf("cross-origin request created a token: records=%v err=%v", records, err)
	}

	// CLI-style requests without browser headers are allowed.
	req, err = http.NewRequest(http.MethodPost, srv.URL+"/api/tokens", strings.NewReader(`{"name":"cli"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	respCLI, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("cli request: %v", err)
	}
	respCLI.Body.Close()
	if respCLI.StatusCode != http.StatusCreated {
		t.Fatalf("no-origin POST status = %d, want 201", respCLI.StatusCode)
	}
}

func TestHandlerSafeMethodsAllowedCrossOrigin(t *testing.T) {
	manager, _ := openTestManager(t, "")
	srv := httptest.NewServer(manager.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/tokens", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cross-origin GET status = %d, want 200 (reads carry no secret)", resp.StatusCode)
	}
}

func TestHandlerUnavailableAfterClose(t *testing.T) {
	manager, _ := openTestManager(t, "")
	mustCreateToken(t, manager, "reader", false)
	srv := httptest.NewServer(manager.Handler())
	defer srv.Close()
	if err := manager.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	resp, body := apiRequest(t, srv, http.MethodGet, "/api/tokens", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("closed manager status = %d, want 503", resp.StatusCode)
	}
	if body["error"] == "" {
		t.Fatalf("expected JSON error body, got %v", body)
	}
}

func TestHandlerTrailingSlashCollection(t *testing.T) {
	manager, _ := openTestManager(t, "")
	srv := httptest.NewServer(manager.Handler())
	defer srv.Close()

	resp, body := apiRequest(t, srv, http.MethodGet, "/api/tokens/", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("trailing slash status = %d, want 200", resp.StatusCode)
	}
	if _, ok := body["tokens"].([]any); !ok {
		t.Fatalf("trailing slash must be the collection: %v", body)
	}
}

func TestMatchTokensPath(t *testing.T) {
	cases := []struct {
		path string
		id   string
		ok   bool
	}{
		{"/api/tokens", "", true},
		{"/api/tokens/", "", true},
		{"/api/tokens/abc", "abc", true},
		{"/api/tokens/a%2Fb", "a/b", true},
		{"/api/tokens/abc/def", "", false},
		{"/api/tokensx", "", false},
		{"/api", "", false},
		{"/api/tokens/%zz", "", false},
		{"/api/tokens/ id ", "", false},
	}
	for _, tc := range cases {
		id, ok := matchTokensPath(tc.path)
		if id != tc.id || ok != tc.ok {
			t.Errorf("matchTokensPath(%q) = (%q, %v), want (%q, %v)", tc.path, id, ok, tc.id, tc.ok)
		}
	}
}
