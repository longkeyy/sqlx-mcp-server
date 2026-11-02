# Management UI and multi-instance MCP

Build with `make build` (Go 1.26+, Node 22.22+). `make release` embeds the same UI
into each platform binary. `docker build -t sqlx-mcp .` builds frontend assets in
a separate Node stage; runtime needs neither Node nor external static files.

Run `dist/sql-mcp serve --listen 127.0.0.1:8088 --data-dir ./data` and open the
address in a browser. Instances, connection tests, default selection, metadata
browsing and tokens are available without an account. There is no SQL editor or
row-data viewer. Keep the admin listener on a trusted network; MCP tokens do not
protect administration. Browser writes require a matching Origin. Reverse proxies
must preserve the public host and terminate access within the same trusted boundary.

`/mcp` and `/sse` discover instances with `list_instances`; database tool calls
must supply `instance`. `/db/ID/mcp` and `/db/ID/sse` bind a single instance and
reject any supplied instance argument. Legacy SSE messages use `/message` or
`/db/ID/message`. Changing the UI default never reroutes gateway calls.

`sql-mcp stdio --data-dir ./data --instance ID` selects a persisted instance;
omitting `--instance` uses the saved default when no static DSN is configured.
The default `data/instances.sqlite`, when present, is discovered on startup.
An explicit DSN or instance takes precedence over the saved default.

Environment/CLI databases are displayed as external and cannot be edited through
the UI; they override persisted records with the same ID without overwriting them.
SQLite management stores persist independently of business databases. Back up the
data directory securely; DSNs are protected by filesystem permissions, not encrypted.

Tokens are shown only at creation and stored as digests. Execute-disabled tokens
cannot call `execute`; database account privileges remain the real read/write
boundary. Revocation applies to later calls on existing sessions, including SSE.
Revoking the last token does not restore anonymous access. AUTH_TOKEN remains a
legacy execute-enabled credential. Stdio has no HTTP-token requirement.

Table groups are naming heuristics, not proof of native partitions or identical
schemas. Expand a group to choose a real object for schema inspection. Pagination
cursors are invalidated by changes to matching metadata or filter options.

## Verification (2026-11-02)

All six module branches were reviewed and assembled. Validation passed:

- `go test -race ./...` across all Go packages.
- Docker integration acceptance: command modes, persisted instance lifecycle,
  explicit multi-instance routing, token permissions, metadata pagination/grouping.
- Chromium integration: actual embedded assets and actual management backend,
  instance CRUD/test/default, schema browsing and token create/revoke, with browser
  outbound network blocked. The module-only UI test's API mocks were not used here.
- Production Docker image build for the local linux/arm64 platform.

Other database engines retain existing drivers but were not live-tested in this
integration run; real database acceptance used SQLite. Cross-platform release
recipes are provided but this run did not execute every target architecture.
