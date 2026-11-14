# adks — ADK Go 脚手架 Harness 架构设计

> 设计台账：2026-11-13 · adks · v1 · 关键决策 11 项 · 未知区 11 项 · 独立评审：存疑 10 项，必修 5 项已回修，复审 1 轮结论见 §16
>
> 术语：**builder** = 脚手架本体（带 Web 界面的单二进制）；**生成物** = 导出的独立 ADK Go 工程；**manifest** = `agent.yaml`，唯一真相源；**kit** = 把 manifest 编译成 ADK 对象的 Go 代码包，builder 与生成物共用同一份源码。

---

## 1. 需求规格

| 维度 | 内容 | 状态 |
|---|---|---|
| 目标与用途 | 用 Web 界面配置模型/指令/工具/MCP/SQL/插件，一键得到可独立运行、可编译、可部署的 ADK Go Agent 工程 | 已确认 |
| 成功标准 | ① 从空白到"能对话的 agent"≤10 分钟且不写一行代码；② 导出工程 `go build` 零错误、`go vet` 零告警；③ builder 内试跑与导出工程对同一输入产出行为一致（同 manifest 同 session 同回复语义） | 已确认 |
| 验收来源（oracle） | 确定性：`go build`/`go vet`/`go test` 退出码 + 生成物冒烟测试（真实跑一轮 run 并断言有 final event）；配置正确性：manifest schema 校验 + 连通性探测（MCP listTools / SQL ping / model 1-token 调用） | 已确认 |
| 任务结构 | 用户任务是"配置驱动的一次性生成"，非多轮推理；生成物承载真正的对话任务 | 已确认 |
| 数据/资源边界 | 项目文件存本地文件系统（`~/.adks/`），不引入数据库；密钥只从环境变量读，manifest 只存 `${ENV}` 引用 | 已确认 |
| 硬约束 | 单二进制、CGO_ENABLED=0、无 Node 运行时依赖；Go ≥1.27；ADK `google.golang.org/adk/v2` ≥2.7.0 | 已确认 |
| 用户与场景 | 内部开发者/团队自用（本地或内网），非公网多租户 SaaS | 已确认 |
| 边界（不做什么） | 不做多 Agent 编排**画布**（拖拽）；Graph workflow 可视化只做**只读拓扑图**（v0.3 已实现，见 §6）；不做运行时多租户鉴权；不做在线代码编辑器写自定义 Go 工具函数 | v0.3 修订 |
| 技术栈约束 | Go + 标准库 net/http + embed + HTMX/Alpine；存储用文件系统 | 已确认 |
| 模型策略与成本 | 生成物：形态 A（单模型），档位由用户选。**builder 自身也会产生 LLM 调用**（模型探测 1 次 1-token、试跑每轮 1+ 次、冒烟 run 1 轮），这部分成本单列在 §3.1，不混入生成物账 | 默认假设 |
| 效率与体感 | 导出 ≤2s（p95，纯内存渲染 100 次基准）；MCP 探测硬超时 3s；SQL ping ≤2s；试跑首字节 ≤1.5s（流式 SSE） | 默认假设 |

### 1.1 与用户沟通确认的四项架构分叉

| 分叉 | 结论 | 状态 |
|---|---|---|
| 产物形态 | 配置即运行 **且** 可导出源码，两出口共享同一份 IR 与同一份编译器 | 已确认 |
| 编排范围 | 首版单 Agent 全配（instruction/模型/工具/MCP/SQL/插件/回调） | 已确认 |
| UI 技术栈 | 纯 Go 单二进制（embed 静态资源 + HTMX/Alpine） | 已确认 |
| 数据模型 | 多项目工作台（每个项目独立 manifest + 独立后端配置） | 已确认 |

---

## 2. 架构范式 P

| 子维度 | 决策 | 理由 |
|---|---|---|
| 扩展方式 | **插件化（在配置层）**：新增能力 = 新增一个 `ToolSpec` / `MCPRef` / `PluginRef` 的 kind，不改核心流程 | 脚手架的价值在"加能力不改代码"；但 kit 本身不引入 Go plugin 动态加载（Windows 受限、跨编译脆弱） |
| 配置方式 | **声明式为主 + 一个代码逃生舱**：manifest 覆盖 95% 场景；导出后的 Go 源码可任意改（逃生舱不在 builder 内，而在产物里） | 声明式让 UI 可校验可预览；不做"UI 里写 Go 代码"，避免在 builder 里造一个不安全的编译器 |
| 部署拓扑 | **单机**：builder 与生成物都单进程；生成物可后续上 Cloud Run/GKE，但不做分布式子 agent | 首版单 Agent，分布式是 P2 |
| 编排模式 | **中心化、无编排**：一个 LLM 循环，工具平铺 | 与"单 Agent 全配"一致 |
| 能力供给形态 | **五形态并列**：内置工具 / 声明式 HTTP 工具 / MCP 服务器 / Skill 包 / 工具延迟加载（`toolsearch`） | 这五类对上下文占位与信任边界完全不同，见 §4.2 供给矩阵 |
| 与 Python ADK 的分界 | **不重造 `adk web`，只做 Go 侧的"配置→可编译产物"** | Python 已有 `root_agent.yaml` + `adk web` + `OpenAPIToolset`，能力面比本首版宽；adks 的差异点只有两条：① Go 单二进制无 Python 运行时依赖；② 产物是可编译、可改、可提交的 Go 工程，不是被框架解释的 YAML。若用户不需要这两条，应直接用 Python ADK —— 这条要写进 README，避免用户选错工具 |

**P 对 E 层的影响**：声明式配置 → E 层不接受用户自定义循环，只暴露预算与终止参数（`limits`）；插件化 → E 层行为可通过 callbacks 与 plugin 挂钩，但不能替换循环。

---

## 3. 技术栈选型

| 项 | 选型 | 理由 |
|---|---|---|
| 语言 | Go 1.27.1（已装；ADK Go 的 `go.mod` 门槛是 **1.26.10**，本机满足且高一个次版本） | 与 ADK Go 要求兼容；不追 1.27 的新特性，避免生成物在用户机器上因低版本构建失败 |
| HTTP 路由 | `net/http` + `http.ServeMux`（Go 1.22+ 支持 `{slug}` 路径参数） | 零依赖，够用；`chi` 只在需要中间件链时才引入 |
| 前端 | 单页 HTML + **HTMX 2.x** + **Alpine.js 3.x**，全部 `//go:embed web/dist` | 无构建步骤即无 Node 依赖；表单与局部刷新正是 HTMX 主场 |
| YAML | `gopkg.in/yaml.v3` | 支持注释保留（导出时用户手改不丢注释） |
| JSON Schema | `github.com/google/jsonschema-go/jsonschema` v0.4.x | ADK `functiontool.Config.InputSchema` 就用这个类型，零转换损失 |
| SQL 会话 | ADK `session/database` + GORM dialector：sqlite → `github.com/glebarez/sqlite`（纯 Go）；postgres → `gorm.io/driver/postgres`；mysql → `gorm.io/driver/mysql` | ADK 官方只给了 GORM 入口；`gorm.io/driver/sqlite` 依赖 CGO，与"单二进制"硬约束冲突，必须换纯 Go 实现 |
| 生成物构建 | 直接调用本机 `go` 工具链做冒烟（`go build ./...`），失败则如实报错 | 不做内置编译器；不预装任何东西 |
| 运行环境 | Windows/macOS/Linux 三平台单二进制，`CGO_ENABLED=0` | 分发即拷即用 |

### 3.1 模型组合

**两本账必须分开记**（初版把它们混成一本，是错误）：

**账 A — 生成物**（形态固定 A：单智能体 + 单模型）

| 档位 | 候选模型 | 职责 | 单价量级 | token 占比 |
|---|---|---|---|---|
| 确定性代码 | — | 工具执行、预算计数、上下文排序 | — | 0% |
| 决策型模型 | 不适用 | 无封闭答案集的高频决策点 | — | — |
| 快速低价 LLM | 用户可选（如 `gemini-flash-latest`） | 生成物主循环 | 知识库未收录（无 model 条目），**不虚构价格**；UI 显示"以供应商官网为准" | 用户选谁就是谁，100% 落在所选那一个模型上 |
| 强力 LLM | 用户可选（如 `gemini-2.5-pro`） | 同上，UI 显式切换 | 同上 | 同上——**不是 0%**，切换后它就是 100% |

**账 B — builder 自身**（配置期真实花费，初版漏记）

| 动作 | 调用次数 | token 量级 | 何时发生 |
|---|---|---|---|
| 模型探测（`/probe/model`） | 1 次 | 输入 <100 + 输出 ≤8 | 用户点"测试连接" |
| 试跑（`/play`） | 每次用户消息 1+ 次（工具往返会叠加） | 首轮输入 = instruction + 工具 schema + 消息；后续含历史 | 每次点发送 |
| 冒烟 run（`/verify`） | 1 轮，通常 1–3 次调用 | 同上，工具会真实执行一次 | 点"导出"前的校验 |

builder 的作用是让这三类调用**可见**：UI 在试跑页显示本次会话累计调用次数与累计输入/输出字符数（**字符数，不换算成钱**——换算需要价格表，而我们不维护价格表）。这是"不虚构价格"的正确做法，而不是把成本整列留空。

**为什么不是形态 B（循环内分档）**：初版理由「脚手架不该强加档位」只对**默认值**成立，对「不提供分档能力」不成立——因为默认模型本身就是一个成本决策。修正后的理由：分档要求用户能说清"哪个步骤该便宜"，这属于对生成物业务的理解，而脚手架的输入是"一个还没写过的 agent"。**v1 不提供分档 UI，但保留 `model` 为单值字段而非硬编码**，导出后用户加 sub-agent 或自定义 `InstructionProvider` 即可分档。该决定记为默认假设，用户若要"同一次生成里就带分档"，需在 §9.3 增加 `models: {main, cheap}` 并改 `Build`，成本约 2 天。

**为什么不是形态 C（多 Agent）**：三条硬墙（上下文退化实测 / 需可丢弃并行上下文 / 工具选不对）在本场景**均无实测证据**；替代清单中第 ② 项（工具延迟加载）现已纳入 T 层设计（ADK Go 官方 `tool/toolsearch`，见 §4.2），第 ④ 项（循环内分档）明确不做，第 ① 项（渐进披露）走 Skill 包列为 P2。因此准确表述是：**替代项未全部实测穷尽，本设计不宣称已穷尽；它只是首版的最小合理选择**。若实测出现"工具 >30 条后选择准确率显著下降"，第一动作是启用 `toolsearch` 而非上多 Agent。

---

## 4. 六层设计

> 本节描述**生成物**的 harness 六层；"builder 怎么把它配置出来"作为每层的落地规格并列。

### 4.1 E 执行循环

- **设计决策**
  - 循环由 ADK `runner` 内置（事件驱动、tool-calling 循环），**不替换**。我们只加三层外部保险。
  - 终止条件：模型返回无 tool call 的 final event 为正常终止；预算耗尽为强制终止（落到 V 的确定性判定，不是模型自评）。
  - 三道保险：`limits.maxLLMCalls`（默认 20）、`limits.maxWallClockSec`（默认 120）、单次工具调用超时（HTTP 工具 `timeoutSec`，默认 20）。
  - 失败回喂：ADK 默认把工具错误作为 function response 回给模型；我们**不吞错**，`http` 工具返回 `{"error": "...", "status": 502}` 而非 `error`，让模型看到结构化失败并自行改参。
  - 打转检测：`BeforeToolCallbacks` 内维护 `(toolName + 参数 sha256)` 指纹环，连续 3 次相同 → 返回终止性错误事件（`tool.ErrConfirmationRejected` 同族的自定义错误，由 callback 返回非 nil 终止后续调用）。
  - 墙钟与并行：ADK 单轮内多工具调用由其内部并发；我们只限制 `maxToolConcurrency`（默认 4）作用在自己实现的 HTTP 工具的信号量上。
- **落地规格**
  - `kit/callbacks.go`：`NewLimitCallbacks(lim manifest.LimitsSpec) []llmagent.BeforeModelCallback` 与 `NewLoopGuard() llmagent.BeforeToolCallback`。
  - 计数落在 `session.State` 的 `temp:adks.llm_calls` 键（`temp:` 前缀 = 不跨轮持久化，符合 ADK 状态语义）。
  - 墙钟：`context.WithTimeout` 包住整次 run，超时后向 SSE 推 `{"type":"error","code":"WALL_CLOCK"}`。
  - 失败分类：瞬时（HTTP 5xx/超时）→ 工具内部重试 1 次（指数退避 200ms + 抖动）；永久（4xx）→ 不重试，结构化回喂；资源（无 API key）→ `Build` 阶段就直接失败，不进入循环。

### 4.2 T 工具注册

- **设计决策**

  | 供给形态 | 上下文占位 | 信任边界 | 首版支持 |
  |---|---|---|---|
  | 内置（`geminitool`、`exitlooptool`、`loadartifactstool`、`preloadmemorytool`） | 极小（模型侧能力） | Google 官方 | ✅ |
  | 声明式 HTTP 工具（**我们自己实现**，补 Go 版无 OpenAPI toolset 的空缺） | 中（每条约 200B schema） | 用户在 UI 里写的 URL，走 host 白名单 | ✅ |
  | MCP 服务器（stdio / streamable HTTP / SSE） | 大（一条 MCP server 可挂 40+ 工具） | 第三方，工具名与 description 是**直入 prompt 的不可信输入** | ✅ |
  | Skill 包（`tool/skilltoolset`） | 按需（渐进披露） | 本地文件夹 | P2 |
  | **工具延迟加载（`tool/toolsearch`）** | 近零（只披露 `search_tools` + 少量核心工具） | Google 官方 | ✅ 开关（默认关，工具 >30 条时 UI 建议开启） |

  - **注册 ≠ 披露 ≠ 授权**三分离：注册在 `Build`（全部纳入 Layer 的 catalog）；披露由 `tool.AllowedToolsPredicate(mcp.ToolAllow)` 与 `tool.FilterToolset` 控制；授权由 `policy.confirmation` 控制——**HTTP 工具走 `functiontool.Config.RequireConfirmation`（稳定字段）；MCP 工具 v1 不接 `tool.WithConfirmation`（EXPERIMENTAL，见 §15 #9），只到"执行后告知"**。
  - **披露序列必须稳定**：所有工具按 `Name()` 字典序排序后再进 `llmagent.Config.Tools` / `Toolsets`，禁止按 manifest 书写顺序（否则用户改一行顺序就重写整段前缀）。
  - 常驻披露阈值：超过 30 条工具时，UI 顶部给黄色告警并提供**二选一**建议——① 收窄 `toolAllow`；② 打开 `tools.toolSearch`。两者**互斥**（见 §15 #10）：开了 `toolSearch` 就忽略 `toolAllow` 过滤，UI 不允许同时配置。30 条是我方经验值，**非官方数据**，按实测调整。
  - 工具描述质量：UI 强制 `description` ≥15 字且必须写"做什么"，占位符给出正面/反面例子，因为描述直接决定调用准确率。
- **落地规格**
  - `kit/tools_http.go`：`NewHTTPTool(spec manifest.HTTPSpec) (tool.Tool, error)`，内部 `functiontool.New[map[string]any, map[string]any](functiontool.Config{Name, Description, InputSchema: schemaFromParams(spec.Params)}, handler)`。
  - `kit/tools_mcp.go`：`NewMCPHandle(ref manifest.MCPRef, deps MCPDeps) (*MCPHandle, error)`。stdio 用 `&mcp.CommandTransport{Command: exec.Command(ref.Command, ref.Args...)}`（`exec.Cmd.Env` 只透传 `EnvAllow` 白名单 + PATH），HTTP 用 `&mcp.StreamableClientTransport{Endpoint: ref.URL, HTTPClient: hc}`，SSE 用 `&mcp.SSEClientTransport{Endpoint: ref.URL, HTTPClient: hc}`。
  - **Close 归属必须分清**（官方硬约束）：`mcptoolset.New` 返回的实例是唯一可 Close 的对象；用 `tool.FilterToolset` 过滤后得到的是包装器，**关它不会释放底层连接**。因此 `MCPHandle` 同时持有 `Raw`（原始实例，唯一 Close 目标）与 `Exposed`（过滤后的包装器，交给 Agent）；`Layer` 只收集 `Raw` 的 Closer，绝不收集 `Exposed`。§8.2 给出结构定义。
  - **凭证不进执行环境**：MCP header 里的 `${MCP_TOKEN}` 在 `Build` 期展开成内存字符串，不写 `exec.Cmd.Env`；HTTP 工具的 `auth.secretEnv` 同样只在 handler 内读 `os.Getenv`，绝不进 manifest 明文、绝不进日志。

### 4.3 C 上下文管理

- **设计决策**
  - 预算：instruction ≤ `limits.maxInstructionBytes`（默认 32KB，超出直接构建失败——比运行时被截断更可预测）；工具 schema 全量（已排序）；历史由 ADK session 事件承载；工具结果在 HTTP 工具内截断到 `maxRespBytes`（默认 64KB）并标记 `{"truncated":true,"originalBytes":N}`。
  - **每轮重新组装**：ADK 每次 `runner.Run` 都重读 session events，天然满足；builder 不做本地缓存。
  - **前缀字节稳定**（本设计最容易被忽略的一条，必须落成规则）：
    1. instruction 里禁止出现时间戳/随机 id（UI 保存时用一个正则扫 `20\d\d-\d\d-\d\d` 与 UUID 并警告）；
    2. 工具列表按名排序（§4.2）；
    3. manifest → kit 的 map 遍历一律先排序（Go map 遍历随机，这是最容易踩的坑）；
    4. 生成物 `//go:embed agent.yaml`，YAML 原文进二进制 → 启动即字节一致；
    5. **`jsonschema.Schema.Properties` 是 map，`PropertyOrder` 未显式设置时最终 JSON 字节序不可控**——"构建时先排序"只决定了 map 的内容，不决定渲染顺序。必须显式设 `PropertyOrder`（按参数名排序），并在 CI 里对每条 HTTP 工具的 schema 渲染结果做 **golden 字节快照测试**；快照变了就说明前缀会重写，必须有意识地接受。
  - **压缩**：暴露 `runtime.compaction` 开关（映射到 `launcher.Config.Compaction`），首版默认关闭。理由：压缩 = 前缀重写，用户看不见成本；默认关，开了在 UI 上明确写"会失效模型前缀缓存，且需要额外一次 LLM 调用做摘要"。
  - 外部内容消毒：HTTP 工具响应与 MCP 工具结果在回喂前用固定包裹标记 `[[UNTRUSTED_DATA src=...]] ... [[/UNTRUSTED_DATA]]`——**这是弱防护**，只降低"内容被当指令"的概率，不承诺防住注入（见 §15）。
- **落地规格**
  - `kit/context.go`：`SanitizeToolOutput(src string, b []byte, max int) map[string]any` —— 截断 + 包裹 + 字节数记录。
  - 排序工具：`sort.SliceStable(tools, func(i,j int) bool { return tools[i].Name() < tools[j].Name() })`，在 `Build` 末尾统一执行一次。

### 4.4 S 状态存储

- **设计决策**
  - 三层状态，各自独立可换：
    | 状态 | 可选后端 | 默认 |
    |---|---|---|
    | 会话（events + state） | `memory` / `sqlite` / `postgres` / `mysql` | `sqlite`（`file:./.adks/sessions.db`） |
    | 制品（artifact） | `memory` / `local`（**我们自己实现**，Go 版只官方提供 gcs） | `local`（`./.adks/artifacts`） |
    | 长期记忆 | `memory`（ADK 无 SQL 实现，Vertex 才有） | `memory` |
  - 并发写：SQLite 用 WAL + `busy_timeout=5000` + `MaxOpenConns=1`（ADK 的 `databaseService` 未声明并发模型，单写最稳）；Postgres/MySQL `MaxOpenConns` 默认 10。
  - `AutoMigrate` 每次启动调用（官方文档明确要求：新版本会加列，不加列则写入失败）。
  - **会话恢复 ≠ 外部行动恢复**：HTTP 工具若有写操作（`method` ∈ POST/PUT/PATCH/DELETE），handler 生成 `Idempotency-Key: {sessionId}:{toolName}:{argHash}` 头；失败重试复用同一 key。无法保证对方支持幂等键的，UI 标注"该工具为重试不安全"。
  - 记忆治理：首版长期记忆只在 `memory` 后端，进程重启即失效——这是**有意不做**，因为长期记忆的可信度/归属/失效条件需要一套治理策略，不该由脚手架默认打开。
- **落地规格**
  - `kit/services.go`：
    ```go
    func newSessionService(s manifest.SessionSpec) (session.Service, error)
    func newArtifactService(a manifest.ArtifactSpec) (artifact.Service, error)
    func newMemoryService(m manifest.BackendSpec) (memory.Service, error)
    ```
  - `kit/artifact_local.go`：`NewLocalArtifactService(root string) (artifact.Service, error)`，实现 `artifact.Service` 六个方法（`Save/Load/Delete/List/Versions/GetArtifactVersion`），布局 `{root}/{app}/{user}/{session}/{file}/v{N}.{ext}` + 同目录 `meta.json`（`ArtifactVersion` 的 `CreateTime/MimeType/CustomMetadata/CanonicalURI` 落在这里，`CanonicalURI = file:///{absPath}`）。

### 4.5 L 生命周期钩子

- **设计决策**
  - 拦截点只用**已核实的真实 API**：`llmagent.Config` 的 `BeforeAgentCallbacks / BeforeModelCallbacks / AfterModelCallbacks / BeforeToolCallbacks / AfterToolCallbacks / OnModelErrorCallbacks / OnToolErrorCallbacks`，加上 `launcher.Config.PluginConfig`（内置 plugin）。
  - **人在回路四档**（逐动作落档，不允许默认全放行）：
    | 档 | 覆盖动作 | 实现 |
    |---|---|---|
    | 自动执行 | 只读 HTTP 工具（GET）、内置工具 | 无额外配置 |
    | 执行后告知 | 写操作 HTTP 工具（POST/PUT/PATCH/DELETE），落审计 | `AfterToolCallbacks` 打审计事件 |
    | 执行前确认 | `policy.destructiveTools` 命中的工具 | `functiontool.Config.RequireConfirmation=true`（ADK HITL） |
    | 直接阻断 | 未在 `allowedHosts` 内的 URL、未通过 `EnvAllow` 的环境变量 | `Build` 期拒绝，不进入运行时 |
  - **"执行前确认"必须有协议面，不能只写配置项**（初版缺这一段，等于该档不可实现）：
    - 触发：`functiontool.Config.RequireConfirmation = true`（稳定字段，非 experimental）；HTTP 工具 handler 因此返回 `tool.ErrConfirmationRequired`。
    - builder 侧：试跑 SSE 流中出现 `{"type":"confirm_required","callId":"...","tool":"cancel_order","args":{...}}`，前端弹出确认条；用户点"同意/拒绝"→ `POST /api/projects/{slug}/play/confirm {callId, approved}`；同意则续跑，拒绝则把 `tool.ErrConfirmationRejected` 作为 function response 回喂给模型（模型应改方案或告知用户，而不是卡死）。
    - 超时：确认等待 120s，超时按"拒绝"处理并记审计。
    - 生成物侧：`full`/`api` 模式走 ADK 自带的 HITL 往返（`toolconfirmation` 包）；`console` 模式在终端提示 y/N。
    - **MCP 工具的降级**：MCP toolset 的 `WithConfirmation` 与 `ConfirmationProvider` 在 v2.7.1 标为 EXPERIMENTAL，v1 不依赖它——MCP 工具落在"执行后告知"档，UI 明示"MCP 工具不支持执行前确认"。这是能力缺口，写进 §15。
  - 鉴权协议：builder 只监听 `127.0.0.1`；若用户显式 `--host 0.0.0.0`，强制要求 `ADKS_TOKEN` 环境变量并校验 `Authorization: Bearer`，否则拒绝启动。**生成物**侧映射 `launcher.Config.Authenticator`：`authn.NewNoop()` 为默认；Bearer 需用 `authn.NewCustom(authn.AuthenticatorFunc(...))` 自己实现——`server/authn` **没有现成的 Bearer 构造器**（只有 `NewNoop/NewHeader/NewCustom/NewGoogleOIDC/NewIdentityAwareProxy`）。UI 里明示"面向公网必须换成 GoogleOIDC 或自建鉴权服务"。
  - 纵深：URL host 白名单在 `Build` 期校验 + handler 内二次校验（防 DNS rebinding，与 ADK web 的 Host 检查同思路）。
- **落地规格**
  - `kit/callbacks.go`：`NewAuditCallbacks(sink *AuditSink) []llmagent.AfterToolCallback`，审计事件写 JSONL 到 `./.adks/audit.jsonl`，字段：`ts/sessionID/tool/argsHash/ok/durMs/truncated`。
  - 审计**只记哈希不记明文参数**（可能含 PII），debug 模式才记原文且需显式 `ADKS_AUDIT_VERBOSE=1`。

### 4.6 V 评估接口

- **设计决策**
  - 生成物的"做对了吗"由**确定性规则**判定，不用模型自评：`go build` + `go vet` 退出码 + 冒烟 run 是否产出 `final` 事件。
  - builder 的"配置对吗"三档：① schema 校验（确定性）；② 连通性探测（MCP listTools / SQL ping / model 1-token）；③ 冒烟 run（真实一轮，断言有 final event 且无 `error` 事件）。三档全绿才允许在 UI 点"导出"。
  - 生成者与评估者分离（builder 场景）：**builder 校验的是自己渲染的产物**，理论上存在"自证"风险，因此导出后额外跑一次**外部** `go vet`（独立进程，不看模板上下文），这是本项目能拿到的最强独立性。更彻底的模型裁判在首版不引入（无网络依赖要求）。
  - 工具回执 ≠ 业务事实：HTTP 工具把 `2xx` 判成功，但审计里记 `status` 而非 `ok:true`，UI 明示"200 不代表业务成功"。
- **落地规格**
  - `internal/verify/verify.go`：
    ```go
    type Check struct{ Name, Kind string; OK bool; Detail string; Dur time.Duration }
    func Run(ctx context.Context, dir string) ([]Check, error) // go build / go vet / smoke run
    ```
  - 冒烟 run：用生成的二进制起子进程，`-port=0`（或固定随机高位端口），POST `/api/run`，断言响应 JSON 里有 `content.parts[].text`，30s 超时后 kill 整个进程组。

---

## 5. 模型组合与任务分派

- **形态判定**：A（单智能体 + 单模型）。
- **对"为什么不选 B/C"的显式质疑记录**：见 §3.1 末段。核心一句：**分档是业务判断，不是脚手架判断**；脚手架若默认分档，用户会得到一份自己看不懂成本结构的产物。
- **任务-模型分派表**（生成物视角，档位由用户在 UI 选，此处给默认映射）：

| 步骤 | 档位 | 理由 | 成本/延迟预算 |
|---|---|---|---|
| 意图理解 + 工具选择 + 生成回复 | 用户选定模型（默认 `gemini-flash-latest`） | **本设计不拆分**——ADK 有 `workflowagents`、自定义 agent、`BeforeModelCallback` 可改请求等多种拆分手段，但拆分把"哪个步骤该便宜"的业务判断塞进了模板；不是框架做不到 | 首轮输入 = instruction + 工具 schema + 用户消息；输出受 `maxOutputTokens` 约束 |
| 工具执行 | 确定性代码 | 无模型参与 | 单工具 ≤20s |
| 循环预算判定 | 确定性代码 | callback 计数 | O(1) |
| 配置校验 / 连通性探测 | 确定性代码 | 无模型参与 | ≤5s 总预算 |

- **token 预算按档位分配**：不适用（单档 100%）。builder 侧给「首轮输入粗估」，**按字符类别分别折算**（中文与英文的字符/token 比差 2 倍以上，用单一系数会系统性低估中文）：
  ```go
  // ascii 约 3.8 字符/token；CJK 及全角约 1.7 字符/token。
  // 这两个系数是公开经验的量级估计，不是官方值；UI 必须标注"粗估，仅供容量判断，不作计费依据"。
  est := float64(asciiCount)/3.8 + float64(nonASCIICount)/1.7
  ```
  UI 只显示"约 N tokens（粗估）"与字符数原值，不显示金额。
- **缓存与重算账**：命中率目标由用户模型供应商决定，builder 能控制的是"不改就不重写"——manifest 未改则 instruction 与工具 schema 字节不变（§4.3 五条规则）。重写触发点三个：改 instruction、增删工具、改模型。UI 在这三处显示"将失效提示前缀缓存"。
- **级联升级条件**：不适用（无分档）。
- **降级链**：模型不可用（429/5xx）→ 在 `OnModelErrorCallbacks` 里把错误原样回喂并允许重试 1 次；仍失败 → 清理并抛到 SSE。**v1 不做自动换模型，也不做"可见降级"**——评审指出"换模型 + UI 明示"对可用性是更优解，这一点成立；不做的理由是首版要保证成本可预测与回答风格稳定，而换模型会同时改变这两者。登记为「有意不做」（§12），P2 以 `policy.fallbackModel` 形式提供，开关默认关且必须用户勾选。
- **评估者档位**：不适用（无模型评估者），评估全部走确定性规则。

---

## 6. 多 Agent 编排

**状态更新（v0.3，勘误）**：原文写「不适用，替换主循环等于另写一个 ADK」。**这个理由是错的**，已于 v0.3 修正并实现。

事实是：ADK Go 的 `google.golang.org/adk/v2/workflow` 本身就是 graph 引擎，`workflowagent.New(Config{Edges})` 直接返回 `agent.Agent`，与单 agent 路径交出的是同一个类型。graph 是 `[]workflow.Edge` **纯数据结构**，编译期一次性装配，不解释、不运行时求值——它完全适配「配置生成」，与「另写一个 ADK」无关。

v0.3 落地内容：

| 维度 | 实现 |
|---|---|
| manifest | `spec.mode: llm\|graph`，`spec.graph.{nodes,edges}` |
| 节点种类 | `agent` / `tool` / `route` / `join` / `subgraph`（嵌套图） |
| 边 | 无条件边 + `route` 标签条件边（`StringRoute`） |
| 条件分支 | 自研 `routeNode`：读上游输出，按 `equals\|contains\|prefix\|suffix\|regex\|always` 打标签 |
| 容错 | 节点级 `timeoutSec` / `retry` / `each`（并行项 worker） |
| 校验 | 环检测、可达性、START 出度、节点入边、死分支、无兜底、route 标签一致性 |
| 前端 | 节点/边表单编辑 + **只读拓扑预览**（分层布局，实时反映编辑） |

**仍然不做的**：拖拽画布（YAML + 表单 + 只读拓扑图已够用）；LLM 节点输出直接决定分支（`AgentNode` 不设 `Event.Routes`，必须插 `route` 节点，这是配置化唯一的硬缺口，已在产物边界里写明）。

| 步 | 结论 |
|---|---|
| 是否真的需要 | ~~否~~ **是**。实测证明 graph 能纯配置生成，且端到端条件路由可用（见 `kit/graph_test.go:TestGraphRoutesConditionally`） |
| 拓扑 | 不适用（单 Agent） |
| 委派契约 | 不适用 |
| 隔离边界 | 不适用 |
| 模型档位（每个子 agent 的档位与升级条件） | 不适用（单档，见 §3.1） |
| 超时与熔断 | 单 Agent 已配 `maxLlmCalls` / `maxWallClockSec` / 工具级 `timeoutSec` |
| 反模式自查 | 不适用（无子 agent，不存在共享可变状态 / 无界扇出 / 过度分解） |
| 评估面（任务集 / 路由准确率 / 嵌套循环账 / 回传损失） | 不适用。替代评估面 = builder 的三档确定性校验（§4.6）+ 生成物冒烟 run；无路由准确率可测（无 orchestrator），无嵌套循环（单循环），无回传（无 handoff） |

---

## 7. 模块 / 目录结构

```
E:/adks/
├── cmd/adks/main.go              # 单二进制入口：adks serve | adks export | adks version
├── internal/
│   ├── api/                      # HTTP 处理器（net/http ServeMux）
│   │   ├── router.go             # 路由注册 + 本地鉴权中间件
│   │   ├── projects.go           # 项目 CRUD
│   │   ├── manifest.go           # manifest 读写 + 校验
│   │   ├── probe.go              # MCP / SQL / model 连通性探测
│   │   ├── play.go               # 试跑 SSE
│   │   └── export.go             # 预览文件树 + 导出 zip/目录
│   ├── store/                    # 文件系统项目存储 + 版本快照
│   │   ├── store.go              # Project/Load/Save/List/Delete/Snapshot/Rollback
│   │   └── paths.go              # ~/.adks 路径解析（可用 ADKS_HOME 覆盖）
│   ├── manifest/                 # manifest 类型 + 校验 + 默认值
│   │   ├── types.go              # 见 §9.3
│   │   ├── validate.go           # 纯函数校验，返回 []Issue
│   │   └── defaults.go           # 缺省填充（指针字段 → 具体值）
│   ├── codegen/                  # 渲染生成物
│   │   ├── render.go             # Render(p *manifest.Project) (map[string][]byte, error)
│   │   ├── templates/            # main.go.tmpl / go.mod.tmpl / Dockerfile.tmpl / README.md.tmpl
│   │   └── kit/                  # ← 与下面 kit/ 同一份源码（go:embed 原样拷出）
│   ├── verify/verify.go          # go build / go vet / 冒烟 run
│   └── probe/                    # mcp.go / sql.go / model.go（探测与运行时解耦）
├── kit/                          # ★ 核心：manifest → ADK 对象。builder 直接 import，导出时原样拷走
│   ├── manifest.go               # ParseManifest([]byte) (*manifest.Project, error)
│   ├── build.go                  # Build(ctx, p) (*Layer, error)  ← 唯一编译入口
│   ├── model.go                  # newModel(ModelSpec) (model.LLM, error)
│   ├── tools_builtin.go          # 内置工具映射表
│   ├── tools_http.go             # 声明式 HTTP 工具（补 ADK Go 空缺）
│   ├── tools_mcp.go              # MCP toolset + Closer 登记
│   ├── callbacks.go              # 限额 / 打转检测 / 审计
│   ├── services.go               # session / artifact / memory 构造
│   ├── artifact_local.go         # 本地文件制品服务
│   ├── context.go                # 消毒 / 截断 / 确定性排序
│   └── layer.go                  # Layer 结构与 Close
├── web/                          # embed 的静态资源
│   ├── index.html
│   ├── app.js                    # Alpine 组件
│   └── styles.css
├── designs/                      # 设计文档（本文件）
└── docs/vendor/adk-docs/         # 官方文档仓库快照（离线参考，实施第 0 步 clone）
```

**关键结构约束**：`kit/` 只允许 import 标准库 + ADK + `jsonschema-go` + `gorm` + `manifest` 包；**禁止 import 任何 `internal/` 下的 builder 代码**。这条约束是"导出即可独立编译"的物理保障，用一条 CI 规则（`go list -deps ./kit | grep internal/` 必须为空）自动检查。

---

## 8. 关键接口签名

### 8.1 编译器（builder 与生成物共用）

```go
package kit

// ParseManifest 解析并做纯语法层校验（不做连通性探测）。
func ParseManifest(raw []byte) (*manifest.Project, error)

// Build 把 manifest 编译成一份可直接交给 launcher 的 Layer。
// 顺序固定：validate → secrets → model → tools(排序) → callbacks → agent → services → plugins。
// 任一环节失败必须回滚已建立资源（MCP 子进程），不得泄漏。
func Build(ctx context.Context, p *manifest.Project) (*Layer, error)

// Layer 是一次编译的产物，持有全部可关闭资源。
type Layer struct {
	Manifest *manifest.Project
	Agent    agent.Agent
	Loader   agent.Loader        // agent.NewSingleLoader(Agent)
	Session  session.Service
	Artifact artifact.Service
	Memory   memory.Service
	Plugins  runner.PluginConfig
	Closers  []io.Closer         // 只收 MCPHandle.Raw 等"原始可关闭对象"，禁止收 FilterToolset 包装器；Close 逆序执行
}

// Close 逆序关闭 Closers，聚合错误（errors.Join）。幂等。
func (l *Layer) Close(ctx context.Context) error

// Layer 的运行入口：由 launcher 或 builder 的试跑调用。
func (l *Layer) LauncherConfig() *launcher.Config
```

### 8.2 工具接口

```go
// 声明式 HTTP 工具：把 manifest.HTTPSpec 变成一个 tool.Tool。
func NewHTTPTool(spec manifest.HTTPSpec, deps HTTPToolDeps) (tool.Tool, error)

type HTTPToolDeps struct {
	HTTPClient *http.Client      // 由 Build 注入，带超时与 host 白名单 RoundTripper
	Secrets    map[string]string // secretEnv → 值，Build 期解析，不进日志
	Audit      *AuditSink
}

// MCP：返回一个句柄，明确区分"可关闭的原始实例"与"给 Agent 的过滤视图"。
// 官方约束：tool.FilterToolset 返回的是包装器，Close 它不会释放连接，必须关 Raw。
func NewMCPHandle(ref manifest.MCPRef, deps MCPDeps) (*MCPHandle, error)

type MCPHandle struct {
	ID      string
	Raw     tool.Toolset // mcptoolset.New 的直接返回值，实现 io.Closer，唯一 Close 目标
	Exposed tool.Toolset // FilterToolset(Raw, AllowedToolsPredicate(ref.ToolAllow))；无过滤时 == Raw
}

// Close 只关闭 Raw；幂等；不关闭 Exposed。Build 失败时也必须调用（防子进程泄漏）。
// 注意类型：mcptoolset.New 的返回类型是 tool.Toolset，io.Closer 是运行期能力，
// 因此实现里必须做 `c, ok := h.Raw.(io.Closer); if ok { c.Close() }`，
// 断言失败视为配置错误（返回明确 error 而不是静默跳过）。
func (h *MCPHandle) Close() error

type MCPDeps struct {
	Headers map[string]string // 已展开的鉴权头
	Base    http.RoundTripper // 复用连接池
}
```

### 8.3 钩子接口

```go
// 限额：返回非 nil 的 model.LLMResponse 既跳过真实模型调用（ADK 行为）。
func NewLimitCallbacks(lim manifest.LimitsSpec) ([]llmagent.BeforeModelCallback, error)

// 打转检测：连续 3 次相同 (toolName, argsHash) 则返回错误终止该次调用。
func NewLoopGuard() llmagent.BeforeToolCallback

// 审计：写 JSONL，只记参数哈希。
func NewAuditCallbacks(sink *AuditSink) ([]llmagent.AfterToolCallback, error)

type AuditSink struct{ Path string; Verbose bool }
func (s *AuditSink) Write(ev AuditEvent) error

type AuditEvent struct {
	TS        time.Time `json:"ts"`
	SessionID string    `json:"sessionId"`
	Tool      string    `json:"tool"`
	ArgsHash  string    `json:"argsHash"`
	Status    int       `json:"status"`      // HTTP 状态，非 HTTP 工具为 0
	OK        bool      `json:"ok"`
	DurMs     int64     `json:"durMs"`
	Truncated bool      `json:"truncated"`
	Args      any       `json:"args,omitempty"` // 仅 Verbose=true 时非空
}
```

### 8.4 builder 侧 REST 接口

| 方法 | 路径 | 入参 | 出参 |
|---|---|---|---|
| GET | `/api/projects` | — | `[{slug,name,updated,model}]` |
| POST | `/api/projects` | `{slug,name}` | `{slug}` |
| GET | `/api/projects/{slug}` | — | `{manifestYaml, issues[]}` |
| PUT | `/api/projects/{slug}` | `{manifestYaml}` | `{issues[]}`（含 lint 警告） |
| POST | `/api/projects/{slug}/probe/mcp` | `{mcpId}` | `{tools:[{name,description,params}], durMs}` |
| POST | `/api/projects/{slug}/probe/sql` | `{backend,dsn}` | `{ok, detail}` |
| POST | `/api/projects/{slug}/probe/model` | — | `{ok, model, detail}` |
| POST | `/api/projects/{slug}/play` | `{sessionId, message}` → SSE | `event: {type: text\|tool_call\|tool_result\|confirm_required\|error\|done, ...}` |
| POST | `/api/projects/{slug}/play/confirm` | `{callId, approved}` | `{ok}`；拒绝 → SSE 推 `tool_result{ok:false}`；**超时 → 同样推 `tool_result{ok:false, code:"CONFIRM_TIMEOUT"}`（不用 error 事件，前端确认条与工具结果复用同一分支）** |
| GET | `/api/projects/{slug}/files` | — | `{files:[{path,bytes,preview}]}` |
| POST | `/api/projects/{slug}/verify` | — | `{checks:[Check]}` |
| POST | `/api/projects/{slug}/export` | `{target, zip}` | `{dir}` 或 `application/zip` 流 |
| POST | `/api/projects/{slug}/rollback` | `{version}` | `{ok}` |

---

## 9. 核心数据结构

### 9.1 状态结构（builder 侧）

```go
// 存储布局（文件系统，无数据库）
// ~/.adks/
//   index.json                       // {projects:[{slug,name,updated,manifestHash}]}
//   projects/{slug}/agent.yaml       // 真相源
//   projects/{slug}/versions/{RFC3339}.yaml  // 每次显式保存一份快照（最多保留 50 份）
//   projects/{slug}/.adks/           // 试跑期产生的 sessions.db / artifacts / audit.jsonl
//   exports/{slug}-{ts}/             // 导出产物（可选保留）

type ProjectIndex struct {
	Projects []ProjectMeta `json:"projects"`
}
type ProjectMeta struct {
	Slug         string    `json:"slug"`
	Name         string    `json:"name"`
	Updated      time.Time `json:"updated"`
	ManifestHash string    `json:"manifestHash"` // sha256(agent.yaml)，用于判断试跑缓存是否失效
}
```

- 并发写：单进程 + 每次保存 `write tmp → fsync → rename`（原子替换），无需锁；同 slug 的保存串行化用 `sync.Map[slug]*sync.Mutex`。
- 回滚：`versions/` 里的快照直接覆盖 `agent.yaml`，覆盖前先给当前版本也留一份快照（避免回滚丢现场）。

### 9.2 消息 / 任务结构（SSE 事件）

```go
type PlayEvent struct {
	Type   string         `json:"type"` // text | tool_call | tool_result | confirm_required | error | done
	Text   string         `json:"text,omitempty"`
	Tool   string         `json:"tool,omitempty"`
	CallID string         `json:"callId,omitempty"` // confirm_required / tool_result 时必填，用于确认往返
	Args   map[string]any `json:"args,omitempty"`   // confirm_required 时给出，供人判断
	OK     *bool          `json:"ok,omitempty"`
	Code   string         `json:"code,omitempty"` // WALL_CLOCK | LLM_LIMIT | LOOP | CONFIG | CONFIRM_TIMEOUT
	DurMs  int64          `json:"durMs,omitempty"`
}
```

### 9.3 配置结构（manifest，Go 定义）

```go
package manifest

type Project struct {
	APIVersion string   `yaml:"apiVersion"` // 固定 "adks/v1"
	Kind       string   `yaml:"kind"`       // 固定 "AgentProject"
	Metadata   Metadata `yaml:"metadata"`
	Spec       Spec     `yaml:"spec"`
}

type Metadata struct {
	ID      string    `yaml:"id"`      // "prj_" + 12 位 base32
	Slug    string    `yaml:"slug"`    // ^[a-z][a-z0-9-]{1,40}$
	Name    string    `yaml:"name"`
	Created time.Time `yaml:"created"`
	Updated time.Time `yaml:"updated"`
}

type Spec struct {
	Agent   AgentSpec   `yaml:"agent"`
	Tools   ToolsSpec   `yaml:"tools,omitempty"`
	Runtime RuntimeSpec `yaml:"runtime"`
	Plugins []PluginRef `yaml:"plugins,omitempty"`
	Policy  PolicySpec  `yaml:"policy,omitempty"`
}

type AgentSpec struct {
	Name        string    `yaml:"name"`        // ^[a-z][a-z0-9_]{1,63}$，不能是 "user"
	Description string    `yaml:"description"` // ≤200 字
	Instruction string    `yaml:"instruction"`
	Model       ModelSpec `yaml:"model"`
	Generation  *GenSpec  `yaml:"generation,omitempty"`
}

type ModelSpec struct {
	Provider  string `yaml:"provider"`           // gemini | openai | anthropic
	Name      string `yaml:"name"`               // 如 gemini-flash-latest
	APIKeyEnv string `yaml:"apiKeyEnv"`          // 默认 GOOGLE_API_KEY / OPENAI_API_KEY / ANTHROPIC_API_KEY
	BaseURL   string `yaml:"baseURL,omitempty"`  // 仅 openai 兼容端点使用
}

type GenSpec struct {
	Temperature     *float32 `yaml:"temperature,omitempty"`     // 0..2
	TopP            *float32 `yaml:"topP,omitempty"`            // 0..1
	MaxOutputTokens *int32   `yaml:"maxOutputTokens,omitempty"` // 1..8192
}

type ToolsSpec struct {
	Builtin    []string  `yaml:"builtin,omitempty"`    // google_search|url_context|exit_loop|load_artifacts|preload_memory
	HTTP       []HTTPSpec `yaml:"http,omitempty"`
	MCP        []MCPRef   `yaml:"mcp,omitempty"`
	ToolSearch bool       `yaml:"toolSearch,omitempty"` // 开=用 tool/toolsearch 折叠披露；与任何 ToolAllow 互斥（§15 #10）
}

type HTTPSpec struct {
	Name         string      `yaml:"name"`        // ^[a-z][a-z0-9_]{1,63}$，全局唯一
	Description  string      `yaml:"description"` // 必填，≥15 字
	Method       string      `yaml:"method"`      // GET|POST|PUT|PATCH|DELETE
	URL          string      `yaml:"url"`         // 可含 {param} 占位
	Params       []ParamSpec `yaml:"params,omitempty"`
	Auth         *AuthSpec   `yaml:"auth,omitempty"`
	AllowedHosts []string    `yaml:"allowedHosts"` // 必填，如 ["api.example.com"]
	TimeoutSec   int         `yaml:"timeoutSec,omitempty"`   // 默认 20
	MaxRespBytes int         `yaml:"maxRespBytes,omitempty"` // 默认 65536
	Destructive  bool        `yaml:"destructive,omitempty"`  // 命中 policy.confirmation=destructive 时需确认
}

type ParamSpec struct {
	Name        string   `yaml:"name"`
	Type        string   `yaml:"type"` // string|integer|number|boolean|array_of_string
	In          string   `yaml:"in"`   // path|query|body
	Required    bool     `yaml:"required"`
	Description string   `yaml:"description"`
	Enum        []string `yaml:"enum,omitempty"`
}

type AuthSpec struct {
	Scheme    string `yaml:"scheme"`    // bearer|apikey-header|basic
	SecretEnv string `yaml:"secretEnv"` // 环境变量名；值永不落盘
	Header    string `yaml:"header,omitempty"` // apikey-header 时的头名
}

type MCPRef struct {
	ID         string            `yaml:"id"`        // ^[a-z][a-z0-9-]{1,20}$，项目内唯一
	Transport  string            `yaml:"transport"` // stdio|http|sse
	Command    string            `yaml:"command,omitempty"`
	Args       []string          `yaml:"args,omitempty"`
	EnvAllow   []string          `yaml:"envAllow,omitempty"` // 透传给子进程的环境变量白名单
	URL        string            `yaml:"url,omitempty"`
	Headers    map[string]string `yaml:"headers,omitempty"` // 值支持 ${ENV}
	ToolAllow  []string          `yaml:"toolAllow,omitempty"`
	TimeoutSec int               `yaml:"timeoutSec,omitempty"` // 默认 20
}

type RuntimeSpec struct {
	Server   ServerSpec   `yaml:"server"`
	Session  SessionSpec  `yaml:"session"`
	Memory   BackendSpec  `yaml:"memory"`
	Artifact ArtifactSpec `yaml:"artifact"`
	Compaction *CompactionSpec `yaml:"compaction,omitempty"`
}

type ServerSpec struct {
	Mode       string `yaml:"mode"` // full|api|console
	Host       string `yaml:"host"` // 默认 127.0.0.1
	Port       int    `yaml:"port"` // 默认 8080
	PathPrefix string `yaml:"pathPrefix,omitempty"`
	A2A        bool   `yaml:"a2a,omitempty"`
	Auth       string `yaml:"auth,omitempty"` // none|bearer
}

type SessionSpec struct {
	Backend      string `yaml:"backend"` // memory|sqlite|postgres|mysql
	DSN          string `yaml:"dsn"`
	AutoMigrate  bool   `yaml:"autoMigrate"`   // 默认 true
	MaxOpenConns int    `yaml:"maxOpenConns,omitempty"`
}

type BackendSpec struct{ Backend string `yaml:"backend"` } // memory

type ArtifactSpec struct {
	Backend string `yaml:"backend"` // memory|local
	Dir     string `yaml:"dir,omitempty"`
}

type CompactionSpec struct {
	Enabled bool `yaml:"enabled"`
	// 具体字段透传 ADK compaction.Config；首版只暴露 Enabled 与 SummarizerModel。
	SummarizerModel string `yaml:"summarizerModel,omitempty"`
}

type PluginRef struct {
	Name   string            `yaml:"name"`
	Kind   string            `yaml:"kind"` // builtin.logging|builtin.retryreflect|audit.webhook
	Config map[string]string `yaml:"config,omitempty"`
}

type PolicySpec struct {
	Confirmation     string     `yaml:"confirmation"` // none|destructive|all
	DestructiveTools []string   `yaml:"destructiveTools,omitempty"`
	Limits           LimitsSpec `yaml:"limits"`
}

type LimitsSpec struct {
	MaxLLMCalls         int `yaml:"maxLlmCalls"`         // 默认 20
	MaxWallClockSec     int `yaml:"maxWallClockSec"`     // 默认 120
	MaxToolConcurrency  int `yaml:"maxToolConcurrency"`  // 默认 4
	MaxInstructionBytes int `yaml:"maxInstructionBytes"` // 默认 32768
}
```

---

## 10. 配置文件格式（完整示例）

```yaml
# ~/.adks/projects/support/agent.yaml
apiVersion: adks/v1
kind: AgentProject
metadata:
  id: prj_7f3k9m2q1x4b
  slug: support                     # 项目标识，也是导出目录名
  name: 客服工单助手
  created: 2026-11-13T21:30:00+08:00
  updated: 2026-11-13T21:30:00+08:00

spec:
  agent:
    name: support_agent             # ADK agent 名，树内唯一，不能是 "user"
    description: 查询订单状态并回答售后问题
    instruction: |
      你是电商客服助手。回答前必须先调用 get_order 查询真实订单状态，不得凭记忆回答。
      查询失败时告知用户"暂时无法查询"，不要编造订单信息。
    model:
      provider: gemini
      name: gemini-flash-latest
      apiKeyEnv: GOOGLE_API_KEY     # 值来自环境变量，不进文件
    generation:
      temperature: 0.3
      maxOutputTokens: 2048

  tools:
    builtin:
      - google_search
      - load_artifacts
    http:
      - name: get_order
        description: 按订单号查询订单的当前状态、金额与物流节点
        method: GET
        url: https://api.example.com/v1/orders/{order_id}
        allowedHosts: [api.example.com]
        timeoutSec: 15
        maxRespBytes: 32768
        auth:
          scheme: bearer
          secretEnv: ORDER_API_TOKEN
        params:
          - name: order_id
            type: string
            in: path
            required: true
            description: 订单号，形如 SO20261113001
      - name: cancel_order
        description: 取消指定订单（写操作，会真实改变业务状态）
        method: POST
        url: https://api.example.com/v1/orders/{order_id}/cancel
        allowedHosts: [api.example.com]
        destructive: true            # 命中 policy.confirmation 时需要用户确认
        params:
          - name: order_id
            type: string
            in: path
            required: true
            description: 订单号
          - name: reason
            type: string
            in: body
            required: false
            description: 取消原因，最多 200 字
    mcp:
      - id: fs
        transport: stdio
        command: npx
        args: ["-y", "@modelcontextprotocol/server-filesystem", "./data"]
        envAllow: [PATH, HOME]       # 只透传白名单变量，凭证不进子进程
        timeoutSec: 20
        toolAllow: [list_directory, read_file]   # 只披露两个只读工具
      - id: kb
        transport: http
        url: https://mcp.example.com/mcp
        headers:
          Authorization: "Bearer ${KB_MCP_TOKEN}"  # 运行时展开
        toolAllow: [search_knowledge]

  runtime:
    server:
      mode: full                     # full=console+api+webui；api=仅 REST；console=仅终端
      host: 127.0.0.1
      port: 8080
      pathPrefix: /api
      a2a: false
      auth: none
    session:
      backend: sqlite                # memory|sqlite|postgres|mysql
      dsn: "file:./.adks/sessions.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
      autoMigrate: true
      maxOpenConns: 1
    memory:
      backend: memory
    artifact:
      backend: local
      dir: ./.adks/artifacts
    compaction:
      enabled: false                 # 开启会失效前缀缓存，且多一次 LLM 摘要调用
      summarizerModel: gemini-flash-latest

  plugins:
    - name: logging
      kind: builtin.logging
    - name: retry
      kind: builtin.retryreflect
      config:
        maxRetries: "2"
    - name: audit
      kind: audit.webhook
      config:
        url: "https://audit.example.com/ingest"
        events: "tool.after"

  policy:
    confirmation: destructive        # none|destructive|all
    destructiveTools: [cancel_order]
    limits:
      maxLlmCalls: 20
      maxWallClockSec: 120
      maxToolConcurrency: 4
      maxInstructionBytes: 32768
```

---

## 11. 层间交叉检查

| 关系 | 说明 |
|---|---|
| T → C | MCP 一个 server 可挂 40+ 工具，工具定义全部进 prompt 前缀。因此 `toolAllow` 不只是权限，也是**上下文预算手段**；>30 条时 UI 告警（§4.2） |
| T → L | `destructive` 标记（T 层）与 `policy.confirmation`（L 层）必须联动：标记了但 policy=none 时，UI 显示红色"写操作无确认" |
| S → E | SQLite 单写 → 同 session 并发 run 必须串行。builder 试跑对同 `sessionId` 加互斥，否则 SQLite `database is locked` 会以工具错误形式被模型看到 |
| S → V | 冒烟 run 用的 session 后端必须与导出配置一致，否则"builder 里能跑、导出后跑不了"（典型：builder 用 memory，导出用 postgres 但 DSN 错） |
| E → C | 每次 run 重读 session events（每轮重组装）；但 instruction 与工具 schema 来自 manifest，不改则字节不变（前缀稳定） |
| L → V | 审计记录（L）是 V 层"工具回执 ≠ 业务事实"的唯一证据来源 |
| C → 缓存 | 前缀稳定的五个支点（§4.3 五条规则）：① instruction 禁时间戳 ② 工具按名排序 ③ map 遍历先排序 ④ embed YAML 原文 ⑤ `PropertyOrder` + golden 快照。缺任一处则命中率归零 |
| P → 全部 | 声明式配置使 E 层不可替换、T 层按 kind 扩展、L 层只能挂载不能改流程——这是刻意的收敛，防止脚手架长成一个半成品框架 |

---

## 12. 六层决策表

> 状态口径：**只有 §1.1 的四项是用户拍板（已确认）**；六层决策是本设计的专业判断，一律标「默认假设（可推翻）」。初版把六行全标"已确认"，是把假设包装成结论，已修正。

| 层 | 决策 | 依据 | 状态 | 是否有意不做（及理由） |
|---|---|---|---|---|
| E | 复用 ADK 官方 runtime 与 agent 内的 tool-calling flow，只加三层外部保险（调用数/墙钟/工具并发）+ 打转检测 | `runner.Config` 无调用数预算字段；自行实现循环等于分叉框架（ADK 有 `workflowagents` 一族，但替换主循环不是"配置"而是"另写一个 ADK"） | 默认假设 | 不做动态脚本编排（任务结构清晰，属过度设计）；不做自动换模型降级（见 §5，P2 以开关形式提供） |
| T | **五**形态并列：内置 / 声明式 HTTP（自研）/ MCP / Skill（P2）/ **工具延迟加载 `toolsearch`（开关）**；注册≠披露≠授权三分离 | Go 版无 OpenAPI toolset（仅 Python 有），HTTP 工具是硬缺口；MCP 的 `toolAllow` 兼作上下文预算；>30 条工具时用官方 `tool/toolsearch` 折叠而不是砍能力 | 默认假设 | 不做 UI 内写 Go 函数的工具（需在 builder 里造编译器，风险与收益不匹配） |
| C | 预算分配 + **五条**前缀稳定规则（含 `PropertyOrder` + golden 字节快照）+ 默认关压缩 + 工具输出截断包裹 | Go map 遍历随机、时间戳进 instruction、`jsonschema` 属性渲染顺序未定，是前缀失效的三个真实来源 | 默认假设 | 不做自动摘要压缩（默认关，用户显式开，且提示"会失效缓存并多一次 LLM 调用"） |
| S | 三后端独立可换；SQLite 单写 + WAL；本地制品服务自研；长期记忆首版只用 memory | 已核实：Go 版仅 `artifact.InMemoryService` 与 `gcsartifact` 两个持久化实现，本地制品是缺口 | 默认假设 | 不做长期记忆的 SQL 后端（需可信度/归属/失效治理，不宜默认开） |
| L | 只用真实 callback 字段 + `launcher.Config.PluginConfig`；四档 HITL 逐动作落档；**确认往返落到 `confirm_required` 事件 + `/play/confirm` 端点**；只监听 loopback | 拦截点必须用真实 API；HITL 若无协议面则该档不可实现（初版缺，已补） | 默认假设 | MCP 工具**不支持执行前确认**（`WithConfirmation` 为 EXPERIMENTAL），只到"执行后告知"；不做 builder 自身多用户鉴权（`--host 0.0.0.0` 时用单 token 兜底） |
| V | 确定性规则三档（`go build` / `go vet` / 冒烟 run）；生成者与评估者靠**独立进程**隔离 | 无 ground truth 的评分不可自动化，不假装能评 | 默认假设 | 不做模型裁判评审（首版零网络依赖）；**不宣称进程隔离等于语义独立**——它只证明"能编译能跑通"，不证明"配置对"，后者靠 §14 三档探测 + 人工 |

---

## 13. 生产级必答项

| # | 必答项 | 结论 |
|---|---|---|
| 1 | 执行环境：工具在哪跑、凭证是否进执行环境 | HTTP 工具在本进程内跑（`http.Client` + host 白名单 RoundTripper）；MCP stdio 在**子进程**跑，`exec.Cmd.Env` 只透传 `EnvAllow` 白名单，**凭证不进子进程**；MCP 远程工具的 token 只在内存 header 里 |
| 2 | 错误回喂：失败如何作为观察回到上下文、如何分类恢复 | 工具错误返回结构化对象 `{error,status}` 而非 Go error（避免被框架吞成不可读文本）；瞬时 5xx/超时重试 1 次（200ms + 抖动）；4xx 不重试直接回喂；缺密钥在 Build 期失败，不进循环 |
| 3 | 打转检测与熔断：如何识别原地打转、如何打断 | `BeforeToolCallback` 内 `(toolName, sha256(args))` 环，连续 3 次相同即返回终止错误；另有 `maxLlmCalls` / `maxWallClockSec` 双保险 |
| 4 | 记忆治理：可信度、归属、失效条件 | 首版长期记忆仅 memory 后端、仅 `user:` 作用域、进程重启失效；**未验证的模型推测不会被写进记忆**（不提供 write-memory 工具），因此不存在"推测升级为事实"的路径 |
| 5 | 外部副作用幂等 | 写操作 HTTP 工具自动带 `Idempotency-Key: {sessionId}:{tool}:{argHash}`；不支持幂等键的端点由用户在 UI 标注"重试不安全"，此时不重试 |
| 6 | 证据版本绑定 | 每次导出在 `README.md` 写 `manifestHash + adkVersion + kitVersion`；`go.mod` 锁定 ADK 次版本。版本变更后旧冒烟结论失效（builder 的"已验证"标记按 manifestHash 判定，hash 变即清空） |
| 7 | 生成者与评估者分离 | builder 渲染 → **独立进程** `go build`/`go vet`/冒烟 run，评估方不看模板与渲染上下文，只看产物目录与退出码 |
| 8 | 前缀稳定与缓存命中 | 五条规则（§4.3）；重写触发点三个（改 instruction / 增删工具 / 换模型），UI 在三处显式提示"将失效前缀缓存"；命中率观测由用户在模型供应商侧看，builder 不伪造指标 |

---

### 13.1 可编码三门槛自检

| 门槛 | 自检结论 |
|---|---|
| **类型完整** | 本文出现并被引用的每个类型都在 §9.1 / §9.2 / §9.3 或 §8.1~8.3 给出了字段定义：`Layer`、`AuditSink`、`AuditEvent`、`HTTPToolDeps`、`MCPDeps`、`Check`、`PlayEvent`、`ProjectIndex`、`ProjectMeta` 及全部 manifest 类型。ADK 侧类型（`llmagent.Config`、`functiontool.Config`、`mcptoolset.Config`、`artifact.Service`、`launcher.Config`、`tool.Tool/Toolset/Predicate`）均按 v2.7.1 实际定义引用，未自定义同名影子类型 |
| **API 真实** | 已逐个核对：`llmagent.New(Config)` / `llmagent.Config` 全字段 / `functiontool.New[TArgs,TResults](Config, Func)` 含 `InputSchema *jsonschema.Schema` / `mcptoolset.New(Config)` 返回实现 `io.Closer` 的 `tool.Toolset` / `tool.AllowedToolsPredicate` / `tool.FilterToolset` / `tool.WithConfirmation` / `session/database.NewSessionService(gorm.Dialector, ...gorm.Option)` 与 `AutoMigrate(session.Service)` / `artifact.Service` 六方法 / `artifact.InMemoryService()` / `launcher.Config` 全字段 / `launcher.Launcher.Execute(ctx, *Config, []string)` / `agent.NewSingleLoader` / `jsonschema.Schema` 的 `Type/Properties/Required/Items`。**三处未核实项已列 §15 #1~#3 并给出占位方案**，不在此假装确定 |
| **标准可达** | ① "导出 ≤2s" → `go test -bench=BenchmarkRender -count=100` 取 p95，渲染为纯内存 strconv/text-template，无 IO；② "试跑首字节 ≤1.5s" → SSE 首帧时间戳 − 请求时间戳，依赖模型首 token 延迟，实测通过则记基线、不达标则如实标注受模型侧限制；③ "MCP 探测 ≤3s" → `context.WithTimeout(3s)` 硬超时，可确定达成；④ "最短 10 分钟从零到能对话" → 人工计时 3 次取中位数，作为可用性验收而非代码断言 |

## 14. 参考架构图

```
┌─────────────────────────── adks（单二进制） ───────────────────────────┐
│                                                                        │
│  ┌── Web UI (embed) ──┐      ┌──────────── internal/api ────────────┐  │
│  │ 项目列表           │◄────►│ /projects  /probe  /play  /export    │  │
│  │ Agent 表单         │ HTMX │ /verify                              │  │
│  │ 工具/MCP/SQL 面板  │      └───────┬──────────────┬───────────────┘  │
│  │ 试跑对话(SSE)      │              │              │                  │
│  │ 导出预览           │              ▼              ▼                  │
│  └────────────────────┘      ┌──────────────┐  ┌──────────────┐        │
│                              │ store(文件系统)│  │ verify       │        │
│                              │ ~/.adks       │  │ go build/vet │        │
│                              └──────┬───────┘  │ smoke run    │        │
│                                     │          └──────────────┘        │
│                        agent.yaml ──┘                                  │
│                                     │                                  │
│                          ┌──────────▼───────────┐                      │
│                          │  kit.Build()  ★唯一  │                      │
│                          │  manifest → Layer    │                      │
│                          └────┬────────────┬────┘                      │
│                     进程内调用 │            │ 源码原样拷贝               │
│                               ▼            ▼                           │
│                    ┌──────────────┐  ┌──────────────────────┐          │
│                    │ 试跑(SSE)    │  │ 导出：go 工程 + kit  │          │
│                    │ ADK runner   │  │ + agent.yaml(embed)  │          │
│                    └──────────────┘  └──────────────────────┘          │
└────────────────────────────────────────────────────────────────────────┘
                                                    │
                                                    ▼
                                    ┌───────────────────────────────┐
                                    │ 生成物（独立二进制，无 builder）│
                                    │ main.go → kit.Build → launcher │
                                    │ full/api/console 三模式        │
                                    │ 可 docker/Cloud Run 部署       │
                                    └───────────────────────────────┘
```

**核心约束重申**：`kit/` 是 builder 与生成物的**同一份源码**（`//go:embed kit/*.go` 原样拷出，不做模板化）。这条决定了"配置即运行"与"导出源码"不会行为漂移——不是靠测试对齐，是靠物理上只有一份实现。

---

## 15. 遗留问题与未知区

| # | 问题 | 若不解决，编码时采用的占位方案 |
|---|---|---|
| 1 | `plugin/loggingplugin`、`plugin/retryandreflect` 的构造函数签名未核实 | 集中封装在 `kit/plugins.go` 一个文件里，按实际签名调整；`kind` 不认识时跳过并给 warning，不阻塞构建 |
| 2 | `runner.New` / runner 事件流的精确签名未核实（试跑主路径） | 先走 ADK 官方 REST（进程内起 `server/adkrest`）作为 Plan B；两条路径都封装在 `kit/layer.go`，UI 不感知 |
| 3 | `prod.NewLauncher()` 是否与 `full.NewLauncher()` 同签名 | 同 #1 处理，`main.go.tmpl` 里用接口变量接收 `launcher.Launcher` |
| 4 | 自造 manifest schema 与 Python ADK 的 `root_agent.yaml` **不兼容** | 明确不做兼容；导出时附一份 `MIGRATION.md` 说明字段映射，避免用户误以为可互换 |
| 5 | `session/database.AutoMigrate` 在多进程同时启动时的竞态（官方未说明） | 文档标注"单实例部署"；多实例部署需先手工跑一次迁移（README 写清） |
| 6 | kit 源码与 ADK v2.x 次版本变更的兼容（`llmagent.Config` 字段可能增删） | `go.mod` 锁 `v2.7.x`；升级 ADK 时把"kit 编译通过"作为唯一门槛，CI 里跑 |
| 7 | `gemini.NewModel` 对 `gemini-flash-latest` 这类别名的支持一致性（文档示例用它，但别名可能漂移） | UI 提供常用模型下拉 + 自由输入，探测按钮用 1-token 调用实测，不假设 |
| 8 | 纯 Go SQLite 驱动（`glebarez/sqlite`）与 ADK `databaseService` 行为差异（尤其 GORM 的 datatype 映射） | 实现首日跑 ADK 自带的 `session/sessiontestsuite` 对 sqlite 后端做一次全量通过性验证；不通过则退回 `gorm.io/driver/sqlite`（接受 CGO） |
| 9 | MCP 工具的 HITL 确认能力缺失：`tool.WithConfirmation` 与 `mcptoolset.Config.RequireConfirmationProvider` 在 v2.7.1 标 EXPERIMENTAL | v1 把 MCP 工具限制在"执行后告知"档，UI 明示该限制；若 v2.8 稳定则升级为完整四档 |
| 10 | `tool/toolsearch` 与 `toolAllow` 叠加时的实际披露行为（搜索能否点到被过滤掉的工具）未实测 | v1 两者互斥：开了 `toolsearch` 就忽略 `toolAllow` 过滤，UI 给出二选一开关，不允许同时配置 |
| 11 | `authn.NewCustom` 的 `AuthenticatorFunc` 精确签名未核实（只在 Bearer 场景需要） | 先只提供 `NewNoop`；`server.auth=bearer` 在 UI 里标"实验"，实现时按实际签名补；不阻塞首版 |

**诚实边界**：
- 本设计的"前缀稳定"只覆盖 builder 能控制的部分（instruction/工具/schema 序列化）；用户改 instruction 一定重写，这部分成本不可由脚手架消除。
- 工具输出包裹标记只能降低提示注入概率，**不能承诺防住**。真正高危的写操作必须走 `policy.confirmation=all`。
- 冒烟 run 证明"能跑通"，不证明"回答得好"；后者需要用户自己的评测集，不在脚手架职责内。

---

## 附录 A：实施顺序（按依赖，非按页面）

| 步 | 内容 | 完成判据 |
|---|---|---|
| 0 | `git clone --depth 1 https://github.com/google/adk-docs docs/vendor/adk-docs`，并把 `llms-full.txt` 落到 `docs/vendor/` | 离线可查 Go API |
| 1 | `manifest` 类型 + `validate` + 默认值 + 单测 | 30 条用例（含非法 slug、超长 instruction、重复工具名）全过 |
| 2 | `kit.Build` 最小闭环：gemini + 内置工具 + memory session | 进程内 `console` 模式能跑通一轮对话 |
| 3 | `kit` 补 MCP / HTTP 工具 / SQL session / 本地制品 / callbacks | `sessiontestsuite` 过 sqlite；MCP 探测能列出工具 |
| 4 | 文件系统 store + REST API + Web UI（项目 + 表单 + 探测） | 不写代码能配出一个能跑的 agent |
| 5 | `codegen`（embed kit 原样拷出）+ `verify`（build/vet/冒烟） | 导出目录 `go build` 零错误，冒烟 run 出 final event |
| 6 | 试跑 SSE + 版本快照/回滚 + 导出预览 | 首字节 ≤1.5s；回滚后 manifestHash 正确变化 |
| 7 | 插件面板（内置 logging/retry + 审计 webhook） | 审计 JSONL 出现且不含明文参数 |

---

## 16. 独立评审记录（v1，1 轮）

**评审方式**：独立上下文子智能体，只读设计文档 + 官方文档/包索引，不看本设计的推理过程；判定三态（通过 / 存疑 / 证据不足）。本地知识库为空，故凡需案例对照处一律标"证据不足"，评审者未补造案例。

**结果**：API 真实性 0 处虚构（11 条断言逐条对照 pkg.go.dev v2.7.1 与 adk.dev 全部为真）；权衡 8 通过 / 10 存疑 / 1 证据不足。已回修 5 条必修 + 4 条顺手修正：

| # | 评审意见 | 处置 |
|---|---|---|
| 1 | 配置期成本账为空，且"builder 不产生 LLM 调用"与试跑/探测/冒烟三处冲突 | **已修**：§3.1 拆成"账 A 生成物 / 账 B builder"，列调用次数与量级；删除"零调用"断言 |
| 2 | 替代项 ④ 与 §5「无分档」自相矛盾；官方 `tool/toolsearch` 从未进 T 层 | **已修**：T 层补第五形态 `toolsearch`（开关）；改写形态 C 的理由为"未全部实测穷尽，不宣称已穷尽" |
| 3 | "执行前确认"档只有配置项、无协议面，不可实现 | **已修**：补 `confirm_required` 事件 + `callId` + `/play/confirm` 端点 + 拒绝分支 + 120s 超时；MCP 侧因 EXPERIMENTAL 降级并登记 §15 #9 |
| 4 | MCP 生命周期归属不清（`FilterToolset` 包装器不可 Close） | **已修**：改为 `MCPHandle{Raw, Exposed}`，`Closers` 只收 `Raw`；§4.2 / §8.1 / §8.2 同步 |
| 5 | 两处置信错误：`authn` 无 Bearer 构造器；ADK 门槛是 Go 1.26.10 而非 1.27 | **已修**：§4.5 改为 `authn.NewCustom`；§3 改为"1.26.10 门槛，本机高一个次版本" |
| 6 | 前缀稳定漏了 `jsonschema.PropertyOrder`（map 内容排序 ≠ 渲染字节序） | **已修**：§4.3 加第五条规则 + golden 字节快照测试 |
| 7 | 六层决策表全标"已确认"，把假设包装成结论 | **已修**：全部改为「默认假设（可推翻）」，并说明只有 §1.1 四项是用户拍板 |
| 8 | 缺"为什么不用 Python ADK Agent Config" | **已修**：§2 加"与 Python ADK 的分界"一行，写明 adks 仅有的两条差异点 |
| 9 | token 粗估用 3.5 字符/token，对中文低估 2 倍 | **已修**：§5 改按 ASCII / 非 ASCII 分别折算，并标注系数非官方值 |

**未采纳 / 留待 P2**：
- 可见降级（换模型 + UI 明示）—— 评审认为对可用性更优，判断成立；但会牺牲成本可预测与风格稳定，登记为「有意不做」，P2 以默认关闭的开关提供（§5、§12 E）。
- 与 Python Agent Config 的正面能力对照表 —— 首版不做（本身就要用户选 Go），只在 §2 写分界。
- 无来源数字（95% 场景、±40%）—— 已删或改为量级描述，不再出现精确数字。

**未通过项**：无。第 6 条之外的所有"存疑"均已在上表处置；`证据不足` 1 项（多 Agent 三条硬墙无实测数据）如实保留在 §3.1，不假装已证。

**第 2 轮复审（回修一致性，仅查 5 处回修是否自相矛盾）**：5 处全部落地；查出 4 处连带矛盾，均已修——
① §2 仍写"四形态"→ 改为"五形态并列"；② §6 仍称替代项 ②④ "尚未穷尽"→ 改为"② 已采纳 / ④ 明确不做 / ① 列 P2"；③ §4.2 授权映射仍在引用 v1 不采用的 `tool.WithConfirmation` → 删除，MCP 明确只到"执行后告知"；④ §11 仍称"三处支点"→ 改为对应 §4.3 五条规则。
另补 3 处实现期会编译失败/分支分叉的洞：`ToolsSpec` 缺 `toolSearch` 字段（已补）、`MCPHandle.Raw` 是 `tool.Toolset` 而 `Closers` 要 `io.Closer`（已写明类型断言与失败处理）、确认超时的 SSE 形态未定（已定为 `tool_result{ok:false, code:CONFIRM_TIMEOUT}`）。
**结论**：v1 定稿。剩余 3 项（toolsearch 实测行为、`authn.NewCustom` 签名、GORM 驱动差异）留在 §15 未知区，带占位方案进入实现期。
