package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/longkeyy/sqlx-mcp-server/internal/dsn"
)

const (
	toolQuery          = "query"
	toolExecute        = "execute"
	toolListObjects    = "list_objects"
	toolDescribeObject = "describe_object"
	toolListInstances  = "list_instances"
)

// unifiedTools returns the gateway tool definitions. They keep the baseline
// tool names, description wording and arguments from internal/server and add
// one required "instance" selector.
func unifiedTools() []mcp.Tool {
	return []mcp.Tool{
		mcp.NewTool(toolQuery,
			mcp.WithDescription("Execute a read-oriented SQL query (SELECT, WITH, EXPLAIN, SHOW, DESCRIBE) on the instance named by \"instance\". Results are returned as compact JSON with columns plus row arrays."),
			mcp.WithString("instance",
				mcp.Required(),
				mcp.Description("Database instance ID to route this call to (see list_instances)"),
			),
			mcp.WithString("sql",
				mcp.Required(),
				mcp.Description("Read-oriented SQL query to execute"),
			),
			mcp.WithNumber("max_rows",
				mcp.Description("Maximum number of rows to return (default: 1000)"),
			),
		),
		mcp.NewTool(toolExecute,
			mcp.WithDescription("Execute a write operation (DML, DDL, or a stored procedure call) on the instance named by \"instance\"."),
			mcp.WithString("instance",
				mcp.Required(),
				mcp.Description("Database instance ID to route this call to (see list_instances)"),
			),
			mcp.WithString("sql",
				mcp.Required(),
				mcp.Description("SQL statement to execute"),
			),
		),
		mcp.NewTool(toolListObjects,
			mcp.WithDescription("List database objects (tables, views, procedures, functions) on the instance named by \"instance\" as JSON object records."),
			mcp.WithString("instance",
				mcp.Required(),
				mcp.Description("Database instance ID to route this call to (see list_instances)"),
			),
			mcp.WithString("type",
				mcp.Description("Object type to list: table, view, procedure, function, or all (default: all)"),
			),
			mcp.WithString("pattern",
				mcp.Description("Regex pattern to filter object names (e.g., 'user.*' for tables starting with user)"),
			),
		),
		mcp.NewTool(toolDescribeObject,
			mcp.WithDescription("Describe a database object (table, view, procedure, function) on the instance named by \"instance\" as JSON metadata."),
			mcp.WithString("instance",
				mcp.Required(),
				mcp.Description("Database instance ID to route this call to (see list_instances)"),
			),
			mcp.WithString("object_type",
				mcp.Description("Type of object: table, view, procedure, function. If not specified, searches all types."),
			),
			mcp.WithString("object_name",
				mcp.Required(),
				mcp.Description("Name of the object"),
			),
		),
	}
}

// fixedTools derives the /db/{id}/... schemas from the unified ones by dropping
// the instance selector. Deriving instead of restating keeps the two schemas in
// lockstep: a baseline argument added to one shows up in both.
func fixedTools() []mcp.Tool {
	unified := unifiedTools()
	fixed := make([]mcp.Tool, len(unified))
	for i, tool := range unified {
		schema := mcp.ToolInputSchema{
			Type:       tool.InputSchema.Type,
			Properties: make(map[string]any, len(tool.InputSchema.Properties)),
			Required:   make([]string, 0, len(tool.InputSchema.Required)),
		}
		for name, property := range tool.InputSchema.Properties {
			if name == "instance" {
				continue
			}
			schema.Properties[name] = property
		}
		for _, name := range tool.InputSchema.Required {
			if name == "instance" {
				continue
			}
			schema.Required = append(schema.Required, name)
		}
		tool.InputSchema = schema
		fixed[i] = tool
	}
	return fixed
}

// buildUnifiedServer creates the gateway server mounted at /mcp and /sse.
// Discovery and every tool call resolve the registry at call time, so no
// database connection exists until a client targets an instance.
func (r *Router) buildUnifiedServer() *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer("sqlx-mcp-gateway", "1.0.0")
	s.AddTool(mcp.NewTool(toolListInstances,
		mcp.WithDescription("List the database instances available through this gateway. Returns IDs, names, dialects, memos and availability. Credentials and DSNs are never exposed."),
	), r.listInstancesHandler)
	for _, tool := range unifiedTools() {
		name := tool.Name
		s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return r.handleUnifiedTool(ctx, name, request)
		})
	}
	return s
}

// buildFixedServer creates the server for /db/{id}/... The instance is bound to
// the URL, so the schemas have no selector and supplying one is rejected.
func (r *Router) buildFixedServer(id string) *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer("sqlx-mcp-gateway", "1.0.0")
	for _, tool := range fixedTools() {
		name := tool.Name
		s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return r.handleFixedTool(ctx, id, name, request)
		})
	}
	return s
}

// instanceDescriptor is the public discovery record. It intentionally carries
// no DSN or credential material.
type instanceDescriptor struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Dialect string `json:"dialect,omitempty"`
	Memo    string `json:"memo,omitempty"`
	Status  string `json:"status"`
}

type listInstancesResponse struct {
	Instances []instanceDescriptor `json:"instances"`
}

// listInstancesHandler is discovery on the unified endpoint. Status only
// reports what the registry knows ("available"); probing each database during
// discovery would slow the call down and turn one broken instance into a
// failed listing.
func (r *Router) listInstancesHandler(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if err := r.authorizeTool(ctx, toolListInstances); err != nil {
		return toolError("%v", err), nil
	}
	configs, err := r.registry.List(ctx)
	if err != nil {
		return toolError("failed to list instances: %v", err), nil
	}
	instances := make([]instanceDescriptor, 0, len(configs))
	for _, cfg := range configs {
		descriptor := instanceDescriptor{ID: cfg.ID, Name: cfg.Name, Memo: cfg.Memo, Status: "available"}
		if parsed, parseErr := dsn.Parse(cfg.DSN); parseErr == nil {
			descriptor.Dialect = describeDialect(parsed.Driver)
		}
		instances = append(instances, descriptor)
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].ID < instances[j].ID })
	encoded, err := json.MarshalIndent(listInstancesResponse{Instances: instances}, "", "  ")
	if err != nil {
		return toolError("failed to encode the instance list: %v", err), nil
	}
	return mcp.NewToolResultText(string(encoded)), nil
}

// handleUnifiedTool routes one call on /mcp or /sse. The instance argument is
// mandatory and names exactly one target; there is deliberately no default and
// no switch_instance state, so concurrent callers cannot observe each other's
// selection.
func (r *Router) handleUnifiedTool(ctx context.Context, name string, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	arguments := request.GetArguments()
	raw, supplied := arguments["instance"]
	instance, ok := raw.(string)
	instance = strings.TrimSpace(instance)
	if !supplied || !ok || instance == "" {
		return toolError("missing required argument %q: name the target instance (see %s)", "instance", toolListInstances), nil
	}
	return r.callInstance(ctx, instance, name, withoutInstance(arguments))
}

// handleFixedTool routes one call on /db/{id}/mcp or /db/{id}/sse. The path
// binding is authoritative: an "instance" argument is an error rather than an
// override, and the registry is resolved for the bound ID at call time.
func (r *Router) handleFixedTool(ctx context.Context, id, name string, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	arguments := request.GetArguments()
	if _, supplied := arguments["instance"]; supplied {
		return toolError("argument %q is not supported on this fixed-instance endpoint; the instance is selected by the URL", "instance"), nil
	}
	return r.callInstance(ctx, id, name, arguments)
}

// authorizeTool runs the configured authorizer before a tool does any work. It
// sees the request context, so token middleware can attach identity on both
// transports; a missing identity is a denial, not a bypass.
func (r *Router) authorizeTool(ctx context.Context, name string) error {
	if r.authorize == nil {
		return nil
	}
	if err := r.authorize(ctx, name); err != nil {
		return fmt.Errorf("tool %q is not authorized: %w", name, err)
	}
	return nil
}

// withoutInstance returns a copy of arguments with the gateway-only selector
// removed, so the baseline schema sees exactly its original argument set. The
// caller's map is left untouched.
func withoutInstance(arguments map[string]any) map[string]any {
	trimmed := make(map[string]any, len(arguments))
	for key, value := range arguments {
		if key == "instance" {
			continue
		}
		trimmed[key] = value
	}
	return trimmed
}

// describeDialect maps the router-level driver names to the dialect labels
// used by the baseline tools.
func describeDialect(driver dsn.Driver) string {
	switch driver {
	case dsn.DriverSQLite:
		return "SQLite"
	case dsn.DriverMySQL:
		return "MySQL"
	case dsn.DriverMariaDB:
		return "MariaDB"
	case dsn.DriverPercona:
		return "Percona Server for MySQL"
	case dsn.DriverTiDB:
		return "TiDB"
	case dsn.DriverDoris:
		return "Apache Doris"
	case dsn.DriverOceanBase:
		return "OceanBase"
	case dsn.DriverStarRocks:
		return "StarRocks"
	case dsn.DriverPostgres:
		return "PostgreSQL"
	case dsn.DriverCockroach:
		return "CockroachDB"
	case dsn.DriverGreenplum:
		return "Greenplum"
	case dsn.DriverOpenGauss:
		return "openGauss"
	case dsn.DriverMogDB:
		return "MogDB"
	case dsn.DriverMSSQL:
		return "SQL Server"
	case dsn.DriverClickHouse:
		return "ClickHouse"
	default:
		return strings.ToUpper(string(driver))
	}
}
