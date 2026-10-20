# SQL MCP Server Design

## Goal

This server exposes configured SQL databases as MCP servers. The tool contract should pass database facts to an LLM in the most direct and least ambiguous form possible: small tool names, explicit parameters, stable structured output, and no hidden prompt-like instructions inside data responses.

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

The process connects to configured databases at startup from URL-style DSNs. Each active database connection and dialect is represented by an explicit server instance instead of broad package-level state. DSNs are credentials: the startup log prints them with the password and credential query parameters masked, parse errors report only that the URL is malformed, and connection errors are scrubbed of the DSN, its password, and credential query values before they are logged. Connection strings reach the drivers unchanged; masking applies only to output.

`DB_URL` is the source of truth for the default target database and includes the database name or SQLite file path. `DB_NAME` controls the MCP server display/logging name for that default database. `DB_MEMO` is an optional short purpose note included in MCP tool descriptions.

Memos reach the model inside a sentence, so configuration normalizes them: control characters and line breaks become spaces, runs of whitespace collapse, and the result is capped at 200 runes with an ellipsis. A warning names the variable that supplied the value when anything changes. Tool descriptions render the memo with Go string quoting, so embedded quotes and backslashes stay data rather than punctuation.

Without `DB_URL` the default database is one of the named `DB_{ID}_URL` databases, so `DB_NAME` and `DB_MEMO` describe that database too. They apply when `DEFAULT_DB` selects it or when it is the only database; `DB_{ID}_NAME` and `DB_{ID}_MEMO` take precedence. When several databases exist and the default was chosen implicitly, the global values cannot be attributed to any of them, so they are reported as unused and a warning names the key that would fix it.

Neither the binary nor the Docker image has a built-in DSN; both refuse to start without one. Only the Compose file supplies a default: a persistent SQLite file under the mounted `./data` directory.

Multiple databases can be configured with Docker-friendly environment variables:

```text
DB_APP_URL=sqlite:///app/data/app.db
DB_APP_NAME=app-db
DB_APP_MEMO=Primary application database
DB_REPORTING_URL=postgres://user:pass@host:5432/reporting
DB_REPORTING_NAME=reporting-db
DB_REPORTING_MEMO=Analytics and BI replica
DB_EVENTS_URL=clickhouse://user:pass@clickhouse:9000/events
DB_OB_URL=oceanbase://user:pass@oceanbase:3306/app
DB_ROACH_URL=cockroach://user:pass@roach:26257/app
DEFAULT_DB=APP
```

The middle token in `DB_{ID}_URL` becomes the stable lowercase route id, matched case-insensitively across the `DB_{ID}_*` keys, so `DB_APP_URL`, `DB_app_NAME` and `DB_aPp_MAX_OPEN_CONNS` configure one database. Duplicate spellings of an id with equal trimmed values are accepted with a warning; different values fail startup with an error naming both keys. Settings for an id without a URL warn instead of being silently ignored, and `DB_DEFAULT_NAME`/`DB_DEFAULT_MEMO` warn because the default database uses the global `DB_NAME`/`DB_MEMO`; `DB_DEFAULT_MAX_*` does apply to the default database. A `DB_{ID}_URL` set to an `http(s)://` address is skipped with a warning: such keys are usually API or health-check settings that share the prefix, and they should not stop the process. Only http(s) is filtered; every other scheme is parsed at startup so a typo still fails. `DB_{ID}_NAME` is optional and controls the MCP server display name for that database. `DB_{ID}_MEMO` is optional and gives agents a plain-language purpose for that database. If `DEFAULT_DB` is not set, `DB_URL` is the default when present; otherwise the first configured id in sorted order is used, which is logged as a warning when several databases are configured.

Names from before the `DB_*` rename are kept as deprecated aliases (`DATABASE_URL`, `DATABASE_{ID}_URL`, `DATABASE_{ID}_NAME`, `DATABASE_{ID}_MEMO`, `DATABASE_MAX_*`, `DATABASE_{ID}_MAX_*`, `INSTANCE_NAME`, `DEFAULT_DATABASE`) so an upgrade does not silently drop configuration. Resolution treats blank values as unset and compares aliases after trimming: a value present only under the deprecated name is used with a warning, equal duplicates warn, and conflicting non-display values fail startup with an error naming both keys but never the values, since values may contain credentials. Display-only settings (server and database names, memos) resolve a conflict in favor of the current key with a warning; `INSTANCE_NAME` keeps winning over `DB_NAME` for compatibility. Warnings are written to stderr so stdio stdout carries only JSON-RPC.

When `LISTEN` is set to a network address such as `:8088` or `127.0.0.1:8088`, the process starts an HTTP server and exposes these paths:

- Streamable HTTP: `/mcp` for the default database and `/db/{id}/mcp` for a named database.
- SSE: `/sse` and `/message` for the default database, plus `/db/{id}/sse` and `/db/{id}/message` for named databases.

Each configured database has its own `database/sql` connection pool. Defaults are tuned for low-frequency MCP use: up to 25 open connections for bursts, but only 1 idle connection retained per database.

Pool defaults can be set globally:

```text
DB_MAX_OPEN_CONNS=25
DB_MAX_IDLE_CONNS=1
```

Individual databases can override them:

```text
DB_APP_MAX_OPEN_CONNS=10
DB_APP_MAX_IDLE_CONNS=1
```

Limits resolve per database as per-id key, then global key, then built-in default, and validation runs on the resolved pair. A per-id idle limit may therefore exceed the built-in open limit when `DB_MAX_OPEN_CONNS` raises it. An idle limit above the effective open limit fails startup with an error naming the database and the key, or the built-in default, that supplied each side.

Supported database drivers:

- MySQL
- MariaDB, Percona, OceanBase, TiDB, Apache Doris, and StarRocks through MySQL-compatible drivers/protocols.
- PostgreSQL
- CockroachDB, Greenplum, openGauss, and MogDB through PostgreSQL-compatible drivers/protocols.
- SQLite through the pure-Go `modernc.org/sqlite` driver, with `CGO_ENABLED=0` builds supported for cross-compilation.
- SQL Server
- ClickHouse through the official ClickHouse Go driver.

If `LISTEN` is empty, the process uses stdio instead of starting a network listener.

HTTP/SSE deployments can set `AUTH_TOKEN` to require either `Authorization: Bearer <token>` or `?token=<token>` on MCP endpoints. Query-token authentication exists as a client compatibility fallback; Bearer headers are preferred.

Supported MCP transports:

- stdio
- SSE
- streamable HTTP
- combined Streamable HTTP and SSE on one network listener with `LISTEN`

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

The project follows the usual Go binary plus internal package layout:

- `cmd/sql-mcp`: executable entrypoint, transport selection, and driver registration.
- `internal/config`: command-line and environment configuration, including multi-database and pool settings.
- `internal/dsn`: URL-style DSN parsing and protocol-compatible driver mapping.
- `internal/server`: per-database MCP server setup, tool handlers, dialect-specific metadata queries, result encoding, and read-query validation.

New shared implementation should stay under `internal/` unless it is intentionally exported as a public Go API.
