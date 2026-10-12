# Navigation Manifest

后台（admin-frontend / `mx-admin-console`）的页面清单，用于回答「这个功能在后台哪个页面」
这类 PRD 场景四的问题。技术设计 §13。

| 文件 | 谁维护 |
|---|---|
| `pages.yaml` | **生成物，不要手改。** 由 blockmatrix-web 的生成器写出 |
| `pages.overlay.yaml` | 手工维护的描述字段，按页面 `id` 索引 |

## 重新生成

生成器在 **blockmatrix-web** 仓库，不在这里：

```bash
cd /path/to/blockmatrix-web
npm run gen:nav-manifest -- \
  --out /path/to/ai-copilot/internal-mcp-server/knowledge/navigation/pages.yaml \
  --overlay /path/to/ai-copilot/internal-mcp-server/knowledge/navigation/pages.overlay.yaml
```

两个路径都必填、没有默认值，免得写错地方。

生成器用 `vite-node` **运行时导入** `APP_ROUTES`，不做 AST 解析。这样天然绕开：注释掉的
死配置（`marketing.ftmo.*`）、`path: CUSTOMERS_LIST_ROUTE` 这类标识符引用、以及 JSX 字段。

产物里**没有时间戳，也没有 source commit** —— 否则每次重跑都产生 diff，
下面的漂移检测就永远失败了。何时变更看 git 历史即可。

### 漂移检测

前端改了菜单/路由后，重跑生成器再看 diff：

```bash
git diff --exit-code knowledge/navigation/
```

干净 = 没漂移。有 diff = 前端动过，检查 overlay 里的描述是否还成立。

生成器在以下情况**非零退出**，不会写出文件：

- `pages.overlay.yaml` 里的 id 在当前路由表中已不存在（前端删/改了页面，描述成了孤儿）
- overlay 条目里出现未知字段（打错字的保护）
- 同一个 `id` 出现多次
- 收进了 `marketing.ftmo.*` 死配置（说明收集方式退化成了 AST 解析）

高频域页面缺 `description` 只告警，不阻断。

## Schema

顶层两个键：

```yaml
known_gaps:   # 后台压根没有的东西，来自 overlay
  - topic: KYC / 实名认证
    note: admin-frontend 中不存在任何 KYC、实名认证相关的页面、路由或接口。
pages:
  - id: finance.cash.deposit
    ...
```

`known_gaps` 的用处是让未来的 tool 能回答「后台目前没有 KYC 页面」，而不是干巴巴的「没找到」。

单条 page：

| 字段 | 来源 | 说明 |
|---|---|---|
| `id` | 生成 | 稳定唯一标识，见下节 |
| `title` | 生成 | `route.label`（即 `defaultLabel`），中文标题 |
| `route` | 生成 | 相对路径。**不含 base_url** —— 部署信息留给 MCP 侧配置 |
| `menu_path` | 生成 | 从顶层菜单到本页的标题链 |
| `menu_key` | 生成 | 前端菜单权限码，**不唯一**，见下节。tool 把它放在 `access.menu_key` 下并标 `checked: false`，不冒充 MCP 权限码 |
| `hidden` | 生成 | `true` = 侧边栏不显示（详情/新建/编辑页） |
| `api_endpoints` | 生成 | 本页自身调用的接口。客户详情抽屉那一整包公共接口已剔除 |
| `entity_params` | 生成 | 路由动态段名；全站只有 4 条非空 |
| `description` | overlay | 这页是干什么的 |
| `keywords` | overlay + 生成 | overlay 关键词 ∪ 该页三语（zh/en/ja）标题，去重 |
| `capabilities` | overlay | 这页能做的具体操作 |
| `lookup_hint` | overlay | 进了页面之后怎么找到目标记录 |
| `reviewed` | overlay | `false` = 描述是按代码信号批量起草的，尚未人工逐条复核 |

`keywords` 是**合并**的：overlay 里写的关键词会和三语标题并起来。所以没写 overlay 的页面
也有基本的检索命中能力，不至于完全搜不到。

## id 怎么来

`menu_key` 既是前端菜单权限码也是后端权限码，是最自然的稳定 id —— 但它在路由表里
**不唯一**：详情/新建/编辑这类隐藏页复用其列表页的 `menu_key`。例如
`/roles/settings`、`/roles/create`、`/roles/edit/:roleId` 三条路由的 `menuKey`
同为 `settings.roles.settings`。

所以规则是：

- `menu_key` 只对应一个页面 → `id` = `menu_key`（48 条可见页面全部如此）
- 多个页面共用 → 可见的那条保留裸 `menu_key`，其余追加 `:后缀`，后缀由路径推导

当前 6 条带后缀的 id：

```
agents.relationship.applications:applicationId   /agents/relationship/applications/:applicationId
agents.relationship.list:structure               /agents/relationship/structure/:ibId
agents.commission.configs:create                 /agents/commission/configs/create
agents.commission.configs:detail                 /agents/commission/configs/:commissionRuleId/detail
settings.roles.settings:create                   /roles/create
settings.roles.settings:edit                     /roles/edit/:roleId
```

## 两个必须如实处理的限制

**1. 记录级深链几乎不存在。** 全站只有 4 条带动态段的路由（都是 `hidden` 的详情页），
没有指向具体客户、入金、出金、订单、佣金记录的 URL。客户详情是 React state 里的抽屉，
不写 URL；列表页也没有 `?user_code=` 预填。所以除这 4 条外，只能给**页面级链接 +
一句 `lookup_hint`**，不要假装每页都有深链。

**2. manifest 不做权限过滤。** 前端的 `menu_key`（菜单位置，如 `finance.cash.deposit`）
和 MCP 的权限码（后端资源+动作，如 `admin.api.deposits.read`）是两套词表，不能靠字符串
变换互推。更关键的是即便映射出来也没法用来过滤：MCP 下游是一套共享的静态 cookie，拿不到
当前运营用户真实的 `menu_keys[]`。所以 manifest 只**携带** `menu_key` 作为信息，
tool 的回答里应说明「能否打开取决于你的后台权限」。

（前端 `src/config/menuUrlMappings.ts` 里有 `menuKey → api_path` 的现成桥，将来真要映射从这里起。）

## 谁在读这份数据

`internal/navigation` 通过 `go:embed` 在构建期把 `pages.yaml` 读进二进制
（本目录的 `manifest.go` 只有六行，存在的唯一原因是 `go:embed` 够不到自己包目录以上）。
所以改完 manifest 要**重新编译**才生效，运行期不会重读文件。

两个 MCP tool：`admin_console_find_page`（检索）、`admin_console_get_page`（按 id 取单页）。
检索按 §12.6 做确定性匹配，没有向量检索；没有中文分词器，所以走字符 bigram + 子串包含。
绝对链接由 MCP 侧的 `admin_console.base_url` 拼出，manifest 只存相对 `route`。
深链只对那 4 条动态路由按 `entity_params` 拼装，且由服务端拼，不让模型自由拼 URL（§13.4）。

启动时会校验：route 必须是以 `/` 开头的纯路径（不含 `..`、`://`、空白、`#`、`?`），
`entity_params` 里的名字必须真的以 `:name` 出现在 route 里，`id` 唯一。任一条不满足
就拒绝启动 —— 拿没校验过的数据拼出来的链接会直接递到运营手上。

### known_gaps 的 keywords

每条 gap 都要写 `keywords`，检索按关键词精确匹配，不对 topic 做启发式。
四个字的 topic（如「公告管理」）靠启发式要么漏掉真实问法（「怎么给客户发公告」），
要么在任何含「管理」的句子上误命中。匹配是**单向**的：查询里必须出现 gap 关键词；
反向（关键词包含查询）会让权限那条 gap 在任何提到「权限」的问题上冒出来。

生成器原样搬运 `known_gaps`，加字段不需要改生成器。
