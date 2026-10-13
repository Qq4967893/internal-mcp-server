# AI Copilot 认证与授权设计

- 日期：2026-10-09
- 范围：`ai-copilot-web`、`ai-copilot-api`、`internal-mcp-server`、`admin-api`（= `mangox-blockmatrix`）
- 状态：设计已确认，待出实施计划

---

## 1. 目标

让 `ai-copilot-web` 拥有自己的登录页，用户与权限完全复用 admin-api，不复制第二份用户体系。同时补掉 MVP 遗留的两个真实缺口：

1. `ai-copilot-api` 的用户身份是配置里的固定开发用户（`app/auth/dependency.py`）。
2. `internal-mcp-server` 的 Tool Authorization 读调用方自己声称的 scopes，且调用 admin-api 时使用一套共享静态 session cookie —— 下游拿不到真实运营用户，per-user 权限失效。

非目标见 §11。

---

## 2. 现状（经代码核对）

### 2.1 admin-api（Laravel 12，`mangox-blockmatrix`）

- 只有一个 `web` guard，`driver: session`，session 存库（`SESSION_DRIVER=database`）。**没有任何 token 认证包**：composer.json 里无 Sanctum / Passport / JWT，vendor 下无 `firebase/php-jwt`、无 `lcobucci/jwt`。
- 登录 `POST /api/auth/login`（`LoginController::login`）：图形验证码 → `Hash::check` → `Auth::loginUsingId()` → `session()->regenerate()` → 写入 `area_id`。该路由 `withoutMiddleware(PreventRequestForgery::class)`。
- 鉴权两个中间件：
  - `Authenticate`：`Auth::check()`，并 `define('LOGIN_USER', login_name(id))`。
  - `Permission`：权限码**就是 API 路径本身**。`AuthService::getUserMenu()` 走 `role_id → role_menus_rel → menus → menu_endpoints.api_path`，再把 `request->path()`（经 `modMethod()` 剥掉数字段）去 `in_array` 匹配；写操作额外拼 `/POST`、`/PUT`、`/DELETE`。`$filterApiArr` 是免权限白名单。
- `config/cors.php`：`supports_credentials: true`，`allowed_origins` 由 `CORS_ALLOWED_ORIGINS` 控制。

两个关键核对结论：

- **`area_id` 是死数据。** `LoginController` 第 66 行写进 session，全项目（`grep -rn "area_id" app/`）**没有任何地方读取它**。`app/` 下除这一行外不使用 `session()`。因此「不建 session 的登录路径」不会破坏任何数据范围逻辑。
- **`GET /api/managers/permissions` 返回的不是接口列表。** `AdminUserController::getPermissions()` → `AdminUserService::buildLoginUser()` 返回用户资料 + `menus[{id, name, menu_key}]` + `menu_tree` + `role_area[]`，与 blockmatrix-web 的 `ManagerPermissionsData` 类型一致。**不含 `api_path`。** 接口级权限列表只在 `AuthService::getUserMenu()` 内部算出，从未经 HTTP 暴露。`menu_key`（如 `finance.cash.deposit`）与 `api_path`（如 `deposits/statistics`）不同构、不可互推（技术设计文档第 1301–1311 行已记录此坑）。

### 2.2 internal-mcp-server（Go）

- 入口：静态 service token（`config.Auth.ServiceToken`，`subtle.ConstantTimeCompare`）+ 四个委派身份头 `X-Copilot-{User,Session,Request}-Id` / `X-Copilot-Scopes`。
- `auth/service.go`：`permissions = splitAndTrim(scopes)` —— **scopes 完全由调用方声称，MCP 不做任何验证**。
- Tool Authorization 词表是自造码（`tools.go`：`admin.api.deposits.read`、`admin.api.commissions.read`），与 admin-api 的权限词表无关。
- 下游凭证：`adapters/adminapi/credentials.go` 的 `Credentials{Cookies map[string]string}` + `StaticCredentialProvider`，取自配置文件 `admin_api.credentials.cookies`。
- `config.go` 默认监听 `127.0.0.1:8081`，但**生产形态确认为独立 Deployment、内网可路由**。

### 2.3 ai-copilot-api（FastAPI）/ ai-copilot-web（React 19 + Vite）

两边都在 MVP 时留好了身份接缝，接缝方向正确：

- `app/auth/dependency.py::get_current_identity()` 返回 `settings.auth.dev_user`，注释写明「接入真实登录后只替换这个函数体」。
- `app/auth/identity.py::mcp_headers()` 组装四个委派头。
- `src/features/auth/index.ts::useCurrentUser()` 硬编码 `{userId:'10001', displayName:'dev'}`。
- `src/api/sse.ts` 是**手写 `fetch` + POST**（非 `EventSource`），因此自定义 `Authorization` 头可用。

### 2.4 部署拓扑（已确认）

同父域、不同子域，各自前后端同源：

```
https://admin.example.com/          → blockmatrix-web
https://admin.example.com/api/*     → admin-api
https://copilot.example.com/        → ai-copilot-web
https://copilot.example.com/api/*   → ai-copilot-api
```

两个 origin，同一个 site（eTLD+1 = `example.com`）。这与现有代码一致：`src/lib/config.ts` 的 `API_BASE_URL = '/api'`、copilot-api 的路由前缀（`/api/conversations` 等）、`vite.config.ts` 本地开发代理 `/api`。该文件注释「前后端同源，浏览器不需要知道后端地址，也不会碰上 CORS」描述的正是这个拓扑，本设计必须维持这一性质（见 D12）。

**`SameSite` 不提供子域隔离。** `copilot.example.com` 与 `admin.example.com` 属同一 site，`SameSite=Strict` 的 cookie 在从后台导航过来时照样发送。子域隔离靠的是 cookie 的 `Domain` 属性 —— 见 §4.2 与 §5.6 关于「不设 `Domain`」的硬要求。

---

## 3. 决策记录

| # | 决策 | 理由 |
|---|---|---|
| D1 | 部署拓扑：**同父域不同子域**（`admin.example.com` / `copilot.example.com`），各自前后端同源 | 既定事实。因 D3 选的是 token 而非 cookie 透传，跨子域对核心机制零影响 |
| D2 | 登录入口：**只做自有登录页**，不做 SSO 跳转 | 见 §11.1（SSO 链路已论证，形态保留备查） |
| D3 | 凭证模型：**admin-api 签发委派 token**；不透传浏览器 session cookie | copilot 未来有写操作，admin-api 必须能区分「人直接操作」与「AI 代操作」；透传 session 做不到，且凭证能力过强 |
| D4 | token 实现：**非对称 JWT**，`firebase/php-jwt`（PHP）+ `golang-jwt/jwt`（Go），公钥经 JWKS 分发 | MCP 和 copilot-api 本地验签，零额外内网往返 |
| D5 | access token 存浏览器**内存**，refresh token 存 **HttpOnly cookie** | Authorization 头天然免 CSRF；CSRF 面收敛到 refresh 单端点；XSS 偷不到长期凭证 |
| D6 | token claims **不含权限列表、不含 `perm_ver`** | 权限列表对 sysAdmin 有数百条 `api_path`，会撞网关 header 上限；`perm_ver` 在签发时固化，无法反映中途的角色变更（见 §4.3 修正说明） |
| D7 | access TTL **30min**，run 开始前检查剩余寿命，不足则先要求前端 refresh | 长 agent run 可能跨越较短的 TTL；中途续期方案见 §11.2 |
| D8 | MVP 的凭证范围限制：**copilot 认证路径只接受 GET**；路由级白名单延后 | MCP 的 tool 集合已是事实白名单，GET-only 以一个判断把泄露 token 的爆炸半径压到「该用户全部只读权限」。写操作上线前必须升级为路由级白名单（§11.3） |
| D9 | Tool Authorization 词表 = admin-api 的 `menu_endpoints.api_path` | 同构词表，无映射表、不会漂移 |
| D10 | MCP 拉取权限列表失败时 **fail-open** | 真正的权限边界在 admin-api 的 `Permission` 中间件，放行不造成越权；fail-closed 会把 admin-api 的任何抖动放大成 copilot 全面不可用 |
| D11 | service token 保留，并在 MVP 实现**双 token 轮换** | 生产为独立 Deployment、内网可路由（§2.2），凭证泄露面是整个内网 |
| D12 | 登录请求**经 copilot-api 代理**（不由浏览器直连 admin-api） | 跨子域下浏览器直连 admin-api 属跨源请求，需要 admin-api 开 CORS 放行 `copilot.example.com`，且 refresh cookie 必须 `SameSite=None`（第三方 cookie，正被浏览器淘汰）。走代理则浏览器只与 `copilot.example.com` 通信，**全程零 CORS**。另外：前端只认识一个后端；换票端点得以强制要求 service token |

---

## 4. 架构设计

### 4.1 身份流全景

```
                    ┌─ 自有登录页，账号密码 ─┐
ai-copilot-web ─────┤                        │
   │  Authorization: Bearer <access token>   │
   ▼                                         │
ai-copilot-api  ──本地验签(JWKS)──> Identity  │
   │                                         │
   │  Authorization: Bearer <service token>     ← 服务身份
   │  X-Copilot-Access-Token: <用户 access token>  ← 委派凭证
   │  X-Copilot-{User,Session,Request}-Id         ← 日志关联
   ▼
internal-mcp-server ──本地验签(同一 JWKS)──> 真实 role_id
   │
   │  Authorization: Bearer <用户 access token>
   ▼
admin-api ── copilot guard ── Permission 中间件 ── 业务数据
                                  （唯一的权限边界）
```

修掉的核心缺陷：MCP 不再信任 `X-Copilot-Scopes`（调用方自称），改为从签过名的 token 读身份。ai-copilot-api 即便被攻破也无法放大权限。

### 4.2 登录链路

```
1. GET  copilot.example.com/api/auth/captcha
        └─> 服务端透传 admin.example.com/api/auth/captcha（现有端点）→ {key, img}

2. POST copilot.example.com/api/auth/login {login_name, password, captcha, key}
        │
3.      └─> POST admin.example.com/api/auth/copilot/token    （服务端调用）
             X-Service-Token: <copilot 服务身份>
             │
4.           ├─ 复用 LoginService::verifyCaptcha()
             ├─ 复用 LoginService::verifyCredentials()
             └─ 不建 session、不调 Auth::loginUsingId()
             │
5.           └─ 签发 access token (JWT, 30min) + refresh token (opaque, 8h)

6. copilot-api 落地：
        refresh token → Set-Cookie: HttpOnly; Secure; SameSite=Strict;
                                    Path=/api/auth
                                    （不设 Domain → host-only）
        access token  → 响应体（前端存内存，不进 localStorage）

7. 后续请求：Authorization: Bearer <access token>
```

浏览器只与 `copilot.example.com` 通信，全程不产生跨源请求（D12）。

**不设 `Domain` 属性是硬要求。** 顺手写成 `Domain=.example.com` 会把 refresh token 发给 `admin.example.com` 及其余所有子域，直接放弃 §12 记录的 origin 隔离收益。这一条要进 code review 检查清单 —— 它的错误形态不产生任何功能异常，只是静默扩大泄露面。

### 4.3 token 设计

access token（JWT，RS256 或 EdDSA）：

```json
{
  "iss": "admin-api",
  "aud": ["copilot"],
  "sub": "10001",
  "login_name": "zhangsan",
  "role_id": 7,
  "is_sys_admin": false,
  "actor": "ai-copilot",
  "jti": "...",
  "iat": 1760000000,
  "exp": 1760001800
}
```

- `actor` 是 AI 审计标记的来源（§5.5）。
- **不放权限列表**：`getUserMenu()` 对 sysAdmin 返回数百条 `api_path`，逐请求携带会撞网关 8KB header 上限。
- **不放 `perm_ver`**：设计过程中曾打算用它做 MCP 缓存失效键，但 token 在登录时签发，其中的 `perm_ver` 是签发那一刻的值；管理员中途修改角色菜单时它不会变，缓存照样命中旧权限直到 token 刷新。改为 MCP 按 `role_id` 缓存、TTL 60 秒：陈旧窗口从一个 access TTL（30min）收到 60 秒，实现更简单。

refresh token：不透明随机串，存 Redis。

### 4.4 权限的两个正交轴

```
AI 实际能调 = (该用户的 api_path 权限)  ∩  (copilot 凭证被授予的范围)
              └── 已实现，不改 ──┘        └──── D8 新增 ────┘
```

第一个轴回答「这个人能看什么」，由 admin-api 的 `Permission` 中间件判定，是**唯一的权限边界**。

第二个轴是 OAuth scope 的概念：用户把自己权限的哪个子集委托给了 copilot 这个客户端。它防的是**凭证泄露后绕过 MCP 直连 admin-api**：没有这一层，泄露的 access token 可以调用该运营用户有权限的任何端点，包括 `PUT managers/reset`（重置他人密码）、`PUT managers/change/{id}/{status}`（禁用账号）。

MVP 用 GET-only 实现第二个轴（D8）。这足够的前提是：MCP 没有「调用任意端点」的通用 tool，每个 tool 调的端点在代码里写死，所以走 MCP 正常路径时 AI 能碰的端点早已被 tool 集合限死。

---

## 5. admin-api 改动

### 5.1 新增 `copilot` guard

```php
// config/auth.php —— 'web' 完全不动
'guards' => [
    'web'     => ['driver' => 'session',       'provider' => 'users'],
    'copilot' => ['driver' => 'copilot-token', 'provider' => 'users'],
],
```

新建 `CopilotAuthServiceProvider`，内部 `Auth::extend('copilot-token', ...)`，返回实现 `Illuminate\Contracts\Auth\Guard` 的 `CopilotTokenGuard`：

1. 取 `Authorization: Bearer`，验签并校验 `iss`、`aud=copilot`、`exp`
2. 查撤销名单 `copilot:revoked:<jti>`
3. 按 `sub` 经 `AdminUserRepository` 取 `AdminUser`，**同时校验 `status === AdminUser::STATUS_NORMAL`**
4. 将 claims 挂到 request，供审计使用

第 3 步保证账号被禁用或删除时已签发的 token 立即失效；反正解析 token 后必须取用户对象，顺带校验不增加开销。

Guard 只回答「这个 token 对应哪个用户」。GET-only 限制（D8）不放在 Guard 内 —— Guard 不应检查 HTTP 方法。它与 AI 审计标记一并放在 `Authenticate` 中间件的 copilot 分支（§5.4、§5.5）。

### 5.2 抽取登录逻辑（抽取，不改行为）

`LoginController::login()` 当前把验证码校验、密码校验、建 session 焊在一起。抽两个方法到 `LoginService`：

```php
verifyCaptcha(string $key, string $captcha): void          // 失败抛 BusinessException
verifyCredentials(string $loginName, string $password): array  // 返回 userInfo 或抛
```

`LoginController::login()` 改为调这两个 + 保留原有 `Auth::loginUsingId()` / `session()->regenerate()` / `area_id` 写入。**外部行为必须逐字节不变。**

抽取的理由：密码校验只能有一份实现。两个入口各写一份 `Hash::check`，将来加密码策略（过期强制改密、连续失败锁定）必然漏掉一边。

### 5.3 新增端点

挂在现有 `Route::prefix('auth')` 组下：

| 端点 | 中间件 | 说明 |
|---|---|---|
| `POST /api/auth/copilot/token` | `throttle:login` + 服务身份 | 密码换 token |
| `POST /api/auth/copilot/refresh` | 服务身份 | 换新 access，轮换 refresh |
| `POST /api/auth/copilot/logout` | `auth:copilot` | 撤销 refresh + `jti` 入名单 |
| `GET /api/auth/copilot/permissions` | `auth:copilot` | 返回 `api_path[]` |
| `GET /api/auth/copilot/jwks` | 公开 | 公钥，供 MCP / copilot-api 验签 |

验证码复用现有 `GET /api/auth/captcha`，不开新端点。

`GET /api/auth/copilot/permissions` 补的是 §2.1 指出的缺口：暴露 `AuthService::getUserMenu()` 的结果（`api_path` 列表），这是 `managers/permissions` 不提供的。它**不能**挂 `Permission` 中间件（会死锁：要查权限得先有权限），与现有 `managers/permissions` 走 `$filterApiArr` 白名单同理。

### 5.4 `Authenticate` 中间件改造（多 guard 支持）

MVP 不逐个标注路由（D8），因此现有的 `['auth', 'permission']` 路由组整体改为 `['auth:web,copilot', 'permission']`，GET-only 作为唯一的凭证范围限制。§11.3 的升级做法是把 AI 可访问端点拆成独立一组。

**这里有个必须动手的点：现有 `Authenticate` 不是 Laravel 自带的那个。** `bootstrap/app.php` 第 29 行把 `'auth'` 别名指向自写的 `App\Http\Middleware\Authenticate`，其签名为 `handle($request, Closure $next)` —— 不接收 guard 参数，内部直接 `Auth::check()` 走默认 guard（`web`）。而 PHP 对用户定义函数的多余实参是静默忽略的，所以 `auth:web,copilot` **不会报错，但也完全不生效**，copilot token 会被当作未登录拒掉。

必须改成：

```php
public function handle($request, Closure $next, ...$guards)
{
    $guards = $guards ?: [config('auth.defaults.guard')];

    foreach ($guards as $guard) {
        if (Auth::guard($guard)->check()) {
            Auth::shouldUse($guard);   // 让 Permission 中间件的 Auth::user() 取到人
            return $this->afterAuthenticated($request, $guard, $next);
        }
    }

    return $this->fail(trans('middleware.permission.login_required'), 10000);
}
```

`afterAuthenticated()` 承载两件事：原有的 `LOGIN_USER` 定义（按 guard 分支，§5.5），以及 copilot 分支的 GET-only 检查。把 GET-only 放在这里而不是路由上，是为了让它无法被漏配 —— 任何经 copilot guard 认证通过的请求都必然流经这一点。

不传 guard 参数时回退默认 guard，保证任何漏改的调用点行为不变。

`Permission` 中间件**不需要修改**：`Auth::shouldUse($guard)` 之后它的 `Auth::user()` 正常解析，`role_id → menu_endpoints.api_path` 的判定对两种 guard 完全一致 —— 这正是「权限边界只有一处」的体现。

### 5.5 AI 写操作审计标记

现有 `Authenticate` 中间件第 44 行的 `LOGIN_USER` 常量被 20+ 处审计日志引用（`AdminUserService`、`RoleService`、`MenuService`、`WhiteListService`、`PositionEntryService` 等）。§5.4 的 `afterAuthenticated()` 按 guard 分支定义它即可：

```php
$marker = $guard === 'copilot' ? '[AI]' : '';
defined('LOGIN_USER') || define('LOGIN_USER',
    $user['login_name'] . '(' . $user['id'] . ')' . $marker);
```

**所有现存审计日志自动带上 AI 标记，20+ 调用点零改动。** 等 copilot 开放写操作时，审计线已就位。

用 `defined() ||` 而非裸 `define()`，避免将来两个中间件叠加时的重定义 warning。

这是 D3 最直接的兑现：透传 session 方案下 admin-api 看到的是普通浏览器请求，这个标记无从谈起。

### 5.6 token 存储与撤销

| 对象 | 存储 | TTL | 内容 |
|---|---|---|---|
| access token | 不存（无状态 JWT） | 30min | 见 §4.3 |
| refresh token | Redis `copilot:refresh:<hash>` | 8h | `{user_id, issued_at, rotated_from}` |
| 撤销名单 | Redis `copilot:revoked:<jti>` | = access 剩余寿命 | logout / 改密 / 禁用时写入 |

**refresh 轮换 + 盗用检测**：每次 refresh 作废旧串、发新串。若收到一个已被轮换掉的旧串 → 判定凭证泄露，撤销该用户全部 refresh 并记告警。

**改密联动**：`AdminUserService::resetPassword()` 与 `changePassword()` 追加「撤销该用户全部 copilot refresh」。

---

## 6. internal-mcp-server 改动

### 6.1 身份传递

```
ai-copilot-api → internal-mcp-server
  Authorization: Bearer <service token>          服务身份（保留）
  X-Copilot-Access-Token: <用户 access token>    新增
  X-Copilot-{User,Session,Request}-Id            保留，降级为日志关联字段
  X-Copilot-Scopes                               删除
```

MCP 用 JWKS 公钥本地验签 `X-Copilot-Access-Token`，从 claims 取 `sub`、`role_id`。`X-Copilot-User-Id` 必须与 token 的 `sub` 一致，否则 401 —— 它从「身份来源」降级为「冗余校验 + 日志字段」。

代码落点：

- `internal/auth/identity.go`：`Identity` 增加 `RoleID`、`RawToken`；`Permissions` 字段语义由「调用方声明」改为「从 admin-api 拉取并缓存」。
- `internal/auth/service.go`：`Middleware(serviceToken, defaultPermissions, next)` 改为 `Middleware(serviceToken, verifier, next)`。`defaultPermissions` 删除 —— 它存在的前提是「调用方可能不传 scopes」，而 scopes 不再由调用方提供。
- `internal/config/config.go`：删除 `AuthConfig.DefaultPermissions` 与 `defaultPermission = "admin.api.ping.read"` 常量。

### 6.2 下游凭证

`adapters/adminapi/credentials.go` 的 `CredentialProvider` 接口正是为此预留的接缝：

```go
type Credentials struct {
    Cookies     map[string]string  // 保留，仅本地开发
    BearerToken string             // 新增
}

// 从 context 中的 Identity 取 RawToken
type DelegatedCredentialProvider struct{}
```

`credentialTransport.RoundTrip` 优先使用 `BearerToken`；存在 bearer 时不发 `Cookie` 头。

**配置文件中的静态 cookies 降级为「仅本地调试」**，并新增启动校验：**非 dev 环境配置了 `admin_api.credentials.cookies` → 拒绝启动。**

这条校验不是洁癖。否则生产环境残留的静态 cookie 配置等于那个共享超级账号的后门长期敞开；而因为 bearer 优先、它平时根本不会被使用，不会有任何症状提示它还在。

### 6.3 Tool Authorization

删除自造权限码（`tools.go` 第 19–20 行的 `depositsPermission` / `commissionsPermission`）。改为每个 tool 声明它实际调用的 admin-api endpoint：

```go
tool "deposits.search"      → requiredEndpoints: ["api/deposits"]
tool "deposits.statistics"  → requiredEndpoints: ["api/deposits/statistics"]
```

这些字符串与 `menu_endpoints.api_path` 同构 —— 同一词表、无映射表、不会漂移（D9）。

**`api/` 前缀必须有。** `Permission` 中间件比对的是 `modMethod($request->path())`，对 `https://host/api/deposits` 得到 `api/deposits`；`BlockmatrixInitSeeder` 里的实际值也是 `api/menus`、`api/menus/POST`。而 MCP adapter 的端点字符串是相对 `base_url` 的（`config.local.json` 的 `base_url` 已含 `/api`，所以 `deposits.go` 写 `/deposits`）。两者差一个前缀，必须显式换算而不是直接复用：

```go
// 权限码 = "api" + adapter 端点；一个函数，不是一张表
func permissionCode(endpoint string) string { return "api" + endpoint }
```

写操作的权限码还要拼方法后缀（`api/menus/POST`、`api/menus/status/PUT`），MVP 为 GET-only（D8）故不涉及；§11.3 升级时需一并处理。

**顺带修一个现有 bug：** `adapters/adminapi/client.go` 的 `Health()` 用的端点是 `/api/managers/permissions`，与 `base_url` 的 `/api` 拼成 `/api/api/managers/permissions`。它被自身注释掩盖了 —— 「any response proves the service is reachable, even when it reports an HTTP or business error」使得 404 也算探活成功。改为相对端点 `/managers/permissions`。

判定：`requiredEndpoints ⊆ api_paths`，其中 `api_paths` 来自 `GET /api/auth/copilot/permissions`，按 `role_id` 缓存 60 秒。

**这一层不是安全边界，是 UX 层。** 作用：①不把用户用不了的 tool 暴露给模型；②过不了就提前拒绝，省一次往返；③让模型收到「你没有这个权限」而不是一个裸 403 去自由发挥。真正的边界永远是 admin-api 的 `Permission` 中间件。

因此拉取权限列表失败时 **fail-open**（D10）：放行，让 admin-api 去拒。这读起来像「权限检查失败时放行」，在安全评审中需要解释，理由必须随代码注释一并写明 —— 边界在 admin-api，放行不造成越权；反之 fail-closed 会把 admin-api 的任何抖动放大成 copilot 全面不可用。

### 6.4 service token 轮换（MVP 必做）

生产为独立 Deployment、内网可路由，因此：

1. service token 从配置文件移至环境变量 / 密钥管理，配置文件不留明文。
2. 支持**双 token 并存**（`service_token` + `service_token_next`），两者都接受，轮换时无需停机。对两个都执行 `subtle.ConstantTimeCompare`，不引入时序侧信道。

service token 与 user token 是两条不相交的泄露路径（前者走服务端环境变量/密钥管理，后者走浏览器），这是它在已有 per-user token 之后仍然成立的主要价值；其次是限流维度（`httpapi/server.go` 的限流键已经是 `service:<id>:user:<id>`）和未来多调用方的扩展点。

完整 mTLS 或短命服务 JWT 见 §11.4。

---

## 7. ai-copilot-api 改动

| 文件 | 改动 |
|---|---|
| `app/auth/dependency.py` | `get_current_identity()` 改为解析 `Authorization` 头 + 本地验签（JWKS 带缓存）。这正是该函数注释预留的改动点 |
| `app/auth/identity.py` | `Identity` 增加 `role_id`、`raw_token`；`mcp_headers()` 删除 `X-Copilot-Scopes`、新增 `X-Copilot-Access-Token` |
| `app/api/auth.py`（新增） | login / refresh / logout / captcha 四个端点，代理 admin-api 并管理 refresh cookie（D12） |
| admin-api client（新增） | copilot-api 目前只认识 MCP。这是一条新增的、显式的、单向的依赖边，仅用于认证 |
| `app/config/settings.py` | 删除 `auth.dev_user`；新增 admin-api base_url、service token、JWKS URL |

`identity.py` 顶部注释的预判成立：`Identity` 字段需扩展，但没有调用方需要修改。

---

## 8. ai-copilot-web 改动

| 改动 | 内容 |
|---|---|
| 新增 `features/auth/LoginPage.tsx` | 账号 / 密码 / 验证码表单，验证码可点击刷新 |
| 新增 `features/auth/AuthProvider.tsx` | access token 存 React state（内存），不进 localStorage |
| 改 `features/auth/index.ts` | `useCurrentUser()` 从 context 读取，不再硬编码 |
| 改 `api/client.ts` | 加 `Authorization` 头；401 拦截 + **single-flight refresh** + 重放 |
| 改 `api/sse.ts` | 加 `Authorization` 头；401 处理策略不同（见下） |
| 改 `routes/router.tsx` | `/login` 路由 + `ProtectedRoute` 包住现有路由 |
| 新增启动 bootstrap | 应用挂载时先 refresh，期间 loading；失败跳 `/login` |

### 8.1 refresh 的触发时机（全部自动，业务代码永不调用）

1. **页面加载**：access token 在内存，刷新页面即丢失。启动时先 `POST /api/auth/refresh`，拿到前路由停在 loading；失败跳登录页。这是 D5 的代价——一次几十毫秒的白屏。
2. **请求遇到 401**：拦截器暂停 → refresh → 成功则重放原请求，失败则跳登录页。采用被动刷新而非定时主动刷新：主动方案要处理标签页休眠、客户端时钟漂移、多标签页竞争，复杂度明显更高，收益仅是省掉一次 401 往返。
3. **用户登出**：不 refresh，调 logout 撤销 refresh token 并清 cookie。

### 8.2 single-flight 是必须做对的地方

页面上常有多个请求并发，一起 401 会打出 N 个并发 refresh。而 refresh token 是轮换的：第一个成功后作废旧串，剩下 N-1 个会被 §5.6 的盗用检测判定为重放，触发该用户全量撤销，用户直接被踢出登录。

必须实现为「一个 in-flight refresh Promise，所有等待者复用」。其故障表现是「用户莫名被踢出登录」—— 难复现、难归因，因此值得单独写并发测试。

### 8.3 流式请求的 401 不能重放

`api/sse.ts` 是流。若已吐出部分 token 再遇 401，重放会让用户看到内容从头重复。该文件头部注释已因同样理由弃用 `@microsoft/fetch-event-source`（它会自动重连并重发）。

处理：建连时刚校验过 token，加上 D7 的「run 前检查剩余寿命」，流中途 401 应极罕见。真遇到就作为错误报给用户，**不自动重放**。中途续期方案见 §11.2。

---

## 9. 错误处理约定

三套错误信封需对齐：admin-api 为 `{is_succ, code, message, data}`，copilot-api 另一套，MCP 为 `{error:{code,message}}`。

copilot-api 将 admin-api 的登录失败统一翻译为：

- `AUTH_INVALID_CREDENTIALS`
- `AUTH_CAPTCHA_INVALID`
- `AUTH_CAPTCHA_EXPIRED`
- `AUTH_ACCOUNT_DISABLED`
- `AUTH_RATE_LIMITED`

前端按 code 出文案，**不透传 admin-api 的原始 message**。现有 `LoginController` 的注释「模糊错误，避免枚举」已在防账号枚举，翻译层不得破坏这一点。

**401 与 403 必须分清，这是前端拦截器的分叉点：**

- `401` = token 无效 / 过期 → 触发 refresh 或跳登录
- `403` = 权限不足 → 直接显示给用户，**绝不触发 refresh**

混淆的后果：用户问了个超出权限的问题，前端误判为登录过期，refresh 一圈后仍 403，最终把人踢到登录页。MCP 现有的 `UNAUTHENTICATED` / `FORBIDDEN` 两个 code 对得上，保持不变。

---

## 10. 测试策略

遵循 TDD：先写失败的测试。

**admin-api**

- `CopilotTokenGuard` 单测：验签通过 / 签名错误 / 过期 / `aud` 不匹配 / `jti` 已撤销 / 用户 `status != STATUS_NORMAL`
- `Authenticate` 多 guard 单测：session 用户走 `web` 分支 / copilot token 走 `copilot` 分支 / 两者皆无 → 401 / 不传 guard 参数时回退默认 guard
- copilot token 打非 GET 端点 → 被拒；session 用户打同一端点 → 正常
- `LOGIN_USER` 在 copilot 分支带 `[AI]` 后缀、在 web 分支不带
- `POST /api/auth/copilot/token` feature 测试：成功、密码错、验证码错、验证码过期、缺 service token、限流
- refresh 轮换 + 盗用检测（用已轮换掉的旧串 → 全量撤销）
- `GET /api/auth/copilot/permissions` 返回的 `api_path[]` 与 `Permission` 中间件实际使用的集合一致
- **抽取的验收标准：现有登录相关测试一行不改、全部通过**

**internal-mcp-server**

- `internal/auth/service_test.go` 三个用例重写（现断言的是基于 scopes 头的旧行为）
- 新增签发测试 token 的 helper + 假 JWKS endpoint
- `DelegatedCredentialProvider`：bearer 优先；有 bearer 时不发 `Cookie` 头
- 非 dev 环境配置了 `credentials.cookies` → 拒绝启动
- Tool Authorization：`requiredEndpoints ⊆ api_paths` 的通过 / 拒绝；拉取权限失败 → fail-open
- `X-Copilot-User-Id` 与 token `sub` 不一致 → 401
- 双 service token 均被接受
- `internal/mcpserver/integration_test.go` 适配

**ai-copilot-api**

- `get_current_identity()` 验签：有效 / 过期 / 签名错 / `aud` 不匹配
- JWKS 缓存行为
- `mcp_headers()` 新形状

**ai-copilot-web**

- single-flight refresh 并发用例（§8.2）
- 401 → refresh → 重放；403 → 不 refresh
- 流式请求中途 401 → 报错不重放
- 未登录访问受保护路由 → 跳 `/login`

**端到端**

一个真实运营账号登录 → 提问一个需要 deposits 权限的问题 → 换成无该权限的角色 → 确认被拒且提示合理。这条串起全部四层，是唯一能验证「权限真的来自 admin-api」的测试。

---

## 11. 不做什么 / 演进路线

### 11.1 不做 SSO 跳转入口（D2）

链路已论证过，形态保留备查，以便将来需要时直接实施：

- **SSO-A（cookie bootstrap 换票）**：**在 D1 的拓扑下不可用。** 它要求 admin session cookie 能被带到 copilot 的后端，而跨子域下这需要把 `SESSION_DOMAIN` 放宽到 `.example.com` —— 等于把后台 session cookie 暴露给所有子域，是明确的安全降级。记录于此仅为说明它为何被排除。
- **SSO-B（一次性 handoff code）**：本拓扑下唯一可行的 SSO 形态，且完全不受子域影响。blockmatrix-web 先调 `admin.example.com/api/auth/copilot/handoff` 取一次性 code（Redis，TTL 30s，单次使用），跳转 `https://copilot.example.com/#/sso?code=xxx`（放 fragment：不进网关访问日志、不进 Referer），copilot-web 读出后立即 `history.replaceState` 清 URL，再经 copilot-api 换票。admin session cookie 全程不离开 `admin.example.com`。代价：blockmatrix-web 需改动，admin-api 多一个端点和一份 Redis 状态。

两者都可与自有登录页共用 `POST /api/auth/copilot/token` 这一个出口 —— 入口汇聚到同一签发端点，下游链路不感知用户从哪进入。

### 11.2 access token 中途续期（D7 的方案 iii）

MCP 调用下游失败时回报明确的 `TOKEN_EXPIRED`，由 copilot-api 刷新后重试该次 tool 调用。MVP 不做；它能让长 agent run 彻底摆脱 TTL 约束，也是 §8.3 的根本解法。

### 11.3 路由级白名单（D8 的升级项，**写操作上线前必做**）

```php
Route::group(['middleware' => ['auth:copilot', 'permission']], function () {
    Route::get('deposits',            [DepositController::class, 'list']);
    Route::get('deposits/statistics', [DepositController::class, 'statistics']);
    // ...
});
```

默认拒绝体现在路由定义本身：读 `routes/web.php` 就能看出哪些端点对 AI 开放，无需跨文件对照配置表。写操作落地时这一层是必需的 —— 那时 GET-only 不再是约束，而「copilot token 顺带能调重置他人密码」这件事必须被显式排除，即使该运营用户在后台确实有那个权限。

### 11.4 服务身份体系

完整 mTLS 或短命可轮换的服务 JWT，替代静态共享 service token。若公司内网已有统一服务身份体系，直接接入，`service_token` 可撤除。

### 11.5 明确不做

- 不在 copilot 侧复制用户表、角色表、菜单表。
- 不实现密码修改 / 重置 / 账号管理 UI —— 这些留在后台，copilot 只做登录。
- 不实现完整 OIDC（同域、同一团队维护，不需要多租户与同意页）。
- 不实现记住登录 / 长期免登录。

---

## 12. 风险与已知取舍

| 风险 | 缓解 |
|---|---|
| access token 穿过三个服务，任一处日志误打即泄露 | TTL 30min；GET-only（D8）压缩爆炸半径；三处均需审查日志不打 `Authorization` 头和 `X-Copilot-Access-Token` |
| service token 是静态共享串，泄露后长期有效 | 移至密钥管理 + 双 token 轮换（§6.4）；长期见 §11.4 |
| fail-open 在安全评审中易被误读 | 理由随代码注释写明；§6.3 已记录完整论证 |
| MCP 权限缓存最长 60 秒陈旧 | 不是安全问题（边界在 admin-api）；最坏后果是 tool 列表略旧或多一次 403 |
| `LoginController` 抽取改动了生产登录路径 | 验收标准是现有测试一行不改全部通过（§10） |
| 生产残留静态 cookie 配置成为静默后门 | 非 dev 环境配置即拒绝启动（§6.2），并有对应测试 |
| 启动 bootstrap 带来一次白屏 | D5 的已知代价；loading 态需要设计得不突兀 |
| refresh cookie 若被设成 `Domain=.example.com`，即泄露给所有子域 | 必须不设 `Domain`（host-only）；§4.2 已明确，列入 code review 检查项。错误形态无功能症状，只能靠评审拦住 |

**D1 拓扑带来的一项额外收益**：`copilot.example.com` 与 `admin.example.com` 是两个 origin，浏览器层面互相隔离 —— 后台前端若出现 XSS 读不到 copilot 的 access token，反之亦然。若两者同 origin（例如同域不同路径），任一侧的 XSS 爆炸半径都会覆盖另一侧。
