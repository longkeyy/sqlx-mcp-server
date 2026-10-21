# 02 — Instance Management Module

`internal/instances` is a self-contained Go package that owns the persistent
registry of database instances and its HTTP administration API. It depends on
`internal/dsn` and `internal/config` only; it knows nothing about MCP,
routing, tokens or the UI. The integration layer consumes it through the
small interfaces below.

## Public API

```go
func Open(dataDir string) (*Manager, error)
func (*Manager) Handler() http.Handler
func (*Manager) List(context.Context) ([]config.DatabaseConfig, error)
func (*Manager) Resolve(context.Context, string) (config.DatabaseConfig, error)
func (*Manager) Default(context.Context) (string, error)
func (*Manager) Close() error
```

- `Open` creates `dataDir` (mode `0700`) when missing and opens/creates
  `<dataDir>/instances.sqlite` (mode `0600`). It registers no globals; the
  package imports every supported SQL driver so callers never rely on
  another package's `init` side effects.
- `List` returns every managed instance ordered by ID. The slice is never
  `nil`. Values include the DSN and are intended for trusted in-process
  consumers.
- `Resolve` matches the ID **exactly**. An unknown ID is an error; there is
  no fallback to the default instance. Reads always hit the store, so edits
  made through another `Manager`, another process, or a later `Open` are
  visible.
- `Default` returns the persisted default instance ID, or `""` when unset.
  It returns `""` rather than an error if the stored ID no longer exists.
- `Handler` returns the admin API described below. It can be mounted
  directly with `http.Handle("/api/instances", m.Handler())`.
- `Close` waits for in-flight operations, is idempotent, and makes later
  calls fail with `errClosed` (HTTP 503).

## HTTP API

All routes are absolute. Responses are JSON, sent with
`Cache-Control: no-store` and `X-Content-Type-Options: nosniff`. Errors use
`{"error":"..."}`.

| Method | Path | Success | Notes |
| --- | --- | --- | --- |
| `GET` | `/api/instances` | 200 `{"instances":[...]}` | Empty store yields `[]`, never `null` |
| `POST` | `/api/instances` | 201 | Body `{id,name,dsn,memo}`; `Location` header set |
| `GET` | `/api/instances/{id}` | 200 | Single instance view |
| `PATCH` | `/api/instances/{id}` | 200 | Partial `{id?,name?,memo?,dsn?}` |
| `DELETE` | `/api/instances/{id}` | 204 | Also clears it as default |
| `POST` | `/api/instances/{id}/test` | 200 | Always 200: `{ok,instance,latency_ms,error?}` |
| `GET` | `/api/settings` | 200 `{"default_instance":"id"}` | `""` when unset |
| `PUT` | `/api/settings/default` | 200 | Body `{"instance":"id"}`; echoes the same shape |

Instance view:

```json
{
  "id": "alpha",
  "name": "Alpha",
  "memo": "",
  "driver": "postgres",
  "dialect": "PostgreSQL",
  "source": "managed",
  "status": "available",
  "last_error": "",
  "tested_at": "2026-10-21T03:52:00Z",
  "is_default": true,
  "max_open_conns": 25,
  "max_idle_conns": 1,
  "created_at": "2026-10-21T03:51:00Z",
  "updated_at": "2026-10-21T03:51:00Z"
}
```

The view has **no** DSN, host, user or password field. `status` is one of
`unknown`, `available`, `unavailable`. `source` is always `managed` for
instances persisted by this module; the integration layer overlays
environment/CLI instances and must present its own `source` for those.

Status codes: 400 invalid input, 404 unknown route or instance, 405 wrong
method (with `Allow`), 409 duplicate ID, 413 body over 1 MiB, 415 missing
JSON content type, 503 store closed, 504 request context expired.

Validation rules:

- `id`: trimmed and lowercased, `^[a-z0-9][a-z0-9_-]*$`, at most 64 chars.
  It is immutable: `PATCH` with a different `id` is rejected.
- `name`: trimmed, defaults to the ID, at most 256 chars.
- `memo`: trimmed, at most 4096 chars.
- `dsn`: required, at most 8192 chars, must parse with `internal/dsn`,
  must include a host (or a SQLite path). Reachability is **not** checked:
  a syntactically valid but unreachable instance is accepted, so it can be
  corrected later. Validation errors never echo the DSN.
- `PATCH` with an omitted or empty `dsn` keeps the stored credential and
  leaves the ID stable. Replacing the DSN resets the recorded status to
  `unknown`.

Routing: `/api/instances/{id}/objects*` and any deeper path return 404 from
this module. The integration adapter (task 03) owns those routes and must
mount them more specifically than `/api/instances/`.

## Storage

- Single SQLite file at `<dataDir>/instances.sqlite`, separate from every
  business database. `dataDir` and the store file are created `0700`/`0600`;
  an existing directory keeps its mode because it may be shared.
- Connection parameters: `busy_timeout(5000)`, `journal_mode(WAL)`,
  `synchronous(FULL)`, `_txlock=immediate`.
- Schema version is tracked in `PRAGMA user_version` (currently 1) and is
  migrated inside one immediate transaction, so concurrent opens serialise.
  A newer schema version is refused rather than silently downgraded.
- Writes from this process are serialised with a mutex; across processes
  they rely on immediate transactions plus the busy timeout. There is no
  in-memory cache, so every read reflects the file.
- The store is a local file. Do not put `dataDir` on a network filesystem;
  WAL and `busy_timeout` assume local POSIX locking.

## Connection tests

`POST /api/instances/{id}/test` runs the **real** driver for the stored DSN:
`sql.Open` → `PingContext` → `SELECT 1` (SQLite: `SELECT count(*) FROM
sqlite_master`) → close. It never mutates the user's schema or data.

- Bounded by `testTimeout` (5s by default, 300ms in the bounded unit test).
  The probe runs in a goroutine with a cancelled context; a `select` on the
  timeout also covers drivers that ignore cancellation.
- SQLite files are `os.Stat`-checked before opening so a test against a
  missing database fails instead of creating an empty file.
- A failed connection is a normal result (`200 {"ok":false,"error":"..."}`),
  not an HTTP error.
- The outcome is stored (`status`, `status_error`, `tested_at`) only if the
  DSN still matches the one that was tested, so a concurrent edit cannot be
  overwritten by a stale result.
- Error messages are sanitized: URL user info and `password=`-style pairs
  are masked, credential values from the DSN (raw and URL-escaped) are
  replaced with `***`, whitespace collapsed, and the text truncated to 300
  bytes. Original driver errors are never logged with the DSN attached.

## Security

- HTTP responses never contain a DSN or password, including validation
  errors, test results and `last_error`.
- Writes that carry an `Origin` header are accepted only when the origin's
  host equals `r.Host`. `Origin: null` is rejected. Requests without an
  `Origin` (curl, CLI, server-side clients) are allowed on the assumption
  that the admin listener is on a trusted network. A reverse proxy must
  preserve the public `Host` header for this check to be meaningful.
- There is no authentication in this module. Do not expose the admin API to
  untrusted networks; put it behind the product's auth layer.
- Store files are `0600`, the data directory `0700`. Secrets live inside the
  SQLite file and in `List`/`Resolve` results, so treat both as sensitive.

## Wiring requirements (integration task)

`cmd/sql-mcp` and `internal/server` are owned by other tasks. When wiring
this module:

1. Call `instances.Open(dataDir)` during startup and `Close()` on shutdown.
2. Mount `Handler()` at `/api/instances`, `/api/instances/` and
   `/api/settings`, `/api/settings/default`. Mount the `/objects*` adapter
   under the more specific `/api/instances/` prefix.
3. Pass the `*Manager` as the routing registry (`List`/`Resolve`). Prefer an
   explicit DSN (CLI/env) over the persisted default; use `Default()` only
   when no explicit target is given.
4. Overlay environment/CLI-configured instances in the listing with their
   own `source`, and reject UI edits to them. The reserved ID `default` is
   for the `DB_URL` instance; this store accepts it as a normal ID, so the
   integration layer must filter it out of the managed list and refuse
   creating or patching it through the admin routes.
5. Parse a connection probe result as `{ok,instance,latency_ms,error?}` and
   reflect `status` in `data-status`.

## Tests

- `internal/instances/manager_test.go`, `handler_test.go`,
  `coverage_test.go`: unit tests using real SQLite files in `t.TempDir()`;
  no network databases are touched.
- `tasks/02-instance-management/tests/acceptance_test.go`: lifecycle over
  `httptest` (create, patch, default, test, persistence across reopen,
  delete, secret leak checks).
- Results: `go test ./...` passes; `go test -race -cover ./internal/instances/`
  reports 86.6% coverage; `./tasks/02-instance-management/test.sh` (Docker,
  `golang:1.26.3-bookworm`, `CGO_ENABLED=0`) passes.

## Unverified boundaries

- Successful connection tests are covered only for SQLite. MySQL,
  PostgreSQL, SQL Server and ClickHouse tests are covered indirectly via
  refused/unreachable endpoints; no live server was available in this
  environment.
- Multi-process concurrency is designed for but not stress-tested; only
  in-process concurrency (24 parallel creates, race detector) is tested.
- MCP, routing, UI and CLI wiring are out of scope for this task and remain
  unverified until the integration task lands.
