package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthMiddlewareAllowsRequestsWhenTokenEmpty(t *testing.T) {
	handler := AuthMiddleware("")(okHandler())
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if resp.Code != http.StatusOK {
		t.Fatalf("expected OK without token, got %d", resp.Code)
	}
}

func TestAuthMiddlewareAcceptsBearerToken(t *testing.T) {
	handler := AuthMiddleware("secret-token")(okHandler())
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "bearer secret-token")
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected OK with bearer token, got %d", resp.Code)
	}
}

func TestAuthMiddlewareAcceptsQueryToken(t *testing.T) {
	handler := AuthMiddleware("secret-token")(okHandler())
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/sse?token=secret-token", nil))

	if resp.Code != http.StatusOK {
		t.Fatalf("expected OK with query token, got %d", resp.Code)
	}
}

func TestAuthMiddlewareRejectsMissingOrInvalidToken(t *testing.T) {
	handler := AuthMiddleware("secret-token")(okHandler())
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/mcp", nil),
		httptest.NewRequest(http.MethodPost, "/mcp?token=wrong", nil),
		func() *http.Request {
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.Header.Set("Authorization", "Bearer wrong")
			return req
		}(),
	} {
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("expected unauthorized for %s, got %d", req.URL.String(), resp.Code)
		}
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}
