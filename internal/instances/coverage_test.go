package instances

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/longkeyy/sqlx-mcp-server/internal/dsn"
)

func TestDialectNames(t *testing.T) {
	cases := map[dsn.Driver]string{
		dsn.DriverMySQL:      "MySQL",
		dsn.DriverMariaDB:    "MariaDB",
		dsn.DriverPercona:    "Percona Server for MySQL",
		dsn.DriverTiDB:       "TiDB",
		dsn.DriverDoris:      "Apache Doris",
		dsn.DriverOceanBase:  "OceanBase",
		dsn.DriverStarRocks:  "StarRocks",
		dsn.DriverPostgres:   "PostgreSQL",
		dsn.DriverCockroach:  "CockroachDB",
		dsn.DriverGreenplum:  "Greenplum",
		dsn.DriverOpenGauss:  "openGauss",
		dsn.DriverMogDB:      "MogDB",
		dsn.DriverSQLite:     "SQLite",
		dsn.DriverMSSQL:      "SQL Server",
		dsn.DriverClickHouse: "ClickHouse",
		dsn.Driver("other"):  "SQL",
	}
	for driver, want := range cases {
		if got := dialectName(driver); got != want {
			t.Errorf("dialectName(%q) = %q, want %q", driver, got, want)
		}
	}
}

func TestParseRoute(t *testing.T) {
	cases := []struct {
		path  string
		route string
		id    string
		ok    bool
	}{
		{"/api/instances", "instances", "", true},
		{"/api/instances/", "instances", "", true},
		{"/api/instances/alpha", "item", "alpha", true},
		{"/api/instances/alpha/", "item", "alpha", true},
		{"/api/instances/alpha/test", "test", "alpha", true},
		{"/api/instances/alpha/test/", "test", "alpha", true},
		{"/api/settings", "settings", "", true},
		{"/api/settings/default", "default", "", true},
		// Deeper paths belong to the integration adapter, not this module.
		{"/api/instances/alpha/objects", "", "", false},
		{"/api/instances/alpha/test/extra", "", "", false},
		{"/api/instances//test", "", "", false},
		{"/", "", "", false},
		{"/api/unknown", "", "", false},
	}
	for _, tc := range cases {
		route, id, ok := parseRoute(tc.path)
		if ok != tc.ok || route != tc.route || id != tc.id {
			t.Errorf("parseRoute(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.path, route, id, ok, tc.route, tc.id, tc.ok)
		}
	}
}

func TestContentTypeDetection(t *testing.T) {
	cases := map[string]bool{
		"application/json":                  true,
		"application/json; charset=utf-8":   true,
		"application/vnd.api+json":          true,
		"text/plain":                        false,
		"":                                  false,
		"application/json;;":                false,
		"application/x-www-form-urlencoded": false,
	}
	for raw, want := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/instances", strings.NewReader("{}"))
		if raw != "" {
			req.Header.Set("Content-Type", raw)
		}
		if got := hasJSONContentType(req); got != want {
			t.Errorf("hasJSONContentType(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestNormalizationHelpers(t *testing.T) {
	if _, ok := lookupID(""); ok {
		t.Error("empty id accepted")
	}
	if _, ok := lookupID(strings.Repeat("a", maxIDLength+1)); ok {
		t.Error("overlong id accepted")
	}
	if id, ok := lookupID(" Alpha-1 "); !ok || id != "alpha-1" {
		t.Errorf("lookupID did not canonicalise: %q %v", id, ok)
	}
	if _, err := normalizeID(strings.Repeat("a", maxIDLength+1)); err == nil {
		t.Error("overlong id accepted by normalizeID")
	}
	if _, err := normalizeName(strings.Repeat("n", maxNameLength+1), "alpha"); err == nil {
		t.Error("overlong name accepted")
	}
	if _, err := normalizeMemo(strings.Repeat("m", maxMemoLength+1)); err == nil {
		t.Error("overlong memo accepted")
	}
	if _, err := validateDSN(strings.Repeat("x", maxDSNLength+1)); err == nil {
		t.Error("overlong dsn accepted")
	}
	if _, err := validateDSN("sqlite:///tmp/only-params?mode=ro"); err != nil {
		t.Errorf("sqlite dsn rejected: %v", err)
	}
}

func TestFormatUnix(t *testing.T) {
	if got := formatUnix(0); got != "" {
		t.Errorf("formatUnix(0) = %q", got)
	}
	if got := formatUnix(1600000000); got != "2020-09-13T12:26:40Z" {
		t.Errorf("formatUnix(1600000000) = %q", got)
	}
}

func TestHandlerErrorsAfterClose(t *testing.T) {
	dir := t.TempDir()
	m, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ method, path, body string }{
		{http.MethodGet, "/api/instances/alpha", ""},
		{http.MethodPatch, "/api/instances/alpha", `{"name":"x"}`},
		{http.MethodDelete, "/api/instances/alpha", ""},
		{http.MethodPost, "/api/instances/alpha/test", `{}`},
		{http.MethodGet, "/api/settings", ""},
		{http.MethodPut, "/api/settings/default", `{"instance":"alpha"}`},
		{http.MethodPost, "/api/instances", `{"id":"alpha","dsn":"sqlite:///tmp/x.db"}`},
	}
	for _, tc := range cases {
		rec := request(t, m, tc.method, tc.path, tc.body, nil)
		assertJSONError(t, rec, http.StatusServiceUnavailable)
	}
}

func TestConfigClampsPoolSettings(t *testing.T) {
	rec := record{ID: "alpha", MaxOpenConns: 4, MaxIdleConns: 9}
	if cfg := rec.config(); cfg.MaxIdleConns != 4 {
		t.Errorf("idle connections not clamped: %d", cfg.MaxIdleConns)
	}
	rec = record{ID: "alpha"}
	if cfg := rec.config(); cfg.MaxOpenConns != defaultMaxOpenConns || cfg.MaxIdleConns != defaultMaxIdleConns {
		t.Errorf("defaults not applied: %d/%d", cfg.MaxOpenConns, cfg.MaxIdleConns)
	}
}

// TestStoredPoolValuesSurviveDirectEdits verifies List reports stored pool
// values (and repairs nonsensical ones) without rewriting the store.
func TestStoredPoolValuesSurviveDirectEdits(t *testing.T) {
	m, _ := openTestManager(t)
	mustCreate(t, m, "alpha", sqliteFixture(t))

	if _, err := m.db.ExecContext(context.Background(),
		`UPDATE instances SET max_open_conns = 3, max_idle_conns = 7 WHERE id = 'alpha'`); err != nil {
		t.Fatal(err)
	}
	items, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].MaxOpenConns != 3 || items[0].MaxIdleConns != 3 {
		t.Fatalf("unexpected pool settings: %#v", items)
	}
	var stored int
	if err := m.db.QueryRowContext(context.Background(),
		`SELECT max_idle_conns FROM instances WHERE id = 'alpha'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 7 {
		t.Fatalf("List rewrote the store: max_idle_conns = %d", stored)
	}
}

func TestOpenRejectsInvalidDataDir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(file); err == nil {
		t.Fatal("data directory that is a regular file was accepted")
	}
	if _, err := Open(filepath.Join(dir, "has?query")); err == nil {
		t.Fatal("data directory containing '?' was accepted")
	}
}

func TestProbeConn(t *testing.T) {
	ctx := context.Background()
	if err := probeConn(ctx, "sqlite://:memory:"); err == nil {
		t.Log("driver accepted sqlite://:memory:")
	}
	if err := probeConn(ctx, "http://example.com/db"); err == nil {
		t.Error("unsupported scheme probed successfully")
	} else if strings.Contains(err.Error(), "example.com") {
		t.Errorf("probe error echoed the DSN: %v", err)
	}
	if !isMemorySQLite(":memory:") || isMemorySQLite("/tmp/app.sqlite") {
		t.Error("in-memory detection is wrong")
	}
}

func TestServeTestReportsParseFailure(t *testing.T) {
	m, _ := openTestManager(t)
	dsn := sqliteFixture(t)
	mustCreate(t, m, "alpha", dsn)

	// Simulate a row written by an older version with a DSN this build cannot
	// parse; the endpoint must still answer with ok=false rather than 500.
	if _, err := m.db.ExecContext(context.Background(),
		`UPDATE instances SET dsn = 'bogus://user:topsecret@host/db' WHERE id = 'alpha'`); err != nil {
		t.Fatal(err)
	}
	rec := request(t, m, http.MethodPost, "/api/instances/alpha/test", `{}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	checkNoSecret(t, "unparsable test", rec.Body.String(), "topsecret")
}

// TestProbeConnSelectsRows covers the query step after a successful ping:
// the fixture database contains a table, so the count is non-zero.
func TestProbeConnSelectsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE marker (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := probeConn(context.Background(), "sqlite://"+path); err != nil {
		t.Fatalf("probe failed: %v", err)
	}
}
