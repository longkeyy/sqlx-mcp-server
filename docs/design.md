# SQL MCP Server Design

## Goal

This server exposes a single configured SQL database as an MCP server. The tool contract should pass database facts to an LLM in the most direct and least ambiguous form possible: small tool names, explicit parameters, stable structured output, and no hidden prompt-like instructions inside data responses.

## Tool Surface

The server registers four MCP tools:

- `query`: executes a read-oriented SQL query and returns a compact structured result set.
- `execute`: executes DML, DDL, or stored procedure calls.
- `list_objects`: lists database objects by type.
- `describe_object`: describes one database object.

`describe_object` is the preferred name because it matches what the tool actually does across object types. For tables, it returns structural metadata such as columns and indexes. For views, procedures, and functions, it returns the available definition text. The previous name, `get_schema_object_definition`, is longer and suggests that every object type has the same kind of definition, which is not true.

The supported object types are:

- `table`
- `view`
- `procedure`
- `function`

## Runtime Model

The process connects to one database at startup from a URL-style DSN. The active database connection and dialect should be represented by an explicit server or tool context rather than broad package-level state when the code grows beyond the current single-binary shape.

`DATABASE_URL` is the source of truth for the legacy/default target database and includes the database name or SQLite file path. `INSTANCE_NAME` only controls the MCP server display/logging name. `DB_NAME` is accepted only as a legacy alias for the instance name and should not be used in new deployments.

Multiple databases can be configured with Docker-friendly environment variables:

```text
DATABASE_TCM_URL=sqlite:///app/data/tcm_unified.db
DATABASE_HERB_URL=sqlite:///app/data/herb.db
DATABASE_REPORTING_URL=postgres://user:pass@host:5432/reporting
DEFAULT_DATABASE=TCM
```

The middle token in `DATABASE_{ID}_URL` becomes the stable lowercase route id. `DATABASE_{ID}_NAME` is optional and controls the MCP server display name for that database. If `DEFAULT_DATABASE` is not set, `DATABASE_URL` is the default when present; otherwise the first configured id in sorted order is used.

HTTP paths:

- Streamable HTTP: `/mcp` for the default database and `/db/{id}/mcp` for a named database.
- SSE: `/sse` and `/message` for the default database, plus `/db/{id}/sse` and `/db/{id}/message` for named databases.

Each configured database has its own `database/sql` connection pool. Defaults are tuned for low-frequency MCP use: up to 25 open connections for bursts, but only 1 idle connection retained per database.

Pool defaults can be set globally:

```text
DATABASE_MAX_OPEN_CONNS=25
DATABASE_MAX_IDLE_CONNS=1
```

Individual databases can override them:

```text
DATABASE_TCM_MAX_OPEN_CONNS=10
DATABASE_TCM_MAX_IDLE_CONNS=1
```

Supported database drivers:

- MySQL
- PostgreSQL
- SQLite through the pure-Go `modernc.org/sqlite` driver, with `CGO_ENABLED=0` builds supported for cross-compilation.
- SQL Server

Supported MCP transports:

- stdio
- SSE
- streamable HTTP

## Result Format

Tool results should be valid JSON text. JSON gives the LLM a standard parser-friendly format while avoiding markdown table formatting and custom CSV/TSV escaping rules.

Query results use a compact table shape:

```json
{
  "format": "columnar_rows",
  "columns": ["id", "name", "created_at"],
  "rows": [
    [1, "alice", "2026-05-17T10:00:00Z"],
    [2, "bob", null]
  ],
  "row_count": 2,
  "truncated": false,
  "max_rows": 1000,
  "dialect": "PostgreSQL"
}
```

This avoids repeating field names for every row, preserves column order, represents nulls clearly, and does not require the caller to understand Go's map formatting.

Object listings use object records:

```json
{
  "database": "main",
  "dialect": "SQLite",
  "objects": [
    {"type": "table", "name": "prescriptions"},
    {"type": "view", "name": "active_prescriptions"}
  ]
}
```

Object descriptions use semantic JSON. Table descriptions return columns and indexes. View, procedure, and function descriptions return definition text when available:

```json
{
  "object": {"type": "table", "schema": "public", "name": "users"},
  "columns": [
    {"name": "id", "type": "integer", "nullable": false, "default": null},
    {"name": "name", "type": "text", "nullable": true, "default": null}
  ],
  "indexes": [
    {"name": "users_pkey", "columns": ["id"], "unique": true}
  ],
  "dialect": "PostgreSQL"
}
```

## Read Query Guard

The `query` tool should enforce its read-oriented contract before sending SQL to the database. A regular-expression based check is acceptable as a first guardrail, but it is not a complete security boundary.

The guard should:

- Strip or ignore comments before classification.
- Reject multiple statements.
- Allow only read-oriented leading forms such as `SELECT`, `WITH`, `EXPLAIN`, `SHOW`, and `DESCRIBE`.
- Reject known mutating forms such as `INSERT`, `UPDATE`, `DELETE`, `CREATE`, `ALTER`, `DROP`, `TRUNCATE`, `MERGE`, `CALL`, `EXEC`, `GRANT`, `REVOKE`, and dialect-specific unsafe pragmas.

The database account should still be configured with the least privileges required by the deployment, because SQL dialects can expose side effects through functions, extensions, or special statement forms.

## Metadata Lookup

Metadata lookup should avoid treating object names as arbitrary SQL fragments. The preferred shape is:

- Parse a qualified object name into `schema` and `name` where the dialect supports schemas.
- Use parameterized metadata queries wherever the database driver supports them.
- Quote identifiers only when an identifier must appear in identifier position, such as SQLite PRAGMA statements.
- Return schema-qualified object names from `list_objects` for databases that support schemas, and accept the same names in `describe_object`.

## LLM Contract

Tool responses should contain facts and explicit metadata, not hidden instructions. Instead of embedding XML-like prompt attachments, responses should carry fields such as:

- `database`
- `dialect`
- `objects`
- `definition_available`
- `description`
- `truncated`

If the model should inspect an object before querying it, that belongs in the tool description or client policy, not as concealed instructions inside a data response.

## Code Organization

The current single-package layout is acceptable for a small binary, but the code should keep clear internal boundaries:

- configuration and DSN parsing
- MCP server setup
- tool handlers
- dialect-specific metadata queries
- result encoding
- read-query validation

When the implementation grows, these boundaries should move into small internal packages or explicit types rather than more global helper functions.
