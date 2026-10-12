# internal-mcp-server

AI Copilot MVP 的内部 MCP Business Gateway。当前阶段提供 Streamable HTTP 骨架、admin-api 连通性 Tool 和入金/返佣/分润业务 Tool。

## 能力

- `GET /healthz`：进程健康检查。
- `GET /readyz`：检查 admin-api `/api/managers/permissions` 连通性；该端点是临时探针，收到 HTTP 响应即视为可达，不校验业务响应内容。
- `POST|GET /mcp`：Official MCP Go SDK Streamable HTTP endpoint。
- `admin_api_ping`：只读连通性探针，输出统一 Tool envelope。
- `admin_api_data_overview`：基于 `GET /data_overview` 返回入金、代理商返佣和高手分润总额。
- `admin_api_deposits_statistics`：基于 `GET /deposits/statistics` 返回成功入金总数、客户数和各链金额/笔数/客户数。
- `admin_api_commission_statistics`：基于 `GET /commission/statistics` 返回代理商返佣人数和高手分润人数。
- `admin_api_deposits_list`：基于 `GET /deposits` 的入金列表，筛选和日期参数选填，使用 `limit/offset` 分页。
- `admin_api_commissions_agent_details`：指定代理商的已结算返佣金额和时间列表。
- `admin_console_find_page`：按运营的自然语言描述检索后台页面，返回菜单路径、绝对链接、页面能做什么，以及进页面后怎么找到目标记录。命中为空但带 `known_gaps` 表示后台确实没有这个功能。
- `admin_console_get_page`：按页面 id 取单页，并在服务端把实体值替换进动态路由拼出最终链接。

当前没有实现 website-api、业务数据 Tool、Knowledge、Navigation、Source、数据库持久化和 Agent 编排。

## 安全模型

MCP 请求必须携带：

```text
Authorization: Bearer <service token>
X-Copilot-User-Id: <user id>
X-Copilot-Session-Id: <session id>
X-Copilot-Request-Id: <request id>
```

可选的 `X-Copilot-Scopes` 会覆盖配置中的 `auth.default_permissions`，多个权限用英文逗号分隔。

admin-api 使用配置中的 cookie（`admin_api.credentials.cookies`，按 cookie 名逐项填写，例如 `session_id` 和 `XSRF-TOKEN`；代码负责拼装 Cookie 头）。这是 README 约定的本地/受控内部环境过渡方案，不提供真正的多用户下游权限隔离；真实配置必须放在 `config/config.local.json` 或环境变量中，不能提交。

入金工具要求 `admin.api.deposits.read` 权限；返佣和分润工具要求 `admin.api.commissions.read` 权限。`data_overview` 同时包含入金和返佣/分润金额，因此要求同时具备这两个权限；它返回上游的全部字段，包括注册数、交易人数/手数/金额/笔数、出金额和返佣/分润的受益人数。`data_overview` 工具的 6 个参数均必填；统计接口的起止时间必填；列表筛选日期按上游 Request 保持选填或必填，默认每页 20 条。

## 配置

复制示例配置：

```bash
cp config/config.example.json config/config.local.json
```

常用环境变量：

```text
SERVER_ADDR
MCP_SERVICE_TOKEN
MCP_DEFAULT_PERMISSIONS
ADMIN_API_BASE_URL
ADMIN_API_TIMEOUT
ADMIN_API_COOKIES
ADMIN_CONSOLE_BASE_URL
RATE_LIMIT_PER_MINUTE
RATE_LIMIT_BURST
LOG_LEVEL
```

`ADMIN_API_COOKIES` 接受整段 Cookie 头（`session_id=...; XSRF-TOKEN=...`），可直接从浏览器 DevTools 复制。

`admin_console.base_url` 指向运营用的后台控制台（不是它的 API），只用于把导航 manifest 里的相对路由拼成绝对链接，本服务从不请求这个地址。**它没有默认值，缺失会导致启动失败** —— 一个看似合理的默认值会产出模型自信发布给运营的死链，响亮地失败更好。

环境变量优先于 JSON 配置。`ADMIN_API_TIMEOUT`、`RATE_LIMIT_PER_MINUTE`、`RATE_LIMIT_BURST` 如果值无法解析，进程会在启动时报错退出，而不是回退到默认值。

## 本地运行

```bash
make test
make build
./bin/internal-mcp-server -config config/config.local.json
```

## Tool 错误语义

两个 `admin_console.*` tool 不访问任何下游，数据全部来自构建期内嵌的
`knowledge/navigation/pages.yaml`（见该目录的 README）。注意限流桶的 key 是
`service:<id>:user:<id>`，**不含工具名**，所以导航调用和其它工具共用同一份配额；
本地联调把 `rate_limit` 调高即可。

`admin_api_ping` 不使用 MCP protocol error 表示普通下游失败，而是返回：

```json
{
  "status": "ERROR",
  "data": null,
  "source": null,
  "pagination": null,
  "warnings": [],
  "error": {
    "code": "UPSTREAM_ERROR",
    "message": "admin-api request failed",
    "retryable": true
  }
}
```

`NO_DATA`、`SUCCESS` 与工具执行失败严格区分。

`retryable` 反映上游状态：超时、限流、5xx 以及传输层失败为 `true`；上游 4xx 是对请求本身的判定，重试只会再次失败，为 `false`。
