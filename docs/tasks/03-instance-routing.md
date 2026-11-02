# 03 多实例路由模块（internal/routing）

本模块实现多实例 MCP 网关：一个可独立挂载的 `http.Handler`，在 `/mcp`、`/db/{id}/mcp`、`/sse`、`/message`、`/db/{id}/sse`、`/db/{id}/message` 上提供 Streamable HTTP 与旧 SSE 两套传输，并在每次工具调用时按显式实例 ID 解析目标数据库。

本任务只改动 `internal/routing/` 与本文件；未修改 `cmd/`、`internal/server/`、`internal/config/`、其他模块、`tasks/`、`do.sh`、`go.mod`/`go.sum` 或构建文件，未提交、未推送。

## 公开接口

```go
package routing

type Registry interface {
	List(context.Context) ([]config.DatabaseConfig, error)
	Resolve(context.Context, string) (config.DatabaseConfig, error)
}

type Option func(*Router)

func New(registry Registry, options ...Option) (*Router, error)
func WithAuthorizer(func(context.Context, string) error) Option

func (*Router) ServeHTTP(http.ResponseWriter, *http.Request)
func (*Router) Close() error

var ErrClosed = errors.New("routing: router is closed")
```

- 依赖只有 `internal/config` 与 `internal/server`，不依赖实例管理模块的具体类型；`instances.Manager` 在合并后只要具备 `List`/`Resolve` 两个方法即可直接作为 `Registry` 传入。
- `New` 不建立任何数据库连接；连接池在第一次定位到该实例的调用时惰性创建。
- `WithAuthorizer` 未传入或传入 nil 时不增加权限限制。回调收到的是 HTTP 请求的 context（SSE 消息路径中 mcp-go 使用 `context.WithoutCancel`，但 context 值会保留）和 MCP 工具名。
- `Close` 幂等：关闭全部连接池与传输会话；关闭后已知路径返回 503，未知路径仍为 404。

## 端点与实例选择语义

| 路径 | 行为 |
| --- | --- |
| `/mcp` | 统一入口。`query`、`execute`、`list_objects`、`describe_object` 的 `instance` 为必填字符串；缺失、非字符串或空白都返回工具错误，不回退默认实例。`list_instances` 只在此入口提供。 |
| `/db/{id}/mcp` | 固定实例。schema 由统一 schema 派生并移除 `instance`；携带任何 `instance` 参数（包括 null）都会被拒绝，路径绑定不会被参数覆盖。不提供 `list_instances`。 |
| `/sse` + `/message` | 统一入口的旧 SSE 传输，语义与 `/mcp` 相同。 |
| `/db/{id}/sse` + `/db/{id}/message` | 固定实例的旧 SSE 传输。 |

- 未知或已删除的实例：`/db/{id}/...` 在创建端点时返回 404；已经建立的 SSE 会话中每次工具调用仍会重新解析 registry，失败时返回工具错误，不会回退到其他实例。固定端点按实例 ID 缓存，缓存本身不持有连接。
- 客户端并发调用不同实例时互不影响：不存在 `switch_instance` 或任何“当前实例”状态，目标只来自本次调用的参数或 URL。
- registry 的增删改在操作边界生效：新增实例可立即发现；DSN 或连接参数变化会为该实例建立新连接池，旧池在仍有在途调用时保持打开，最后一个引用释放后关闭；实例被删除后调用失败。
- 固定端点上的会话可以在实例被删除后继续连接；此时任何工具调用都会失败。

## 工具与结果

- 统一入口保留基线工具名、参数名与描述，仅新增必填 `instance`；固定入口的 schema 由统一 schema 派生，保证两者除 `instance` 外完全一致（后续任务 05 若给基线增加分页参数，这里会自动继承，但需要重新审视是否需要显式更新）。
- 转发方式：构造一条真实的 `tools/call` JSON-RPC 消息，调用目标实例基线 `AppServer.MCPServer().HandleMessage(ctx, raw)`，复用基线已有的工具注册表、参数校验、hook 与错误文案；没有复制 SQL 执行器。基线返回的 JSON-RPC 错误被转换为工具错误结果，而不是传输层错误。
- 成功结果会注入顶层 `instance`：
  - JSON 文本内容按偏移插入/替换 `"instance"` 字段，不经过 `map[string]any` 往返，因此保留键顺序、缩进与超过 2^53 的整数精度；
  - `structuredContent` 是 map 时做浅拷贝并加入 `instance`；
  - 失败结果不改写（错误本来就属于客户端调用的端点）。
- `execute` 的成功文案（`Operation successful.` 等）原样保留；`query` 等 JSON 结果新增顶层 `instance`。
- `list_instances` 返回 `{"instances":[{"id","name","dialect","memo","status"}]}`，不包含 DSN 或凭据。`status` 当前固定为 `"available"`，表示 registry 中存在该实例；发现过程不会逐个探测数据库，避免单个坏实例拖垮列表。

## 接线要求（由后续集成任务完成，本模块未做）

1. 在装配层用实例管理器实现 `routing.Registry`（`List` 返回当前配置快照，`Resolve` 按 ID 查找），调用 `routing.New(manager, routing.WithAuthorizer(...))`。
2. 授权：`WithAuthorizer(func(ctx, tool) error { return tokens.Authorize(tokens.TokenFromContext(ctx), tool) })`。token 中间件必须包在 router 外层，把身份写入请求 context；SSE 消息路径的 context 值由 mcp-go 保留（`context.WithoutCancel`），因此对已有会话同样生效。Router 不实现第二套 token 系统。
3. 挂载：网络模式下用 `mux.Handle("/", router)`（或直接把 router 作为 `http.Server.Handler`）替代 `MountHTTPServers`/`MountSSEServers`；router 自行处理全部路径与 404。若需要前缀挂载，需要在装配层做 `http.StripPrefix`，本模块未验证该用法。
4. 驱动注册：连接池由 `internal/server.BuildAppServers` 建立，驱动仍由 `cmd` 的空白导入注册（SQLite、MySQL、PostgreSQL、SQL Server、ClickHouse）。本模块测试只注册 SQLite。
5. 生命周期：进程退出时调用 `router.Close()`；stdio 模式不受影响，仍走基线 `ServeStdio`。
6. 认证中间件仍按现有方式包裹整个 handler；`AUTH_TOKEN` 的 query 参数回退属于装配层行为，Router 不做额外处理。

## 复用说明

- 连接池、方言识别与工具执行全部复用 `internal/server` 的导出能力（`BuildAppServers`、`CloseAppServers`、`AppServer.MCPServer`），未修改其内部实现。
- 工具 schema 在 `internal/routing/tools.go` 中静态声明并镜像基线参数；这样 `/db/{id}/...` 无需连接数据库即可完成 `initialize` 与 `tools/list`，满足“固定端点不依赖可用连接”的语义。
- 方言标签映射（`describeDialect`）在基线中是未导出函数，本模块为 `list_instances` 复制了这张小的映射表；若基线将来导出该函数，应改为直接调用。

## 测试结果（2026-10-21）

- `./tasks/03-instance-routing/test.sh`：退出码 **0**（Docker 容器内 `go test ./...` 全绿，验收用例 `TestExplicitRouting`、`TestRoutingAuthorizerPerCall` 通过）。
- `go test -count=1 ./...`：全部包通过。
- `go test -race -count=1 ./internal/routing/`：通过。
- `go vet ./...`：通过；`go fmt ./internal/routing/` 无差异。
- `go test -cover ./internal/routing/`：语句覆盖率 **81.1%**。
- 模块内测试覆盖：统一/固定两套传输与 schema、显式路由与无默认实例、未知/删除实例失败、registry 变更可见性、并发客户端隔离、每调用授权（含 `list_instances` 与固定端点、SSE 已有会话的动态拒绝）、响应中 `instance` 标记与结构化内容保留、连接池复用/退休/删除、关闭语义与 404/503。
- 本轮修复点：第一轮验收因 `internal/routing` 包不存在而失败（`no required module provides package .../internal/routing`），该模块当时为空；本轮补齐实现与测试。期间修正了实例字段插入导致空对象产生尾逗号的问题，以及两个测试自身的问题（SSE 测试在断言恢复后未复位 deny 标志；大整数用例经 `any` 解码导致精度丢失）。

## 未验证边界

- 未接入 `cmd`/serve：网络模式仍由基线 `MountHTTPServers`/`MountSSEServers` 提供，没有默认实例逻辑之外的统一入口；本模块测试全部通过 `httptest` 驱动。
- 未接入真实 token 授权链：只验证了回调契约与 context 传递，没有验证 `internal/tokens` 的实际行为（该模块属于任务 04）。
- 只测试 SQLite（`modernc.org/sqlite`）。PostgreSQL/MySQL/SQL Server/ClickHouse 连接与方言行为依赖 `internal/server`，本模块只用 DSN 解析验证了“不泄漏密码”和“无驱动时不连接也应成功初始化”的路径。
- `list_instances.status` 是静态值，不是实时健康检查；多实例中某个实例不可用时，列表不会显示降级状态。
- 未验证非根路径前缀挂载、负载均衡/多副本部署、会话跨进程迁移。
- 未实现也不依赖 UI；UI 集成由后续任务负责。
- 模块测试通过不代表产品集成、真实 MCP 授权链或 UI 完成。
