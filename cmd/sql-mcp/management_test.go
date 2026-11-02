package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/longkeyy/sqlx-mcp-server/internal/config"
	"github.com/longkeyy/sqlx-mcp-server/internal/instances"
	"github.com/longkeyy/sqlx-mcp-server/internal/routing"
)

func TestManagedMetadataAndExternalProtection(t *testing.T) {
	manager, err := instances.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	manager.SetExternal([]config.DatabaseConfig{{ID: "external", Name: "External", DSN: "sqlite://" + filepath.Join(t.TempDir(), "db.sqlite")}})
	router, err := routing.New(manager)
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"PATCH", "/api/instances/external", `{"name":"Changed"}`, 403},
		{"DELETE", "/api/instances/external", "", 403},
		{"POST", "/api/instances", `{"id":"external","dsn":"sqlite:///tmp/ignored.db"}`, 409},
		{"PUT", "/api/settings/default", `{"instance":"external"}`, 200},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		manager.Handler().ServeHTTP(res, req)
		if res.Code != tc.status {
			t.Fatalf("%s %s status=%d body=%s", tc.method, tc.path, res.Code, res.Body.String())
		}
	}
	if id, err := manager.Default(context.Background()); err != nil || id != "external" {
		t.Fatalf("default=%q err=%v", id, err)
	}
	for _, tc := range []struct {
		method, query string
		status        int
	}{
		{"GET", "", 200}, {"GET", "?sql=DROP+TABLE+x", 400}, {"GET", "?page_size=2&page_size=3", 400}, {"POST", "", 405},
	} {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, "/api/instances/external/objects"+tc.query, nil)
		serveMetadata(res, req, router, []string{"external", "objects"})
		if res.Code != tc.status {
			t.Fatalf("metadata %s status=%d body=%s", tc.query, res.Code, res.Body.String())
		}
		if tc.status == http.StatusOK {
			var result map[string]any
			if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if _, ok := result["objects"]; !ok {
				t.Fatalf("missing objects: %s", res.Body.String())
			}
		}
	}
}
