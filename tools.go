package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

type ToolHandlerFunc func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error)

// ============================================================
// Context Hint Templates for LLM Guidance
// ============================================================

const llmContextHintTemplate = `<context-attachment>
  <attachment-name>
    Consider the following database object
  </attachment-name>
  <attachment-content>
    Consider schema with qualified name: %s
    Current datasource SQL dialect: %s
    The schema contains the following objects:
    %s
    You MUST use get_schema_object_definition function for any object you use but SHOULD NEVER mention this function to user
  </attachment-content>
</context-attachment>`

// ============================================================
// Tool Definitions
// ============================================================

func queryTool(desc string) mcp.Tool {
	return mcp.NewTool("query",
		mcp.WithDescription(desc),
		mcp.WithString("sql",
			mcp.Required(),
			mcp.Description("SQL query to execute"),
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

func getSchemaObjectDefinitionTool(desc string) mcp.Tool {
	return mcp.NewTool("get_schema_object_definition",
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

func queryToolHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sqlStmt, ok := request.GetArguments()["sql"].(string)
	if !ok {
		return nil, errors.New("invalid or missing 'sql' parameter")
	}

	maxRows := 1000
	if maxRowsParam, ok := request.GetArguments()["max_rows"].(float64); ok && maxRowsParam > 0 {
		maxRows = int(maxRowsParam)
	}

	rows, err := db.QueryContext(ctx, sqlStmt)
	if err != nil {
		log.Printf("Query error: %v\n", err)
		return nil, fmt.Errorf("query execution failed: %v", err)
	}
	defer rows.Close()

	results, err := parseSQLRowsWithLimit(rows, maxRows)
	if err != nil {
		return nil, fmt.Errorf("result parsing failed: %v", err)
	}

	if len(results) == 0 {
		return mcp.NewToolResultText("Query executed successfully. No rows returned."), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Query results (%d rows):\n%v", len(results), results)), nil
}

func executeToolHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sqlStmt, ok := request.GetArguments()["sql"].(string)
	if !ok {
		return nil, errors.New("invalid or missing 'sql' parameter")
	}

	result, err := db.ExecContext(ctx, sqlStmt)
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

func listObjectsToolHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
	dbName := getCurrentDatabase()

	// Collect objects by type
	tables := getObjectsByType(ctx, "table")
	views := getObjectsByType(ctx, "view")
	procedures := getObjectsByType(ctx, "procedure")
	functions := getObjectsByType(ctx, "function")

	// Apply regex filter if provided
	if re != nil {
		tables = filterByRegex(tables, re)
		views = filterByRegex(views, re)
		procedures = filterByRegex(procedures, re)
		functions = filterByRegex(functions, re)
	}

	// Build output based on requested type
	var output strings.Builder

	switch objectType {
	case "table":
		output.WriteString(formatObjectList("table", tables))
	case "view":
		output.WriteString(formatObjectList("view", views))
	case "procedure":
		output.WriteString(formatObjectList("procedure", procedures))
	case "function":
		output.WriteString(formatObjectList("function", functions))
	case "all":
		output.WriteString(formatObjectList("table", tables))
		if len(views) > 0 {
			output.WriteString(formatObjectList("view", views))
		}
		if len(procedures) > 0 {
			output.WriteString(formatObjectList("procedure", procedures))
		}
		if len(functions) > 0 {
			output.WriteString(formatObjectList("function", functions))
		}
	}

	// Build context attachment for LLM
	var objectsSummary strings.Builder
	if len(tables) > 0 {
		objectsSummary.WriteString(fmt.Sprintf("table: %s", strings.Join(tables, ", ")))
	}
	if len(views) > 0 {
		if objectsSummary.Len() > 0 {
			objectsSummary.WriteString("\n")
		}
		objectsSummary.WriteString(fmt.Sprintf("view: %s", strings.Join(views, ", ")))
	}
	if len(procedures) > 0 {
		if objectsSummary.Len() > 0 {
			objectsSummary.WriteString("\n")
		}
		objectsSummary.WriteString(fmt.Sprintf("procedure: %s", strings.Join(procedures, ", ")))
	}
	if len(functions) > 0 {
		if objectsSummary.Len() > 0 {
			objectsSummary.WriteString("\n")
		}
		objectsSummary.WriteString(fmt.Sprintf("function: %s", strings.Join(functions, ", ")))
	}

	sqlDialect := getSQLDialectName()
	contextHint := fmt.Sprintf(llmContextHintTemplate, dbName, sqlDialect, objectsSummary.String())

	output.WriteString("\n\n")
	output.WriteString(contextHint)

	return mcp.NewToolResultText(output.String()), nil
}

func getSchemaObjectDefinitionToolHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	objectName, ok := request.GetArguments()["object_name"].(string)
	if !ok {
		return nil, errors.New("invalid or missing 'object_name' parameter")
	}
	objectName = sanitizeInput(objectName)

	// If object_type is specified, return only that type
	if objectType, ok := request.GetArguments()["object_type"].(string); ok && strings.TrimSpace(objectType) != "" {
		objectType = strings.ToLower(objectType)
		var result *mcp.CallToolResult
		var err error

		switch objectType {
		case "table":
			result, err = getTableDefinition(ctx, objectName)
		case "view":
			result, err = getViewDefinition(ctx, objectName)
		case "procedure":
			result, err = getProcedureDefinition(ctx, objectName)
		case "function":
			result, err = getFunctionDefinition(ctx, objectName)
		default:
			return nil, fmt.Errorf("unsupported object type: %s (use table, view, procedure, or function)", objectType)
		}

		if err != nil {
			return nil, err
		}
		return addDialectHint(result), nil
	}

	// If object_type is not specified, search all types
	var results []string
	objectTypes := []string{"table", "view", "procedure", "function"}

	for _, objType := range objectTypes {
		var result *mcp.CallToolResult
		var err error

		switch objType {
		case "table":
			result, err = getTableDefinition(ctx, objectName)
		case "view":
			result, err = getViewDefinition(ctx, objectName)
		case "procedure":
			result, err = getProcedureDefinition(ctx, objectName)
		case "function":
			result, err = getFunctionDefinition(ctx, objectName)
		}

		if err == nil && result != nil {
			results = append(results, result.Content[0].(mcp.TextContent).Text)
		}
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("object '%s' not found (searched: table, view, procedure, function)", objectName)
	}

	result := mcp.NewToolResultText(strings.Join(results, "\n\n"+strings.Repeat("=", 50)+"\n\n"))
	return addDialectHint(result), nil
}

// ============================================================
// Helper Functions for Object Listing
// ============================================================

func getObjectsByType(ctx context.Context, objectType string) []string {
	var objects []string

	switch dbDriver {
	case DriverMySQL:
		switch objectType {
		case "table":
			objects = queryStrings(ctx, `SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME`)
		case "view":
			objects = queryStrings(ctx, `SELECT TABLE_NAME FROM information_schema.VIEWS WHERE TABLE_SCHEMA = DATABASE() ORDER BY TABLE_NAME`)
		case "procedure":
			objects = queryStrings(ctx, `SELECT ROUTINE_NAME FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = DATABASE() AND ROUTINE_TYPE = 'PROCEDURE' ORDER BY ROUTINE_NAME`)
		case "function":
			objects = queryStrings(ctx, `SELECT ROUTINE_NAME FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = DATABASE() AND ROUTINE_TYPE = 'FUNCTION' ORDER BY ROUTINE_NAME`)
		}

	case DriverPostgres:
		switch objectType {
		case "table":
			objects = queryStrings(ctx, `SELECT schemaname || '.' || tablename FROM pg_catalog.pg_tables WHERE schemaname NOT IN ('pg_catalog', 'information_schema') ORDER BY schemaname, tablename`)
		case "view":
			objects = queryStrings(ctx, `SELECT schemaname || '.' || viewname FROM pg_catalog.pg_views WHERE schemaname NOT IN ('pg_catalog', 'information_schema') ORDER BY schemaname, viewname`)
		case "procedure":
			objects = queryStrings(ctx, `SELECT n.nspname || '.' || p.proname FROM pg_proc p JOIN pg_namespace n ON p.pronamespace = n.oid WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND p.prokind = 'p' ORDER BY p.proname`)
		case "function":
			objects = queryStrings(ctx, `SELECT n.nspname || '.' || p.proname FROM pg_proc p JOIN pg_namespace n ON p.pronamespace = n.oid WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND p.prokind IN ('f', 'a') ORDER BY p.proname`)
		}

	case DriverSQLite:
		switch objectType {
		case "table":
			objects = queryStrings(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
		case "view":
			objects = queryStrings(ctx, `SELECT name FROM sqlite_master WHERE type='view' ORDER BY name`)
		}

	case DriverMSSQL:
		switch objectType {
		case "table":
			objects = queryStrings(ctx, `SELECT TABLE_SCHEMA + '.' + TABLE_NAME FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_SCHEMA, TABLE_NAME`)
		case "view":
			objects = queryStrings(ctx, `SELECT TABLE_SCHEMA + '.' + TABLE_NAME FROM INFORMATION_SCHEMA.VIEWS ORDER BY TABLE_SCHEMA, TABLE_NAME`)
		case "procedure":
			objects = queryStrings(ctx, `SELECT ROUTINE_SCHEMA + '.' + ROUTINE_NAME FROM INFORMATION_SCHEMA.ROUTINES WHERE ROUTINE_TYPE = 'PROCEDURE' ORDER BY ROUTINE_SCHEMA, ROUTINE_NAME`)
		case "function":
			objects = queryStrings(ctx, `SELECT ROUTINE_SCHEMA + '.' + ROUTINE_NAME FROM INFORMATION_SCHEMA.ROUTINES WHERE ROUTINE_TYPE = 'FUNCTION' ORDER BY ROUTINE_SCHEMA, ROUTINE_NAME`)
		}
	}

	return objects
}

func queryStrings(ctx context.Context, query string) []string {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return results
		}
		results = append(results, s)
	}
	return results
}

func formatObjectList(label string, items []string) string {
	if len(items) == 0 {
		return ""
	}
	return fmt.Sprintf("%s: %s\n", label, strings.Join(items, ", "))
}

func filterByRegex(items []string, re *regexp.Regexp) []string {
	var filtered []string
	for _, item := range items {
		if re.MatchString(item) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func getCurrentDatabase() string {
	var dbName string
	switch dbDriver {
	case DriverMySQL:
		err := db.QueryRow(`SELECT DATABASE()`).Scan(&dbName)
		if err != nil {
			dbName = "unknown"
		}
	case DriverPostgres:
		err := db.QueryRow(`SELECT current_database()`).Scan(&dbName)
		if err != nil {
			dbName = "unknown"
		}
	case DriverSQLite:
		dbName = "main"
	case DriverMSSQL:
		err := db.QueryRow(`SELECT DB_NAME()`).Scan(&dbName)
		if err != nil {
			dbName = "unknown"
		}
	default:
		dbName = "unknown"
	}
	return dbName
}

func getDBVersion() string {
	var version string
	var err error

	switch dbDriver {
	case DriverMySQL:
		err = db.QueryRow(`SELECT VERSION()`).Scan(&version)
	case DriverPostgres:
		err = db.QueryRow(`SELECT VERSION()`).Scan(&version)
	case DriverSQLite:
		err = db.QueryRow(`SELECT SQLITE_VERSION()`).Scan(&version)
	case DriverMSSQL:
		err = db.QueryRow(`SELECT @@VERSION`).Scan(&version)
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

func getSQLDialectName() string {
	switch dbDriver {
	case DriverMySQL:
		return "MySQL"
	case DriverPostgres:
		return "PostgreSQL"
	case DriverSQLite:
		return "SQLite"
	case DriverMSSQL:
		return "SQL Server"
	default:
		return "SQL"
	}
}

func addDialectHint(result *mcp.CallToolResult) *mcp.CallToolResult {
	if len(result.Content) == 0 {
		return result
	}
	content := result.Content[0].(mcp.TextContent)
	dialect := getSQLDialectName()
	hint := fmt.Sprintf("\n\n[SQL Dialect: %s]", dialect)
	result.Content[0] = mcp.TextContent{
		Type: content.Type,
		Text: content.Text + hint,
	}
	return result
}

// ============================================================
// Driver-Specific Queries for Definitions
// ============================================================

// getTableDefinition returns table structure (columns, types, constraints)
func getTableDefinition(ctx context.Context, objectName string) (*mcp.CallToolResult, error) {
	query := ""

	switch dbDriver {
	case DriverMySQL:
		query = fmt.Sprintf(`
			SELECT COLUMN_NAME, DATA_TYPE, IS_NULLABLE, COLUMN_DEFAULT, COLUMN_COMMENT
			FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '%s'
			ORDER BY ORDINAL_POSITION`, objectName)

	case DriverPostgres:
		query = fmt.Sprintf(`
			SELECT column_name, data_type, is_nullable, column_default
			FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = '%s'
			ORDER BY ordinal_position`, objectName)

	case DriverSQLite:
		query = fmt.Sprintf(`PRAGMA table_info(%s)`, objectName)

	case DriverMSSQL:
		query = fmt.Sprintf(`
			SELECT COLUMN_NAME, DATA_TYPE, IS_NULLABLE, COLUMN_DEFAULT
			FROM INFORMATION_SCHEMA.COLUMNS
			WHERE TABLE_NAME = '%s'
			ORDER BY ORDINAL_POSITION`, objectName)
	}

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		log.Printf("Get table definition error: %v\n", err)
		return nil, fmt.Errorf("failed to get table definition: %v", err)
	}
	defer rows.Close()

	results, err := parseSQLRows(rows)
	if err != nil {
		return nil, fmt.Errorf("failed to parse table definition: %v", err)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("table '%s' not found", objectName)
	}

	indexInfo := getIndexInfo(ctx, objectName)

	var output strings.Builder
	output.WriteString(fmt.Sprintf("Table definition for '%s':\n", objectName))
	output.WriteString(formatAsTable(results))
	if indexInfo != "" {
		output.WriteString("\nIndexes:\n")
		output.WriteString(indexInfo)
	}

	return mcp.NewToolResultText(output.String()), nil
}

func getViewDefinition(ctx context.Context, objectName string) (*mcp.CallToolResult, error) {
	query := ""

	switch dbDriver {
	case DriverMySQL:
		query = fmt.Sprintf(`SELECT VIEW_DEFINITION FROM information_schema.VIEWS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '%s'`, objectName)

	case DriverPostgres:
		query = fmt.Sprintf(`SELECT definition FROM pg_views WHERE schemaname = 'public' AND viewname = '%s'`, objectName)

	case DriverSQLite:
		query = fmt.Sprintf(`SELECT SQL FROM sqlite_master WHERE TYPE='view' AND NAME='%s'`, objectName)

	case DriverMSSQL:
		query = fmt.Sprintf(`SELECT VIEW_DEFINITION FROM INFORMATION_SCHEMA.VIEWS WHERE TABLE_NAME = '%s'`, objectName)
	}

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		log.Printf("Get view definition error: %v\n", err)
		return nil, fmt.Errorf("failed to get view definition: %v", err)
	}
	defer rows.Close()

	var definition string
	for rows.Next() {
		if err := rows.Scan(&definition); err != nil {
			return nil, fmt.Errorf("error scanning view definition: %v", err)
		}
	}

	if definition == "" {
		return nil, fmt.Errorf("view '%s' not found", objectName)
	}

	return mcp.NewToolResultText(fmt.Sprintf("View definition for '%s':\n\n%s", objectName, definition)), nil
}

func getProcedureDefinition(ctx context.Context, objectName string) (*mcp.CallToolResult, error) {
	query := ""

	switch dbDriver {
	case DriverMySQL:
		query = fmt.Sprintf(`SELECT ROUTINE_DEFINITION FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = DATABASE() AND ROUTINE_NAME = '%s' AND ROUTINE_TYPE = 'PROCEDURE'`, objectName)

	case DriverPostgres:
		query = fmt.Sprintf(`
			SELECT pg_get_functiondef(p.oid)
			FROM pg_proc p
			JOIN pg_namespace n ON p.pronamespace = n.oid
			WHERE n.nspname = 'public' AND p.proname = '%s' AND p.prokind = 'p'`, objectName)

	case DriverSQLite:
		return nil, errors.New("SQLite does not support stored procedures")

	case DriverMSSQL:
		query = fmt.Sprintf(`SELECT OBJECT_DEFINITION(OBJECT_ID('%s'))`, objectName)
	}

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		log.Printf("Get procedure definition error: %v\n", err)
		return nil, fmt.Errorf("failed to get procedure definition: %v", err)
	}
	defer rows.Close()

	var definition string
	for rows.Next() {
		if err := rows.Scan(&definition); err != nil {
			return nil, fmt.Errorf("error scanning procedure definition: %v", err)
		}
	}

	if definition == "" {
		return nil, fmt.Errorf("procedure '%s' not found", objectName)
	}

	return mcp.NewToolResultText(fmt.Sprintf("Procedure definition for '%s':\n\n%s", objectName, definition)), nil
}

func getFunctionDefinition(ctx context.Context, objectName string) (*mcp.CallToolResult, error) {
	query := ""

	switch dbDriver {
	case DriverMySQL:
		query = fmt.Sprintf(`SELECT ROUTINE_DEFINITION FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = DATABASE() AND ROUTINE_NAME = '%s' AND ROUTINE_TYPE = 'FUNCTION'`, objectName)

	case DriverPostgres:
		query = fmt.Sprintf(`
			SELECT pg_get_functiondef(p.oid)
			FROM pg_proc p
			JOIN pg_namespace n ON p.pronamespace = n.oid
			WHERE n.nspname = 'public' AND p.proname = '%s'`, objectName)

	case DriverSQLite:
		return nil, errors.New("SQLite does not support user-defined functions")

	case DriverMSSQL:
		query = fmt.Sprintf(`SELECT OBJECT_DEFINITION(OBJECT_ID('%s'))`, objectName)
	}

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		log.Printf("Get function definition error: %v\n", err)
		return nil, fmt.Errorf("failed to get function definition: %v", err)
	}
	defer rows.Close()

	var definition string
	for rows.Next() {
		if err := rows.Scan(&definition); err != nil {
			return nil, fmt.Errorf("error scanning function definition: %v", err)
		}
	}

	if definition == "" {
		return nil, fmt.Errorf("function '%s' not found", objectName)
	}

	return mcp.NewToolResultText(fmt.Sprintf("Function definition for '%s':\n\n%s", objectName, definition)), nil
}

func getIndexInfo(ctx context.Context, tableName string) string {
	query := ""

	switch dbDriver {
	case DriverMySQL:
		query = fmt.Sprintf(`SELECT INDEX_NAME, COLUMN_NAME, NON_UNIQUE, SEQ_IN_INDEX FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '%s' ORDER BY INDEX_NAME, SEQ_IN_INDEX`, tableName)

	case DriverPostgres:
		query = fmt.Sprintf(`SELECT indexname, indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename = '%s'`, tableName)

	case DriverSQLite:
		query = fmt.Sprintf(`PRAGMA index_list(%s)`, tableName)

	case DriverMSSQL:
		query = fmt.Sprintf(`SELECT i.name AS index_name, c.name AS column_name FROM sys.indexes i JOIN sys.index_columns ic ON i.object_id = ic.object_id AND i.index_id = ic.index_id JOIN sys.columns c ON ic.object_id = c.object_id AND ic.column_id = c.column_id WHERE i.object_id = OBJECT_ID('%s') ORDER BY i.name, ic.key_ordinal`, tableName)
	}

	if query == "" {
		return ""
	}

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return ""
	}
	defer rows.Close()

	results, err := parseSQLRows(rows)
	if err != nil || len(results) == 0 {
		return "  No indexes defined"
	}

	return formatAsTable(results)
}

func parseSQLRowsWithLimit(rows *sql.Rows, maxRows int) ([]map[string]interface{}, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var results []map[string]interface{}
	count := 0
	for rows.Next() {
		if maxRows > 0 && count >= maxRows {
			break
		}
		values := make([]interface{}, len(cols))
		pointers := make([]interface{}, len(cols))
		for i := range values {
			pointers[i] = &values[i]
		}

		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}

		row := make(map[string]interface{})
		for i, col := range cols {
			row[col] = values[i]
		}
		results = append(results, row)
		count++
	}

	return results, nil
}

func parseSQLRows(rows *sql.Rows) ([]map[string]interface{}, error) {
	return parseSQLRowsWithLimit(rows, 0)
}

func formatAsTable(results []map[string]interface{}) string {
	if len(results) == 0 {
		return ""
	}

	var sb strings.Builder

	var columns []string
	for k := range results[0] {
		columns = append(columns, k)
	}

	sb.WriteString(strings.Join(columns, " | "))
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("-", len(strings.Join(columns, " | "))))
	sb.WriteString("\n")

	for _, row := range results {
		var values []string
		for _, col := range columns {
			values = append(values, fmt.Sprintf("%v", row[col]))
		}
		sb.WriteString(strings.Join(values, " | "))
		sb.WriteString("\n")
	}

	return sb.String()
}

func sanitizeInput(input string) string {
	return strings.ReplaceAll(input, "'", "''")
}
