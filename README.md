# SQL MCP Server

A Model Context Protocol (MCP) server that exposes SQL databases as MCP tools. It supports MySQL-compatible, PostgreSQL-compatible, SQLite, SQL Server, and ClickHouse databases, with Streamable HTTP, SSE, and stdio transports.

The SQLite driver is `modernc.org/sqlite`, so SQLite builds can run with `CGO_ENABLED=0` for simpler cross-compilation and container builds.

## Features

- Query SQL databases through MCP tools.
- Embedded React + Ant Design administration UI: instance CRUD, connection tests, default selection, metadata browsing, and token management. No login or SQL editor.
- Persistent instance/token stores, per-call token revocation checks, and optional execute permission.
- Paginated object discovery with rule-based grouping of suspected shard tables.
- Supports MySQL, MariaDB, Percona, TiDB, OceanBase, Apache Doris, StarRocks, PostgreSQL, CockroachDB, Greenplum, openGauss, MogDB, SQLite, SQL Server, and ClickHouse DSNs.
- Provides `query`, `execute`, `list_objects`, and `describe_object` tools.
- Supports Streamable HTTP at `/mcp`.
- Supports SSE at `/sse` plus `/message`.
- Supports multiple databases in one process with routes such as `/db/reporting/mcp`.
- Docker-friendly environment variable configuration.
- Per-database `database/sql` connection pools.
- Pure-Go SQLite support for CGO-free builds.

## Quick Start

Build from source (Go 1.26+ and Node 22.22+):

```bash
make build
./dist/sql-mcp serve --listen 127.0.0.1:8088 --data-dir ./data
```

Open **http://127.0.0.1:8088/** to add database instances. No database needs to be
configured before starting. UI assets are embedded in the binary: Node, a static
asset directory, and internet access are not needed at runtime.

- `/mcp`: multi-instance Streamable HTTP gateway.
- `/db/INSTANCE_ID/mcp`: fixed-instance endpoint.
- `/healthz`: liveness, including when no database is available.

Run stdio with an explicit DSN:

```bash
./dist/sql-mcp stdio --dsn sqlite:///tmp/example.db
```

Or use a persisted instance without running the HTTP service:

```bash
./dist/sql-mcp stdio --data-dir ./data --instance reporting
```

Omit `--instance` to use the saved default when no static DSN is configured.
The default `data/instances.sqlite`, when present, is discovered automatically.
No subcommand defaults to stdio. Explicit `stdio` ignores `LISTEN`; legacy
`--listen`/`LISTEN` startup remains supported with a migration notice.

`make release` builds embedded-UI binaries for the configured target platforms.
See [management and build details](docs/management-ui.md).

## Docker

Build and run the current source (pushing Git does not publish a Docker Hub image):

```bash
docker build -t sqlx-mcp:local .
mkdir -p data
docker run --rm -p 127.0.0.1:8088:8088 \
  -v "$PWD/data:/app/data" \
  -e DB_URL= \
  sqlx-mcp:local serve --listen :8088 --data-dir /app/data
```

Alternatively, run the published Docker Hub image (features depend on its release):

```bash
docker run --rm -p 8088:8088 \
  -e DB_URL=sqlite://:memory: \
  -e DB_NAME=sql-mcp \
  -e DB_MEMO="Local scratch database for first-run testing" \
  -e LISTEN=:8088 \
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

The compose file uses `DB_URL=sqlite://:memory:` by default and mounts `./data` to `/app/data` for local file-backed SQLite overrides.

For a local SQLite file, use an ignored `docker-compose.override.yml` or export `DB_URL` before starting Compose:

```bash
DB_URL=sqlite:///app/data/example.db docker compose up -d
```

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
| `--listen` | `LISTEN` | `127.0.0.1:8088` in `serve` | Network address; explicit `stdio` never opens a listener. |
| `--auth-token` | `AUTH_TOKEN` | empty | Legacy execute-enabled MCP token; persisted token policy still applies when empty. |
| `--data-dir` | `DATA_DIR` | `data` in `serve` | Persistent management stores. |
| `--instance` | none | saved/static default | Explicit stdio instance ID. |

Named database instances use the same pattern: `DB_{ID}_URL`, optional `DB_{ID}_NAME`, and optional `DB_{ID}_MEMO`.

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
/mcp                    -> multi-instance gateway, Streamable HTTP
/db/app/mcp             -> app database, Streamable HTTP
/db/reporting/mcp       -> reporting database, Streamable HTTP

/sse                    -> multi-instance gateway, SSE stream
/message                -> multi-instance gateway, SSE message endpoint
/db/app/sse             -> app database, SSE stream
/db/app/message         -> app database, SSE message endpoint
```

Use `serve` to expose UI, Streamable HTTP and SSE on the same port. The gateway
provides `list_instances`; `query`, `execute`, `list_objects`, and
`describe_object` require an `instance` argument. Fixed `/db/{id}/...` endpoints
omit that argument and reject it if supplied. Changing the default never
retargets gateway calls.

UI-created instances persist in the data directory. CLI/environment instances
are shown as external and cannot be edited through the UI.

Optional display names and memos:

```env
DB_APP_NAME=app-db
DB_APP_MEMO=Primary application database
DB_REPORTING_NAME=reporting-db
DB_REPORTING_MEMO=Analytics and BI replica
```

`DB_MEMO` and `DB_{ID}_MEMO` appear in gateway `list_instances` results so agents can distinguish instances. Static stdio also includes memos in its database-specific tool descriptions.

If `DEFAULT_DB` is not set, `DB_URL` is used as the default when present. Otherwise, the first configured database id in sorted order is used.

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

Create tokens in the UI at `/tokens`. Tokens are displayed only at creation and
stored as digests. `allow_execute=false` blocks the `execute` tool; database
account permissions remain the SQL privilege boundary. Credentials and permissions
are checked on every tool call, including existing SSE sessions after revocation.

A fresh store with no tokens or `AUTH_TOKEN` allows anonymous MCP access. Once
tokens have been created, revoking the last token does not reopen anonymous access.
MCP tokens do **not** protect the admin UI/API; bind to loopback or a trusted network.
Direct stdio does not require an HTTP token.

Alternatively, set the legacy execute-enabled `AUTH_TOKEN`:

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

Gateway calls require `instance` for all database tools below. Fixed-instance
endpoints omit it. `list_instances()` is available only on the gateway and returns
instance IDs and descriptive metadata without credentials.

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
| `page_size` | no | Integer 1–1000; default 1000. |
| `cursor` | no | Opaque continuation cursor from the preceding page. |
| `group_shards` | no | Group suspected shard tables; default true. |
| `group_id` | no | Expand a returned group into real members. |

Results include `objects`, `has_more`, and `next_cursor` (empty at the end).
Filtering precedes grouping and pagination. Groups require at least four tables
sharing a schema/prefix and suffix width: decimal `_0`…`_99`, or two-character
hex suffixes with at least one a–f member. Group entries include `kind: table_group`,
`prefix`, `rule`, `member_count`, limited `examples`, and `group_id`. These are naming
heuristics, not proof of native partitions or identical schemas. Expand a group
before describing a real table. Changed filters or metadata invalidate cursors.

### `describe_object`

Describes a table, view, procedure, or function.

Arguments:

| Name | Required | Description |
| --- | --- | --- |
| `object_name` | yes | Object name, optionally schema-qualified. |
| `object_type` | no | `table`, `view`, `procedure`, or `function`. |

## Security Notes

The `query` tool includes a read-oriented SQL guardrail, but it is not a complete security boundary. Use database users with the least privileges required for your deployment.

Remote administration requires trusted-network isolation, a VPN, or reverse-proxy access control. `AUTH_TOKEN` and UI-created tokens protect MCP endpoints only, not the management UI/API.

## Design

SQL MCP Server is designed to expose configured databases through small, explicit MCP tools. Tool responses should contain database facts and structured metadata directly, not hidden prompt-like instructions.

### Runtime Model

The server creates one runtime instance per configured database. Each instance owns:

- A parsed URL-style DSN.
- A dedicated `database/sql` connection pool.
- A database dialect.
- A database-specific MCP server. HTTP gateways expose generic tool schemas and return names/memos through `list_instances`.

Multi-instance gateways select the target explicitly with the `instance` argument. Fixed-instance paths bind the target and reject overrides. Managed configurations are resolved at operation boundaries; existing calls finish on their original pool.

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
  "instance": "reporting",
  "has_more": false,
  "next_cursor": "",
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

- `internal/instances`: persistent management store and instance administration.
- `internal/routing`: dynamic multi-instance and fixed-instance MCP gateways.
- `internal/tokens`: token digests, HTTP authentication, and per-call authorization.
- `internal/webui`: embedded static assets and SPA handler.
- `web`: React + Ant Design source and frontend build.

## Development

Run tests:

```bash
go test ./...
```

Verify CGO-free SQLite support:

```bash
CGO_ENABLED=0 go test ./...
```

Build for Linux ARM64 (regenerate UI assets first):

```bash
make frontend
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/sql-mcp-linux-arm64 ./cmd/sql-mcp
```

Build all release targets from the Makefile:

```bash
make release
```

## License

This project is licensed under the MIT License. See [LICENSE](LICENSE).
