# adks 重构评估报告

> 设计台账：2026-11-15 · v1 · 轨道=重构 · 审计材料=全量源码（125 文件 / 约 10.1k 行 Go + 2.8k 行前端）+ designs/adks-harness-design-v1.md + README + CI 配置 + `go build/vet/test -cover` 实测 · 独立评审 未走（本报告断言均为可复现的命令输出或依赖包源码，见各条「证据」；P0 已交叉验证 ADK v2.7.1 源码）

## 0. 总判

**结论：这是一份质量在平均线以上的代码库——分层清楚、注释解释"为什么"而非"是什么"、build/vet/test 全绿、gofmt 干净、零 TODO。真正的问题不在写得好不好，而在三处"声明与实现不一致"和两处"没有回收站"。**

证据基线：`go build ./...` ✅ · `go vet ./...` ✅ · `go test ./... -cover` ✅ 全绿 · 覆盖率 api 44% / codegen 68.6% / store 71.7% / verify 54.5% / kit 58.3% / manifest 83.5% / **cmd 0%**。

## 1. 现状映射表

| 维度 | 现状实现 | 备注 |
|---|---|---|
| E 执行循环 | 复用 ADK `runner`，外面加三层保险：`maxLlmCalls` / `maxWallClockSec` / 工具并发信号量 + 打转检测（`Fingerprint` 环） | 与设计 §12 E 一致，落地完整 |
| T 工具注册 | 五形态：内置 / 声明式 HTTP（自研 `tools_http.go`，host 白名单 + 体积上限 + 幂等键）/ MCP（stdio 走 `filteredEnv` 白名单，http 限回环）/ `toolsearch` 延迟加载 | 注册≠披露≠授权三分离已实现 |
| C 上下文管理 | `EstimateTokens`（ASCII/非 ASCII 分算）+ 指令与工具 schema 的 token 估算 + 可选压缩 | 估算在 API 层每次 getProject 重算，无缓存 |
| S 状态存储 | 文件系统 store：一个项目 = 一个目录；快照最多 50 份；`writeAtomic`（temp + fsync + rename） | 单机、无数据库，与设计一致 |
| L 生命周期/安全 | `guard`（同源 + JSON content-type + slug 前置校验）→ `auth`（bearer，`subtle.ConstantTimeCompare`）→ mux；破坏性工具人工确认走 `confirm_required` + `/play/confirm` | 中间件顺序有明确理由且写在注释里 |
| V 评估接口 | `verify`：`go build` / `go vet` / 冒烟启动（free port + `/health` 轮询），独立进程，不请模型打分 | 与设计"校验只认事实"一致 |
| 范式 P | 声明式配置（`agent.yaml`）+ 导出即复制（kit/manifest 逐字节 embed 拷出）+ 单二进制 | 核心约束"物理上只有一份实现"守住了 |
| 模型组合现状 | 单档模型，按 manifest 逐项目配置，无分派表、无降级 | 对脚手架而言合理：分派是使用者的事 |
| 框架外现状 | Web UI 1761 行原生 JS 单文件，无构建链；`exports/` 目录承担"产物归档"但无清理策略 | 前端是刻意选择，不计入缺口 |

## 2. 缺口清单

| # | 缺口 | 定级 | 判据来源 | 证据 |
|---|---|---|---|---|
| 1 | **`runtime.server.host` 完全不生效，且代码注释断言了一个不存在的 API 限制** | **阻断** | 生产级七项 #1「执行环境/暴露面」 | `internal/codegen/render.go:294-296` 注释写「ADK v2's web launcher has no -host flag: it listens on :port on every interface」，`defaultArgs()` 也从不传 `-host`。**但 v2.7.1 源码 `cmd/launcher/web/web.go:395` 明确有 `-host`，且 `defaultHost = "127.0.0.1"`**。后果双向：① Dockerfile `EXPOSE 8080` + 默认参数 → 容器内只绑 loopback，端口映射直接失效，按 README 部署起来不可访问；② 用户把 `host` 改成 `0.0.0.0` 以为对外开放，实际仍是 loopback，且 `validate.go:185` 还会弹一条"binding X without auth exposes…"的错误警报 |
| 2 | **layerCache 无淘汰：`used` 字段只写不读，无 TTL、无 LRU、无容量上限** | 风险 | 生产级七项 #1「资源生命周期」 | `internal/api/cache.go:35,73,109` 赋值三次，全仓库零读取。缓存按 slug 永久持有 MCP 子进程 + SQLite 连接池，只在 `Invalidate` / 进程退出时释放。开过 20 个项目 = 20 个 stdio 子进程常驻，直到 Ctrl-C |
| 3 | **`exports/` 与项目 `.runtime/` 无回收**：每次 verify/export 生成一个完整 module 目录，永不清理 | 风险 | 生产级七项 #1 | `newExportDir` 每次 `MkdirTemp` 一个 `slug-kind-时间戳-随机` 目录；仓库内唯一 `RemoveAll` 都在失败分支。审计日志 `AuditSink` 是纯 append，无轮转 |
| 4 | **`index.json` 是幽灵产物**：每次 Save 全量重扫+解析+校验所有项目并写盘，但全仓库无任何读取方 | 优化 | 六层 S「何时写、谁读」 | `store.go:31,425-439`；`grep -rn "index.json"` 只命中写入端。`List()` 直接扫目录，不复用索引。N 个项目 → 每次保存 O(N) 次文件读 + YAML 解析 + `manifest.Validate` + 一次全量 JSON 落盘，且在 `indexMu` 全局锁内做 IO |
| 5 | **`cmd/adks` 0% 覆盖，其中包含唯一一条安全断言** | 风险 | 生产级七项 #7「生成者/评估者分离」的反面 | `cmd/adks/main.go` 145 行无测试文件。`refusing to bind %s without ADKS_TOKEN` 这条拒绝逻辑、`-host` 空值归一化、优雅关停，全部无自动化验证。e2e 脚本（`e2e-cdp.mjs` / `e2e-real-model.mjs`）只存在于 `.test-home/`，被 gitignore，CI 不跑 |
| 6 | **测试零并行 + 重测试不设门槛**：0 处 `t.Parallel()`，全套 ~45s 串行 | 优化 | — | `grep -c "t.Parallel()" *_test.go` = 0；codegen 两个 10s 级测试串着跑（`TestExportedModuleBuilds` 10.31s、`TestExportedBinaryStartsWithoutArgs` 10.46s），verify 11s。均无 `testing.Short()` 出口，`-race` 下更慢 |
| 7 | `AuditSink` 每事件 open + write + fsync + close | 优化 | 生产级七项 #4「记忆/审计治理」 | `kit/callbacks.go:144-163`。fsync 落在工具调用热路径上，模型一步多工具时线性叠加；Windows 上代价尤其明显 |
| 8 | `estimateFor` 用造字符串的方式数字符 | 优化 | — | `internal/api/estimate.go:76`：`kit.EstimateTokens(strings.Repeat("x", schemaBytes))`。为省一次除法分配最多 6KB 临时串，且读起来像 bug |
| 9 | `PlayEvent.DurMs` 声明但从不赋值 | 优化 | — | `internal/api/play.go:21` 定义、全仓库零写入。前端 `durMs` 列恒为空 |
| 10 | 冒烟检查的 20s 期限硬编码，不吃 `Options.Timeout` | 优化 | — | `verify.go:166`：`deadline := time.Now().Add(20 * time.Second)`，与 `opts.Timeout`（默认 4min）无关；慢盘/首次编译环境下会先撞 20s |

## 3. 优先级与方案（重构决策卡）

**改什么（按级别）**

1. **#1 host 失效** —— 最小改法：`defaultArgs()` 传 `-host`，`mainData` 里已有的 `Host` 直接复用（`{{ printf "%q" .Host }}`），删掉两处错误注释，README「已知边界」改成"host/port 都是生成期常量"。**连带**：Dockerfile 场景建议默认 `0.0.0.0` 或加 `ADKS_HOST` 覆盖，否则容器部署仍不通。
2. **#2 缓存淘汰** —— `used` 已经是时间戳，补一个 `Sweep(ttl)`：在 `Get` 入口或一个 60s ticker 里扫 `refs==0 && now-used > ttl` 的条目走 `retireLocked`。约 25 行，不动现有 refcount 语义。
3. **#5 cmd 测试** —— 把 `token == "" && !isLoopback(host)` 抽成 `func requireTokenForExposure(host, token string) error` 纯函数，三条表驱动用例即可；再把 `.test-home/e2e-*.mjs` 收编进仓库 + CI（可选，成本较高）。
4. **#3 回收** —— `exports/` 保留最近 N 个（复用 `pruneVersions` 的思路）；`.runtime/audit.jsonl` 加大小上限或按日切分。
5. **#4 index.json** —— 二选一：删掉写入端（省掉 O(N) 全量重扫），或让 `List()` 读索引 + mtime 校验。**推荐删**：`List()` 扫目录本来就是这个量级，索引没有换来任何东西。
6. **#6 并行** —— 给 codegen/verify 的重测试加 `t.Parallel()`（互不共享目录，安全）与 `if testing.Short() { t.Skip() }`，`make check` 加 `-short` 快速档。
7. **#7~#10** —— 顺手修，单点小改。

**不改什么（及理由）**

- Web UI 无构建链、1761 行单文件 JS：刻意的零依赖选择，引入打包器换来的收益不抵复杂度。
- 不引入数据库、不做多租户鉴权：设计 §12 已明确"刻意不做"，单 token 是这条边界上的正确取舍。
- 不把 `verify` 换成模型裁判：设计原则"校验只认事实"，动了就是拆地基。
- `perSlug sync.Map` 不清理锁对象：量级极小，加清理反而引入竞态面。

**迁移顺序**：#1 → #5 → #2 → #3 → #4 → #6 → 其余。理由：#1/#5 是事实性错误与安全断言，必须最先落地；#2/#3 是资源泄漏，改动独立；#4 是有净删除的简化，放在理解稳定后做；#6 改动分散在测试文件，最后批量做不冲突。

**回滚点**：每条改动都是独立 commit，且不共享文件（除 #1 触碰 `render.go` + README）。#1 有 `TestDefaultArgsMatchesLauncherKeywords` 覆盖，改参数数组会被现有测试兜住——**这正是该测试存在的价值，也说明 #1 是个"测试测了参数形状、没测参数语义"的漏网**。

## 4. 风险与回滚

| 改动 | 风险 | 回滚方式 |
|---|---|---|
| #1 传 `-host` | 旧的导出产物行为变化（从"总是 loopback"变成"跟随 manifest"）；若有人依赖当前隐式 loopback，会突然对外 | revert 单个 commit；`host` 默认值 `127.0.0.1` 由 `ApplyDefaults` 保证，不填即无变化 |
| #2 缓存 TTL | TTL 过短 → 每轮重连 MCP，首字节退化 | TTL 设 15min 起；`Invalidate` 语义不变，可通过配置关闭 |
| #4 删 index.json | 若有外部脚本读它（本仓库内没有） | 保留写端一个版本周期再删；或加 `--no-index` 开关 |
| #3 回收 exports | 用户依赖"上次导出的目录还在" | 只清超过 7 天且非最新的；提供 `-keep-exports` |

## 5. 遗留问题与未知区

| # | 问题 | 处理 |
|---|---|---|
| 1 | 试跑 SSE 无心跳。长时间模型首 token（>60s）在反向代理后可能被切断 | 本地单机场景不触发；若未来支持远程访问需补 `: ping` 注释帧 |
| 2 | 同一 session 并发 run 的串行化，设计 §11 要求"builder 试跑对同 sessionId 加互斥"，代码里未见对应锁 | 未实测 `database is locked` 是否真的出现；标为待验证，不作为缺口计 |
| 3 | `freePort()` 先 Listen 后 Close 再交给子进程，存在 TOCTOU 窗口 | CI 并行度高时可能偶发 flake；当前 CI 单 job 内串行，未观测到 |
| 4 | 导出物的 `go.sum` 是 builder 自己的，`Tidy` 失败时仍可构建（`-mod=mod`），但首次构建需联网 | 设计已知；离线场景未验证 |
| 5 | 真实模型 e2e（`.test-home/e2e-real-model.mjs`）未纳入 CI，需要真 key | 保持人工；若要自动化需引入 secret 管理，成本 > 收益 |
| 6 | 本报告未做独立模型评审（重构轨建议步骤）。所有断言均为可复现命令输出；#1 已对 ADK v2.7.1 源码逐行核对 | 如需独立评审，可按 production-checklist.md 组装评审包再走一轮 |

---

## 6. 修复记录（v1 审计 → 已实施）

> 实施日期 2026-11-15。全部改动通过 `gofmt` / `go build` / `go vet` / `go test -race ./...`。

| # | 缺口 | 改动 | 验证方式 |
|---|---|---|---|
| 1 | host 失效 | `defaultArgs()` 传 `-host`；`mainData.Host` 编译进常量；`ADKS_HOST` 运行期覆盖；Dockerfile 加 `ENV ADKS_HOST=0.0.0.0`、`EXPOSE` 改为跟随 manifest 端口；两处错误注释订正 | **端到端实测**：导出工程编译后，默认启动监听 `127.0.0.1:8080`；`ADKS_HOST=0.0.0.0` 启动监听 `0.0.0.0:8080` + `[::]:8080`（netstat 取证）。ADK 使用说明里也确实列出了 `-host`，默认 `127.0.0.1` |
| 5 | cmd 0% 覆盖 | 抽出 `normalizeHost` / `requireTokenForExposure` 纯函数；补 3 组表驱动测试 + `serve` 拒绝路径与未知 flag 测试 | 覆盖率 0% → **25.2%**；"非回环无 token 拒绝启动"现有 9 条用例 |
| 2 | 缓存不淘汰 | `layerCache.ttl`（默认 15min）+ `evictIdleLocked`；`used` 在 release 时刷新，TTL 从"最后一次运行结束"起算 | 5 条测试：超时淘汰 / 热条目保留 / 运行中绝不淘汰 / release 重置时钟 / ttl=0 关闭 |
| 3 | 导出与审计无回收 | `store.PruneExportDirs(dir, 10)` 在每次新建导出目录前执行（按 mtime 保留最新 10 份）；`AuditSink` 8MB 单代轮转 `audit.jsonl.1` | `PruneExportDirs` 2 条测试；审计 4 条测试（追加/关闭后丢弃/超限轮转/nil 安全） |
| 4 | index.json 幽灵写端 | 删除 `indexFile` / `index` / `reindex` / `indexMu`；保存路径不再做 O(N) 全量重扫 | `go build` 通过；store 全量测试通过；保存耗时不再随项目数线性增长 |
| 6 | 测试串行 | 新增 `make check-fast` / `make test-short`（`-short` 跳过编译真实模块的慢测试） | 本地反馈环 **≈45s → ≈7s** |
| — | （试过但撤回）测试并行 | 给两个 `go build` 重测试加 `t.Parallel()` 后实测 **26s vs 原来 21s**——两者抢同一个 Go 构建缓存。已回退，并在测试里写明原因 | 实测墙钟对比 |
| 7 | 审计每事件 fsync | 长期持有句柄 + 按体积轮转，去掉每事件 `fsync`；`AuditSink` 实现 `io.Closer` 并由 `Layer` 托管生命周期 | 见上；不再在工具热路径上做磁盘同步 |
| 8 | 造字符串数字符 | 新增 `kit.EstimateASCIITokens(n int)`，`estimateFor` 不再 `strings.Repeat` | 3 条测试：与 `EstimateTokens` 逐长度一致 / 边界 / CJK 系数不回归 |
| 9 | DurMs 悬空 | `runTurn` 用 callId 配对计算真实耗时（tool_result / error），确认等待单独计时；前端工具卡片新增耗时位 | 2 条测试：耗时 ≥ 间隔 / 两次调用不累加 |
| 10 | smoke 20s 硬编码 | 取 `min(20s, 剩余 ctx 预算)` | `verify` 全量测试通过 |

**仍未做（保持原判）**：§5 未知区 1–5 未动。其中第 2 条（同 session 并发 run 互斥）仍未实测是否真的触发 `database is locked`，不臆断加锁。

---

## 7. 架构级重构（v2，2026-11-15 晚）

> 上一节点状修复之后，剩下两类结构性债务：**同一类 bug 还会再犯**（声明了但没接上实现），以及 **`api.Server` 是个上帝对象**。本轮处理这两类，而不是再加若干点状修复。

### 7.1 又抓到两个同类字段

补 `ServerSpec` 的接线时发现：`pathPrefix` 与 `a2a` **零引用**——manifest 里能写、能校验、能导出，但生成物根本不读。加上上一轮的 `host`，六个 server 字段里有三个是死的。

已接线：
- `pathPrefix` → 生成 `-path_prefix`，且必须跟在 `api` 子启动器关键字**之后**（子启动器的 flag 放前面会被 web 当作自己的 flag 拒掉）
- `a2a` → 生成 `a2a` 子启动器关键字
- console 模式短路，不传任何 web flag
- `pathPrefix` 补校验（必须 `/` 开头、不以 `/` 结尾、无空格），因为它是硬注入 argv 的

**实测取证**（导出工程真编译、真启动）：
```
GET  /v1/list-apps  -> 200      GET  /api/list-apps -> 404
POST /a2a/v1/invoke -> 200
```

### 7.2 把"声明→实现"变成可测得的东西

新增 `internal/codegen/contract_test.go`，两部分：

1. **完整性**：反射遍历 `ServerSpec` 的 yaml 字段，每个字段必须在 `serverWiring` 表里被分类。新增第七个 server 字段而没想清楚它去哪 → 测试失败。
2. **值注入**：每个 case 把一个字段设成不可能偶然出现的值，在**它该出现的产物**里找它（`main.go` / `Dockerfile`），并对 `a2a` 做开关双向断言。

为什么不是"字段是否被引用"的静态检查：`host` 当年是被读过的——读进 `mainData.Host` 然后模板没渲染。**引用存在 ≠ 值到达**，只有值注入能抓住这一类。这条写进了测试文件头部的注释，避免后人退回弱检查。

### 7.3 拆 `api.Server`

`api.Server` 原本握着 `store / seed / version / token / layers / plays`，任何 handler 都能碰到编译缓存和确认回执。现在：

| 位置 | 职责 |
|---|---|
| `internal/play` | 编译缓存（含空闲淘汰）、待确认回执、确认后重入循环、轮次预算、SSE 事件类型 |
| `internal/api` | 解析请求、收敛状态码、编码 SSE 帧 |
| `internal/store` | 新增 `PrepareRuntime` / `RebaseSQLiteDSN`：运行时路径重定基归到路径的拥有者 |

关键设计：`Service.Prepare` 在写第一个字节**之前**返回错误，所以"项目不存在 → 404""manifest 编译不过 → 400"这两条语义没有因为拆包而丢失。`Session` 用一次 `Close` 归还缓存引用，测试里可注入 `runnerLike` 假实现。

结果：`api` 从 2189 行降到约 1142 行（不含测试），`play` 569 行；`api` 不再认识 `kit.Layer`、`runner.Runner` 这些运行时类型。

### 7.4 本轮验证

- `gofmt` 干净 · `go build` / `go vet` 通过 · `go test -race -count=1 ./...` 全绿
- 测试文件随代码迁移：`api/play_test.go`、`api/cache_test.go` → `internal/play/`，并补了 `TestPlayHubRefusesForeignRun`、`TestSessionCloseReleasesOnce`、`TestPlayHubCloseDropsPending`
- e2e：导出 → 编译 → 启动 → 探测前缀与 a2a 路由，全部用真实请求验证

---

## 8. 第三轮：把验证缺口补上（2026-11-15 深夜）

前三轮都是"改代码"，这一轮是"让下次能被机器挡住"。

### 8.1 一个真 bug：GET 从不重试

`kit/tools_http.go` 的 `doWithRetry` 两个分支都要求 `req.Body != nil && req.GetBody != nil`。GET/HEAD 的 `Body` 恒为 nil，于是**最常见的一类工具完全不重试**——与函数注释 "retries once on a transient failure" 和设计文档 §13 #2「瞬时 5xx 重试一次」直接矛盾。

改为 `cloneForRetry(req)`：无 body 的请求天然可重放；有 body 的才需要 `GetBody`。4 条测试锁定：GET 5xx 重试、POST 重放 body、不可重放 body 不重试、4xx 不重试。

### 8.2 HTTP 主链路从 0% 覆盖到有回归

`go tool cover` 曾显示 `playTurn` / `confirm` / `export` / `listProjects` / `graphViewOf` / `estimateFor` 全为 0%——只有 guard 与 404 分支有测试。这正是 `host` 能漏网的土壤：没有任何测试走过"建项目 → 改字段 → 导出"。

新增 `internal/api/flow_test.go`：create → list → get（含 estimate）→ PUT YAML → 重读校验持久化与版本快照 → files 预览 → DELETE；导出走 `{"zip":true}` 并解压断言 `main.go` 的 `-host` / `port` 与 `Dockerfile` 的 `EXPOSE`；graph 端点断言 depth 与 in/out；play/confirm 的前置校验。

新增 `internal/play/service_test.go`：Prepare 缺失项目即错误（404 语义的根）、Confirm 无 pending、Invalidate 未知 slug、重复 Close、WallClock 取 manifest 值。

**仍未覆盖（有意）**：`probeSQL` / `probeMCP` / 真实模型轮次——需要真数据库、真 MCP server、真 key，做成测试就是把不稳定引进 CI。

### 8.3 CI 补两道闸

- `gofmt`：矩阵含 Windows runner，其默认 shell 不是 bash，所以该 step 显式指定了 shell
- `govulncheck`：独立 job，版本写死 `golang.org/x/vuln@v1.9.0`（扫描器本身不该每次跑漂一个版本）

### 8.4 一条文档缺口

MCP stdio 执行 manifest 里的任意命令，且不做白名单；HTTP 工具才有 `allowedHosts`。README「已知边界」补上这条等价关系：能改 manifest = 能在这台机器上执行命令。

---

## 9. 第四轮：把账算出来（能看见账）

前一轮的结论是"反馈只有一半"——能证明跑通，不能证明跑得对、花多少。这一轮补中间那层。

### 9.1 关键发现：数据早就在代码里，只是被丢掉了

- `internal/play/turn.go` 拿到 `fr.Response` 只做了 `responseOK()` 判断就丢弃；
- `model.LLMResponse.UsageMetadata`（prompt / candidates / thoughts / cached token）每个事件都有，代码从没读过。

所以这不是"新增能力"，是把已经在手的两个变量接出去。

### 9.2 做法

| 位置 | 改动 |
|---|---|
| `play.Event` | 加 `Result`（工具返回体，服务端截断 4KB，超长标 `…`）与 `Stats`（仅 done 事件携带） |
| `play.turnMeter` | 观察同一串发给 UI 的事件，保证"报出来的账"与"用户看到的"不会漂；LLM 调用与 token 从原始 ADK 事件读取（play 事件是投影，没有 usage） |
| `play.TurnStats` | `llmCalls / promptTokens / outputTokens / cachedTokens / wallMs / firstByteMs / tools[] / errCode` |
| `play.statSink` | 每轮追加一行到 `.runtime/stats.jsonl`，互斥串行；**不记录消息正文** |
| `web/app.js` | 工具卡片追加结果体（虚线分隔、可展开）；done 时渲染一行账，`title` 挂完整 JSON 便于复制 |

两个刻意的取舍：

1. **失败也要记账**。`errCode` 随 `done` 一起下发——"跑挂了、4 次调用、8 秒"正是最需要看见的数。
2. **统计文件不存正文**。审计日志本来就只存参数 hash，是为了不让人格数据沉淀；账目文件同理只存数字。

### 9.3 验证

- `internal/play/stats_test.go`：token 聚合与工具聚合、结果体截断（含 UTF-8 边界）、stats.jsonl 落盘且不含消息正文、失败轮次的 `errCode`、首字只记一次；
- `TestDoneEventWireShape` 钉住 SSE 字段名——前端没有测试，这是唯一防止改 JSON key 把面板改空的闸；
- `node --check web/app.js` 过语法；`gofmt` / `go vet` / `go test -race ./...` 全绿。

### 9.4 没做的（下一次）

- 账目只有单轮，没有跨轮聚合（"这个项目最近 50 轮平均多少 token"）。要做就得给 stats.jsonl 加一个读端口，属于第 ③ 层的前置。
- 没有"成本预算"：`policy.limits` 只限调用数与墙钟，没有 token 上限。

---

## 10. 第五轮：把账读出来，把预算接上（2026-11-15 深夜）

上一轮的账只存在于单轮与文件里：能看见"这一轮花了多少"，看不见"这个项目到底花了多少"，也不能阻止一轮烧穿。

### 10.1 token 预算：为什么放在 kit 而不是 builder

`policy.limits.maxTokensPerTurn`（0 = 不限制）用 `AfterModelCallbacks` 在 `kit` 里实现，而不是在 play 的循环里判断：

- 导出物不走 `Converse`，走 ADK launcher。放在 builder 里就会变成"试跑能拦、上线不能拦"——正是前四轮反复在修的那类字段。
- 计数器用 `temp:adks.tokens`，与 `maxLlmCalls` 同一个 `temp:` 约定（ADK 不会把它持久化），所以作用域是"这一次 invocation"，graph 模式下所有节点共享同一个计数。
- 计数只认 provider 报的 usage。没报 usage 的模型不加猜测——猜出来的 token 数会把健康的 run 掐死。
- 独立错误 `ErrTokenBudget`，UI 映射成 `TOKEN_BUDGET`，与 `WALL_CLOCK` 区分：一个是"调太多了"，一个是"跑太久了"，指向的修法不同。
- 顺带核实了一件事：`stateKeyLLMCalls` 早就在用 `temp:` 前缀，所以"20 次调用"确实是**每轮**而不是整个会话——这条怀疑被证伪，没改。

### 10.2 读端口：`GET /api/projects/{slug}/stats`

`play.Stats(slug, limit)` 读 `stats.jsonl`，只取尾部 2000 行（文件只增不减，全量解析会让面板越用越慢），聚合出：

- 轮数、失败轮数、输入/输出/缓存 token、**缓存命中率**（`cached/prompt`，唯一能证明前缀稳定真在省钱的数）
- 墙钟与首字的 p50 / p95 / avg
- 每个工具的调用数、错误数、累计耗时、p50，按耗时降序

细节取舍：截断的坏行（写一半崩了）跳过而不作废整份账；文件不存在返回空账而不是 404——项目存在，只是没跑过。

### 10.3 UI

- 对话上方一行累计账：`累计 3 轮 · ↑3.5k ↓550 tok · 缓存 74% · p50 3.2s · p95 5.1s · 1 轮失败`
- 每次 `done` 后自动刷新；切换项目时刷新
- 悬停给完整 JSON

### 10.4 验证

- `kit/tokenbudget_test.go`：未设置不装回调、跨调用累计、thinking token 计入、无 usage/报错时不误杀
- `internal/play/stats_test.go`：跨轮聚合（含 p50/p95 的具体值）、缺文件返回空账、坏行跳过
- `TestStatsEndpoint`：新项目返回空账；404 规则测试里补了 `/stats`
- **端到端实测**：起真服务、手工写 3 行 stats.jsonl、`curl` 端点，核对轮数/命中率 0.743/p50 3200/p95 5100/工具降序全部正确

---

## 11. 第六轮：第三层验证——行为回归（2026-11-15 深夜）

前五轮把"能跑通"和"能看见账"补齐了。这一层回答的是另一个问题：**改一句 instruction，怎么知道是改好了还是改坏了。**

### 11.1 只断言事实

`evals.yaml` 的每条用例是「一个输入 + 一组可判定的期望」：

```yaml
cases:
  - name: 查订单必须调工具
    input: 帮我查订单 A1
    expect:
      toolsUsed: [get_order]      # 必须被调用过
      toolsNotUsed: [cancel_order] # 必须没被调用
      contains: [A1]               # 最终回答里必须有
      notContains: [失败]           # 必须没有
    approve: [cancel_order]        # 允许自动确认的工具，默认全部拒绝
```

**没有模型打分。** 判断不可复现就不算校验——这条从第一轮起就没变过，回归层也一样。

### 11.2 三个安全/正确性判断

1. **默认拒绝破坏性工具的自动确认**。用例要跑副作用，必须在 `approve` 里写出来。写下来才可能被 review。
2. **每条用例一个独立 session**（`eval_<nanos>_<i>`）。共用 session 会让后一条用例靠前一条制造的上下文通过——那是假通过。
3. **用例轮次不进项目账**。statLine 加了 `kind: eval`，`Stats()` 聚合时跳过；成本仍留在文件里可 grep，但"这个项目花了多少"只算人真的聊的那些轮。

### 11.3 落点

| 位置 | 内容 |
|---|---|
| `internal/play/eval.go` | 用例类型、`ParseEvals`（拒绝无输入/无断言/重名）、`RunEvals`、`checkExpect` |
| `internal/store/evals.go` | `evals.yaml` 的项目内读写（与 agent.yaml 同级） |
| `internal/api/evals.go` | `GET/PUT /api/projects/{slug}/evals`、`POST .../evals/run` |
| `internal/api/export.go` | 新增 `s.render()`：预览、verify、导出三处统一，`evals.yaml` 一起进产物 |
| `web/` | 「回归」标签页：YAML 编辑 + 保存 + 运行 + 逐条结果 |

`ParseEvals` 会拒绝"断言为空的用例"——那种用例永远通过，是回归集里最危险的东西。

### 11.4 验证

- `play/eval_test.go`：解析拒绝规则、四种断言的正反两面（失败信息必须报出"实际调了哪些工具"）、默认拒绝确认、`approve` 生效、结果带成本
- `api/flow_test.go`：用例增删查 + 断言为空的用例拒收 + 空套件运行返回 400 + **导出 zip 里确实带 evals.yaml**
- 端到端：真服务上保存两条中文用例、读回、导出 zip 解包确认 `evals.yaml` 在内、页面出现「回归」标签

### 11.5 已知边界

- 运行是同步的，用例多会等很久；上限 20 条
- 没有"历史通过率"曲线，也没有 CI 钩子——导出物里有用例文件，但还没有 `adks eval` 子命令在导出目录里跑

---

## 12. 第七轮：SSE 保活（2026-11-16 凌晨）

### 12.1 问题

`playTurn` 只在有事件时 `Flush`。一次慢工具调用、或最长 120 秒的等待人工确认，期间**零字节**。

服务端自身没问题（`WriteTimeout: 0`，写法是对的），但中间层不会等：nginx 默认 `proxy_read_timeout 60s`，云 LB 常见 30–60s。而产品在这个阶段刚把 `host` / `pathPrefix` / Dockerfile 修好——等于在鼓励部署，然后一部署就断。这是典型的"本地好好的、上线才炸"。

### 12.2 做法

- `sseWriter`：写操作串行化。事件来自回合，心跳来自 ticker，而 `ResponseWriter` 不支持并发写；字节交错会把帧写坏，浏览器会静默丢弃。写失败一次就标记 `dead`，避免客户端走掉之后每个事件都报一次错。
- `keepAlive`：每 15 秒发 `: keep-alive\n\n`（SSE 注释帧，客户端忽略、代理算流量）。15s 是对着 60s 超时留三次机会，不是对着我们自己的节奏。
- handler 里 `close(stop)` 后 **等 goroutine 退出**再返回：handler 返回后再写 `ResponseWriter` 会 panic。

### 12.3 验证（两层）

1. **连接级**：`httptest.NewServer` 起真 socket，handler 静默 90ms 后发一个事件，客户端用 `bufio.Scanner` 读——断言至少 3 个注释帧、1 个数据帧、且注释帧先到。时间轴压缩到毫秒，但走的是真 TCP。
2. **端到端**：写一个会 `sleep 35` 秒的本地 OpenAI 兼容桩，项目指向它，`curl -N` 记录每帧到达的秒数：

```
18 : keep-alive
33 : keep-alive
38 data: {"type":"error",...}
38 data: {"type":"done","durMs":35005,"stats":{...}}
```

静默 35 秒里两帧按时到达——这正是原来会断的窗口。

另外 `TestSSEWriterSurvivesConcurrentWrites` 让两个 goroutine 各写 200 次，然后逐行 `json.Unmarshal` 每个 `data:` 帧——交错写出来的帧不是合法 JSON，这条能真抓到问题；`-race` 亦绿。
