package tokens

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type seenRequest struct {
	token string
	tool  string
	calls int
}

func testDownstream(manager *Manager, seen *seenRequest) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.calls++
		seen.token = TokenFromContext(r.Context())
		// The downstream stands in for the MCP layer, where tool names carry no
		// leading slash; the path is shortened only to select one.
		seen.tool = strings.TrimPrefix(r.URL.Path, "/")
		if err := manager.Authorize(seen.token, seen.tool); err != nil {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func serveThrough(middleware http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	middleware.ServeHTTP(rec, req)
	return rec
}

func TestMiddlewareBootstrapAllowsAnonymous(t *testing.T) {
	manager, _ := openTestManager(t, "")
	seen := &seenRequest{}
	handler := manager.Middleware(testDownstream(manager, seen))

	rec := serveThrough(handler, http.MethodGet, "/query", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("bootstrap request status = %d, want 204: %s", rec.Code, rec.Body)
	}
	if seen.token != "" {
		t.Fatalf("bootstrap context token = %q, want empty", seen.token)
	}
}

func TestMiddlewareAcceptsBearerAndQuery(t *testing.T) {
	manager, _ := openTestManager(t, "")
	_, secret := mustCreateToken(t, manager, "reader", false)
	seen := &seenRequest{}
	handler := manager.Middleware(testDownstream(manager, seen))

	tests := []struct {
		name    string
		target  string
		headers map[string]string
		want    string
	}{
		{"bearer", "/query", map[string]string{"Authorization": "Bearer " + secret}, secret},
		{"lower-case scheme", "/query", map[string]string{"Authorization": "bearer   " + secret + "  "}, secret},
		{"query param", "/query?token=" + secret, nil, secret},
		{"query wins as fallback", "/query?token=" + secret, map[string]string{"Authorization": "Bearer invalid"}, secret},
		{"bearer wins when valid", "/query?token=invalid", map[string]string{"Authorization": "Bearer " + secret}, secret},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen.token = ""
			rec := serveThrough(handler, http.MethodGet, tt.target, tt.headers)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body)
			}
			if seen.token != tt.want {
				t.Fatalf("context token = %q, want %q", seen.token, tt.want)
			}
		})
	}
}

func TestMiddlewareRejectsMissingAndInvalidCredentials(t *testing.T) {
	manager, _ := openTestManager(t, "")
	_, secret := mustCreateToken(t, manager, "reader", false)
	seen := &seenRequest{}
	handler := manager.Middleware(testDownstream(manager, seen))

	cases := []struct {
		name       string
		target     string
		headers    map[string]string
		challenge  string
		wantStatus int
	}{
		{"no credential", "/query", nil, `Bearer realm="sql-mcp"`, http.StatusUnauthorized},
		{"invalid bearer", "/query", map[string]string{"Authorization": "Bearer nope"}, `Bearer realm="sql-mcp", error="invalid_token"`, http.StatusUnauthorized},
		{"wrong scheme", "/query", map[string]string{"Authorization": "Basic " + secret}, `Bearer realm="sql-mcp"`, http.StatusUnauthorized},
		{"invalid query", "/query?token=nope", nil, `Bearer realm="sql-mcp", error="invalid_token"`, http.StatusUnauthorized},
		{"empty bearer", "/query", map[string]string{"Authorization": "Bearer "}, `Bearer realm="sql-mcp"`, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveThrough(handler, http.MethodGet, tc.target, tc.headers)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != tc.challenge {
				t.Fatalf("WWW-Authenticate = %q, want %q", got, tc.challenge)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
			if seen.calls != 0 {
				t.Fatalf("downstream called %d times on unauthorized request", seen.calls)
			}
		})
	}
}

func TestMiddlewarePropagatesExecuteDenialToDownstream(t *testing.T) {
	manager, _ := openTestManager(t, "")
	_, reader := mustCreateToken(t, manager, "reader", false)
	_, writer := mustCreateToken(t, manager, "writer", true)
	seen := &seenRequest{}
	handler := manager.Middleware(testDownstream(manager, seen))

	if rec := serveThrough(handler, http.MethodGet, "/execute", map[string]string{"Authorization": "Bearer " + reader}); rec.Code != http.StatusForbidden {
		t.Fatalf("reader execute status = %d, want 403", rec.Code)
	}
	if rec := serveThrough(handler, http.MethodGet, "/execute", map[string]string{"Authorization": "Bearer " + writer}); rec.Code != http.StatusNoContent {
		t.Fatalf("writer execute status = %d, want 204", rec.Code)
	}
	if rec := serveThrough(handler, http.MethodGet, "/query", map[string]string{"Authorization": "Bearer " + reader}); rec.Code != http.StatusNoContent {
		t.Fatalf("reader query status = %d, want 204", rec.Code)
	}
}

func TestMiddlewareRejectsRevokedTokenOnExistingHandler(t *testing.T) {
	manager, _ := openTestManager(t, "")
	rec, secret := mustCreateToken(t, manager, "reader", false)
	seen := &seenRequest{}
	handler := manager.Middleware(testDownstream(manager, seen))

	if rec := serveThrough(handler, http.MethodGet, "/query", map[string]string{"Authorization": "Bearer " + secret}); rec.Code != http.StatusNoContent {
		t.Fatalf("valid token status = %d, want 204", rec.Code)
	}
	if removed, err := manager.revokeToken(context.Background(), rec.ID); err != nil || !removed {
		t.Fatalf("revoke: removed=%v err=%v", removed, err)
	}
	if rec := serveThrough(handler, http.MethodGet, "/query", map[string]string{"Authorization": "Bearer " + secret}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token status = %d, want 401", rec.Code)
	}
}

func TestMiddlewareFailsClosedWhenClosed(t *testing.T) {
	manager, _ := openTestManager(t, "")
	_, secret := mustCreateToken(t, manager, "reader", false)
	handler := manager.Middleware(testDownstream(manager, &seenRequest{}))
	if err := manager.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rec := serveThrough(handler, http.MethodGet, "/query", map[string]string{"Authorization": "Bearer " + secret})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed manager status = %d, want 503", rec.Code)
	}
}

func TestMiddlewareDoesNotStripQueryToken(t *testing.T) {
	manager, _ := openTestManager(t, "")
	_, secret := mustCreateToken(t, manager, "reader", false)
	var rawQuery string
	handler := manager.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusNoContent)
	}))
	serveThrough(handler, http.MethodGet, "/sse?token="+secret, nil)
	if rawQuery == "" {
		t.Fatal("middleware must preserve the token query for SSE message endpoints")
	}
}

func TestMiddlewareRequiresHandler(t *testing.T) {
	manager, _ := openTestManager(t, "")
	defer func() {
		if recover() == nil {
			t.Fatal("expected Middleware(nil) to panic")
		}
	}()
	manager.Middleware(nil)
}

func TestBearerTokenParsing(t *testing.T) {
	cases := []struct {
		header string
		want   string
		ok     bool
	}{
		{"Bearer abc", "abc", true},
		{"bearer abc", "abc", true},
		{"  BEARER   abc  ", "abc", true},
		{"Bearer", "", false},
		{"Bearer   ", "", false},
		{"Basic abc", "", false},
		{"", "", false},
		{"Bearer a b", "a b", true},
	}
	for _, tc := range cases {
		got, ok := bearerToken(tc.header)
		if got != tc.want || ok != tc.ok {
			t.Errorf("bearerToken(%q) = (%q, %v), want (%q, %v)", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}

func TestAuthorizeRejectsOversizedToken(t *testing.T) {
	manager, _ := openTestManager(t, "")
	mustCreateToken(t, manager, "reader", false)
	err := manager.Authorize(strings.Repeat("a", 10_000), "query")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("oversized token error = %v, want ErrUnauthorized", err)
	}
}
