# Command Modes and Static Connection Lifecycle

This document describes the command surface owned by task 01 (`cmd/sql-mcp/**`,
`internal/config/**`) and what the human integrator still has to assemble. It is
the source of truth for modes, flags, precedence, exit codes, route binding,
degraded behaviour and secret handling. The full-product behaviour lives in
`tasks/common/CONTRACT.md`; the tool surface lives in `docs/design.md`.

## Commands

```text
sql-mcp [flags]           stdio by default; legacy HTTP when --listen/LISTEN is set
sql-mcp stdio [flags]     stdio; never opens a listener, ignores --listen and LISTEN
sql-mcp serve [flags]     Streamable HTTP + legacy SSE; defaults to 127.0.0.1:8088
sql-mcp help | -h         usage on stdout, exit 0
```

Flags may appear before or after the subcommand. Parsing is pure: no database
is opened, no directory is created, and no listener is bound until `run` has
resolved the configuration.

| Flag | Environment | Meaning |
|---|---|---|
| `--dsn` | `DB_URL` | Default database DSN |
| `--name` | `DB_NAME` | Display name for the default database |
| `--listen` | `LISTEN` | HTTP bind address for `serve` |
| `--data-dir` | `DATA_DIR` | Directory reserved for the instance store |
| `--auth-token` | `AUTH_TOKEN` | Bearer/query token for the MCP routes |

Command-line flags win over environment variables. Named databases come from
`DB_{ID}_URL`, `DB_{ID}_NAME` and `DB_{ID}_MEMO`; `DEFAULT_DB` selects the
default id. In stdio mode the database must be selected statically: the
default instance from `--dsn`/`DB_URL` or `DEFAULT_DB`.

### Legacy startup

Running without a subcommand and with a non-empty `--listen` (or `LISTEN`)
starts the HTTP server exactly like `serve` and logs:

```text
Notice: starting HTTP without a subcommand is deprecated; use "sql-mcp serve --listen ADDR" instead
```

Only the explicit `serve` command may start with zero configured databases.
Legacy startup and stdio keep requiring a DSN, so an old deployment that loses
its configuration fails loudly instead of silently serving nothing.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Normal shutdown, `help`, or SIGINT/SIGTERM |
| 1 | Runtime failure: listen error, unusable DSN, database unreachable at startup |
| 2 | Invalid command line or environment (unknown command, bad flag, `DEFAULT_DB` naming no instance, data directory is a file) |

Configuration errors are printed as `Configuration error: ...`; runtime errors
as `Startup error: ...`, `Server error: ...` or `Stdio server error: ...`. Both
go to stderr only.

## Output and logging

- stdout is reserved for MCP JSON-RPC in stdio mode. The only non-JSON output
  on stdout is the usage text for `help`.
- All diagnostics use a `log.Logger` writing to stderr, prefixed with the
  standard date/time.
- `serve` logs the bound address, how many instances are healthy, the default
  id and whether authentication is enabled. It never logs a raw DSN: the
  baseline connection log applies `config.MaskDSN`.
- Failure reasons for an instance are redacted with `config.RedactDSN` before
  they reach the operator log, and are never sent to HTTP clients.

## Lifecycle

`run` performs these steps in order:

1. Parse flags/environment (`config.ParseArgs`). Pure, no I/O.
2. Log notices (legacy HTTP, `--listen` ignored in stdio).
3. If `--data-dir` is set, create it with `0700` and resolve it to an absolute
   path (`config.PrepareDataDir`). An existing path that is not a directory is
   a configuration error (exit 2).
4. Dispatch to `runStdio` or `runServe`.

### stdio

- Opens only the selected static database. One MCP stdio session bound to
  `stdin`/`stdout`.
- Opens no listener, regardless of `LISTEN` or `--listen`.
- Returns 0 on EOF of stdin or when the context (SIGINT/SIGTERM) is cancelled;
  database pools are closed on the way out.
- An unreachable database is a startup failure (exit 1): unlike `serve`, a
  stdio client has no alternative route and must not receive a silent empty
  server.

### serve

1. Bind the listener **first**. An address conflict exits 1 before any
   database is opened.
2. Open every configured database concurrently; each failure is isolated and
   logged. Startup never aborts because one DSN is broken.
3. Mount transports per instance and start serving.
4. On context cancellation, stop accepting connections, log
   `Shutdown requested; draining connections (up to 10s)`, cancel long-lived
   GET streams (SSE and Streamable HTTP notification streams), and wait up to
   10 seconds for in-flight requests. Anything still running is force-closed
   and the process exits 0.

`/healthz` is a liveness probe, not a readiness probe: it stays 200 while the
process serves HTTP, including with zero or failed database instances. That is
what lets an orchestrator keep the container up while an operator repairs a
DSN.

## Routes

| Path | Transport | Bound to |
|---|---|---|
| `/healthz` | GET/HEAD, no auth | process liveness |
| `/mcp` | Streamable HTTP | default instance |
| `/sse` + `/message` | legacy SSE | default instance |
| `/db/{id}/mcp` | Streamable HTTP | that statically configured instance |
| `/db/{id}/sse` + `/db/{id}/message` | legacy SSE | that instance |

`/healthz` sits outside authentication so probes need no credentials; every
MCP route is behind `server.AuthMiddleware` when `--auth-token`/`AUTH_TOKEN`
is set. The middleware accepts `Authorization: Bearer <token>` or the query
fallback `?token=<token>`, and answers 401 with `WWW-Authenticate: Bearer`.

Streamable HTTP follows mcp-go's session rules: `initialize` issues an
`Mcp-Session-Id` header that clients must echo on later POSTs.

## Degraded mode

| Situation | Result |
|---|---|
| No default configured | `/mcp`, `/sse`, `/message` → 503 `{"error":"no default database instance is configured"}` |
| Default configured but failed to open | default routes → 503, **no fallback** to another instance |
| Named instance failed to open | its `/db/{id}/...` routes → 503 |
| Unknown id under `/db/` | 404 `{"error":"unknown database instance"}` |
| Any of the above | `/healthz` → 200; healthy instances unaffected |

The 503 body is deliberately generic:

```json
{"error":"database instance \"broken\" is unavailable; see the server log"}
```

Driver errors routinely contain hosts, usernames, absolute paths and (for
some drivers) the DSN itself. Only the instance id is exposed; the redacted
reason is written to the operator log. The default-binding rule is explicit:
a client that did not select an instance is never silently routed to one that
happens to be healthy.

## `--data-dir` pass-through

Task 01 accepts, validates and creates the data directory but does **not**
implement instance storage. Startup logs:

```text
Using data directory /srv/sql-mcp/data (persistent instance storage is not part of this release)
```

The directory is created with `0700` (files created by later modules should be
`0600`). No MCP route reads it, so stdio never depends on HTTP or on any store
being present. This is intentional: task 02 owns the SQLite instance store and
task 04 owns the token store, both keyed by the same data directory.

## Secret handling

- `config.MaskDSN` masks the userinfo password and the values of
  `password`, `passwd`, `pwd`, `secret`, `token`, `access_token` and
  `api_key` query parameters. Used for routine connection logs.
- `config.RedactDSN` removes every occurrence of the raw DSN, its
  `strconv.Quote`d form and its URL-decoded form from an error string, then
  applies the query-secret masking. Used for errors that may echo the DSN
  (`url.Parse` failures do exactly that).
- Duplicate `DB_{ID}_URL` ids are reported by variable name, never by value.
- Credentials come from flags/environment (or `.env` for local development);
  nothing is hardcoded. Tests generate their tokens at run time.

## Known limitations

- Driver-level connection timeouts are the driver defaults; a DSN pointing at
  an unroutable host can hold up startup for the driver's timeout. A bounded
  startup ping would need a change in the baseline `internal/server`
  connection code, which this task does not own.
- `serve` without a default database serves 503 on `/mcp`; until task 03's
  router is assembled there is no unified multi-instance gateway.
- Legacy no-subcommand HTTP still requires a DSN; only `serve` tolerates an
  empty configuration.
- stdio serves exactly one static instance. Selecting instances from the
  management store is an integration step, not implemented here.

## Integration and assembly notes

What the integrator must wire (none of it is imported by this module):

### Instance management (task 02)

```go
manager, err := instances.Open(cfg.DataDir)   // requires --data-dir
defer manager.Close()
admin := manager.Handler()
root.Handle("/api/instances", admin)
root.Handle("/api/instances/", admin)         // {id}, {id}/test
root.Handle("/api/settings", admin)
root.Handle("/api/settings/", admin)          // default
```

- `instances.Manager` satisfies `routing.Registry` (`List`, `Resolve`) with no
  adapter.
- `--data-dir` is already normalised and created by `run`; pass `cfg.DataDir`
  straight to `Open`.
- The static configuration (`cfg.Databases`, `cfg.DefaultDatabase`) must stay
  visible and read-only; the settings API must reject edits to
  environment-managed instances.
- stdio selection: when no `--dsn`/`DB_URL`/`DEFAULT_DB` is given, resolve the
  persisted default from the store *before* entering `runStdio`. The baseline
  requirement "explicit DSN wins over persisted default" is preserved by
  checking `cfg.DSN` first.

### Routing (task 03)

```go
router, err := routing.New(manager, routing.WithAuthorizer(authorize))
root.Handle("/", router)   // replaces newServeHandler's mcpMux
```

- Today `newServeHandler` builds the fixed-instance table from the static map:
  `/mcp`, `/sse`, `/message` for the default and `/db/{id}/...` per instance.
  The router takes over those paths and the unified `/mcp` gateway.
- `buildServeApps` should then be replaced by the router's own pool lifecycle;
  until then, keep it so static DSNs work without the store.
- Request context must flow through to tool handlers so token checks and
  cancellation keep working.

### Tokens (task 04)

```go
tokensManager, err := tokens.Open(cfg.DataDir, cfg.AuthToken)
root.Handle("/api/tokens", tokensManager.Handler())
protected := tokensManager.Middleware(next)         // MCP transports only
authorize := func(ctx context.Context, tool string) error {
    return tokensManager.Authorize(tokens.TokenFromContext(ctx), tool)
}
```

- Mount `/api/tokens` and `/healthz` outside `Middleware`; mount MCP routes
  inside it. The admin API must not require an MCP token.
- `cfg.AuthToken` is the legacy token and must be passed to `Open` so it stays
  execute-enabled.
- `Authorize` has to run on every `tools/call`, including messages on existing
  SSE sessions — not only when the connection is established.

### Embedded UI (task 06)

```go
root.Handle("/", webui.Handler())
```

Mount after the API and MCP patterns; `ServeMux` prefers the more specific
patterns, so `/mcp`, `/sse`, `/message`, `/db/`, `/api/` and `/healthz` keep
winning. The UI must be reachable without MCP token auth.

### Container/build proposal

The task does not touch `Makefile` or `Dockerfile`. Suggested final wiring:

```dockerfile
ENTRYPOINT ["/usr/local/bin/sql-mcp"]
CMD ["serve", "--listen", "0.0.0.0:8088", "--data-dir", "/data"]
```

with a declared volume on `/data` (owner-only permissions are enforced at
startup) and `HEALTHCHECK CMD ["wget", "-qO-", "http://127.0.0.1:8088/healthz"]`
or an equivalent probe. In-container binding must be `0.0.0.0`; the process
default stays loopback.

## Tests

- `internal/config/command_test.go` covers command parsing, precedence,
  legacy detection, notices, data-dir preparation and usage text.
- `internal/config/mask_test.go` covers masking/redaction and the duplicate-id
  error path.
- `cmd/sql-mcp/serve_test.go` covers `/healthz` without databases, auth,
  Streamable HTTP and SSE against real SQLite fixtures, per-instance
  isolation, no fallback, credential non-leakage and graceful shutdown with an
  open stream.
- `cmd/sql-mcp/main_test.go` covers exit codes, stdout purity in stdio,
  `LISTEN` being ignored by stdio, cancellation, serve startup/shutdown with
  `--data-dir`, listen conflicts and the legacy notice.

Fixtures are created per test in temporary directories; tokens are random per
run. No fixture values are asserted as production constants.
