# AI Copilot 技术选型

> 文档名称：`AI Copilot 技术选型`  
> 文档版本：`V1.0`  
> 创建日期：`2026-09-18`  
> 最后更新：`2026-09-18`  
> 当前状态：`已确认 / MVP 基线`

---

## 1. 文档目的

本文档用于明确 AI Copilot MVP 阶段三个核心项目的技术栈、AI Agent 开发框架、关键基础设施以及暂不采用的方案，作为后续架构设计、工程初始化、代码开发、CI/CD 和技术评审的统一依据。

涉及项目：

- `ai-copilot-web`
- `ai-copilot-api`
- `internal-mcp-server`

产品范围以 `ai-copilot-prd.md` 为准，系统架构和实现细节以 `ai-copilot-technical-design.md` 为准。

---

## 2. 选型目标与原则

### 2.1 选型目标

MVP 阶段优先解决以下问题：

1. 快速验证 AI Copilot 是否能提升运营查询和问题定位效率；
2. 保持 Agent Runtime 足够简单，避免提前建设复杂 Agent 平台；
3. 保证业务数据访问、权限和审计边界清晰；
4. 最大化复用现有 `website-api` / `admin-api` 业务能力；
5. 为后续扩展 RAG、长任务、写操作和复杂 Agent 工作流保留演进空间；
6. 降低首期基础设施和运维复杂度。

### 2.2 核心原则

- 不要求三个项目统一使用同一种语言；
- AI Runtime 优先选择 AI 生态成熟度，而不是单纯追求语言统一；
- Business Gateway 优先选择稳定性、安全性和现有后端工程经验；
- MVP 使用单 Agent，不使用 Multi-Agent；
- MCP 是 AI 与业务能力之间的统一协议边界；
- LLM 不直接访问数据库；
- LLM 不直接访问任意内部 HTTP API；
- 首期不引入 RAG / Vector DB；
- 首期不引入长任务编排框架；
- 首期只提供只读业务 Tool。

---

## 3. 最终技术栈摘要

| 领域 | 技术选择 | 决策 |
|---|---|---|
| Web Framework | React + TypeScript + Vite | 已确认 |
| Web UI | shadcn/ui + Tailwind CSS | 已确认 |
| Web Routing | React Router | 已确认 |
| Server State | TanStack Query | 已确认 |
| Client State | React State，必要时 Zustand | 已确认 |
| Web Package Manager | pnpm | 推荐默认 |
| AI Copilot API | Python + FastAPI | 已确认 |
| Python Package Manager | uv | 推荐默认 |
| Agent Framework | OpenAI Agents SDK | 已确认 |
| Data Validation | Pydantic | 框架默认 / 推荐 |
| Python ORM | SQLAlchemy 2.x | 推荐默认 |
| DB Migration | Alembic | 推荐默认 |
| MCP Server | Go | 已确认 |
| MCP SDK | Official MCP Go SDK | 已确认 |
| MCP Transport | Streamable HTTP | 已确认 |
| MCP HTTP Layer | Go `net/http`，必要时使用轻量 Router | 推荐默认 |
| Database | MySQL | 已确认 |
| Cache | Redis，按需使用 | 已确认 |
| Knowledge | Git + YAML / Markdown | 已确认 |
| RAG / Vector DB | 不引入 | MVP 不采用 |
| Agent Topology | Single Agent | 已确认 |
| LangGraph | 不引入 | MVP 不采用 |
| Multi-Agent | 不引入 | MVP 不采用 |
| WebSocket | 不引入 | MVP 默认不采用 |
| Response Streaming | HTTP Streaming / SSE 风格前端消费 | 已确认 |
| Observability | OpenTelemetry + Structured Logging | 已确认 |
| CI | 公司现有 CI；如无统一约束则 GitHub Actions | 待环境确认 |

版本策略：文档不绑定 patch 版本。项目初始化时选择当前稳定版本并通过 lockfile / toolchain 文件锁定，升级通过单独 PR 完成。

其中“推荐默认”属于不影响总体架构的工程级默认值；如果团队已有统一的包管理、ORM、CI 或 Router 规范，应优先遵循团队规范，不需要改变本文档的核心架构决策。

---

## 4. `ai-copilot-web` 技术选型

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

### 4.2 为什么选择 React + Vite

AI Copilot 是内部运营工具，核心页面是 Chat UI 和结构化结果展示，不需要：

- SEO；
- SSR；
- SSG；
- 面向公网的内容分发能力。

因此没有必要为了框架能力引入 Next.js。Vite 更简单，构建、部署和本地开发成本更低，也与现有 React 技术栈一致。

### 4.3 为什么选择 shadcn/ui + Tailwind CSS

公司目前没有现有 Design System，因此 MVP 需要一套：

- 可快速落地；
- 样式可控；
- 不形成强供应商绑定；
- 后续容易沉淀为内部组件规范；
- 适合 Chat、Dialog、Table、Card、Command、Tooltip 等后台组件的方案。

`shadcn/ui` 的组件代码直接进入项目，可以按业务需求修改，不需要将未来 Design System 绑定在第三方组件库抽象上。

建议把 MVP 中实际使用的组件逐步沉淀到：

```text
src/components/ui/
src/components/copilot/
```

后续如果产品规模扩大，可从现有组件中抽取：

```text
Design Tokens
Typography
Spacing
Form Components
Data Display Components
Feedback Components
```

逐步形成公司的轻量 Design System，而不是 MVP 阶段先建设完整 Design System。

### 4.4 状态管理

建议明确区分：

```text
Server State
  -> TanStack Query

Local UI State
  -> React State

Cross-page UI State（确有需要时）
  -> Zustand
```

不建议 MVP 直接引入 Redux。

### 4.5 Chat Streaming

推荐：

```text
POST /api/conversations/{id}/messages
        |
        v
HTTP streaming response
```

前端逐段展示：

- assistant text；
- tool status；
- structured result；
- error；
- completion event。

MVP 不需要 WebSocket。只有未来出现长任务、双向事件或复杂实时状态同步后再评估。

---

## 5. `ai-copilot-api` 技术选型

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

### 5.2 为什么使用 Python

`ai-copilot-api` 的主要职责不是传统 CRUD，而是：

- LLM 调用；
- Agent Runtime；
- Tool Calling；
- MCP Client；
- Conversation Context；
- Guardrails；
- Structured Output；
- Tracing；
- Eval；
- Prompt / Model Versioning。

该服务的工作负载主要是等待 LLM、MCP 和下游 API，属于 I/O 密集型服务，语言运行时性能不是主要瓶颈。

Python 在 Agent、LLM、评估和实验工具链方面生态更完整，因此选择 Python 可以明显降低 AI Runtime 的自研成本。

### 5.3 为什么选择 FastAPI

FastAPI 适合作为 Agent API 的薄 Web 层：

- 原生 async；
- 与 Pydantic 集成良好；
- 适合 Streaming；
- API Schema 清晰；
- 易于测试；
- 不强制复杂应用框架结构。

`ai-copilot-api` 不应发展为新的业务服务，因此保持 FastAPI 层简单。

### 5.4 为什么选择 OpenAI Agents SDK

MVP 所需能力与 OpenAI Agents SDK 的能力高度重合：

- Agent loop；
- Tools；
- MCP Server integration；
- Sessions；
- Guardrails；
- Tracing；
- Human-in-the-loop 的后续扩展能力。

因此 MVP 不再自行实现完整的：

```text
Planner Framework
Executor Framework
Tool Dispatch Framework
Agent State Machine
```

只实现本产品需要的策略、上下文、证据和安全边界。

### 5.5 Agent 结构

MVP 使用一个 Agent：

```text
Operations Copilot Agent
```

其能力来自 `internal-mcp-server`：

```text
Operations Copilot Agent
    |
    +-- Data Tools
    +-- Knowledge Tools
    +-- Navigation Tools
    +-- Source Code Tools
```

不拆分：

```text
Data Agent
Knowledge Agent
Navigation Agent
Source Agent
Supervisor Agent
```

除非后续评估证明单 Agent 出现明确的能力边界或可维护性问题。

### 5.6 Agent Runtime 限制

MVP 建议通过配置限制：

- 最大 Agent turns；
- 最大 Tool calls；
- 单次 Tool timeout；
- 整体请求 timeout；
- 最大上下文大小；
- 单次结果最大数据量；
- Token / Cost budget。

具体数值通过压测和 Eval 调整，不在选型文档硬编码。

### 5.7 Python 工程工具

推荐：

```text
uv              dependency / environment
Ruff            lint + format
pytest          tests
pyright         type checking（建议）
```

不建议 MVP 同时堆叠多个重叠 lint / formatter。

---

## 6. `internal-mcp-server` 技术选型

### 6.1 技术栈

```text
Go
Official MCP Go SDK
net/http
OpenTelemetry
slog
```

如果需要路由辅助，可使用团队熟悉的轻量 Router，但不需要引入大型 Web Framework。

### 6.2 为什么使用 Go

`internal-mcp-server` 本质是一个受控的 AI Business Gateway，核心职责是：

- MCP Tool Registry；
- Authentication；
- Authorization；
- Delegated User Identity；
- 参数校验；
- 下游 API Adapter；
- Timeout；
- Rate Limit；
- Audit；
- Sensitive Data Filtering；
- Knowledge / Source read-only access。

这些属于典型后端和基础设施问题，Go 更适合保持服务稳定、边界清晰，并可复用现有后端团队工程经验。

### 6.3 MCP SDK

使用官方：

```text
github.com/modelcontextprotocol/go-sdk
```

不自行实现 MCP JSON-RPC 协议。

### 6.4 MCP Transport

使用：

```text
Streamable HTTP
```

服务关系：

```text
ai-copilot-api
      |
      | MCP / Streamable HTTP
      v
internal-mcp-server
```

不使用 stdio 作为服务间通信协议。

旧式 HTTP+SSE transport 只用于兼容已有系统，不作为新项目默认方案。

### 6.5 MCP Server 是否保存 Agent 状态

不保存。

`internal-mcp-server` 应尽量保持 stateless：

```text
Agent / Conversation State
    -> ai-copilot-api

Business Tool Execution
    -> internal-mcp-server
```

MCP Server 不承担：

- Conversation Memory；
- Agent Planning State；
- LLM Context；
- Agent Resume State。

---

## 7. AI Agent 框架选择

### 7.1 MVP 结论

```text
OpenAI Agents SDK
+
Single Agent
+
MCP Tools
```

### 7.2 不直接使用 Responses API 自研 Agent Loop 的原因

直接使用 Responses API 可获得更高控制权，但需要自行维护：

- turn loop；
- tool dispatch；
- tool result management；
- session / context；
- guardrail integration；
- tracing integration；
- error recovery。

当前项目没有足够业务收益支持这部分自研成本。

如果未来 Agents SDK 的抽象限制产品能力，可在保持 MCP Tool Contract 不变的情况下替换 Runtime。

### 7.3 MVP 不使用 LangGraph

LangGraph 更适合：

- long-running workflow；
- durable execution；
- explicit state graph；
- pause / resume；
- 多个人工审批节点；
- 复杂 deterministic + agentic 混合流程。

当前 MVP 主要是：

```text
Question
  -> Agent
  -> 1~N Tool Calls
  -> Synthesis
  -> Answer
```

因此 LangGraph 会增加不必要的：

- graph definition；
- state persistence；
- workflow abstraction；
- 调试层级；
- 运维复杂度。

### 7.4 何时重新评估 LangGraph

出现以下任一情况时重新评估：

1. 单个 Agent Run 经常跨分钟甚至跨小时；
2. 任务需要可靠 pause / resume；
3. 一个流程包含多个强约束人工审批节点；
4. 需要持久化复杂 workflow state；
5. Agent 任务需要异步后台继续执行；
6. 大量步骤必须以确定性状态机控制；
7. 单 Agent + Tool Loop 已明显无法表达业务流程。

### 7.5 MVP 不使用 Multi-Agent

Multi-Agent 会额外增加：

- Token consumption；
- Latency；
- Routing 错误；
- Context 传递复杂度；
- Trace 复杂度；
- Eval 成本；
- 权限边界复杂度。

当前 Data / Knowledge / Navigation / Source 已经通过 MCP Tool 分类形成能力边界，不需要再通过 Agent 拆分一次。

---

## 8. 数据与基础设施选择

### 8.1 MySQL

继续使用 MySQL 保存 AI Copilot 自身需要持久化的数据：

- conversations；
- messages；
- feedback；
- agent_run metadata；
- audit metadata；
- prompt/model version references。

业务数据仍由现有业务服务持有。

### 8.2 Redis

Redis 不是 MVP Agent Runtime 的必需依赖。

仅在存在明确用途时使用，例如：

- rate limiting；
- ephemeral cache；
- distributed lock；
- short-lived session metadata；
- idempotency。

不要把 Redis 作为 Agent Memory 的默认方案。

### 8.3 Knowledge Storage

MVP：

```text
Git
├── concepts/*.yaml
├── rules/*.yaml
├── navigation/*.yaml
└── sop/*.md
```

优点：

- Code Review；
- Version Control；
- Owner 清晰；
- Diff 清晰；
- 可回滚；
- 不需要新增基础设施。

---

## 9. Observability 选型

统一采用 OpenTelemetry 作为跨服务可观测性标准。

链路：

```text
Browser
  -> ai-copilot-api
     -> Agent Run
        -> MCP Tool
           -> internal-mcp-server
              -> admin-api / website-api
```

核心 Trace Context：

```text
trace_id
conversation_id
agent_run_id
tool_call_id
user_id
```

日志采用结构化日志：

- Python：标准 logging / structlog 二选一，MVP 优先少依赖；
- Go：`log/slog`。

Agent SDK 自带 tracing 用于 Agent 开发和调试；系统仍保留 OpenTelemetry 作为公司内部服务链路的统一观测标准，两者用途不同。

---

## 10. Testing / Eval 选型

### 10.1 传统测试

`ai-copilot-web`：

- Vitest；
- React Testing Library；
- 关键 E2E 可使用 Playwright。

`ai-copilot-api`：

- pytest；
- Agent service unit test；
- MCP client integration test。

`internal-mcp-server`：

- Go standard testing；
- Tool contract tests；
- Adapter integration tests；
- Authorization tests。

### 10.2 Agent Eval

Agent 不能只依赖传统单元测试。

MVP 需要维护一组真实运营问题 Eval Dataset，至少覆盖：

- Tool selection 正确性；
- 不必要 Tool call；
- Tool 参数正确性；
- Permission handling；
- Tool failure handling；
- No-data vs failure 区分；
- Final answer groundedness；
- Evidence completeness；
- Sensitive data handling。

Prompt、Model、Tool Schema 发生重要变化时运行回归 Eval。

---

## 11. 暂不采用的技术

| 技术 | MVP 是否使用 | 原因 |
|---|---:|---|
| Next.js | 否 | 内部 SPA，无 SSR / SEO 需求 |
| Redux | 否 | 当前状态复杂度不足以支撑引入 |
| WebSocket | 否 | HTTP Streaming 足够 |
| LangChain | 否 | 当前主要能力已由 Agents SDK + MCP 覆盖 |
| LangGraph | 否 | 暂无 durable long-running workflow 需求 |
| Multi-Agent | 否 | 增加成本和不可控性 |
| Vector DB | 否 | 首期知识规模有限，可结构化 |
| RAG | 否 | Structured Knowledge + deterministic search 足够 |
| 任意 SQL Agent | 禁止 | 绕过业务和权限边界 |
| 任意 Shell / Code Execution | 禁止 | 安全风险高且无 MVP 必要性 |
| 独立 Agent Service | 否 | Agent Runtime 直接位于 ai-copilot-api |

---

## 12. 项目初始化建议

### 12.1 ai-copilot-web

```text
React + TypeScript + Vite
pnpm
Tailwind CSS
shadcn/ui
React Router
TanStack Query
Vitest
```

### 12.2 ai-copilot-api

```text
Python
uv
FastAPI
OpenAI Agents SDK
Pydantic
SQLAlchemy
Alembic
pytest
Ruff
OpenTelemetry
```

### 12.3 internal-mcp-server

```text
Go
Official MCP Go SDK
net/http
slog
OpenTelemetry
```

---

## 13. 技术决策记录

### ADR-001：AI Runtime 使用 Python

**Status:** Accepted

原因：AI Agent 生态、开发效率和后续 Eval / Experiment 能力优先于语言统一。

### ADR-002：MCP Server 使用 Go

**Status:** Accepted

原因：MCP Server 本质是受控业务网关，强调权限、审计、稳定性和 API Adapter 能力。

### ADR-003：Agent 使用 OpenAI Agents SDK

**Status:** Accepted for MVP

原因：避免重复实现 Agent loop、MCP integration、Guardrails、Sessions 和 Tracing。

### ADR-004：MVP 使用 Single Agent

**Status:** Accepted

原因：当前业务复杂度无需 Multi-Agent；Tool 分类已经承担能力分层。

### ADR-005：MVP 不引入 LangGraph

**Status:** Accepted

原因：当前不存在 durable long-running workflow 的必要性。

### ADR-006：Web 使用 shadcn/ui + Tailwind CSS

**Status:** Accepted

原因：公司无现成 Design System，需要快速形成一致 UI，同时保留完全自定义能力。

### ADR-007：MCP 使用 Streamable HTTP

**Status:** Accepted

原因：`ai-copilot-api` 与 `internal-mcp-server` 独立部署，适合通过内部网络进行标准 MCP HTTP 通信。

### ADR-008：MVP 不引入 RAG

**Status:** Accepted

原因：当前知识规模和知识形态可通过 Git 版本化结构化知识满足。

---

## 14. 官方参考资料

- [OpenAI Agents SDK](https://openai.github.io/openai-agents-python/)
- [OpenAI Agents SDK - MCP](https://openai.github.io/openai-agents-python/mcp/)
- [Model Context Protocol - Go SDK](https://github.com/modelcontextprotocol/go-sdk)
- [MCP Specification](https://modelcontextprotocol.io/)
- [FastAPI](https://fastapi.tiangolo.com/)
- [shadcn/ui - Vite](https://ui.shadcn.com/docs/installation/vite)
- [Tailwind CSS](https://tailwindcss.com/)
