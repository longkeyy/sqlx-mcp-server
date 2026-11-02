package instances

import (
	"context"
	"github.com/longkeyy/sqlx-mcp-server/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExternalOverlay(t *testing.T) {
	m, dir := openTestManager(t)
	mustCreate(t, m, "alpha", sqliteFixture(t))
	dsn := sqliteFixture(t)
	m.SetExternal([]config.DatabaseConfig{{ID: "alpha", Name: "external", DSN: dsn}})
	c, e := m.Resolve(context.Background(), "alpha")
	if e != nil || c.DSN != dsn {
		t.Fatal(c, e)
	}
	cs, e := m.List(context.Background())
	if e != nil || len(cs) != 1 || cs[0].Name != "external" {
		t.Fatal(cs, e)
	}
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		code, _ := call(t, m, method, "/api/instances/alpha", `{"name":"changed"}`)
		if code != 403 {
			t.Fatal(method, code)
		}
	}
	code, body := call(t, m, http.MethodGet, "/api/instances", "")
	if code != 200 || strings.Contains(body, dsn) || !strings.Contains(body, `"source":"external"`) {
		t.Fatal(code, body)
	}
	m.SetExternal([]config.DatabaseConfig{{ID: "outside", DSN: dsn}})
	code, body = call(t, m, http.MethodPut, "/api/settings/default", `{"instance":"outside"}`)
	if code != 200 {
		t.Fatal(code, body)
	}
	n, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer n.Close()
	id, e := n.Default(context.Background())
	if e != nil || id != "outside" {
		t.Fatal(id, e)
	}
	m.Close()
	if _, e = m.Resolve(context.Background(), "outside"); e == nil {
		t.Fatal("closed manager resolved external")
	}
}
func TestStrictOriginAndTrailingJSON(t *testing.T) {
	for _, origin := range []string{"https://example.com", "ftp://example.com", "http://user@example.com", "http://example.com/path", "http://example.com?x=1"} {
		r := httptest.NewRequest("POST", "http://example.com/api/instances", nil)
		r.Header.Set("Origin", origin)
		if originAllowed(r) {
			t.Fatal("accepted", origin)
		}
	}
	m, _ := openTestManager(t)
	for _, body := range []string{`{"instance":"x"} {}`, `{"instance":"x"}]`} {
		code, _ := call(t, m, http.MethodPut, "/api/settings/default", body)
		if code != 400 {
			t.Fatal(code)
		}
	}
}
