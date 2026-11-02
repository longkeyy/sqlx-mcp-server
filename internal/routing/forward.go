package routing

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// unifiedCallID is the JSON-RPC ID used for the internal hop from the router to
// the instance's baseline MCP server. The baseline tools never inspect it, and
// every call builds its own request, so a constant keeps the payload minimal.
const unifiedCallID = 1

// callInstance forwards one tools/call to the baseline MCP server that owns the
// target instance. The call travels as a real JSON-RPC message through
// HandleMessage rather than invoking the tool handler directly, so the
// baseline's registry lookup, hook pipeline and error formatting are the ones
// in effect. The context carries the caller's identity and cancellation.
//
// A JSON-RPC error from the baseline (unknown tool, unparsable request) becomes
// a failed tool result: the router always reports failures for the endpoint the
// client actually called, never as a transport error.
func (r *Router) callInstance(ctx context.Context, id, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	if err := r.authorizeTool(ctx, name); err != nil {
		return toolError("%v", err), nil
	}

	return r.forwardInstance(ctx, id, name, arguments)
}

// CallMetadata serves the trusted admin metadata API; it cannot execute SQL.
// The caller must apply its admin origin/network policy before invoking this method.
func (r *Router) CallMetadata(ctx context.Context, id, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	if name != "list_objects" && name != "describe_object" {
		return nil, fmt.Errorf("metadata tool is not allowed")
	}
	return r.forwardInstance(ctx, id, name, arguments)
}

func (r *Router) forwardInstance(ctx context.Context, id, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	cfg, err := r.registry.Resolve(ctx, id)
	if err != nil {
		_ = r.forget(id)
		return toolError("unknown instance %q: %v", id, err), nil
	}
	if cfg.ID == "" {
		cfg.ID = id
	}
	entry, err := r.acquire(ctx, cfg)
	if err != nil {
		return toolError("instance %q is unavailable: %v", id, err), nil
	}
	defer r.release(entry)

	if arguments == nil {
		arguments = map[string]any{}
	}
	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      unifiedCallID,
		"method":  mcp.MethodToolsCall,
		"params": map[string]any{
			"name":      name,
			"arguments": arguments,
		},
	})
	if err != nil {
		return toolError("failed to encode the %q call: %v", name, err), nil
	}

	response := entry.app.MCPServer().HandleMessage(ctx, request)
	switch message := response.(type) {
	case nil:
		return toolError("instance %q returned no response for %q", id, name), nil
	case mcp.JSONRPCError:
		return toolError("%s", message.Error.Message), nil
	case mcp.JSONRPCResponse:
		result, err := toolResultFrom(message.Result)
		if err != nil {
			return toolError("instance %q returned an invalid %q result: %v", id, name, err), nil
		}
		return tagResult(result, id), nil
	default:
		return toolError("instance %q returned an unexpected %T response for %q", id, response, name), nil
	}
}

// toolResultFrom normalizes the Result field of a JSON-RPC response. mcp-go
// returns the concrete *mcp.CallToolResult for tools/call; the map and raw
// forms are accepted so a transport that re-encodes the message still works.
func toolResultFrom(value any) (*mcp.CallToolResult, error) {
	switch typed := value.(type) {
	case *mcp.CallToolResult:
		if typed == nil {
			return nil, fmt.Errorf("empty result")
		}
		return typed, nil
	case mcp.CallToolResult:
		return &typed, nil
	case json.RawMessage:
		return mcp.ParseCallToolResult(&typed)
	case map[string]any:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return nil, err
		}
		raw := json.RawMessage(encoded)
		return mcp.ParseCallToolResult(&raw)
	default:
		return nil, fmt.Errorf("unsupported result type %T", value)
	}
}
