# AI Copilot 技术实现设计

> 文档名称：`AI Copilot 技术实现设计`  
> 文档版本：`V2.0`  
> 技术负责人：`待定`  
> 参与人员：`后端、前端、架构、测试、安全、产品`  
> 创建日期：`2026-09-15`  
> 最后更新：`2026-09-18`  
> 当前状态：`MVP 技术基线`

---

## 1. 文档说明

### 1.1 文档目的

本文档描述 AI Copilot MVP 的技术实现方案，覆盖：

- 系统边界；
- 三个核心项目的职责；
- 技术栈；
- Agent Runtime；
- MCP Server；
- MCP Tool Contract；
- 身份与权限；
- 会话与上下文；
- 业务数据访问；
- Knowledge；
- 后台导航；
- Source Code Knowledge；
- 回答证据；
- Streaming；
- 错误模型；
- 审计；
- 安全；
- 可观测性；
- 测试与 Agent Eval；
- 部署和演进策略。

产品目标、用户场景和功能范围以 `ai-copilot-prd.md` 为准。

具体技术选型依据见 `ai-copilot-technology-selection.md`。

### 1.2 已确认技术决策

MVP 正式采用：

```text
ai-copilot-web
  React + TypeScript + Vite
  shadcn/ui + Tailwind CSS

ai-copilot-api
  Python + FastAPI
  OpenAI Agents SDK

internal-mcp-server
  Go
  Official MCP Go SDK

Agent
  Single Agent

MCP Transport
  Streamable HTTP

Knowledge
  Git + YAML / Markdown

Business Data
  website-api / admin-api

Persistence
  MySQL

Observability
  OpenTelemetry
```

MVP 明确不采用：

- LangGraph；
- Multi-Agent；
- RAG / Vector DB；
- LLM 直连数据库；
- 任意 SQL；
- 任意 Shell；
- 独立 `ai-agent-service`；
- WebSocket 作为默认聊天通道；
- AI 自动写入业务数据。

### 1.3 核心技术原则

1. LLM 不直接访问业务数据库；
2. AI 不绕过 `website-api` / `admin-api`；
3. AI 不扩大当前运营用户权限；
4. `internal-mcp-server` 是统一 AI Business Gateway，不是新的核心业务服务；
5. `ai-copilot-api` 是 Agent Runtime 和会话编排层；
6. Agent 只能调用注册、授权、可审计的 MCP Tools；
7. MVP 只提供只读业务能力；
8. 核心业务规则仍由现有业务系统执行；
9. MCP 不复制业务规则，只做 AI-friendly capability adaptation；
10. Knowledge 首期采用结构化、Git 版本化维护；
11. Source Code 只作为当前实现行为的核实来源，不替代业务语义；
12. 事实数据、业务规则、系统当前实现和模型解释必须可以区分；
13. Tool Failure 与 No Data 必须严格区分；
14. 敏感数据遵循最小暴露原则；
15. 所有关键链路必须可追踪、可审计、可回放定位。

---

## 2. 系统与服务边界

### 2.1 现有和新增项目

```text
website-frontend
website-api

admin-frontend
admin-api

ai-copilot-web
ai-copilot-api

internal-mcp-server
```

其中 AI Copilot 新增三个项目：

```text
ai-copilot-web
ai-copilot-api
internal-mcp-server
```

### 2.2 服务职责总览

| 项目 | 主要职责 | 不负责 |
|---|---|---|
| `ai-copilot-web` | Chat UI、结果展示、会话 UI、后台深链、反馈 | Agent 推理、业务规则执行 |
| `ai-copilot-api` | 会话、Agent Runtime、LLM、MCP Client、上下文、Evidence、Streaming | 业务 DB、核心业务规则 |
| `internal-mcp-server` | Tool Registry、权限、审计、API Adapter、Knowledge、Navigation、Source | Agent 推理、会话 Memory、核心业务逻辑 |
| `website-api` | 网站业务能力和业务数据 | AI 编排 |
| `admin-api` | 中后台业务能力、权限判断和业务数据 | AI 编排 |

### 2.3 `ai-copilot-web`

负责：

- 新建会话；
- 历史会话；
- 用户消息输入；
- Assistant Streaming 展示；
- Markdown / 文本；
- 简单表格；
- 有序 / 无序列表；
- Key-Value；
- Timeline；
- 后台页面链接；
- Tool 执行状态的轻量展示；
- 有帮助 / 无帮助反馈；
- 错误和权限提示。

不向前端暴露：

- Chain-of-Thought；
- 隐式推理步骤；
- 内部 Prompt；
- 内部系统凭证；
- 未过滤 Tool 原始敏感数据。

### 2.4 `ai-copilot-api`

定位：

> AI Copilot Application Backend + Agent Runtime。

负责：

- 用户身份接入；
- 会话管理；
- Message 持久化；
- OpenAI 模型调用；
- Agent Instructions；
- Agent Context；
- OpenAI Agents SDK Runtime；
- MCP Client；
- Tool allowlist / runtime policy；
- 多步 Tool 调用；
- Evidence 聚合；
- Final Answer 生成；
- Streaming；
- 用户反馈；
- Prompt / Model 配置；
- Agent Run 审计；
- Agent Eval 接入；
- 可观测性。

不负责：

- 生产业务数据库读取；
- 业务规则重新实现；
- 任意内部 API 访问；
- 源码执行；
- 任意 Shell；
- 权限绕过。

### 2.5 `internal-mcp-server`

定位：

> 公司内部统一 AI Business Gateway。

负责：

- MCP Server；
- Tool Registry；
- Tool discovery；
- Tool Input / Output Schema；
- Service Authentication；
- Delegated User Identity 验证；
- Tool Authorization；
- 请求参数校验；
- `admin-api` / `website-api` adapters；
- Timeout；
- Rate Limit；
- Error normalization；
- Sensitive data filtering；
- Tool Audit；
- Knowledge Tools；
- Navigation Tools；
- Source Code Tools。

不负责：

- Agent Planning；
- Conversation Memory；
- Answer Generation；
- 模型调用；
- 将现有业务规则复制到 MCP；
- 绕过下游 API 权限。

---

## 3. 总体架构

```text
┌──────────────────────────────────────┐
│ ai-copilot-web                       │
│ React / TypeScript / Vite            │
│ shadcn/ui / Tailwind CSS             │
│                                      │
│ Chat / Table / Timeline / Deep Link  │
└──────────────────┬───────────────────┘
                   │
                   │ HTTPS
                   │ HTTP Streaming
                   ▼
┌──────────────────────────────────────────────┐
│ ai-copilot-api                               │
│ Python / FastAPI                             │
│                                              │
│ OpenAI Agents SDK                            │
│ ├── Operations Copilot Agent                 │
│ ├── Instructions / Policy                    │
│ ├── Conversation Context                     │
│ ├── Guardrails                               │
│ ├── MCP Client                               │
│ ├── Evidence Collector                       │
│ ├── Response Streaming                       │
│ └── Agent Audit / Trace                      │
└──────────────────┬───────────────────────────┘
                   │
                   │ MCP / Streamable HTTP
                   │ Delegated User Identity
                   ▼
┌──────────────────────────────────────────────┐
│ internal-mcp-server                          │
│ Go / Official MCP Go SDK                     │
│                                              │
│ AuthN / AuthZ / Audit / Rate Limit           │
│                                              │
│ ├── Data Tools                               │
│ ├── Knowledge Tools                          │
│ ├── Navigation Tools                         │
│ └── Source Code Tools                        │
└──────────────┬───────────────┬───────────────┘
               │               │
               │ HTTPS         │ Read Only
               ▼               ▼
      ┌────────────────┐   ┌──────────────────┐
      │ website-api    │   │ Knowledge / Git  │
      │ admin-api      │   │ Repositories     │
      └───────┬────────┘   └──────────────────┘
              │
              ▼
         Business / DB
```

### 3.1 请求主链路

```text
User
  -> ai-copilot-web
  -> ai-copilot-api
  -> Operations Copilot Agent
  -> MCP Tool
  -> internal-mcp-server
  -> admin-api / website-api / knowledge / source
  -> Tool Result
  -> Agent Synthesis
  -> Streaming Answer
  -> User
```

---

## 4. Web 技术设计

### 4.1 技术栈

```text
React
TypeScript
Vite
React Router
TanStack Query
shadcn/ui
Tailwind CSS
pnpm
```

### 4.2 UI 方案

公司目前没有 Design System，因此 MVP 使用：

```text
shadcn/ui
+
Tailwind CSS
```

原则：

- 不在 MVP 前先建设完整 Design System；
- 对 shadcn/ui 组件做项目内持有和修改；
- 建立最小 Design Tokens；
- 保持 AI Copilot 页面风格统一；
- 后续从已验证组件中逐步抽取公司级 Design System。

建议最小 Token：

```text
color
font size
font weight
spacing
radius
shadow
z-index
```

### 4.3 前端组件分层

```text
src/
├── components/
│   ├── ui/                 # shadcn/ui 基础组件
│   └── copilot/            # Copilot 业务组件
│       ├── message/
│       ├── composer/
│       ├── result/
│       ├── table/
│       ├── timeline/
│       ├── evidence/
│       └── navigation/
│
├── features/
│   ├── conversation/
│   ├── feedback/
│   └── auth/
│
├── api/
├── routes/
├── hooks/
└── lib/
```

### 4.4 消息内容能力

MVP Assistant 内容支持：

- Text；
- Paragraph；
- Heading；
- Ordered List；
- Unordered List；
- Simple Table；
- Inline Code；
- Code Block（仅用于解释，不执行）；
- Link；
- Business Data Result；
- Navigation Link；
- Error / Warning。

不支持任意 HTML。

Markdown 渲染需要做 XSS 防护和 URL scheme 过滤。

### 4.5 状态管理

```text
TanStack Query
  -> Server State

React State
  -> Local UI State

Zustand
  -> 仅在确有跨页面客户端状态时引入
```

MVP 不使用 Redux。

### 4.6 Chat Streaming

前端通过 `fetch` 接收 POST 请求的 Streaming Response。

建议事件：

```text
message.started
message.delta
message.completed

tool.started
tool.completed
tool.failed

result.data
result.navigation
result.evidence

error
```

说明：Web 与 API 的 Streaming 可以使用 SSE event framing，但这与 MCP 的旧式 HTTP+SSE transport 无关。

### 4.7 新建会话

点击“新建会话”：

```text
1. 前端生成新页面状态
2. 清空当前 message list
3. 输入框回到新会话初始位置
4. 用户首次发送消息时再创建服务端 conversation
```

这样可避免创建大量空 conversation。

---

## 5. `ai-copilot-api` 技术设计

### 5.1 技术栈

```text
Python
FastAPI
OpenAI Agents SDK
Pydantic
SQLAlchemy 2.x
Alembic
uv
pytest
Ruff
OpenTelemetry
```

### 5.2 Application Layer

FastAPI 只承担：

- Authentication integration；
- HTTP API；
- Request validation；
- Streaming response；
- Conversation API；
- Feedback API；
- Health / readiness；
- Observability hooks。

Agent Runtime 独立放在应用内部模块中，不把 Agent 逻辑写进 Route Handler。

### 5.3 推荐模块结构

```text
ai-copilot-api/
├── app/
│   ├── main.py
│   │
│   ├── api/
│   │   ├── conversations.py
│   │   ├── messages.py
│   │   ├── feedback.py
│   │   └── health.py
│   │
│   ├── auth/
│   │   ├── dependency.py
│   │   └── identity.py
│   │
│   ├── conversation/
│   │   ├── models.py
│   │   ├── repository.py
│   │   └── service.py
│   │
│   ├── agent/
│   │   ├── runtime.py
│   │   ├── agent.py
│   │   ├── instructions.py
│   │   ├── context.py
│   │   ├── policy.py
│   │   ├── guardrails.py
│   │   ├── evidence.py
│   │   └── response.py
│   │
│   ├── mcp/
│   │   ├── client.py
│   │   ├── auth.py
│   │   └── errors.py
│   │
│   ├── model/
│   │   ├── provider.py
│   │   └── config.py
│   │
│   ├── audit/
│   ├── observability/
│   ├── db/
│   └── config/
│
├── migrations/
├── tests/
├── evals/
├── pyproject.toml
└── uv.lock
```

MVP 不预设：

```text
planner.py
executor.py
reasoner.py
supervisor.py
```

这些抽象只有在真实复杂度出现后再引入。

---

## 6. Agent Runtime

### 6.1 Agent 定位

MVP 只有一个主要 Agent：

```text
Operations Copilot Agent
```

Agent 的职责：

- 理解用户运营问题；
- 判断是否需要 Tool；
- 选择最小必要 Tool；
- 生成 Tool 参数；
- 调用一个或多个 Tool；
- 根据 Tool Result 决定是否继续查询；
- 区分事实、规则、实现和推断；
- 组织 Evidence；
- 生成运营人员可理解的最终回答。

### 6.2 Runtime 实现

使用 OpenAI Agents SDK 管理：

- Agent loop；
- Tool calls；
- MCP integration；
- Sessions / context integration；
- Guardrails；
- Agent tracing。

产品自行实现：

- System / Agent instructions；
- User context；
- Tool policy；
- Evidence model；
- Permission context；
- Response contract；
- Business safety rules；
- Eval dataset。

### 6.3 为什么不自研 Planner / Executor

MVP 的典型流程：

```text
Question
  -> Model
  -> Tool Call
  -> Tool Result
  -> Model
  -> Optional Tool Call
  -> Final Answer
```

不需要额外建设：

```text
Intent Classifier Service
Planner Service
Executor Service
Reasoning Service
```

避免出现“在验证业务价值前先开发 Agent Framework”的情况。

### 6.4 Agent 约束

Agent 必须遵循：

- 只能使用已注册 MCP Tools；
- 不直接访问生产 DB；
- 不构造任意 SQL；
- 不执行 Shell；
- 不执行 Source Code；
- 不访问任意 URL；
- 不绕过 Tool 权限；
- Tool Error 不得解释为无数据；
- Tool 未返回的信息不得包装成事实；
- 查询尽量遵循最小必要原则；
- 最终回答进行敏感信息检查；
- 重要结论尽可能关联 Evidence。

### 6.5 Runtime Budget

Runtime 需要支持配置：

```text
max_turns
max_tool_calls
agent_timeout
tool_timeout
max_tool_result_size
max_context_size
model
model_parameters
```

具体数值通过压测和 Eval 确定。

### 6.6 Model 配置

模型配置不得硬编码在业务逻辑中。

建议：

```text
model profile:
  default
  fast
  complex
```

MVP 可以只有 `default`，但保留配置结构。

模型升级必须经过 Eval，不因为新模型发布自动切换生产环境。

---

## 7. Conversation 与 Context

### 7.1 Conversation Source of Truth

AI Copilot 自身数据库保存：

- conversation；
- messages；
- user feedback；
- agent run reference；
- model / prompt version reference。

`ai-copilot-api` 是 conversation source of truth。

不要同时维护两套独立长期 Memory：

```text
Application Conversation DB
+
Another Hidden Agent Memory DB
```

### 7.2 Agents SDK Session

Agents SDK Session 只作为 Runtime 的上下文机制使用。

如果使用 SDK Session persistence，应通过同一 Conversation Storage 或 Adapter 与应用会话一致，避免数据分叉。

### 7.3 Context 构成

每次 Agent Run 的上下文可包含：

```text
Authenticated User
Current Conversation
Recent Messages / Summary
Current Business Entity Context
Permission Context
Locale / Timezone
Prompt Version
Model Profile
```

### 7.4 Context Compression

当会话较长时：

```text
recent messages
+
conversation summary
+
important structured facts
```

不要无限把完整历史消息发送给模型。

### 7.5 Entity Context

可维护当前对话已确认实体，例如：

```text
user_id: 123456
withdrawal_id: WD001
account_id: ...
```

新的用户明确修改实体后，必须更新上下文，防止跨用户数据串联。

---

## 8. MCP 设计

### 8.1 MCP 职责

MCP 将内部能力转换为稳定、可授权、可审计的 AI Tool Contract：

```text
Internal Business Capability
        ↓
MCP Tool Contract
        ↓
Agent
```

### 8.2 技术实现

```text
Go
Official MCP Go SDK
Streamable HTTP
```

MCP endpoint 通过内部网络暴露，只允许授权服务调用。

### 8.3 状态模型

`internal-mcp-server` 应尽量保持 stateless。

Conversation / Agent State：

```text
ai-copilot-api
```

Tool Execution State：

```text
request scoped
```

### 8.4 Tool 分类

#### Data Tools

```text
users.get
users.search

wallet.get_balance

deposits.search
deposits.get

withdrawals.search
withdrawals.get

orders.search
trades.search
positions.get

kyc.get_status

ib.get_account
ib.commissions.search
```

#### Knowledge Tools

```text
concepts.search
concepts.get

rules.search
rules.get

sop.search
sop.get
```

#### Navigation Tools

```text
admin_console.find_page
admin_console.get_page
```

#### Source Code Tools

```text
source.search
source.read
source.find_symbol
```

MVP 暂不需要复杂代码语义分析平台。

---

## 9. MCP Tool Contract

### 9.1 命名原则

按照业务能力命名：

推荐：

```text
withdrawals.search
wallet.get_balance
accounts.get_status
```

禁止采用数据库实现命名：

```text
query_withdrawal_table
select_wallet_balance
get_user_table_row
```

### 9.2 Tool Metadata

每个 Tool 必须定义：

```text
name
description
version
input_schema
output_schema
required_permissions
sensitivity_level
timeout
audit_policy
owner
```

### 9.3 Description 设计

Tool Description 需要明确：

- 能解决什么问题；
- 不能解决什么问题；
- 关键参数含义；
- 数据范围；
- 返回值语义；
- 典型使用条件。

Tool Description 是 Agent Tool Selection 的重要输入，需要纳入 Code Review。

### 9.4 Input Schema

要求：

- 参数尽量结构化；
- 不允许 arbitrary SQL；
- 不允许 arbitrary filter expression；
- 不允许 arbitrary URL；
- 时间范围显式；
- 分页显式；
- enum 使用稳定业务语义；
- 参数设置上限。

例如：

```json
{
  "user_id": "123456",
  "status": ["pending", "reviewing"],
  "limit": 20
}
```

### 9.5 Output Schema

Tool Output 不直接照搬下游 API 全量 Response。

应：

- 只返回 Agent 需要字段；
- 清除内部实现噪音；
- 保留稳定业务语义；
- 标记数据时间和来源；
- 明确分页；
- 明确是否截断。

### 9.6 Tool Result Envelope

建议统一：

```json
{
  "status": "SUCCESS",
  "data": {},
  "source": {
    "service": "admin-api",
    "resource": "withdrawal",
    "observed_at": "..."
  },
  "pagination": null,
  "warnings": []
}
```

Agent 不需要看到下游服务的内部堆栈和异常详情。

---

## 10. 权限模型

### 10.1 总体模型

采用：

> Service Authentication + Delegated User Identity + Tool Authorization + Downstream Authorization

链路：

```text
User
  ↓
ai-copilot-api
  ↓ Service Identity + Delegated User Identity
internal-mcp-server
  ↓ Tool Authorization
admin-api / website-api
  ↓ Downstream Authorization
Business Data
```

### 10.2 Service Authentication

只有被授权服务可以访问 MCP：

```text
service: ai-copilot-api
        ↓
internal-mcp-server
```

具体 token / mTLS 机制复用公司的内部服务身份体系。

如果当前没有统一体系，MVP 使用短生命周期、可轮换的内部 JWT，不使用永久静态 token 作为最终方案。

### 10.3 Delegated User Identity

MCP 请求必须携带真实运营用户身份。

禁止：

```text
User
  -> AI
  -> MCP Super Admin
  -> All Data
```

必须：

```text
User A
  -> Copilot
  -> Delegated User A
  -> MCP
  -> Admin API
```

建议 Delegated Identity 至少包含：

```text
user_id
actor_service
session / request id
issued_at
expires_at
audience
```

### 10.4 Tool Authorization

例如：

```yaml
tool: withdrawals.search
required_permissions:
  - withdrawal.read
```

MCP 执行前先做 Tool 级授权。

### 10.5 Downstream Authorization

最终业务数据权限仍由：

```text
admin-api
website-api
```

判断。

MCP Tool Authorization 是额外防线，不取代业务 API 权限。

### 10.6 字段级权限和数据范围

现有后台如果支持：

- field-level permissions；
- data scope；
- department scope；
- PII masking；
- role scope；

AI Copilot 必须保持一致。

优先使用下游 API 已过滤后的 Response。

---

## 11. Data Tools 与业务 API

### 11.1 数据访问原则

禁止：

```text
LLM
  -> SQL
  -> Production DB
```

必须：

```text
Agent
  -> MCP Tool
  -> admin-api / website-api
  -> Application / Domain Logic
  -> DB
```

### 11.2 为什么不直连数据库

- 复用现有权限；
- 复用业务规则；
- 避免 Schema 泄露；
- 避免 LLM 构造任意 SQL；
- 避免 DB Schema 变化直接破坏 AI；
- 便于审计；
- 便于限流；
- Tool Contract 比数据库表更稳定。

### 11.3 API Adapter

MCP 内部为下游 API 建立明确 Adapter：

```text
internal/adapters/adminapi
internal/adapters/websiteapi
```

Adapter 负责：

- HTTP 调用；
- timeout；
- retry policy；
- error mapping；
- delegated identity propagation；
- response decoding。

业务语义转换在 Tool 层完成，HTTP 细节不泄露给 Agent。

---

## 12. Knowledge 设计

### 12.1 MVP 定位

MVP 不建设：

- Vector DB；
- RAG Pipeline；
- Knowledge Graph；
- Ontology Platform。

采用：

> Structured Knowledge + Deterministic Search + Source Lookup

### 12.2 目录

```text
knowledge/
├── concepts/
├── rules/
├── navigation/
└── sop/
```

### 12.3 Concepts

示例：

```yaml
id: withdrawable_balance
name: 可提现余额
aliases:
  - 可提余额
  - 可出金额
  - withdrawable amount

description: >
  用户当前允许提交提现申请的最大金额。

domain: wallet

related_tools:
  - wallet.get_balance
  - withdrawals.search
  - positions.get

source_code:
  repository: admin-api
  symbol: CalculateWithdrawableBalance

admin_pages:
  - user_wallet_detail

owner:
  - wallet-team
```

### 12.4 Rules

示例：

```yaml
id: withdrawal.pending_limit
name: 未完成提现限制

description: >
  用户存在指定未完成状态的提现时，
  是否允许再次提交提现由当前业务规则决定。

related_tools:
  - withdrawals.search

source_code:
  repository: admin-api
  symbol: ValidateWithdrawalRequest

last_verified_commit: "..."
owner:
  - payment-team
```

注意：如果规则本质由代码和配置动态决定，不应在 YAML 中复制一份可能失效的完整业务逻辑。Knowledge 主要描述业务语义、适用范围和来源。

### 12.5 SOP

SOP 使用 Markdown：

```text
knowledge/sop/*.md
```

SOP 需要：

- owner；
- last reviewed date；
- applicable scope；
- related pages；
- related tools。

### 12.6 Knowledge 搜索

MVP 支持：

- ID lookup；
- alias lookup；
- keyword search；
- basic ranking；
- domain filtering。

未来如召回率成为主要瓶颈，可在 Tool 内部升级为 Hybrid / Vector Search，而不修改 Agent Tool Contract。

---

## 13. 后台导航

### 13.1 原则

Navigation 不依赖人工维护的独立说明文档作为唯一来源。

目标是建立与 `admin-frontend` 菜单、路由和权限尽量一致的 Navigation Manifest。

### 13.2 Manifest 示例

```yaml
id: user_login_history

title: 用户登录记录

description: >
  查看用户历史登录时间、IP 和设备信息。

keywords:
  - 登录记录
  - login
  - IP
  - device

route: /users/:user_id/login-history

required_permissions:
  - user.login_history.read

capabilities:
  - 查询用户登录记录
  - 查询登录设备
```

### 13.3 Tools

```text
admin_console.find_page
admin_console.get_page
```

命名与现有的 `admin_api.*` 同构，且语义分得开：`admin_api` 调后台接口取数据，
`admin_console` 找后台页面。

返回：

- title；
- description；
- menu_path；
- route；
- url（服务端拼好的绝对链接）与 deep_link_template；
- requires_entity（该页需要哪些动态段才拼得出链接）；
- capabilities；
- lookup_hint（进了页面之后怎么找到目标记录）；
- access。

**access 而不是 required_permissions。** manifest 里能拿到的是 `menu_key`，即前端的
**菜单位置码**（`finance.cash.deposit`），它和 MCP 的权限码（后端资源+动作，
`admin.api.deposits.read`）是两套词表，不能靠字符串变换互推。更关键的是即便映射出来
也没法用来过滤：MCP 下游是一套共享的静态 cookie，拿不到当前运营用户真实的 `menu_keys[]`。
把 `menu_key` 装进一个叫 `required_permissions` 的字段，会被模型原样当成权限码转述给运营。
所以返回：

```json
"access": {
  "menu_key": "finance.cash.deposit",
  "checked": false,
  "note": "能否打开取决于该运营人员在后台的菜单权限；本工具不做权限过滤。"
}
```

### 13.3.1 检索与缺口

检索按 §12.6 做确定性匹配，不引入向量检索。没有中文分词器，所以走字符 bigram +
子串包含，并对 gram 覆盖度封顶，避免长句用 bigram 把整个 manifest 刷一遍。

`find_page` 命中为空时返回的是 **SUCCESS + 空 matches + 相关 known_gaps**，不是错误、
也不发 warning —— 「后台压根没有 KYC 页面」是一个有信息量的正确答案，和「查询失败」不是一回事。

### 13.4 Deep Link

最终链接由服务端根据已知业务实体安全生成。

不要允许模型直接自由拼接任意后台 URL。

具体做法：MCP 侧配置 `admin_console.base_url`（必填、无默认值），manifest 只存相对
`route`，不掺部署信息。`deep_link_template` 恒有；`url` 只在该页没有未解析的动态段时给出 ——
全站 54 页里 50 页如此。其余 4 页返回空 `url` 加 `requires_entity`，由 `admin_console.get_page`
接收实体值后**在服务端**替换。实体值限定为字母、数字、连字符和下划线并经 `PathEscape`，
这是挡住用户在聊天框里输入 `../`、`%2f`、`?`、`#` 的闸门。

任何能产出可导航链接的 tool，都在 envelope 的 `data` 里放一个顶层 `navigation`
数组（`{label, url, description}`）。Agent 层据此产出 `result.navigation` 事件，
不按工具名分支。

---

## 14. Source Code Knowledge

### 14.1 使用场景

业务文档可能滞后，而生产实际行为最终由当前代码和配置执行。

因此 Source Code Tools 用于：

- 核实当前实现；
- 查找规则实现入口；
- 查找字段计算逻辑；
- 确认导航路由；
- 辅助定位文档和系统实现不一致。

### 14.2 MVP Tools

```text
source.search
source.read
source.find_symbol
```

### 14.3 允许仓库

按 allowlist 配置，例如：

```text
website-api
admin-api
website-frontend
admin-frontend
```

### 14.4 安全边界

允许：

- Search；
- Read；
- Symbol lookup。

禁止：

- execute source code；
- arbitrary shell；
- modify repository；
- git push；
- read arbitrary server filesystem；
- secret file access。

### 14.5 Path Policy

示例：

```yaml
allow_paths:
  - internal/**
  - app/**
  - src/**

deny_paths:
  - .env
  - "**/*.pem"
  - "**/*.key"
  - credentials/**
  - secrets/**
```

### 14.6 Source Result

返回至少包含：

```text
repository
branch
commit
path
symbol
line_range
content
```

### 14.7 代码与业务语义的关系

```text
Source Code
  = 当前系统实现行为

Business Knowledge
  = 业务含义 / 设计意图 / SOP
```

如果两者冲突：

- 不自动认定其中一方绝对正确；
- 回答中明确说明“当前实现”和“业务规则文档”不一致；
- 必要时提示人工确认。

---

## 15. Source of Truth 与 Evidence

### 15.1 信息优先级

不是简单的“一条来源覆盖所有其他来源”，而是按问题类型判断：

#### Runtime Business Fact

例如：

```text
当前余额
当前提现状态
当前 KYC 状态
```

Source of Truth：

```text
admin-api / website-api runtime data
```

#### Current Executable Behavior

例如：

```text
当前代码如何计算某字段
当前代码有哪些状态判断
```

Source：

```text
current deployed code / source reference
```

#### Business Meaning / Intent

例如：

```text
可提现余额的业务含义
SOP 应如何处理
```

Source：

```text
structured knowledge / approved SOP
```

### 15.2 Evidence Model

Agent 最终回答前收集 Evidence：

```text
Evidence
├── data
├── rule
├── source_code
├── navigation
└── limitation
```

Evidence 需要包含：

```text
type
source
reference
timestamp / version
summary
```

### 15.3 Final Answer

复杂问题优先输出：

```text
1. 结论
2. 关键数据
3. 原因解释
4. 规则 / 实现依据
5. 后台入口
6. 不确定性 / 限制
```

不要求所有简单问题机械套用固定模板。

---

## 16. Error Model

### 16.1 MCP Error Codes

统一：

```text
SUCCESS
NOT_FOUND
PERMISSION_DENIED
INVALID_ARGUMENT
UNSUPPORTED
DOWNSTREAM_ERROR
TIMEOUT
RATE_LIMITED
```

必要时增加内部错误子码，但 Agent 只消费稳定的高层错误语义。

### 16.2 No Data vs Failure

必须严格区分：

```text
SUCCESS + empty result
```

和：

```text
DOWNSTREAM_ERROR
TIMEOUT
PERMISSION_DENIED
```

Agent 不得把查询失败表述成：

> 没有相关记录。

### 16.3 Partial Failure

综合问题中如果：

```text
Tool A = success
Tool B = timeout
Tool C = success
```

Agent 可以给出当前已确认的信息，但必须明确：

- 哪部分已确认；
- 哪部分未能查询；
- 结论是否因此受到限制。

---

## 17. Guardrails 与安全

### 17.1 输入边界

用户输入不能改变：

- System policy；
- Tool permission；
- Source allowlist；
- Sensitive data policy；
- Write prohibition。

### 17.2 Prompt Injection

以下全部视为不可信数据：

- User content；
- Business data；
- SOP；
- Knowledge files；
- Source code；
- Source code comments；
- API response text。

即使其中出现：

```text
ignore previous instructions
```

也不能改变 Agent policy。

### 17.3 Tool Guardrails

MCP Tool 调用前检查：

- Tool 是否在 allowlist；
- 参数是否符合 Schema；
- 用户是否具备权限；
- 查询范围是否超过限制；
- 是否包含高风险参数；
- 是否属于当前只读能力。

### 17.4 Output Guardrails

最终输出前检查：

- PII；
- KYC；
- 银行信息；
- 联系方式；
- 身份证件；
- 风控内部标签；
- 内部备注；
- Secret；
- 不允许展示的 Source 内容。

### 17.5 Secrets

Secrets 只能通过 Secret Manager / Deployment Environment 注入。

禁止：

- 写入 Prompt；
- 写入 Knowledge；
- 返回给 Tool Result；
- 写入前端；
- 写入普通日志。

---

## 18. Audit

### 18.1 Agent Audit

记录：

```text
conversation_id
message_id
agent_run_id
user_id
model_profile
model_version
prompt_version
selected_tools
tool_call_ids
status
latency
token_usage
cost metadata
final_response reference
created_at
```

不建议把未经过滤的全部模型内部输入输出永久保存。

保留策略根据公司安全和合规要求配置。

### 18.2 Tool Audit

记录：

```text
trace_id
conversation_id
agent_run_id
tool_call_id
user_id
caller_service
tool_name
tool_version
resource identifier
permission result
downstream service
status
latency
timestamp
```

### 18.3 Sensitive Audit Data

Audit 默认记录 metadata，不记录不必要的大量业务 payload。

确需记录 payload 时：

- 做字段级脱敏；
- 限定访问权限；
- 设置 retention；
- 支持审计查询。

---

## 19. Observability

### 19.1 OpenTelemetry

跨服务统一使用 OpenTelemetry：

```text
Browser
  -> ai-copilot-api
    -> Agent Run
      -> MCP Call
        -> internal-mcp-server
          -> downstream API
```

### 19.2 Correlation IDs

贯穿：

```text
trace_id
conversation_id
message_id
agent_run_id
tool_call_id
```

### 19.3 Agent SDK Tracing

OpenAI Agents SDK 自带 Tracing 用于：

- Agent Run 调试；
- Tool Call 调试；
- Guardrail 调试；
- Agent 行为分析。

它不替代公司内部 OpenTelemetry。

定位：

```text
Agents SDK Trace
  -> Agent-level observability

OpenTelemetry
  -> System-level observability
```

### 19.4 Agent Metrics

```text
requests
success_rate
answer_latency
model_latency
tool_calls_per_run
tool_error_rate
tokens
cost
unresolved_rate
max_turn_reached_rate
```

### 19.5 MCP Metrics

```text
tool_qps
tool_success_rate
tool_p95_latency
tool_p99_latency
downstream_error_rate
permission_denied_rate
rate_limit_rate
timeout_rate
```

### 19.6 Product Metrics

```text
helpful_rate
no_help_rate
navigation_click_rate
unresolved_questions
top_failed_tools
top_missing_knowledge
repeat_question_rate
```

---

## 20. Database 设计边界

### 20.1 AI Copilot 自有数据

MySQL 可保存：

```text
conversation
message
feedback
agent_run
prompt_version
model_profile
```

Audit 可根据现有公司审计平台决定独立存储或统一日志平台。

### 20.2 不复制业务主数据

AI Copilot DB 不保存业务系统主数据副本，例如：

```text
user
withdrawal
deposit
trade
position
kyc
```

业务数据实时通过 Tool 查询。

### 20.3 Message 内容

Message 数据需要支持：

```text
role
content
content_type
status
created_at
```

结构化结果建议通过单独 JSON payload 保存，避免强行把所有 UI 数据塞入 Markdown。

---

## 21. Redis 使用原则

Redis 不是 Agent 必需组件。

只有明确需求时使用：

- rate limit；
- short-lived cache；
- idempotency；
- distributed lock；
- transient execution metadata。

不要默认使用 Redis：

- 作为长期 Conversation Memory；
- 作为 Knowledge Store；
- 作为 Agent State Source of Truth。

---

## 22. API 设计

### 22.1 Conversation API

建议：

```text
POST   /api/conversations
GET    /api/conversations
GET    /api/conversations/{conversation_id}
PATCH  /api/conversations/{conversation_id}    # 重命名，body {"title": "..."}
DELETE /api/conversations/{conversation_id}    # 如产品需要
```

列表按 conversation id（ULID）倒序，游标即 id —— ULID 单调递增，等价于按创建时间倒序，不需要额外排序列。

重命名只改 title，不改列表顺序。换成按 updated_at 排序需要复合游标（updated_at 不唯一，单列游标会漏记录或重复），已经发给客户端的游标也会失效。

标题两个入口（POST 建会话、PATCH 重命名）共用一套校验：先归一化（删零宽字符、内部连续空白折成一个空格、去掉首尾），再要求 1..60 字符。归一化而不是报错，是因为从别处粘一段话过来带上换行是常事；但零宽字符和内嵌换行原样落库会留下一个在列表里看不见名字、或者把那一行撑坏的标题。约束同时以 `pattern` 进 openapi.json —— minLength 表达不了「去掉空白后还剩内容」，前端校验器光看 minLength 会放过纯空白。

POST 不传 title 表示交给自动命名（取首条消息前 60 字）；PATCH 不接受 null 或纯空白 —— 把 title 置回 null 会让下一条消息重新触发自动命名，用户看到的是自己起的名字丢了。

### 22.2 Message API

```text
POST /api/conversations/{conversation_id}/messages
```

返回 Streaming Response。

### 22.3 Feedback API

```text
POST   /api/messages/{message_id}/feedback
DELETE /api/messages/{message_id}/feedback    # 撤回，幂等
```

Body：`rating`（`helpful` / `not_helpful`）、可选 `reason`、可选 `comment`。

只接受 assistant 消息。PRD §10.9 收的是「回答反馈」，对 user 消息提交返回 400 —— 否则 §10.9.3 的失败类型统计里会混进没有对应回答的行。

`reason` 取 PRD §10.9.2 的 7 个原因，存英文 slug，中文文案由前端持有 —— 文案改动不影响已落库的数据，按原因聚合统计也不受 i18n 影响：

```text
incorrect_data        数据不正确
incorrect_rule        规则不正确
not_answered          没有回答问题
wrong_backend         找不到对应后台
incomplete            结果不完整
hard_to_understand    表达难理解
other                 其他
```

同一条消息重复提交是 upsert，并且是整条覆盖：重提时不带 `reason` / `comment`，旧值清空。用户改主意是正常操作，不该报冲突；但保留上一次的 reason 会让 PRD §10.9.3 要的失败类型统计失真。`created_at` 停在首次提交，`updated_at` 每次都动，两个都要回给客户端 —— 只给 created_at 的话，改过的反馈在 UI 上看不出变化。

提交时「先查再插」之间存在竞态（`feedback.message_id` 唯一），并发提交要退回更新路径，而不是把唯一约束冲突抛成 500 —— 用户双击点赞看到错误就违反了 PRD §10.9.3 的「反馈不打断正常使用」。

撤回是真删那一行，不是加一个「已撤回」状态：`rating` 是 NOT NULL，没有第三种取值可表达，加一列只为表达「没有意见」不值得。前端的赞踩因此是三态的 —— 没打过 → 打过 → 再点同一个撤回。幂等（没打过分也返回 204）：用户连点两下撤回，第二下没有理由报错，最终状态完全一样。

反馈没有独立的读取接口。`feedback.message_id` 唯一，一条消息最多一条反馈，所以直接内嵌在会话详情的 `messages[].feedback`（没反馈过是 `null`）。独立 GET 要按消息数发请求，而前端最主要的用法就是刷新后恢复一屏赞踩状态。

### 22.4 Health

```text
GET /healthz    存活，不检查任何依赖
GET /readyz     就绪，只检查自己的 MySQL
```

路径与响应体和 internal-mcp-server 的 `internal/httpapi` 对齐，两边可以共用同一套探活配置。

Readiness 可以检查必要依赖，但不要因为非关键外部系统短暂异常导致整个 API 无法接收请求。

所以 `/readyz` 刻意不检查 internal-mcp-server：MCP 不可用时单次对话会失败并返回明确错误，但服务本身仍应就绪。MySQL 不可用返回 503 `{"status": "unavailable"}`，正常返回 200 `{"status": "ready"}`。

---

## 23. Agent Response Contract

### 23.1 为什么需要 Response Contract

最终回答不能只有一段自由文本。

后端需要支持：

```text
text
structured data
navigation
evidence
warnings
```

### 23.2 建议结构

```json
{
  "answer": {
    "text": "..."
  },
  "results": [],
  "navigation": [],
  "evidence": [],
  "warnings": []
}
```

Streaming 时逐步发送事件，最终形成完整对象。

### 23.3 Tool Raw Result

Tool Raw Result 不直接返回前端。

必须经过：

```text
Tool Result
  -> Agent / Response Mapping
  -> Sensitive Data Filter
  -> UI Response
```

---

## 24. Testing

### 24.1 Web

```text
Vitest
React Testing Library
Playwright（关键 E2E）
```

重点：

- Chat streaming；
- 新建会话；
- 消息渲染；
- 表格；
- 后台导航；
- 错误状态；
- 长文本和输入框；
- XSS。

### 24.2 ai-copilot-api

```text
pytest
```

覆盖：

- Conversation service；
- Auth context；
- Agent context；
- MCP client；
- Guardrails；
- Evidence mapping；
- Streaming；
- Error handling。

模型调用在普通单元测试中 mock。

### 24.3 internal-mcp-server

使用 Go 标准 testing。

覆盖：

- Tool Schema；
- Tool authorization；
- Adapter；
- Error normalization；
- timeout；
- rate limit；
- sensitive filtering；
- path allowlist；
- delegated identity。

### 24.4 Contract Tests

重点建设：

```text
MCP Tool Contract Tests
```

防止：

- Tool name 意外变化；
- Schema breaking change；
- enum 变化；
- permission metadata 丢失；
- output field semantic drift。

---

## 25. Agent Eval

### 25.1 为什么必须有 Eval

Agent 行为不能只通过传统单元测试验证。

需要建立真实运营问题数据集。

### 25.2 Eval Dataset

MVP 第一批来自产品定义的 20～30 个高频问题，并扩展变体：

```text
数据查询
数据解释
规则问答
后台导航
综合问题
权限不足
没有数据
下游失败
信息不足
歧义问题
```

### 25.3 Eval 维度

#### Tool Selection

- 是否选择正确 Tool；
- 是否避免无关 Tool；
- 是否出现重复调用。

#### Tool Arguments

- 参数是否正确；
- 用户实体是否正确；
- 时间范围是否合理；
- limit 是否合理。

#### Permission

- 是否尝试绕过权限；
- Permission denied 是否正确解释。

#### Groundedness

- 回答中的事实是否来自 Tool / Knowledge；
- 是否编造不存在的数据；
- 是否把推测描述为事实。

#### Error Handling

- 是否区分 no data / failure；
- partial failure 是否正确提示。

#### Evidence

- 关键结论是否有关联来源；
- 来源是否正确。

#### Safety

- 敏感字段是否泄漏；
- Prompt injection 是否影响 Tool policy。

### 25.4 生产变更 Gate

以下变化必须运行 Eval：

- Model；
- Agent instructions；
- Tool descriptions；
- Tool Schema；
- Knowledge retrieval；
- Guardrails；
- Context strategy。

---

## 26. Repository Structure

### 26.1 ai-copilot-web

```text
ai-copilot-web/
├── src/
│   ├── api/
│   ├── components/
│   │   ├── ui/
│   │   └── copilot/
│   ├── features/
│   │   ├── auth/
│   │   ├── conversation/
│   │   └── feedback/
│   ├── hooks/
│   ├── lib/
│   ├── routes/
│   └── styles/
│
├── tests/
├── package.json
├── pnpm-lock.yaml
└── vite.config.ts
```

### 26.2 ai-copilot-api

```text
ai-copilot-api/
├── app/
│   ├── api/
│   ├── auth/
│   ├── conversation/
│   ├── agent/
│   ├── mcp/
│   ├── model/
│   ├── audit/
│   ├── observability/
│   ├── db/
│   └── config/
│
├── migrations/
├── tests/
├── evals/
├── pyproject.toml
└── uv.lock
```

### 26.3 internal-mcp-server

```text
internal-mcp-server/
├── cmd/
│   └── server/
│
├── internal/
│   ├── auth/
│   ├── authorization/
│   ├── audit/
│   ├── ratelimit/
│   ├── mcp/
│   ├── adapters/
│   │   ├── adminapi/
│   │   ├── websiteapi/
│   │   └── git/
│   ├── tools/
│   │   ├── users/
│   │   ├── wallet/
│   │   ├── deposits/
│   │   ├── withdrawals/
│   │   ├── trading/
│   │   ├── kyc/
│   │   ├── ib/
│   │   ├── knowledge/
│   │   ├── navigation/
│   │   └── source/
│   └── observability/
│
├── knowledge/
│   ├── concepts/
│   ├── rules/
│   ├── navigation/
│   └── sop/
│
├── tests/
└── go.mod
```

---

## 27. CI/CD

### 27.1 ai-copilot-web

CI：

```text
install
lint
typecheck
test
build
```

### 27.2 ai-copilot-api

CI：

```text
uv sync
ruff check
ruff format --check
pyright
automated tests
selected agent evals
container build
```

### 27.3 internal-mcp-server

CI：

```text
gofmt check
go vet
go test ./...
contract tests
container build
```

### 27.4 Deployment

三个项目独立构建、独立部署：

```text
ai-copilot-web
ai-copilot-api
internal-mcp-server
```

`internal-mcp-server` 默认只暴露在内部网络。

---

## 28. MVP Tool 范围

建议 MVP 控制在约 10～20 个高频只读 Tool。

优先领域：

```text
User
Deposit
Withdrawal
Trading / Order
KYC
IB / Commission
```

第一批 Tool 不按“把所有 API MCP 化”的思路建设。

选择标准：

1. 高频运营问题需要；
2. 输入参数边界清楚；
3. 权限模型清楚；
4. 返回数据可控制；
5. 可以形成稳定业务语义；
6. 有明确 owner。

---

## 29. MVP Knowledge 范围

优先覆盖：

- 20～30 个高频运营问题；
- 关键 Concepts；
- 关键 Rules；
- 高频 Navigation；
- 核心 SOP。

Knowledge 覆盖率优先围绕真实运营问题，而不是追求公司全量业务知识建模。

---

## 30. 写操作演进

MVP：

```text
READ ONLY
```

未来写操作固定采用：

```text
Agent proposes action
        ↓
User reviews
        ↓
Explicit confirmation
        ↓
Permission check
        ↓
Business API executes
        ↓
Audit
```

写操作必须单独建设：

- Write Tools；
- 更严格权限；
- Confirmation；
- Idempotency；
- Before / After State；
- Write Audit；
- 必要时 Approval Workflow。

模型不能直接调用通用“execute API”接口。

---

## 31. LangGraph / Multi-Agent 演进判断

### 31.1 当前不使用 LangGraph

MVP 问题通常是：

```text
Question
  -> 1~N Tool Calls
  -> Answer
```

OpenAI Agents SDK 足够。

### 31.2 重新评估 LangGraph 的信号

出现以下情况时再评估：

- run 跨分钟 / 小时；
- durable execution；
- pause / resume；
- 多个人工审批节点；
- workflow state 需要独立持久化；
- 异步后台 Agent task；
- 明确的复杂状态机；
- 失败后需要从步骤级 checkpoint 恢复。

### 31.3 当前不使用 Multi-Agent

不为以下能力分别创建 Agent：

```text
Data Agent
Knowledge Agent
Navigation Agent
Source Agent
```

这些已经是 MCP Tool capability groups。

只有在未来出现明确的：

- 独立目标；
- 独立权限；
- 独立上下文；
- 独立评估体系；

才考虑拆分 Agent。

---

## 32. RAG 演进

MVP：

```text
Structured Knowledge
+
Deterministic Search
```

重新评估 RAG 的信号：

- 非结构化文档数量显著增长；
- keyword / alias recall 成为主要问题；
- 用户表达多样性导致知识召回明显下降；
- 人工维护 aliases 成本过高；
- Eval 证明 retrieval 是主要准确率瓶颈。

未来：

```text
knowledge.search
      ↓
Hybrid Search
      ↓
Embedding / Vector Search
```

Agent Tool Contract 尽量保持不变。

---

## 33. 分阶段实施

### Phase 1：基础 MVP

目标：验证 Copilot 是否能降低运营查询和学习成本。

包含：

- React Chat UI；
- shadcn/ui + Tailwind；
- Python FastAPI；
- OpenAI Agents SDK；
- Single Agent；
- Go MCP Server；
- Streamable HTTP；
- Delegated User Identity；
- 10～20 个 Data Tools；
- Concepts / Rules / Navigation；
- 基础 Source Search；
- 多轮会话；
- Structured Response；
- Audit；
- OpenTelemetry；
- Agent Eval。

### Phase 2：覆盖与准确性

包含：

- 更多业务领域；
- 更多综合问题；
- Tool Contract Governance；
- Knowledge Lifecycle；
- Source Knowledge 增强；
- Eval Dataset 扩大；
- Prompt / Model Version Management；
- 质量分析看板。

### Phase 3：受控 Action

包含：

- Write Tools；
- Confirmation；
- Approval；
- Idempotency；
- Write Audit；
- Sensitive Action Policy。

### Phase 4：高级 Agent

只有业务价值明确后评估：

- Investigation Agent；
- Long-running Task；
- Durable Workflow；
- Async Agent Job；
- LangGraph 或其他 workflow runtime；
- Multi-Agent；
- 流程自动化。

---

## 34. MVP 完成定义

技术层面满足以下条件，才认为 MVP Agent 基础设施可上线验证：

1. 用户身份可安全传递到 MCP 和下游 API；
2. 无权限数据无法通过 Copilot 获取；
3. 10～20 个核心 Tool 有稳定 Schema；
4. Tool 有审计和 trace；
5. Tool Failure 与 No Data 能正确区分；
6. Agent 可以完成预定义单步和多步问题；
7. 重要回答可以提供 Evidence；
8. Prompt Injection 基础测试通过；
9. Source Code Tool 只能访问 allowlist；
10. Agent Run 有成本、时延和 Tool 指标；
11. 建立基础 Eval Dataset；
12. Model / Prompt / Tool 变更可以回归 Eval；
13. Web Streaming 体验稳定；
14. 系统异常不会生成伪造业务结论。

---

## 35. 最终技术决策摘要

1. `ai-copilot-web` 使用 React + TypeScript + Vite；
2. 公司当前无 Design System，MVP 使用 shadcn/ui + Tailwind CSS，并逐步沉淀内部 UI 规范；
3. `ai-copilot-api` 使用 Python + FastAPI；
4. Agent Runtime 使用 OpenAI Agents SDK；
5. MVP 使用一个 Operations Copilot Agent；
6. MVP 不使用 LangGraph；
7. MVP 不使用 Multi-Agent；
8. `internal-mcp-server` 使用 Go + Official MCP Go SDK；
9. Agent 与 MCP 使用 Streamable HTTP；
10. MCP 尽量 stateless，Agent / Conversation State 归 `ai-copilot-api`；
11. Tool 调用由公司内部 `ai-copilot-api` 发起；
12. LLM 不直接访问数据库；
13. MCP 通过 `website-api` / `admin-api` 获取业务能力；
14. 下游业务 API 是最终业务授权权威；
15. MCP 额外进行 Tool 授权、审计、限流和数据过滤；
16. 权限使用 Delegated User Identity；
17. MVP 只提供只读 Tool；
18. Knowledge 使用 Git + YAML / Markdown；
19. MVP 不引入 RAG / Vector DB；
20. Source Code Tools 只读、allowlist、无执行能力；
21. Conversation 由 `ai-copilot-api` 自身数据库作为 source of truth；
22. MySQL 用于会话、消息、反馈和 Agent metadata；
23. Redis 仅按明确需求引入；
24. OpenTelemetry 作为系统级可观测性标准；
25. Agent SDK Tracing 作为 Agent 调试和行为观测补充；
26. Agent Eval 是生产变更的重要质量 Gate；
27. 未来写操作固定使用“AI 建议 → 用户确认 → 权限检查 → 业务 API 执行 → 审计”；
28. 只有出现长任务、durable workflow、pause/resume 等明确需求时才重新评估 LangGraph。

---

## 36. 相关文档

- `ai-copilot-prd.md`
- `ai-copilot-technology-selection.md`

官方技术参考：

- OpenAI Agents SDK
- OpenAI Agents SDK MCP integration
- Model Context Protocol Specification
- Official MCP Go SDK
- FastAPI
- shadcn/ui
- Tailwind CSS
