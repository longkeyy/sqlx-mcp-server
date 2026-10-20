# SQL MCP Server

A Model Context Protocol (MCP) server that exposes SQL databases as MCP tools. It supports MySQL-compatible, PostgreSQL-compatible, SQLite, SQL Server, and ClickHouse databases, with Streamable HTTP, SSE, and stdio transports.

The SQLite driver is `modernc.org/sqlite`, so SQLite builds can run with `CGO_ENABLED=0` for simpler cross-compilation and container builds.

## Features

- Query SQL databases through MCP tools.
- Supports MySQL, MariaDB, Percona, TiDB, OceanBase, Apache Doris, StarRocks, PostgreSQL, CockroachDB, Greenplum, openGauss, MogDB, SQLite, SQL Server, and ClickHouse DSNs.
- Provides `query`, `execute`, `list_objects`, and `describe_object` tools.
- Supports Streamable HTTP at `/mcp`.
- Supports SSE at `/sse` plus `/message`.
- Supports multiple databases in one process with routes such as `/db/reporting/mcp`.
- Docker-friendly environment variable configuration.
- Per-database `database/sql` connection pools.
- Pure-Go SQLite support for CGO-free builds.

## Quick Start

Run with SQLite:

```bash
DB_URL=sqlite:///tmp/example.db \
LISTEN=:8088 \
go run ./cmd/sql-mcp
```

The Streamable HTTP MCP endpoint is:

```text
http://localhost:8088/mcp
```

Run with stdio:

```bash
go run ./cmd/sql-mcp --dsn sqlite:///tmp/example.db
```

## Docker

The image does not contain a default DSN. Supply one with `DB_URL` or one or more `DB_{ID}_URL` variables; without it the container exits with a "DSN is required" error instead of silently opening a scratch database.

Run the Docker Hub image with a persistent SQLite file:

```bash
mkdir -p data
docker run --rm -p 8088:8088 \
  -e DB_URL=sqlite:///app/data/sql-mcp.db \
  -e DB_NAME=sql-mcp \
  -e LISTEN=:8088 \
  -v "$PWD/data:/app/data" \
  longkeyy/sqlx-mcp-server:latest
```

The Dockerfile supports multi-architecture images for `linux/amd64` and `linux/arm64`.

Start the default Docker Compose service:

```bash
mkdir -p data
docker compose up -d
```

By default, `docker-compose.yml` maps:

```text
http://localhost:8088/mcp
```

The compose file defaults `DB_URL` to `sqlite:///app/data/sql-mcp.db` and mounts `./data` to `/app/data`, so the database survives container restarts.

To use another database, export `DB_URL` before starting Compose or put it in `.env`:

```bash
DB_URL=postgres://user:pass@host:5432/app docker compose up -d
```

Set `DB_URL=` explicitly (empty value) to drop the SQLite default and serve only `DB_{ID}_URL` databases. Compose still forwards the legacy `DATABASE_URL`, `INSTANCE_NAME`, and `DEFAULT_DATABASE` variables to their current names.

Publish a multi-architecture image:

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -t longkeyy/sqlx-mcp-server:latest \
  --push .
```

## Configuration

Configuration can be provided by command-line flags, environment variables, or a `.env` file.

CLI flags have priority over environment variables.

For local development, start from the example file:

```bash
cp .env.example .env
```

| Option | Environment | Default | Description |
| --- | --- | --- | --- |
| `--dsn` | `DB_URL` | none | Default database URL-style DSN. |
| `--name` | `DB_NAME` | generated | MCP server display/logging name. |
| none | `DB_MEMO` | empty | Short purpose note included in MCP tool descriptions. |
| `--listen` | `LISTEN` | empty | Network listen address for HTTP/SSE, such as `:8088` or `127.0.0.1:8088`. Empty uses stdio. |
| `--auth-token` | `AUTH_TOKEN` | empty | Optional token for HTTP/SSE authentication. Empty disables built-in authentication. |

Named database instances use the same pattern: `DB_{ID}_URL`, optional `DB_{ID}_NAME`, and optional `DB_{ID}_MEMO`.

### Legacy environment variable names

Variables from releases before the `DB_*` rename are still accepted, so an existing deployment does not silently lose a database after an upgrade. Every use logs a deprecation warning on stderr.

| Deprecated | Current |
| --- | --- |
| `DATABASE_URL` | `DB_URL` |
| `DATABASE_{ID}_URL` | `DB_{ID}_URL` |
| `DATABASE_{ID}_NAME`, `DATABASE_{ID}_MEMO` | `DB_{ID}_NAME`, `DB_{ID}_MEMO` |
| `DATABASE_MAX_OPEN_CONNS`, `DATABASE_MAX_IDLE_CONNS` | `DB_MAX_OPEN_CONNS`, `DB_MAX_IDLE_CONNS` |
| `DATABASE_{ID}_MAX_OPEN_CONNS`, `DATABASE_{ID}_MAX_IDLE_CONNS` | `DB_{ID}_MAX_OPEN_CONNS`, `DB_{ID}_MAX_IDLE_CONNS` |
| `INSTANCE_NAME` | `DB_NAME` |
| `DEFAULT_DATABASE` | `DEFAULT_DB` |

Only the deprecated name set: it is used, with a warning. Both set to the same value: the current name is used, with a warning. Both set to different values: startup fails with an error naming both keys; DSNs are never printed. `INSTANCE_NAME` is the exception and still wins over `DB_NAME` for compatibility, with a warning. Per-database display settings (`*_NAME`, `*_MEMO`) prefer the current key on a conflict and warn.

Warnings are written to stderr, so stdio output stays valid JSON-RPC.

### DSN Formats

```text
mysql://user:pass@host:3306/dbname
mariadb://user:pass@host:3306/dbname
percona://user:pass@host:3306/dbname
tidb://user:pass@host:4000/dbname
oceanbase://user:pass@host:3306/dbname
doris://user:pass@host:9030/dbname
starrocks://user:pass@host:9030/dbname
postgres://user:pass@host:5432/dbname
postgresql://user:pass@host:5432/dbname
cockroach://user:pass@host:26257/dbname
greenplum://user:pass@host:5432/dbname
opengauss://user:pass@host:5432/dbname
mogdb://user:pass@host:5432/dbname
sqlite:///path/to/db.sqlite
sqlserver://user:pass@host:1433/dbname
clickhouse://user:pass@host:9000/dbname
```

MySQL-compatible databases reuse the MySQL Go driver internally while keeping their own dialect names in tool descriptions and results. PostgreSQL-compatible databases reuse the PostgreSQL driver in the same way. ClickHouse uses the official ClickHouse Go driver.

DSNs are never logged in clear text: the startup log masks the password and any credential query parameter (`password`, `token`, `secret`, and similar), and startup errors are scrubbed of the DSN, its password, and those values before they are printed.

## Multi-Database Routing

Use `DB_{ID}_URL` variables to expose multiple databases from one process:

```env
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

The `{ID}` part becomes the lowercase route id. For example, `DB_REPORTING_URL` becomes `reporting`.

Routes:

```text
/mcp                    -> default database, Streamable HTTP
/db/app/mcp             -> app database, Streamable HTTP
/db/reporting/mcp       -> reporting database, Streamable HTTP

/sse                    -> default database, SSE stream
/message                -> default database, SSE message endpoint
/db/app/sse             -> app database, SSE stream
/db/app/message         -> app database, SSE message endpoint
```

Set `LISTEN` to expose Streamable HTTP and SSE routes on the same port. Leave it empty to use stdio.

Optional display names and memos:

```env
DB_APP_NAME=app-db
DB_APP_MEMO=Primary application database
DB_REPORTING_NAME=reporting-db
DB_REPORTING_MEMO=Analytics and BI replica
```

`DB_MEMO` and `DB_{ID}_MEMO` are included in MCP tool descriptions so agents can distinguish similarly named database instances before they call a tool.

If `DEFAULT_DB` is not set, `DB_URL` is used as the default when present. Otherwise, the first configured database id in sorted order is used; when more than one database is configured this logs a warning naming the chosen id.

## Connection Pool Settings

Each configured database has its own `database/sql` connection pool.

Global defaults:

```env
DB_MAX_OPEN_CONNS=25
DB_MAX_IDLE_CONNS=1
```

Per-database overrides:

```env
DB_APP_MAX_OPEN_CONNS=10
DB_APP_MAX_IDLE_CONNS=1
```

The default idle connection count is intentionally low because MCP SQL access is often low-frequency.

## Authentication

Set `AUTH_TOKEN` to protect HTTP/SSE endpoints:

```env
AUTH_TOKEN=change-me
LISTEN=127.0.0.1:8088
```

Preferred clients should send:

```http
Authorization: Bearer change-me
```

For clients that cannot send custom headers, a query parameter fallback is also accepted:

```text
http://127.0.0.1:8088/mcp?token=change-me
http://127.0.0.1:8088/sse?token=change-me
```

`?token=` is less private because URLs can appear in logs and shell history. Prefer Bearer headers when the client supports them.

### Client Examples

Codex:

```toml
[mcp_servers.sql-mcp]
url = "http://127.0.0.1:8088/mcp"
bearer_token_env_var = "SQL_MCP_AUTH_TOKEN"
```

Claude Code:

```bash
claude mcp add --transport http sql-mcp http://127.0.0.1:8088/mcp \
  --header "Authorization: Bearer ${SQL_MCP_AUTH_TOKEN}"
```

opencode:

```json
{
  "mcp": {
    "sql-mcp": {
      "type": "remote",
      "url": "http://127.0.0.1:8088/mcp",
      "oauth": false,
      "headers": {
        "Authorization": "Bearer {env:SQL_MCP_AUTH_TOKEN}"
      }
    }
  }
}
```

Fallback URL form:

```text
http://127.0.0.1:8088/mcp?token=change-me
http://127.0.0.1:8088/sse?token=change-me
```

## MCP Tools

### `query`

Executes read-oriented SQL and returns compact JSON with columns and row arrays.

Allowed leading statements include `SELECT`, `WITH`, `EXPLAIN`, `SHOW`, and `DESCRIBE`. Mutating statements are rejected by a guardrail.

Arguments:

| Name | Required | Description |
| --- | --- | --- |
| `sql` | yes | Read-oriented SQL query. |
| `max_rows` | no | Maximum rows to return. Defaults to `1000`. |

### `execute`

Executes write or DDL SQL, including `INSERT`, `UPDATE`, `DELETE`, `CREATE`, `ALTER`, and `DROP`.

Arguments:

| Name | Required | Description |
| --- | --- | --- |
| `sql` | yes | SQL statement to execute. |

### `list_objects`

Lists database objects as structured JSON.

Arguments:

| Name | Required | Description |
| --- | --- | --- |
| `type` | no | `table`, `view`, `procedure`, `function`, or `all`. |
| `pattern` | no | Case-insensitive regex filter. |

### `describe_object`

Describes a table, view, procedure, or function.

Arguments:

| Name | Required | Description |
| --- | --- | --- |
| `object_name` | yes | Object name, optionally schema-qualified. |
| `object_type` | no | `table`, `view`, `procedure`, or `function`. |

## Security Notes

The `query` tool includes a read-oriented SQL guardrail, but it is not a complete security boundary. Use database users with the least privileges required for your deployment.

Remote HTTP deployments should be protected by `AUTH_TOKEN`, a trusted reverse proxy, VPN, or network policy.

## Design

SQL MCP Server is designed to expose configured databases through small, explicit MCP tools. Tool responses should contain database facts and structured metadata directly, not hidden prompt-like instructions.

### Runtime Model

The server creates one runtime instance per configured database. Each instance owns:

- A parsed URL-style DSN.
- A dedicated `database/sql` connection pool.
- A database dialect.
- An MCP server with tool descriptions generated from that database, including the configured memo when present.

For multi-database deployments, the URL path selects the database instance. Tool calls do not include a database selector argument, which keeps each MCP session scoped to one database and reduces the chance of an LLM accidentally querying the wrong database.

### Tool Contract

The public tool surface is intentionally small:

- `query` is for read-oriented SQL and returns compact structured result sets.
- `execute` is for writes, DDL, and stored procedure calls.
- `list_objects` lists tables, views, procedures, and functions.
- `describe_object` returns object metadata or definitions.

`describe_object` is used instead of a longer schema-specific name because it reflects the actual behavior across object types. Tables return structural metadata such as columns and indexes. Views, procedures, and functions return definition text when the database exposes it.

### Result Format

Tool results are JSON text. Query results use a compact table shape:

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

This preserves column order, represents `null` clearly, and avoids repeating field names for every row.

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

Object descriptions use semantic JSON. Table descriptions return columns and indexes; other supported object types return definitions when available:

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

### Read Query Guard

The `query` tool enforces a read-oriented contract before sending SQL to the database.

The guard:

- Strips or ignores comments before classification.
- Rejects multiple statements.
- Allows read-oriented leading forms such as `SELECT`, `WITH`, `EXPLAIN`, `SHOW`, and `DESCRIBE`.
- Rejects known mutating forms such as `INSERT`, `UPDATE`, `DELETE`, `CREATE`, `ALTER`, `DROP`, `TRUNCATE`, `MERGE`, `CALL`, `EXEC`, `GRANT`, `REVOKE`, and unsafe SQLite pragmas.

This is a guardrail, not a complete sandbox. SQL dialects can expose side effects through functions, extensions, or special statement forms. Production deployments should still use least-privilege database accounts.

### Metadata Lookup

Metadata lookup avoids treating object names as arbitrary SQL fragments.

The preferred behavior is:

- Parse qualified object names into `schema` and `name` where the dialect supports schemas.
- Use parameterized metadata queries wherever the database driver supports them.
- Quote identifiers only when an identifier must appear in identifier position, such as SQLite PRAGMA statements.
- Return schema-qualified object names from `list_objects` for databases that support schemas, and accept those names in `describe_object`.

### LLM Contract

Tool responses should contain explicit fields such as:

- `database`
- `dialect`
- `objects`
- `definition`
- `truncated`

If a client wants an LLM to inspect an object before querying it, that policy belongs in the client or tool description, not as hidden instructions embedded in data responses.

### Code Organization

The project uses a conventional Go binary plus internal package layout:

- `cmd/sql-mcp`: executable entrypoint, transport selection, and driver registration.
- `internal/config`: command-line and environment configuration.
- `internal/dsn`: URL-style DSN parsing and driver mapping.
- `internal/server`: per-database MCP server setup, tool handlers, metadata queries, result encoding, and read-query validation.

If the implementation grows, these boundaries should move into small internal packages or explicit types rather than broad package-level state.

## Development

Run tests:

```bash
go test ./...
```

Verify CGO-free SQLite support:

```bash
CGO_ENABLED=0 go test ./...
```

Build for Linux ARM64:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/sql-mcp-linux-arm64 ./cmd/sql-mcp
```

Build all release targets from the Makefile:

```bash
make release
```

## License

This project is licensed under the MIT License. See [LICENSE](LICENSE).
