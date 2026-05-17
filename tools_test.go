package main

import (
	"context"
	"encoding/json"
	"testing"

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
	t.Cleanup(resetTestDB)

	dsnConfig, err := ParseDSN("sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := initConnectionPool(dsnConfig); err != nil {
		t.Fatal(err)
	}
	dbDriver = dsnConfig.Driver
	t.Cleanup(func() {
		_ = db.Close()
	})

	if _, err := db.ExecContext(ctx, `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);
INSERT INTO users (name) VALUES ('alice'), ('bob');
CREATE VIEW user_names AS SELECT name FROM users;`); err != nil {
		t.Fatal(err)
	}

	queryResult, err := queryToolHandler(ctx, newToolRequest(map[string]interface{}{
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

	listResult, err := listObjectsToolHandler(ctx, newToolRequest(nil))
	if err != nil {
		t.Fatal(err)
	}
	var listResp listObjectsResponse
	decodeToolJSON(t, listResult, &listResp)
	if len(listResp.Objects) != 2 {
		t.Fatalf("expected table and view, got %+v", listResp.Objects)
	}

	describeResult, err := describeObjectToolHandler(ctx, newToolRequest(map[string]interface{}{
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

func resetTestDB() {
	if db != nil {
		_ = db.Close()
	}
	db = nil
	dbDriver = ""
}
