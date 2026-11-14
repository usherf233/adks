# adks

**一个单二进制的 Google ADK（Go）脚手架：在网页里配好一个 Agent，就能直接试跑，也能导出一份可编译、可部署的独立 Go 工程。**

- 不需要写 Go 代码，也不需要 Node 运行时。
- builder 里跑的和你导出的，是同一份 `kit` 源码、同一个 manifest，行为和字节都不会漂移。

## 快速开始

```bash
go build -o adks.exe ./cmd/adks      # 构建
./adks.exe serve                     # 默认 http://127.0.0.1:7788
```

| 参数 | 说明 |
|---|---|
| `-host` | 监听地址，默认 `127.0.0.1`；绑非回环地址必须设 `ADKS_TOKEN` |
| `-port` | 端口，默认 `7788` |
| `-home` | 项目存放目录，默认 `~/.adks`（也可用 `ADKS_HOME`） |

模型密钥不进 manifest，只写 `${ENV}` 引用，运行时由环境变量提供。

## 核心概念

| 名词 | 含义 |
|---|---|
| **builder** | 脚手架本体：带 Web UI 的单二进制，负责编辑、校验、试跑、导出 |
| **manifest** | `agent.yaml`，唯一真相源。schema 版本 `adks/v1`，kind `AgentProject` |
| **kit** | 把 manifest 编译成 ADK 对象的 Go 包，builder 与生成物共用同一份 |
| **生成物** | 导出的独立 Go module，自带 `kit`/`manifest` 源码与 `go.mod` |

两条原则撑起了整个设计：

1. **导出 = 复制，不重写**。`kit/` 与 `manifest/` 从 builder 自己的 embed FS 里逐字节拷出，不做模板渲染——两份实现永远无法保证同步，所以干脆不要第二份。
2. **校验只认事实**。`build` / `vet` / 二进制能否启动是 pass/fail 事实，不请模型给自己的输出打分。

## 目录结构

```
cmd/adks/          入口：serve / version
internal/api/      HTTP 层：CRUD、探测、流式试跑、导出、状态码收敛
internal/store/    文件系统存储（一个项目 = 一个目录），无数据库
internal/codegen/  渲染导出工程：整包复制 kit/manifest + 生成 main.go / Dockerfile / README
internal/verify/   build、vet、冒烟启动三项确定性检查
kit/               manifest → ADK 对象（embed 进 builder，也拷进生成物）
manifest/          schema 定义、校验规则、默认值、拓扑校验
web/               UI 静态资源（原生 JS，无构建步骤）
designs/           架构设计台账
```

依赖方向单向：`cmd → internal/* → kit → manifest`，无反向引用。`manifest` 除 `yaml.v3` 外零依赖，正是为了能被原样拷走。

## 能配什么

- **形态**：单 LLM Agent（`mode: llm`，默认）或节点/边工作流（`mode: graph`，节点类型 `agent` / `tool` / `route` / `join` / `subgraph`）
- **模型**：OpenAI 兼容端点，`apiKeyEnv` + 可选 `baseURL`（留空则回退 `OPENAI_BASE_URL`）
- **工具**：内置工具、HTTP 工具（域名白名单）、MCP server（仅本地命令/回环地址，不连任意外部端点）
- **存储**：SQLite / MySQL / Postgres 连接与建表
- **插件**：ADK 插件引用、回调
- **运行时**：监听参数、健康检查、打包 Dockerfile

## HTTP API

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/api/health` | 存活探测 |
| GET/POST | `/api/projects` | 列表 / 新建 |
| GET/PUT/DELETE | `/api/projects/{slug}` | 读取 / 保存 / 删除 |
| GET | `/api/projects/{slug}/versions` | 版本列表 |
| POST | `/api/projects/{slug}/rollback` | 回滚到某个版本 |
| GET | `/api/projects/{slug}/graph` | 只读拓扑图 |
| POST | `/api/projects/{slug}/probe/{model,sql,mcp}` | 连通性探测 |
| POST | `/api/projects/{slug}/play` | 流式试跑（SSE） |
| POST | `/api/projects/{slug}/play/confirm` | 工具调用确认 |
| GET | `/api/projects/{slug}/files` | 导出前文件预览 |
| POST | `/api/projects/{slug}/verify` | build / vet / 冒烟 |
| POST | `/api/projects/{slug}/export` | 导出 zip |

状态码统一：项目或快照不存在 → `404`；manifest 解析失败 → `getProject` 返回 200 + `loadError`（编辑器必须能打开修），其余端点 `400`。

## 开发

```bash
go build ./...
go vet ./...
go test -race -count=1 ./...
```

改了 `kit/`、`manifest/` 或 `web/` 之后**必须重新编译 `adks.exe`**——embed 在编译期快照，旧二进制会导出缺文件或旧代码的工程。

浏览器端到端脚本在 `.test-home/`（Playwright 免装版，用 Edge headless + CDP 驱动），属本地调试产物，不入库。

## 已知边界

- 不做多 Agent 拖拽编排画布；graph 可视化只读。
- 不做运行时多租户鉴权，只靠单个 bearer token（`ADKS_TOKEN`）。
- 不支持在 UI 里写自定义 Go 工具函数。
- 导出工程的 launcher 参数是生成期常量，`runtime.server` 的 host/port 改了需重新导出。

## 文档

- [`designs/adks-harness-design-v1.md`](designs/adks-harness-design-v1.md) — 架构设计台账：需求规格、关键决策、未知区、评审结论。
