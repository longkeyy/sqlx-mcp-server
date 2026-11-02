package routing

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestFixedServerSessionReloadsAndRejectsDeletedInstance(t *testing.T) {
	ctx := context.Background()
	registry := newMemRegistry()
	registry.put("alpha", markerFixture(t, "before-update"))
	router, err := New(registry)
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	fixed, err := router.FixedServer(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	session, err := client.NewInProcessClient(fixed)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "stdio-regression", Version: "1"}
	if _, err = session.Initialize(ctx, init); err != nil {
		t.Fatal(err)
	}
	request := mcp.CallToolRequest{}
	request.Params.Name = "query"
	request.Params.Arguments = map[string]any{"sql": "SELECT value FROM marker"}
	call := func(want string, wantError bool) {
		t.Helper()
		result, err := session.CallTool(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError != wantError {
			t.Fatalf("IsError=%v want=%v result=%+v", result.IsError, wantError, result)
		}
		if want != "" {
			matched := false
			for _, content := range result.Content {
				if text, ok := content.(mcp.TextContent); ok && strings.Contains(text.Text, want) {
					matched = true
				}
			}
			if !matched {
				t.Fatalf("missing marker %q in %+v", want, result)
			}
		}
	}
	call("before-update", false)
	registry.put("alpha", markerFixture(t, "after-update"))
	call("after-update", false)
	registry.remove("alpha")
	call("", true)
}
