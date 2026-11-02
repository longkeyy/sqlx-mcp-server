# 04 — Token 管理独立模块

实现位于 `internal/tokens/`，只依赖标准库、`database/sql` 与已有的 `modernc.org/sqlite` 驱动。
不修改 `cmd/`、`internal/server/`、`internal/config/`、`go.mod`、构建文件或验收脚本；产品装配留给后续集成任务。

## 公开接口

```go
func Open(dataDir, legacyToken string) (*Manager, error)
func (*Manager) Handler() http.Handler
func (*Manager) Authorize(token, tool string) error
func (*Manager) Middleware(next http.Handler) http.Handler
func (*Manager) Close() error
func TokenFromContext(ctx context.Context) string

var ErrUnauthorized error // 凭据缺失/无效/已撤销
var ErrForbidden    error // 凭据有效但无 execute 权限
var ErrClosed       error // Manager 已关闭
```

### Manager 生命周期

- `Open` 打开（必要时创建）`<dataDir>/tokens.sqlite`，目录权限 0700，数据库文件权限 0600。
  `PRAGMA user_version` 做迁移；库版本高于本程序时拒绝打开。
- `legacyToken` 非空时立即启用认证，该 token 永不落盘、始终拥有 execute 权限。
- `Close` 幂等；关闭后所有入口返回包装 `ErrClosed` 的错误（HTTP 层映射为 503）。

### 认证与授权模型

- 凭据只以 `SHA-256(token)` 十六进制摘要存储，明文仅出现在 `POST /api/tokens` 的 201 响应里。
  比较使用 `crypto/subtle.ConstantTimeCompare`；随机源为 `crypto/rand`
  （secret 形如 `sqlmcp_<64 hex>`，id 为 24 hex 字符）。
- `Authorize(token, tool)` **每次都查库**，因此撤销对已经建立的会话立即生效。
  `tool` 大小写不敏感，只有工具名 `execute` 受 `allow_execute` 约束；其他工具任何有效凭据都可用。
  该开关不承诺 SQL 只读隔离，实际权限由数据库账号决定。
- 引导（bootstrap）语义：从未配置认证（无 legacy token 且从未创建过 token）时，匿名调用被接受为
  拥有 execute 权限的引导主体；创建第一个 token 时在同一事务内写入 `auth_enabled=1`，
  此后即使删光所有 token 也不会重新放开匿名访问，重启后状态仍在。
- `Middleware` 只做 HTTP 身份认证：依次尝试 `Authorization: Bearer <token>`（scheme 大小写不敏感）
  和旧式 `?token=` 查询参数，成功者写入 context；保留原 query 供 SSE message 端点使用。
  缺失/无效应答 401 + `WWW-Authenticate: Bearer realm="sql-mcp"`（提交了凭据时追加
  `, error="invalid_token"`）；存储失败或已关闭应答 503（fail closed）。
  每个工具调用仍必须自行用 `TokenFromContext(ctx)` + `Authorize` 校验。

### 管理 API（`Handler()`）

`Handler` 直接匹配 `/api/tokens`，**不能**套 `Middleware`，也不应 `http.StripPrefix`；
它没有自己的登录态，必须只暴露在可信（默认回环）管理端口上。

| 方法 | 路径 | 结果 |
|---|---|---|
| GET / HEAD | `/api/tokens`（含尾斜杠） | 200 `{"tokens":[{id,name,allow_execute,created_at}]}`，不含明文或哈希 |
| POST | `/api/tokens` | 201 `{id,name,allow_execute,created_at,token}`，`token` 仅此一次返回 |
| DELETE | `/api/tokens/{id}` | 204；不存在 404 |

- 请求体 `{"name":"...","allow_execute":false}`；name 去空白后必填、≤128 个 Unicode 字符、
  不含控制字符；`allow_execute` 缺省为 false；名字可重复。
- 错误统一为 JSON `{"error":"..."}`，所有响应带 `Cache-Control: no-store` 和
  `X-Content-Type-Options: nosniff`；500 不回显存储细节。
- 其他方法返回 405 并带 `Allow`；集合外的路径 404。
- 写操作经 `http.NewCrossOriginProtection()` 保护：跨站浏览器请求 403，
  不带 Origin/Sec-Fetch-Site 的 CLI 请求照常放行。

## 接线要求（后续集成任务）

本模块**没有被 `cmd/sql-mcp` 或 `internal/server` 引用**，模块测试通过不代表产品已接入。集成时需要：

1. 用 `tokens.Open(dataDir, cfg.AuthToken)` 创建 Manager，替换/包住现有的 `internal/server/auth.go`
   HTTP token 校验；把 `Middleware` 包在 HTTP/SSE MCP transport 外层。
2. 把 `Handler()` 挂到管理监听器（例如 `/api/tokens`），注意不能经过 `Middleware`；
   需要同步处理既有的 `/api/instances` 等管理路由与之共存。
3. 在每个 `tools/call` 内（包括已建立 SSE 会话的后续调用）执行
   `tokenManager.Authorize(tokens.TokenFromContext(ctx), toolName)`，
   把 `ErrForbidden` 映射为工具错误；不能只在 `initialize` 时检查一次。
4. 管理 UI（若存在）通过该 Handler 创建/撤销 token，并提示明文只显示一次。

## 测试与结果

模块内单元测试（`internal/tokens/*_test.go`）覆盖：bootstrap→首次建 token 后收紧、撤销即时生效且持久、
legacy token 兼容、存储中无明文且文件权限 0600、特殊字符 dataDir、并发授权、关闭后 fail closed、
Bearer/query 解析与优先级、401 challenge、跨站写拒绝、全部输入校验分支、Handler 生命周期。

实测：

| 命令 | 结果 |
|---|---|
| `go test -count=1 -race ./internal/tokens/` | PASS |
| 覆盖率（`-cover`，本地 darwin/arm64） | 86.2% |
| `go vet ./internal/tokens/`、`go build ./...` | 通过 |
| `go test -count=1 ./...`（本机） | 全部包 PASS |
| `./tasks/04-token-management/test.sh`（Docker `golang:1.26.3-bookworm`，CGO_ENABLED=0） | 退出 0；`TestTokenModule`、`TestLegacyTokenModule` PASS，仓库全部包 PASS |

## 未验证边界

- 真实 MCP 会话/传输接线、`tools/call` 内的逐调用授权、serve/CLI/UI 的端到端行为均未在本任务验证。
- 多进程同时打开同一 `tokens.sqlite` 未做压力验证；仅依赖 SQLite 的 `busy_timeout` 与
  `BEGIN IMMEDIATE` 事务，Windows 文件锁语义未测试。
- 管理 API 没有认证与 TLS，安全性取决于部署时只绑定回环/受信网络。
- 未实现管理 API 的分页/审计日志，也未做 token 过期或轮换策略。
- SQLite journal/WAL 文件的明文残留未做深度取证（测试覆盖同目录常规文件扫描与 `secure_delete`）。
