# 参与开发

## 环境

- Go 1.27+（版本以 `go.mod` 为准）
- 一个能用的模型密钥（`GOOGLE_API_KEY` / `OPENAI_API_KEY` / `ANTHROPIC_API_KEY`，视你选的 provider）
- 不需要 Node、不需要数据库（默认 SQLite）

## 三条铁律

1. **改了 `kit/`、`manifest/` 或 `web/`，必须重新编译 `adks`。**
   `embed.FS` 在编译期快照，旧二进制会导出缺文件或旧代码的工程——这是本项目最容易踩的坑。

2. **不要把 `kit/` 或 `manifest/` 的逻辑复制到 `internal/`。**
   导出 = 逐字节复制，所以这两包必须自足（`manifest` 除 `yaml.v3` 外零依赖）。
   你在 `internal/` 里写的任何"同一件事的第二次实现"都会在导出后消失或漂移。

3. **校验只认事实。**
   能不能 build、能不能 vet、二进制能不能起来——这些是唯一可信的 pass/fail。
   不要引入"让模型检查自己的输出"这类判定。

## 提交前

```bash
make check          # 或 go build ./... && go vet ./... && go test -race -count=1 ./...
```

只动文档可以跳过。动到导出链路（`internal/codegen`、`internal/verify`）时，请手动跑一次
「新建项目 → verify → 导出 → `go mod tidy && go build`」，测试里覆盖不到真实工具链的全部边角。

## 代码风格

- 注释用英文，写**为什么**不写**是什么**；对"看起来可以更简单但故意没这么做"的地方一定要留注释。
- 错误信息小写、无标点、带上下文：`fmt.Errorf("load %s: %w", path, err)`。
- 依赖方向单向：`cmd → internal/* → kit → manifest`。反向引用一律拒绝。
- 前端保持零构建链：`web/` 里只有原生 JS / CSS，不引入打包器。

## 提交信息

一句话说清改了什么，必要时在正文写清权衡。

```
Converge project status codes on 404/400/200
```

## 分支与发布

- 主分支 `master`，CI 必须全绿。
- 打 `v*` tag 触发交叉编译，产出 linux/darwin/windows 的压缩包并附在 Release 上。
- `designs/` 下的设计台账随决策更新，不要只改代码不改台账。
