# AI Copilot 认证与授权 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 ai-copilot-web 拥有自己的登录页，用户与权限完全复用 admin-api，并把 copilot 链路的身份从「固定开发用户 + 调用方自称的 scopes + 共享静态 cookie」换成「admin-api 签发的短命委派 token，逐跳验签，下游按真实运营用户鉴权」。

**Architecture:** admin-api 新增一个 `copilot` token guard 与签发端点，用非对称 JWT（RS256）签发 access token，公钥经 JWKS 分发；ai-copilot-api 与 internal-mcp-server 各自用公钥本地验签，MCP 把用户 token 原样透传给 admin-api，由 admin-api 既有的 `Permission` 中间件做最终鉴权。浏览器只与 `copilot.example.com` 通信，access token 存内存、refresh token 存 HttpOnly cookie。

**Tech Stack:** admin-api = Laravel 12 + PHP 8 + `firebase/php-jwt` + Redis + PHPUnit 11；internal-mcp-server = Go 1.25 + `golang-jwt/jwt/v5` + `go test`；ai-copilot-api = FastAPI + pydantic-settings + `pyjwt[crypto]` + pytest；ai-copilot-web = React 19 + Vite 7 + Vitest 3 + Testing Library。

**Spec:** `docs/superpowers/specs/2026-10-09-copilot-auth-design.md`

---

## Global Constraints

下列取值从 spec 逐字复制，每个 Task 的要求都隐含包含本节。

- **签名算法：RS256**（非对称）。私钥只存在于 admin-api；公钥经 `GET /api/auth/copilot/jwks` 分发。
- **access token TTL：30 分钟**（spec D7）。**refresh token TTL：8 小时**（spec §5.6）。**MCP 权限列表缓存 TTL：60 秒**（spec §4.3）。
- **access token claims（spec §4.3，字段集合是封闭的）：**
  `iss`（固定 `"admin-api"`）、`aud`（固定 `["copilot"]`）、`sub`（`admin_user.id` 的**字符串**形式）、`login_name`、`role_id`、`is_sys_admin`、`actor`（固定 `"ai-copilot"`）、`jti`、`iat`、`exp`。
  **不含权限列表，不含 `perm_ver`。**
- **refresh cookie 属性：** `HttpOnly; Secure; SameSite=Strict; Path=/api/auth`，**不设 `Domain`**（host-only，仅 `copilot.example.com`）。
- **Redis 键名（spec §5.6）：** `copilot:refresh:<hash>`（TTL 8h）、`copilot:revoked:<jti>`（TTL = access 剩余寿命）。
- **权限码格式：** `api/` 前缀 + 路径，如 `api/deposits`、`api/deposits/statistics`。换算关系 `permissionCode = "api" + adapterEndpoint`（spec §6.3）。
- **MVP 凭证范围：copilot 认证路径只接受 GET**（spec D8）。
- **MCP 拉取权限列表失败时 fail-open**（spec D10）—— 放行，由 admin-api 去拒。
- **copilot 认证/授权失败必须返回真实 HTTP 状态码 401 / 403**，不可使用项目默认的 `fail($msg, $code)`（其 `$status` 默认为 200）。理由见 Review Focus 第 1 条。
- **错误码（spec §9）：** `AUTH_INVALID_CREDENTIALS`、`AUTH_CAPTCHA_INVALID`、`AUTH_CAPTCHA_EXPIRED`、`AUTH_ACCOUNT_DISABLED`、`AUTH_RATE_LIMITED`。不透传 admin-api 的原始 message（防账号枚举）。
- **`LoginController` 抽取的验收标准：现有登录相关测试一行不改、全部通过**（spec §5.2）。
- **非 dev 环境配置了 `admin_api.credentials.cookies` → MCP 拒绝启动**（spec §6.2）。
- **service token 支持双 token 并存**（`service_token` + `service_token_next`），两者都用 `subtle.ConstantTimeCompare` 比较（spec §6.4）。

---

## Review Focus

spec 隐含要求、但没有任务的测试会自然覆盖、且最可能咬到使用者的五类输入／失败模式。每条都已在「拥有该代码的 Task」里补了对应测试。

1. **鉴权失败返回 HTTP 200。** 本项目 `Response::fail()` 的 `$status` 默认 **200**，现有 `Authenticate` 和 `Permission` 都直接用默认值 —— 也就是说「未登录」和「无权限」目前都是 `HTTP 200 + is_succ:false`。若 copilot 路径沿用这个默认值，前端拦截器永远看不到 401，refresh 逻辑完全不会触发，用户 token 过期后会卡死在「请求都成功但内容是错误信封」的状态里。copilot 的认证/授权响应必须显式传 `$status`。→ 测试在 **Task 5**（中间件返回 401/403）与 **Task 6**（端点返回 401）。
2. **多服务验签的时钟偏移。** 同一个 token 被 admin-api、MCP、copilot-api 三处独立验签，三者时钟各自漂移。零 leeway 的 `exp` 校验会在边界产生零星、不可复现的 401。spec 对此沉默。统一使用 **60 秒 leeway**。→ 测试在 **Task 7**（MCP）与 **Task 11**（copilot-api）。
3. **并发 401 触发多次 refresh，导致用户被踢出登录。** refresh token 是轮换的，并发 refresh 中第一个成功后其余会被盗用检测判定为重放，触发该用户全量撤销。spec §8.2 点名了这件事，其故障表现（「用户莫名被登出」）难复现难归因。→ 测试在 **Task 15**（single-flight 并发用例）。
4. **refresh cookie 被设上 `Domain` 属性。** 写成 `Domain=.example.com` 后一切功能正常 —— 登录、刷新、登出全部照常工作 —— 只是 refresh token 从此发给 `admin.example.com` 及其余所有子域。这个错误没有任何功能症状，只能靠断言拦住。→ 测试在 **Task 13**（断言 `Set-Cookie` 不含 `Domain=`）。
5. **委派身份头与 token 主体不一致。** `X-Copilot-User-Id` 降级为日志字段后，若不校验它与 token `sub` 一致，日志与审计会指向一个与实际鉴权主体不同的人 —— 出事时追溯到错误的运营人员。→ 测试在 **Task 8**（不一致即 401）。

---

## File Structure

### admin-api（`/Users/antony/Workspace/mangox/mangox-blockmatrix`）

| 文件 | 职责 |
|---|---|
| `config/copilot.php`（新建） | copilot 认证的全部可调参数：密钥路径、TTL、Redis 前缀、service token |
| `app/Services/Blockmatrix/Copilot/TokenIssuer.php`（新建） | 只做签发与验签：造 JWT、造 refresh 串、验签返回 claims |
| `app/Services/Blockmatrix/Copilot/TokenStore.php`（新建） | 只碰 Redis：refresh 的存/取/轮换/撤销、jti 撤销名单 |
| `app/Services/Blockmatrix/Copilot/CopilotAuthService.php`（新建） | 编排：密码换票、refresh 换票、登出 |
| `app/Auth/CopilotTokenGuard.php`（新建） | 实现 `Illuminate\Contracts\Auth\Guard`，解析 token → `AdminUser` |
| `app/Providers/CopilotAuthServiceProvider.php`（新建） | `Auth::extend('copilot-token', ...)` |
| `app/Http/Controllers/Blockmatrix/CopilotAuthController.php`（新建） | 5 个端点的 HTTP 层 |
| `app/Services/Blockmatrix/LoginService.php`（改） | 抽出 `verifyCaptcha()` / `verifyCredentials()` |
| `app/Http/Controllers/Blockmatrix/LoginController.php`（改） | 改为调用抽出的方法，行为不变 |
| `app/Http/Middleware/Authenticate.php`（改） | 支持多 guard；copilot 分支加 GET-only 与 `[AI]` 审计标记 |
| `config/auth.php`（改） | 注册 `copilot` guard |
| `routes/web.php`（改） | 新增 copilot 认证路由；主路由组改为 `auth:web,copilot` |
| `app/Services/Blockmatrix/AdminUserService.php`（改） | 改密/重置密码时撤销该用户全部 copilot refresh |

三个 Service 拆开是因为它们的依赖完全不同：`TokenIssuer` 只依赖密钥和时钟，`TokenStore` 只依赖 Redis，`CopilotAuthService` 依赖前两者加 `LoginService`。合成一个文件会让「签发」的单测被迫拉起 Redis。

### internal-mcp-server（`/Users/antony/Workspace/mangox/ai-copilot/internal-mcp-server`）

| 文件 | 职责 |
|---|---|
| `internal/auth/verifier.go`（新建） | JWKS 拉取与缓存、RS256 验签、claims 结构 |
| `internal/auth/permissions.go`（新建） | 按 `role_id` 拉取并缓存 `api_path[]`（TTL 60s），fail-open |
| `internal/auth/identity.go`（改） | `Identity` 增加 `RoleID`、`RawToken` |
| `internal/auth/service.go`（改） | 删 scopes 头；改为验签 + `sub` 一致性校验；双 service token |
| `internal/adapters/adminapi/credentials.go`（改） | `Credentials` 增加 `BearerToken`；新增 `DelegatedCredentialProvider` |
| `internal/adapters/adminapi/client.go`（改） | 修 `Health()` 的双 `/api` 前缀 bug |
| `internal/config/config.go`（改） | 删 `DefaultPermissions`；加 JWKS URL、`service_token_next`；非 dev 禁止静态 cookie |
| `internal/mcpserver/tools.go`（改） | 权限码改为 `api/` 前缀的 endpoint 词表 |

### ai-copilot-api（`/Users/antony/Workspace/mangox/ai-copilot/ai-copilot-api`）

| 文件 | 职责 |
|---|---|
| `app/auth/verifier.py`（新建） | JWKS 缓存 + RS256 验签 |
| `app/auth/identity.py`（改） | `Identity` 加 `role_id`/`raw_token`；`mcp_headers()` 换头 |
| `app/auth/dependency.py`（改） | `get_current_identity()` 改为解析 `Authorization` |
| `app/auth/admin_client.py`（新建） | 调 admin-api 认证端点的唯一出口 |
| `app/api/auth.py`（新建） | login / refresh / logout / captcha 四个端点 + cookie 管理 |
| `app/config/settings.py`（改） | 删 `auth.dev_user`；加 `auth.admin_api_base_url` / `jwks_url` / `service_token` |

### ai-copilot-web（`/Users/antony/Workspace/mangox/ai-copilot/ai-copilot-web`）

| 文件 | 职责 |
|---|---|
| `src/features/auth/token-store.ts`（新建） | 模块级 token 持有 + single-flight refresh，供拦截器使用 |
| `src/features/auth/AuthProvider.tsx`（新建） | React 侧的登录/登出动作与用户信息 |
| `src/features/auth/LoginPage.tsx`（新建） | 登录表单 |
| `src/features/auth/index.ts`（改） | `useCurrentUser()` 改为读 context |
| `src/api/client.ts`（改） | 加 `Authorization`；401 → refresh → 重放；403 不 refresh |
| `src/api/sse.ts`（改） | 加 `Authorization`；401 不重放 |
| `src/routes/router.tsx`（改） | `/login` 路由 + `ProtectedRoute` |

`token-store.ts` 与 `AuthProvider.tsx` 分开，是因为 `client.ts` 是非 React 模块，不能用 hook 读 token；拦截器需要一个模块级的同步读取点。

---

## 阶段依赖

四个阶段严格顺序执行，后一阶段依赖前一阶段产出的真实 token / 真实端点：

1. **Phase 1（Task 1–6）admin-api** —— 产出可签发、可验签的 token 与 5 个端点。完成后可用 curl 端到端验证。
2. **Phase 2（Task 7–10）internal-mcp-server** —— 消费 Phase 1 的 JWKS 与权限端点。
3. **Phase 3（Task 11–13）ai-copilot-api** —— 消费 Phase 1 的签发端点与 JWKS，并向 Phase 2 传递委派凭证。
4. **Phase 4（Task 14–17）ai-copilot-web** —— 消费 Phase 3 的端点。

每个阶段内部也是顺序的（各 Task 的 Interfaces 块标明依赖）。

**所有 git 命令都在对应子仓库内执行**（umbrella 目录不是 git 仓库）：admin-api 在 `mangox-blockmatrix`，其余三个在 `ai-copilot/<service>`。

---

# Phase 1 — admin-api

**测试风格说明（照抄本仓库现有约定）：** 纯逻辑单测继承 `PHPUnit\Framework\TestCase`（例：`tests/Unit/LoginRateLimiterTest.php`）；需要 `config()` / `trans()` / 容器的测试继承 `Tests\TestCase`；Controller 测试直接实例化 Controller 并用 Mockery 注入依赖（例：`tests/Feature/PositionRecordControllerTest.php`），不走 HTTP 层 —— 因为 HTTP 层测试会要求真实的 `blockmatrix` MySQL 连接。

所有 Phase 1 的命令都在 `/Users/antony/Workspace/mangox/mangox-blockmatrix` 下执行。

---

### Task 1: TokenIssuer —— JWT 签发、验签与 JWKS

**Files:**
- Create: `app/Services/Blockmatrix/Copilot/AccessToken.php`
- Create: `app/Services/Blockmatrix/Copilot/TokenIssuer.php`
- Create: `config/copilot.php`
- Modify: `composer.json`（新增 `firebase/php-jwt`）
- Test: `tests/Unit/Copilot/TokenIssuerTest.php`

**Interfaces:**
- Consumes: 无（本阶段起点）
- Produces:
  - `AccessToken` 值对象：`public readonly string $jwt`、`public readonly string $jti`、`public readonly int $expiresAt`
  - `TokenIssuer::__construct(string $privateKey, string $publicKey, string $keyId, int $accessTtlSeconds)`
  - `TokenIssuer::issueAccessToken(array $user, bool $isSysAdmin): AccessToken` —— `$user` 需含 `id`、`login_name`、`role_id`
  - `TokenIssuer::verify(string $jwt): array` —— 返回 claims 关联数组；失败抛 `Firebase\JWT\*` 异常或 `UnexpectedValueException`
  - `TokenIssuer::jwks(): array`
  - `TokenIssuer::newRefreshToken(): string`
  - 常量 `TokenIssuer::ISSUER`、`AUDIENCE`、`ACTOR`、`ALGORITHM`、`LEEWAY_SECONDS`

- [ ] **Step 1: 装依赖**

```bash
composer require firebase/php-jwt:^6.10
```

- [ ] **Step 2: 建配置文件**

创建 `config/copilot.php`：

```php
<?php

return [
    'jwt' => [
        // 私钥只存在于 admin-api。生产由环境变量注入 PEM 内容。
        'private_key' => env('COPILOT_JWT_PRIVATE_KEY', ''),
        'public_key'  => env('COPILOT_JWT_PUBLIC_KEY', ''),
        'key_id'      => env('COPILOT_JWT_KEY_ID', 'copilot-1'),
    ],

    // 30 分钟。长 agent run 期间 copilot 侧会在 run 前检查剩余寿命（spec D7）。
    'access_ttl'  => (int) env('COPILOT_ACCESS_TTL', 1800),
    // 8 小时。
    'refresh_ttl' => (int) env('COPILOT_REFRESH_TTL', 28800),

    'redis_prefix' => env('COPILOT_REDIS_PREFIX', 'copilot:'),

    // 只有被授权服务可以换票。与 internal-mcp-server 自己的 service token
    // 是两个不同的凭证，不要复用同一个值。
    'service_token' => env('COPILOT_SERVICE_TOKEN', ''),
];
```

- [ ] **Step 3: 写失败测试**

创建 `tests/Unit/Copilot/TokenIssuerTest.php`：

```php
<?php

namespace Tests\Unit\Copilot;

use App\Services\Blockmatrix\Copilot\TokenIssuer;
use PHPUnit\Framework\TestCase;

class TokenIssuerTest extends TestCase
{
    private string $privateKey = '';
    private string $publicKey = '';

    protected function setUp(): void
    {
        parent::setUp();

        $resource = openssl_pkey_new([
            'private_key_bits' => 2048,
            'private_key_type' => OPENSSL_KEYTYPE_RSA,
        ]);
        openssl_pkey_export($resource, $this->privateKey);
        $this->publicKey = openssl_pkey_get_details($resource)['key'];
    }

    private function issuer(int $ttl = 1800): TokenIssuer
    {
        return new TokenIssuer($this->privateKey, $this->publicKey, 'test-kid', $ttl);
    }

    public function test_access_token_carries_exactly_the_specified_claims(): void
    {
        $issuer = $this->issuer();

        $token = $issuer->issueAccessToken([
            'id'         => 10001,
            'login_name' => 'zhangsan',
            'role_id'    => 7,
        ], false);

        $claims = $issuer->verify($token->jwt);

        self::assertSame('admin-api', $claims['iss']);
        self::assertSame(['copilot'], $claims['aud']);
        // sub 必须是字符串：MCP 与 copilot-api 都拿它和 X-Copilot-User-Id 头比较
        self::assertSame('10001', $claims['sub']);
        self::assertSame('zhangsan', $claims['login_name']);
        self::assertSame(7, $claims['role_id']);
        self::assertFalse($claims['is_sys_admin']);
        self::assertSame('ai-copilot', $claims['actor']);
        self::assertNotSame('', $claims['jti']);
        self::assertSame($claims['iat'] + 1800, $claims['exp']);

        // claims 集合是封闭的（spec §4.3）
        self::assertArrayNotHasKey('perm_ver', $claims);
        self::assertArrayNotHasKey('scopes', $claims);
    }

    public function test_access_token_exposes_jti_and_expiry_to_the_caller(): void
    {
        $token = $this->issuer()->issueAccessToken(
            ['id' => 1, 'login_name' => 'a', 'role_id' => 1],
            true
        );

        $claims = $this->issuer()->verify($token->jwt);

        self::assertSame($claims['jti'], $token->jti);
        self::assertSame($claims['exp'], $token->expiresAt);
        self::assertTrue($claims['is_sys_admin']);
    }

    public function test_verify_rejects_a_token_signed_by_another_key(): void
    {
        $other = openssl_pkey_new(['private_key_bits' => 2048, 'private_key_type' => OPENSSL_KEYTYPE_RSA]);
        openssl_pkey_export($other, $otherPrivate);

        $foreign = new TokenIssuer(
            $otherPrivate,
            openssl_pkey_get_details($other)['key'],
            'other-kid',
            1800
        );
        $token = $foreign->issueAccessToken(['id' => 1, 'login_name' => 'a', 'role_id' => 1], false);

        $this->expectException(\Firebase\JWT\SignatureInvalidException::class);
        $this->issuer()->verify($token->jwt);
    }

    public function test_verify_rejects_an_expired_token_beyond_the_leeway(): void
    {
        // TTL 为负 → 已过期；超出 60 秒 leeway
        $token = $this->issuer(-(TokenIssuer::LEEWAY_SECONDS + 10))
            ->issueAccessToken(['id' => 1, 'login_name' => 'a', 'role_id' => 1], false);

        $this->expectException(\Firebase\JWT\ExpiredException::class);
        $this->issuer()->verify($token->jwt);
    }

    public function test_jwks_exposes_the_public_key_with_the_configured_kid(): void
    {
        $jwks = $this->issuer()->jwks();

        self::assertCount(1, $jwks['keys']);
        self::assertSame('RSA', $jwks['keys'][0]['kty']);
        self::assertSame('RS256', $jwks['keys'][0]['alg']);
        self::assertSame('sig', $jwks['keys'][0]['use']);
        self::assertSame('test-kid', $jwks['keys'][0]['kid']);
        // base64url：不含 +、/、=
        self::assertDoesNotMatchRegularExpression('#[+/=]#', $jwks['keys'][0]['n']);
        self::assertDoesNotMatchRegularExpression('#[+/=]#', $jwks['keys'][0]['e']);
    }

    public function test_refresh_tokens_are_unique_and_long_enough(): void
    {
        $issuer = $this->issuer();

        $a = $issuer->newRefreshToken();
        $b = $issuer->newRefreshToken();

        self::assertNotSame($a, $b);
        self::assertSame(64, strlen($a)); // 32 字节 → 64 个 hex 字符
    }
}
```

- [ ] **Step 4: 跑测试确认失败**

Run: `./vendor/bin/phpunit tests/Unit/Copilot/TokenIssuerTest.php`
Expected: FAIL，`Class "App\Services\Blockmatrix\Copilot\TokenIssuer" not found`

- [ ] **Step 5: 实现 AccessToken**

创建 `app/Services/Blockmatrix/Copilot/AccessToken.php`：

```php
<?php

namespace App\Services\Blockmatrix\Copilot;

/**
 * 一次签发的结果。jti 和 expiresAt 单独暴露，是为了让调用方不必再解一遍 JWT
 * 就能把 jti 写进撤销名单、把 expiresAt 回给前端。
 */
final class AccessToken
{
    public function __construct(
        public readonly string $jwt,
        public readonly string $jti,
        public readonly int $expiresAt,
    ) {
    }
}
```

- [ ] **Step 6: 实现 TokenIssuer**

创建 `app/Services/Blockmatrix/Copilot/TokenIssuer.php`：

```php
<?php

namespace App\Services\Blockmatrix\Copilot;

use Firebase\JWT\JWT;
use Firebase\JWT\Key;
use UnexpectedValueException;

/**
 * copilot access token 的签发与验签。
 *
 * 只依赖密钥和时钟 —— 不碰 Redis、不碰数据库，这样签发逻辑的单测不需要任何外部
 * 依赖。撤销名单在 TokenStore，编排在 CopilotAuthService。
 */
class TokenIssuer
{
    public const ISSUER = 'admin-api';
    public const AUDIENCE = 'copilot';
    public const ACTOR = 'ai-copilot';
    public const ALGORITHM = 'RS256';

    /**
     * 同一个 token 会被 admin-api、internal-mcp-server、ai-copilot-api 三处
     * 独立验签，三者时钟各自漂移。零 leeway 会在 exp 边界产生零星且不可
     * 复现的 401，所以三边统一留 60 秒。
     */
    public const LEEWAY_SECONDS = 60;

    public function __construct(
        private string $privateKey,
        private string $publicKey,
        private string $keyId,
        private int $accessTtlSeconds,
    ) {
    }

    /**
     * @param array $user 需含 id、login_name、role_id
     */
    public function issueAccessToken(array $user, bool $isSysAdmin): AccessToken
    {
        $issuedAt  = time();
        $expiresAt = $issuedAt + $this->accessTtlSeconds;
        $jti       = bin2hex(random_bytes(16));

        $payload = [
            'iss'          => self::ISSUER,
            'aud'          => [self::AUDIENCE],
            // 字符串：下游要和 X-Copilot-User-Id 这个头做全等比较
            'sub'          => (string) $user['id'],
            'login_name'   => $user['login_name'],
            'role_id'      => $user['role_id'],
            'is_sys_admin' => $isSysAdmin,
            'actor'        => self::ACTOR,
            'jti'          => $jti,
            'iat'          => $issuedAt,
            'exp'          => $expiresAt,
        ];

        return new AccessToken(
            JWT::encode($payload, $this->privateKey, self::ALGORITHM, $this->keyId),
            $jti,
            $expiresAt,
        );
    }

    /**
     * @return array claims 关联数组
     */
    public function verify(string $jwt): array
    {
        JWT::$leeway = self::LEEWAY_SECONDS;

        $decoded = JWT::decode($jwt, new Key($this->publicKey, self::ALGORITHM));
        $claims  = json_decode(json_encode($decoded), true);

        // firebase/php-jwt 不校验 iss/aud，必须自己来
        if (($claims['iss'] ?? null) !== self::ISSUER) {
            throw new UnexpectedValueException('iss mismatch');
        }
        if (! in_array(self::AUDIENCE, (array) ($claims['aud'] ?? []), true)) {
            throw new UnexpectedValueException('aud mismatch');
        }

        return $claims;
    }

    public function jwks(): array
    {
        $details = openssl_pkey_get_details(openssl_pkey_get_public($this->publicKey));

        return [
            'keys' => [[
                'kty' => 'RSA',
                'use' => 'sig',
                'alg' => self::ALGORITHM,
                'kid' => $this->keyId,
                'n'   => self::base64Url($details['rsa']['n']),
                'e'   => self::base64Url($details['rsa']['e']),
            ]],
        ];
    }

    public function newRefreshToken(): string
    {
        return bin2hex(random_bytes(32));
    }

    private static function base64Url(string $binary): string
    {
        return rtrim(strtr(base64_encode($binary), '+/', '-_'), '=');
    }
}
```

- [ ] **Step 7: 跑测试确认通过**

Run: `./vendor/bin/phpunit tests/Unit/Copilot/TokenIssuerTest.php`
Expected: PASS，6 个测试全绿

- [ ] **Step 8: 跑全量测试确认没破坏既有行为**

Run: `./vendor/bin/phpunit`
Expected: 与改动前相同的通过数

- [ ] **Step 9: Commit**

```bash
git add composer.json composer.lock config/copilot.php \
        app/Services/Blockmatrix/Copilot/ tests/Unit/Copilot/
git commit -m "feat(copilot): add RS256 token issuer, verifier and JWKS"
```

---

### Task 2: TokenStore —— refresh 轮换、盗用检测与撤销名单

**Files:**
- Create: `app/Services/Blockmatrix/Copilot/TokenStore.php`
- Test: `tests/Unit/Copilot/TokenStoreTest.php`

**Interfaces:**
- Consumes: `config('copilot.redis_prefix')`、`config('copilot.refresh_ttl')`（Task 1 建立）
- Produces:
  - `TokenStore::__construct(RedisConnectionLike $redis, string $prefix, int $refreshTtlSeconds)`
  - `TokenStore::storeRefresh(string $refreshToken, int $userId): void`
  - `TokenStore::consumeRefresh(string $refreshToken): int` —— 返回 userId；原子性作废旧串；若该串已被轮换过则抛 `RefreshTokenReplayed`
  - `TokenStore::revokeAllRefreshForUser(int $userId): void`
  - `TokenStore::revokeJti(string $jti, int $expiresAt): void`
  - `TokenStore::isJtiRevoked(string $jti): bool`
  - `RefreshTokenReplayed` 异常类（同文件内）

- [ ] **Step 1: 写失败测试**

测试用一个内存假 Redis，只实现本类用到的五个命令 —— 比拉起真 Redis 快，也让盗用检测的时序断言稳定。

创建 `tests/Unit/Copilot/TokenStoreTest.php`：

```php
<?php

namespace Tests\Unit\Copilot;

use App\Services\Blockmatrix\Copilot\RefreshTokenReplayed;
use App\Services\Blockmatrix\Copilot\TokenStore;
use PHPUnit\Framework\TestCase;

/** 只实现 TokenStore 用到的命令。 */
class FakeRedis
{
    public array $data = [];

    public function get(string $key): ?string
    {
        return $this->data[$key] ?? null;
    }

    public function setex(string $key, int $ttl, string $value): bool
    {
        $this->data[$key] = $value;
        return true;
    }

    public function del(string ...$keys): int
    {
        $n = 0;
        foreach ($keys as $key) {
            if (array_key_exists($key, $this->data)) {
                unset($this->data[$key]);
                $n++;
            }
        }
        return $n;
    }

    public function sadd(string $key, string $member): int
    {
        $this->data[$key] ??= [];
        $this->data[$key][$member] = true;
        return 1;
    }

    public function smembers(string $key): array
    {
        return array_keys($this->data[$key] ?? []);
    }
}

class TokenStoreTest extends TestCase
{
    private FakeRedis $redis;
    private TokenStore $store;

    protected function setUp(): void
    {
        parent::setUp();
        $this->redis = new FakeRedis();
        $this->store = new TokenStore($this->redis, 'copilot:', 28800);
    }

    public function test_a_stored_refresh_token_resolves_to_its_user(): void
    {
        $this->store->storeRefresh('refresh-a', 10001);

        self::assertSame(10001, $this->store->consumeRefresh('refresh-a'));
    }

    public function test_consuming_a_refresh_token_invalidates_it(): void
    {
        $this->store->storeRefresh('refresh-a', 10001);
        $this->store->consumeRefresh('refresh-a');

        $this->expectException(RefreshTokenReplayed::class);
        $this->store->consumeRefresh('refresh-a');
    }

    public function test_a_replayed_token_reports_its_former_owner(): void
    {
        $this->store->storeRefresh('refresh-a', 10001);
        $this->store->storeRefresh('refresh-b', 10001);
        $this->store->consumeRefresh('refresh-a');

        try {
            $this->store->consumeRefresh('refresh-a');
            self::fail('期望抛 RefreshTokenReplayed');
        } catch (RefreshTokenReplayed $e) {
            // userId 非 null 才是「曾经有效、已被轮换」的盗用信号；为 null
            // 则是伪造或早已过期。
            self::assertSame(10001, $e->userId);
        }

        // 连带撤销是 CopilotAuthService 的决定（Task 6），不在这一层：store
        // 只报告事实。所以同一用户的其他串在这里仍然有效。
        self::assertSame(10001, $this->store->consumeRefresh('refresh-b'));
    }

    public function test_an_unknown_refresh_token_is_rejected_without_a_user(): void
    {
        try {
            $this->store->consumeRefresh('never-issued');
            self::fail('期望抛 RefreshTokenReplayed');
        } catch (RefreshTokenReplayed $e) {
            self::assertNull($e->userId);
        }
    }

    public function test_revoke_all_refresh_for_user_leaves_other_users_alone(): void
    {
        $this->store->storeRefresh('refresh-a', 10001);
        $this->store->storeRefresh('refresh-z', 20002);

        $this->store->revokeAllRefreshForUser(10001);

        self::assertSame(20002, $this->store->consumeRefresh('refresh-z'));
    }

    public function test_revoked_jti_is_reported_until_the_access_token_would_expire(): void
    {
        self::assertFalse($this->store->isJtiRevoked('jti-1'));

        $this->store->revokeJti('jti-1', time() + 600);

        self::assertTrue($this->store->isJtiRevoked('jti-1'));
    }

    public function test_revoking_an_already_expired_jti_is_a_no_op(): void
    {
        // TTL 会算成 <= 0；写进去只会制造一条永不过期的垃圾键
        $this->store->revokeJti('jti-old', time() - 10);

        self::assertSame([], array_keys($this->redis->data));
    }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./vendor/bin/phpunit tests/Unit/Copilot/TokenStoreTest.php`
Expected: FAIL，`Class "App\Services\Blockmatrix\Copilot\TokenStore" not found`

- [ ] **Step 3: 实现 TokenStore**

创建 `app/Services/Blockmatrix/Copilot/TokenStore.php`：

```php
<?php

namespace App\Services\Blockmatrix\Copilot;

use RuntimeException;

/**
 * refresh token 被重放。
 *
 * userId 为 null 表示这个串从未签发过（或已过期）；非 null 表示它曾经属于某个
 * 用户、已被轮换掉 —— 那是凭证泄露的信号，调用方应连带撤销该用户全部 refresh。
 */
class RefreshTokenReplayed extends RuntimeException
{
    public function __construct(public readonly ?int $userId)
    {
        parent::__construct('refresh token is not valid');
    }
}

/**
 * copilot token 的全部 Redis 状态。
 *
 * 只碰 Redis —— 不签发、不验签。构造函数收一个 Redis 连接而不是自己去调
 * BaseRedis::getBlockmatrixRedis()，这样单测可以塞一个内存假实现。
 */
class TokenStore
{
    public function __construct(
        private object $redis,
        private string $prefix,
        private int $refreshTtlSeconds,
    ) {
    }

    public function storeRefresh(string $refreshToken, int $userId): void
    {
        $this->redis->setex($this->refreshKey($refreshToken), $this->refreshTtlSeconds, (string) $userId);
        // 为了能按用户批量撤销，额外记一份索引。TTL 与 refresh 同量级。
        $this->redis->sadd($this->userIndexKey($userId), $this->hash($refreshToken));
    }

    /**
     * 取出并立即作废。返回 userId。
     *
     * @throws RefreshTokenReplayed
     */
    public function consumeRefresh(string $refreshToken): int
    {
        $key   = $this->refreshKey($refreshToken);
        $value = $this->redis->get($key);

        if ($value === null || $value === false || $value === '') {
            // 可能是伪造、已过期，或已经被轮换掉。后两者无法区分，但处理方式相同：
            // 拒绝。若曾属于某用户，索引里还留着痕迹 —— 见 revoked 标记。
            throw new RefreshTokenReplayed($this->rotatedOwner($refreshToken));
        }

        $userId = (int) $value;
        $this->redis->del($key);
        // 留一个「曾经存在、已被轮换」的墓碑，使重放可以被识别为盗用而不是伪造。
        $this->redis->setex($this->rotatedKey($refreshToken), $this->refreshTtlSeconds, (string) $userId);

        return $userId;
    }

    public function revokeAllRefreshForUser(int $userId): void
    {
        foreach ($this->redis->smembers($this->userIndexKey($userId)) as $hash) {
            $this->redis->del($this->prefix . 'refresh:' . $hash);
            $this->redis->del($this->prefix . 'rotated:' . $hash);
        }
        $this->redis->del($this->userIndexKey($userId));
    }

    public function revokeJti(string $jti, int $expiresAt): void
    {
        $ttl = $expiresAt - time();
        if ($ttl <= 0) {
            // 已经过期的 access token 不需要拉黑，写进去只会留一条垃圾键。
            return;
        }
        $this->redis->setex($this->prefix . 'revoked:' . $jti, $ttl, '1');
    }

    public function isJtiRevoked(string $jti): bool
    {
        $value = $this->redis->get($this->prefix . 'revoked:' . $jti);

        return $value !== null && $value !== false && $value !== '';
    }

    private function rotatedOwner(string $refreshToken): ?int
    {
        $value = $this->redis->get($this->rotatedKey($refreshToken));

        return ($value === null || $value === false || $value === '') ? null : (int) $value;
    }

    private function refreshKey(string $refreshToken): string
    {
        return $this->prefix . 'refresh:' . $this->hash($refreshToken);
    }

    private function rotatedKey(string $refreshToken): string
    {
        return $this->prefix . 'rotated:' . $this->hash($refreshToken);
    }

    private function userIndexKey(int $userId): string
    {
        return $this->prefix . 'refresh_index:' . $userId;
    }

    /** 只存 hash：Redis 被读走也拿不到可用的 refresh token。 */
    private function hash(string $refreshToken): string
    {
        return hash('sha256', $refreshToken);
    }
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `./vendor/bin/phpunit tests/Unit/Copilot/TokenStoreTest.php`
Expected: PASS，7 个测试全绿

- [ ] **Step 5: Commit**

```bash
git add app/Services/Blockmatrix/Copilot/TokenStore.php tests/Unit/Copilot/TokenStoreTest.php
git commit -m "feat(copilot): add refresh rotation store with replay detection"
```

---

### Task 3: 抽取登录校验逻辑（行为必须不变）

**Files:**
- Create: `app/Exceptions/LoginRejected.php`
- Modify: `app/Services/Blockmatrix/LoginService.php`
- Modify: `app/Http/Controllers/Blockmatrix/LoginController.php:29-77`
- Test: `tests/Feature/Copilot/LoginServiceExtractionTest.php`

**Interfaces:**
- Consumes: 无
- Produces:
  - `LoginRejected::__construct(string $reason, string $message)`，`public readonly string $reason`
  - 常量 `LoginRejected::CAPTCHA_EXPIRED`、`CAPTCHA_INVALID`、`INVALID_CREDENTIALS`、`ACCOUNT_DISABLED`（取值即 spec §9 的错误码字符串）
  - `LoginService::verifyCaptcha(?string $key, ?string $captcha): void`
  - `LoginService::verifyCredentials(string $loginName, string $password): array`

**为什么用异常而不是让 Service 直接返回错误响应：** 两个入口需要**不同的响应形态** —— `LoginController` 必须继续返回 `fail($msg)`（HTTP 200、`code: 0`，保持现有行为逐字节不变），而 `CopilotAuthController` 要返回 HTTP 401 + 稳定错误码。异常携带 `reason`，由各入口自己翻译。

**注意不要用 `BusinessException`：** 它的 render 产出 `{is_succ, code, message, data: null}`，与 `fail()` 的 `{is_succ, message, code, request_id}` 不同 —— 换成它会改变现有登录接口的响应体。

- [ ] **Step 1: 写失败测试**

创建 `tests/Feature/Copilot/LoginServiceExtractionTest.php`：

```php
<?php

namespace Tests\Feature\Copilot;

use App\Exceptions\LoginRejected;
use App\Models\Blockmatrix\AdminUser;
use App\Repositories\Blockmatrix\AdminUserRepository;
use App\Services\Blockmatrix\LoginService;
use Illuminate\Support\Facades\Hash;
use Mockery;
use Mockery\Adapter\Phpunit\MockeryPHPUnitIntegration;
use Tests\TestCase;

class LoginServiceExtractionTest extends TestCase
{
    use MockeryPHPUnitIntegration;

    private function service(?AdminUserRepository $repo = null): LoginService
    {
        return new LoginService($repo ?? Mockery::mock(AdminUserRepository::class));
    }

    public function test_captcha_check_is_skipped_when_captcha_is_disabled(): void
    {
        config()->set('blockmatrix.captcha.none', true);

        // 没有任何 Redis 交互就应该直接返回
        $this->service()->verifyCaptcha('any-key', 'any-code');

        self::assertTrue(true);
    }

    public function test_unknown_account_and_wrong_password_produce_the_same_reason(): void
    {
        $repo = Mockery::mock(AdminUserRepository::class);
        $repo->shouldReceive('getUserInfoByAccount')->with('ghost')->andReturn(null);
        $repo->shouldReceive('getUserInfoByAccount')->with('real')->andReturn([
            'id'         => 10001,
            'login_name' => 'real',
            'role_id'    => 7,
            'password'   => Hash::make('correct-horse'),
        ]);

        $service = $this->service($repo);

        $reasons = [];
        foreach ([['ghost', 'whatever'], ['real', 'wrong-password']] as [$name, $password]) {
            try {
                $service->verifyCredentials($name, $password);
                self::fail('期望抛 LoginRejected');
            } catch (LoginRejected $e) {
                $reasons[] = $e->reason;
                // 消息必须与抽取前完全一致，否则现有前端文案会变
                self::assertSame(trans('login.account_no'), $e->getMessage());
            }
        }

        // 两种失败不可区分 —— 这是防账号枚举，抽取前的注释「模糊错误，避免枚举」
        self::assertSame(
            [LoginRejected::INVALID_CREDENTIALS, LoginRejected::INVALID_CREDENTIALS],
            $reasons
        );
    }

    public function test_correct_credentials_return_the_user_record(): void
    {
        $user = $this->service($this->repoReturningModel())
            ->verifyCredentials('real', 'correct-horse');

        self::assertSame(10001, $user['id']);
        self::assertSame('real', $user['login_name']);
        self::assertSame(7, $user['role_id']);
    }

    /**
     * 仓库返回的是 Eloquent 模型，不是数组。
     *
     * `(array) $model` **不会**给出属性数组 —— 它给出的是 Eloquent 的内部属性
     * （` * attributes`、` * hidden` 等被名字修饰的键）。必须走 toArray()。
     * 这个用例把它钉住：一旦有人改回强制类型转换，这里立刻红。
     */
    public function test_the_returned_record_is_a_real_attribute_array(): void
    {
        $user = $this->service($this->repoReturningModel())
            ->verifyCredentials('real', 'correct-horse');

        self::assertSame(['id', 'login_name', 'role_id', 'status'], array_keys($user));
    }

    /** password 在 AdminUser::$hidden 里，所以 toArray() 不会带出它 —— 这是好事。 */
    public function test_the_password_hash_does_not_travel_any_further(): void
    {
        $user = $this->service($this->repoReturningModel())
            ->verifyCredentials('real', 'correct-horse');

        self::assertArrayNotHasKey('password', $user);
    }

    private function repoReturningModel(): AdminUserRepository
    {
        $model = new AdminUser();
        $model->forceFill([
            'id'         => 10001,
            'login_name' => 'real',
            'role_id'    => 7,
            'status'     => AdminUser::STATUS_NORMAL,
            'password'   => Hash::make('correct-horse'),
        ]);

        $repo = Mockery::mock(AdminUserRepository::class);
        $repo->shouldReceive('getUserInfoByAccount')->with('real')->andReturn($model);

        return $repo;
    }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./vendor/bin/phpunit tests/Feature/Copilot/LoginServiceExtractionTest.php`
Expected: FAIL，`Class "App\Exceptions\LoginRejected" not found`

- [ ] **Step 3: 实现 LoginRejected**

创建 `app/Exceptions/LoginRejected.php`：

```php
<?php

namespace App\Exceptions;

use Illuminate\Contracts\Debug\ShouldntReport;
use RuntimeException;

/**
 * 登录校验被拒。
 *
 * 刻意不继承 BusinessException：后者的 render 会产出和 Response::fail() 不同的
 * 响应体，而现有登录接口的响应形态必须保持不变。两个入口各自翻译这个异常 ——
 * LoginController 翻成 fail()（HTTP 200），CopilotAuthController 翻成 HTTP 401。
 */
class LoginRejected extends RuntimeException implements ShouldntReport
{
    public const CAPTCHA_EXPIRED     = 'AUTH_CAPTCHA_EXPIRED';
    public const CAPTCHA_INVALID     = 'AUTH_CAPTCHA_INVALID';
    public const INVALID_CREDENTIALS = 'AUTH_INVALID_CREDENTIALS';
    public const ACCOUNT_DISABLED    = 'AUTH_ACCOUNT_DISABLED';

    public function __construct(
        public readonly string $reason,
        string $message,
    ) {
        parent::__construct($message);
    }
}
```

- [ ] **Step 4: 在 LoginService 里加两个方法**

修改 `app/Services/Blockmatrix/LoginService.php` —— 补 import 并在 `getUserInfoByAccount()` 之后插入：

```php
use App\Exceptions\LoginRejected;
use App\Utils\BaseRedis;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Support\Facades\Hash;
```

```php
    /**
     * 校验图形验证码。
     *
     * 逻辑逐行搬自抽取前的 LoginController::login()，包括
     * config('blockmatrix.captcha.none') 非空时整段跳过这一条。
     *
     * @throws LoginRejected
     */
    public function verifyCaptcha(?string $key, ?string $captcha): void
    {
        if (! empty(config('blockmatrix.captcha.none'))) {
            return;
        }

        $captchaKey = config('blockmatrix.redis.captcha') . $key;
        $redis      = BaseRedis::getBlockmatrixRedis();
        $checkCode  = $redis->get($captchaKey);

        if (empty($checkCode)) {
            throw new LoginRejected(LoginRejected::CAPTCHA_EXPIRED, trans('login.captcha_expired'));
        }
        if ($checkCode !== strtolower((string) $captcha)) {
            throw new LoginRejected(LoginRejected::CAPTCHA_INVALID, trans('login.captcha_incorrect'));
        }

        $redis->del($captchaKey);
    }

    /**
     * 校验账号密码，返回用户记录。
     *
     * 账号不存在和密码错误返回同一个 reason 和同一句消息 —— 防账号枚举，
     * 与抽取前一致。
     *
     * @throws LoginRejected
     */
    public function verifyCredentials(string $loginName, string $password): array
    {
        $userInfo = $this->getUserInfoByAccount($loginName);

        if (empty($userInfo) || ! Hash::check($password, $userInfo['password'])) {
            throw new LoginRejected(LoginRejected::INVALID_CREDENTIALS, trans('login.account_no'));
        }

        // toArray()，不是 (array)。强制类型转换给出的是 Eloquent 的内部属性
        // （` * attributes`、` * hidden` …），不是属性数组。toArray() 顺带丢掉
        // password（它在 AdminUser::$hidden 里），凭证不再往下流。
        return $userInfo instanceof Model ? $userInfo->toArray() : (array) $userInfo;
    }
```

- [ ] **Step 5: 跑测试确认通过**

Run: `./vendor/bin/phpunit tests/Feature/Copilot/LoginServiceExtractionTest.php`
Expected: PASS，3 个测试全绿

- [ ] **Step 6: 让 LoginController 改用抽出的方法**

修改 `app/Http/Controllers/Blockmatrix/LoginController.php`。把 `login()` 里从「验证码校验」到「用户名密码验证」的整段（原第 36–56 行）替换为：

```php
        try {
            $this->loginService->verifyCaptcha($key, $captcha);
            $userInfo = $this->loginService->verifyCredentials($loginName, $password);
        } catch (LoginRejected $e) {
            // 与抽取前逐字节一致：fail($msg) → HTTP 200、code 0、同样的 message
            return $this->fail($e->getMessage());
        }
```

补 `use App\Exceptions\LoginRejected;`。抽取后 `LoginController` 里这两个 import 变成无引用，删掉：

- `use App\Utils\BaseRedis;`（只有原 `login()` 的验证码校验用过它；`captcha()` 用的是 `CaptchaUtil`）
- `use Illuminate\Support\Facades\Hash;`（密码校验移走了）

`CaptchaUtil`、`Auth`、`Log`、`Str`、`Ip` 仍在使用，保留。

`login()` 后半段（`Auth::loginUsingId()`、`session()->regenerate()`、`area_id` 写入、日志、`Auth::check()` 复查、`succeed()`）**一行都不要改**。

- [ ] **Step 7: 跑全量测试 —— 这是抽取的验收标准**

Run: `./vendor/bin/phpunit`
Expected: PASS。**现有登录相关测试（含 `tests/Unit/LoginRateLimiterTest.php`）必须一行未改且全部通过。** 如有任何现有测试需要修改才能通过，说明抽取改变了行为，回到 Step 6 修正而不是改测试。

- [ ] **Step 8: 跑静态检查**

Run: `./vendor/bin/pint --test && ./vendor/bin/phpstan analyse`
Expected: 无新增告警

- [ ] **Step 9: Commit**

```bash
git add app/Exceptions/LoginRejected.php \
        app/Services/Blockmatrix/LoginService.php \
        app/Http/Controllers/Blockmatrix/LoginController.php \
        tests/Feature/Copilot/LoginServiceExtractionTest.php
git commit -m "refactor(login): extract captcha and credential checks, behaviour unchanged"
```

---

### Task 4: CopilotTokenGuard 与 guard 注册

**Files:**
- Create: `app/Auth/CopilotTokenGuard.php`
- Create: `app/Providers/CopilotAuthServiceProvider.php`
- Modify: `config/auth.php:40-45`（`guards` 数组）
- Modify: `bootstrap/providers.php`
- Test: `tests/Feature/Copilot/CopilotTokenGuardTest.php`

**Interfaces:**
- Consumes: `TokenIssuer`（Task 1）、`TokenStore`（Task 2）
- Produces:
  - `CopilotTokenGuard` 实现 `Illuminate\Contracts\Auth\Guard`
  - `CopilotTokenGuard::claims(): ?array` —— 认证成功后的 claims，供审计使用
  - guard 名 `copilot`，driver 名 `copilot-token`
  - 容器里 `TokenIssuer::class` 与 `TokenStore::class` 为 singleton

- [ ] **Step 1: 写失败测试**

创建 `tests/Feature/Copilot/CopilotTokenGuardTest.php`：

```php
<?php

namespace Tests\Feature\Copilot;

use App\Auth\CopilotTokenGuard;
use App\Models\Blockmatrix\AdminUser;
use App\Services\Blockmatrix\Copilot\TokenIssuer;
use App\Services\Blockmatrix\Copilot\TokenStore;
use Illuminate\Contracts\Auth\UserProvider;
use Illuminate\Http\Request;
use Mockery;
use Mockery\Adapter\Phpunit\MockeryPHPUnitIntegration;
use Tests\TestCase;

class CopilotTokenGuardTest extends TestCase
{
    use MockeryPHPUnitIntegration;

    private string $privateKey = '';
    private string $publicKey = '';

    protected function setUp(): void
    {
        parent::setUp();

        $resource = openssl_pkey_new([
            'private_key_bits' => 2048,
            'private_key_type' => OPENSSL_KEYTYPE_RSA,
        ]);
        openssl_pkey_export($resource, $this->privateKey);
        $this->publicKey = openssl_pkey_get_details($resource)['key'];
    }

    private function issuer(int $ttl = 1800): TokenIssuer
    {
        return new TokenIssuer($this->privateKey, $this->publicKey, 'test-kid', $ttl);
    }

    private function adminUser(int $status = AdminUser::STATUS_NORMAL): AdminUser
    {
        $user = new AdminUser();
        $user->forceFill(['id' => 10001, 'login_name' => 'zhangsan', 'role_id' => 7, 'status' => $status]);

        return $user;
    }

    /** @param array<string,string> $headers */
    private function guard(
        TokenIssuer $issuer,
        ?AdminUser $user,
        bool $jtiRevoked = false,
        array $headers = []
    ): CopilotTokenGuard {
        $provider = Mockery::mock(UserProvider::class);
        $provider->shouldReceive('retrieveById')->andReturn($user);

        $store = Mockery::mock(TokenStore::class);
        $store->shouldReceive('isJtiRevoked')->andReturn($jtiRevoked);

        $request = Request::create('/api/deposits', 'GET');
        foreach ($headers as $name => $value) {
            $request->headers->set($name, $value);
        }

        return new CopilotTokenGuard($provider, $issuer, $store, $request);
    }

    public function test_a_valid_token_resolves_to_the_admin_user(): void
    {
        $issuer = $this->issuer();
        $token = $issuer->issueAccessToken(
            ['id' => 10001, 'login_name' => 'zhangsan', 'role_id' => 7],
            false
        );

        $guard = $this->guard($issuer, $this->adminUser(), false, [
            'Authorization' => 'Bearer ' . $token->jwt,
        ]);

        self::assertTrue($guard->check());
        self::assertSame(10001, $guard->id());
        self::assertSame('zhangsan', $guard->claims()['login_name']);
        self::assertSame('ai-copilot', $guard->claims()['actor']);
    }

    public function test_no_authorization_header_is_a_guest(): void
    {
        $guard = $this->guard($this->issuer(), $this->adminUser());

        self::assertFalse($guard->check());
        self::assertTrue($guard->guest());
        self::assertNull($guard->claims());
    }

    public function test_a_token_signed_by_another_key_is_rejected(): void
    {
        $other = openssl_pkey_new(['private_key_bits' => 2048, 'private_key_type' => OPENSSL_KEYTYPE_RSA]);
        openssl_pkey_export($other, $otherPrivate);
        $foreign = new TokenIssuer($otherPrivate, openssl_pkey_get_details($other)['key'], 'x', 1800);

        $token = $foreign->issueAccessToken(['id' => 10001, 'login_name' => 'z', 'role_id' => 7], false);

        $guard = $this->guard($this->issuer(), $this->adminUser(), false, [
            'Authorization' => 'Bearer ' . $token->jwt,
        ]);

        self::assertFalse($guard->check());
    }

    public function test_an_expired_token_is_rejected(): void
    {
        $issuer = $this->issuer(-(TokenIssuer::LEEWAY_SECONDS + 10));
        $token = $issuer->issueAccessToken(['id' => 10001, 'login_name' => 'z', 'role_id' => 7], false);

        $guard = $this->guard($this->issuer(), $this->adminUser(), false, [
            'Authorization' => 'Bearer ' . $token->jwt,
        ]);

        self::assertFalse($guard->check());
    }

    public function test_a_revoked_jti_is_rejected(): void
    {
        $issuer = $this->issuer();
        $token = $issuer->issueAccessToken(['id' => 10001, 'login_name' => 'z', 'role_id' => 7], false);

        $guard = $this->guard($issuer, $this->adminUser(), true, [
            'Authorization' => 'Bearer ' . $token->jwt,
        ]);

        self::assertFalse($guard->check());
    }

    public function test_a_deleted_or_disabled_account_invalidates_an_already_issued_token(): void
    {
        $issuer = $this->issuer();
        $token = $issuer->issueAccessToken(['id' => 10001, 'login_name' => 'z', 'role_id' => 7], false);

        $guard = $this->guard($issuer, $this->adminUser(AdminUser::STATUS_DELETE), false, [
            'Authorization' => 'Bearer ' . $token->jwt,
        ]);

        self::assertFalse($guard->check());
    }

    public function test_a_token_whose_subject_no_longer_exists_is_rejected(): void
    {
        $issuer = $this->issuer();
        $token = $issuer->issueAccessToken(['id' => 10001, 'login_name' => 'z', 'role_id' => 7], false);

        $guard = $this->guard($issuer, null, false, [
            'Authorization' => 'Bearer ' . $token->jwt,
        ]);

        self::assertFalse($guard->check());
    }

    public function test_a_malformed_bearer_value_does_not_throw(): void
    {
        $guard = $this->guard($this->issuer(), $this->adminUser(), false, [
            'Authorization' => 'Bearer not-a-jwt',
        ]);

        self::assertFalse($guard->check());
    }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./vendor/bin/phpunit tests/Feature/Copilot/CopilotTokenGuardTest.php`
Expected: FAIL，`Class "App\Auth\CopilotTokenGuard" not found`

- [ ] **Step 3: 实现 CopilotTokenGuard**

创建 `app/Auth/CopilotTokenGuard.php`：

```php
<?php

namespace App\Auth;

use App\Models\Blockmatrix\AdminUser;
use App\Services\Blockmatrix\Copilot\TokenIssuer;
use App\Services\Blockmatrix\Copilot\TokenStore;
use Illuminate\Contracts\Auth\Authenticatable;
use Illuminate\Contracts\Auth\Guard;
use Illuminate\Contracts\Auth\UserProvider;
use Illuminate\Http\Request;
use Throwable;

/**
 * 用 copilot access token 认证。
 *
 * 只回答「这个 token 对应哪个用户」。HTTP 方法限制（GET-only）和审计标记在
 * Authenticate 中间件里 —— guard 不应该检查请求方法。
 *
 * 任何校验失败都返回 guest 而不是抛异常：Laravel 的 guard 契约是
 * check()/user() 不抛，由中间件决定响应形态。
 */
class CopilotTokenGuard implements Guard
{
    private ?Authenticatable $user = null;
    private ?array $claims = null;
    private bool $resolved = false;

    public function __construct(
        private UserProvider $provider,
        private TokenIssuer $issuer,
        private TokenStore $store,
        private Request $request,
    ) {
    }

    public function check(): bool
    {
        return $this->user() !== null;
    }

    public function guest(): bool
    {
        return ! $this->check();
    }

    public function user(): ?Authenticatable
    {
        if ($this->resolved) {
            return $this->user;
        }
        $this->resolved = true;

        $jwt = $this->request->bearerToken();
        if (empty($jwt)) {
            return null;
        }

        try {
            $claims = $this->issuer->verify($jwt);
        } catch (Throwable) {
            // 签名错、过期、iss/aud 不匹配、格式损坏 —— 一律按未认证处理
            return null;
        }

        if (empty($claims['jti']) || $this->store->isJtiRevoked((string) $claims['jti'])) {
            return null;
        }

        $user = $this->provider->retrieveById($claims['sub'] ?? '');
        if ($user === null) {
            return null;
        }

        // 账号被禁用或删除时，已签发的 token 必须立即失效。反正解析 token 后
        // 无论如何都要取用户对象，顺带校验不增加开销。
        if ((int) ($user['status'] ?? 0) !== AdminUser::STATUS_NORMAL) {
            return null;
        }

        $this->claims = $claims;

        return $this->user = $user;
    }

    /** 认证成功后的 claims；未认证时为 null。 */
    public function claims(): ?array
    {
        $this->user();

        return $this->claims;
    }

    public function id()
    {
        return $this->user()?->getAuthIdentifier();
    }

    /** copilot guard 不做凭证校验 —— 换票在 CopilotAuthService。 */
    public function validate(array $credentials = []): bool
    {
        return false;
    }

    public function hasUser(): bool
    {
        return $this->user !== null;
    }

    public function setUser(Authenticatable $user): void
    {
        $this->user = $user;
        $this->resolved = true;
    }
}
```

- [ ] **Step 4: 实现 ServiceProvider**

创建 `app/Providers/CopilotAuthServiceProvider.php`：

```php
<?php

namespace App\Providers;

use App\Auth\CopilotTokenGuard;
use App\Services\Blockmatrix\Copilot\TokenIssuer;
use App\Services\Blockmatrix\Copilot\TokenStore;
use App\Utils\BaseRedis;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\ServiceProvider;

class CopilotAuthServiceProvider extends ServiceProvider
{
    public function register(): void
    {
        $this->app->singleton(TokenIssuer::class, fn () => new TokenIssuer(
            (string) config('copilot.jwt.private_key'),
            (string) config('copilot.jwt.public_key'),
            (string) config('copilot.jwt.key_id'),
            (int) config('copilot.access_ttl'),
        ));

        $this->app->singleton(TokenStore::class, fn () => new TokenStore(
            BaseRedis::getBlockmatrixRedis(),
            (string) config('copilot.redis_prefix'),
            (int) config('copilot.refresh_ttl'),
        ));
    }

    public function boot(): void
    {
        Auth::extend('copilot-token', function ($app, string $name, array $config) {
            return new CopilotTokenGuard(
                Auth::createUserProvider($config['provider']),
                $app->make(TokenIssuer::class),
                $app->make(TokenStore::class),
                $app->make('request'),
            );
        });
    }
}
```

- [ ] **Step 5: 注册 guard 与 provider**

修改 `config/auth.php` 的 `guards` 数组（`web` 一行都不要动）：

```php
    'guards' => [
        'web' => [
            'driver' => 'session',
            'provider' => 'users',
        ],

        // copilot 链路：无状态 token，不建 session。见 config/copilot.php。
        'copilot' => [
            'driver' => 'copilot-token',
            'provider' => 'users',
        ],
    ],
```

修改 `bootstrap/providers.php`：

```php
<?php

return [
    App\Providers\AppServiceProvider::class,
    App\Providers\CopilotAuthServiceProvider::class,
];
```

- [ ] **Step 6: 跑测试确认通过**

Run: `./vendor/bin/phpunit tests/Feature/Copilot/CopilotTokenGuardTest.php`
Expected: PASS，8 个测试全绿

- [ ] **Step 7: 跑全量测试**

Run: `./vendor/bin/phpunit`
Expected: 与改动前相同的通过数（新增 guard 不应影响 `web` guard 的任何行为）

- [ ] **Step 8: Commit**

```bash
git add app/Auth/ app/Providers/CopilotAuthServiceProvider.php \
        config/auth.php bootstrap/providers.php \
        tests/Feature/Copilot/CopilotTokenGuardTest.php
git commit -m "feat(copilot): add copilot token guard backed by admin_user"
```

---

### Task 5: Authenticate 多 guard 改造、GET-only 与 AI 审计标记

**Files:**
- Modify: `app/Http/Middleware/Authenticate.php`（整个 `handle()`）
- Modify: `routes/web.php:73`（主路由组的中间件）
- Test: `tests/Feature/Copilot/AuthenticateMultiGuardTest.php`（含 `auditLabel()` 的用例）

**Interfaces:**
- Consumes: `CopilotTokenGuard`、guard 名 `copilot`（Task 4）
- Produces:
  - `auth:web,copilot` 可用于路由中间件
  - copilot 认证通过的请求：非 GET 一律 403；`LOGIN_USER` 常量带 `[AI]` 后缀
  - 认证失败返回 **HTTP 401**；GET-only 拒绝返回 **HTTP 403**

**为什么必须改这个文件：** `bootstrap/app.php:29` 把 `'auth'` 别名指向自写的 `App\Http\Middleware\Authenticate`，其签名是 `handle($request, Closure $next)` —— 不接收 guard 参数，内部直接 `Auth::check()` 走默认 guard（`web`）。PHP 对用户定义函数的多余实参是静默忽略的，所以 `auth:web,copilot` **不会报错也不会生效**，copilot token 会被当作未登录拒掉。

**为什么要显式传 HTTP 状态码：** `Response::fail()` 的 `$status` 默认是 **200**。现有 `Authenticate` 用的就是默认值，所以「未登录」目前返回 `HTTP 200 + is_succ:false + code:10000`。copilot 链路若沿用，前端拦截器永远看不到 401，refresh 永不触发，token 过期后用户会卡死。`web` 分支保持 200 不变（避免改动现有前端），**copilot 分支必须返回真实状态码**。

- [ ] **Step 1: 写失败测试**

创建 `tests/Feature/Copilot/AuthenticateMultiGuardTest.php`：

```php
<?php

namespace Tests\Feature\Copilot;

use App\Http\Middleware\Authenticate;
use App\Repositories\Blockmatrix\AdminUserRepository;
use Illuminate\Contracts\Auth\Guard as GuardContract;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Auth;
use Mockery;
use Mockery\Adapter\Phpunit\MockeryPHPUnitIntegration;
use Tests\TestCase;

class AuthenticateMultiGuardTest extends TestCase
{
    use MockeryPHPUnitIntegration;

    private function middleware(): Authenticate
    {
        return new Authenticate(
            Mockery::mock(GuardContract::class),
            Mockery::mock(AdminUserRepository::class),
        );
    }

    /** 把某个 guard 名伪装成已登录（或未登录）。 */
    private function fakeGuard(string $name, ?array $user): void
    {
        $guard = Mockery::mock(GuardContract::class);
        $guard->shouldReceive('check')->andReturn($user !== null);
        $guard->shouldReceive('user')->andReturn($user === null ? null : (object) $user);
        Auth::shouldReceive('guard')->with($name)->andReturn($guard);
    }

    public function test_copilot_token_is_accepted_on_a_get_request(): void
    {
        Auth::shouldReceive('guard')->with('web')->andReturn(
            Mockery::mock(GuardContract::class)->shouldReceive('check')->andReturn(false)->getMock()
        );
        $this->fakeGuard('copilot', ['id' => 10001, 'login_name' => 'zhangsan']);
        Auth::shouldReceive('shouldUse')->with('copilot')->once();

        $request = Request::create('/api/deposits', 'GET');
        $called = false;

        $response = $this->middleware()->handle($request, function () use (&$called) {
            $called = true;
            return response('ok');
        }, 'web', 'copilot');

        self::assertTrue($called);
        self::assertSame(200, $response->getStatusCode());
    }

    public function test_copilot_token_is_refused_on_a_write_request_with_403(): void
    {
        Auth::shouldReceive('guard')->with('web')->andReturn(
            Mockery::mock(GuardContract::class)->shouldReceive('check')->andReturn(false)->getMock()
        );
        $this->fakeGuard('copilot', ['id' => 10001, 'login_name' => 'zhangsan']);
        Auth::shouldReceive('shouldUse')->with('copilot')->once();

        $request = Request::create('/api/managers/reset', 'PUT');
        $called = false;

        $response = $this->middleware()->handle($request, function () use (&$called) {
            $called = true;
            return response('ok');
        }, 'web', 'copilot');

        self::assertFalse($called, 'GET-only：写请求不得进入业务逻辑');
        self::assertSame(403, $response->getStatusCode());
    }

    public function test_an_unauthenticated_request_gets_a_real_401(): void
    {
        foreach (['web', 'copilot'] as $name) {
            Auth::shouldReceive('guard')->with($name)->andReturn(
                Mockery::mock(GuardContract::class)->shouldReceive('check')->andReturn(false)->getMock()
            );
        }

        $response = $this->middleware()->handle(
            Request::create('/api/deposits', 'GET'),
            fn () => response('ok'),
            'web',
            'copilot'
        );

        // Review Focus 第 1 条：必须是真 401，不是 HTTP 200 + is_succ:false
        self::assertSame(401, $response->getStatusCode());
        self::assertFalse($response->getData(true)['is_succ']);
        self::assertSame(10000, $response->getData(true)['code']);
    }

    public function test_no_guard_argument_falls_back_to_the_default_guard(): void
    {
        config()->set('auth.defaults.guard', 'web');
        $this->fakeGuard('web', ['id' => 1, 'login_name' => 'admin']);
        Auth::shouldReceive('shouldUse')->with('web')->once();

        $called = false;
        $this->middleware()->handle(
            Request::create('/api/deposits', 'GET'),
            function () use (&$called) {
                $called = true;
                return response('ok');
            }
        );

        self::assertTrue($called, '漏传 guard 参数的调用点行为必须不变');
    }

    public function test_write_requests_still_work_for_a_session_user(): void
    {
        $this->fakeGuard('web', ['id' => 1, 'login_name' => 'admin']);
        Auth::shouldReceive('shouldUse')->with('web')->once();

        $called = false;
        $this->middleware()->handle(
            Request::create('/api/managers/reset', 'PUT'),
            function () use (&$called) {
                $called = true;
                return response('ok');
            },
            'web',
            'copilot'
        );

        self::assertTrue($called, 'GET-only 只约束 copilot guard，不影响后台用户');
    }
}
```

**关于 `LOGIN_USER` 的测试：** 它是 `define()` 出来的全局常量，同一个 PHP 进程里只能赋值一次，没法在多个测试方法之间重新断言。所以标签的生成逻辑拆成了静态纯函数 `Authenticate::auditLabel()`，直接测它 —— 比 `#[RunInSeparateProcess]` + facade mock 稳得多（后者在 Laravel 里很容易因为容器未重建而偶发失败）。

追加到同一个测试文件：

```php
    public function test_the_audit_label_marks_copilot_requests(): void
    {
        $user = (object) ['id' => 10001, 'login_name' => 'zhangsan'];

        // 这 20+ 处审计日志引用的常量，AI 发起的请求必须带标记
        self::assertSame('zhangsan(10001)[AI]', Authenticate::auditLabel($user, 'copilot'));
    }

    public function test_the_audit_label_is_unchanged_for_session_users(): void
    {
        $user = (object) ['id' => 1, 'login_name' => 'admin'];

        // 后台用户的日志形态必须和改造前逐字节一致
        self::assertSame('admin(1)', Authenticate::auditLabel($user, 'web'));
    }
```

`(object)` 转换出的 stdClass 不支持 `ArrayAccess`，而 `auditLabel()` 用的是 `$user['login_name']` —— 所以这两个用例要传能按键访问的对象。实现时用一个匿名类或 `AdminUser` 实例：

```php
    private function userLike(int $id, string $loginName): AdminUser
    {
        $user = new AdminUser();
        $user->forceFill(['id' => $id, 'login_name' => $loginName]);

        return $user;
    }
```

把上面两个用例里的 `(object) [...]` 换成 `$this->userLike(10001, 'zhangsan')` / `$this->userLike(1, 'admin')`，并补 `use App\Models\Blockmatrix\AdminUser;`。

- [ ] **Step 2: 跑测试确认失败**

Run: `./vendor/bin/phpunit tests/Feature/Copilot/AuthenticateMultiGuardTest.php`
Expected: FAIL —— `handle()` 不接收第三、第四个参数，`auditLabel()` 不存在

- [ ] **Step 3: 改写 Authenticate**

修改 `app/Http/Middleware/Authenticate.php`，把 `handle()` 整个替换为：

```php
    /**
     * Handle an incoming request.
     *
     * 支持多 guard：`auth:web,copilot`。按顺序尝试，第一个 check() 通过的胜出。
     * 不传 guard 参数时回退默认 guard，保证漏改的调用点行为不变。
     *
     * @param object  $request 请求信息
     * @param Closure $next    匿名函数
     * @param string  ...$guards guard 名
     *
     * @return mixed
     */
    public function handle($request, Closure $next, string ...$guards)
    {
        $guards = $guards ?: [(string) config('auth.defaults.guard')];

        foreach ($guards as $name) {
            $guard = Auth::guard($name);

            if ($guard->check()) {
                // 让后续的 Permission 中间件的 Auth::user() 取到正确的人
                Auth::shouldUse($name);

                // 用户对象取自命中的那个 guard，而不是再调一次 Auth::user()：
                // 少一次解析，也让这个方法不依赖 shouldUse() 的副作用。
                return $this->afterAuthenticated($request, $name, $guard->user(), $next);
            }
        }

        // 真 401，不是 HTTP 200 —— 否则 copilot 前端的 refresh 永不触发。
        return $this->fail(trans('middleware.permission.login_required'), 10000, 401);
    }

    /**
     * 认证通过之后的共用处理：审计标记 + copilot 的 GET-only 约束。
     *
     * @param object  $request 请求信息
     * @param string  $guard   命中的 guard 名
     * @param object  $user    该 guard 解析出的用户
     * @param Closure $next    匿名函数
     *
     * @return mixed
     */
    private function afterAuthenticated($request, string $guard, $user, Closure $next)
    {
        // MVP 的凭证范围限制：copilot token 只能读。放在这里而不是路由上，
        // 是为了让它无法被漏配 —— 任何经 copilot guard 认证的请求都流经此处。
        // copilot 自己的认证端点要豁免（登出是 POST）。
        if ($guard === 'copilot'
            && $request->method() !== 'GET'
            && ! $request->is('api/auth/copilot/*')
        ) {
            return $this->fail(trans('middleware.permission.access_denied'), 0, 403);
        }

        defined('LOGIN_USER') || define('LOGIN_USER', self::auditLabel($user, $guard));

        //设置角色数据权限

        return $next($request);
    }

    /**
     * 审计日志里的操作者标签。
     *
     * LOGIN_USER 被 20+ 处 Log::info 引用（AdminUserService、RoleService、
     * MenuService、WhiteListService、PositionEntryService…）。给 copilot 分支
     * 加 [AI] 后缀，所有现存审计日志自动区分人工与 AI，调用点零改动。
     *
     * 单独拆成静态纯函数，是为了能直接测它 —— LOGIN_USER 是 define() 出来的
     * 常量，同一进程内只能赋值一次，没法在多个测试方法里反复断言。
     *
     * @param object $user  用户对象
     * @param string $guard 命中的 guard 名
     */
    public static function auditLabel($user, string $guard): string
    {
        $marker = $guard === 'copilot' ? '[AI]' : '';

        return $user['login_name'] . '(' . $user['id'] . ')' . $marker;
    }
```

- [ ] **Step 4: 跑测试确认通过**

Run: `./vendor/bin/phpunit tests/Feature/Copilot/AuthenticateMultiGuardTest.php`
Expected: PASS，7 个测试全绿

- [ ] **Step 5: 把主路由组改成双 guard**

修改 `routes/web.php` 第 73 行附近：

```php
        Route::group(
            ['middleware' => ['auth:web,copilot', 'permission']],
            function () {
```

`Permission` 中间件**不要改**：`Auth::shouldUse($guard)` 之后它的 `Auth::user()` 正常解析，`role_id → menu_endpoints.api_path` 的判定对两种 guard 完全一致 —— 这正是「权限边界只有一处」的体现。

- [ ] **Step 6: 跑全量测试**

Run: `./vendor/bin/phpunit`
Expected: PASS，且**现有测试一行未改**

- [ ] **Step 7: 静态检查**

Run: `./vendor/bin/pint --test && ./vendor/bin/phpstan analyse`
Expected: 无新增告警

- [ ] **Step 8: Commit**

```bash
git add app/Http/Middleware/Authenticate.php routes/web.php tests/Feature/Copilot/
git commit -m "feat(copilot): support multi-guard auth with GET-only scope and AI audit marker"
```

---

### Task 6: 换票编排与 5 个认证端点

**Files:**
- Create: `app/Services/Blockmatrix/Copilot/CopilotAuthService.php`
- Create: `app/Http/Controllers/Blockmatrix/CopilotAuthController.php`
- Create: `app/Http/Middleware/CopilotServiceToken.php`
- Modify: `routes/web.php:61-70`（`auth` 路由组）
- Modify: `bootstrap/app.php:29-33`（中间件别名）
- Modify: `app/Services/Blockmatrix/AdminUserService.php`（`resetPassword()`、`changePassword()`）
- Test: `tests/Feature/Copilot/CopilotAuthServiceTest.php`
- Test: `tests/Feature/Copilot/CopilotAuthControllerTest.php`

**Interfaces:**
- Consumes: `TokenIssuer`（Task 1）、`TokenStore`（Task 2）、`LoginService::verifyCaptcha/verifyCredentials` + `LoginRejected`（Task 3）、`TokenIssuer::jwks()`（Task 1）
- Produces（Phase 3 的 ai-copilot-api 直接依赖这些端点的形状）：
  - `POST /api/auth/copilot/token` body `{login_name, password, captcha, key}` → `{is_succ:true, data:{access_token, expires_at, refresh_token, user:{id, login_name}}}`
  - `POST /api/auth/copilot/refresh` body `{refresh_token}` → 同上形状
  - `POST /api/auth/copilot/logout` header `Authorization: Bearer <access>`，body `{refresh_token}` → `{is_succ:true}`
  - `GET /api/auth/copilot/permissions` header `Authorization: Bearer <access>` → `{is_succ:true, data:{api_paths:[...]}}`
  - `GET /api/auth/copilot/jwks` → `{keys:[...]}`（**裸 JWKS，不套 is_succ 信封** —— 标准格式，Go/Python 的 JWKS 客户端直接消费）
  - 失败一律 `HTTP 401` + `{is_succ:false, code:"AUTH_*", message}`
  - `CopilotAuthService::issueForPassword(string $loginName, string $password, ?string $captcha, ?string $key): array`
  - `CopilotAuthService::issueForRefresh(string $refreshToken): array`
  - `CopilotAuthService::logout(array $claims, ?string $refreshToken): void`
  - `CopilotAuthService::apiPathsForUser(array $user): array`

**按 id 取用户用现成方法：** `AdminUserRepository` 没有 `getUserInfoById`。它的 `getUserInfoByAccount($value, $column = 'login_name')` 第二个参数就是为此准备的 —— 传 `'id'` 即可。注意该方法**已经内置 `where('status', AdminUser::STATUS_NORMAL)`**：账号被禁用或删除时它返回 `null`，所以 refresh 路径必须处理 null；而密码路径根本看不到被禁用的账号（返回 null → `verifyCredentials()` 抛 `INVALID_CREDENTIALS`）。

- [ ] **Step 1: 写 Service 的失败测试**

创建 `tests/Feature/Copilot/CopilotAuthServiceTest.php`：

```php
<?php

namespace Tests\Feature\Copilot;

use App\Exceptions\LoginRejected;
use App\Models\Blockmatrix\AdminUser;
use App\Repositories\Blockmatrix\AdminUserRepository;
use App\Services\Blockmatrix\AuthService;
use App\Services\Blockmatrix\Copilot\CopilotAuthService;
use App\Services\Blockmatrix\Copilot\RefreshTokenReplayed;
use App\Services\Blockmatrix\Copilot\TokenIssuer;
use App\Services\Blockmatrix\Copilot\TokenStore;
use App\Services\Blockmatrix\LoginService;
use Mockery;
use Mockery\Adapter\Phpunit\MockeryPHPUnitIntegration;
use Tests\TestCase;

class CopilotAuthServiceTest extends TestCase
{
    use MockeryPHPUnitIntegration;

    private const USER = ['id' => 10001, 'login_name' => 'zhangsan', 'role_id' => 7, 'status' => 1];

    private function issuer(): TokenIssuer
    {
        $resource = openssl_pkey_new(['private_key_bits' => 2048, 'private_key_type' => OPENSSL_KEYTYPE_RSA]);
        openssl_pkey_export($resource, $private);

        return new TokenIssuer($private, openssl_pkey_get_details($resource)['key'], 'kid', 1800);
    }

    public function test_password_grant_returns_an_access_and_a_refresh_token(): void
    {
        $login = Mockery::mock(LoginService::class);
        $login->shouldReceive('verifyCaptcha')->with('k', 'abcd')->once();
        $login->shouldReceive('verifyCredentials')->with('zhangsan', 'pw')->once()->andReturn(self::USER);

        $store = Mockery::mock(TokenStore::class);
        $store->shouldReceive('storeRefresh')->once();

        $issuer = $this->issuer();
        $service = new CopilotAuthService($login, $issuer, $store, Mockery::mock(AuthService::class), $this->users());

        $result = $service->issueForPassword('zhangsan', 'pw', 'abcd', 'k');

        $claims = $issuer->verify($result['access_token']);
        self::assertSame('10001', $claims['sub']);
        self::assertSame('ai-copilot', $claims['actor']);
        self::assertSame(64, strlen($result['refresh_token']));
        self::assertSame($claims['exp'], $result['expires_at']);
        self::assertSame(['id' => 10001, 'login_name' => 'zhangsan'], $result['user']);
    }

    public function test_password_grant_propagates_the_login_rejection_reason(): void
    {
        $login = Mockery::mock(LoginService::class);
        $login->shouldReceive('verifyCaptcha')->once();
        $login->shouldReceive('verifyCredentials')->once()
            ->andThrow(new LoginRejected(LoginRejected::INVALID_CREDENTIALS, 'nope'));

        $service = new CopilotAuthService(
            $login,
            $this->issuer(),
            Mockery::mock(TokenStore::class),
            Mockery::mock(AuthService::class),
            $this->users(),
        );

        $this->expectException(LoginRejected::class);
        $service->issueForPassword('zhangsan', 'bad', 'abcd', 'k');
    }

    public function test_refreshing_for_an_account_that_was_disabled_is_refused(): void
    {
        // getUserInfoByAccount 内置 where(status, STATUS_NORMAL)，所以账号被
        // 禁用后按 id 查会返回 null。密码路径看不到这种情况（那边更早就被
        // verifyCredentials 以 INVALID_CREDENTIALS 拒掉了），但已登录的用户
        // 中途被禁用时，refresh 必须拒。
        $store = Mockery::mock(TokenStore::class);
        $store->shouldReceive('consumeRefresh')->once()->andReturn(10001);

        $users = Mockery::mock(AdminUserRepository::class);
        $users->shouldReceive('getUserInfoByAccount')->with(10001, 'id')->once()->andReturn(null);

        $service = new CopilotAuthService(
            Mockery::mock(LoginService::class),
            $this->issuer(),
            $store,
            Mockery::mock(AuthService::class),
            $users,
        );

        try {
            $service->issueForRefresh('r');
            self::fail('期望抛 LoginRejected');
        } catch (LoginRejected $e) {
            self::assertSame(LoginRejected::ACCOUNT_DISABLED, $e->reason);
        }
    }

    public function test_refresh_grant_rotates_the_refresh_token(): void
    {
        $store = Mockery::mock(TokenStore::class);
        $store->shouldReceive('consumeRefresh')->with('old-refresh')->once()->andReturn(10001);
        $store->shouldReceive('storeRefresh')->once();

        $service = new CopilotAuthService(
            Mockery::mock(LoginService::class),
            $this->issuer(),
            $store,
            Mockery::mock(AuthService::class),
            $this->users(),
        );

        $result = $service->issueForRefresh('old-refresh');

        self::assertNotSame('old-refresh', $result['refresh_token']);
        self::assertSame(64, strlen($result['refresh_token']));
    }

    public function test_replayed_refresh_revokes_every_refresh_of_that_user(): void
    {
        $store = Mockery::mock(TokenStore::class);
        $store->shouldReceive('consumeRefresh')->once()->andThrow(new RefreshTokenReplayed(10001));
        // 盗用信号：必须连带撤销该用户全部 refresh
        $store->shouldReceive('revokeAllRefreshForUser')->with(10001)->once();

        $service = new CopilotAuthService(
            Mockery::mock(LoginService::class),
            $this->issuer(),
            $store,
            Mockery::mock(AuthService::class),
            $this->users(),
        );

        $this->expectException(RefreshTokenReplayed::class);
        $service->issueForRefresh('replayed');
    }

    public function test_an_unknown_refresh_token_does_not_trigger_mass_revocation(): void
    {
        $store = Mockery::mock(TokenStore::class);
        $store->shouldReceive('consumeRefresh')->once()->andThrow(new RefreshTokenReplayed(null));
        $store->shouldNotReceive('revokeAllRefreshForUser');

        $service = new CopilotAuthService(
            Mockery::mock(LoginService::class),
            $this->issuer(),
            $store,
            Mockery::mock(AuthService::class),
            $this->users(),
        );

        $this->expectException(RefreshTokenReplayed::class);
        $service->issueForRefresh('never-issued');
    }

    public function test_logout_revokes_the_jti_and_the_refresh_token(): void
    {
        $store = Mockery::mock(TokenStore::class);
        $store->shouldReceive('revokeJti')->with('jti-1', 1999999999)->once();
        $store->shouldReceive('consumeRefresh')->with('r')->once()->andReturn(10001);

        $service = new CopilotAuthService(
            Mockery::mock(LoginService::class),
            $this->issuer(),
            $store,
            Mockery::mock(AuthService::class),
            $this->users(),
        );

        $service->logout(['jti' => 'jti-1', 'exp' => 1999999999], 'r');
    }

    public function test_api_paths_come_from_the_same_source_as_the_permission_middleware(): void
    {
        $authService = Mockery::mock(AuthService::class);
        // 与 Permission 中间件调用的是同一个方法 —— 词表只有一份
        $authService->shouldReceive('getUserMenu')->with(self::USER)->once()
            ->andReturn(['api/deposits', 'api/deposits/statistics']);

        $service = new CopilotAuthService(
            Mockery::mock(LoginService::class),
            $this->issuer(),
            Mockery::mock(TokenStore::class),
            $authService,
            $this->users(),
        );

        self::assertSame(
            ['api/deposits', 'api/deposits/statistics'],
            $service->apiPathsForUser(self::USER)
        );
    }

    /**
     * 仓库返回 Eloquent 模型（生产行为），不是数组 —— issueForRefresh 对它调
     * toArray()。用数组 mock 会掩盖掉强制类型转换的坑。
     */
    private function users(): AdminUserRepository
    {
        $model = new AdminUser();
        $model->forceFill(self::USER);

        $repo = Mockery::mock(AdminUserRepository::class);
        $repo->shouldReceive('getUserInfoByAccount')->with(10001, 'id')->andReturn($model);

        return $repo;
    }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./vendor/bin/phpunit tests/Feature/Copilot/CopilotAuthServiceTest.php`
Expected: FAIL，`Class "App\Services\Blockmatrix\Copilot\CopilotAuthService" not found`

- [ ] **Step 3: 实现 CopilotAuthService**

创建 `app/Services/Blockmatrix/Copilot/CopilotAuthService.php`：

```php
<?php

namespace App\Services\Blockmatrix\Copilot;

use App\Exceptions\LoginRejected;
use App\Models\Blockmatrix\AdminUser;
use App\Repositories\Blockmatrix\AdminUserRepository;
use App\Services\Blockmatrix\AuthService;
use App\Services\Blockmatrix\LoginService;

/**
 * copilot 换票的编排层。
 *
 * 不签发（TokenIssuer）、不碰 Redis（TokenStore）、不校验密码（LoginService）。
 * 这里只决定「按什么顺序做、失败怎么办」。
 */
class CopilotAuthService
{
    public function __construct(
        private LoginService $loginService,
        private TokenIssuer $issuer,
        private TokenStore $store,
        private AuthService $authService,
        private AdminUserRepository $users,
    ) {
    }

    /**
     * 密码换票。不建 session、不调 Auth::loginUsingId()。
     *
     * @throws LoginRejected
     */
    public function issueForPassword(
        string $loginName,
        string $password,
        ?string $captcha,
        ?string $key
    ): array {
        $this->loginService->verifyCaptcha($key, $captcha);
        $user = $this->loginService->verifyCredentials($loginName, $password);

        return $this->issueFor($user);
    }

    /**
     * refresh 换票，并轮换 refresh token。
     *
     * @throws RefreshTokenReplayed
     */
    public function issueForRefresh(string $refreshToken): array
    {
        try {
            $userId = $this->store->consumeRefresh($refreshToken);
        } catch (RefreshTokenReplayed $e) {
            if ($e->userId !== null) {
                // 这个串曾经有效、已被轮换掉 —— 凭证泄露的信号。
                // userId 为 null 时是伪造或早已过期，不做全量撤销，
                // 否则任何人都能用一个乱编的串把别人踢下线。
                $this->store->revokeAllRefreshForUser($e->userId);
            }
            throw $e;
        }

        // getUserInfoByAccount 内置 status = STATUS_NORMAL 过滤，所以被禁用或
        // 删除的账号在这里就是 null。
        $user = $this->users->getUserInfoByAccount($userId, 'id');
        if (empty($user)) {
            throw new LoginRejected(
                LoginRejected::ACCOUNT_DISABLED,
                trans('middleware.permission.login_required')
            );
        }

        // toArray()，不是 (array)：仓库返回的是 Eloquent 模型，强制类型转换给出
        // 的是它的内部属性（` * attributes` 这类名字被修饰的键），不是属性数组。
        return $this->issueFor($user->toArray());
    }

    public function logout(array $claims, ?string $refreshToken): void
    {
        if (! empty($claims['jti']) && ! empty($claims['exp'])) {
            $this->store->revokeJti((string) $claims['jti'], (int) $claims['exp']);
        }
        if (! empty($refreshToken)) {
            try {
                $this->store->consumeRefresh($refreshToken);
            } catch (RefreshTokenReplayed) {
                // 登出时 refresh 已失效不是错误 —— 目标状态已达成
            }
        }
    }

    /**
     * 该用户有权访问的 api_path 列表。
     *
     * 刻意复用 Permission 中间件用的同一个 AuthService::getUserMenu()：
     * 词表只有一份，不会漂移。
     */
    public function apiPathsForUser(array $user): array
    {
        return array_values($this->authService->getUserMenu($user));
    }

    public function jwks(): array
    {
        return $this->issuer->jwks();
    }

    private function issueFor(array $user): array
    {
        if ((int) ($user['status'] ?? 0) !== AdminUser::STATUS_NORMAL) {
            throw new LoginRejected(
                LoginRejected::ACCOUNT_DISABLED,
                trans('middleware.permission.login_required')
            );
        }

        $isSysAdmin = ($user['login_name'] ?? null) === AdminUser::SYS_ADMIN;
        $access     = $this->issuer->issueAccessToken($user, $isSysAdmin);
        $refresh    = $this->issuer->newRefreshToken();

        $this->store->storeRefresh($refresh, (int) $user['id']);

        return [
            'access_token'  => $access->jwt,
            'expires_at'    => $access->expiresAt,
            'refresh_token' => $refresh,
            'user'          => [
                'id'         => (int) $user['id'],
                'login_name' => $user['login_name'],
            ],
        ];
    }
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `./vendor/bin/phpunit tests/Feature/Copilot/CopilotAuthServiceTest.php`
Expected: PASS，8 个测试全绿

- [ ] **Step 5: 写 Controller 的失败测试**

创建 `tests/Feature/Copilot/CopilotAuthControllerTest.php`：

```php
<?php

namespace Tests\Feature\Copilot;

use App\Exceptions\LoginRejected;
use App\Http\Controllers\Blockmatrix\CopilotAuthController;
use App\Services\Blockmatrix\Copilot\CopilotAuthService;
use App\Services\Blockmatrix\Copilot\RefreshTokenReplayed;
use Illuminate\Http\Request;
use Mockery;
use Mockery\Adapter\Phpunit\MockeryPHPUnitIntegration;
use Tests\TestCase;

class CopilotAuthControllerTest extends TestCase
{
    use MockeryPHPUnitIntegration;

    public function test_successful_token_grant_returns_the_shape_copilot_api_expects(): void
    {
        $service = Mockery::mock(CopilotAuthService::class);
        $service->shouldReceive('issueForPassword')->once()->andReturn([
            'access_token'  => 'jwt-value',
            'expires_at'    => 1999999999,
            'refresh_token' => 'refresh-value',
            'user'          => ['id' => 10001, 'login_name' => 'zhangsan'],
        ]);

        $response = (new CopilotAuthController($service))->token(Request::create(
            '/api/auth/copilot/token',
            'POST',
            ['login_name' => 'zhangsan', 'password' => 'pw', 'captcha' => 'abcd', 'key' => 'k']
        ));

        self::assertSame(200, $response->getStatusCode());
        $body = $response->getData(true);
        self::assertTrue($body['is_succ']);
        self::assertSame('jwt-value', $body['data']['access_token']);
        self::assertSame('refresh-value', $body['data']['refresh_token']);
        self::assertSame(1999999999, $body['data']['expires_at']);
        self::assertSame(10001, $body['data']['user']['id']);
    }

    public function test_rejected_login_returns_a_real_401_with_a_stable_code(): void
    {
        $service = Mockery::mock(CopilotAuthService::class);
        $service->shouldReceive('issueForPassword')->once()
            ->andThrow(new LoginRejected(LoginRejected::INVALID_CREDENTIALS, '账号或密码错误'));

        $response = (new CopilotAuthController($service))->token(Request::create(
            '/api/auth/copilot/token',
            'POST',
            ['login_name' => 'zhangsan', 'password' => 'bad', 'captcha' => 'abcd', 'key' => 'k']
        ));

        // Review Focus 第 1 条：真 401，否则前端拦截器看不见
        self::assertSame(401, $response->getStatusCode());
        $body = $response->getData(true);
        self::assertFalse($body['is_succ']);
        self::assertSame('AUTH_INVALID_CREDENTIALS', $body['code']);
    }

    public function test_captcha_failures_keep_their_own_codes(): void
    {
        foreach ([
            LoginRejected::CAPTCHA_EXPIRED => 'AUTH_CAPTCHA_EXPIRED',
            LoginRejected::CAPTCHA_INVALID => 'AUTH_CAPTCHA_INVALID',
        ] as $reason => $expected) {
            $service = Mockery::mock(CopilotAuthService::class);
            $service->shouldReceive('issueForPassword')->once()
                ->andThrow(new LoginRejected($reason, 'x'));

            $response = (new CopilotAuthController($service))->token(Request::create(
                '/api/auth/copilot/token',
                'POST',
                ['login_name' => 'a', 'password' => 'b', 'captcha' => 'c', 'key' => 'd']
            ));

            self::assertSame(401, $response->getStatusCode());
            self::assertSame($expected, $response->getData(true)['code']);
        }
    }

    public function test_replayed_refresh_returns_401(): void
    {
        $service = Mockery::mock(CopilotAuthService::class);
        $service->shouldReceive('issueForRefresh')->once()->andThrow(new RefreshTokenReplayed(10001));

        $response = (new CopilotAuthController($service))->refresh(
            Request::create('/api/auth/copilot/refresh', 'POST', ['refresh_token' => 'replayed'])
        );

        self::assertSame(401, $response->getStatusCode());
        self::assertSame('AUTH_REFRESH_REJECTED', $response->getData(true)['code']);
    }

    public function test_jwks_is_returned_bare_without_the_is_succ_envelope(): void
    {
        $service = Mockery::mock(CopilotAuthService::class);
        $service->shouldReceive('jwks')->once()->andReturn(['keys' => [['kty' => 'RSA', 'kid' => 'k']]]);

        $response = (new CopilotAuthController($service))->jwks();

        $body = $response->getData(true);
        // 标准 JWKS：Go 和 Python 的 JWKS 客户端直接消费，不能套业务信封
        self::assertArrayHasKey('keys', $body);
        self::assertArrayNotHasKey('is_succ', $body);
        self::assertSame('RSA', $body['keys'][0]['kty']);
    }
}
```

- [ ] **Step 6: 跑测试确认失败**

Run: `./vendor/bin/phpunit tests/Feature/Copilot/CopilotAuthControllerTest.php`
Expected: FAIL，`Class "App\Http\Controllers\Blockmatrix\CopilotAuthController" not found`

- [ ] **Step 7: 实现 Controller**

创建 `app/Http/Controllers/Blockmatrix/CopilotAuthController.php`：

```php
<?php

namespace App\Http\Controllers\Blockmatrix;

use App\Auth\CopilotTokenGuard;
use App\Exceptions\LoginRejected;
use App\Http\Controllers\BaseController;
use App\Services\Blockmatrix\Copilot\CopilotAuthService;
use App\Services\Blockmatrix\Copilot\RefreshTokenReplayed;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Auth;

/**
 * copilot 链路的认证端点。
 *
 * 失败一律返回真实 HTTP 401 并带稳定错误码 —— 项目默认的 fail() 是 HTTP 200，
 * 那样 copilot 前端的 refresh 拦截器永远不会触发。
 */
class CopilotAuthController extends BaseController
{
    public function __construct(
        private CopilotAuthService $copilotAuth
    ) {
    }

    public function token(Request $request): JsonResponse
    {
        try {
            $result = $this->copilotAuth->issueForPassword(
                (string) $request->input('login_name'),
                (string) $request->input('password'),
                $request->input('captcha'),
                $request->input('key'),
            );
        } catch (LoginRejected $e) {
            return $this->failWithCode($e->reason, $e->getMessage());
        }

        return $this->succeed($result);
    }

    public function refresh(Request $request): JsonResponse
    {
        try {
            $result = $this->copilotAuth->issueForRefresh((string) $request->input('refresh_token'));
        } catch (RefreshTokenReplayed) {
            return $this->failWithCode('AUTH_REFRESH_REJECTED', trans('middleware.permission.login_required'));
        } catch (LoginRejected $e) {
            return $this->failWithCode($e->reason, $e->getMessage());
        }

        return $this->succeed($result);
    }

    public function logout(Request $request): JsonResponse
    {
        // claims() 不在 Guard 契约上，直接调会被 larastan 拦下来。
        $guard  = Auth::guard('copilot');
        $claims = $guard instanceof CopilotTokenGuard ? ($guard->claims() ?? []) : [];

        $this->copilotAuth->logout($claims, $request->input('refresh_token'));

        return $this->succeed();
    }

    public function permissions(): JsonResponse
    {
        return $this->succeed([
            'api_paths' => $this->copilotAuth->apiPathsForUser((array) Auth::user()),
        ]);
    }

    /** 裸 JWKS —— 标准格式，不套 is_succ 信封。 */
    public function jwks(): JsonResponse
    {
        return response()->json($this->copilotAuth->jwks());
    }

    private function failWithCode(string $code, string $message): JsonResponse
    {
        return response()->json([
            'is_succ' => false,
            'code'    => $code,
            'message' => $message,
            'data'    => null,
        ], 401);
    }
}
```

- [ ] **Step 8: 实现 service token 中间件**

创建 `app/Http/Middleware/CopilotServiceToken.php`：

```php
<?php

namespace App\Http\Middleware;

use Closure;
use Illuminate\Http\Request;

/**
 * 只有被授权服务可以换票。
 *
 * 这一道是为了让「知道某个运营账号密码」的人也无法在 copilot 体系外自行换票。
 * 与 internal-mcp-server 自己的 service token 是两个不同的凭证。
 */
class CopilotServiceToken
{
    public function handle(Request $request, Closure $next)
    {
        $expected = (string) config('copilot.service_token');
        $provided = (string) $request->header('X-Service-Token', '');

        if ($expected === '' || ! hash_equals($expected, $provided)) {
            return response()->json([
                'is_succ' => false,
                'code'    => 'AUTH_SERVICE_UNAUTHORIZED',
                'message' => 'service not authorised',
                'data'    => null,
            ], 401);
        }

        return $next($request);
    }
}
```

`$expected === ''` 也拒绝：未配置 service token 时必须是「全部拒绝」而不是「全部放行」。

- [ ] **Step 9: 注册中间件别名与路由**

修改 `bootstrap/app.php` 的 `$middleware->alias([...])`，新增一行：

```php
            'copilot.service' => CopilotServiceToken::class,
```

并补 `use App\Http\Middleware\CopilotServiceToken;`。

修改 `routes/web.php` 的 `Route::prefix('auth')` 组，在现有路由之后追加：

```php
                Route::prefix('copilot')->group(function () {
                    // 换票要服务身份；密码爆破另有 throttle:login 兜一层
                    Route::post('token', [CopilotAuthController::class, 'token'])
                        ->middleware(['copilot.service', 'throttle:login'])
                        ->withoutMiddleware([PreventRequestForgery::class]);

                    Route::post('refresh', [CopilotAuthController::class, 'refresh'])
                        ->middleware('copilot.service')
                        ->withoutMiddleware([PreventRequestForgery::class]);

                    Route::post('logout', [CopilotAuthController::class, 'logout'])
                        ->middleware('auth:copilot')
                        ->withoutMiddleware([PreventRequestForgery::class]);

                    // 不挂 permission —— 会死锁（要查权限得先有权限），
                    // 与现有 managers/permissions 走 $filterApiArr 白名单同理。
                    Route::get('permissions', [CopilotAuthController::class, 'permissions'])
                        ->middleware('auth:copilot');

                    // 公开：只含公钥
                    Route::get('jwks', [CopilotAuthController::class, 'jwks']);
                });
```

补 `use App\Http\Controllers\Blockmatrix\CopilotAuthController;`。

注意 `logout` 挂 `auth:copilot` 而非 `auth:web,copilot` —— 它读的是 copilot guard 的 claims。这条路由是 POST，会撞上 Task 5 的 GET-only 检查；该检查**已经**包含了 `! $request->is('api/auth/copilot/*')` 的豁免，所以无需再改中间件。在 `AuthenticateMultiGuardTest` 里补一个用例把这个豁免钉住：

```php
    public function test_copilot_own_auth_endpoints_are_exempt_from_get_only(): void
    {
        $guard = Mockery::mock(GuardContract::class);
        $guard->shouldReceive('check')->andReturn(true);
        $guard->shouldReceive('user')->andReturn($this->userLike(1, 'a'));
        Auth::shouldReceive('guard')->with('copilot')->andReturn($guard);
        Auth::shouldReceive('shouldUse')->with('copilot')->once();

        $called = false;
        $this->middleware()->handle(
            Request::create('/api/auth/copilot/logout', 'POST'),
            function () use (&$called) {
                $called = true;
                return response('ok');
            },
            'copilot'
        );

        self::assertTrue($called, '登出必须能走 POST');
    }
```

- [ ] **Step 10: 改密与重置密码时撤销 copilot refresh**

修改 `app/Services/Blockmatrix/AdminUserService.php`：注入 `TokenStore`，在 `resetPassword()`（约第 111 行的 `Log::info` 附近）和 `changePassword()` 成功路径上各加一行：

```php
        $this->copilotTokenStore->revokeAllRefreshForUser((int) $id);
```

（`changePassword()` 里用它实际的用户 id 变量名。）

在 `tests/Feature/Copilot/CopilotAuthServiceTest.php` 旁新建 `tests/Feature/Copilot/PasswordChangeRevokesCopilotTest.php`，断言改密路径调用了 `revokeAllRefreshForUser`：

```php
<?php

namespace Tests\Feature\Copilot;

use App\Repositories\Api\AreaRepository;
use App\Repositories\Blockmatrix\AdminUserRepository;
use App\Repositories\Blockmatrix\RoleAreaRepository;
use App\Repositories\Blockmatrix\RoleRepository;
use App\Services\Blockmatrix\AdminUserService;
use App\Services\Blockmatrix\AuthService;
use App\Services\Blockmatrix\Copilot\TokenStore;
use App\Services\Blockmatrix\MenuService;
use App\Services\Blockmatrix\UserLevelService;
use Mockery;
use Mockery\Adapter\Phpunit\MockeryPHPUnitIntegration;
use Tests\TestCase;

class PasswordChangeRevokesCopilotTest extends TestCase
{
    use MockeryPHPUnitIntegration;

    public function test_resetting_a_password_revokes_that_users_copilot_refresh_tokens(): void
    {
        $store = Mockery::mock(TokenStore::class);
        $store->shouldReceive('revokeAllRefreshForUser')->with(10001)->once();

        $repo = Mockery::mock(AdminUserRepository::class);
        $repo->shouldReceive('update')->andReturn(1);

        // 构造签名取自 AdminUserService::__construct，新增的 TokenStore 追加在最后。
        $service = new AdminUserService(
            $repo,
            Mockery::mock(RoleRepository::class),
            Mockery::mock(RoleAreaRepository::class),
            Mockery::mock(AreaRepository::class),
            Mockery::mock(AuthService::class),
            Mockery::mock(MenuService::class),
            Mockery::mock(UserLevelService::class),
            $store,
        );

        $service->resetPassword(10001, 'new-password');
    }
}
```

`AdminUserService::__construct()` 现有 7 个参数（`AdminUserRepository`、`RoleRepository`、`RoleAreaRepository`、`AreaRepository`、`AuthService`、`MenuService`、`UserLevelService`）。`TokenStore` 追加为第 8 个，放最后，避免改动任何现有调用点的实参顺序。`resetPassword()` 的实际参数名与签名实现时按原文件确认。

- [ ] **Step 11: 跑全部 Phase 1 测试**

Run: `./vendor/bin/phpunit`
Expected: PASS，现有测试一行未改

- [ ] **Step 12: 静态检查**

Run: `./vendor/bin/pint --test && ./vendor/bin/phpstan analyse`
Expected: 无新增告警

- [ ] **Step 13: 生成一对密钥并手工验证整条链路**

```bash
openssl genrsa -out /tmp/copilot-private.pem 2048
openssl rsa -in /tmp/copilot-private.pem -pubout -out /tmp/copilot-public.pem

export COPILOT_JWT_PRIVATE_KEY="$(cat /tmp/copilot-private.pem)"
export COPILOT_JWT_PUBLIC_KEY="$(cat /tmp/copilot-public.pem)"
export COPILOT_SERVICE_TOKEN="local-dev-service-token"

php artisan serve --port=8000 &

# 1. JWKS 可取
curl -s localhost:8000/api/auth/copilot/jwks | head -c 200
# 2. 缺 service token → 401
curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8000/api/auth/copilot/token
# 3. 换票（用一个真实的 admin_user 账号）
curl -s -X POST localhost:8000/api/auth/copilot/token \
  -H 'X-Service-Token: local-dev-service-token' \
  -H 'Content-Type: application/json' \
  -d '{"login_name":"<真实账号>","password":"<密码>","captcha":"","key":""}'
# 4. 用拿到的 access token 查权限
curl -s localhost:8000/api/auth/copilot/permissions -H "Authorization: Bearer <access_token>"
# 5. 用它打一个写端点 → 必须 403
curl -s -o /dev/null -w '%{http_code}\n' -X PUT localhost:8000/api/managers/password \
  -H "Authorization: Bearer <access_token>"
```

Expected：第 2 步 `401`，第 4 步返回 `api_paths` 数组且元素形如 `api/deposits`，第 5 步 `403`。

- [ ] **Step 14: Commit**

```bash
git add app/Services/Blockmatrix/Copilot/CopilotAuthService.php \
        app/Http/Controllers/Blockmatrix/CopilotAuthController.php \
        app/Http/Middleware/CopilotServiceToken.php \
        app/Http/Middleware/Authenticate.php \
        app/Services/Blockmatrix/AdminUserService.php \
        bootstrap/app.php routes/web.php tests/Feature/Copilot/
git commit -m "feat(copilot): add token grant, refresh, logout, permissions and jwks endpoints"
```

**Phase 1 完成标志：** 上面第 13 步的五条 curl 全部符合预期。Phase 2 和 Phase 3 依赖 `/api/auth/copilot/jwks` 与 `/api/auth/copilot/permissions` 这两个端点的真实响应。

---

# Phase 2 — internal-mcp-server

所有 Phase 2 的命令都在 `/Users/antony/Workspace/mangox/ai-copilot/internal-mcp-server` 下执行。测试风格照抄 `internal/auth/service_test.go`：标准库 `testing` + `httptest`，无第三方断言库，表驱动或独立函数皆可。

---

### Task 7: JWKS 验签器

**Files:**
- Create: `internal/auth/verifier.go`
- Create: `internal/auth/verifier_test.go`
- Modify: `go.mod`（新增 `github.com/golang-jwt/jwt/v5`）

**Interfaces:**
- Consumes: Phase 1 的 `GET /api/auth/copilot/jwks`（裸 JWKS，不套 `is_succ` 信封）
- Produces:
  - `type Claims struct { Subject string; LoginName string; RoleID int64; IsSysAdmin bool; Actor string; JTI string; ExpiresAt time.Time }`
  - `type TokenVerifier interface { Verify(ctx context.Context, token string) (Claims, error) }`
  - `func NewJWKSVerifier(url string, ttl time.Duration, client *http.Client) *JWKSVerifier`
  - 常量 `ExpectedIssuer = "admin-api"`、`ExpectedAudience = "copilot"`、`ClockSkewLeeway = 60 * time.Second`

**为什么手写 JWKS 解析而不引入 keyfunc 库：** 本仓库只有两个直接依赖，JWKS 的 RSA 分支就是把 `n`/`e` 两个 base64url 串转成 `rsa.PublicKey`，二十行代码。多一个依赖不值。

- [ ] **Step 1: 装依赖**

```bash
go get github.com/golang-jwt/jwt/v5
```

- [ ] **Step 2: 写失败测试**

创建 `internal/auth/verifier_test.go`：

```go
package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type signer struct {
	key *rsa.PrivateKey
	kid string
}

func newSigner(t *testing.T, kid string) signer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return signer{key: key, kid: kid}
}

// sign builds a token with the claim set admin-api issues. Overrides let a
// test bend one field without restating the rest.
func (s signer) sign(t *testing.T, overrides map[string]any) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":          ExpectedIssuer,
		"aud":          []string{ExpectedAudience},
		"sub":          "10001",
		"login_name":   "zhangsan",
		"role_id":      7,
		"is_sys_admin": false,
		"actor":        "ai-copilot",
		"jti":          "jti-1",
		"iat":          time.Now().Unix(),
		"exp":          time.Now().Add(30 * time.Minute).Unix(),
	}
	for key, value := range overrides {
		claims[key] = value
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = s.kid

	signed, err := token.SignedString(s.key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

func (s signer) jwks() map[string]any {
	n := base64.RawURLEncoding.EncodeToString(s.key.PublicKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(s.key.PublicKey.E)).Bytes())
	return map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": s.kid, "n": n, "e": e,
	}}}
}

// jwksServer serves the signer's public key and counts how often it is hit.
func jwksServer(t *testing.T, s signer, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.jwks())
	}))
	t.Cleanup(server.Close)
	return server
}

func TestVerifyAcceptsAValidToken(t *testing.T) {
	s := newSigner(t, "kid-1")
	server := jwksServer(t, s, nil)
	verifier := NewJWKSVerifier(server.URL, time.Minute, server.Client())

	claims, err := verifier.Verify(context.Background(), s.sign(t, nil))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if claims.Subject != "10001" || claims.LoginName != "zhangsan" {
		t.Errorf("claims = %+v", claims)
	}
	if claims.RoleID != 7 {
		t.Errorf("role_id = %d, want 7", claims.RoleID)
	}
	if claims.Actor != "ai-copilot" {
		t.Errorf("actor = %q", claims.Actor)
	}
	if claims.JTI != "jti-1" {
		t.Errorf("jti = %q", claims.JTI)
	}
}

func TestVerifyRejectsAnotherSignersToken(t *testing.T) {
	trusted := newSigner(t, "kid-1")
	foreign := newSigner(t, "kid-1") // same kid, different key
	server := jwksServer(t, trusted, nil)
	verifier := NewJWKSVerifier(server.URL, time.Minute, server.Client())

	if _, err := verifier.Verify(context.Background(), foreign.sign(t, nil)); err == nil {
		t.Fatal("expected a signature failure")
	}
}

func TestVerifyRejectsWrongIssuerAndAudience(t *testing.T) {
	s := newSigner(t, "kid-1")
	server := jwksServer(t, s, nil)
	verifier := NewJWKSVerifier(server.URL, time.Minute, server.Client())

	for name, overrides := range map[string]map[string]any{
		"issuer":   {"iss": "someone-else"},
		"audience": {"aud": []string{"not-copilot"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := verifier.Verify(context.Background(), s.sign(t, overrides)); err == nil {
				t.Fatalf("expected %s to be rejected", name)
			}
		})
	}
}

// Review Focus 第 2 条：admin-api、MCP、copilot-api 三处独立验签，时钟各自漂移。
// exp 刚过去几秒必须仍然接受，否则会产生零星且不可复现的 401。
func TestVerifyToleratesClockSkewWithinTheLeeway(t *testing.T) {
	s := newSigner(t, "kid-1")
	server := jwksServer(t, s, nil)
	verifier := NewJWKSVerifier(server.URL, time.Minute, server.Client())

	justExpired := s.sign(t, map[string]any{
		"exp": time.Now().Add(-10 * time.Second).Unix(),
	})

	if _, err := verifier.Verify(context.Background(), justExpired); err != nil {
		t.Fatalf("a token expired 10s ago must survive the %v leeway: %v", ClockSkewLeeway, err)
	}
}

func TestVerifyRejectsExpiryBeyondTheLeeway(t *testing.T) {
	s := newSigner(t, "kid-1")
	server := jwksServer(t, s, nil)
	verifier := NewJWKSVerifier(server.URL, time.Minute, server.Client())

	longExpired := s.sign(t, map[string]any{
		"exp": time.Now().Add(-(ClockSkewLeeway + 30*time.Second)).Unix(),
	})

	if _, err := verifier.Verify(context.Background(), longExpired); err == nil {
		t.Fatal("expected an expired token to be rejected")
	}
}

func TestVerifyCachesTheKeySet(t *testing.T) {
	s := newSigner(t, "kid-1")
	var hits atomic.Int64
	server := jwksServer(t, s, &hits)
	verifier := NewJWKSVerifier(server.URL, time.Minute, server.Client())

	for range 3 {
		if _, err := verifier.Verify(context.Background(), s.sign(t, nil)); err != nil {
			t.Fatalf("verify: %v", err)
		}
	}

	if hits.Load() != 1 {
		t.Errorf("jwks fetches = %d, want 1 (the set must be cached)", hits.Load())
	}
}

func TestVerifyRefetchesOnceForAnUnknownKid(t *testing.T) {
	s := newSigner(t, "kid-rotated")
	var hits atomic.Int64
	server := jwksServer(t, s, &hits)
	verifier := NewJWKSVerifier(server.URL, time.Hour, server.Client())

	// Prime the cache with a set that does not contain kid-rotated.
	verifier.seedForTest(map[string]*rsa.PublicKey{"kid-old": &s.key.PublicKey}, time.Now())

	if _, err := verifier.Verify(context.Background(), s.sign(t, nil)); err != nil {
		t.Fatalf("an unknown kid must trigger exactly one refetch: %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("jwks fetches = %d, want 1", hits.Load())
	}
}

func TestVerifyFailsWhenJWKSIsUnavailable(t *testing.T) {
	s := newSigner(t, "kid-1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	verifier := NewJWKSVerifier(server.URL, time.Minute, server.Client())

	// Identity verification is fail-closed. Only tool authorisation is
	// fail-open (see Task 10) -- an unverifiable token has no identity at all.
	if _, err := verifier.Verify(context.Background(), s.sign(t, nil)); err == nil {
		t.Fatal("expected verification to fail when the key set cannot be fetched")
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/auth/ -run TestVerify -v`
Expected: 编译失败，`undefined: NewJWKSVerifier`

- [ ] **Step 4: 实现 verifier.go**

创建 `internal/auth/verifier.go`：

```go
package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// ExpectedIssuer and ExpectedAudience must match what admin-api puts in
	// the token (see its TokenIssuer). golang-jwt does not check either
	// unless asked, so both are passed to the parser explicitly.
	ExpectedIssuer   = "admin-api"
	ExpectedAudience = "copilot"

	// ClockSkewLeeway exists because the same token is verified independently
	// by admin-api, this service and ai-copilot-api, each with its own clock.
	// Zero leeway turns a few seconds of drift into sporadic, unreproducible
	// 401s. All three sides use the same value.
	ClockSkewLeeway = 60 * time.Second
)

// Claims is the closed claim set admin-api issues. Anything not listed here is
// not carried by the token -- notably there is no permission list and no
// perm_ver; permissions are fetched separately (see permissions.go).
type Claims struct {
	Subject    string
	LoginName  string
	RoleID     int64
	IsSysAdmin bool
	Actor      string
	JTI        string
	ExpiresAt  time.Time
}

type TokenVerifier interface {
	Verify(ctx context.Context, token string) (Claims, error)
}

type copilotClaims struct {
	jwt.RegisteredClaims
	LoginName  string `json:"login_name"`
	RoleID     int64  `json:"role_id"`
	IsSysAdmin bool   `json:"is_sys_admin"`
	Actor      string `json:"actor"`
}

// JWKSVerifier verifies RS256 tokens against admin-api's published key set.
// The set is cached for ttl; an unknown kid triggers at most one refetch so a
// key rotation does not need a restart, and a stream of bogus kids cannot turn
// into a stream of outbound requests.
type JWKSVerifier struct {
	url        string
	ttl        time.Duration
	httpClient *http.Client

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

func NewJWKSVerifier(url string, ttl time.Duration, client *http.Client) *JWKSVerifier {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &JWKSVerifier{url: url, ttl: ttl, httpClient: client}
}

func (v *JWKSVerifier) Verify(ctx context.Context, token string) (Claims, error) {
	var claims copilotClaims

	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithIssuer(ExpectedIssuer),
		jwt.WithAudience(ExpectedAudience),
		jwt.WithLeeway(ClockSkewLeeway),
	)

	if _, err := parser.ParseWithClaims(token, &claims, v.keyFunc(ctx)); err != nil {
		return Claims{}, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}

	expiresAt := time.Time{}
	if claims.ExpiresAt != nil {
		expiresAt = claims.ExpiresAt.Time
	}

	return Claims{
		Subject:    claims.Subject,
		LoginName:  claims.LoginName,
		RoleID:     claims.RoleID,
		IsSysAdmin: claims.IsSysAdmin,
		Actor:      claims.Actor,
		JTI:        claims.ID,
		ExpiresAt:  expiresAt,
	}, nil
}

func (v *JWKSVerifier) keyFunc(ctx context.Context) jwt.Keyfunc {
	return func(token *jwt.Token) (any, error) {
		kid, _ := token.Header["kid"].(string)

		if key, ok := v.cachedKey(kid); ok {
			return key, nil
		}
		if err := v.refresh(ctx); err != nil {
			return nil, err
		}
		if key, ok := v.cachedKey(kid); ok {
			return key, nil
		}
		return nil, fmt.Errorf("no key for kid %q", kid)
	}
}

func (v *JWKSVerifier) cachedKey(kid string) (*rsa.PublicKey, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.keys == nil || time.Since(v.fetchedAt) > v.ttl {
		return nil, false
	}
	key, ok := v.keys[kid]
	return key, ok
}

func (v *JWKSVerifier) refresh(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, v.url, nil)
	if err != nil {
		return err
	}
	response, err := v.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch jwks: status %d", response.StatusCode)
	}

	var document struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		return fmt.Errorf("decode jwks: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(document.Keys))
	for _, entry := range document.Keys {
		if entry.Kty != "RSA" {
			continue
		}
		modulus, err := base64.RawURLEncoding.DecodeString(entry.N)
		if err != nil {
			continue
		}
		exponent, err := base64.RawURLEncoding.DecodeString(entry.E)
		if err != nil {
			continue
		}
		keys[entry.Kid] = &rsa.PublicKey{
			N: new(big.Int).SetBytes(modulus),
			E: int(new(big.Int).SetBytes(exponent).Int64()),
		}
	}
	if len(keys) == 0 {
		return fmt.Errorf("jwks carried no usable RSA key")
	}

	v.mu.Lock()
	v.keys = keys
	v.fetchedAt = time.Now()
	v.mu.Unlock()

	return nil
}

// seedForTest primes the cache so a test can assert refetch behaviour without
// standing up two different key servers.
func (v *JWKSVerifier) seedForTest(keys map[string]*rsa.PublicKey, fetchedAt time.Time) {
	v.mu.Lock()
	v.keys = keys
	v.fetchedAt = fetchedAt
	v.mu.Unlock()
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/auth/ -run TestVerify -v`
Expected: PASS，8 个测试全绿

- [ ] **Step 6: vet + fmt**

Run: `make fmt && make vet`
Expected: 无输出

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/auth/verifier.go internal/auth/verifier_test.go
git commit -m "feat(auth): verify copilot tokens against admin-api JWKS"
```

---

### Task 8: 身份中间件改造 —— 删 scopes 头、验签、sub 一致性、双 service token

**Files:**
- Modify: `internal/auth/identity.go`
- Modify: `internal/auth/service.go`
- Modify: `internal/auth/service_test.go`（三个现有用例全部重写）
- Modify: `internal/config/config.go:59-62`（`AuthConfig`）、`internal/config/config.go:15`（删 `defaultPermission`）
- Modify: `internal/httpapi/server.go:45-63`（`secure()`）
- Modify: `cmd/server/main.go`（装配 verifier）
- Modify: `config/config.example.json`

**Interfaces:**
- Consumes: `TokenVerifier`、`Claims`（Task 7）
- Produces:
  - `type Identity struct { ServiceID, UserID, LoginName, SessionID, RequestID, RawToken string; RoleID int64; IsSysAdmin bool }`
    （**删掉 `Permissions []string` 和 `HasPermission()`** —— 权限不再随身份传递，见 Task 10）
  - `type MiddlewareConfig struct { ServiceTokens []string; Verifier TokenVerifier }`
  - `func Middleware(cfg MiddlewareConfig, next http.Handler) http.Handler`
  - 请求头约定：`Authorization: Bearer <service token>`、`X-Copilot-Access-Token: <用户 token>`、`X-Copilot-User-Id`、`X-Copilot-Session-Id`、`X-Copilot-Request-Id`。**`X-Copilot-Scopes` 不再被读取。**
  - `AuthConfig{ ServiceToken, ServiceTokenNext, JWKSURL string; JWKSCacheTTL Duration }`
  - 环境变量 `COPILOT_MCP_SERVICE_TOKEN` / `COPILOT_MCP_SERVICE_TOKEN_NEXT` 覆盖配置文件（spec §6.4：配置文件不留明文）

- [ ] **Step 1: 重写 service_test.go**

把 `internal/auth/service_test.go` 整个替换为（三个旧用例断言的都是基于 scopes 头的旧行为，无法保留）：

```go
package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testMiddleware(t *testing.T, s signer, tokens ...string) (http.Handler, *Identity) {
	t.Helper()
	server := jwksServer(t, s, nil)
	captured := &Identity{}

	handler := Middleware(MiddlewareConfig{
		ServiceTokens: tokens,
		Verifier:      NewJWKSVerifier(server.URL, time.Minute, server.Client()),
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if identity, ok := FromContext(r.Context()); ok {
			*captured = identity
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	return handler, captured
}

func request(serviceToken, userToken, userID string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+serviceToken)
	r.Header.Set(accessTokenHeader, userToken)
	r.Header.Set(userIDHeader, userID)
	r.Header.Set(sessionIDHeader, "session-1")
	r.Header.Set(requestIDHeader, "request-1")
	return r
}

func TestMiddlewareBuildsIdentityFromTheSignedToken(t *testing.T) {
	s := newSigner(t, "kid-1")
	handler, captured := testMiddleware(t, s, "service-token")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request("service-token", s.sign(t, nil), "10001"))

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if captured.UserID != "10001" || captured.LoginName != "zhangsan" {
		t.Errorf("identity = %+v", captured)
	}
	if captured.RoleID != 7 {
		t.Errorf("role_id = %d, want 7", captured.RoleID)
	}
	if captured.RawToken == "" {
		t.Error("RawToken must be carried so the adapter can delegate it downstream")
	}
	if captured.ServiceID != serviceID {
		t.Errorf("service_id = %q", captured.ServiceID)
	}
}

// The scopes header used to be the source of permissions, which meant the
// caller declared its own authority. It must now be ignored outright.
func TestMiddlewareIgnoresASelfDeclaredScopesHeader(t *testing.T) {
	s := newSigner(t, "kid-1")
	handler, captured := testMiddleware(t, s, "service-token")

	r := request("service-token", s.sign(t, nil), "10001")
	r.Header.Set("X-Copilot-Scopes", "api/managers/reset,api/roles/POST")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, r)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
	// Identity carries no permission field at all any more; the compiler
	// enforces most of this, the assertion pins the behaviour.
	if captured.RoleID != 7 {
		t.Errorf("role_id must still come from the token, got %d", captured.RoleID)
	}
}

func TestMiddlewareRejectsAnUnknownServiceToken(t *testing.T) {
	s := newSigner(t, "kid-1")
	handler, _ := testMiddleware(t, s, "service-token")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request("wrong-token", s.sign(t, nil), "10001"))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}

func TestMiddlewareAcceptsEitherServiceTokenDuringRotation(t *testing.T) {
	s := newSigner(t, "kid-1")
	handler, _ := testMiddleware(t, s, "service-token", "service-token-next")

	for _, token := range []string{"service-token", "service-token-next"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request(token, s.sign(t, nil), "10001"))

		if response.Code != http.StatusNoContent {
			t.Errorf("token %q: status = %d, want 204", token, response.Code)
		}
	}
}

func TestMiddlewareRejectsAMissingOrInvalidUserToken(t *testing.T) {
	s := newSigner(t, "kid-1")
	foreign := newSigner(t, "kid-1")
	handler, _ := testMiddleware(t, s, "service-token")

	for name, token := range map[string]string{
		"missing":    "",
		"garbage":    "not-a-jwt",
		"other-key":  foreign.sign(t, nil),
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request("service-token", token, "10001"))

			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", response.Code)
			}
		})
	}
}

// Review Focus 第 5 条：X-Copilot-User-Id 降级成日志字段之后，若不校验它与
// token sub 一致，审计会指向一个与实际鉴权主体不同的人。
func TestMiddlewareRejectsAUserIDHeaderThatDisagreesWithTheToken(t *testing.T) {
	s := newSigner(t, "kid-1")
	handler, _ := testMiddleware(t, s, "service-token")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request("service-token", s.sign(t, nil), "20002"))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when X-Copilot-User-Id != sub", response.Code)
	}
}

func TestMiddlewareStillValidatesCorrelationHeaderShape(t *testing.T) {
	s := newSigner(t, "kid-1")
	handler, _ := testMiddleware(t, s, "service-token")

	r := request("service-token", s.sign(t, nil), "10001")
	r.Header.Set(sessionIDHeader, "../secret")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, r)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/auth/`
Expected: 编译失败，`undefined: MiddlewareConfig`、`undefined: accessTokenHeader`

- [ ] **Step 3: 改 identity.go**

把 `internal/auth/identity.go` 的 `Identity` 与 `HasPermission` 替换为：

```go
// Identity is who a request acts for, as established by the signed copilot
// access token -- not by anything the caller asserts about itself.
//
// There is deliberately no permission field. Permissions used to arrive in a
// caller-supplied header, which meant the caller declared its own authority.
// They are now fetched from admin-api keyed on RoleID (see permissions.go),
// and the real boundary stays in admin-api's Permission middleware either way.
type Identity struct {
	ServiceID string

	UserID     string
	LoginName  string
	RoleID     int64
	IsSysAdmin bool

	// RawToken is the user's access token, passed straight through to
	// admin-api so downstream authorisation sees the real operator.
	RawToken string

	SessionID string
	RequestID string
}
```

删掉 `HasPermission()` 方法。`WithIdentity` / `FromContext` 不变。

- [ ] **Step 4: 改 service.go**

把 `internal/auth/service.go` 的常量块、`Authorizer`/`DefaultAuthorizer` 与 `Middleware` 替换为：

```go
const (
	serviceID = "ai-copilot-api"

	accessTokenHeader = "X-Copilot-Access-Token"
	userIDHeader      = "X-Copilot-User-Id"
	sessionIDHeader   = "X-Copilot-Session-Id"
	requestIDHeader   = "X-Copilot-Request-Id"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,128}$`)

// MiddlewareConfig groups what the middleware needs. ServiceTokens holds one
// or two values so a rotation can run without downtime: both are accepted
// while the new one propagates.
type MiddlewareConfig struct {
	ServiceTokens []string
	Verifier      TokenVerifier
}

func Middleware(cfg MiddlewareConfig, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !serviceTokenAccepted(cfg.ServiceTokens, bearerToken(r.Header.Get("Authorization"))) {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "missing or invalid service token")
			return
		}

		userToken := r.Header.Get(accessTokenHeader)
		if userToken == "" {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "missing delegated access token")
			return
		}

		claims, err := cfg.Verifier.Verify(r.Context(), userToken)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "invalid delegated access token")
			return
		}

		sessionID := r.Header.Get(sessionIDHeader)
		requestID := r.Header.Get(requestIDHeader)
		if !identifierPattern.MatchString(sessionID) || !identifierPattern.MatchString(requestID) {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "invalid correlation headers")
			return
		}

		// The user id header is now only a correlation field, but a value that
		// disagrees with the token would make every log line and audit entry
		// point at the wrong operator. Refuse rather than silently prefer one.
		if userID := r.Header.Get(userIDHeader); userID != claims.Subject {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "delegated identity header does not match the token")
			return
		}

		ctx := WithIdentity(r.Context(), Identity{
			ServiceID:  serviceID,
			UserID:     claims.Subject,
			LoginName:  claims.LoginName,
			RoleID:     claims.RoleID,
			IsSysAdmin: claims.IsSysAdmin,
			RawToken:   userToken,
			SessionID:  sessionID,
			RequestID:  requestID,
		})

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// serviceTokenAccepted compares against every configured token in constant
// time. Empty configured values never match, so a service with no token
// configured rejects everything rather than accepting everything.
func serviceTokenAccepted(configured []string, provided string) bool {
	if provided == "" {
		return false
	}
	accepted := false
	for _, candidate := range configured {
		if candidate == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(provided)) == 1 {
			accepted = true
		}
	}
	return accepted
}
```

`DefaultAuthorizer` 与旧的 `Authorizer` 接口一并删除 —— 它们被 Task 10 的 `EndpointAuthorizer` 取代。`bearerToken()` 保持不变。

**注意：** 现有 `service.go` 里 `bearerToken()` 有一处 `len(schema())` 的笔误（应为 `len(scheme)`）。实现时顺手修掉，并补一个用例：

```go
func TestBearerTokenParsing(t *testing.T) {
	for input, want := range map[string]string{
		"Bearer abc": "abc",
		"bearer abc": "abc",
		"abc":        "",
		"Bearer ":    "",
		"":           "",
	} {
		if got := bearerToken(input); got != want {
			t.Errorf("bearerToken(%q) = %q, want %q", input, got, want)
		}
	}
}
```

- [ ] **Step 5: 改 config.go**

删掉 `defaultPermission` 常量和 `AuthConfig.DefaultPermissions`，`AuthConfig` 改为：

```go
type AuthConfig struct {
	// ServiceToken and ServiceTokenNext are both accepted. Keeping two lets a
	// rotation roll through callers without a window where either is refused.
	ServiceToken     string `json:"service_token"`
	ServiceTokenNext string `json:"service_token_next"`

	// JWKSURL points at admin-api's public key set.
	JWKSURL      string   `json:"jwks_url"`
	JWKSCacheTTL Duration `json:"jwks_cache_ttl"`
}
```

`Load()` 的默认值里删掉 `DefaultPermissions`，加上 `JWKSCacheTTL: Duration(10 * time.Minute)`。

在 `Load()` 的校验段加：JWKS URL 必填（空则返回错误），理由与 `AdminConsoleConfig.BaseURL` 相同 —— 一个貌似合理但错误的默认值会让验签静默失败。

- [ ] **Step 6: 改 httpapi/server.go 的 secure()**

`Middleware` 的签名变了，`secure()` 里改为：

```go
func (s *Server) secure(next http.Handler) http.Handler {
	middlewareConfig := auth.MiddlewareConfig{
		ServiceTokens: []string{s.cfg.Auth.ServiceToken, s.cfg.Auth.ServiceTokenNext},
		Verifier:      s.verifier,
	}

	return auth.Middleware(middlewareConfig, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if identity, ok := auth.FromContext(r.Context()); ok {
			if !s.limiter.Allow("service:" + identity.ServiceID + ":user:" + identity.UserID) {
				writeJSON(w, http.StatusTooManyRequests, map[string]any{
					"error": map[string]string{"code": "RATE_LIMITED", "message": "too many requests"},
				})
				return
			}
		}
		next.ServeHTTP(w, r)
	}))
}
```

`Server` 结构体加一个 `verifier auth.TokenVerifier` 字段，`New()` 多收一个参数。

注意原实现把 `auth.Middleware(...)` 建在每个请求里（`secure` 返回的 handler 内部再调 `Middleware`），这会让每个请求重建一次中间件链。改成在 `secure()` 里建一次、返回它 —— 顺手修掉。

- [ ] **Step 7: 改 cmd/server/main.go 装配**

在构造 `httpapi.New(...)` 之前创建 verifier：

```go
	verifier := auth.NewJWKSVerifier(
		cfg.Auth.JWKSURL,
		cfg.Auth.JWKSCacheTTL.Duration(),
		&http.Client{Timeout: 5 * time.Second},
	)
```

并把它传给 `httpapi.New`。

- [ ] **Step 8: 更新 config.example.json**

```json
  "auth": {
    "service_token": "replace-with-a-random-local-token",
    "service_token_next": "",
    "jwks_url": "http://127.0.0.1:8000/api/auth/copilot/jwks",
    "jwks_cache_ttl": "10m"
  },
```

`config.local.json` 同步修改（它的 `admin_api.base_url` 指向 `https://blockmatrix.ginfx-inc.net/api`，`jwks_url` 要跟着指向同一主机的 `/api/auth/copilot/jwks`）。

- [ ] **Step 9: 让 service token 可由环境变量覆盖**

spec §6.4 要求 service token 不在配置文件里留明文。在 `Load()` 的末尾、返回之前加：

```go
	// 环境变量优先于配置文件：生产由密钥管理注入，配置文件里留空。
	// 轮换期间两个都设，两个都被接受（见 auth.serviceTokenAccepted）。
	if value := os.Getenv("COPILOT_MCP_SERVICE_TOKEN"); value != "" {
		cfg.Auth.ServiceToken = value
	}
	if value := os.Getenv("COPILOT_MCP_SERVICE_TOKEN_NEXT"); value != "" {
		cfg.Auth.ServiceTokenNext = value
	}

	if cfg.Auth.ServiceToken == "" && cfg.Auth.ServiceTokenNext == "" {
		return Config{}, errors.New("auth.service_token is required (config file or COPILOT_MCP_SERVICE_TOKEN)")
	}
```

两个都为空时拒绝启动 —— 否则 `serviceTokenAccepted` 会对所有请求返回 false，表现成「MCP 全部 401」，排查时很难想到是配置缺失。

追加测试到 `internal/config/config_test.go`：

```go
func TestServiceTokenCanComeFromTheEnvironment(t *testing.T) {
	body := strings.Replace(minimalConfig, `"service_token": "t", `, "", 1)
	path := writeConfig(t, body)
	// writeConfig cleared the overrides; set ours after it.
	t.Setenv("COPILOT_MCP_SERVICE_TOKEN", "from-env")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Auth.ServiceToken != "from-env" {
		t.Errorf("service_token = %q, want from-env", cfg.Auth.ServiceToken)
	}
}

func TestTheEnvironmentWinsOverTheConfigFile(t *testing.T) {
	path := writeConfig(t, minimalConfig)
	t.Setenv("COPILOT_MCP_SERVICE_TOKEN", "from-env")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Auth.ServiceToken != "from-env" {
		t.Errorf("service_token = %q, want the env value to win", cfg.Auth.ServiceToken)
	}
}

func TestLoadRefusesWhenNoServiceTokenIsConfiguredAnywhere(t *testing.T) {
	body := strings.Replace(minimalConfig, `"service_token": "t", `, "", 1)

	if _, err := Load(writeConfig(t, body)); err == nil {
		t.Fatal("expected startup to fail with no service token at all")
	}
}
```

- [ ] **Step 10: 跑测试确认通过**

Run: `go test ./...`
Expected: `internal/auth` 与 `internal/config` 全绿。`internal/mcpserver` 可能因 Task 10 未完成而失败 —— 若是，记下失败项，在 Task 10 结束时必须全绿。

- [ ] **Step 11: vet + fmt + 构建**

Run: `make fmt && make vet && make build`
Expected: 全部成功

- [ ] **Step 12: Commit**

```bash
git add internal/auth/ internal/config/ internal/httpapi/server.go \
        cmd/server/main.go config/config.example.json config/config.local.json
git commit -m "feat(auth): derive identity from the signed token, drop caller-declared scopes"
```

---

### Task 9: 委派下游凭证，并封死静态 cookie 后门

**Files:**
- Modify: `internal/adapters/adminapi/credentials.go`
- Modify: `internal/adapters/adminapi/client.go:186`（`Health()` 的端点）
- Modify: `internal/config/config.go`（加 `Env`，加启动校验）
- Modify: `cmd/server/main.go:36-38`
- Test: `internal/adapters/adminapi/credentials_test.go`（新建）
- Test: `internal/config/config_test.go`（新建或追加）

**Interfaces:**
- Consumes: `auth.Identity.RawToken`、`auth.FromContext`（Task 8）
- Produces:
  - `type Credentials struct { Cookies map[string]string; BearerToken string }`
  - `func NewDelegatedCredentialProvider() CredentialProvider` —— 从 context 取 `Identity.RawToken`
  - `Config.Env string`（`"development"` / `"production"`，默认 `"production"`）
  - `Config.IsDevelopment() bool`

- [ ] **Step 1: 写失败测试**

创建 `internal/adapters/adminapi/credentials_test.go`：

```go
package adminapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Global-MongoX/internal-mcp-server/internal/auth"
)

type capturingTransport struct {
	request *http.Request
}

func (t *capturingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.request = r
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       http.NoBody,
		Header:     http.Header{},
	}, nil
}

func TestDelegatedProviderSendsTheUsersBearerToken(t *testing.T) {
	captured := &capturingTransport{}
	transport := NewCredentialTransport(NewDelegatedCredentialProvider(), captured)

	ctx := auth.WithIdentity(context.Background(), auth.Identity{
		UserID:   "10001",
		RawToken: "user-access-token",
	})
	request := httptest.NewRequest(http.MethodGet, "http://admin.test/api/deposits", nil).WithContext(ctx)

	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatalf("round trip: %v", err)
	}

	if got := captured.request.Header.Get("Authorization"); got != "Bearer user-access-token" {
		t.Errorf("Authorization = %q", got)
	}
	// A bearer and a shared session cookie must never travel together: the
	// cookie would let admin-api authenticate the shared account instead.
	if got := captured.request.Header.Get("Cookie"); got != "" {
		t.Errorf("Cookie = %q, want empty when a bearer is present", got)
	}
}

func TestDelegatedProviderFailsWhenThereIsNoIdentity(t *testing.T) {
	transport := NewCredentialTransport(NewDelegatedCredentialProvider(), &capturingTransport{})
	request := httptest.NewRequest(http.MethodGet, "http://admin.test/api/deposits", nil)

	if _, err := transport.RoundTrip(request); err == nil {
		t.Fatal("a downstream call with no delegated identity must fail, not fall back to anything")
	}
}

func TestStaticProviderStillSendsCookiesForLocalDevelopment(t *testing.T) {
	captured := &capturingTransport{}
	provider := NewStaticCredentialProvider(Credentials{
		Cookies: map[string]string{"session_id": "s", "XSRF-TOKEN": "x"},
	})
	transport := NewCredentialTransport(provider, captured)

	if _, err := transport.RoundTrip(httptest.NewRequest(http.MethodGet, "http://admin.test/api/deposits", nil)); err != nil {
		t.Fatalf("round trip: %v", err)
	}

	if got := captured.request.Header.Get("Cookie"); got != "XSRF-TOKEN=x; session_id=s" {
		t.Errorf("Cookie = %q", got)
	}
}

func TestBearerWinsOverCookiesWhenBothArePresent(t *testing.T) {
	captured := &capturingTransport{}
	provider := NewStaticCredentialProvider(Credentials{
		Cookies:     map[string]string{"session_id": "s"},
		BearerToken: "token",
	})
	transport := NewCredentialTransport(provider, captured)

	if _, err := transport.RoundTrip(httptest.NewRequest(http.MethodGet, "http://admin.test/api/deposits", nil)); err != nil {
		t.Fatalf("round trip: %v", err)
	}

	if captured.request.Header.Get("Authorization") != "Bearer token" {
		t.Error("bearer must be used")
	}
	if captured.request.Header.Get("Cookie") != "" {
		t.Error("cookies must be dropped when a bearer is present")
	}
}
```

创建 `internal/config/config_test.go`（若已存在则追加）：

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateEnv clears the service-token overrides so a value in the developer's
// shell cannot make these tests pass or fail by accident.
func isolateEnv(t *testing.T) {
	t.Helper()
	t.Setenv("COPILOT_MCP_SERVICE_TOKEN", "")
	t.Setenv("COPILOT_MCP_SERVICE_TOKEN_NEXT", "")
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	isolateEnv(t)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

const minimalConfig = `{
  "auth": {"service_token": "t", "jwks_url": "http://admin.test/api/auth/copilot/jwks"},
  "admin_api": {"base_url": "http://admin.test/api"},
  "admin_console": {"base_url": "http://console.test"}
}`

// A leftover static cookie in production is a silent back door: the bearer
// takes precedence, so the cookie is never used and nothing ever looks wrong,
// yet the shared super-account credential sits in the config file.
func TestLoadRefusesStaticCookiesOutsideDevelopment(t *testing.T) {
	body := strings.Replace(
		minimalConfig,
		`"admin_api": {"base_url": "http://admin.test/api"}`,
		`"admin_api": {"base_url": "http://admin.test/api", "credentials": {"cookies": {"session_id": "leftover"}}}`,
		1,
	)

	if _, err := Load(writeConfig(t, body)); err == nil {
		t.Fatal("expected startup to fail when production config carries static cookies")
	}
}

func TestLoadAllowsStaticCookiesInDevelopment(t *testing.T) {
	body := strings.Replace(
		minimalConfig,
		`"admin_api": {"base_url": "http://admin.test/api"}`,
		`"env": "development", "admin_api": {"base_url": "http://admin.test/api", "credentials": {"cookies": {"session_id": "local"}}}`,
		1,
	)

	if _, err := Load(writeConfig(t, body)); err != nil {
		t.Fatalf("development must still allow a static cookie: %v", err)
	}
}

func TestLoadRequiresAJWKSURL(t *testing.T) {
	body := strings.Replace(minimalConfig, `, "jwks_url": "http://admin.test/api/auth/copilot/jwks"`, "", 1)

	if _, err := Load(writeConfig(t, body)); err == nil {
		t.Fatal("expected startup to fail without a jwks_url")
	}
}

func TestEnvDefaultsToProduction(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// The safe default: forgetting to set env makes the strict checks apply.
	if cfg.IsDevelopment() {
		t.Error("env must default to production")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/adapters/adminapi/ ./internal/config/`
Expected: 编译失败，`undefined: NewDelegatedCredentialProvider`、`cfg.IsDevelopment undefined`

- [ ] **Step 3: 改 credentials.go**

`Credentials` 加字段，`CookieHeader()` 不变，并追加：

```go
// Credentials carries whatever the downstream call should authenticate with.
//
// BearerToken is the real path: the end user's copilot access token, passed
// through so admin-api authorises the actual operator. Cookies remain only for
// local development against a browser session, and config.Load refuses them
// outside development -- a leftover cookie in production would be a shared
// super-account credential that nothing ever exercises and nobody notices.
type Credentials struct {
	Cookies     map[string]string
	BearerToken string
}
```

`RoundTrip` 改为：

```go
func (t *credentialTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	credentials, err := t.provider.Credentials(request.Context())
	if err != nil {
		return nil, err
	}

	out := request.Clone(request.Context())

	// A bearer and a cookie must never travel together: admin-api would
	// authenticate the cookie's session instead of the delegated user.
	if credentials.BearerToken != "" {
		out.Header.Set("Authorization", "Bearer "+credentials.BearerToken)
		out.Header.Del("Cookie")
	} else if header := credentials.CookieHeader(); header != "" {
		out.Header.Set("Cookie", header)
	}

	return t.base.RoundTrip(out)
}
```

并新增：

```go
// DelegatedCredentialProvider takes the user's access token from the request
// context, where the auth middleware put it. It deliberately has no fallback:
// a downstream call with no delegated identity is a bug, and failing loudly is
// better than quietly reaching admin-api as somebody else.
type DelegatedCredentialProvider struct{}

func NewDelegatedCredentialProvider() CredentialProvider {
	return DelegatedCredentialProvider{}
}

func (DelegatedCredentialProvider) Credentials(ctx context.Context) (Credentials, error) {
	identity, ok := auth.FromContext(ctx)
	if !ok || identity.RawToken == "" {
		return Credentials{}, errors.New("no delegated access token in context")
	}
	return Credentials{BearerToken: identity.RawToken}, nil
}
```

补 import `errors` 与 `github.com/Global-MongoX/internal-mcp-server/internal/auth`。

**依赖方向注意：** `adapters/adminapi` 依赖 `auth`，反向不可以 —— Task 10 的权限拉取因此用一个注入的函数而不是直接 import adapter。

- [ ] **Step 4: 改 config.go**

`Config` 加字段与方法：

```go
type Config struct {
	// Env gates the development-only escape hatches. It defaults to
	// production so that forgetting to set it makes the strict checks apply.
	Env string `json:"env"`

	Server       ServerConfig       `json:"server"`
	// ...其余不变
}

func (c Config) IsDevelopment() bool {
	return strings.EqualFold(c.Env, "development")
}
```

`Load()` 的默认值加 `Env: "production"`，并在校验段加：

```go
	if cfg.Auth.JWKSURL == "" {
		return Config{}, errors.New("auth.jwks_url is required")
	}
	if !cfg.IsDevelopment() && len(nonEmptyCookies(cfg.AdminAPI.Credentials.Cookies)) > 0 {
		return Config{}, errors.New(
			"admin_api.credentials.cookies is only allowed when env=development: " +
				"a shared session cookie in production is a back door that the bearer " +
				"path never exercises and therefore never surfaces",
		)
	}
```

```go
func nonEmptyCookies(cookies map[string]string) []string {
	names := make([]string, 0, len(cookies))
	for name, value := range cookies {
		if name != "" && value != "" {
			names = append(names, name)
		}
	}
	return names
}
```

空值不算 —— `config.example.json` 里 `"session_id": ""` 这种占位不应该让生产启动失败。

- [ ] **Step 5: 修 Health() 的双前缀 bug**

`internal/adapters/adminapi/client.go` 第 186 行：

```go
	// base_url already carries the /api prefix (see config.local.json), so the
	// endpoint here is relative to it. The old value was "/api/managers/..."
	// which produced /api/api/... -- a 404 that this probe treated as success,
	// because any response "proves the service is reachable".
	request, err := c.newRequest(ctx, http.MethodGet, "/managers/permissions", nil)
```

补一个用例固定它，追加到 `internal/adapters/adminapi/client_test.go`：

```go
func TestHealthProbesAPathRelativeToTheBaseURL(t *testing.T) {
	var probed string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probed = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"is_succ":true}`))
	}))
	defer server.Close()

	client, err := NewClient(
		server.URL+"/api",
		5*time.Second,
		NewStaticCredentialProvider(Credentials{BearerToken: "t"}),
		server.Client().Transport,
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	if _, err := client.Health(context.Background()); err != nil {
		t.Fatalf("health: %v", err)
	}

	if probed != "/api/managers/permissions" {
		t.Errorf("probed %q, want /api/managers/permissions (no doubled prefix)", probed)
	}
}
```

- [ ] **Step 6: 改 main.go 的装配**

把第 36–38 行的静态 provider 替换为：

```go
	// Production delegates the end user's token (config.Load refuses static
	// cookies unless env=development).
	var credentials adminapi.CredentialProvider = adminapi.NewDelegatedCredentialProvider()
	if cfg.IsDevelopment() && len(cfg.AdminAPI.Credentials.Cookies) > 0 {
		credentials = adminapi.NewStaticCredentialProvider(adminapi.Credentials{
			Cookies: cfg.AdminAPI.Credentials.Cookies,
		})
	}
```

- [ ] **Step 7: 跑测试确认通过**

Run: `go test ./internal/adapters/adminapi/ ./internal/config/ -v`
Expected: PASS

- [ ] **Step 8: vet + fmt + 构建**

Run: `make fmt && make vet && make build`
Expected: 全部成功

- [ ] **Step 9: Commit**

```bash
git add internal/adapters/adminapi/ internal/config/ cmd/server/main.go
git commit -m "feat(adminapi): delegate the user's token downstream, refuse static cookies in production"
```

---

### Task 10: Tool Authorization —— 权限词表、60 秒缓存与 fail-open

**Files:**
- Create: `internal/auth/permissions.go`
- Create: `internal/auth/permissions_test.go`
- Modify: `internal/mcpserver/tools.go:19-20, 178, 210, 229, 257, 283, 291-295`
- Modify: `internal/mcpserver/server.go`（authorizer 装配）
- Modify: `internal/mcpserver/integration_test.go`
- Modify: `cmd/server/main.go`（注入权限拉取函数）
- Modify: `internal/adapters/adminapi/client.go`（新增 `CopilotPermissions()`）

**Interfaces:**
- Consumes: `Identity`（Task 8）、Phase 1 的 `GET /api/auth/copilot/permissions`
- Produces:
  - `func PermissionCode(adapterEndpoint string) string` —— `"/deposits"` → `"api/deposits"`
  - `type PermissionFetcher func(ctx context.Context, userToken string) ([]string, error)`
  - `func NewPermissionCache(fetch PermissionFetcher, ttl time.Duration) *PermissionCache`
  - `type EndpointAuthorizer struct` + `Authorize(ctx context.Context, identity Identity, adapterEndpoints []string) error`
  - `(*Client).CopilotPermissions(ctx context.Context) ([]string, error)`
  - tool 定义改为声明 `requiredEndpoints`（adapter 端点，带前导 `/`）

**词表换算：** `Permission` 中间件比对的是 `modMethod($request->path())`，对 `https://host/api/deposits` 得到 `api/deposits`；`BlockmatrixInitSeeder` 里的实际值也是 `api/menus`、`api/menus/POST`。而 adapter 的端点是相对 `base_url`（已含 `/api`）的 `/deposits`。两者差一个前缀，必须显式换算 —— 这是 `PermissionCode()` 存在的全部理由。

- [ ] **Step 1: 写失败测试**

创建 `internal/auth/permissions_test.go`：

```go
package auth

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestPermissionCodeAddsTheAPIPrefix(t *testing.T) {
	for endpoint, want := range map[string]string{
		"/deposits":            "api/deposits",
		"/deposits/statistics": "api/deposits/statistics",
		"/commission/statistics": "api/commission/statistics",
		"/data_overview":       "api/data_overview",
	} {
		if got := PermissionCode(endpoint); got != want {
			t.Errorf("PermissionCode(%q) = %q, want %q", endpoint, got, want)
		}
	}
}

func identity(roleID int64) Identity {
	return Identity{ServiceID: serviceID, UserID: "10001", RoleID: roleID, RawToken: "tok"}
}

func TestAuthorizeAllowsAToolWhoseEndpointsAreGranted(t *testing.T) {
	cache := NewPermissionCache(func(context.Context, string) ([]string, error) {
		return []string{"api/deposits", "api/deposits/statistics"}, nil
	}, time.Minute)
	authorizer := EndpointAuthorizer{Permissions: cache}

	if err := authorizer.Authorize(context.Background(), identity(7), []string{"/deposits"}); err != nil {
		t.Fatalf("authorize: %v", err)
	}
}

func TestAuthorizeRefusesAnEndpointTheRoleLacks(t *testing.T) {
	cache := NewPermissionCache(func(context.Context, string) ([]string, error) {
		return []string{"api/deposits"}, nil
	}, time.Minute)
	authorizer := EndpointAuthorizer{Permissions: cache}

	err := authorizer.Authorize(context.Background(), identity(7), []string{"/commission/statistics"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

func TestAuthorizeRequiresEveryDeclaredEndpoint(t *testing.T) {
	cache := NewPermissionCache(func(context.Context, string) ([]string, error) {
		return []string{"api/deposits"}, nil
	}, time.Minute)
	authorizer := EndpointAuthorizer{Permissions: cache}

	// A tool that reads both must hold both.
	err := authorizer.Authorize(context.Background(), identity(7),
		[]string{"/deposits", "/commission/statistics"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

func TestAuthorizeRefusesAnUnknownCallingService(t *testing.T) {
	cache := NewPermissionCache(func(context.Context, string) ([]string, error) {
		return []string{"api/deposits"}, nil
	}, time.Minute)
	authorizer := EndpointAuthorizer{Permissions: cache}

	stranger := identity(7)
	stranger.ServiceID = "someone-else"

	if err := authorizer.Authorize(context.Background(), stranger, []string{"/deposits"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

// spec D10: tool authorisation is a UX layer, not the security boundary --
// admin-api's Permission middleware is. Failing open cannot cause an
// escalation, while failing closed would turn any admin-api wobble into a
// total copilot outage.
func TestAuthorizeFailsOpenWhenPermissionsCannotBeFetched(t *testing.T) {
	cache := NewPermissionCache(func(context.Context, string) ([]string, error) {
		return nil, errors.New("admin-api unreachable")
	}, time.Minute)
	authorizer := EndpointAuthorizer{Permissions: cache}

	if err := authorizer.Authorize(context.Background(), identity(7), []string{"/deposits"}); err != nil {
		t.Fatalf("expected fail-open, got %v", err)
	}
}

func TestPermissionsAreCachedPerRole(t *testing.T) {
	var calls atomic.Int64
	cache := NewPermissionCache(func(context.Context, string) ([]string, error) {
		calls.Add(1)
		return []string{"api/deposits"}, nil
	}, time.Minute)
	authorizer := EndpointAuthorizer{Permissions: cache}

	for range 3 {
		if err := authorizer.Authorize(context.Background(), identity(7), []string{"/deposits"}); err != nil {
			t.Fatalf("authorize: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("fetches = %d, want 1", calls.Load())
	}

	// A different role must not reuse the first role's entry.
	if err := authorizer.Authorize(context.Background(), identity(9), []string{"/deposits"}); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("fetches = %d, want 2 (per-role cache)", calls.Load())
	}
}

func TestCachedPermissionsExpire(t *testing.T) {
	var calls atomic.Int64
	cache := NewPermissionCache(func(context.Context, string) ([]string, error) {
		calls.Add(1)
		return []string{"api/deposits"}, nil
	}, time.Millisecond)
	authorizer := EndpointAuthorizer{Permissions: cache}

	for range 2 {
		if err := authorizer.Authorize(context.Background(), identity(7), []string{"/deposits"}); err != nil {
			t.Fatalf("authorize: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if calls.Load() != 2 {
		t.Errorf("fetches = %d, want 2 after the entry expired", calls.Load())
	}
}

func TestAFetchFailureIsNotCached(t *testing.T) {
	var calls atomic.Int64
	cache := NewPermissionCache(func(context.Context, string) ([]string, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("transient")
		}
		return []string{"api/deposits"}, nil
	}, time.Minute)
	authorizer := EndpointAuthorizer{Permissions: cache}

	// First call fails open.
	_ = authorizer.Authorize(context.Background(), identity(7), []string{"/deposits"})
	// Second must retry rather than serve a cached failure, otherwise one
	// blip would leave authorisation open for the whole TTL.
	_ = authorizer.Authorize(context.Background(), identity(7), []string{"/deposits"})

	if calls.Load() != 2 {
		t.Errorf("fetches = %d, want 2 (failures must not be cached)", calls.Load())
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/auth/ -run 'TestPermission|TestAuthorize|TestCached|TestAFetch'`
Expected: 编译失败，`undefined: PermissionCode`

- [ ] **Step 3: 实现 permissions.go**

创建 `internal/auth/permissions.go`：

```go
package auth

import (
	"context"
	"strings"
	"sync"
	"time"
)

// PermissionCode turns an adapter endpoint into the code admin-api's
// Permission middleware compares against.
//
// The two strings differ by one prefix and it is easy to get wrong: the
// adapter endpoints are relative to admin_api.base_url, which already carries
// /api (see config.local.json), so deposits.go writes "/deposits". The
// permission codes stored in menu_endpoints.api_path carry it explicitly --
// "api/deposits", "api/menus/POST". One function, not a lookup table, so the
// two cannot drift apart as endpoints are added.
//
// Write methods additionally take a method suffix ("api/menus/POST"). The MVP
// is GET-only so that case does not arise; it must be handled when the route
// level whitelist lands.
func PermissionCode(adapterEndpoint string) string {
	return "api" + adapterEndpoint
}

// PermissionFetcher retrieves the caller's granted api_paths from admin-api.
// It is injected rather than imported so that this package does not depend on
// the adminapi adapter, which already depends on this one.
type PermissionFetcher func(ctx context.Context, userToken string) ([]string, error)

type cacheEntry struct {
	paths    map[string]struct{}
	cachedAt time.Time
}

// PermissionCache caches granted api_paths per role for ttl.
//
// Keyed on role rather than on user because that is what the grant actually
// depends on (role_id -> role_menus_rel -> menu_endpoints). The token carries
// no perm_ver: it is signed at login, so a perm_ver inside it would be frozen
// at that moment and a mid-session role change would never invalidate
// anything. A short TTL bounds staleness instead, and staleness here is not a
// security question -- see EndpointAuthorizer.
type PermissionCache struct {
	fetch PermissionFetcher
	ttl   time.Duration

	mu      sync.Mutex
	entries map[int64]cacheEntry
}

func NewPermissionCache(fetch PermissionFetcher, ttl time.Duration) *PermissionCache {
	return &PermissionCache{fetch: fetch, ttl: ttl, entries: map[int64]cacheEntry{}}
}

// APIPaths reports the granted paths. The boolean is false when they could not
// be determined -- callers fail open on that, so a failure must never be
// cached: one blip would otherwise leave authorisation open for a whole TTL.
func (c *PermissionCache) APIPaths(ctx context.Context, roleID int64, userToken string) (map[string]struct{}, bool) {
	c.mu.Lock()
	entry, ok := c.entries[roleID]
	c.mu.Unlock()

	if ok && time.Since(entry.cachedAt) <= c.ttl {
		return entry.paths, true
	}

	paths, err := c.fetch(ctx, userToken)
	if err != nil {
		return nil, false
	}

	set := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		set[strings.TrimSpace(path)] = struct{}{}
	}

	c.mu.Lock()
	c.entries[roleID] = cacheEntry{paths: set, cachedAt: time.Now()}
	c.mu.Unlock()

	return set, true
}

type permissionSource interface {
	APIPaths(ctx context.Context, roleID int64, userToken string) (map[string]struct{}, bool)
}

// EndpointAuthorizer checks that the caller's role grants every admin-api
// endpoint a tool is going to call.
//
// This is a UX layer, not the security boundary. The boundary is admin-api's
// Permission middleware, which runs on every delegated call regardless. What
// this buys: tools the user cannot use are not advertised to the model, a
// refusal costs no round trip, and the model is told "you lack this
// permission" instead of being handed a bare 403 to improvise around.
//
// Because of that, an unavailable permission list fails open (spec D10):
// letting the call through cannot cause an escalation, while refusing would
// turn any admin-api wobble into a total copilot outage.
type EndpointAuthorizer struct {
	Permissions permissionSource
}

func (a EndpointAuthorizer) Authorize(ctx context.Context, identity Identity, adapterEndpoints []string) error {
	if identity.ServiceID != serviceID {
		return ErrForbidden
	}
	if len(adapterEndpoints) == 0 {
		return nil
	}

	granted, ok := a.Permissions.APIPaths(ctx, identity.RoleID, identity.RawToken)
	if !ok {
		return nil // fail open; admin-api remains the boundary
	}

	for _, endpoint := range adapterEndpoints {
		if _, allowed := granted[PermissionCode(endpoint)]; !allowed {
			return ErrForbidden
		}
	}

	return nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/auth/ -v`
Expected: PASS，Task 7、8、10 的用例全绿

- [ ] **Step 5: 在 adminapi client 上加权限拉取**

在 `internal/adapters/adminapi/client.go` 追加：

```go
// CopilotPermissions returns the api_paths the delegated user is granted.
// The endpoint is relative to base_url, which already carries /api.
func (c *Client) CopilotPermissions(ctx context.Context) ([]string, error) {
	result, err := get[struct {
		APIPaths []string `json:"api_paths"`
	}](ctx, c, "/auth/copilot/permissions", nil)
	if err != nil {
		return nil, err
	}
	return result.APIPaths, nil
}
```

追加测试到 `internal/adapters/adminapi/client_test.go`：

```go
func TestCopilotPermissionsReadsTheAPIPathList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/copilot/permissions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"is_succ":true,"data":{"api_paths":["api/deposits","api/deposits/statistics"]}}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL+"/api", 5*time.Second,
		NewStaticCredentialProvider(Credentials{BearerToken: "t"}), server.Client().Transport)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	paths, err := client.CopilotPermissions(context.Background())
	if err != nil {
		t.Fatalf("permissions: %v", err)
	}
	if len(paths) != 2 || paths[0] != "api/deposits" {
		t.Errorf("paths = %v", paths)
	}
}
```

- [ ] **Step 6: 改 tools.go 的词表**

删掉第 19–20 行的 `depositsPermission` / `commissionsPermission`。把注册函数的第三个参数从「权限码」改为「adapter 端点」，并把每个 tool 的声明改成它**实际调用的端点**：

| tool | requiredEndpoints |
|---|---|
| 原用 `[]string{depositsPermission, commissionsPermission}`（第 178 行） | `[]string{"/deposits", "/commission/statistics"}` |
| 第 210 行 | `[]string{"/deposits"}` |
| 第 229 行 | `[]string{"/commission/statistics"}` |
| 第 257 行 | `[]string{"/deposits/statistics"}` |
| 第 283 行 | `[]string{"/commission/statistics"}` |

**实现时必须逐个核对每个 tool 的 handler 实际调了哪些 adapter 方法**（`deposits.go`、`commissions.go`、`data_overview.go` 里的端点字符串），按实际调用填写，不要照抄上表 —— 上表是按现有权限码的分布推断的，可能与真实调用有出入。调用 `/ib/{user_code}/commissions` 的 tool 对应 `"/ib/commissions"`（`modMethod()` 会剥掉数字段，所以权限码里没有 `user_code`）。

把第 291–295 行的注册函数里 `definition.RequiredPermissions = requiredPermissions` 改为携带 endpoints，并在 tool 调用路径上改调 `EndpointAuthorizer.Authorize(ctx, identity, endpoints)`。

- [ ] **Step 7: 改 server.go 与 main.go 装配**

`mcpserver.New(...)` 收一个 `auth.EndpointAuthorizer`，替换原先的 `auth.DefaultAuthorizer{}`。

`cmd/server/main.go` 里注入拉取函数：

```go
	permissions := auth.NewPermissionCache(
		func(ctx context.Context, _ string) ([]string, error) {
			// The client's transport already attaches the delegated token from
			// the context, so the token argument is not needed here.
			return adminClient.CopilotPermissions(ctx)
		},
		60*time.Second,
	)
	authorizer := auth.EndpointAuthorizer{Permissions: permissions}
```

60 秒是 spec §4.3 定的值。

- [ ] **Step 8: 跑全量测试**

Run: `go test ./...`
Expected: PASS，**包含 Task 8 Step 9 记下的所有失败项**

- [ ] **Step 9: vet + fmt + 构建**

Run: `make fmt && make vet && make build`
Expected: 全部成功

- [ ] **Step 10: Commit**

```bash
git add internal/auth/permissions.go internal/auth/permissions_test.go \
        internal/adapters/adminapi/ internal/mcpserver/ cmd/server/main.go
git commit -m "feat(mcp): authorize tools against admin-api's own api_path vocabulary"
```

**Phase 2 完成标志：** `go test ./...` 全绿，`make build` 成功，且 `grep -rn "admin.api.deposits.read\|X-Copilot-Scopes\|DefaultPermissions" internal/ cmd/ config/` 无结果（旧词表与自称 scopes 彻底移除）。

---

# Phase 3 — ai-copilot-api

所有 Phase 3 的命令都在 `/Users/antony/Workspace/mangox/ai-copilot/ai-copilot-api` 下执行。测试风格照抄 `tests/test_auth.py`（纯函数用普通 `def test_*`）与 `tests/conftest.py`（端点用 `httpx.AsyncClient` + `ASGITransport`，`pytest-asyncio`）。

---

### Task 11: 本地验签与真实身份

**Files:**
- Create: `app/auth/verifier.py`
- Create: `tests/test_token_verifier.py`
- Modify: `app/auth/identity.py`
- Modify: `app/auth/dependency.py`
- Modify: `app/config/settings.py:60-67`（删 `DevUser`/改 `AuthSettings`）
- Modify: `tests/conftest.py`（`AuthSettings` 的构造变了）
- Modify: `tests/test_auth.py`（`mcp_headers` 的契约变了）
- Modify: `pyproject.toml`（新增 `pyjwt[crypto]`）
- Modify: `config.example.json`

**Interfaces:**
- Consumes: Phase 1 的 `GET /api/auth/copilot/jwks`
- Produces:
  - `app.auth.verifier.Claims`：`user_id: str`、`login_name: str`、`role_id: int`、`is_sys_admin: bool`、`actor: str`、`jti: str`、`expires_at: int`
  - `app.auth.verifier.InvalidToken(Exception)`
  - `TokenVerifier(jwks_url: str, cache_ttl_seconds: int = 600, client: httpx.AsyncClient | None = None)`，`async def verify(self, token: str) -> Claims`
  - `Identity`：`user_id`、`display_name`、`role_id: int`、`raw_token: str`（**删掉 `scopes`**）
  - `mcp_headers(...)` 产出 `X-Copilot-Access-Token`，**不再产出 `X-Copilot-Scopes`**
  - `AuthSettings`：`jwks_url: str`、`jwks_cache_ttl_seconds: int = 600`、`admin_api_base_url: str`、`service_token: SecretStr`（**删掉 `dev_user`**）

- [ ] **Step 1: 装依赖**

```bash
uv add 'pyjwt[crypto]>=2.9'
```

`cryptography` 已经在依赖里（MySQL 的 `caching_sha2_password` 需要它），所以 `[crypto]` extra 不引入新的传递依赖。

- [ ] **Step 2: 写失败测试**

创建 `tests/test_token_verifier.py`：

```python
"""access token 的本地验签。"""

from __future__ import annotations

import base64
import json
import time

import httpx
import jwt
import pytest
from cryptography.hazmat.primitives.asymmetric import rsa

from app.auth.verifier import AUDIENCE, ISSUER, LEEWAY_SECONDS, InvalidToken, TokenVerifier


def _b64(value: int) -> str:
    raw = value.to_bytes((value.bit_length() + 7) // 8, "big")
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


class Signer:
    """造一个 RSA 密钥并按 admin-api 的 claim 集签 token。"""

    def __init__(self, kid: str = "kid-1") -> None:
        self.kid = kid
        self.key = rsa.generate_private_key(public_exponent=65537, key_size=2048)

    def sign(self, **overrides: object) -> str:
        now = int(time.time())
        claims: dict[str, object] = {
            "iss": ISSUER,
            "aud": [AUDIENCE],
            "sub": "10001",
            "login_name": "zhangsan",
            "role_id": 7,
            "is_sys_admin": False,
            "actor": "ai-copilot",
            "jti": "jti-1",
            "iat": now,
            "exp": now + 1800,
        }
        claims.update(overrides)
        return jwt.encode(claims, self.key, algorithm="RS256", headers={"kid": self.kid})

    def jwks(self) -> dict[str, object]:
        numbers = self.key.public_key().public_numbers()
        return {
            "keys": [
                {
                    "kty": "RSA",
                    "use": "sig",
                    "alg": "RS256",
                    "kid": self.kid,
                    "n": _b64(numbers.n),
                    "e": _b64(numbers.e),
                }
            ]
        }


def jwks_client(signer: Signer, counter: list[int] | None = None) -> httpx.AsyncClient:
    def handler(request: httpx.Request) -> httpx.Response:
        if counter is not None:
            counter.append(1)
        return httpx.Response(200, json=signer.jwks())

    return httpx.AsyncClient(transport=httpx.MockTransport(handler))


@pytest.mark.asyncio
async def test_a_valid_token_yields_the_signed_claims():
    signer = Signer()
    verifier = TokenVerifier("http://admin.test/jwks", client=jwks_client(signer))

    claims = await verifier.verify(signer.sign())

    assert claims.user_id == "10001"
    assert claims.login_name == "zhangsan"
    assert claims.role_id == 7
    assert claims.is_sys_admin is False
    assert claims.actor == "ai-copilot"
    assert claims.jti == "jti-1"


@pytest.mark.asyncio
async def test_a_token_from_another_key_is_rejected():
    trusted, foreign = Signer(), Signer()
    verifier = TokenVerifier("http://admin.test/jwks", client=jwks_client(trusted))

    with pytest.raises(InvalidToken):
        await verifier.verify(foreign.sign())


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "overrides",
    [{"iss": "someone-else"}, {"aud": ["not-copilot"]}],
    ids=["issuer", "audience"],
)
async def test_issuer_and_audience_are_enforced(overrides: dict[str, object]):
    signer = Signer()
    verifier = TokenVerifier("http://admin.test/jwks", client=jwks_client(signer))

    with pytest.raises(InvalidToken):
        await verifier.verify(signer.sign(**overrides))


@pytest.mark.asyncio
async def test_clock_skew_within_the_leeway_is_tolerated():
    """Review Focus 第 2 条。

    同一个 token 被 admin-api、MCP、copilot-api 三处独立验签，时钟各自漂移。
    零 leeway 会把几秒的偏差变成零星、不可复现的 401。
    """
    signer = Signer()
    verifier = TokenVerifier("http://admin.test/jwks", client=jwks_client(signer))

    just_expired = signer.sign(exp=int(time.time()) - 10)

    claims = await verifier.verify(just_expired)
    assert claims.user_id == "10001"


@pytest.mark.asyncio
async def test_expiry_beyond_the_leeway_is_rejected():
    signer = Signer()
    verifier = TokenVerifier("http://admin.test/jwks", client=jwks_client(signer))

    long_expired = signer.sign(exp=int(time.time()) - (LEEWAY_SECONDS + 30))

    with pytest.raises(InvalidToken):
        await verifier.verify(long_expired)


@pytest.mark.asyncio
async def test_the_key_set_is_cached():
    signer = Signer()
    hits: list[int] = []
    verifier = TokenVerifier("http://admin.test/jwks", client=jwks_client(signer, hits))

    for _ in range(3):
        await verifier.verify(signer.sign())

    assert len(hits) == 1


@pytest.mark.asyncio
async def test_an_unknown_kid_triggers_one_refetch():
    signer = Signer(kid="kid-rotated")
    hits: list[int] = []
    verifier = TokenVerifier("http://admin.test/jwks", client=jwks_client(signer, hits))
    verifier.seed_for_test({"kid-old": object()})

    await verifier.verify(signer.sign())

    assert len(hits) == 1


@pytest.mark.asyncio
async def test_an_unreachable_jwks_fails_closed():
    """身份验证 fail-closed —— 验不了签就没有身份。

    只有 tool 授权是 fail-open（见 MCP 的 Task 10）。
    """
    signer = Signer()

    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(500)

    verifier = TokenVerifier(
        "http://admin.test/jwks",
        client=httpx.AsyncClient(transport=httpx.MockTransport(handler)),
    )

    with pytest.raises(InvalidToken):
        await verifier.verify(signer.sign())


@pytest.mark.asyncio
async def test_a_malformed_token_is_rejected_without_leaking_an_internal_error():
    signer = Signer()
    verifier = TokenVerifier("http://admin.test/jwks", client=jwks_client(signer))

    for value in ["", "not-a-jwt", "a.b.c"]:
        with pytest.raises(InvalidToken):
            await verifier.verify(value)
```

- [ ] **Step 3: 跑测试确认失败**

Run: `uv run pytest tests/test_token_verifier.py`
Expected: FAIL，`ModuleNotFoundError: No module named 'app.auth.verifier'`

- [ ] **Step 4: 实现 verifier.py**

创建 `app/auth/verifier.py`：

```python
"""copilot access token 的本地验签。

和 internal-mcp-server 的 internal/auth/verifier.go 是同一套逻辑的两个实现：
同一个 JWKS、同一个算法、同一个 leeway。改动其中一边时另一边要跟着改。

不用 PyJWT 自带的 PyJWKClient：它走同步 urllib，在 async 端点里会阻塞事件循环。
"""

from __future__ import annotations

import base64
import json
import time

import httpx
import jwt
from jwt.algorithms import RSAAlgorithm

ISSUER = "admin-api"
AUDIENCE = "copilot"

# 三处独立验签，时钟各自漂移。三边必须用同一个值。
LEEWAY_SECONDS = 60


class InvalidToken(Exception):
    """token 无法被信任。

    刻意不携带原因细节：调用方统一翻成 401，把「签名错」和「过期」区分开来
    回给客户端只会方便攻击者试探。细节走日志。
    """


class Claims:
    """admin-api 签发的封闭 claim 集。"""

    __slots__ = ("user_id", "login_name", "role_id", "is_sys_admin", "actor", "jti", "expires_at")

    def __init__(
        self,
        *,
        user_id: str,
        login_name: str,
        role_id: int,
        is_sys_admin: bool,
        actor: str,
        jti: str,
        expires_at: int,
    ) -> None:
        self.user_id = user_id
        self.login_name = login_name
        self.role_id = role_id
        self.is_sys_admin = is_sys_admin
        self.actor = actor
        self.jti = jti
        self.expires_at = expires_at


class TokenVerifier:
    def __init__(
        self,
        jwks_url: str,
        cache_ttl_seconds: int = 600,
        client: httpx.AsyncClient | None = None,
    ) -> None:
        self._url = jwks_url
        self._ttl = cache_ttl_seconds
        self._client = client or httpx.AsyncClient(timeout=5.0)
        self._keys: dict[str, object] = {}
        self._fetched_at = 0.0

    async def verify(self, token: str) -> Claims:
        try:
            kid = jwt.get_unverified_header(token).get("kid")
        except jwt.PyJWTError as exc:
            raise InvalidToken("malformed token header") from exc

        key = await self._key_for(kid)

        try:
            payload = jwt.decode(
                token,
                key,
                algorithms=["RS256"],
                audience=AUDIENCE,
                issuer=ISSUER,
                leeway=LEEWAY_SECONDS,
            )
        except jwt.PyJWTError as exc:
            raise InvalidToken("token rejected") from exc

        return Claims(
            user_id=str(payload["sub"]),
            login_name=str(payload.get("login_name", "")),
            role_id=int(payload.get("role_id", 0)),
            is_sys_admin=bool(payload.get("is_sys_admin", False)),
            actor=str(payload.get("actor", "")),
            jti=str(payload.get("jti", "")),
            expires_at=int(payload.get("exp", 0)),
        )

    async def _key_for(self, kid: str | None) -> object:
        if kid is None:
            raise InvalidToken("token has no kid")

        key = self._cached(kid)
        if key is not None:
            return key

        await self._refresh()

        key = self._cached(kid)
        if key is None:
            raise InvalidToken("no key for this kid")
        return key

    def _cached(self, kid: str) -> object | None:
        if not self._keys or (time.monotonic() - self._fetched_at) > self._ttl:
            return None
        return self._keys.get(kid)

    async def _refresh(self) -> None:
        try:
            response = await self._client.get(self._url)
            response.raise_for_status()
            document = response.json()
        except (httpx.HTTPError, ValueError) as exc:
            # fail-closed：验不了签就没有身份
            raise InvalidToken("key set unavailable") from exc

        keys: dict[str, object] = {}
        for entry in document.get("keys", []):
            if entry.get("kty") != "RSA" or not entry.get("kid"):
                continue
            try:
                keys[entry["kid"]] = RSAAlgorithm.from_jwk(json.dumps(entry))
            except (jwt.PyJWTError, ValueError, KeyError):
                continue

        if not keys:
            raise InvalidToken("key set carried no usable RSA key")

        self._keys = keys
        self._fetched_at = time.monotonic()

    def seed_for_test(self, keys: dict[str, object]) -> None:
        self._keys = keys
        self._fetched_at = time.monotonic()
```

- [ ] **Step 5: 跑测试确认通过**

Run: `uv run pytest tests/test_token_verifier.py -v`
Expected: PASS，10 个测试全绿

- [ ] **Step 6: 改 settings.py**

删掉 `DevUser` 类，`AuthSettings` 改为：

```python
class AuthSettings(BaseModel):
    """copilot 链路的认证配置。

    身份来自 admin-api 签发的 access token，本服务只验签 —— 不签发、不存用户。
    """

    # admin-api 的公钥集
    jwks_url: str
    jwks_cache_ttl_seconds: int = Field(default=600, ge=30, le=86400)

    # admin-api 的认证端点前缀（已含 /api）。登录请求经本服务代理，
    # 浏览器从不直连 admin-api —— 跨子域直连会引入 CORS 和第三方 cookie。
    admin_api_base_url: str

    # 换票时带给 admin-api 的服务身份。
    service_token: SecretStr = SecretStr("")
```

`Settings` 里 `auth: AuthSettings` 保持必填。在 `_operations_agent_needs_credentials` 校验器里追加 `auth.service_token` 的检查（与 `mcp.service_token` 同理：缺了就启动失败，而不是等第一个登录请求 401）：

```python
                    ("auth.service_token", self.auth.service_token.get_secret_value()),
```

`load_settings()` 的 docstring 里「必填项（database.dsn、auth.dev_user）」改成「（database.dsn、auth.jwks_url、auth.admin_api_base_url）」。

- [ ] **Step 7: 改 identity.py**

```python
"""调用方身份。

身份来自 admin-api 签发并由 app/auth/verifier.py 验签的 access token。
往下传给 MCP 时带两样东西：服务身份（service token）和用户的 access token
本身 —— MCP 会用同一个 JWKS 独立验签，不信任本服务声称的任何权限。
"""

from __future__ import annotations

import re

from pydantic import BaseModel

from app.config.settings import IDENTITY_VALUE_PATTERN

_VALUE_RE = re.compile(IDENTITY_VALUE_PATTERN)


class Identity(BaseModel):
    user_id: str
    display_name: str
    role_id: int
    # 用户的 access token 原文，透传给 MCP 再透传给 admin-api，
    # 使下游鉴权看到的是真实运营用户。
    raw_token: str


def mcp_headers(
    *, identity: Identity, session_id: str, request_id: str, service_token: str
) -> dict[str, str]:
    """构造调用 internal-mcp-server 所需的头。

    MCP 的 auth 中间件（internal/auth/service.go）要求：service token、
    用户 access token、以及三个关联 ID 且值匹配
    ^[A-Za-z0-9_.:@-]{1,128}$。X-Copilot-User-Id 必须与 token 的 sub 一致，
    否则 MCP 返回 401 —— 它已经降级为日志字段，但不一致会让审计指向错误的人。

    不再传 X-Copilot-Scopes：权限由 MCP 自己向 admin-api 拉取。调用方声称的
    权限等于调用方自授权，那是这次改造要修掉的主要缺陷。
    """
    for name, value in (
        ("user_id", identity.user_id),
        ("session_id", session_id),
        ("request_id", request_id),
    ):
        if not _VALUE_RE.match(value):
            raise ValueError(f"{name} 不满足 internal-mcp-server 的取值约束: {value!r}")

    return {
        "Authorization": f"Bearer {service_token}",
        "X-Copilot-Access-Token": identity.raw_token,
        "X-Copilot-User-Id": identity.user_id,
        "X-Copilot-Session-Id": session_id,
        "X-Copilot-Request-Id": request_id,
    }
```

- [ ] **Step 8: 改 dependency.py**

```python
async def get_current_identity(request: Request) -> Identity:
    """当前请求的用户身份。

    身份只来自 Authorization 头里的 access token，本地验签。没有任何回退 ——
    拿不到有效 token 就是 401，不存在「默认用户」。
    """
    # 不再需要 settings：verifier 在 app.state 上，由 main.py 用配置构造好。
    token = _bearer_token(request.headers.get("Authorization", ""))
    if not token:
        raise HTTPException(status_code=401, detail={"code": "AUTH_REQUIRED"})

    try:
        claims = await request.app.state.token_verifier.verify(token)
    except InvalidToken:
        raise HTTPException(status_code=401, detail={"code": "AUTH_TOKEN_INVALID"}) from None

    return Identity(
        user_id=claims.user_id,
        display_name=claims.login_name,
        role_id=claims.role_id,
        raw_token=token,
    )


def _bearer_token(header: str) -> str:
    scheme, _, value = header.partition(" ")
    return value.strip() if scheme.lower() == "bearer" else ""
```

在 `app/main.py` 的应用装配处（`app.state.settings` / `app.state.agent` 附近）加：

```python
    app.state.token_verifier = TokenVerifier(
        settings.auth.jwks_url,
        settings.auth.jwks_cache_ttl_seconds,
    )
```

- [ ] **Step 9: 改 conftest.py 与 test_auth.py**

`tests/conftest.py` 里构造 `Settings` 的地方，`AuthSettings(dev_user=DevUser(...))` 改为：

```python
        auth=AuthSettings(
            jwks_url="http://admin.test/api/auth/copilot/jwks",
            admin_api_base_url="http://admin.test/api",
            service_token=SecretStr("test-service-token"),
        ),
```

并删掉 `DevUser` 的 import。同时给 `app` fixture 注入一个假 verifier，使现有端点测试仍能通过 —— 在 fixture 里设置：

```python
    class _AlwaysValidVerifier:
        async def verify(self, token: str):
            from app.auth.verifier import Claims

            return Claims(
                user_id="10001",
                login_name="dev",
                role_id=7,
                is_sys_admin=False,
                actor="ai-copilot",
                jti="jti-test",
                expires_at=2 ** 31,
            )

    application.state.token_verifier = _AlwaysValidVerifier()
```

并让现有端点测试的 `AsyncClient` 默认带上 `headers={"Authorization": "Bearer test-token"}`。

`tests/test_auth.py` 的三个用例改为断言新契约：

```python
"""身份与 MCP 头构造。"""

from __future__ import annotations

import pytest

from app.auth.identity import Identity, mcp_headers


def make_identity(user_id: str = "10001") -> Identity:
    return Identity(user_id=user_id, display_name="zhangsan", role_id=7, raw_token="user-token")


def test_headers_match_internal_mcp_server_contract():
    headers = mcp_headers(
        identity=make_identity(),
        session_id="01H0000000000000000000000A",
        request_id="01H0000000000000000000000B",
        service_token="tok",
    )
    assert headers["Authorization"] == "Bearer tok"
    assert headers["X-Copilot-Access-Token"] == "user-token"
    assert headers["X-Copilot-User-Id"] == "10001"
    assert headers["X-Copilot-Session-Id"] == "01H0000000000000000000000A"
    assert headers["X-Copilot-Request-Id"] == "01H0000000000000000000000B"


def test_self_declared_scopes_are_never_sent():
    """调用方声称权限 == 调用方自授权，这是本次改造要修掉的主要缺陷。"""
    headers = mcp_headers(
        identity=make_identity(),
        session_id="s",
        request_id="r",
        service_token="tok",
    )
    assert "X-Copilot-Scopes" not in headers


def test_a_user_id_that_cannot_be_a_header_value_is_refused_here():
    with pytest.raises(ValueError):
        mcp_headers(
            identity=make_identity(user_id="../secret"),
            session_id="s",
            request_id="r",
            service_token="tok",
        )
```

- [ ] **Step 10: 更新 config.example.json**

```json
  "auth": {
    "jwks_url": "http://127.0.0.1:8000/api/auth/copilot/jwks",
    "jwks_cache_ttl_seconds": 600,
    "admin_api_base_url": "http://127.0.0.1:8000/api",
    "service_token": ""
  },
```

删掉原来的 `auth.dev_user` 整段。

- [ ] **Step 11: 跑全量测试**

Run: `uv run pytest`
Expected: PASS。`tests/test_config.py` 可能因 `AuthSettings` 字段变化而失败 —— 按新字段更新它的断言。

- [ ] **Step 12: 静态检查**

Run: `uv run ruff check . && uv run pyright`
Expected: 无告警

- [ ] **Step 13: Commit**

```bash
git add pyproject.toml uv.lock app/auth/ app/config/settings.py app/main.py \
        config.example.json tests/
git commit -m "feat(auth): take identity from the signed access token, drop the dev user"
```

---

### Task 12: admin-api 认证客户端

**Files:**
- Create: `app/auth/admin_client.py`
- Create: `tests/test_admin_auth_client.py`

**Interfaces:**
- Consumes: `AuthSettings.admin_api_base_url`、`AuthSettings.service_token`（Task 11）；Phase 1 Task 6 的四个端点
- Produces:
  - `AdminAuthClient(base_url: str, service_token: str, client: httpx.AsyncClient | None = None, timeout: float = 10.0)`
  - `async def captcha(self) -> dict` → `{"key": ..., "img": ...}`
  - `async def grant_password(self, login_name, password, captcha, key) -> TokenGrant`
  - `async def grant_refresh(self, refresh_token: str) -> TokenGrant`
  - `async def logout(self, access_token: str, refresh_token: str | None) -> None`
  - `TokenGrant`：`access_token: str`、`expires_at: int`、`refresh_token: str`、`user_id: int`、`login_name: str`
  - `AdminAuthError(Exception)` 带 `code: str`（spec §9 的错误码之一，或 `AUTH_UPSTREAM`）

- [ ] **Step 1: 写失败测试**

创建 `tests/test_admin_auth_client.py`：

```python
"""调 admin-api 认证端点的客户端。"""

from __future__ import annotations

import httpx
import pytest

from app.auth.admin_client import AdminAuthClient, AdminAuthError


def client_for(handler) -> AdminAuthClient:
    return AdminAuthClient(
        "http://admin.test/api",
        "svc-token",
        client=httpx.AsyncClient(transport=httpx.MockTransport(handler)),
    )


@pytest.mark.asyncio
async def test_password_grant_sends_the_service_token_and_parses_the_envelope():
    seen: dict[str, object] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        seen["url"] = str(request.url)
        seen["service_token"] = request.headers.get("X-Service-Token")
        return httpx.Response(
            200,
            json={
                "is_succ": True,
                "data": {
                    "access_token": "jwt",
                    "expires_at": 1999999999,
                    "refresh_token": "r",
                    "user": {"id": 10001, "login_name": "zhangsan"},
                },
            },
        )

    grant = await client_for(handler).grant_password("zhangsan", "pw", "abcd", "k")

    assert seen["url"] == "http://admin.test/api/auth/copilot/token"
    assert seen["service_token"] == "svc-token"
    assert grant.access_token == "jwt"
    assert grant.refresh_token == "r"
    assert grant.expires_at == 1999999999
    assert grant.user_id == 10001
    assert grant.login_name == "zhangsan"


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "code",
    [
        "AUTH_INVALID_CREDENTIALS",
        "AUTH_CAPTCHA_INVALID",
        "AUTH_CAPTCHA_EXPIRED",
        "AUTH_ACCOUNT_DISABLED",
    ],
)
async def test_the_stable_error_code_is_preserved(code: str):
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(401, json={"is_succ": False, "code": code, "message": "上游原文"})

    with pytest.raises(AdminAuthError) as excinfo:
        await client_for(handler).grant_password("a", "b", "c", "d")

    assert excinfo.value.code == code


@pytest.mark.asyncio
async def test_the_upstream_message_is_not_propagated():
    """spec §9：不透传 admin-api 的原始 message，防账号枚举。"""

    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            401,
            json={"is_succ": False, "code": "AUTH_INVALID_CREDENTIALS", "message": "账号 zhangsan 不存在"},
        )

    with pytest.raises(AdminAuthError) as excinfo:
        await client_for(handler).grant_password("zhangsan", "b", "c", "d")

    assert "zhangsan" not in str(excinfo.value)


@pytest.mark.asyncio
async def test_a_429_from_admin_api_becomes_the_rate_limited_code():
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(429, text="Too Many Requests")

    with pytest.raises(AdminAuthError) as excinfo:
        await client_for(handler).grant_password("a", "b", "c", "d")

    assert excinfo.value.code == "AUTH_RATE_LIMITED"


@pytest.mark.asyncio
async def test_a_transport_failure_becomes_an_upstream_code():
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("refused")

    with pytest.raises(AdminAuthError) as excinfo:
        await client_for(handler).grant_password("a", "b", "c", "d")

    assert excinfo.value.code == "AUTH_UPSTREAM"


@pytest.mark.asyncio
async def test_refresh_grant_posts_the_refresh_token():
    seen: dict[str, object] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        seen["url"] = str(request.url)
        seen["body"] = request.content.decode()
        return httpx.Response(
            200,
            json={
                "is_succ": True,
                "data": {
                    "access_token": "jwt2",
                    "expires_at": 1,
                    "refresh_token": "r2",
                    "user": {"id": 10001, "login_name": "zhangsan"},
                },
            },
        )

    grant = await client_for(handler).grant_refresh("r1")

    assert seen["url"] == "http://admin.test/api/auth/copilot/refresh"
    assert "r1" in str(seen["body"])
    assert grant.refresh_token == "r2"


@pytest.mark.asyncio
async def test_captcha_is_proxied_unchanged():
    def handler(request: httpx.Request) -> httpx.Response:
        assert str(request.url) == "http://admin.test/api/auth/captcha"
        return httpx.Response(200, json={"is_succ": True, "data": {"key": "k", "img": "data:image/png;base64,xx"}})

    result = await client_for(handler).captcha()

    assert result == {"key": "k", "img": "data:image/png;base64,xx"}


@pytest.mark.asyncio
async def test_logout_sends_the_users_access_token():
    seen: dict[str, object] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        seen["authorization"] = request.headers.get("Authorization")
        return httpx.Response(200, json={"is_succ": True})

    await client_for(handler).logout("user-jwt", "r1")

    assert seen["authorization"] == "Bearer user-jwt"


@pytest.mark.asyncio
async def test_logout_does_not_raise_when_the_token_is_already_dead():
    """登出必须幂等：目标状态是「已登出」，上游说 401 也算达成。"""

    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(401, json={"is_succ": False, "code": "AUTH_TOKEN_INVALID"})

    await client_for(handler).logout("stale-jwt", "r1")
```

- [ ] **Step 2: 跑测试确认失败**

Run: `uv run pytest tests/test_admin_auth_client.py`
Expected: FAIL，`ModuleNotFoundError: No module named 'app.auth.admin_client'`

- [ ] **Step 3: 实现 admin_client.py**

创建 `app/auth/admin_client.py`：

```python
"""调 admin-api 认证端点的唯一出口。

这是 ai-copilot-api 对 admin-api 的唯一直接依赖，只用于认证 —— 业务数据一律
经 internal-mcp-server。登录走代理而不是让浏览器直连 admin-api：两者是不同
子域，直连会引入 CORS 和第三方 cookie（正被浏览器淘汰）。
"""

from __future__ import annotations

from dataclasses import dataclass

import httpx

_KNOWN_CODES = frozenset(
    {
        "AUTH_INVALID_CREDENTIALS",
        "AUTH_CAPTCHA_INVALID",
        "AUTH_CAPTCHA_EXPIRED",
        "AUTH_ACCOUNT_DISABLED",
        "AUTH_RATE_LIMITED",
        "AUTH_REFRESH_REJECTED",
        "AUTH_SERVICE_UNAUTHORIZED",
        "AUTH_TOKEN_INVALID",
    }
)


class AdminAuthError(Exception):
    """admin-api 拒绝了这次认证请求。

    只带稳定错误码，不带上游的 message —— admin-api 的原文可能区分
    「账号不存在」和「密码错误」，透传出去就是账号枚举。
    """

    def __init__(self, code: str) -> None:
        self.code = code
        super().__init__(code)


@dataclass(frozen=True)
class TokenGrant:
    access_token: str
    expires_at: int
    refresh_token: str
    user_id: int
    login_name: str


class AdminAuthClient:
    def __init__(
        self,
        base_url: str,
        service_token: str,
        client: httpx.AsyncClient | None = None,
        timeout: float = 10.0,
    ) -> None:
        self._base = base_url.rstrip("/")
        self._service_token = service_token
        self._client = client or httpx.AsyncClient(timeout=timeout)

    async def captcha(self) -> dict:
        body = await self._request("GET", "/auth/captcha")
        return body.get("data") or {}

    async def grant_password(
        self, login_name: str, password: str, captcha: str | None, key: str | None
    ) -> TokenGrant:
        body = await self._request(
            "POST",
            "/auth/copilot/token",
            json={
                "login_name": login_name,
                "password": password,
                "captcha": captcha or "",
                "key": key or "",
            },
            service_token=True,
        )
        return self._grant(body)

    async def grant_refresh(self, refresh_token: str) -> TokenGrant:
        body = await self._request(
            "POST",
            "/auth/copilot/refresh",
            json={"refresh_token": refresh_token},
            service_token=True,
        )
        return self._grant(body)

    async def logout(self, access_token: str, refresh_token: str | None) -> None:
        try:
            await self._request(
                "POST",
                "/auth/copilot/logout",
                json={"refresh_token": refresh_token or ""},
                bearer=access_token,
            )
        except AdminAuthError:
            # 登出是幂等的：目标状态是「已登出」，上游说凭证无效也算达成。
            return

    async def _request(
        self,
        method: str,
        path: str,
        *,
        json: dict | None = None,
        service_token: bool = False,
        bearer: str | None = None,
    ) -> dict:
        headers: dict[str, str] = {"Accept": "application/json"}
        if service_token:
            headers["X-Service-Token"] = self._service_token
        if bearer:
            headers["Authorization"] = f"Bearer {bearer}"

        try:
            response = await self._client.request(
                method, f"{self._base}{path}", json=json, headers=headers
            )
        except httpx.HTTPError as exc:
            raise AdminAuthError("AUTH_UPSTREAM") from exc

        if response.status_code == 429:
            raise AdminAuthError("AUTH_RATE_LIMITED")

        try:
            body = response.json()
        except ValueError as exc:
            raise AdminAuthError("AUTH_UPSTREAM") from exc

        if response.status_code >= 400 or not body.get("is_succ", False):
            code = str(body.get("code") or "")
            raise AdminAuthError(code if code in _KNOWN_CODES else "AUTH_UPSTREAM")

        return body

    @staticmethod
    def _grant(body: dict) -> TokenGrant:
        data = body.get("data") or {}
        user = data.get("user") or {}
        try:
            return TokenGrant(
                access_token=str(data["access_token"]),
                expires_at=int(data["expires_at"]),
                refresh_token=str(data["refresh_token"]),
                user_id=int(user["id"]),
                login_name=str(user["login_name"]),
            )
        except (KeyError, TypeError, ValueError) as exc:
            raise AdminAuthError("AUTH_UPSTREAM") from exc
```

- [ ] **Step 4: 跑测试确认通过**

Run: `uv run pytest tests/test_admin_auth_client.py -v`
Expected: PASS，10 个测试全绿

- [ ] **Step 5: 静态检查并 commit**

Run: `uv run ruff check . && uv run pyright && uv run pytest`

```bash
git add app/auth/admin_client.py tests/test_admin_auth_client.py
git commit -m "feat(auth): add admin-api authentication client"
```

---

### Task 13: 登录端点与 refresh cookie

**Files:**
- Create: `app/api/auth.py`
- Create: `tests/test_auth_endpoints.py`
- Modify: `app/main.py`（注册 router、装配 `AdminAuthClient`）

**Interfaces:**
- Consumes: `AdminAuthClient`（Task 12）、`AuthSettings`（Task 11）
- Produces（Phase 4 的前端直接依赖这些端点的形状）：
  - `GET /api/auth/captcha` → `{"key": str, "img": str}`
  - `POST /api/auth/login` body `{login_name, password, captcha, key}` → `{"access_token", "expires_at", "user": {"user_id", "display_name"}}` + `Set-Cookie: copilot_refresh=...`
  - `POST /api/auth/refresh` （无 body，靠 cookie）→ 同上形状
  - `POST /api/auth/logout` → 204，并清 cookie
  - 失败 → `401` + 项目统一错误信封 `{"error": {"code": "AUTH_*", "message": str}}`（与 `src/api/errors.ts` 对齐，否则前端 `toAppError` 会把登录错误全降级成 `UNKNOWN`）
  - cookie 名 `copilot_refresh`，属性 `HttpOnly; Secure; SameSite=Strict; Path=/api/auth`，**不设 `Domain`**

- [ ] **Step 1: 写失败测试**

创建 `tests/test_auth_endpoints.py`：

```python
"""登录、刷新、登出端点。"""

from __future__ import annotations

import httpx
import pytest
from fastapi import FastAPI
from httpx import ASGITransport, AsyncClient

from app.api import auth as auth_api
from app.auth.admin_client import AdminAuthError, TokenGrant

GRANT = TokenGrant(
    access_token="jwt",
    expires_at=1999999999,
    refresh_token="refresh-value",
    user_id=10001,
    login_name="zhangsan",
)


class FakeAdminAuth:
    def __init__(self, grant: TokenGrant | None = None, error: AdminAuthError | None = None) -> None:
        self._grant = grant
        self._error = error
        self.logged_out_with: tuple[str, str | None] | None = None

    async def captcha(self) -> dict:
        return {"key": "k", "img": "img"}

    async def grant_password(self, *args, **kwargs) -> TokenGrant:
        if self._error:
            raise self._error
        assert self._grant is not None
        return self._grant

    async def grant_refresh(self, refresh_token: str) -> TokenGrant:
        if self._error:
            raise self._error
        assert self._grant is not None
        return self._grant

    async def logout(self, access_token: str, refresh_token: str | None) -> None:
        self.logged_out_with = (access_token, refresh_token)


def build_app(admin: FakeAdminAuth) -> FastAPI:
    application = FastAPI()
    application.state.admin_auth = admin
    application.include_router(auth_api.router)
    return application


async def client_for(admin: FakeAdminAuth) -> AsyncClient:
    return AsyncClient(
        transport=ASGITransport(app=build_app(admin)),
        base_url="https://copilot.test",
    )


@pytest.mark.asyncio
async def test_login_returns_the_access_token_in_the_body():
    async with await client_for(FakeAdminAuth(GRANT)) as client:
        response = await client.post(
            "/api/auth/login",
            json={"login_name": "zhangsan", "password": "pw", "captcha": "abcd", "key": "k"},
        )

    assert response.status_code == 200
    body = response.json()
    assert body["access_token"] == "jwt"
    assert body["expires_at"] == 1999999999
    assert body["user"] == {"user_id": "10001", "display_name": "zhangsan"}
    # refresh token 绝不能出现在响应体里 —— 它只走 HttpOnly cookie
    assert "refresh_token" not in body


@pytest.mark.asyncio
async def test_the_refresh_cookie_is_httponly_secure_strict_and_path_scoped():
    async with await client_for(FakeAdminAuth(GRANT)) as client:
        response = await client.post(
            "/api/auth/login",
            json={"login_name": "zhangsan", "password": "pw", "captcha": "abcd", "key": "k"},
        )

    header = response.headers["set-cookie"]
    lowered = header.lower()
    assert "copilot_refresh=refresh-value" in header
    assert "httponly" in lowered
    assert "secure" in lowered
    assert "samesite=strict" in lowered
    assert "path=/api/auth" in lowered


@pytest.mark.asyncio
async def test_the_refresh_cookie_has_no_domain_attribute():
    """Review Focus 第 4 条。

    写成 Domain=.example.com 之后一切功能照常 —— 登录、刷新、登出全部正常 ——
    只是 refresh token 从此发给 admin.example.com 及其余所有子域。这个错误
    没有任何功能症状，只能靠断言拦住。
    """
    async with await client_for(FakeAdminAuth(GRANT)) as client:
        response = await client.post(
            "/api/auth/login",
            json={"login_name": "zhangsan", "password": "pw", "captcha": "abcd", "key": "k"},
        )

    assert "Domain=" not in response.headers["set-cookie"]


@pytest.mark.asyncio
async def test_rejected_login_is_a_401_with_the_stable_code():
    admin = FakeAdminAuth(error=AdminAuthError("AUTH_INVALID_CREDENTIALS"))

    async with await client_for(admin) as client:
        response = await client.post(
            "/api/auth/login",
            json={"login_name": "zhangsan", "password": "bad", "captcha": "abcd", "key": "k"},
        )

    assert response.status_code == 401
    # 统一信封：前端 src/api/errors.ts 的 toAppError 只认 {"error": {...}}
    assert response.json()["error"]["code"] == "AUTH_INVALID_CREDENTIALS"
    assert "set-cookie" not in response.headers


@pytest.mark.asyncio
async def test_refresh_uses_the_cookie_and_rotates_it():
    async with await client_for(FakeAdminAuth(GRANT)) as client:
        response = await client.post("/api/auth/refresh", cookies={"copilot_refresh": "old"})

    assert response.status_code == 200
    assert response.json()["access_token"] == "jwt"
    assert "copilot_refresh=refresh-value" in response.headers["set-cookie"]


@pytest.mark.asyncio
async def test_refresh_without_a_cookie_is_a_401():
    async with await client_for(FakeAdminAuth(GRANT)) as client:
        response = await client.post("/api/auth/refresh")

    assert response.status_code == 401
    assert response.json()["error"]["code"] == "AUTH_REFRESH_REQUIRED"


@pytest.mark.asyncio
async def test_a_rejected_refresh_clears_the_cookie():
    """被拒的 refresh 必须清 cookie，否则前端会拿着一个死 cookie 反复重试。"""
    admin = FakeAdminAuth(error=AdminAuthError("AUTH_REFRESH_REJECTED"))

    async with await client_for(admin) as client:
        response = await client.post("/api/auth/refresh", cookies={"copilot_refresh": "replayed"})

    assert response.status_code == 401
    assert 'copilot_refresh=""' in response.headers["set-cookie"] or "copilot_refresh=;" in response.headers["set-cookie"]


@pytest.mark.asyncio
async def test_logout_clears_the_cookie_and_tells_admin_api():
    admin = FakeAdminAuth(GRANT)

    async with await client_for(admin) as client:
        response = await client.post(
            "/api/auth/logout",
            headers={"Authorization": "Bearer user-jwt"},
            cookies={"copilot_refresh": "r1"},
        )

    assert response.status_code == 204
    assert admin.logged_out_with == ("user-jwt", "r1")
    assert "copilot_refresh" in response.headers["set-cookie"]


@pytest.mark.asyncio
async def test_logout_succeeds_even_without_a_cookie():
    async with await client_for(FakeAdminAuth(GRANT)) as client:
        response = await client.post("/api/auth/logout", headers={"Authorization": "Bearer x"})

    assert response.status_code == 204


@pytest.mark.asyncio
async def test_captcha_is_proxied():
    async with await client_for(FakeAdminAuth(GRANT)) as client:
        response = await client.get("/api/auth/captcha")

    assert response.status_code == 200
    assert response.json() == {"key": "k", "img": "img"}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `uv run pytest tests/test_auth_endpoints.py`
Expected: FAIL，`ModuleNotFoundError: No module named 'app.api.auth'`

- [ ] **Step 3: 实现 app/api/auth.py**

```python
"""登录、刷新、登出。

access token 回到响应体里由前端存内存；refresh token 只走 HttpOnly cookie。
这样 access token 经 Authorization 头传输（天然免 CSRF），而整个 CSRF 面收敛
到 refresh 这一个端点，由 SameSite=Strict + 单一 Path 守住。
"""

from __future__ import annotations

from fastapi import APIRouter, Request, Response
from fastapi.responses import JSONResponse
from pydantic import BaseModel, Field

from app.auth.admin_client import AdminAuthError, TokenGrant

router = APIRouter(prefix="/api/auth", tags=["auth"])

REFRESH_COOKIE = "copilot_refresh"

# Path 收窄到 /api/auth：refresh cookie 不会被带到任何业务请求上。
_COOKIE_PATH = "/api/auth"


class LoginRequest(BaseModel):
    login_name: str = Field(min_length=1, max_length=64)
    password: str = Field(min_length=1, max_length=128)
    captcha: str = ""
    key: str = ""


def _set_refresh_cookie(response: Response, value: str) -> None:
    """写 refresh cookie。

    刻意不传 domain：不设 Domain 属性意味着 host-only，cookie 只回到
    copilot.example.com。写成 .example.com 会把它发给 admin.example.com 和
    其余所有子域，而且功能上毫无症状 —— 见 tests/test_auth_endpoints.py。
    """
    response.set_cookie(
        REFRESH_COOKIE,
        value,
        httponly=True,
        secure=True,
        samesite="strict",
        path=_COOKIE_PATH,
    )


def _clear_refresh_cookie(response: Response) -> None:
    response.delete_cookie(REFRESH_COOKIE, path=_COOKIE_PATH)


def _grant_body(grant: TokenGrant) -> dict:
    return {
        "access_token": grant.access_token,
        "expires_at": grant.expires_at,
        "user": {"user_id": str(grant.user_id), "display_name": grant.login_name},
    }


def _rejected(code: str, clear_cookie: bool = False) -> JSONResponse:
    """统一错误信封。

    形状必须和 app/errors.py 的其余端点一致 —— 前端 src/api/errors.ts 的
    toAppError 只认 {"error": {"code", "message"}}，别的形状会被降级成
    UNKNOWN，错误码就白传了。message 只是兜底，前端按 code 出文案
    （spec §9：不透传上游原文，防账号枚举）。
    """
    response = JSONResponse({"error": {"code": code, "message": code}}, status_code=401)
    if clear_cookie:
        _clear_refresh_cookie(response)
    return response


@router.get("/captcha")
async def captcha(request: Request) -> dict:
    return await request.app.state.admin_auth.captcha()


@router.post("/login")
async def login(request: Request, payload: LoginRequest) -> Response:
    try:
        grant = await request.app.state.admin_auth.grant_password(
            payload.login_name, payload.password, payload.captcha, payload.key
        )
    except AdminAuthError as exc:
        return _rejected(exc.code)

    response = JSONResponse(_grant_body(grant))
    _set_refresh_cookie(response, grant.refresh_token)
    return response


@router.post("/refresh")
async def refresh(request: Request) -> Response:
    current = request.cookies.get(REFRESH_COOKIE)
    if not current:
        return _rejected("AUTH_REFRESH_REQUIRED")

    try:
        grant = await request.app.state.admin_auth.grant_refresh(current)
    except AdminAuthError as exc:
        # 清掉死 cookie，否则前端会拿着它反复重试同一个失败
        return _rejected(exc.code, clear_cookie=True)

    response = JSONResponse(_grant_body(grant))
    _set_refresh_cookie(response, grant.refresh_token)
    return response


@router.post("/logout", status_code=204)
async def logout(request: Request) -> Response:
    header = request.headers.get("Authorization", "")
    scheme, _, access_token = header.partition(" ")
    access_token = access_token.strip() if scheme.lower() == "bearer" else ""

    await request.app.state.admin_auth.logout(access_token, request.cookies.get(REFRESH_COOKIE))

    response = Response(status_code=204)
    _clear_refresh_cookie(response)
    return response
```

- [ ] **Step 4: 在 main.py 注册**

```python
    app.state.admin_auth = AdminAuthClient(
        settings.auth.admin_api_base_url,
        settings.auth.service_token.get_secret_value(),
    )
    app.include_router(auth.router)
```

认证端点**不能**依赖 `get_current_identity`（登录时还没有 token）。确认 `app/main.py` 里没有把身份依赖挂成全局 `dependencies=[...]`；若有，要把 `/api/auth/*` 排除在外。

- [ ] **Step 5: 跑测试确认通过**

Run: `uv run pytest tests/test_auth_endpoints.py -v`
Expected: PASS，10 个测试全绿

- [ ] **Step 6: 跑全量测试与静态检查**

Run: `uv run pytest && uv run ruff check . && uv run pyright`
Expected: 全绿

- [ ] **Step 7: Commit**

```bash
git add app/api/auth.py app/main.py tests/test_auth_endpoints.py
git commit -m "feat(api): add login, refresh and logout with an HttpOnly refresh cookie"
```

**Phase 3 完成标志：** `uv run pytest` 全绿；手工用 Phase 1 起的 admin-api 跑通 `POST /api/auth/login` 并确认响应头里的 `Set-Cookie` 不含 `Domain=`。

---

# Phase 4 — ai-copilot-web

所有 Phase 4 的命令都在 `/Users/antony/Workspace/mangox/ai-copilot/ai-copilot-web` 下执行。测试风格照抄 `tests/http-client.test.ts`：Vitest + `vi.stubGlobal('fetch', ...)`，`it()` 描述用中文。

---

### Task 14: token-store —— 内存持有与 single-flight refresh

**Files:**
- Create: `src/features/auth/token-store.ts`
- Create: `tests/auth-token-store.test.ts`
- Modify: `src/api/errors.ts:10-19`（`ERROR_CODES` 增加 AUTH 码）

**Interfaces:**
- Consumes: Phase 3 的 `POST /api/auth/refresh`
- Produces:
  - `getAccessToken(): string | null`
  - `setSession(session: Session | null): void`
  - `getSession(): Session | null`
  - `subscribe(listener: () => void): () => void`
  - `refreshAccessToken(): Promise<string | null>` —— **single-flight**：并发调用共用同一个在飞的 Promise
  - `type Session = { accessToken: string; expiresAt: number; user: { userId: string; displayName: string } }`
  - `ERROR_CODES` 增加 `'AUTH_REQUIRED'`、`'AUTH_TOKEN_INVALID'`、`'AUTH_REFRESH_REQUIRED'`、`'AUTH_REFRESH_REJECTED'`、`'AUTH_INVALID_CREDENTIALS'`、`'AUTH_CAPTCHA_INVALID'`、`'AUTH_CAPTCHA_EXPIRED'`、`'AUTH_ACCOUNT_DISABLED'`、`'AUTH_RATE_LIMITED'`、`'AUTH_UPSTREAM'`

**为什么是模块级而不是 React state：** `src/api/client.ts` 是非 React 模块，拦截器不能用 hook 读 token。`AuthProvider`（Task 17）通过 `subscribe` 把这里的状态镜像进 React。

- [ ] **Step 1: 写失败测试**

创建 `tests/auth-token-store.test.ts`：

```ts
import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  getAccessToken,
  getSession,
  refreshAccessToken,
  setSession,
  subscribe,
} from '@/features/auth/token-store'

afterEach(() => {
  setSession(null)
  vi.unstubAllGlobals()
})

function okRefresh(accessToken = 'new-token') {
  return new Response(
    JSON.stringify({
      access_token: accessToken,
      expires_at: 1999999999,
      user: { user_id: '10001', display_name: 'zhangsan' },
    }),
    { status: 200, headers: { 'Content-Type': 'application/json' } },
  )
}

describe('token-store', () => {
  it('setSession 之后能同步读到 token', () => {
    setSession({
      accessToken: 'tok',
      expiresAt: 1,
      user: { userId: '10001', displayName: 'zhangsan' },
    })

    expect(getAccessToken()).toBe('tok')
    expect(getSession()?.user.displayName).toBe('zhangsan')
  })

  it('token 只在内存里，不落 localStorage', () => {
    setSession({ accessToken: 'tok', expiresAt: 1, user: { userId: '1', displayName: 'a' } })

    expect(JSON.stringify(localStorage)).not.toContain('tok')
  })

  it('订阅者在会话变化时被通知', () => {
    const listener = vi.fn()
    const unsubscribe = subscribe(listener)

    setSession({ accessToken: 'tok', expiresAt: 1, user: { userId: '1', displayName: 'a' } })
    expect(listener).toHaveBeenCalledTimes(1)

    unsubscribe()
    setSession(null)
    expect(listener).toHaveBeenCalledTimes(1)
  })

  it('refresh 成功后写入新会话', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.resolve(okRefresh())),
    )

    await expect(refreshAccessToken()).resolves.toBe('new-token')
    expect(getAccessToken()).toBe('new-token')
  })

  /**
   * 这是整个前端改动里唯一有并发语义的地方，也是最容易出 bug 的地方。
   * refresh token 是轮换的：并发 refresh 中第一个成功后旧串立即作废，其余
   * 会被服务端的盗用检测判定为重放，触发该用户全量撤销 —— 表现成「用户莫名
   * 被登出」，难复现难归因。
   */
  it('并发调用只触发一次 refresh 请求', async () => {
    let resolveFetch: ((response: Response) => void) | undefined
    const spy = vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          resolveFetch = resolve
        }),
    )
    vi.stubGlobal('fetch', spy)

    const inFlight = [refreshAccessToken(), refreshAccessToken(), refreshAccessToken()]
    resolveFetch?.(okRefresh())

    await expect(Promise.all(inFlight)).resolves.toEqual([
      'new-token',
      'new-token',
      'new-token',
    ])
    expect(spy).toHaveBeenCalledTimes(1)
  })

  it('前一次 refresh 结束后，下一次会重新发请求', async () => {
    const spy = vi.fn(() => Promise.resolve(okRefresh()))
    vi.stubGlobal('fetch', spy)

    await refreshAccessToken()
    await refreshAccessToken()

    expect(spy).toHaveBeenCalledTimes(2)
  })

  it('refresh 被拒时清空会话并返回 null', async () => {
    setSession({ accessToken: 'old', expiresAt: 1, user: { userId: '1', displayName: 'a' } })
    vi.stubGlobal(
      'fetch',
      vi.fn(() =>
        Promise.resolve(
          new Response(JSON.stringify({ error: { code: 'AUTH_REFRESH_REJECTED', message: 'x' } }), {
            status: 401,
            headers: { 'Content-Type': 'application/json' },
          }),
        ),
      ),
    )

    await expect(refreshAccessToken()).resolves.toBeNull()
    expect(getAccessToken()).toBeNull()
  })

  it('refresh 失败后并发等待者都拿到 null，且仍然只发一次请求', async () => {
    const spy = vi.fn(() => Promise.resolve(new Response('', { status: 401 })))
    vi.stubGlobal('fetch', spy)

    const results = await Promise.all([refreshAccessToken(), refreshAccessToken()])

    expect(results).toEqual([null, null])
    expect(spy).toHaveBeenCalledTimes(1)
  })

  it('网络异常不抛出，按 refresh 失败处理', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.reject(new TypeError('Failed to fetch'))),
    )

    await expect(refreshAccessToken()).resolves.toBeNull()
  })
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `pnpm vitest run tests/auth-token-store.test.ts`
Expected: FAIL，无法解析 `@/features/auth/token-store`

- [ ] **Step 3: 实现 token-store.ts**

创建 `src/features/auth/token-store.ts`：

```ts
/**
 * access token 的持有者。
 *
 * 为什么是模块级而不是 React state：`src/api/client.ts` 和 `src/api/sse.ts` 是
 * 非 React 模块，拦截器没法用 hook 读 token。AuthProvider 通过 subscribe 把这里
 * 的状态镜像进 React，单一数据源仍在这里。
 *
 * access token 只存在内存里 —— 不进 localStorage、不进 sessionStorage。刷新页面
 * 会丢，由启动时的一次 refresh 补回来（refresh token 在 HttpOnly cookie 里，JS
 * 读不到也偷不走）。
 */

import { API_BASE_URL } from '@/lib/config'

export interface Session {
  accessToken: string
  expiresAt: number
  user: { userId: string; displayName: string }
}

let session: Session | null = null
const listeners = new Set<() => void>()

/**
 * 在飞的 refresh。
 *
 * 这个变量就是 single-flight 的全部实现，但它不是可选的优化：refresh token
 * 是轮换的，并发 refresh 中第一个成功后旧串立即作废，其余会被服务端判定为
 * 重放并触发该用户全量撤销。页面上同时有多个请求一起 401 是常态，所以没有
 * 它就会出现「用户莫名被登出」。
 */
let inFlight: Promise<string | null> | null = null

export function getSession(): Session | null {
  return session
}

export function getAccessToken(): string | null {
  return session?.accessToken ?? null
}

export function setSession(next: Session | null): void {
  session = next
  for (const listener of listeners) listener()
}

export function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

/**
 * 用 HttpOnly cookie 里的 refresh token 换一个新的 access token。
 *
 * 不经 `request()`：那会绕回 401 拦截器，形成递归。这里直接用 fetch。
 * 任何失败都 resolve 成 null 而不是 reject —— 调用方全是拦截器，它们关心的
 * 是「能不能继续」，不是失败的分类。
 */
export function refreshAccessToken(): Promise<string | null> {
  if (inFlight) return inFlight

  inFlight = (async () => {
    try {
      const response = await fetch(`${API_BASE_URL}/auth/refresh`, {
        method: 'POST',
        headers: { Accept: 'application/json' },
        // cookie 是 SameSite=Strict + Path=/api/auth 的 first-party cookie，
        // same-origin 请求默认就会带上；显式写出来是为了让意图不依赖默认值。
        credentials: 'same-origin',
      })

      if (!response.ok) {
        setSession(null)
        return null
      }

      const body = (await response.json()) as {
        access_token: string
        expires_at: number
        user: { user_id: string; display_name: string }
      }

      setSession({
        accessToken: body.access_token,
        expiresAt: body.expires_at,
        user: { userId: body.user.user_id, displayName: body.user.display_name },
      })

      return body.access_token
    } catch {
      setSession(null)
      return null
    } finally {
      inFlight = null
    }
  })()

  return inFlight
}
```

- [ ] **Step 4: 扩 ERROR_CODES**

修改 `src/api/errors.ts` 的 `ERROR_CODES`，在现有条目后追加：

```ts
  // 认证相关。后端 app/api/auth.py 用同一个 {error:{code,message}} 信封返回
  // 这些码；不列在这里的话 toAppError 会把它们全降级成 UNKNOWN。
  'AUTH_REQUIRED', // 401 没带 token
  'AUTH_TOKEN_INVALID', // 401 token 无效或过期
  'AUTH_REFRESH_REQUIRED', // 401 没有 refresh cookie
  'AUTH_REFRESH_REJECTED', // 401 refresh 被拒（含重放检测）
  'AUTH_INVALID_CREDENTIALS', // 401 账号或密码错误
  'AUTH_CAPTCHA_INVALID', // 401 验证码错误
  'AUTH_CAPTCHA_EXPIRED', // 401 验证码过期
  'AUTH_ACCOUNT_DISABLED', // 401 账号被禁用
  'AUTH_RATE_LIMITED', // 401 上游限流
  'AUTH_UPSTREAM', // 401 admin-api 不可用
```

同时在 `describeError()` 的 switch 里给这些码补中文文案（登录页会直接用）：账号或密码错误 / 验证码错误 / 验证码已过期，请重新获取 / 账号已被禁用 / 操作过于频繁，请稍后再试 / 登录服务暫时不可用 / 登录状态已过期，请重新登录。

- [ ] **Step 5: 跑测试确认通过**

Run: `pnpm vitest run tests/auth-token-store.test.ts`
Expected: PASS，10 个测试全绿

- [ ] **Step 6: 类型检查并 commit**

Run: `pnpm typecheck && pnpm lint`

```bash
git add src/features/auth/token-store.ts src/api/errors.ts tests/auth-token-store.test.ts
git commit -m "feat(auth): hold the access token in memory with single-flight refresh"
```

---

### Task 15: client.ts —— Authorization 头、401 重放、403 不刷新

**Files:**
- Modify: `src/api/client.ts`
- Test: `tests/auth-client-retry.test.ts`（新建）
- Test: `tests/http-client.test.ts`（现有用例需要适配新增的 header）

**Interfaces:**
- Consumes: `getAccessToken()`、`refreshAccessToken()`（Task 14）
- Produces: `request()` 的签名不变；行为新增 —— 带 `Authorization`、401 自动 refresh 并重放一次、403 不 refresh、`/auth/*` 的 401 不 refresh

- [ ] **Step 1: 写失败测试**

创建 `tests/auth-client-retry.test.ts`：

```ts
import { afterEach, describe, expect, it, vi } from 'vitest'

import { request } from '@/api/client'
import { refreshAccessToken, setSession } from '@/features/auth/token-store'

afterEach(() => {
  setSession(null)
  vi.unstubAllGlobals()
})

function session(accessToken = 'old-token') {
  setSession({
    accessToken,
    expiresAt: 1999999999,
    user: { userId: '10001', displayName: 'zhangsan' },
  })
}

function json(status: number, body: unknown) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

const REFRESHED = {
  access_token: 'fresh-token',
  expires_at: 1999999999,
  user: { user_id: '10001', display_name: 'zhangsan' },
}

describe('request 的认证行为', () => {
  it('带上 Authorization 头', async () => {
    session('tok')
    // 形参必须写出来：零参的 vi.fn 会把 mock.calls 推导成空元组，
    // 后面 calls[0][1] 过不了 tsc（照抄 tests/http-client.test.ts 的 stubFetch）。
    const spy = vi.fn((_url: string, _init?: RequestInit) =>
      Promise.resolve(json(200, { ok: true })),
    )
    vi.stubGlobal('fetch', spy)

    await request('/conversations')

    const init = spy.mock.calls[0][1] as RequestInit
    expect((init.headers as Record<string, string>)['Authorization']).toBe('Bearer tok')
  })

  it('没有 token 时不带 Authorization 头', async () => {
    const spy = vi.fn((_url: string, _init?: RequestInit) =>
      Promise.resolve(json(200, { ok: true })),
    )
    vi.stubGlobal('fetch', spy)

    await request('/conversations')

    const init = spy.mock.calls[0][1] as RequestInit
    expect((init.headers as Record<string, string>)['Authorization']).toBeUndefined()
  })

  it('401 之后 refresh 并用新 token 重放原请求', async () => {
    session('old-token')
    const authorizations: (string | undefined)[] = []
    const spy = vi.fn((url: string, init?: RequestInit) => {
      if (String(url).includes('/auth/refresh')) return Promise.resolve(json(200, REFRESHED))

      const authorization = (init?.headers as Record<string, string> | undefined)?.['Authorization']
      authorizations.push(authorization)

      return Promise.resolve(
        authorization === 'Bearer fresh-token'
          ? json(200, { ok: true })
          : json(401, { error: { code: 'AUTH_TOKEN_INVALID', message: 'x' } }),
      )
    })
    vi.stubGlobal('fetch', spy)

    await expect(request('/conversations')).resolves.toEqual({ ok: true })

    // 第一次用旧 token，重放时用新 token
    expect(authorizations).toEqual(['Bearer old-token', 'Bearer fresh-token'])
    // 原请求 + refresh + 重放
    expect(spy).toHaveBeenCalledTimes(3)
  })

  it('refresh 失败时把 401 抛给调用方', async () => {
    session('old-token')
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) =>
        Promise.resolve(
          String(url).includes('/auth/refresh')
            ? json(401, { error: { code: 'AUTH_REFRESH_REJECTED', message: 'x' } })
            : json(401, { error: { code: 'AUTH_TOKEN_INVALID', message: 'x' } }),
        ),
      ),
    )

    await expect(request('/conversations')).rejects.toMatchObject({ httpStatus: 401 })
  })

  it('只重放一次 —— 重放后仍然 401 就抛出，不进入循环', async () => {
    session('old-token')
    const spy = vi.fn((url: string) =>
      Promise.resolve(
        String(url).includes('/auth/refresh')
          ? json(200, REFRESHED)
          : json(401, { error: { code: 'AUTH_TOKEN_INVALID', message: 'x' } }),
      ),
    )
    vi.stubGlobal('fetch', spy)

    await expect(request('/conversations')).rejects.toMatchObject({ httpStatus: 401 })
    // 原请求 + refresh + 一次重放 = 3；不能更多
    expect(spy).toHaveBeenCalledTimes(3)
  })

  /**
   * 403 和 401 混淆的后果：用户问了个超出权限的问题，前端误判成登录过期，
   * refresh 一圈仍然 403，最后把人踢到登录页。
   */
  it('403 不触发 refresh，直接抛给调用方', async () => {
    session('tok')
    const spy = vi.fn(() =>
      Promise.resolve(json(403, { error: { code: 'PERMISSION_DENIED', message: '无权限' } })),
    )
    vi.stubGlobal('fetch', spy)

    await expect(request('/conversations')).rejects.toMatchObject({
      code: 'PERMISSION_DENIED',
      httpStatus: 403,
    })
    expect(spy).toHaveBeenCalledTimes(1)
  })

  /**
   * Review Focus 第 3 条的集成版：页面上多个请求一起 401 时，必须只打一次
   * refresh。否则轮换中的 refresh token 会被判定重放，用户被全量撤销。
   */
  it('多个请求同时 401 时只发一次 refresh', async () => {
    session('old-token')
    let refreshCalls = 0
    let refreshed = false

    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) => {
        if (String(url).includes('/auth/refresh')) {
          refreshCalls += 1
          return new Promise<Response>((resolve) =>
            setTimeout(() => {
              refreshed = true
              resolve(json(200, REFRESHED))
            }, 10),
          )
        }
        return Promise.resolve(
          refreshed
            ? json(200, { ok: true })
            : json(401, { error: { code: 'AUTH_TOKEN_INVALID', message: 'x' } }),
        )
      }),
    )

    await Promise.all([
      request('/conversations'),
      request('/conversations/a'),
      request('/conversations/b'),
    ])

    expect(refreshCalls).toBe(1)
  })

  /**
   * 登录端点的 401 意思是「凭证不对」，不是「token 过期」。在这里 refresh 会
   * 让一次输错密码变成「刷新 + 重放登录」—— 多打两个请求，而且如果 cookie
   * 还活着，会悄悄建出一个新会话，用户看到报错却已经登录了。
   */
  it('/auth/* 的 401 不触发 refresh', async () => {
    const spy = vi.fn((url: string) =>
      Promise.resolve(json(401, { error: { code: 'AUTH_INVALID_CREDENTIALS', message: 'x' } })),
    )
    vi.stubGlobal('fetch', spy)

    await expect(
      request('/auth/login', { method: 'POST', body: { login_name: 'a', password: 'b' } }),
    ).rejects.toMatchObject({ code: 'AUTH_INVALID_CREDENTIALS' })

    expect(spy).toHaveBeenCalledTimes(1)
  })

  it('refresh 端点自身的 401 不会被再次拦截', async () => {
    const spy = vi.fn(() => Promise.resolve(json(401, { error: { code: 'AUTH_REQUIRED', message: 'x' } })))
    vi.stubGlobal('fetch', spy)

    await expect(refreshAccessToken()).resolves.toBeNull()
    expect(spy).toHaveBeenCalledTimes(1)
  })
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `pnpm vitest run tests/auth-client-retry.test.ts`
Expected: FAIL —— 没有 Authorization 头，401 不重试

- [ ] **Step 3: 改 client.ts**

把 `request()` 重构为「构造 + 发送」两步，并在 401 时刷新重放一次：

```ts
import { getAccessToken, refreshAccessToken } from '@/features/auth/token-store'

/**
 * 发一个 JSON 请求。
 *
 * 非 2xx 一律抛 AppError；网络层失败抛 code=NETWORK 的 AppError。
 * AbortError 原样抛出 —— 取消不是错误，由调用方区分处理。
 *
 * 401 会自动 refresh 并重放**一次**。只重放一次是硬上限：如果重放后仍然 401，
 * 说明问题不是 token 过期，再试只会变成死循环。403 绝不触发 refresh ——
 * 那是权限不足，刷新多少次都还是 403，混淆的后果是把人踢到登录页。
 * `/auth/*` 整体豁免：那些端点的 401 是对凭证的答复，不是过期信号。
 */
export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, signal, query } = options
  const requestId = newRequestId()
  const url = buildUrl(path, query)

  const send = (token: string | null): Promise<Response> => {
    const headers: Record<string, string> = {
      Accept: 'application/json',
      [REQUEST_ID_HEADER]: requestId,
    }
    if (body !== undefined) headers['Content-Type'] = 'application/json'
    if (token) headers['Authorization'] = `Bearer ${token}`

    return fetch(url, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal,
    })
  }

  let response: Response
  try {
    response = await send(getAccessToken())
  } catch (cause) {
    if (isAbortError(cause)) throw cause
    throw new AppError('NETWORK', '网络请求失败', { requestId, cause })
  }

  // /auth/* 自己就是认证端点：它们的 401 表示「凭证不对」，不是「token 过期」。
  // 在这里 refresh 会把一次输错密码变成「刷新 + 重放登录」，甚至悄悄建出一个
  // 会话 —— 用户看到报错却已经登录了。
  if (response.status === 401 && !path.startsWith('/auth/')) {
    const fresh = await refreshAccessToken()
    if (fresh) {
      try {
        response = await send(fresh)
      } catch (cause) {
        if (isAbortError(cause)) throw cause
        throw new AppError('NETWORK', '网络请求失败', { requestId, cause })
      }
    }
  }

  const responseId = response.headers.get(REQUEST_ID_HEADER) ?? requestId

  if (!response.ok) {
    throw toAppError(await safeJson(response), response.status, responseId)
  }

  if (response.status === 204) return undefined as T
  return (await safeJson(response)) as T
}
```

`refreshAccessToken()` 用的是裸 `fetch`（Task 14），所以 refresh 端点自身的 401 不会回到这里 —— 不存在递归。

- [ ] **Step 4: 跑测试确认通过**

Run: `pnpm vitest run tests/auth-client-retry.test.ts -v`
Expected: PASS，8 个测试全绿

- [ ] **Step 5: 跑全量测试并修现有用例**

Run: `pnpm test`
Expected: PASS。`tests/http-client.test.ts` 里断言 header 完整相等的用例要加上 `Authorization` 的考虑 —— 若有，改成只断言关心的那几个 key，不要放宽成「不检查」。

- [ ] **Step 6: 类型检查并 commit**

Run: `pnpm typecheck && pnpm lint`

```bash
git add src/api/client.ts tests/auth-client-retry.test.ts tests/http-client.test.ts
git commit -m "feat(api): attach the access token and replay once after a 401"
```

---

### Task 16: sse.ts —— 带 token，但流中途 401 不重放

**Files:**
- Modify: `src/api/sse.ts:116-132`
- Test: `tests/auth-sse.test.ts`（新建）

**Interfaces:**
- Consumes: `getAccessToken()`、`refreshAccessToken()`、`getSession()`（Task 14）
- Produces:
  - `ensureFreshToken(minRemainingSeconds?: number): Promise<string | null>`（加在 `token-store.ts`）—— 剩余寿命不足就先 refresh。这是 spec D7 方案 (i) 的落地点
  - 流式请求带 `Authorization`；**建连时**的 401 可 refresh 重试一次，**流中途**的 401 作为错误上报、不重放

- [ ] **Step 1: 写失败测试**

创建 `tests/auth-sse.test.ts`：

```ts
import { afterEach, describe, expect, it, vi } from 'vitest'

import { streamMessage } from '@/api/sse'
import { setSession } from '@/features/auth/token-store'

import { envelopeResponse, frame, sseResponse } from './helpers/sse'

afterEach(() => {
  setSession(null)
  vi.unstubAllGlobals()
})

function session(accessToken = 'tok') {
  setSession({
    accessToken,
    expiresAt: 1999999999,
    user: { userId: '10001', displayName: 'zhangsan' },
  })
}

function refreshOk() {
  return new Response(
    JSON.stringify({
      access_token: 'fresh',
      expires_at: 1999999999,
      user: { user_id: '10001', display_name: 'zhangsan' },
    }),
    { status: 200, headers: { 'Content-Type': 'application/json' } },
  )
}

function authorizationOf(call: unknown[] | undefined): string | undefined {
  const init = call?.[1] as RequestInit | undefined
  return (init?.headers as Record<string, string> | undefined)?.['Authorization']
}

describe('流式请求的认证', () => {
  it('建连时带上 Authorization 头', async () => {
    session('tok')
    const spy = vi.fn(() => Promise.resolve(sseResponse(frame('message.delta', { text: 'hi' }))))
    vi.stubGlobal('fetch', spy)

    await streamMessage({
      conversationId: 'c1',
      content: '你好',
      signal: new AbortController().signal,
    })

    expect(authorizationOf(spy.mock.calls[0])).toBe('Bearer tok')
  })

  it('建连返回 401 时 refresh 并用新 token 重试一次', async () => {
    session('old')
    let refreshed = false
    const spy = vi.fn((url: string) => {
      if (String(url).includes('/auth/refresh')) {
        refreshed = true
        return Promise.resolve(refreshOk())
      }
      return Promise.resolve(
        refreshed
          ? sseResponse(frame('message.delta', { text: 'hi' }))
          : envelopeResponse(401, 'AUTH_TOKEN_INVALID', 'expired'),
      )
    })
    vi.stubGlobal('fetch', spy)

    await streamMessage({
      conversationId: 'c1',
      content: '你好',
      signal: new AbortController().signal,
    })

    // 原请求 + refresh + 重试
    expect(spy).toHaveBeenCalledTimes(3)
    expect(authorizationOf(spy.mock.calls[2])).toBe('Bearer fresh')
  })

  it('refresh 也失败时，按契约 a 抛出建连错误', async () => {
    session('old')
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) =>
        Promise.resolve(
          String(url).includes('/auth/refresh')
            ? new Response('', { status: 401 })
            : envelopeResponse(401, 'AUTH_TOKEN_INVALID', 'expired'),
        ),
      ),
    )

    await expect(
      streamMessage({
        conversationId: 'c1',
        content: '你好',
        signal: new AbortController().signal,
      }),
    ).rejects.toMatchObject({ httpStatus: 401 })
  })

  /**
   * 流已经吐出内容之后再重放，用户会看到答案从头重复一遍。sse.ts 的文件头
   * 注释正是因为这个理由弃用了 @microsoft/fetch-event-source（它会自动重连
   * 并重发）。同样的理由适用于 token 过期。
   *
   * 这种情况本就极罕见：建连时刚验过 token，且 access TTL 是 30 分钟并在
   * run 开始前检查剩余寿命（spec D7）。真发生就按既有的 stream-error 上报。
   */
  it('流中途失败时不重放，按 stream-error 上报并保留已收到的内容', async () => {
    session('tok')

    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new TextEncoder().encode(frame('message.delta', { text: '部分答案' })))
        controller.error(new Error('stream broke'))
      },
    })
    const spy = vi.fn(() =>
      Promise.resolve(
        new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } }),
      ),
    )
    vi.stubGlobal('fetch', spy)

    const outcome = await streamMessage({
      conversationId: 'c1',
      content: '你好',
      signal: new AbortController().signal,
    })

    expect(outcome.kind).toBe('stream-error')
    if (outcome.kind === 'stream-error') {
      expect(outcome.partial).toBe('部分答案')
    }
    // 只有最初那一次请求 —— 没有 refresh、没有重放
    expect(spy).toHaveBeenCalledTimes(1)
  })
})
```

`streamMessage` 的签名是 `({ conversationId, content, signal, onEvent? }) => Promise<StreamOutcome>`：**建连前的 HTTP 错误抛 `AppError`（契约 a），流中途的失败作为 `{ kind: '''stream-error''' }` 返回值**。两者的区别正是这个 Task 的全部内容 —— 前者可以安全重试，后者不可以。

- [ ] **Step 2: 跑测试确认失败**

Run: `pnpm vitest run tests/auth-sse.test.ts`
Expected: FAIL，没有 Authorization 头

- [ ] **Step 3: 在 token-store.ts 加 ensureFreshToken**

spec D7 选的是方案 (i)：access TTL 30 分钟，**run 开始前检查剩余寿命，不足则先 refresh**。没有这一步，一次长 agent run 可能在中途跨过 `exp`，而中途过期恰恰是上面那个「不能重放」的场景 —— 只能报错给用户。所以要在建连前把它挡掉。

追加到 `src/features/auth/token-store.ts`：

```ts
/** 一次 agent run 的预留余量。长流在建连时就要确保 token 活得过整段。 */
export const MIN_REMAINING_SECONDS = 300

/**
 * 确保 token 的剩余寿命足够跑完一次长请求。
 *
 * spec D7 方案 (i)：与其让流在中途过期（那时已经吐了内容给用户，不能重放，
 * 只能报错），不如在建连前先换一个。不足时 refresh，足够时原样返回。
 */
export function ensureFreshToken(
  minRemainingSeconds: number = MIN_REMAINING_SECONDS,
): Promise<string | null> {
  const current = getSession()
  if (!current) return refreshAccessToken()

  const remaining = current.expiresAt - Math.floor(Date.now() / 1000)
  if (remaining > minRemainingSeconds) return Promise.resolve(current.accessToken)

  return refreshAccessToken()
}
```

对应测试追加到 `tests/auth-token-store.test.ts`：

```ts
describe('ensureFreshToken', () => {
  it('剩余寿命充足时不发请求', async () => {
    setSession({
      accessToken: 'tok',
      expiresAt: Math.floor(Date.now() / 1000) + 1800,
      user: { userId: '1', displayName: 'a' },
    })
    const spy = vi.fn()
    vi.stubGlobal('fetch', spy)

    await expect(ensureFreshToken()).resolves.toBe('tok')
    expect(spy).not.toHaveBeenCalled()
  })

  it('剩余寿命不足时先 refresh —— 否则长流会在中途过期而无法重放', async () => {
    setSession({
      accessToken: 'tok',
      expiresAt: Math.floor(Date.now() / 1000) + 30,
      user: { userId: '1', displayName: 'a' },
    })
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.resolve(okRefresh())),
    )

    await expect(ensureFreshToken()).resolves.toBe('new-token')
  })

  it('没有会话时直接走 refresh', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.resolve(okRefresh())),
    )

    await expect(ensureFreshToken()).resolves.toBe('new-token')
  })
})
```

（`ensureFreshToken` 要加进该文件顶部的 import。）

- [ ] **Step 4: 改 sse.ts**

在第 116 行的 `fetch` 调用处加 token，并把「建连」与「流中」两个阶段分开：

```ts
  // 建连阶段：401 可以安全地 refresh 并重试，因为还没有任何内容吐给用户。
  const open = async (token: string | null): Promise<Response> =>
    fetch(`${API_BASE_URL}/conversations/${conversationId}/messages`, {
      method: 'POST',
      headers: {
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        // ...原有的头保持不变
      },
      // ...原有的 body / signal 保持不变
    })

  // spec D7 方案 (i)：建连前先确保 token 活得过整段 run。中途过期是唯一
  // 无法重放的失败，提前换票比事后补救便宜得多。
  let response = await open(await ensureFreshToken())

  // 文件顶部需要补这一行 import：
  //   import { ensureFreshToken, refreshAccessToken } from '@/features/auth/token-store'

  if (response.status === 401) {
    const fresh = await refreshAccessToken()
    if (fresh) response = await open(fresh)
  }
```

读流的那一段（第 155 行 `response.body.getReader()` 之后）**不要加任何重试**。中途失败按现有的错误路径上报即可 —— 流已经吐出内容后重放会让用户看到答案从头重复一遍，这正是文件头注释弃用 `@microsoft/fetch-event-source` 的理由。

- [ ] **Step 5: 跑测试确认通过**

Run: `pnpm vitest run tests/auth-sse.test.ts tests/auth-token-store.test.ts tests/sse.test.ts`
Expected: PASS，含现有 sse 用例

注意：上面「建连时带上 Authorization 头」那个用例里的会话 `expiresAt` 是 1999999999（远未到期），所以 `ensureFreshToken` 不会额外发请求，`spy.mock.calls[0]` 仍然是建连那一次。

- [ ] **Step 6: 类型检查并 commit**

Run: `pnpm typecheck && pnpm lint && pnpm test`

```bash
git add src/api/sse.ts src/features/auth/token-store.ts \
        tests/auth-sse.test.ts tests/auth-token-store.test.ts
git commit -m "feat(sse): authenticate the stream and pre-refresh before a long run"
```

---

### Task 17: 登录页、AuthProvider、路由守卫与启动 bootstrap

**Files:**
- Create: `src/features/auth/AuthProvider.tsx`
- Create: `src/features/auth/LoginPage.tsx`
- Create: `src/routes/ProtectedRoute.tsx`
- Modify: `src/features/auth/index.ts`
- Modify: `src/routes/router.tsx`
- Modify: `src/main.tsx`（挂 `AuthProvider`）
- Test: `tests/auth-provider.test.tsx`（新建）
- Test: `tests/login-page.test.tsx`（新建）

**Interfaces:**
- Consumes: token-store 全部导出（Task 14）、Phase 3 的 `/api/auth/login` 与 `/api/auth/captcha`
- Produces:
  - `AuthProvider`：挂载时跑一次 bootstrap refresh，期间 `status === 'loading'`
  - `useAuth(): { status: 'loading' | 'authenticated' | 'anonymous'; user: CurrentUser | null; login(...): Promise<void>; logout(): Promise<void> }`
  - `useCurrentUser(): CurrentUser`（已认证区域内使用，`CurrentUser = { userId, displayName }` 形状不变）
  - `ProtectedRoute`：`loading` 显示占位，`anonymous` 重定向 `/login`
  - `/login` 路由

- [ ] **Step 1: 写失败测试**

创建 `tests/auth-provider.test.tsx`：

```tsx
import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { AuthProvider, useAuth } from '@/features/auth/AuthProvider'
import { setSession } from '@/features/auth/token-store'

afterEach(() => {
  setSession(null)
  vi.unstubAllGlobals()
})

function Probe() {
  const { status, user } = useAuth()
  return (
    <div>
      <span data-testid="status">{status}</span>
      <span data-testid="user">{user?.displayName ?? '-'}</span>
    </div>
  )
}

function stubRefresh(response: Response, delayMs = 0) {
  const spy = vi.fn(
    () =>
      new Promise<Response>((resolve) => {
        if (delayMs === 0) resolve(response)
        else setTimeout(() => resolve(response), delayMs)
      }),
  )
  vi.stubGlobal('fetch', spy)
  return spy
}

const OK = () =>
  new Response(
    JSON.stringify({
      access_token: 'tok',
      expires_at: 1999999999,
      user: { user_id: '10001', display_name: 'zhangsan' },
    }),
    { status: 200, headers: { 'Content-Type': 'application/json' } },
  )

describe('AuthProvider', () => {
  it('挂载时先跑一次 refresh，期间状态是 loading', async () => {
    stubRefresh(OK(), 20)

    render(
      <AuthProvider>
        <Probe />
      </AuthProvider>,
    )

    expect(screen.getByTestId('status').textContent).toBe('loading')
    await waitFor(() => expect(screen.getByTestId('status').textContent).toBe('authenticated'))
    expect(screen.getByTestId('user').textContent).toBe('zhangsan')
  })

  it('bootstrap refresh 失败后状态是 anonymous', async () => {
    stubRefresh(new Response('', { status: 401 }))

    render(
      <AuthProvider>
        <Probe />
      </AuthProvider>,
    )

    await waitFor(() => expect(screen.getByTestId('status').textContent).toBe('anonymous'))
  })

  it('只在挂载时 bootstrap 一次', async () => {
    const spy = stubRefresh(OK())

    const { rerender } = render(
      <AuthProvider>
        <Probe />
      </AuthProvider>,
    )
    await waitFor(() => expect(screen.getByTestId('status').textContent).toBe('authenticated'))

    rerender(
      <AuthProvider>
        <Probe />
      </AuthProvider>,
    )

    expect(spy).toHaveBeenCalledTimes(1)
  })

  it('token-store 被外部清空时（refresh 被拒）状态跟着变 anonymous', async () => {
    stubRefresh(OK())

    render(
      <AuthProvider>
        <Probe />
      </AuthProvider>,
    )
    await waitFor(() => expect(screen.getByTestId('status').textContent).toBe('authenticated'))

    setSession(null)

    await waitFor(() => expect(screen.getByTestId('status').textContent).toBe('anonymous'))
  })
})
```

创建 `tests/login-page.test.tsx`：

```tsx
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { LoginPage } from '@/features/auth/LoginPage'
import { setSession } from '@/features/auth/token-store'

afterEach(() => {
  setSession(null)
  vi.unstubAllGlobals()
})

function stubEndpoints(loginResponse: Response, loginDelayMs = 0) {
  const spy = vi.fn((url: string) => {
    if (String(url).includes('/auth/captcha')) {
      return Promise.resolve(
        new Response(JSON.stringify({ key: 'k', img: 'data:image/png;base64,xx' }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
    }
    // 延迟要发生在 handler 里。在 stubEndpoints 的实参上 await 一个
    // setTimeout 只会推迟 stub 本身，请求照样立刻返回。
    return loginDelayMs === 0
      ? Promise.resolve(loginResponse)
      : new Promise<Response>((resolve) => setTimeout(() => resolve(loginResponse), loginDelayMs))
  })
  vi.stubGlobal('fetch', spy)
  return spy
}

function loginOk() {
  return new Response(
    JSON.stringify({
      access_token: 'tok',
      expires_at: 1999999999,
      user: { user_id: '10001', display_name: 'zhangsan' },
    }),
    { status: 200, headers: { 'Content-Type': 'application/json' } },
  )
}

function loginRejected(code: string) {
  return new Response(JSON.stringify({ error: { code, message: code } }), {
    status: 401,
    headers: { 'Content-Type': 'application/json' },
  })
}

describe('LoginPage', () => {
  it('加载时取一次验证码', async () => {
    const spy = stubEndpoints(loginOk())

    render(<LoginPage onSuccess={() => {}} />)

    await waitFor(() => expect(screen.getByAltText(/验证码/)).toBeInTheDocument())
    expect(spy.mock.calls.some(([url]) => String(url).includes('/auth/captcha'))).toBe(true)
  })

  it('提交成功后写入会话并回调', async () => {
    stubEndpoints(loginOk())
    const onSuccess = vi.fn()

    render(<LoginPage onSuccess={onSuccess} />)
    await waitFor(() => expect(screen.getByAltText(/验证码/)).toBeInTheDocument())

    await userEvent.type(screen.getByLabelText(/账号/), 'zhangsan')
    await userEvent.type(screen.getByLabelText(/密码/), 'pw')
    await userEvent.type(screen.getByLabelText(/验证码/), 'abcd')
    await userEvent.click(screen.getByRole('button', { name: /登录/ }))

    await waitFor(() => expect(onSuccess).toHaveBeenCalledTimes(1))
  })

  it('账号或密码错误时显示对应文案，并刷新验证码', async () => {
    const spy = stubEndpoints(loginRejected('AUTH_INVALID_CREDENTIALS'))

    render(<LoginPage onSuccess={() => {}} />)
    await waitFor(() => expect(screen.getByAltText(/验证码/)).toBeInTheDocument())

    const captchaFetchesBefore = spy.mock.calls.filter(([url]) =>
      String(url).includes('/auth/captcha'),
    ).length

    await userEvent.type(screen.getByLabelText(/账号/), 'zhangsan')
    await userEvent.type(screen.getByLabelText(/密码/), 'bad')
    await userEvent.type(screen.getByLabelText(/验证码/), 'abcd')
    await userEvent.click(screen.getByRole('button', { name: /登录/ }))

    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/账号或密码/))

    // 验证码是一次性的：失败后必须换一张，否则用户第二次提交必然再失败
    const captchaFetchesAfter = spy.mock.calls.filter(([url]) =>
      String(url).includes('/auth/captcha'),
    ).length
    expect(captchaFetchesAfter).toBeGreaterThan(captchaFetchesBefore)
  })

  it('验证码过期和账号禁用各显示自己的文案', async () => {
    for (const [code, pattern] of [
      ['AUTH_CAPTCHA_EXPIRED', /验证码/],
      ['AUTH_ACCOUNT_DISABLED', /禁用/],
    ] as const) {
      stubEndpoints(loginRejected(code))
      const { unmount } = render(<LoginPage onSuccess={() => {}} />)
      await waitFor(() => expect(screen.getByAltText(/验证码/)).toBeInTheDocument())

      await userEvent.type(screen.getByLabelText(/账号/), 'a')
      await userEvent.type(screen.getByLabelText(/密码/), 'b')
      await userEvent.type(screen.getByLabelText(/验证码/), 'c')
      await userEvent.click(screen.getByRole('button', { name: /登录/ }))

      await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(pattern))
      unmount()
      vi.unstubAllGlobals()
    }
  })

  it('提交期间按钮禁用，避免重复提交', async () => {
    stubEndpoints(loginOk(), 50)

    render(<LoginPage onSuccess={() => {}} />)
    await waitFor(() => expect(screen.getByAltText(/验证码/)).toBeInTheDocument())

    await userEvent.type(screen.getByLabelText(/账号/), 'a')
    await userEvent.type(screen.getByLabelText(/密码/), 'b')
    await userEvent.type(screen.getByLabelText(/验证码/), 'c')

    const button = screen.getByRole('button', { name: /登录/ })
    await userEvent.click(button)

    expect(button).toBeDisabled()
  })
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `pnpm vitest run tests/auth-provider.test.tsx tests/login-page.test.tsx`
Expected: FAIL，无法解析两个新模块

- [ ] **Step 3: 实现 AuthProvider.tsx**

```tsx
/**
 * 把 token-store 的状态镜像进 React，并负责启动时那一次 bootstrap refresh。
 *
 * access token 存在内存里，刷新页面就没了 —— 所以挂载时必须先用 HttpOnly
 * cookie 里的 refresh token 换一个回来。这期间 status 是 'loading'，路由停在
 * 占位界面：这就是设计里那一次「几十毫秒白屏」的来源。
 */

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
  useSyncExternalStore,
} from 'react'
import type { ReactNode } from 'react'

import { request } from '@/api/client'

import {
  getSession,
  refreshAccessToken,
  setSession,
  subscribe,
} from './token-store'

export interface CurrentUser {
  userId: string
  displayName: string
}

export type AuthStatus = 'loading' | 'authenticated' | 'anonymous'

interface AuthValue {
  status: AuthStatus
  user: CurrentUser | null
  login: (input: {
    loginName: string
    password: string
    captcha: string
    key: string
  }) => Promise<void>
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthValue | null>(null)

interface LoginResponse {
  access_token: string
  expires_at: number
  user: { user_id: string; display_name: string }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const session = useSyncExternalStore(subscribe, getSession, getSession)
  const [bootstrapped, setBootstrapped] = useState(false)

  useEffect(() => {
    let cancelled = false
    void refreshAccessToken().finally(() => {
      // 组件已卸载就不要 setState —— 开发模式下 StrictMode 会挂载两次。
      if (!cancelled) setBootstrapped(true)
    })
    return () => {
      cancelled = true
    }
  }, [])

  const login = useCallback(
    async (input: { loginName: string; password: string; captcha: string; key: string }) => {
      const body = await request<LoginResponse>('/auth/login', {
        method: 'POST',
        body: {
          login_name: input.loginName,
          password: input.password,
          captcha: input.captcha,
          key: input.key,
        },
      })
      setSession({
        accessToken: body.access_token,
        expiresAt: body.expires_at,
        user: { userId: body.user.user_id, displayName: body.user.display_name },
      })
    },
    [],
  )

  const logout = useCallback(async () => {
    try {
      await request<void>('/auth/logout', { method: 'POST' })
    } finally {
      // 即使上游失败也要本地登出 —— 用户的意图是离开。
      setSession(null)
    }
  }, [])

  const status: AuthStatus = !bootstrapped
    ? 'loading'
    : session
      ? 'authenticated'
      : 'anonymous'

  return (
    <AuthContext.Provider value={{ status, user: session?.user ?? null, login, logout }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth(): AuthValue {
  const value = useContext(AuthContext)
  if (!value) throw new Error('useAuth 必须在 AuthProvider 内使用')
  return value
}
```

- [ ] **Step 4: 实现 index.ts**

```ts
/**
 * 身份入口。
 *
 * MVP 阶段这里返回的是硬编码的开发用户；现在它读 AuthProvider 的 context。
 * 只能在已认证区域（ProtectedRoute 之内）调用 —— 匿名状态下没有用户可返回，
 * 抛错比返回一个假用户安全：后者会让越权问题变成静默的数据错位。
 */

import { useAuth } from './AuthProvider'
import type { CurrentUser } from './AuthProvider'

export type { CurrentUser }
export { AuthProvider, useAuth } from './AuthProvider'
export { LoginPage } from './LoginPage'

export function useCurrentUser(): CurrentUser {
  const { user } = useAuth()
  if (!user) throw new Error('useCurrentUser 只能在已认证区域使用')
  return user
}
```

- [ ] **Step 5: 实现 LoginPage.tsx**

要点（具体 markup 跟随 `src/components` 现有的 shadcn/Tailwind 约定）：

- 三个受控输入，`<label>` 文案含「账号」「密码」「验证码」以便 `getByLabelText` 命中
- 验证码图片 `alt` 含「验证码」，点击可刷新
- 挂载时 `GET /api/auth/captcha` 取 `{key, img}`
- 提交调 `useAuth().login(...)`，成功后 `onSuccess()`
- 失败时捕获 `AppError`，用 `describeError()` 的文案渲染进 `role="alert"`，**并重新取一张验证码**（验证码是一次性的，服务端校验后即 `del`，不换就必然再失败）
- 提交期间按钮 `disabled`

```tsx
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  const submit = async (event: React.FormEvent) => {
    event.preventDefault()
    setSubmitting(true)
    setError(null)
    try {
      await login({ loginName, password, captcha, key })
      onSuccess()
    } catch (cause) {
      setError(describeError(cause))
      // 验证码一次性：服务端校验后已 del，不换新的话第二次提交必然再失败
      await loadCaptcha()
      setCaptcha('')
    } finally {
      setSubmitting(false)
    }
  }
```

- [ ] **Step 6: 实现 ProtectedRoute 与路由**

创建 `src/routes/ProtectedRoute.tsx`：

```tsx
import { Navigate, Outlet, useLocation } from 'react-router-dom'

import { useAuth } from '@/features/auth'

export function ProtectedRoute() {
  const { status } = useAuth()
  const location = useLocation()

  // bootstrap 还没跑完就渲染 Outlet 的话，子组件会用「匿名」的前提发请求，
  // 全部 401 —— 必须先等。
  if (status === 'loading') {
    return <div className="flex min-h-screen items-center justify-center text-sm text-muted-foreground">加载中…</div>
  }
  if (status === 'anonymous') {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />
  }

  return <Outlet />
}
```

修改 `src/routes/router.tsx`：

```tsx
export const router = createBrowserRouter([
  { path: '/login', element: <LoginRoute /> },
  {
    path: '/',
    element: <ProtectedRoute />,
    children: [
      {
        element: <AppLayout />,
        children: [
          { index: true, element: <Navigate to="/chat" replace /> },
          { path: 'chat', element: <ChatRoute /> },
          { path: 'chat/:conversationId', element: <ChatRoute /> },
          { path: '*', element: <NotFound /> },
        ],
      },
    ],
  },
])
```

`LoginRoute` 是一个薄壳。创建 `src/routes/LoginRoute.tsx`：

```tsx
import { Navigate, useLocation, useNavigate } from 'react-router-dom'

import { LoginPage, useAuth } from '@/features/auth'

export function LoginRoute() {
  const { status } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()
  const from = (location.state as { from?: string } | null)?.from

  // bootstrap 还没跑完就渲染登录表单，会让已登录用户看到一闪而过的登录页。
  if (status === 'loading') {
    return <div className="flex min-h-screen items-center justify-center text-sm text-muted-foreground">加载中…</div>
  }
  if (status === 'authenticated') {
    return <Navigate to={from ?? '/chat'} replace />
  }

  return <LoginPage onSuccess={() => navigate(from ?? '/chat', { replace: true })} />
}
```

`/login` 这条路由在 `ProtectedRoute` 之外，但它仍然需要 `useAuth()` —— 所以 `AuthProvider` 必须包在 `RouterProvider` **外面**，不能只包住受保护的那棵子树。

修改 `src/main.tsx`，把 `<RouterProvider>` 包在 `<AuthProvider>` 里（要在 `QueryClientProvider` 之内，因为 `request()` 可能被 Query 调用）。

- [ ] **Step 7: 跑测试确认通过**

Run: `pnpm vitest run tests/auth-provider.test.tsx tests/login-page.test.tsx -v`
Expected: PASS，10 个测试全绿

- [ ] **Step 8: 跑全量测试与检查**

Run: `pnpm test && pnpm typecheck && pnpm lint && pnpm build`
Expected: 全绿

- [ ] **Step 9: 手工端到端验证**

同时起四个服务（admin-api :8000、MCP :8081、copilot-api :8082、web :5173），然后：

1. 访问 `/chat` → 应重定向到 `/login`
2. 用一个真实运营账号登录 → 进入 `/chat`，侧边栏显示该账号的 `login_name`
3. 打开 DevTools → Application → Cookies：只有 `copilot_refresh`，`HttpOnly` 勾选，`Path=/api/auth`，**Domain 列是当前主机而不是 `.example.com`**
4. Local Storage / Session Storage 里**搜不到** access token
5. 问一个需要 deposits 权限的问题 → 正常返回
6. 把该账号换成没有 deposits 菜单的角色，重新登录 → 同样的问题应被拒且提示合理
7. 刷新页面 → 短暂「加载中…」后仍然是登录态
8. 在 admin-api 侧 `PUT managers/password` 改该账号密码 → copilot 这边下一次 refresh 应失败并跳回 `/login`

Expected：八条全部符合。第 3 条和第 4 条是 Review Focus 第 4 条与 D5 的人工复核。

- [ ] **Step 10: Commit**

```bash
git add src/features/auth/ src/routes/ src/main.tsx tests/
git commit -m "feat(auth): add login page, auth provider and route guard"
```

---

## 完成标志

- Phase 1：`./vendor/bin/phpunit` 全绿，现有登录测试一行未改；Task 6 Step 13 的五条 curl 符合预期。
- Phase 2：`go test ./...` 全绿，`make build` 成功，且 `grep -rn "admin.api.deposits.read\|X-Copilot-Scopes\|DefaultPermissions" internal/ cmd/ config/` 无结果。
- Phase 3：`uv run pytest` 全绿；`Set-Cookie` 不含 `Domain=`。
- Phase 4：`pnpm test && pnpm typecheck && pnpm build` 全绿；Task 17 Step 9 的八条人工验证通过。

全部完成后，spec §11 的四项演进路线（中途续期、路由级白名单、服务身份体系、SSO 入口）仍然未实现 —— 这是有意的，其中**路由级白名单在开放写操作之前必须先做**。
