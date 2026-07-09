package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

type ToolHandlerFunc func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error)

type queryResponse struct {
	Format    string          `json:"format"`
	Columns   []string        `json:"columns"`
	Rows      [][]interface{} `json:"rows"`
	RowCount  int             `json:"row_count"`
	Truncated bool            `json:"truncated"`
	MaxRows   int             `json:"max_rows"`
	Dialect   string          `json:"dialect"`
}

type objectInfo struct {
	Type   string `json:"type"`
	Schema string `json:"schema,omitempty"`
	Name   string `json:"name"`
}

type listObjectsResponse struct {
	Database string       `json:"database"`
	Dialect  string       `json:"dialect"`
	Objects  []objectInfo `json:"objects"`
}

type objectDescriptionResponse struct {
	Object     objectInfo               `json:"object"`
	Columns    []map[string]interface{} `json:"columns,omitempty"`
	Indexes    []map[string]interface{} `json:"indexes,omitempty"`
	Definition string                   `json:"definition,omitempty"`
	Dialect    string                   `json:"dialect"`
}

type qualifiedObjectName struct {
	Schema string
	Name   string
}

// ============================================================
// Tool Definitions
// ============================================================

func queryTool(desc string) mcp.Tool {
	return mcp.NewTool("query",
		mcp.WithDescription(desc),
		mcp.WithString("sql",
			mcp.Required(),
			mcp.Description("Read-oriented SQL query to execute"),
		),
		mcp.WithNumber("max_rows",
			mcp.Description("Maximum number of rows to return (default: 1000)"),
		),
	)
}

func executeTool(desc string) mcp.Tool {
	return mcp.NewTool("execute",
		mcp.WithDescription(desc),
		mcp.WithString("sql",
			mcp.Required(),
			mcp.Description("SQL statement to execute"),
		),
	)
}

func listObjectsTool(desc string) mcp.Tool {
	return mcp.NewTool("list_objects",
		mcp.WithDescription(desc),
		mcp.WithString("type",
			mcp.Description("Object type to list: table, view, procedure, function, or all (default: all)"),
		),
		mcp.WithString("pattern",
			mcp.Description("Regex pattern to filter object names (e.g., 'user.*' for tables starting with user)"),
		),
	)
}

func describeObjectTool(desc string) mcp.Tool {
	return mcp.NewTool("describe_object",
		mcp.WithDescription(desc),
		mcp.WithString("object_type",
			mcp.Description("Type of object: table, view, procedure, function. If not specified, searches all types."),
		),
		mcp.WithString("object_name",
			mcp.Required(),
			mcp.Description("Name of the object"),
		),
	)
}

// ============================================================
// Tool Handlers
// ============================================================

func (a *appServer) queryToolHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sqlStmt, ok := request.GetArguments()["sql"].(string)
	if !ok {
		return nil, errors.New("invalid or missing 'sql' parameter")
	}
	if err := validateReadQuery(sqlStmt); err != nil {
		return nil, err
	}

	maxRows := 1000
	if maxRowsParam, ok := request.GetArguments()["max_rows"].(float64); ok && maxRowsParam > 0 {
		maxRows = int(maxRowsParam)
	}

	rows, err := a.db.QueryContext(ctx, sqlStmt)
	if err != nil {
		log.Printf("Query error: %v\n", err)
		return nil, fmt.Errorf("query execution failed: %v", err)
	}
	defer rows.Close()

	results, err := parseSQLRowsWithLimit(rows, maxRows)
	if err != nil {
		return nil, fmt.Errorf("result parsing failed: %v", err)
	}

	results.MaxRows = maxRows
	results.Dialect = a.getSQLDialectName()
	return newJSONToolResult(results)
}

func (a *appServer) executeToolHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sqlStmt, ok := request.GetArguments()["sql"].(string)
	if !ok {
		return nil, errors.New("invalid or missing 'sql' parameter")
	}

	result, err := a.db.ExecContext(ctx, sqlStmt)
	if err != nil {
		log.Printf("Execute error: %v\n", err)
		return nil, fmt.Errorf("execution failed: %v", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		return mcp.NewToolResultText(fmt.Sprintf("Operation successful. Rows affected: %d", rowsAffected)), nil
	}

	return mcp.NewToolResultText("Operation successful."), nil
}

func (a *appServer) listObjectsToolHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	objectType := "all"
	if t, ok := request.GetArguments()["type"].(string); ok {
		objectType = strings.ToLower(t)
	}

	pattern := ""
	if p, ok := request.GetArguments()["pattern"].(string); ok {
		pattern = p
	}

	// Compile regex if provided
	var re *regexp.Regexp
	if pattern != "" {
		var err error
		re, err = regexp.Compile("(?i)" + pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid regex pattern: %v", err)
		}
	}

	// Get current database name
	dbName := a.getCurrentDatabase()

	// Collect objects by type
	tables := a.getObjectsByType(ctx, "table")
	views := a.getObjectsByType(ctx, "view")
	procedures := a.getObjectsByType(ctx, "procedure")
	functions := a.getObjectsByType(ctx, "function")

	// Apply regex filter if provided
	if re != nil {
		tables = filterObjectsByRegex(tables, re)
		views = filterObjectsByRegex(views, re)
		procedures = filterObjectsByRegex(procedures, re)
		functions = filterObjectsByRegex(functions, re)
	}

	var objects []objectInfo
	switch objectType {
	case "table":
		objects = append(objects, tables...)
	case "view":
		objects = append(objects, views...)
	case "procedure":
		objects = append(objects, procedures...)
	case "function":
		objects = append(objects, functions...)
	case "all":
		objects = append(objects, tables...)
		objects = append(objects, views...)
		objects = append(objects, procedures...)
		objects = append(objects, functions...)
	default:
		return nil, fmt.Errorf("unsupported object type: %s (use table, view, procedure, function, or all)", objectType)
	}

	response := listObjectsResponse{
		Database: dbName,
		Dialect:  a.getSQLDialectName(),
		Objects:  objects,
	}
	return newJSONToolResult(response)
}

func (a *appServer) describeObjectToolHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	objectName, ok := request.GetArguments()["object_name"].(string)
	if !ok {
		return nil, errors.New("invalid or missing 'object_name' parameter")
	}
	objectName = strings.TrimSpace(objectName)
	if objectName == "" {
		return nil, errors.New("invalid or empty 'object_name' parameter")
	}

	// If object_type is specified, return only that type
	if objectType, ok := request.GetArguments()["object_type"].(string); ok && strings.TrimSpace(objectType) != "" {
		objectType = strings.ToLower(objectType)
		var result objectDescriptionResponse
		var err error

		switch objectType {
		case "table":
			result, err = a.getTableDefinition(ctx, objectName)
		case "view":
			result, err = a.getViewDefinition(ctx, objectName)
		case "procedure":
			result, err = a.getProcedureDefinition(ctx, objectName)
		case "function":
			result, err = a.getFunctionDefinition(ctx, objectName)
		default:
			return nil, fmt.Errorf("unsupported object type: %s (use table, view, procedure, or function)", objectType)
		}

		if err != nil {
			return nil, err
		}
		return newJSONToolResult(result)
	}

	// If object_type is not specified, search all types
	var results []objectDescriptionResponse
	objectTypes := []string{"table", "view", "procedure", "function"}

	for _, objType := range objectTypes {
		var result objectDescriptionResponse
		var err error

		switch objType {
		case "table":
			result, err = a.getTableDefinition(ctx, objectName)
		case "view":
			result, err = a.getViewDefinition(ctx, objectName)
		case "procedure":
			result, err = a.getProcedureDefinition(ctx, objectName)
		case "function":
			result, err = a.getFunctionDefinition(ctx, objectName)
		}

		if err == nil {
			results = append(results, result)
		}
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("object '%s' not found (searched: table, view, procedure, function)", objectName)
	}

	if len(results) == 1 {
		return newJSONToolResult(results[0])
	}
	return newJSONToolResult(map[string]interface{}{
		"dialect": a.getSQLDialectName(),
		"matches": results,
	})
}

// ============================================================
// Helper Functions for Object Listing
// ============================================================

func (a *appServer) getObjectsByType(ctx context.Context, objectType string) []objectInfo {
	var objects []objectInfo

	switch a.driver {
	case DriverMySQL, DriverTiDB, DriverDoris:
		switch objectType {
		case "table":
			objects = a.queryObjects(ctx, objectType, `SELECT TABLE_SCHEMA, TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME`)
		case "view":
			objects = a.queryObjects(ctx, objectType, `SELECT TABLE_SCHEMA, TABLE_NAME FROM information_schema.VIEWS WHERE TABLE_SCHEMA = DATABASE() ORDER BY TABLE_NAME`)
		case "procedure":
			objects = a.queryObjects(ctx, objectType, `SELECT ROUTINE_SCHEMA, ROUTINE_NAME FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = DATABASE() AND ROUTINE_TYPE = 'PROCEDURE' ORDER BY ROUTINE_NAME`)
		case "function":
			objects = a.queryObjects(ctx, objectType, `SELECT ROUTINE_SCHEMA, ROUTINE_NAME FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = DATABASE() AND ROUTINE_TYPE = 'FUNCTION' ORDER BY ROUTINE_NAME`)
		}

	case DriverPostgres:
		switch objectType {
		case "table":
			objects = a.queryObjects(ctx, objectType, `SELECT schemaname, tablename FROM pg_catalog.pg_tables WHERE schemaname NOT IN ('pg_catalog', 'information_schema') ORDER BY schemaname, tablename`)
		case "view":
			objects = a.queryObjects(ctx, objectType, `SELECT schemaname, viewname FROM pg_catalog.pg_views WHERE schemaname NOT IN ('pg_catalog', 'information_schema') ORDER BY schemaname, viewname`)
		case "procedure":
			objects = a.queryObjects(ctx, objectType, `SELECT n.nspname, p.proname FROM pg_proc p JOIN pg_namespace n ON p.pronamespace = n.oid WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND p.prokind = 'p' ORDER BY n.nspname, p.proname`)
		case "function":
			objects = a.queryObjects(ctx, objectType, `SELECT n.nspname, p.proname FROM pg_proc p JOIN pg_namespace n ON p.pronamespace = n.oid WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND p.prokind IN ('f', 'a') ORDER BY n.nspname, p.proname`)
		}

	case DriverSQLite:
		switch objectType {
		case "table":
			objects = a.queryObjects(ctx, objectType, `SELECT '', name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
		case "view":
			objects = a.queryObjects(ctx, objectType, `SELECT '', name FROM sqlite_master WHERE type='view' ORDER BY name`)
		}

	case DriverMSSQL:
		switch objectType {
		case "table":
			objects = a.queryObjects(ctx, objectType, `SELECT TABLE_SCHEMA, TABLE_NAME FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_SCHEMA, TABLE_NAME`)
		case "view":
			objects = a.queryObjects(ctx, objectType, `SELECT TABLE_SCHEMA, TABLE_NAME FROM INFORMATION_SCHEMA.VIEWS ORDER BY TABLE_SCHEMA, TABLE_NAME`)
		case "procedure":
			objects = a.queryObjects(ctx, objectType, `SELECT ROUTINE_SCHEMA, ROUTINE_NAME FROM INFORMATION_SCHEMA.ROUTINES WHERE ROUTINE_TYPE = 'PROCEDURE' ORDER BY ROUTINE_SCHEMA, ROUTINE_NAME`)
		case "function":
			objects = a.queryObjects(ctx, objectType, `SELECT ROUTINE_SCHEMA, ROUTINE_NAME FROM INFORMATION_SCHEMA.ROUTINES WHERE ROUTINE_TYPE = 'FUNCTION' ORDER BY ROUTINE_SCHEMA, ROUTINE_NAME`)
		}

	case DriverClickHouse:
		switch objectType {
		case "table":
			objects = a.queryObjects(ctx, objectType, `SELECT database, name FROM system.tables WHERE database = currentDatabase() AND engine NOT IN ('View', 'MaterializedView', 'LiveView', 'WindowView') ORDER BY name`)
		case "view":
			objects = a.queryObjects(ctx, objectType, `SELECT database, name FROM system.tables WHERE database = currentDatabase() AND engine IN ('View', 'MaterializedView', 'LiveView', 'WindowView') ORDER BY name`)
		case "function":
			objects = a.queryObjects(ctx, objectType, `SELECT '', name FROM system.functions ORDER BY name`)
		}
	}

	return objects
}

func (a *appServer) queryObjects(ctx context.Context, objectType, query string, args ...interface{}) []objectInfo {
	rows, err := a.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []objectInfo
	for rows.Next() {
		var schema, name string
		if err := rows.Scan(&schema, &name); err != nil {
			return results
		}
		results = append(results, objectInfo{Type: objectType, Schema: schema, Name: name})
	}
	return results
}

func filterObjectsByRegex(items []objectInfo, re *regexp.Regexp) []objectInfo {
	var filtered []objectInfo
	for _, item := range items {
		if re.MatchString(item.Name) || re.MatchString(qualifiedName(item.Schema, item.Name)) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (a *appServer) getCurrentDatabase() string {
	var dbName string
	switch a.driver {
	case DriverMySQL, DriverTiDB, DriverDoris:
		err := a.db.QueryRow(`SELECT DATABASE()`).Scan(&dbName)
		if err != nil {
			dbName = "unknown"
		}
	case DriverPostgres:
		err := a.db.QueryRow(`SELECT current_database()`).Scan(&dbName)
		if err != nil {
			dbName = "unknown"
		}
	case DriverSQLite:
		dbName = "main"
	case DriverMSSQL:
		err := a.db.QueryRow(`SELECT DB_NAME()`).Scan(&dbName)
		if err != nil {
			dbName = "unknown"
		}
	case DriverClickHouse:
		err := a.db.QueryRow(`SELECT currentDatabase()`).Scan(&dbName)
		if err != nil {
			dbName = "unknown"
		}
	default:
		dbName = "unknown"
	}
	return dbName
}

func (a *appServer) getDBVersion() string {
	var version string
	var err error

	switch a.driver {
	case DriverMySQL, DriverTiDB, DriverDoris:
		err = a.db.QueryRow(`SELECT VERSION()`).Scan(&version)
	case DriverPostgres:
		err = a.db.QueryRow(`SELECT VERSION()`).Scan(&version)
	case DriverSQLite:
		err = a.db.QueryRow(`SELECT SQLITE_VERSION()`).Scan(&version)
	case DriverMSSQL:
		err = a.db.QueryRow(`SELECT @@VERSION`).Scan(&version)
	case DriverClickHouse:
		err = a.db.QueryRow(`SELECT version()`).Scan(&version)
	default:
		return "Unknown"
	}

	if err != nil {
		return "Unknown"
	}

	// Truncate long version strings
	if len(version) > 80 {
		version = version[:77] + "..."
	}

	return version
}

func (a *appServer) getSQLDialectName() string {
	switch a.driver {
	case DriverMySQL:
		return "MySQL"
	case DriverTiDB:
		return "TiDB"
	case DriverDoris:
		return "Apache Doris"
	case DriverPostgres:
		return "PostgreSQL"
	case DriverSQLite:
		return "SQLite"
	case DriverMSSQL:
		return "SQL Server"
	case DriverClickHouse:
		return "ClickHouse"
	default:
		return "SQL"
	}
}

// ============================================================
// Driver-Specific Queries for Definitions
// ============================================================

// getTableDefinition returns table structure (columns, types, constraints)
func (a *appServer) getTableDefinition(ctx context.Context, objectName string) (objectDescriptionResponse, error) {
	obj := parseQualifiedObjectName(objectName)
	response := objectDescriptionResponse{
		Object:  objectInfo{Type: "table", Schema: obj.Schema, Name: obj.Name},
		Dialect: a.getSQLDialectName(),
	}

	switch a.driver {
	case DriverMySQL, DriverTiDB, DriverDoris:
		rows, err := a.db.QueryContext(ctx, `
			SELECT COLUMN_NAME AS name, DATA_TYPE AS type, IS_NULLABLE AS nullable, COLUMN_DEFAULT AS default_value, COLUMN_COMMENT AS comment
			FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = COALESCE(NULLIF(?, ''), DATABASE()) AND TABLE_NAME = ?
			ORDER BY ORDINAL_POSITION`, obj.Schema, obj.Name)
		if err != nil {
			return response, fmt.Errorf("failed to get table definition: %v", err)
		}
		defer rows.Close()
		columns, err := parseSQLRows(rows)
		if err != nil {
			return response, fmt.Errorf("failed to parse table definition: %v", err)
		}
		response.Columns = columns

	case DriverPostgres:
		rows, err := a.db.QueryContext(ctx, `
			SELECT column_name AS name, data_type AS type, is_nullable AS nullable, column_default AS default_value
			FROM information_schema.columns
			WHERE table_schema = COALESCE(NULLIF($1, ''), 'public') AND table_name = $2
			ORDER BY ordinal_position`, obj.Schema, obj.Name)
		if err != nil {
			return response, fmt.Errorf("failed to get table definition: %v", err)
		}
		defer rows.Close()
		columns, err := parseSQLRows(rows)
		if err != nil {
			return response, fmt.Errorf("failed to parse table definition: %v", err)
		}
		response.Columns = columns

	case DriverSQLite:
		rows, err := a.db.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, quoteSQLiteIdentifier(obj.Name)))
		if err != nil {
			return response, fmt.Errorf("failed to get table definition: %v", err)
		}
		defer rows.Close()
		columns, err := parseSQLRows(rows)
		if err != nil {
			return response, fmt.Errorf("failed to parse table definition: %v", err)
		}
		response.Columns = columns

	case DriverClickHouse:
		rows, err := a.db.QueryContext(ctx, `
			SELECT name, type, if(default_expression = '', NULL, default_expression) AS default_value, comment
			FROM system.columns
			WHERE database = if(? = '', currentDatabase(), ?) AND table = ?
			ORDER BY position`, obj.Schema, obj.Schema, obj.Name)
		if err != nil {
			return response, fmt.Errorf("failed to get table definition: %v", err)
		}
		defer rows.Close()
		columns, err := parseSQLRows(rows)
		if err != nil {
			return response, fmt.Errorf("failed to parse table definition: %v", err)
		}
		response.Columns = columns

	case DriverMSSQL:
		rows, err := a.db.QueryContext(ctx, `
			SELECT COLUMN_NAME AS name, DATA_TYPE AS type, IS_NULLABLE AS nullable, COLUMN_DEFAULT AS default_value
			FROM INFORMATION_SCHEMA.COLUMNS
			WHERE TABLE_SCHEMA = COALESCE(NULLIF(@p1, ''), SCHEMA_NAME()) AND TABLE_NAME = @p2
			ORDER BY ORDINAL_POSITION`, obj.Schema, obj.Name)
		if err != nil {
			return response, fmt.Errorf("failed to get table definition: %v", err)
		}
		defer rows.Close()
		columns, err := parseSQLRows(rows)
		if err != nil {
			return response, fmt.Errorf("failed to parse table definition: %v", err)
		}
		response.Columns = columns
	}

	if len(response.Columns) == 0 {
		return response, fmt.Errorf("table '%s' not found", objectName)
	}

	response.Indexes = a.getIndexInfo(ctx, obj)
	return response, nil
}

func (a *appServer) getViewDefinition(ctx context.Context, objectName string) (objectDescriptionResponse, error) {
	obj := parseQualifiedObjectName(objectName)
	response := objectDescriptionResponse{
		Object:  objectInfo{Type: "view", Schema: obj.Schema, Name: obj.Name},
		Dialect: a.getSQLDialectName(),
	}
	var row *sql.Row

	switch a.driver {
	case DriverMySQL, DriverTiDB, DriverDoris:
		row = a.db.QueryRowContext(ctx, `SELECT VIEW_DEFINITION FROM information_schema.VIEWS WHERE TABLE_SCHEMA = COALESCE(NULLIF(?, ''), DATABASE()) AND TABLE_NAME = ?`, obj.Schema, obj.Name)

	case DriverPostgres:
		row = a.db.QueryRowContext(ctx, `SELECT definition FROM pg_views WHERE schemaname = COALESCE(NULLIF($1, ''), 'public') AND viewname = $2`, obj.Schema, obj.Name)

	case DriverSQLite:
		row = a.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'view' AND name = ?`, obj.Name)

	case DriverMSSQL:
		row = a.db.QueryRowContext(ctx, `SELECT VIEW_DEFINITION FROM INFORMATION_SCHEMA.VIEWS WHERE TABLE_SCHEMA = COALESCE(NULLIF(@p1, ''), SCHEMA_NAME()) AND TABLE_NAME = @p2`, obj.Schema, obj.Name)

	case DriverClickHouse:
		row = a.db.QueryRowContext(ctx, `SELECT create_table_query FROM system.tables WHERE database = if(? = '', currentDatabase(), ?) AND name = ? AND engine IN ('View', 'MaterializedView', 'LiveView', 'WindowView')`, obj.Schema, obj.Schema, obj.Name)
	}

	err := row.Scan(&response.Definition)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return response, fmt.Errorf("view '%s' not found", objectName)
		}
		return response, fmt.Errorf("failed to get view definition: %v", err)
	}
	return response, nil
}

func (a *appServer) getProcedureDefinition(ctx context.Context, objectName string) (objectDescriptionResponse, error) {
	obj := parseQualifiedObjectName(objectName)
	response := objectDescriptionResponse{
		Object:  objectInfo{Type: "procedure", Schema: obj.Schema, Name: obj.Name},
		Dialect: a.getSQLDialectName(),
	}
	var row *sql.Row

	switch a.driver {
	case DriverMySQL, DriverTiDB, DriverDoris:
		row = a.db.QueryRowContext(ctx, `SELECT ROUTINE_DEFINITION FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = COALESCE(NULLIF(?, ''), DATABASE()) AND ROUTINE_NAME = ? AND ROUTINE_TYPE = 'PROCEDURE'`, obj.Schema, obj.Name)

	case DriverPostgres:
		row = a.db.QueryRowContext(ctx, `
			SELECT pg_get_functiondef(p.oid)
			FROM pg_proc p
			JOIN pg_namespace n ON p.pronamespace = n.oid
			WHERE n.nspname = COALESCE(NULLIF($1, ''), 'public') AND p.proname = $2 AND p.prokind = 'p'`, obj.Schema, obj.Name)

	case DriverSQLite:
		return response, errors.New("SQLite does not support stored procedures")

	case DriverClickHouse:
		return response, errors.New("ClickHouse does not support stored procedures")

	case DriverMSSQL:
		row = a.db.QueryRowContext(ctx, `SELECT OBJECT_DEFINITION(OBJECT_ID(@p1))`, sqlServerQualifiedName(obj))
	}

	err := row.Scan(&response.Definition)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return response, fmt.Errorf("procedure '%s' not found", objectName)
		}
		return response, fmt.Errorf("failed to get procedure definition: %v", err)
	}
	return response, nil
}

func (a *appServer) getFunctionDefinition(ctx context.Context, objectName string) (objectDescriptionResponse, error) {
	obj := parseQualifiedObjectName(objectName)
	response := objectDescriptionResponse{
		Object:  objectInfo{Type: "function", Schema: obj.Schema, Name: obj.Name},
		Dialect: a.getSQLDialectName(),
	}
	var row *sql.Row

	switch a.driver {
	case DriverMySQL, DriverTiDB, DriverDoris:
		row = a.db.QueryRowContext(ctx, `SELECT ROUTINE_DEFINITION FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = COALESCE(NULLIF(?, ''), DATABASE()) AND ROUTINE_NAME = ? AND ROUTINE_TYPE = 'FUNCTION'`, obj.Schema, obj.Name)

	case DriverPostgres:
		row = a.db.QueryRowContext(ctx, `
			SELECT pg_get_functiondef(p.oid)
			FROM pg_proc p
			JOIN pg_namespace n ON p.pronamespace = n.oid
			WHERE n.nspname = COALESCE(NULLIF($1, ''), 'public') AND p.proname = $2`, obj.Schema, obj.Name)

	case DriverSQLite:
		return response, errors.New("SQLite does not support user-defined functions")

	case DriverClickHouse:
		var name string
		err := a.db.QueryRowContext(ctx, `SELECT name FROM system.functions WHERE name = ?`, obj.Name).Scan(&name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return response, fmt.Errorf("function '%s' not found", objectName)
			}
			return response, fmt.Errorf("failed to get function definition: %v", err)
		}
		response.Definition = "ClickHouse function: " + name
		return response, nil

	case DriverMSSQL:
		row = a.db.QueryRowContext(ctx, `SELECT OBJECT_DEFINITION(OBJECT_ID(@p1))`, sqlServerQualifiedName(obj))
	}

	err := row.Scan(&response.Definition)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return response, fmt.Errorf("function '%s' not found", objectName)
		}
		return response, fmt.Errorf("failed to get function definition: %v", err)
	}
	return response, nil
}

func (a *appServer) getIndexInfo(ctx context.Context, obj qualifiedObjectName) []map[string]interface{} {
	var rows *sql.Rows
	var err error

	switch a.driver {
	case DriverMySQL, DriverTiDB, DriverDoris:
		rows, err = a.db.QueryContext(ctx, `SELECT INDEX_NAME AS name, COLUMN_NAME AS column_name, NON_UNIQUE AS non_unique, SEQ_IN_INDEX AS seq FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = COALESCE(NULLIF(?, ''), DATABASE()) AND TABLE_NAME = ? ORDER BY INDEX_NAME, SEQ_IN_INDEX`, obj.Schema, obj.Name)

	case DriverPostgres:
		rows, err = a.db.QueryContext(ctx, `SELECT indexname AS name, indexdef AS definition FROM pg_indexes WHERE schemaname = COALESCE(NULLIF($1, ''), 'public') AND tablename = $2 ORDER BY indexname`, obj.Schema, obj.Name)

	case DriverSQLite:
		rows, err = a.db.QueryContext(ctx, fmt.Sprintf(`PRAGMA index_list(%s)`, quoteSQLiteIdentifier(obj.Name)))

	case DriverMSSQL:
		rows, err = a.db.QueryContext(ctx, `SELECT i.name AS name, c.name AS column_name FROM sys.indexes i JOIN sys.index_columns ic ON i.object_id = ic.object_id AND i.index_id = ic.index_id JOIN sys.columns c ON ic.object_id = c.object_id AND ic.column_id = c.column_id WHERE i.object_id = OBJECT_ID(@p1) ORDER BY i.name, ic.key_ordinal`, sqlServerQualifiedName(obj))

	case DriverClickHouse:
		return nil
	}

	if err != nil {
		return nil
	}
	defer rows.Close()

	results, err := parseSQLRows(rows)
	if err != nil {
		return nil
	}
	return results
}

func parseSQLRowsWithLimit(rows *sql.Rows, maxRows int) (queryResponse, error) {
	cols, err := rows.Columns()
	if err != nil {
		return queryResponse{}, err
	}

	response := queryResponse{
		Format:  "columnar_rows",
		Columns: cols,
		Rows:    [][]interface{}{},
	}
	count := 0
	for rows.Next() {
		if maxRows > 0 && count >= maxRows {
			response.Truncated = true
			break
		}
		values := make([]interface{}, len(cols))
		pointers := make([]interface{}, len(cols))
		for i := range values {
			pointers[i] = &values[i]
		}

		if err := rows.Scan(pointers...); err != nil {
			return response, err
		}

		row := make([]interface{}, len(cols))
		for i := range cols {
			row[i] = normalizeSQLValue(values[i])
		}
		response.Rows = append(response.Rows, row)
		count++
	}

	if err := rows.Err(); err != nil {
		return response, err
	}

	response.RowCount = len(response.Rows)
	return response, nil
}

func parseSQLRows(rows *sql.Rows) ([]map[string]interface{}, error) {
	result, err := parseSQLRowsWithLimit(rows, 0)
	if err != nil {
		return nil, err
	}
	mapped := make([]map[string]interface{}, 0, len(result.Rows))
	for _, row := range result.Rows {
		item := make(map[string]interface{}, len(result.Columns))
		for i, col := range result.Columns {
			item[col] = row[i]
		}
		mapped = append(mapped, item)
	}
	return mapped, nil
}

func newJSONToolResult(value interface{}) (*mcp.CallToolResult, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return mcp.NewToolResultText(string(data)), nil
}

func normalizeSQLValue(value interface{}) interface{} {
	switch v := value.(type) {
	case []byte:
		return string(v)
	default:
		return v
	}
}

func validateReadQuery(sqlStmt string) error {
	cleaned := strings.TrimSpace(stripSQLComments(sqlStmt))
	if cleaned == "" {
		return errors.New("query must not be empty")
	}
	if hasMultipleStatements(cleaned) {
		return errors.New("query must contain exactly one statement")
	}

	upper := strings.ToUpper(strings.TrimSpace(strings.TrimSuffix(cleaned, ";")))
	allowedLeading := regexp.MustCompile(`^(SELECT|WITH|EXPLAIN|SHOW|DESCRIBE)\b`)
	if !allowedLeading.MatchString(upper) {
		return errors.New("query only allows read-oriented statements: SELECT, WITH, EXPLAIN, SHOW, DESCRIBE")
	}

	upperWithoutLiterals := strings.ToUpper(stripSQLStringLiterals(upper))
	blocked := regexp.MustCompile(`\b(INSERT|UPDATE|DELETE|CREATE|ALTER|DROP|TRUNCATE|MERGE|CALL|EXEC|GRANT|REVOKE|REPLACE|VACUUM|ANALYZE|ATTACH|DETACH)\b`)
	if blocked.MatchString(upperWithoutLiterals) {
		return errors.New("query contains a mutating or unsafe SQL keyword; use execute for write operations")
	}
	if strings.Contains(upperWithoutLiterals, "PRAGMA WRITABLE_SCHEMA") {
		return errors.New("query contains an unsafe SQLite pragma")
	}
	return nil
}

func stripSQLComments(sqlStmt string) string {
	var b strings.Builder
	inSingle := false
	inDouble := false
	inLineComment := false
	inBlockComment := false
	runes := []rune(sqlStmt)

	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}

		if inLineComment {
			if ch == '\n' {
				inLineComment = false
				b.WriteRune(ch)
			}
			continue
		}
		if inBlockComment {
			if ch == '*' && next == '/' {
				inBlockComment = false
				i++
			}
			continue
		}
		if !inSingle && !inDouble && ch == '-' && next == '-' {
			inLineComment = true
			i++
			continue
		}
		if !inSingle && !inDouble && ch == '/' && next == '*' {
			inBlockComment = true
			i++
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
		}
		b.WriteRune(ch)
	}
	return b.String()
}

func hasMultipleStatements(sqlStmt string) bool {
	inSingle := false
	inDouble := false
	runes := []rune(sqlStmt)
	for i, ch := range runes {
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if ch == ';' && !inSingle && !inDouble {
			rest := strings.TrimSpace(string(runes[i+1:]))
			return rest != ""
		}
	}
	return false
}

func stripSQLStringLiterals(sqlStmt string) string {
	var b strings.Builder
	inSingle := false
	inDouble := false
	runes := []rune(sqlStmt)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if ch == '\'' && !inDouble {
			if inSingle && i+1 < len(runes) && runes[i+1] == '\'' {
				i++
				continue
			}
			inSingle = !inSingle
			b.WriteRune(' ')
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			b.WriteRune(' ')
			continue
		}
		if inSingle || inDouble {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(ch)
	}
	return b.String()
}

func parseQualifiedObjectName(input string) qualifiedObjectName {
	parts := strings.Split(strings.TrimSpace(input), ".")
	if len(parts) == 1 {
		return qualifiedObjectName{Name: trimIdentifierQuotes(parts[0])}
	}
	return qualifiedObjectName{
		Schema: trimIdentifierQuotes(parts[len(parts)-2]),
		Name:   trimIdentifierQuotes(parts[len(parts)-1]),
	}
}

func trimIdentifierQuotes(input string) string {
	input = strings.TrimSpace(input)
	input = strings.Trim(input, "`")
	input = strings.Trim(input, `"`)
	input = strings.Trim(input, `[]`)
	return input
}

func qualifiedName(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}

func quoteSQLiteIdentifier(name string) string {
	var b strings.Builder
	b.WriteRune('"')
	for _, ch := range name {
		if ch == '"' {
			b.WriteString(`""`)
			continue
		}
		b.WriteRune(ch)
	}
	b.WriteRune('"')
	return b.String()
}

func sqlServerQualifiedName(obj qualifiedObjectName) string {
	if obj.Schema == "" {
		return obj.Name
	}
	return obj.Schema + "." + obj.Name
}
