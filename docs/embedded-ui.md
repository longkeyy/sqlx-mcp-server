# Embedded management UI

Build with `cd web && npm ci && npm run build`, then build the Go binary. Assets are committed in internal/webui/assets so ordinary Go builds also work. Node is only a build dependency. Mount webui.Handler() at `/`; mount management `/api/` handlers separately first. UI navigation supports `/instances` and `/tokens` without runtime CDN access.

The interface manages instances, connection tests, defaults, metadata browsing, token creation and revocation. DSNs are write-only and an empty edit preserves the stored connection. Tokens appear only after creation and are not persisted by the frontend. SQL execution and row previews are intentionally absent. Metadata naming groups can be expanded and pages followed.

Module browser acceptance uses mocked API responses with actual embedded assets. Full application acceptance must mount the real instance, metadata and token APIs. Backend administrative origin protections remain required.
