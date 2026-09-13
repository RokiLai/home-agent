# GitHub 工作流测试与构建提速方案

## 1. 文档状态

| 阶段 | 状态 | 说明 |
| --- | --- | --- |
| 设计 | 阶段 A 评审通过 | 已确认 runner 与计费成本 1.5 倍上限，并授权阶段 A；阶段 B/C 仍须等待真实基线证据后评审 |
| 实施 | 阶段 A 本地完成 | 已实现静态目标清单、共享计时执行器和无发布权限的冷缓存实验 Workflow；未实施测试并行或构建 matrix |
| 验收 | 阶段 A 本地门禁通过 | 本地真实 `-race`、16 目标构建与制品校验已通过；尚未完成 GitHub Actions 10 组交错基线及三类真实发布冒烟 |

### 1.1 设计审查重点

设计审查必须确认以下事项后，才能授权实施：

1. 性能目标以墙钟时间、GitHub Actions runner 分钟和可靠性三项共同约束，不以牺牲其中任一项换取表面提速；
2. 测试命令继续包含 `-count=1 -race ./...`，所有现有用例、断言、真实端口与协议路径保持执行；
3. 哪些测试具备包内并行条件，哪些测试因全局状态、环境变量、固定端口、进程或文件系统共享而必须串行；
4. Server 与 Agent 的构建 matrix 目标、并发预算和聚合完整性契约；
5. 性能基线采样窗口、对照方式、阈值和回退条件是否可由真实 GitHub Actions 运行直接证明。

## 2. 背景与问题定义

当前 Release Workflow 在合并 `dev` 到 `main` 后触发。`prepare` 完成版本判定后，`test`、`build-server` 和 `build-agent` 按组件变更条件并行；发布 Job 等待测试与对应构建 Job 都成功后才创建 Release。

用户提供的 [GitHub Actions Run 34734225374](https://github.com/RokiLai/home-agent/actions/runs/34734225374) 是提交 `a73b23186b82c51fa756e23900a2a2f230dfd7d5` 的一次真实 Server-only 发布。GitHub Actions API 和完整 Job 日志显示：

| Job | 耗时 | 结果 |
| --- | ---: | --- |
| `prepare` | 5 秒 | 成功 |
| `test` | 2 分 21 秒 | 成功 |
| `build-server` | 1 分 48 秒 | 成功 |
| `build-agent` | 0 秒 | 因 Agent 未变更而跳过 |
| `release-server` | 10 秒 | 成功 |
| `release-agent` | 0 秒 | 跳过 |

该样本的发布关键路径约为 `prepare 5 秒 → test 141 秒 → release-server 10 秒`，总计约 2 分 36 秒。因为 `test` 比并行的 `build-server` 慢约 33 秒，单独缩短 Server 构建不能缩短该次运行的关键路径，除非测试也同步优化。

该次运行的 Step 时间进一步表明：

| Job | Step | 起止时间（UTC） | 约耗时 |
| --- | --- | --- | ---: |
| `test` | checkout | 02:55:44–02:55:46 | 2 秒 |
| `test` | `setup-go` | 02:55:46–02:55:48 | 2 秒 |
| `test` | 全量 `-race` 回归 | 02:55:48–02:57:58 | 130 秒 |
| `build-server` | checkout | 02:55:43–02:55:44 | 1 秒 |
| `build-server` | `setup-go` | 02:55:44–02:55:45 | 1 秒 |
| `build-server` | 7 目标构建与 SHA256 验证 | 02:55:45–02:57:22 | 97 秒 |
| `build-server` | artifact 上传 | 02:57:22–02:57:25 | 3 秒 |

因此，本样本中测试和构建命令本身分别占对应 Job 的绝大部分，checkout、Go 工具准备和 artifact 上传不是首要瓶颈。但是该数据仍只是一份缓存未命中的真实样本，不代表中位数或高分位表现；当前 Workflow 也没有分离 Go 测试编译与用例执行，或记录每个构建目标的独立耗时。

### 2.1 仓库直接证据

- `.github/workflows/release.yml` 的 `test` Job 使用 `actions/setup-go@v5`，已设置 `cache: true` 和 `cache-dependency-path: go.sum`；
- 全量回归固定执行 `go test -count=1 -race ./...`；不同 Go package 默认可并发，但仓库 77 个 Go 测试文件中当前没有 `t.Parallel()`，同一 package 内的测试仍顺序执行；
- 真实 Run 34734225374 的 `-race` 输出中，`internal/auth`、`internal/api` 和 `cmd/homeagent-agent` 分别约为 101.8、98.7 和 67.8 秒，是 GitHub 端首要分析对象；`internal/daemon` 约为 12.0 秒，未处于该次运行的关键路径；
- 本地一次非 `-race` 全量测试中，`cmd/homeagent-agent`、`internal/daemon`、`internal/qualitygate`、`internal/api` 和 `internal/serverupgrade` 分别约为 16.0、15.1、12.0、8.3 和 6.2 秒；本地排序与 GitHub `-race` 排序明显不同，证明不能用本地非 race 数据代替 GitHub 基线；
- 测试代码包含固定 `time.Sleep`、超时等待以及测试内 `go build`，但固定等待不等于已确认瓶颈，必须以逐用例耗时和调用链证明；
- `build-server` 在一个 Job 内依次构建 7 个 `GOOS/GOARCH` 目标，`build-agent` 依次构建 9 个目标；每个二进制随后生成独立 SHA256 文件；
- Server 与 Agent 构建 Job 已彼此并行，不应把现有 Job 级并发重复描述为新收益。
- Run 34734225374 使用 Go 1.26.8、Ubuntu 24.04 runner；`test` 和 `build-server` 的 `setup-go` 均报告 `Cache is not found`，测试随后下载了 `golang.org/x/crypto`、`github.com/go-sql-driver/mysql` 和 `filippo.io/edwards25519`；
- 该次运行结束时，`build-server` 保存与其他并行 Job 相同键的缓存失败，日志明确显示另一 Job 可能正在创建该缓存；此竞争没有使构建失败，但证明同次运行的并行 Job 不能依赖彼此尚未完成的缓存保存。

### 2.2 外部平台事实与待验证假设

GitHub 官方文档说明：GitHub-hosted runner 的 Job 从干净环境启动；`setup-go` 可以依据 `go.sum` 恢复依赖与构建缓存；matrix 默认会按 runner 可用性尽量并发，并可用 `max-parallel` 限制并发；artifact 用于跨 Job 传递构建产物。GitHub 会计算并比较 artifact digest，但 digest 不匹配默认只产生警告，因此本方案不能把平台警告当作阻断性完整性门禁，仍须显式校验仓库定义的逐文件 SHA256 和清单摘要。

这些平台能力只证明方案可实现，不证明本仓库一定加速。实施前后必须通过真实 Workflow 日志验证以下假设：

| 假设 | 当前状态 | 验证方式 |
| --- | --- | --- |
| `setup-go` 缓存恢复显著减少本仓库编译时间 | 未验证；已确认参考运行为 miss | 记录 cache hit/miss、恢复耗时和测试编译阶段耗时 |
| 每目标 matrix 在受控并发下的收益大于 runner 启动与 artifact 传输开销 | 未验证 | 对等价提交交错运行串行基线与候选 matrix 方案 |
| 最慢测试 package 的主要成本可通过安全并行和同步机制优化 | 候选原因 | 输出逐 package、逐测试耗时并审计共享状态 |
| GitHub runner 并发配额足以按设计及时启动 matrix 目标 | 未验证 | 记录 queued、started 和 completed 时间 |

参考资料：

- [GitHub Actions：构建和测试 Go](https://docs.github.com/en/actions/tutorials/build-and-test-code/go)
- [GitHub Actions：Workflow matrix 与 max-parallel](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax)
- [GitHub Actions：依赖缓存](https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching)
- [GitHub Actions：跨 Job 存储和共享 artifact](https://docs.github.com/en/actions/tutorials/store-and-share-data)
- [setup-go：并行构建缓存建议](https://github.com/actions/setup-go/blob/main/docs/advanced-usage.md#parallel-builds)

## 3. 目标与非目标

### 3.1 目标

1. 在完全保留现有测试集合、断言、`-race`、`-count=1`、真实端口及协议验证的前提下缩短 `test` Job 墙钟时间；
2. 在保留全部平台、链接参数、文件命名、SHA256 校验和发布前完整性检查的前提下缩短跨平台构建墙钟时间；
3. 缩短 Server-only、Agent-only 和 Server+Agent 三类发布路径的端到端时间；
4. 使每次优化都能由结构化阶段耗时、测试结果、制品清单和 runner 用量直接验证；
5. 当并行化导致竞态、随机失败、资源争用、排队或成本超限时，可以单独回退测试并行度或构建 matrix 并发，不影响既有发布能力。

### 3.2 非目标

- 不移除、跳过、抽样或弱化任何测试与断言；
- 不移除 `-race`，不启用 Go 测试结果缓存来绕过 `-count=1`；
- 不减少 Server 的 7 个目标或 Agent 的 9 个目标；
- 不跳过 SHA256 生成、逐文件校验、artifact 完整性验证或 Release 下载后复验；
- 不通过提高失败重试次数掩盖随机失败；
- 不在本任务中修改公开产品接口、运行时架构或新增第三方依赖；
- 不承诺仅凭一次截图达到固定百分比收益。

## 4. 性能与质量契约

### 4.1 不可降低的质量门禁

候选方案必须保持以下行为等价：

| 能力 | 必须保持的契约 |
| --- | --- |
| 全量回归 | 每次发布实际执行一次 `go test -count=1 -race ./...`，退出码非零时发布失败 |
| 测试集合 | `go list ./...` 可见的全部含测试 package 均进入同一次回归，不允许按改动范围缩减 |
| 失败语义 | 任一测试、matrix 目标、校验、上传、聚合或下载失败，相关 Release Job 不得运行 |
| 平台覆盖 | Server 7 个目标、Agent 9 个目标及其名称、架构和链接参数不变 |
| 制品完整性 | 每个二进制恰有一个匹配的 `.sha256`；聚合后数量、文件名集合、校验内容全部匹配 |
| 条件发布 | 未变更组件继续不构建、不发布；变更组件必须等待测试和该组件全部构建成功 |
| 安全 | 缓存和 artifact 不保存 Secret、Token 或认证响应；Release 权限仍只授予发布 Job |

并行测试不得共享可变进程级状态。调用 `t.Setenv`、修改工作目录、替换全局函数或变量、占用固定端口、控制同一后台进程、读写同一路径或依赖严格时序的测试，在完成隔离前必须保持串行。使用 `t.Parallel()` 后，循环变量、临时目录和 fixture 必须由每个测试独占。

### 4.2 性能指标

性能实验使用两个固定版本的实验 Workflow：基线版本保持当前串行构建拓扑，候选版本使用本方案拓扑；两者调用同一提交、同一固定 Go patch 版本、相同测试与构建脚本、相同 runner label、相同环境变量和依赖锁文件，所有 GitHub Action 固定到相同完整 commit SHA。两种 Workflow 按 `基线→候选→候选→基线` 的顺序成组交错运行，至少完成 10 组，减少不同时段 runner 波动造成的偏差。若任一可固定项不同，该组无效并重跑；runner image 无法固定时必须记录 image version，只比较 image version 相同的配对样本。

生产 Release 由 `pull_request: closed` 触发，Run 34734225374 也显示 cache mode 为 `read` 且未命中。PR cache 具有 ref 作用域，GitHub cache 又不可原地更新，因此本文不把跨发布缓存命中作为提速前提。基线与候选性能测量均关闭 Go cache restore/save，以冷缓存形成可重复主验收组；基线和候选使用同一组 ID 关联配对结果。另行记录当前生产 Workflow 自然发生的 hit/miss，只作为观察数据，不用于证明候选达到性能目标，也不得通过清空生产缓存制造样本。

性能统计只对命令成功且环境有效的配对样本计算，但所有失败、取消和超时均进入可靠性分母，任一候选新增失败都会阻断验收。中位数按排序后中间值计算，偶数样本取两个中间值均值；P90 使用 nearest-rank，即排序后第 `ceil(0.9 × N)` 项。`runner 执行秒数` 定义为所有实际启动 Job 的 `completed_at - started_at` 之和；另行记录 GitHub 账单提供的计费分钟，不以自行换算值冒充计费数据。

初始目标如下，设计审查可根据正式基线收紧，但不得在实施后为迁就结果而放宽：

| 指标 | 初始目标 |
| --- | --- |
| Server-only `test` Job 冷缓存中位数 | 不高于 90 秒 |
| Server-only 构建关键路径冷缓存中位数 | 不高于 75 秒；从首个 Server matrix Job 启动到 Server 聚合成功 |
| Server-only 从 `prepare` 开始到 `release-server` 成功的冷缓存中位数 | 不高于 110 秒 |
| Agent-only 执行关键路径冷缓存中位数 | 相对正式基线至少降低 30% |
| Server+Agent 执行关键路径冷缓存中位数 | 相对正式基线至少降低 30% |
| 三类路径的用户等待时间 P90 | 不高于对应基线 P90 的 110% |
| 候选 runner 执行秒数中位数 | 不超过对应基线的 1.5 倍 |
| 候选 GitHub 计费分钟 | 若账户可取得账单证据，不超过对应基线的 1.5 倍 |
| 可靠性 | 基线和候选全部尝试均纳入统计，候选连续 30 次无新增随机失败、竞态报告、缺失制品或错误发布 |

分别报告两个端到端指标：`执行关键路径` 从首个 Job 的 `started_at` 到 Release Job 的 `completed_at`；`用户等待时间` 从 Workflow `created_at` 到 Release Job 的 `completed_at`，包含 Workflow 和 Job 排队。排队时间不计入代码执行性能收益，但按上表约束用户等待时间 P90。

## 5. 总体方案

方案分为四个阶段，严格按证据推进：

```text
阶段 A：建立可重复基线与分阶段计时
  → 阶段 B：优化最慢测试 package
    → 阶段 C：对跨平台构建做受控 matrix 并行
      → 阶段 D：真实发布对照验收与参数固化
```

阶段 A 不改变测试与构建调度，只增加可观测性。阶段 B 和阶段 C 可以分别回退；阶段 D 只有在质量、性能和成本三类门禁全部通过后才确认实施完成。

## 6. 阶段 A：基线与可观测性

### 6.1 采集内容

每个 Job 至少输出以下时间点和持续时间：

- Workflow 创建时间，以及 Job 启动与完成；
- checkout；
- `setup-go` 与缓存恢复，包含 cache hit/miss；
- 测试编译与执行；
- 每个 Go package 与测试用例耗时；
- 每个构建目标的开始、结束和退出状态；
- artifact 打包、上传、下载与聚合验证；
- Release 创建、下载复验和发布。

测试计时使用单次 `go test -json -count=1 -race ./...` 的事件流，解析器通过管道读取时必须启用 `pipefail` 并保留 `go test` 的原始退出码；不得为采集时间再运行第二遍测试。逐 package 和逐测试耗时来自该事件流。Go 工具没有提供可直接等价拆分“全仓编译”和“测试执行”的稳定字段时，必须标记为不可分离，不通过额外预编译改变缓存状态来制造伪精度。

Workflow 创建时间来自 Workflow Run API，Job 启动和完成时间来自 Jobs API；当前 API 不提供可靠的每 Job `queued_at`，因此不宣称能分离每个 Job 的排队耗时。`用户等待时间` 直接使用 Workflow `created_at` 到 Release 完成时间，依赖满足到 Job 启动的间隔只作为“调度与排队合计”观察值。构建目标在命令执行器内部记录单调时钟起止值。原始 `go test` 仍是唯一测试通过条件。结构化计时只是旁路证据；解析或采集失败必须使性能验收无结论，但不能把失败测试改写为成功。

### 6.2 基线分组

至少覆盖三类真实变更：

1. 仅 Server 版本变化；
2. 仅 Agent 版本变化；
3. Server 与 Agent 同时变化。

每类按第 4.2 节执行冷缓存交错配对，并把自然缓存状态作为附加维度记录。性能实验 Workflow 不创建 Release，但必须调用与真实发布相同的测试脚本、构建脚本、目标清单和聚合验证器；实验 Workflow 不得拥有 `contents: write` 权限。真实发布 Workflow 另执行 Server-only、Agent-only 和双组件冒烟，证明实验拓扑与发布拓扑一致。

## 7. 阶段 B：测试提速

### 7.1 热点定位

以 GitHub runner 的逐 package 和逐用例耗时为准。Run 34734225374 建议按以下顺序先检查：

- `internal/auth`；
- `internal/api`；
- `cmd/homeagent-agent`。

`internal/daemon`、`internal/qualitygate` 和 `internal/serverupgrade` 可以保留为本地候选热点，但在新的 GitHub 分阶段数据证明其进入关键路径前，不应优先于上述三个 package。

每个候选改动必须闭合“耗时证据 → 触发路径 → 等价同步或隔离改造 → 候选耗时 → 竞态与稳定性结果”。没有直接证据时只能记录为候选原因。

### 7.2 安全的包内并行

只对满足以下条件的测试或子测试启用 `t.Parallel()`：

1. 临时目录、监听地址、存储实例、时钟和依赖均为用例独占；
2. 不写进程级环境、当前目录或共享全局变量；
3. 不依赖其他测试先执行或完成；
4. 并发运行时资源消耗有上界；
5. 单独、全包、全仓和 `-race` 模式均通过。

包内并行度默认交给 Go 测试运行器管理。只有真实 runner 发生 CPU、内存或端口资源争用时才设置显式上限；上限必须来自测量，不写死为单线程。

### 7.3 消除固定等待

固定 `time.Sleep` 应按真实意图替换为：

- 被测代码可观察事件或 channel；
- 带明确条件和截止时间的轮询；
- 可注入的时钟、ticker 或调度器；
- 后台 goroutine、进程或连接的显式 ready/done 信号。

替换后仍保留失败截止时间。禁止把 Sleep 简单缩短到更脆弱的数值，也禁止删除对异步完成、清理或超时行为的断言。测试必须增加反例，证明事件未发生时会在上限内安全失败，而不是误通过。

### 7.4 复用测试构建制品

若同一 package 的多个测试重复执行等价 `go build`，可在包级测试生命周期内构建一次不可变二进制，由各用例复制或只读复用。复用键至少包含源码提交、Go 版本、目标平台、构建标签和链接参数；构建失败必须使所有依赖用例失败，不得回退到旧制品。

跨 package 或跨 Job 复用测试二进制不作为首选，因为它会扩大缓存失效和错误制品污染边界。只有阶段 A 证明重复编译是关键成本，才另行设计带摘要的 artifact 复用契约。

## 8. 阶段 C：跨平台构建提速

### 8.1 每目标 matrix 与全局并发预算

为满足“批量结果来自每个目标独立状态”的仓库契约，每个 `component + GOOS + GOARCH` 是一个 matrix 项和独立 Job，不在单个 Shell 循环中容纳多个目标。新增一个受版本控制的静态目标清单作为 16 个目标的唯一事实源，包含组件、平台、架构、输出名和链接版本变量；当前串行构建、候选 matrix 生成器、聚合器和结构测试都读取该清单，禁止在 Workflow 中复制第二份目标集合。

`prepare` 根据组件版本变化，从静态清单分别输出 Server 和 Agent 两份 JSON matrix；对应组件无变化时输出空集合并跳过该组件构建 Job。两个构建 Job 的 `strategy.matrix` 只消费各自输出，不在 Job 级 `if` 中引用尚未展开的 `matrix` 上下文。生成器必须拒绝未知组件、重复或缺失目标、非法 JSON 和空字段；结构测试确认 Server-only 为 `7+0` 项、Agent-only 为 `0+9` 项、双组件为 `7+9` 项、无变化为 `0+0` 项。

Server 和 Agent 使用独立 matrix，使一个组件失败不会污染另一个组件的 `needs` 结论。单组件变化时对应 matrix 的 `max-parallel` 为 4；双组件变化时两边各为 2，因此构建 runner 合计最多为 4，再加独立 `test` Job，测试与构建总并发预算最多为 5。该条件值只依赖 `prepare` 的组件变化输出，不依赖 matrix 项。正式数值必须由阶段 A 的排队、执行时间和 runner 成本验证，可以降低，但总和不得超过设计评审确认的账户并发预算。

每目标 Job 会增加 checkout、setup 和 artifact 固定开销。若候选 runner 执行秒数超过第 4.2 节上限，即使墙钟时间更短也不得验收；应降低 `max-parallel`，而不是合并目标后失去独立终态。

### 8.2 每目标输出契约

每个 matrix Job 只上传自己的二进制、对应 `.sha256` 和机器可读状态清单。清单至少包含：

- 组件；
- `GOOS`、`GOARCH`；
- 输出文件名；
- 二进制 SHA256；
- 构建提交；
- Go 版本；
- matrix 目标 ID；
- 业务状态 `succeeded|failed|canceled|timed_out|skipped|unknown` 及失败阶段。

构建命令失败时，Job 仍须通过 `if: always()` 尝试上传失败状态清单，随后保持非零结论；不得上传可供发布使用的成功二进制 artifact。同一 artifact 名称不得由多个 Job 共同写入。目标成功制品与失败状态 artifact 的名称和内部清单包含 `run_id + run_attempt + component + target_id`；聚合 artifact 包含 `run_id + run_attempt + component + aggregate`。聚合器只接受当前 run 和 attempt 的精确前缀，旧 attempt、重复名称或身份字段不一致均失败。

### 8.3 聚合与发布状态

为每个变更组件增加唯一聚合 Job。聚合 Job 使用 `if: always()` 和最小的 `actions: read` 权限，并从以下两条独立证据聚合结果：对应组件 matrix 的 `needs` 总结论用于阻断；GitHub Actions Jobs API 按当前 `run_id`、`run_attempt`、提交和 matrix 目标名读取每个子 Job 的真实终态。Server 聚合不读取 Agent matrix 结论，Agent 聚合也不读取 Server matrix 结论。API 不可用、目标无法唯一匹配或状态不是终态时，聚合安全失败。artifact 只证明输出内容，不能替代 Job 终态；API 响应不得包含或落盘认证头。

GitHub Job conclusion 到业务状态固定映射如下，禁止直接透传未知协议值：

| GitHub conclusion | 业务状态 | 聚合结果 |
| --- | --- | --- |
| `success` | `succeeded` | 继续校验 artifact |
| `failure` | `failed` | 失败 |
| `cancelled` | `canceled` | 失败；允许状态 artifact 缺失 |
| `timed_out` | `timed_out` | 失败；允许状态 artifact 缺失 |
| `skipped` | `skipped` | 仅目标不适用时接受；适用目标出现则失败 |
| `neutral`、`startup_failure`、`stale`、`action_required`、`null` 或未知值 | `unknown` | 安全失败并保留原值诊断 |

取消、超时或 runner 启动失败时通常无法上传状态清单；此时以 Jobs API 终态和预期目标集合生成缺失记录，不把 artifact 缺失误判为成功。

聚合 Job 必须：

1. 取得该组件全部预期 matrix 子 Job 的独立终态；
2. 将每个 artifact 下载到独立目标目录，禁止使用会在验证前合并同名文件的 `merge-multiple`；
3. 验证目标清单的提交、组件、matrix 目标 ID 和 Job 终态；
4. 验证目标全集与预期集合精确相等，无缺失、重复或额外目标；
5. 通过 Artifacts API 取得每个 artifact ID 和平台记录的 SHA256 digest，再通过 REST 下载原始 artifact archive 并对原始字节重算 SHA256；API digest 缺失、算法未知或不相等均失败，不依赖下载动作的 warning 文本或不存在的 digest 输出；
6. 解压到独立目录并验证通过后才复制到空的聚合目录，重复文件名立即失败，禁止覆盖；
7. 验证每个二进制及其 `.sha256`；
8. 验证文件总数：Server 14 个，Agent 18 个；
9. 输出单一聚合 artifact，供 Release Job 使用。

任一目标被取消、失败、超时、缺失或上传不完整时，聚合失败。Release Job 的 `needs` 必须指向 `test` 和对应聚合 Job，不直接依赖任一 matrix 子 Job。

### 8.4 fail-fast 选择

构建 matrix 使用 `fail-fast: false`，使一个目标失败时其余目标仍独立执行并产生终态；这符合批量结果必须从每个目标独立状态聚合的仓库规则。聚合 Job 只在全部目标成功且 artifact 完整时输出成功。

该选择可能比 `fail-fast: true` 消耗更多失败场景 runner 分钟，但不会因首个失败取消其他目标而丢失独立状态。若未来希望快速终止，必须单独修改批量状态契约并重新评审。

## 9. 缓存策略

生产 Release 候选首期关闭 `setup-go` 内置缓存，也不增加自定义 cache action；测试和构建始终从源码及模块代理完成，主性能指标按冷缓存验收。这避免把 `pull_request` ref 作用域、不可变 cache key 和并行保存竞争引入发布正确性或性能结论。Run 34734225374 中依赖下载约 4 秒，当前证据也不足以证明缓存设计比测试与 matrix 优化更优先。

缓存提速若未来单独实施，必须另行设计并满足：

1. 由受信任的 `push` 或 `workflow_dispatch` main-scope Job 作为唯一写者，Release 的 PR Job 只能 restore，不能写缓存；
2. 保存键包含 runner OS、精确 Go patch、角色/目标、`go.sum`、缓存 schema 和唯一源码提交，保证每次保存使用新键；
3. restore 使用不含提交的稳定前缀取得最近兼容代，不能期望不可变精确键被后续提交更新；
4. 清楚区分模块缓存与各目标构建缓存，评估容量、逐出和预热本身的 runner 成本；
5. 缓存缺失、partial hit 或损坏时必须从源码完整重建，最终测试、编译和 SHA256 验证不得跳过；
6. 不缓存发布目录、签名文件、Token、GitHub CLI 状态或任何 Secret；
7. 通过真实生产触发链证明后续 `pull_request: closed` Run 可以读取 main-scope 缓存，不能只用实验 Workflow 的 exact hit 代替。

在该独立设计和真实验证完成前，任何自然 cache hit 只作为附加观察数据，不计入本文的性能成功标准。

## 10. 状态、失败与回退

### 10.1 状态矩阵

| `test` | 对应构建聚合 | Release 行为 |
| --- | --- | --- |
| 成功 | 成功 | 允许创建草稿 Release 并执行下载复验 |
| 失败、取消或超时 | 任意 | 禁止发布 |
| 成功 | 失败、取消、超时或 artifact 不完整 | 禁止发布 |
| 跳过 | 组件发生变化 | 视为 Workflow 契约错误，禁止发布 |
| 成功 | 因组件未变化而跳过 | 对该组件不发布，另一组件独立判定 |

Server 与 Agent 同时变化时维持现有独立发布语义：一个组件通过测试、对应构建聚合和发布复验后可以成功发布，即使另一个组件构建或发布失败。整体 Workflow 结论为失败，并明确记录部分发布事实；不得回滚已公开且校验通过的另一个组件 Release。

当前 Release Job 先创建草稿再下载复验。创建请求必须在草稿正文中原子写入不可变来源标记 `run_id + run_attempt + commit + component`，并记录预期 tag。每个组件增加独立的 `if: always()` 同运行补偿 Job；另增加由 `workflow_run: completed` 触发且支持人工 `workflow_dispatch` 的外部对账 Workflow，处理 runner 丢失、整条运行强制取消或同运行补偿未启动的情况。外部对账只接收已完成的本 Release Workflow，使用最小 `contents: write` 和 `actions: read` 权限。

发生创建响应丢失时，同运行补偿和外部对账按预期 tag 进行最长 5 分钟的有界轮询，使用指数退避并在截止前执行最终查询，以覆盖 GitHub API 可见性延迟。若上传、下载、SHA256 复验或转正式发布失败，补偿执行者只允许删除来源标记精确匹配、目标提交匹配且仍为 draft 的 Release，再独立查询并删除精确 tag；两项删除后必须分别查询确认不存在，才允许重跑。

若 Release 已删除但 tag 删除失败、目标已公开、身份不匹配、对账超时或任一最终查询失败，则保留可观察的失败状态并标记 `manual_recovery_required`。后续 `prepare` 必须同时查询 Release 和 Git ref；任一仍存在都阻止同 tag 发布并输出人工恢复指引。不得删除本次运行开始前已存在的 Release 或 tag。补偿逻辑必须覆盖发布 Job failure、cancelled、timed_out、强制取消、响应丢失、延迟可见和部分清理失败反例。

### 10.2 回退机制

- 测试并行导致不稳定时，只回退有证据关联的 package 或用例并行改动，保留计时能力；
- 构建 matrix 收益不足、成本超限或排队严重时，将 `max-parallel` 回退至上一已验证值；
- artifact 聚合存在平台问题时，回退到当前单 Job 串行构建，同时在串行执行器内继续为每个目标记录独立状态且不中途遗漏后续目标；
- 回退不能禁用 `-race`、删除测试、减少平台或放松校验；
- 任何候选连续运行出现新增随机失败、竞态报告、缺失或重复制品，立即停止性能验收并恢复上一稳定配置。

## 11. 实施范围建议

后续实施应拆分为独立任务，避免同时改变测试同步语义和发布拓扑而难以归因：

| 任务 | 主要允许路径 | 产物 |
| --- | --- | --- |
| A：性能证据 | `.github/workflows/**`、计时脚本及其测试 | 基线与结构化耗时 |
| B1：Agent/Daemon 测试 | 对应测试与必要的可测试性扩展点 | 安全并行、事件同步、回归测试 |
| B2：其他热点测试 | 经阶段 A 证明的包 | 按热点逐包优化 |
| C：构建 matrix | `.github/workflows/release.yml`、Workflow 契约测试 | 每目标 matrix、聚合与制品完整性 |
| D：验收与固化 | 设计状态、验收证据 | 对照结果、最终参数、回退结论 |

每个实施任务必须有自己的 `changes/<task>.yaml`。测试辅助扩展点不得改变现有公开接口；若必须新增外部依赖，需先说明标准库和现有依赖不能满足的必要性并重新评审。

## 12. 测试策略

### 12.1 测试优化验证

每个被并行化或改造同步方式的 package 至少执行：

1. 单个目标测试重复运行；
2. 整个 package `-count` 多次运行；
3. 整个 package `-race` 多次运行；
4. 全仓 `go test -count=1 -race ./...`；
5. 并行压力下的失败路径、超时、清理和无 goroutine/进程残留验证；
6. 真实端口与协议链路回归。

不得以单元 mock 替代仓库要求的真实端口和协议验证。测试替身必须源自真实行为观察并记录差异。

### 12.2 Workflow 契约测试

Workflow 结构测试至少显式断言：

- 测试命令按参数解析后精确包含 `go test`、`-json`、`-count=1`、`-race` 和唯一包模式 `./...`，不依赖参数顺序或旧命令连续字面串；
- 不存在 `-p 1`、跳过测试、忽略失败或降低门禁的参数；
- 静态目标清单中 Server 与 Agent 集合分别精确等于现有 7 和 9 个目标，动态过滤四种组合结果正确；
- 每个目标恰好属于一个 matrix 项；
- 所有 matrix 目标使用相同提交、Go 版本、`CGO_ENABLED=0`、`-trimpath` 和既有 `ldflags`；
- matrix 单项失败、取消、超时、API 状态缺失和 artifact 缺失时聚合失败；
- 重复目标、额外目标、错误文件名、错误摘要和错误提交均安全失败；
- Server-only、Agent-only、Server+Agent 与无版本变化四类条件路径正确；
- 双组件运行中 Agent 失败不阻断已满足自身门禁的 Server，Server 失败也不阻断 Agent；
- 全量重跑、失败 Job 重跑和单 Job 重跑时只消费当前 `run_attempt` artifact，旧 attempt 无法混入；
- Release Job 只消费通过聚合验证的 artifact；
- 发布后下载的全部制品再次通过 SHA256 和数量验证；
- 发布草稿创建后失败、取消、超时、响应丢失、来源标记不匹配和已公开六类补偿路径符合第 10.1 节边界。

### 12.3 完成门禁

实施代码或 Workflow 后仍须执行仓库全部完成门禁，包括变更范围、架构依赖、新模块测试、真实端口和协议的全量 `-race` 回归、Diff Coverage，以及涉及发布时从上一正式版本经真实用户入口完成升级冒烟测试。

## 13. 验收证据

最终验收包至少包含：

- 基线与候选各次 Workflow run URL、提交、runner image、Action SHA、精确 Go patch 和缓存 exact/partial/miss 状态；
- 每次 Job 与阶段耗时原始记录；
- 按第 4.2 节计算的冷缓存配对中位数、P90、最大值、runner 执行秒数和 GitHub 计费分钟对比，以及生产自然 cache hit/miss 观察数据；
- 全量 `-race` 命令及退出结果；
- 逐 package 结果、竞态报告和连续稳定性运行记录；
- Server 14 文件、Agent 18 文件的目标清单、SHA256 和聚合结果；
- Server-only、Agent-only、Server+Agent 三条发布路径；
- 上一正式版本到候选版本的真实升级验收；
- 缓存命中与失效行为、matrix 排队与并发行为、artifact 上传下载和摘要验证的真实 GitHub Actions 日志；
- 测试替身来源、真实协议观察及已知差异；
- 失败注入结果和回退演练记录。

任何数据缺失、样本混组、测试失败后重试取成功值、制品集合不完整或 runner 用量超过约束，都不得标记验收通过。

## 14. 成功标准映射

| 成功标准 | 直接验证手段 |
| --- | --- |
| 测试质量不降低 | Workflow 契约测试、完整测试清单、真实 `-count=1 -race` 日志和连续稳定性记录 |
| 测试墙钟时间达到目标 | 至少 10 次同组真实运行的中位数与 P90 对照 |
| 构建墙钟时间达到目标 | 分目标计时、matrix 关键路径和聚合 Job 计时 |
| 平台与制品完整 | 静态目标集合断言、聚合清单、文件数量及逐文件 SHA256 |
| 发布条件保持正确 | 四类组件变更组合的 Workflow 状态矩阵测试和真实运行 |
| runner 成本受控 | 每 Job 执行秒数聚合及可取得时的 GitHub billing/usage，候选中位数不超过基线 1.5 倍 |
| 无新增不稳定性 | 基线与候选全部尝试和失败分类，以及候选连续 30 次无新增失败、竞态、超时和资源残留 |
| 可安全回退 | 测试并行度与构建 matrix 并发各完成一次回退演练 |

## 15. 分阶段审查与未决问题

本文档允许分阶段评审，避免要求阶段 A 的产出在阶段 A 启动前已经存在：

### 15.1 授权阶段 A 前必须关闭

1. 用户确认 runner 执行秒数与 GitHub 计费分钟 1.5 倍上限符合成本预算；
2. 阶段 A 实施任务明确性能实验 Workflow、静态目标清单、计时脚本及测试的 `allowed_paths`，并证明实验 Workflow 无 `contents: write`；
3. 当前 `GenerateGrantID` 偶发重复作为独立可靠性任务先修复并通过全量门禁，避免污染基线；
4. 设计审查确认阶段 A 只增加观测与无发布权限的实验入口，不改变生产测试集合、构建目标和发布行为。

上述四项关闭后，文档设计状态可标记为“阶段 A 评审通过”，仅授权实施阶段 A，不代表阶段 B/C 已通过。

关闭记录（2026-09-13）：

1. 用户已确认 runner 执行秒数与 GitHub 计费分钟不超过基线 1.5 倍的成本上限；
2. 阶段 A 使用 `changes/github-workflow-performance-phase-a.yaml` 限定允许路径；实验 Workflow 仅有 `contents: read`，不创建 Release；
3. `GenerateGrantID` 的时间分辨率碰撞已由 `crypto/rand` 修复，Agent 版本同步升级至 `v0.6.16`；修复任务的范围、架构、模块、全仓 `-race` 与 Diff Coverage 门禁已通过；
4. 用户已明确授权阶段 A，生产 Release 仅改为调用与实验 Workflow 相同的测试和顺序构建执行器，Job 拓扑、测试集合、目标集合、发布条件与权限不变。

### 15.2 阶段 A 产证后、授权阶段 B/C 前必须关闭

1. `test` 的约 130 秒命令耗时中，逐 package、逐用例及可可靠识别的准备成本分别是多少；
2. Server 7 个和 Agent 9 个目标的单目标真实编译耗时及合理 `max-parallel`；
3. 仓库可用的 GitHub-hosted runner 并发额度、调度间隔和用户等待时间分布；
4. 哪些最慢测试可直接隔离并行，哪些需要先增加时钟、进程或存储扩展点；
5. 每目标 matrix 的 runner 成本是否满足第 4.2 节约束。

阶段 A 证据和上述结论必须回写本文档并重新确认。阶段 B 与阶段 C 可以分别评审和授权，但均不得在其未决问题关闭前实施；阶段 D 只有在 B/C 已实施并完成真实候选运行后才能验收。

### 15.3 阶段 A 本地实施证据

2026-09-13 的本地实施验证记录如下：

- 共享测试执行器实际运行 `go test -json -count=1 -race ./...` 一次，原始退出码为 0，总耗时 63 秒；事件流与汇总报告均成功生成；
- 汇总报告中 `internal/api`、`internal/auth`、`internal/ui`、`cmd/homeagent-agent` 和 `internal/daemon` 分别为 59.449、59.412、26.128、22.876 和 20.980 秒；这些是本地候选热点，不代替 GitHub 冷缓存基线；
- 共享顺序构建执行器完成 Server 7 个和 Agent 9 个目标，每个目标均有独立成功状态；聚合目录分别精确包含 14 和 18 个文件，逐文件 SHA256 全部通过；
- 实验 Workflow 固定 `ubuntu-24.04`、关闭 `setup-go` 缓存，只允许手动触发并保持 `contents: read`；checkout、setup-go 与 artifact 上传分别固定到官方仓库已核实的完整提交 SHA；
- 尚未在 GitHub-hosted runner 上采集 10 组 `基线→候选→候选→基线` 数据，因此阶段 A 的远程性能基线、runner 排队与成本结论仍为待验收，阶段 B/C 不据本地数据启动。
