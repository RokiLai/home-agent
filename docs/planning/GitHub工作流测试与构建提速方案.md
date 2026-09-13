# GitHub 工作流测试与构建提速方案

## 1. 文档状态

| 阶段 | 状态 | 说明 |
| --- | --- | --- |
| 设计 | 评审通过 | 方案定案并获用户明确授权实施 |
| 实施 | 实施完成 | 测试模式 Bcrypt 代价解耦、前端 Node 契约测试并发化、多目标并发构建升级与客户端版本联动已实施完毕 |
| 验收 | 本地通过 | 本地质量门禁全部通过：全量 -race 回归耗时降至 33.7s，Diff Coverage 83.3%（5/6）；待 PR 合并至 main 触发真实 Release 工作流最终验收 |

### 1.1 设计审查待决项

1. **测试环境 Bcrypt Cost 切换机制**：确定采用显式测试包钩子（`internal/auth.SetBcryptCostForTest`），确保生产环境不受影响且契约安全。
2. **构建并发控制度**：确定 `build-server`（7 目标）与 `build-agent`（9 目标）在 GitHub Actions 标准 2 vCPU Runner 上的并发度参数（推荐 `-P 2`），兼顾内存安全与吞吐提速。
3. **缓存锁冲突消除与隔离策略**：针对并行 Job 抢占同一 `setup-go` Cache Key 导致的缓存保存失败问题，明确跨 Job 缓存隔离或显式缓存恢复机制。
4. **工作流契约兼容性**：确保 `.github/workflows/release.yml` 的修改严格通过 `internal/qualitygate/releaseworkflow_test.go` 的契约断言，不引入破坏性变更。

## 2. 背景与问题定义

在 GitHub Actions 工作流运行记录 [Run #34734225374](https://github.com/RokiLai/home-agent/actions/runs/34734225374) 中，PR 触发的 Create Release 流程总耗时为 **2分41秒**。当前工作流拓扑结构如下：

```text
       prepare (5s)
          |
    +-----+-----+----------------+
    |           |                |
    v           v                v
test (2m21s)  build-server (1m48s) build-agent (跳过或~2m10s)
    |           |                |
    +-----+-----+----------------+
          |
          v
  release-server / release-agent (10s)
```

因为汇聚任务 `release-server` 声明了 `needs: [prepare, test, build-server]`，整个发布工作流的耗时被最慢的关键路径 `test` Job 严重卡死。同时，一旦 `test` 耗时被压缩，耗时达 1分48秒 的 `build-server`（以及包含 9 个架构构建的 `build-agent`）将立刻成为新的流水线瓶颈。

### 2.1 真实时序与日志证据

基于 Run #34734225374 API 与详细执行日志，各环节关键指标如下：

1. **Job 级耗时**：
   - `prepare`: 02:55:34 ~ 02:55:39（耗时 **5s**）
   - `test`: 02:55:41 ~ 02:58:02（耗时 **2m 21s**，步骤 `Run full regression tests` 耗时 **2m 10s**）
   - `build-server`: 02:55:41 ~ 02:57:29（耗时 **1m 48s**，步骤 `Build server release assets` 耗时 **1m 37s**）
   - `build-agent`: 本次因仅修改服务端而跳过；若触发客户端版本发布，其包含 9 个架构目标，按相同基线测算串行编译耗时约为 **2m 05s ~ 2m 15s**。
   - `release-server`: 02:58:05 ~ 02:58:15（耗时 **10s**）

2. **构建缓存失效与锁冲突证据**（查看 `build-server` 详细步骤日志）：
   - `Set up Go` 步骤启动时日志：
     ```text
     [command]/opt/hostedtoolcache/go/1.26.8/x64/bin/go env GOCACHE
     /home/runner/go/pkg/mod
     /home/runner/.cache/go-build
     Cache is not found
     ```
     缓存未能命中，导致该次运行完全处于冷编译状态。
   - `Post Set up Go` 步骤后置处理日志：
     ```text
     [command]/usr/bin/tar --posix -cf cache.tzst --exclude cache.tzst -P -C ...
     Failed to save: Unable to reserve cache with key setup-go-Linux-x64-ubuntu24-go-1.26.8-8902bc95268678b1a37c1752b1a27ba7ed01d2e8d9d1d97720859d177d7ba5b9, another job may be creating this cache.
     ```
     `test` 与 `build-server`（以及可能的 `build-agent`）完全并行运行且共用相同的 `go.sum` 派生缓存键，在结束时发生抢占冲突，导致编译缓存无法持久化保存。

3. **`test` Job 内部各包耗时分析**（执行 `go test -count=1 -race ./...`）：
   - `homeagent/internal/auth`: **101.823s**
   - `homeagent/internal/api`: **98.689s**
   - `homeagent/internal/ui`: **30.235s**
   - `homeagent/internal/qualitygate`: **19.785s**
   - `homeagent/internal/daemon`: **11.963s**
   - 其余所有业务包（共计 30 余个）：单包耗时均在 **1.0s ~ 2.0s** 之间。

4. **测试函数耗时采样证据**（以本地多核执行结果为基准）：
   - `internal/auth` 包中：
     - `TestUpdateAdminPassword`: 14.72s
     - `TestUserManager_MultiUserCRUDAndInvariants`: 8.36s
     - `TestSessionVersionInvalidation`: 6.42s
     - `TestSessionManager`: 6.25s
     - `TestHashAndCompare`: 6.17s
     - `TestMigration_LegacySingleAdminToMultiUser`: 4.23s
     - `TestRequirePermissionMiddleware`: 4.22s
     - `TestRequireAdminMiddleware`: 2.08s
   - `internal/api` 包中：
     - `TestAuthChangePasswordFlow`: 14.96s
     - `TestMultiUser_DeviceIsolationAndGrants`: 6.50s
     - `TestMultiUser_UserManagementAndTransferEndpoints`: 6.40s
     - `TestMultiUser_UpgradeAndRollbackCompatibility`: 6.39s
     - `TestAdminAuthFlow_LoginMeLogout`: 6.34s
     - `TestMultiUser_AuditLoggingAndLogoutAll`: 4.29s
     - `TestEnrollmentTokensAndClaimFlow`: 2.22s
     - `TestAuthStatusPublicURL`: 2.09s
     - `TestLegacyJoinTokenMigration`: 2.08s

### 2.2 核心根因分析

1. **密码哈希算法代价固定导致 CPU 盲目空转**：
   - 依据 `internal/auth/hash.go` 第 16 行定义：`DefaultBcryptCost = 12`。
   - Bcrypt 具有指数时间复杂度，Cost 12 意味着单次密码哈希需要执行 $2^{12} = 4096$ 轮散列运算。在开启 `-race` 内存竞争检测时，单次计算耗时约为 250ms ~ 500ms。
   - `internal/auth` 与 `internal/api` 的单元测试重点覆盖了多用户账号体系、管理员初始化引导、密码修改、登录登出、会话失效、权限校验等高频密码操作，累计计算数百次 Bcrypt。在 GitHub Actions 2 vCPU 的 runner 上，单测耗费了超过 200 秒的纯 CPU 算力。
2. **前端 DOM/契约测试串行执行**：
   - `internal/ui/embed_test.go` 中有 11 个测试函数，其中 10 个测试函数通过 `exec.Command("node", "--test", ...)` 独立拉起 Node.js 进程。
   - Go 测试默认在单包内串行执行函数（未调用 `t.Parallel()`），导致 10 个 Node 进程逐一串行启动和初始化，耗费 30.2 秒。
3. **发布资产跨平台构建串行循环**：
   - `.github/workflows/release.yml` 的 `build-server` 任务使用 Shell `while read` 循环，依次串行编译 7 个架构目标（`linux/amd64`, `linux/arm64`, `linux/arm`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`, `windows/arm64`）。
   - 单个架构编译耗时约 12~14 秒，7 个架构累计串行耗时达 1分37秒；`build-agent` 拥有 9 个架构，串行耗时约 2分10秒。
4. **多 Job 并行触发 Cache Key 锁竞争导致持续冷编译**：
   - `test`、`build-server` 和 `build-agent` 三个 Job 同时启动并同时使用 `actions/setup-go@v5` 默认生成的同一 Cache Key；
   - 任务结束时产生并发写锁竞争，GitHub Actions 抛弃后完成者的缓存保存；
   - 导致后续构建流水线无法复用交叉编译预热产物，每次发布都必须从零全量重编标准库与依赖。

## 3. 目标与非目标

### 3.1 核心目标

1. **不减损单测质量与断言覆盖**：严禁删除、跳过或弱化任何断言；保留全部单测用例与功能覆盖。
2. **测试全量 `-race` 回归耗时大幅缩短**：
   - `internal/auth` 测试耗时从 101.8s 降至 2s 以内；
   - `internal/api` 测试耗时从 98.7s 降至 5s 以内；
   - `internal/ui` 测试耗时从 30.2s 降至 8s 以内；
   - `test` Job 总耗时从 2分21秒 压缩至 30~40 秒以内。
3. **构建流程并发化与缓存有效化**：
   - `build-server` 构建耗时从 1分37秒 降至 50~60 秒（冷构建）/ 30~40 秒（命中缓存）；
   - `build-agent` 构建耗时从 2分10秒 降至 65~75 秒（冷构建）/ 35~45 秒（命中缓存）；
   - 严格兼容 `internal/qualitygate/releaseworkflow_test.go` 既有契约断言。
4. **工作流端到端提速**：
   - GitHub Release 工作流总耗时从 2分41秒 降至 1分10秒 左右（提速超 55%），命中缓存时可进一步逼近 1 分钟以内。

### 3.2 非目标

1. 不降低生产环境密码安全等级（生产环境 `DefaultBcryptCost` 保持 12 不变）。
2. 不使用缓存测试结果（保留 `go test -count=1 -race ./...` 严格全量重跑的要求）。
3. 不调整代码质量门禁及 Diff Coverage 门禁阈值。

## 4. 详细方案设计

### 4.1 核心优化一：测试模式密码哈希代价（Bcrypt Cost）解耦

#### 4.1.1 密码学原理与测试有效性评估
- Bcrypt 算法的工作因子（Cost）设计目的是抵抗离线暴力破解（Brute-force Attack）。
- 单元测试与接口集成测试验证的是系统的**逻辑状态机与安全契约**：
  - 正确密码能够成功登录；
  - 错误密码必须被严格拒绝；
  - 密码被更新后旧密码即刻失效；
  - 重复多次哈希生成不同的 Salt；
  - 时序比较（Constant-time Compare）与哈希字符串长度合规。
- 无论 Cost 是 4 还是 12，上述所有业务逻辑分支、错误路径与校验契约均 100% 执行且完全一致。
- 行业规范（Go 官方 `golang.org/x/crypto/bcrypt`、OWASP 测试指南及主流框架测试套件）均推荐：**生产使用安全 Cost，单元与回归测试使用 `bcrypt.MinCost`（即 4）**。

#### 4.1.2 架构设计与实现规格
1. 在 `internal/auth/hash.go` 中，保留生产常量并引入受控的当前代价变量：
   ```go
   const (
       // DefaultBcryptCost 是生产环境下密码哈希的标准计算代价 (2^12 轮次)
       DefaultBcryptCost = 12
   )

   var currentBcryptCost = DefaultBcryptCost

   // SetBcryptCostForTest 允许测试套件在初始化或测试运行期间调整哈希代价。
   // 返回恢复函数以支持 defer 还原。
   func SetBcryptCostForTest(cost int) func() {
       prev := currentBcryptCost
       currentBcryptCost = cost
       return func() {
           currentBcryptCost = prev
       }
   }
   ```
2. `HashPassword` 改造为引用 `currentBcryptCost`：
   ```go
   func HashPassword(password string) (string, error) {
       bytes, err := bcrypt.GenerateFromPassword([]byte(password), currentBcryptCost)
       if err != nil {
           return "", fmt.Errorf("hash password: %w", err)
       }
       return string(bytes), nil
   }
   ```
3. 在测试套件中全局启用 `MinCost`：
   - 在 `internal/auth/auth_test.go` 中，增加包级别 `init()` 声明：
     ```go
     func init() {
         SetBcryptCostForTest(bcrypt.MinCost)
     }
     ```
   - 在 `internal/api/api_test.go` 中，增加包级别 `init()` 声明：
     ```go
     func init() {
         auth.SetBcryptCostForTest(bcrypt.MinCost)
     }
     ```
4. **保留生产默认代价专用测试**：
   在 `internal/auth/auth_test.go` 中新增独立的断言测试，验证：
   - `DefaultBcryptCost == 12`；
   - 显式以 `DefaultBcryptCost` 执行 `bcrypt.GenerateFromPassword` 并通过 `CheckPassword` 校验，确保高强度哈希算法本身在运行时正确。

### 4.2 核心优化二：前端 Node 契约测试并行化

#### 4.2.1 现状与隔离性分析
`internal/ui/embed_test.go` 中的测试函数：
- `TestIdempotencyKeyBrowserCompatibility`
- `TestDeviceListResponseRendersDOMWithoutUncaughtErrors`
- `TestLogoutResponseRendersVisibleLoginPanel`
- `TestUpgradeAllFrontendTracksFinalOutcomes`
- `TestMobileResponsiveContractAndRendering`
- `TestMultiUserAndRBACDOMTests`
- `TestFrontendSyntaxAndScopeIntegrity`
- `TestConsoleInformationArchitecturePhase1`
- `TestConsoleInformationArchitecturePhase2`
- `TestConsoleInformationArchitecturePhase3`
- `TestBrowserLayoutAndAccessibility`
- `TestGetIndexHTML`
- `TestHandler`
- `TestFrontendBackendFieldContracts`
- `TestSyncHashPresentation`
- `TestESMModuleImportsResolve`

这些测试要么纯粹分析静态文件（如正则解析、反射、文件系统遍历），要么启动隔离的 Node 脚本并在隔离的内存 DOM 中运行，或者启动随机监听本地端口的 `httptest.NewServer`。测例之间**完全无共享可变状态**。

#### 4.2.2 优化实现
在各个独立的 `Test*` 函数入口第一行增加：
```go
t.Parallel()
```
Go 测试运行器会在多核心 CPU 上同时调度多个测试子进程，将原本串行排队 30 秒的 10 个 Node 进程并发消化，测试耗时预计缩减至 5~8 秒。

### 4.3 核心优化三：跨平台发布构建并发化

#### 4.3.1 质量门禁契约约束
查看 `internal/qualitygate/releaseworkflow_test.go`，门禁对 `.github/workflows/release.yml` 施加了以下强契约断言：
1. `required` 片段必须全部包含，包括 `CGO_ENABLED=0`、`sha256sum`、`actions/upload-artifact@v4`、`actions/download-artifact@v4` 等；
2. 目标架构列表（`server linux amd64 homeagent-server-linux-amd64`、`agent linux mips homeagent-agent-linux-mips` 等）必须出现且仅出现一次；
3. 禁止包含 `quality-gate.sh`、`docker`、`deploy` 等多余动作；
4. Job 依赖链必须为 `release-server` 依赖 `[prepare, test, build-server]`，`release-agent` 依赖 `[prepare, test, build-agent]`。

#### 4.3.2 优化实现
在保持原有目标声明列表（Heredoc `TARGETS`）完全不变的前提下，将串行的 `while read` 改为受控并发构建（通过 `xargs -P 2`）。以 `build-server` 为例：

```bash
mkdir -p dist
build_target() {
  local component="$1" goos="$2" goarch="$3" output="$4"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath \
    -ldflags "-s -w -X homeagent/internal/version.ServerVersion=${SERVER_VERSION}" \
    -o "dist/${output}" "./cmd/homeagent-${component}"
  (cd dist && sha256sum "$output" > "${output}.sha256")
}
export -f build_target
export SERVER_VERSION

cat <<'TARGETS' | xargs -n 4 -P 2 bash -c 'build_target "$@"' _
server linux amd64 homeagent-server-linux-amd64
server linux arm64 homeagent-server-linux-arm64
server linux arm homeagent-server-linux-arm
server darwin amd64 homeagent-server-darwin-amd64
server darwin arm64 homeagent-server-darwin-arm64
server windows amd64 homeagent-server-windows-amd64.exe
server windows arm64 homeagent-server-windows-arm64.exe
TARGETS

test "$(find dist -maxdepth 1 -type f | wc -l)" -eq 14
(cd dist && sha256sum -c ./*.sha256)
```

对 `build-agent` 采取完全相同的并发模式（9 个目标并发执行，校验文件数为 18）。在 GitHub Actions 2 vCPU 环境下，双并发构建不仅可以提升编译吞吐，还能交替利用 I/O 与 CPU，预计将构建耗时从 97 秒缩减至 50~60 秒，`build-agent` 耗时从 130 秒缩减至 65~75 秒。

### 4.4 核心优化四：构建缓存隔离与锁冲突消除

#### 4.4.1 锁冲突机理与解决策略
在现有工作流中，`test`、`build-server` 与 `build-agent` 同时使用：
```yaml
- name: Set up Go
  uses: actions/setup-go@v5
  with:
    go-version-file: go.mod
    cache: true
    cache-dependency-path: go.sum
```
当三个任务几乎同时结束时，GitHub Cache 服务只允许一个持有锁的作业完成 key 写入，其余均报错退出，导致后续工作流无法命中热缓存。

#### 4.4.2 优化方案
为避免破坏 `qualitygate/releaseworkflow_test.go` 对 `cache-dependency-path: go.sum` 的字符串契约断言，采用以下无损兼容策略：
1. `test` Job 保留 `cache: true` 与 `cache-dependency-path: go.sum`，负责通用的依赖模块与全量测试代码缓存；
2. 在 `build-server` 与 `build-agent` 中，利用独立的缓存作用域键（如使用专有 `actions/cache` 对 `~/.cache/go-build` 按照 `runner.os`-`build-server`-`hashFiles('go.sum')` 独立命名，或保持 `setup-go` 且通过差异化路径避免写锁碰撞）；
3. **缓存命中效果**：一旦构建缓存命中，Go 交叉编译各架构时无需重新解析标准库与第三方库，单个二进制构建耗时将从 14 秒锐减至 3~5 秒。

## 5. 预期耗时对比矩阵

| 流程阶段 | 当前耗时 (Run #34734225374) | 优化后预估 (冷构建) | 优化后预估 (命中热缓存) | 提速比率 (热缓存) | 质量与安全影响 |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **`prepare`** | 5s | 5s | 5s | 0% | 无变化 |
| **`test` (总计)** | **2m 21s** | **~30s - 40s** | **~30s - 40s** | **~75% ⬇️** | **断言 100% 保留，全量 -race 严格执行** |
| ↳ `internal/auth` | 101.8s | ~1.5s | ~1.5s | 98.5% ⬇️ | 业务与安全断言无损，生产默认保持 12 |
| ↳ `internal/api` | 98.7s | ~4.0s | ~4.0s | 96% ⬇️ | 完整 API 请求链路无损 |
| ↳ `internal/ui` | 30.2s | ~6.0s | ~6.0s | 80% ⬇️ | 所有 Node 契约用例并发执行 |
| ↳ `internal/qualitygate` | 19.8s | ~18.0s | ~18.0s | ~10% ⬇️ | 真实集成管道回归完整执行 |
| ↳ `internal/daemon` | 12.0s | ~11.0s | ~11.0s | ~8% ⬇️ | 守护进程与服务状态测试无损 |
| **`build-server`** | **1m 48s** | **~55s - 65s** | **~35s - 45s** | **~60% ⬇️** | **产物完整性及 SHA256 校验 100% 保持** |
| **`build-agent`** *(若触发)* | *~2m 10s* | *~65s - 75s* | *~40s - 50s* | *~65% ⬇️* | **产物完整性及 SHA256 校验 100% 保持** |
| **`release-server / agent`** | 10s | 10s | 10s | 0% | 无变化 |
| **流水线总计 (服务端发布)** | **2分 41秒** | **~1分 10秒** | **~50秒 - 55秒** | **~65% ⬇️** | **全流程质量零妥协** |
| **流水线总计 (客户端发布)** | *~2分 45秒* | *~1分 20秒* | *~55秒 - 1分钟* | *~65% ⬇️* | **全流程质量零妥协** |

## 6. 验证与验收策略

1. **功能与质量门禁验证**：
   - 执行 `./scripts/quality-gate.sh`，确保通过变更清单校验、架构依赖检查、全量 `-race` 回归与 Diff Coverage 门禁（≥ 60%）。
   - 验证 `internal/qualitygate/releaseworkflow_test.go` 契约测试 100% 通过，杜绝破坏工作流契约。
2. **生产安全基线验证**：
   - 验证不调用 `SetBcryptCostForTest` 时，直接初始化生产模块生成的密码哈希确实使用 Cost 12。
3. **真实 CI 运行验证**：
   - 提交流水线并在真实的 GitHub Actions 环境中运行，观察验证：
     - 构建阶段不存在 Cache 锁保存冲突；
     - 观察多架构并发构建的 CPU/内存稳定性与实际总时长。
