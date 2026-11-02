package webui

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedRoutes(t *testing.T) {
	for _, p := range []string{"/", "/instances", "/tokens"} {
		r := httptest.NewRecorder()
		Handler().ServeHTTP(r, httptest.NewRequest("GET", p, nil))
		if r.Code != 200 || !strings.Contains(r.Body.String(), "SQLX") {
			t.Fatalf("%s: %d", p, r.Code)
		}
	}
	for _, p := range []string{"/api/missing", "/missing.js"} {
		r := httptest.NewRecorder()
		Handler().ServeHTTP(r, httptest.NewRequest("GET", p, nil))
		if r.Code != 404 {
			t.Fatal(p, r.Code)
		}
	}
}
