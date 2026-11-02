package instances

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/longkeyy/sqlx-mcp-server/internal/config"
)

// openTestManager opens a Manager in a fresh temporary data directory.
func openTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	m, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%q): %v", dir, err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, dir
}

// sqliteFixture creates a real SQLite database file and returns its DSN.
func sqliteFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "business.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE marker(value TEXT); INSERT INTO marker VALUES ('ok')`); err != nil {
		t.Fatalf("populate fixture: %v", err)
	}
	return "sqlite://" + path
}

// call performs a request against the manager handler and returns status/body.
func call(t *testing.T, m *Manager, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func mustCreate(t *testing.T, m *Manager, id, dsn string) {
	t.Helper()
	body := fmt.Sprintf(`{"id":%q,"name":%q,"dsn":%q,"memo":"memo"}`, id, id, dsn)
	code, resp := call(t, m, http.MethodPost, "/api/instances", body)
	if code != http.StatusCreated {
		t.Fatalf("create %q: status=%d body=%s", id, code, resp)
	}
}

func TestOpenEmptyStore(t *testing.T) {
	m, _ := openTestManager(t)
	ctx := context.Background()

	items, err := m.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if items == nil || len(items) != 0 {
		t.Fatalf("expected non-nil empty list, got %#v", items)
	}

	def, err := m.Default(ctx)
	if err != nil || def != "" {
		t.Fatalf("Default on empty store = %q, %v", def, err)
	}

	if _, err := m.Resolve(ctx, "nope"); err == nil {
		t.Fatal("Resolve on empty store succeeded")
	}

	code, body := call(t, m, http.MethodGet, "/api/instances", "")
	if code != http.StatusOK || !strings.Contains(body, `"instances":[]`) {
		t.Fatalf("GET /api/instances = %d %s", code, body)
	}
	code, body = call(t, m, http.MethodGet, "/api/settings", "")
	if code != http.StatusOK || !strings.Contains(body, `"default_instance":""`) {
		t.Fatalf("GET /api/settings = %d %s", code, body)
	}
}

func TestOpenRequiresDataDir(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("Open with empty data dir succeeded")
	}
}

func TestCloseIsIdempotentAndStopsAccess(t *testing.T) {
	dir := t.TempDir()
	m, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := m.List(context.Background()); !errors.Is(err, errClosed) {
		t.Fatalf("List after Close: %v (want errClosed)", err)
	}
	if _, err := m.Resolve(context.Background(), "x"); !errors.Is(err, errClosed) {
		t.Fatalf("Resolve after Close: %v (want errClosed)", err)
	}
}

func TestCreateListAndResolve(t *testing.T) {
	m, _ := openTestManager(t)
	ctx := context.Background()
	dsn := sqliteFixture(t)
	mustCreate(t, m, "alpha", dsn)

	items, err := m.List(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("List = %#v, %v", items, err)
	}
	got := items[0]
	if got.ID != "alpha" || got.Name != "alpha" || got.Memo != "memo" || got.DSN != dsn {
		t.Fatalf("unexpected instance: %#v", got)
	}
	if got.MaxOpenConns <= 0 || got.MaxIdleConns <= 0 {
		t.Fatalf("pool defaults missing: %#v", got)
	}

	resolved, err := m.Resolve(ctx, "alpha")
	if err != nil || resolved.DSN != dsn {
		t.Fatalf("Resolve = %#v, %v", resolved, err)
	}
	if _, err := m.Resolve(ctx, "ALPHA"); err == nil {
		t.Fatal("Resolve must not match a different id")
	}
	if _, err := m.Resolve(ctx, ""); err == nil {
		t.Fatal("Resolve with empty id succeeded")
	}
}

func TestCreateValidation(t *testing.T) {
	m, _ := openTestManager(t)
	dsn := sqliteFixture(t)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"missing id", fmt.Sprintf(`{"dsn":%q}`, dsn), http.StatusBadRequest},
		{"invalid id characters", fmt.Sprintf(`{"id":"bad id","dsn":%q}`, dsn), http.StatusBadRequest},
		{"id with slash", fmt.Sprintf(`{"id":"a/b","dsn":%q}`, dsn), http.StatusBadRequest},
		{"missing dsn", `{"id":"x"}`, http.StatusBadRequest},
		{"unsupported scheme", `{"id":"x","dsn":"http://example.com/db"}`, http.StatusBadRequest},
		{"malformed json", `{"id":`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := call(t, m, http.MethodPost, "/api/instances", tc.body)
			if code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", code, tc.want, body)
			}
			if !strings.Contains(body, `"error"`) {
				t.Fatalf("error response missing error field: %s", body)
			}
		})
	}

	mustCreate(t, m, "alpha", dsn)
	code, body := call(t, m, http.MethodPost, "/api/instances", fmt.Sprintf(`{"id":"alpha","dsn":%q}`, dsn))
	if code != http.StatusConflict {
		t.Fatalf("duplicate create status=%d body=%s", code, body)
	}
}

func TestCreateAllowsUnreachableButValidDSN(t *testing.T) {
	m, _ := openTestManager(t)
	mustCreate(t, m, "broken", "postgres://u:p@127.0.0.1:1/db?connect_timeout=1")
	if items, err := m.List(context.Background()); err != nil || len(items) != 1 {
		t.Fatalf("unreachable instance was not retained: %#v %v", items, err)
	}
}

func TestPatchPartialUpdateKeepsIDAndDSN(t *testing.T) {
	m, _ := openTestManager(t)
	ctx := context.Background()
	original := sqliteFixture(t)
	mustCreate(t, m, "alpha", original)

	code, body := call(t, m, http.MethodPatch, "/api/instances/alpha", `{"name":"Renamed","memo":"updated"}`)
	if code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", code, body)
	}
	if strings.Contains(body, `"dsn"`) || strings.Contains(body, original) {
		t.Fatalf("patch response leaked DSN: %s", body)
	}
	got, err := m.Resolve(ctx, "alpha")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ID != "alpha" || got.Name != "Renamed" || got.Memo != "updated" || got.DSN != original {
		t.Fatalf("unexpected patched instance: %#v", got)
	}

	replacement := sqliteFixture(t)
	code, body = call(t, m, http.MethodPatch, "/api/instances/alpha", fmt.Sprintf(`{"dsn":%q}`, replacement))
	if code != http.StatusOK {
		t.Fatalf("patch dsn status=%d body=%s", code, body)
	}
	got, err = m.Resolve(ctx, "alpha")
	if err != nil || got.ID != "alpha" || got.DSN != replacement {
		t.Fatalf("Resolve after dsn patch = %#v, %v", got, err)
	}

	if code, _ := call(t, m, http.MethodPatch, "/api/instances/missing", `{"name":"x"}`); code != http.StatusNotFound {
		t.Fatalf("patch missing status=%d", code)
	}
	if code, _ := call(t, m, http.MethodPatch, "/api/instances/alpha", `{"dsn":"http://example.com"}`); code != http.StatusBadRequest {
		t.Fatalf("patch invalid dsn status=%d", code)
	}
	if got, err = m.Resolve(ctx, "alpha"); err != nil || got.DSN != replacement {
		t.Fatalf("invalid patch replaced working DSN: %#v %v", got, err)
	}
	if code, _ := call(t, m, http.MethodPatch, "/api/instances/alpha", `{"dsn":""}`); code != http.StatusOK {
		t.Fatalf("empty dsn patch status=%d", code)
	}
	if got, err = m.Resolve(ctx, "alpha"); err != nil || got.DSN != replacement {
		t.Fatalf("empty dsn patch changed stored DSN: %#v %v", got, err)
	}
}

func TestDeleteClearsDefault(t *testing.T) {
	m, _ := openTestManager(t)
	ctx := context.Background()
	dsn := sqliteFixture(t)
	mustCreate(t, m, "alpha", dsn)
	mustCreate(t, m, "beta", dsn)

	if code, body := call(t, m, http.MethodPut, "/api/settings/default", `{"instance":"alpha"}`); code != http.StatusOK {
		t.Fatalf("set default status=%d body=%s", code, body)
	}
	code, body := call(t, m, http.MethodDelete, "/api/instances/alpha", "")
	if code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", code, body)
	}
	if _, err := m.Resolve(ctx, "alpha"); err == nil {
		t.Fatal("deleted instance still resolves")
	}
	if def, err := m.Default(ctx); err != nil || def != "" {
		t.Fatalf("default after deleting default = %q, %v", def, err)
	}
	if code, _ := call(t, m, http.MethodDelete, "/api/instances/alpha", ""); code != http.StatusNotFound {
		t.Fatalf("second delete status=%d", code)
	}
	items, err := m.List(ctx)
	if err != nil || len(items) != 1 || items[0].ID != "beta" {
		t.Fatalf("remaining instances = %#v, %v", items, err)
	}
}

func TestDefaultSettingValidationAndPersistence(t *testing.T) {
	m, dir := openTestManager(t)
	ctx := context.Background()
	dsn := sqliteFixture(t)
	mustCreate(t, m, "alpha", dsn)

	if code, _ := call(t, m, http.MethodPut, "/api/settings/default", `{}`); code != http.StatusBadRequest {
		t.Fatalf("missing instance status=%d", code)
	}
	if code, _ := call(t, m, http.MethodPut, "/api/settings/default", `{"instance":"missing"}`); code != http.StatusNotFound {
		t.Fatalf("unknown instance status=%d", code)
	}
	code, body := call(t, m, http.MethodPut, "/api/settings/default", `{"instance":"alpha"}`)
	if code != http.StatusOK || !strings.Contains(body, `"default_instance":"alpha"`) {
		t.Fatalf("set default = %d %s", code, body)
	}
	if def, err := m.Default(ctx); err != nil || def != "alpha" {
		t.Fatalf("Default = %q, %v", def, err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if def, err := reopened.Default(context.Background()); err != nil || def != "alpha" {
		t.Fatalf("Default after reopen = %q, %v", def, err)
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	m, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	dsn := sqliteFixture(t)
	mustCreate(t, m, "alpha", dsn)
	if code, body := call(t, m, http.MethodPatch, "/api/instances/alpha", `{"name":"Persisted","memo":"note"}`); code != http.StatusOK {
		t.Fatalf("patch: %d %s", code, body)
	}
	if code, body := call(t, m, http.MethodPut, "/api/settings/default", `{"instance":"alpha"}`); code != http.StatusOK {
		t.Fatalf("default: %d %s", code, body)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	ctx := context.Background()
	items, err := reopened.List(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("List after reopen = %#v, %v", items, err)
	}
	got := items[0]
	if got.ID != "alpha" || got.Name != "Persisted" || got.Memo != "note" || got.DSN != dsn {
		t.Fatalf("persisted instance = %#v", got)
	}
	if def, err := reopened.Default(ctx); err != nil || def != "alpha" {
		t.Fatalf("persisted default = %q, %v", def, err)
	}
}

func TestManagersShareStore(t *testing.T) {
	first, dir := openTestManager(t)
	second, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ctx := context.Background()

	dsn := sqliteFixture(t)
	mustCreate(t, first, "alpha", dsn)
	if _, err := second.Resolve(ctx, "alpha"); err != nil {
		t.Fatalf("second manager cannot see instance: %v", err)
	}

	replacement := sqliteFixture(t)
	if code, body := call(t, second, http.MethodPatch, "/api/instances/alpha", fmt.Sprintf(`{"dsn":%q}`, replacement)); code != http.StatusOK {
		t.Fatalf("patch via second manager: %d %s", code, body)
	}
	got, err := first.Resolve(ctx, "alpha")
	if err != nil || got.DSN != replacement {
		t.Fatalf("first manager did not observe update: %#v %v", got, err)
	}
}

func TestConcurrentCreates(t *testing.T) {
	m, _ := openTestManager(t)
	dsn := sqliteFixture(t)
	const n = 24

	var wg sync.WaitGroup
	errs := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("db-%02d", i)
			body := fmt.Sprintf(`{"id":%q,"name":%q,"dsn":%q}`, id, id, dsn)
			code, resp := call(t, m, http.MethodPost, "/api/instances", body)
			if code != http.StatusCreated {
				errs <- fmt.Sprintf("%s: status=%d body=%s", id, code, resp)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}

	items, err := m.List(context.Background())
	if err != nil || len(items) != n {
		t.Fatalf("List after concurrent creates = %d items, %v", len(items), err)
	}
	for i := 1; i < len(items); i++ {
		if items[i-1].ID >= items[i].ID {
			t.Fatalf("list is not ordered: %q then %q", items[i-1].ID, items[i].ID)
		}
	}
}

func TestListReturnsDefaultsWithoutMutatingStoredDSN(t *testing.T) {
	m, _ := openTestManager(t)
	dsn := sqliteFixture(t)
	mustCreate(t, m, "alpha", dsn)

	items, err := m.List(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("List = %#v, %v", items, err)
	}
	want := config.DatabaseConfig{ID: "alpha", Name: "alpha", Memo: "memo", DSN: dsn, MaxOpenConns: defaultMaxOpenConns, MaxIdleConns: defaultMaxIdleConns}
	if items[0] != want {
		t.Fatalf("List item = %#v, want %#v", items[0], want)
	}
}
