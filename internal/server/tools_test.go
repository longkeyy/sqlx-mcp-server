package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/longkeyy/sqlx-mcp-server/internal/config"
	_ "modernc.org/sqlite"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestValidateReadQuery(t *testing.T) {
	valid := []string{
		"select * from users",
		"WITH users AS (SELECT 1) SELECT * FROM users",
		"/* comment */ SELECT 'drop table users' AS text",
	}
	for _, stmt := range valid {
		if err := validateReadQuery(stmt); err != nil {
			t.Fatalf("expected valid query %q, got %v", stmt, err)
		}
	}

	invalid := []string{
		"delete from users",
		"select * from users; drop table users",
		"select * from users where id in (delete from users)",
	}
	for _, stmt := range invalid {
		if err := validateReadQuery(stmt); err == nil {
			t.Fatalf("expected invalid query %q", stmt)
		}
	}
}

func TestSQLiteToolsReturnStructuredJSON(t *testing.T) {
	ctx := context.Background()

	app, err := newAppServer(testDatabaseConfig("test", "test-db", sqliteFileDSN(t, "structured")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.close)

	if _, err := app.db.ExecContext(ctx, `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);
INSERT INTO users (name) VALUES ('alice'), ('bob');
CREATE VIEW user_names AS SELECT name FROM users;`); err != nil {
		t.Fatal(err)
	}

	queryResult, err := app.queryToolHandler(ctx, newToolRequest(map[string]interface{}{
		"sql":      "SELECT id, name FROM users ORDER BY id",
		"max_rows": float64(1),
	}))
	if err != nil {
		t.Fatal(err)
	}
	var queryResp queryResponse
	decodeToolJSON(t, queryResult, &queryResp)
	if queryResp.Format != "columnar_rows" || queryResp.RowCount != 1 || !queryResp.Truncated {
		t.Fatalf("unexpected query response: %+v", queryResp)
	}
	if got := queryResp.Columns[0]; got != "id" {
		t.Fatalf("expected first column id, got %q", got)
	}

	listResult, err := app.listObjectsToolHandler(ctx, newToolRequest(nil))
	if err != nil {
		t.Fatal(err)
	}
	var listResp listObjectsResponse
	decodeToolJSON(t, listResult, &listResp)
	if len(listResp.Objects) != 2 {
		t.Fatalf("expected table and view, got %+v", listResp.Objects)
	}

	describeResult, err := app.describeObjectToolHandler(ctx, newToolRequest(map[string]interface{}{
		"object_type": "table",
		"object_name": "users",
	}))
	if err != nil {
		t.Fatal(err)
	}
	var descResp objectDescriptionResponse
	decodeToolJSON(t, describeResult, &descResp)
	if descResp.Object.Type != "table" || descResp.Object.Name != "users" {
		t.Fatalf("unexpected object description: %+v", descResp.Object)
	}
	if len(descResp.Columns) != 2 {
		t.Fatalf("expected two columns, got %+v", descResp.Columns)
	}
}

func TestSQLiteToolHandlersUseInstanceConnection(t *testing.T) {
	ctx := context.Background()
	appA, err := newAppServer(testDatabaseConfig("a", "a", sqliteFileDSN(t, "a")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(appA.close)
	appB, err := newAppServer(testDatabaseConfig("b", "b", sqliteFileDSN(t, "b")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(appB.close)

	if _, err := appA.db.ExecContext(ctx, `CREATE TABLE only_a (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := appB.db.ExecContext(ctx, `CREATE TABLE only_b (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}

	resultA, err := appA.listObjectsToolHandler(ctx, newToolRequest(nil))
	if err != nil {
		t.Fatal(err)
	}
	var listA listObjectsResponse
	decodeToolJSON(t, resultA, &listA)
	if !hasObject(listA.Objects, "only_a") || hasObject(listA.Objects, "only_b") {
		t.Fatalf("expected appA to see only only_a, got %+v", listA.Objects)
	}

	resultB, err := appB.listObjectsToolHandler(ctx, newToolRequest(nil))
	if err != nil {
		t.Fatal(err)
	}
	var listB listObjectsResponse
	decodeToolJSON(t, resultB, &listB)
	if !hasObject(listB.Objects, "only_b") || hasObject(listB.Objects, "only_a") {
		t.Fatalf("expected appB to see only only_b, got %+v", listB.Objects)
	}
}

func newToolRequest(args map[string]interface{}) mcp.CallToolRequest {
	if args == nil {
		args = map[string]interface{}{}
	}
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: args,
		},
	}
}

func decodeToolJSON(t *testing.T, result *mcp.CallToolResult, target interface{}) {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("empty tool result")
	}
	content, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("unexpected content type %T", result.Content[0])
	}
	if err := json.Unmarshal([]byte(content.Text), target); err != nil {
		t.Fatalf("invalid JSON %q: %v", content.Text, err)
	}
}

func hasObject(objects []objectInfo, name string) bool {
	for _, object := range objects {
		if object.Name == name {
			return true
		}
	}
	return false
}

func sqliteFileDSN(t *testing.T, name string) string {
	t.Helper()
	return "sqlite://" + t.TempDir() + "/" + name + ".db"
}

func testDatabaseConfig(id, name, dsn string) config.DatabaseConfig {
	return config.DatabaseConfig{
		ID:           id,
		Name:         name,
		DSN:          dsn,
		MaxOpenConns: 25,
		MaxIdleConns: 1,
	}
}
