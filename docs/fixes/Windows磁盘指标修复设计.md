# Windows 磁盘容量指标采集与缺失展示修复设计

## 1. 文档状态

| 阶段 | 状态 | 说明 |
|---|---|---|
| 设计 | 已审查通过 | 功能边界、Win32 API 契约、UI 容器布局对齐契约、兼容矩阵、外部假设审计及测试策略审查通过 |
| 实施 | 已完成 | Windows Agent 磁盘容量采集、UI 缺失态保护、版本升级 v0.6.5 及测试套件全部落地 |
| 验收 | 已完成 | 单元测试、负例测试、真实无头浏览器 Layout 测试、跨平台编译、全量 -race 与 100% Diff Coverage 全部通过 |

## 2. 背景、直接证据与根因边界

### 2.1 用户现象
在 HomeAgent Web 管理页面的设备健康详情弹窗（`#deviceHealthModal`）中，Windows 设备在运行时指标栅格中显示：
`磁盘使用率 [C:\] (0%)`，数值显示 `0.0 / 0 GiB`。

### 2.2 已确认的代码证据
1. `cmd/homeagent-agent/runtime_windows.go` 中的 `populatePlatformRuntimeFacts` 仅包含硬编码的占位桩 `facts.DiskMount = "C:\\"`，未调用任何 Win32 API 采集 `facts.DiskTotalBytes` 与 `facts.DiskAvailableBytes`。
2. `internal/device/device.go` 中 `DiskTotalBytes` 与 `DiskAvailableBytes` 带有 `omitempty` 标签；采集端为零值时，JSON 序列化省略这两个字段。
3. `internal/ui/static/js/devices/actions.js`（第 569-591 行）在渲染 `#healthModalFactsGrid` 时，未校验 `disk_total_bytes` 是否有效，直接执行：
   ```javascript
   const diskTotalGB = facts.disk_total_bytes ? (facts.disk_total_bytes / (1024 * 1024 * 1024)).toFixed(1) : 0;
   const diskAvailGB = facts.disk_available_bytes ? (facts.disk_available_bytes / (1024 * 1024 * 1024)).toFixed(1) : 0;
   const diskUsedRatio = diskTotalGB > 0 ? Math.round(((diskTotalGB - diskAvailGB) / diskTotalGB) * 100) : 0;
   ```
   当 `facts.disk_total_bytes` 为 `undefined` 或 `0` 时，`diskTotalGB` 为 `0`，`diskUsedRatio` 兜底为 `0`，最终渲染出 `(0%)` 与 `0.0 / 0 GiB`。
4. 对照证据：同一文件的内存展示已具备防御逻辑 `hasMemoryFacts`（若缺失则显示 `-`），但磁盘展示缺少等价的 `hasDiskFacts` 校验。

### 2.3 根因因果链
- **采集端缺陷**：Windows Agent 尚未接入 Win32 磁盘容量采集 API，导致上报数据缺失 `disk_total_bytes` 与 `disk_available_bytes`。
- **数据传输与持久化**：Server 接收并持久化了缺少磁盘容量的 Facts。
- **前端错误计算**：UI 针对缺失数据回退为 `0%` 与 `0.0 / 0 GiB`，向用户误报了错误的有效指标。

## 3. 目标与非目标

### 3.1 目标
1. Windows Agent 接入 Windows 原生 API `GetDiskFreeSpaceExW`，准确采集受管根路径（或默认 `C:\`）的物理总容量与可用容量。
2. 采集失败、总量为 0 或可用量大于总量时安全省略字段，不伪造脏数据，不影响 CPU、内存与 Uptime 上报。
3. 前端 UI 对齐内存指标的防御机制，在磁盘字段缺失或非法时渲染未知态（`-`），不再渲染 `(0%)` 或 `0.0 / 0 GiB`。
4. 保持 Linux 与 macOS 既有磁盘采集与展示行为不变。
5. 补充平台单元测试、负例边界测试、浏览器端到端页面渲染测试，并通过全量 `-race` 回归。

### 3.2 非目标
- 不扫描主机上的全部物理磁盘或多分区挂载点列表，维持单挂载卷最小必要契约（`C:\` 或 Agent 可执行文件所在驱动器根路径）。
- 不修改服务端 `internal/health/evaluator.go` 的既有 `disk_space_low` 规则算法。
- 不引入第三方外部依赖或执行外部 CLI（如 `wmic` 或 `powershell`）。

## 4. 公开契约与数据语义

### 4.1 数据模型
既有 `runtime` 对象模型保持不变：
```json
{
  "runtime": {
    "observed_at": "2026-08-27T02:00:00Z",
    "uptime_seconds": 86400,
    "memory_total_bytes": 17179869184,
    "memory_available_bytes": 4294967296,
    "disk_total_bytes": 512110190592,
    "disk_available_bytes": 256055095296,
    "disk_mount": "C:\\"
  }
}
```

### 4.2 字段语义与值域
| 字段 | 语义 | 有效范围 | 失败行为 |
|---|---|---|---|
| `disk_mount` | 磁盘挂载点/驱动器根路径 | 非空字符串，Windows 默认为 `C:\` 或驱动器根路径 | 默认回退为 `C:\` |
| `disk_total_bytes` | 磁盘物理总字节数 | `> 0` | 获取失败时与 available 一并省略 |
| `disk_available_bytes` | 当前进程/调用方可用的空闲物理字节数 | `0 <= available <= total` | 获取失败、大于 total 时与 total 一并省略 |

## 5. 平台采集方案 (Windows)

### 5.1 API 选择与 ABI 契约
使用 Windows 系统自带的 `kernel32.dll` 中的 `GetDiskFreeSpaceExW`：
```c
BOOL WINAPI GetDiskFreeSpaceExW(
  _In_opt_  LPCWSTR         lpDirectoryName,
  _Out_opt_ PULARGE_INTEGER lpFreeBytesAvailableToCaller,
  _Out_opt_ PULARGE_INTEGER lpTotalNumberOfBytes,
  _Out_opt_ PULARGE_INTEGER lpTotalNumberOfFreeBytes
);
```

- 通过项目已有依赖 `golang.org/x/sys/windows` 提供的 `windows.NewLazySystemDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")` 进行窄接口调用。
- 传入 UTF-16 编码的驱动器根目录指针（如 `C:\`）。
- 接收 `lpTotalNumberOfBytes` 映射为 `facts.DiskTotalBytes`。
- 接收 `lpFreeBytesAvailableToCaller` 映射为 `facts.DiskAvailableBytes`（与 Unix `stat.Bavail` 对齐，反映调用方可实际分配的空闲空间）。
- 返回值：非 0 表示成功，0 表示失败。

### 5.2 路径确定与规范化
- 优先通过 `os.Executable()` 解析 Agent 可执行文件所在路径，使用 `filepath.VolumeName(path)` 提取驱动器盘符（如 `C:` 或 `D:`）；若有效则补齐反斜杠（如 `C:\`）。
- 若无法获取或非盘符路径，默认使用 `C:\`。
- 确保传入 API 的目录字符串以 `\` 结尾，符合 Windows API 规范。

### 5.3 采集函数与安全降级
```go
func readWindowsDiskUsage(directory string) (uint64, uint64, bool) {
    // 窄接口调用 GetDiskFreeSpaceExW
    // 校验：r1 != 0 && total > 0 && available <= total
}
```
- 若 API 返回失败、`total == 0` 或 `available > total`，省略 `DiskTotalBytes` 与 `DiskAvailableBytes`。
- 磁盘采集失败不影响 uptime、CPU、内存等其他 Facts 的正常上报。

## 6. UI 容器与布局层级及对齐契约

### 6.1 DOM 容器与布局层级
- 容器：`#deviceHealthModal` 内的 `#healthModalFactsGrid`（CSS grid 容器，桌面端 2 列，移动端 1 列）。
- 栅格项（4 个子项）：
  1. `children[0]`: CPU / 负载
  2. `children[1]`: 运行时间 (Uptime)
  3. `children[2]`: 内存使用率
  4. `children[3]`: 磁盘使用率

### 6.2 渲染与对齐契约
在 `internal/ui/static/js/devices/actions.js` 中：
```javascript
const hasDiskFacts = Number.isFinite(facts.disk_total_bytes)
  && Number.isFinite(facts.disk_available_bytes)
  && facts.disk_total_bytes > 0
  && facts.disk_available_bytes >= 0
  && facts.disk_available_bytes <= facts.disk_total_bytes;

const diskTotalGB = hasDiskFacts ? (facts.disk_total_bytes / (1024 * 1024 * 1024)).toFixed(1) : 0;
const diskAvailGB = hasDiskFacts ? (facts.disk_available_bytes / (1024 * 1024 * 1024)).toFixed(1) : 0;
const diskUsedRatio = hasDiskFacts && Number(diskTotalGB) > 0 ? Math.round(((facts.disk_total_bytes - facts.disk_available_bytes) / facts.disk_total_bytes) * 100) : 0;
const diskMountLabel = facts.disk_mount || '/';
const diskLabel = hasDiskFacts ? `磁盘使用率 [${escapeHTML(diskMountLabel)}] (${diskUsedRatio}%)` : `磁盘使用率 [${escapeHTML(diskMountLabel)}]`;
const diskValue = hasDiskFacts ? `${(diskTotalGB - diskAvailGB).toFixed(1)} / ${diskTotalGB} GiB` : '-';
```

- **数据有效时**：
  - 标签（上方小字）：`磁盘使用率 [C:\] (45%)`
  - 数值（下方粗体）：`225.0 / 500.0 GiB`
- **数据缺失或非法时**：
  - 标签（上方小字）：`磁盘使用率 [C:\]`
  - 数值（下方粗体）：`-`

## 7. 兼容性与版本策略

- **基线版本**：`v0.6.4`。
- **候选版本**：`v0.6.5`（修改了 Agent 采集可执行逻辑，必须升级客户端版本号）。
- **向后兼容**：`v0.6.4` Server 已经完全支持 `disk_total_bytes` 与 `disk_available_bytes` 字段，新 Agent 上报无需更改服务端 API 路由或 JSON 契约。
- **向前兼容**：`v0.6.4` 旧 Agent 仍可正常向 `v0.6.5` Server 上报，缺失磁盘数据时 UI 正确渲染为 `-`。

### 跨版本兼容矩阵
| 方向与场景 | 基线 v0.6.4 行为 | 候选 v0.6.5 预期 | 判定 |
|---|---|---|---|
| v0.6.4 Windows Agent → v0.6.5 Server | 上报 C:\，无磁盘容量 | Server 接收 Facts，UI 磁盘项显示 `-`，不报错 | 向后兼容 |
| v0.6.5 Windows Agent → v0.6.4 Server | 上报 C:\ 及真实磁盘容量 | v0.6.4 Server 接收并持久化，首次 Facts HTTP 200 | 向前兼容 |
| v0.6.5 Windows Agent → v0.6.5 Server | 上报 C:\ 及真实磁盘容量 | Server 接收并持久化，UI 显示真实百分比与 GiB | 完全兼容 |

## 8. 测试策略

测试必须先于实现，并从设计契约推导：

### 8.1 平台单元测试与负例
在 `cmd/homeagent-agent/runtime_windows_test.go` 中添加：
1. **正常采集测试**：注入已知 total 与 available 字节数，断言 `facts.DiskTotalBytes`、`facts.DiskAvailableBytes`、`facts.DiskMount` 正确写入。
2. **负例 1（API 失败）**：API 返回 false/0 时，断言 `facts.DiskTotalBytes` 和 `facts.DiskAvailableBytes` 为 0（JSON 序列化后省略），其他指标不受影响。
3. **负例 2（可用大于总量）**：`available > total` 时，断言两个字段均被省略。
4. **负例 3（总容量为 0）**：`total == 0` 时，断言两个字段均被省略。

### 8.2 前端测试与真实浏览器验收
1. `internal/ui/testdata/mobile-responsive.test.mjs`：
   - 断言弹窗内无 `0.0 / 0 GiB`；
   - 断言缺失磁盘指标时显示 `磁盘使用率` 与 `>-<`。
2. `internal/ui/testdata/browser-layout.test.mjs`（通过 `internal/ui/embed_test.go` 启动真实无头浏览器）：
   - 验证第一个设备（包含有效磁盘数据）正确渲染百分比与 GiB 容量；
   - 验证第二个设备（缺少磁盘数据）正确渲染 `-`；
   - 验证 Console 零未捕获异常。

### 8.3 全量门禁
- `go test -race ./...` 全量通过；
- `./scripts/check-diff-coverage.sh HEAD 60` 覆盖率不低于 60%。

## 9. 预计变更范围 (`allowed_paths`)

- `changes/fix-windows-disk-facts.yaml`
- `docs/fixes/Windows磁盘指标修复设计.md`
- `cmd/homeagent-agent/runtime_windows.go`
- `cmd/homeagent-agent/runtime_windows_test.go`
- `internal/ui/static/js/devices/actions.js`
- `internal/ui/testdata/mobile-responsive.test.mjs`
- `internal/ui/testdata/browser-layout.test.mjs`
- `internal/ui/embed_test.go`
- `internal/version/version.go`
- `internal/version/version_test.go`

## 10. 成功标准与验收证据

| 成功标准 | 验证手段 | 所需证据 |
|---|---|---|
| Windows Agent 正确填充磁盘容量 | 平台单元测试与模拟注入 | `DiskTotalBytes` 与 `DiskAvailableBytes` 精确断言 |
| 采集异常安全降级 | 负例单元测试 | 字段省略且其他 Facts 正常的断言 |
| UI 缺失态优雅展示 | 无头浏览器与 Layout 测试 | 缺失时显示 `-`，有效时显示百分比与 GiB |
| 全量回归与覆盖率达标 | 全量测试与覆盖率脚本 | `go test -race ./...` PASS, Diff Coverage >= 60% |

## 11. 外部假设审计

| 外部系统 | 关键假设 | 验证方式 | 实施要求 |
|---|---|---|---|
| Win32 API (`kernel32.dll`) | `GetDiskFreeSpaceExW` 在传入 UTF-16 驱动器路径时返回正确 64 位总字节数与可用字节数 | 原生 Win32 官方 API 契约，签名及类型与 Go ABI 严格匹配 | 单测覆盖窄接口替身与边界负例 |
| 驱动器路径格式 | 路径以 `\` 结尾（如 `C:\`）是 `GetDiskFreeSpaceExW` 的标准格式 | 代码中确保盘符后拼接反斜杠 | 包含盘符路径规范化断言 |

## 12. 设计审查清单

- [x] 功能边界与非目标已明确。
- [x] Win32 原生 API（`GetDiskFreeSpaceExW`）选择与 64 位整数语义明确。
- [x] 字段语义、单位、有效值域与异常降级行为明确。
- [x] UI 容器与布局层级及对齐契约明确（`#healthModalFactsGrid` 4 项结构对齐）。
- [x] 版本升级策略与双向兼容矩阵明确（`v0.6.4` -> `v0.6.5`）。
- [x] 测试策略涵盖正常流、三类负例及真实浏览器端渲染。
- [x] `allowed_paths` 范围完整精确。

## 13. 设计审查记录

### 13.1 第一轮审查（2026-08-27）
- **审查结论**：设计审查通过。
- **确认内容**：
  1. 根因链完整闭环，涵盖 Windows Agent 采集端缺失与前端 UI 缺少有效性保护两处缺陷；
  2. 明确了 `GetDiskFreeSpaceExW` 的 API 签名、UTF-16 转换及 64 位无符号整数提取机制；
  3. UI 布局与文本契约与既有内存防护完全对称；
  4. 兼容矩阵、版本升级（`v0.6.4` -> `v0.6.5`）、单测负例与无头浏览器验证均已完备定义。
- **实施授权**：已获得用户明确确认并授权实施。

## 14. 实施记录（2026-08-27）
- **Windows Agent 采集**：在 `cmd/homeagent-agent/runtime_windows.go` 中通过 `kernel32.dll!GetDiskFreeSpaceExW` 实现了磁盘总容量与可用容量的采集，支持可执行文件盘符自适应提取与 `C:\` 默认回退。
- **UI 缺失防御**：在 `internal/ui/static/js/devices/actions.js` 中补充了 `hasDiskFacts` 校验逻辑，在数据缺失或非法时展示未知态（`-`），在数据有效时渲染真实百分比与 GiB 读数。
- **版本升级**：在 `internal/version/version.go` 中将客户端版本号从 `v0.6.4` 升级为 `v0.6.5`。
- **测试覆盖**：在 `cmd/homeagent-agent/runtime_windows_test.go` 中实现了平台正常注入与三类负例（API 失败、总容量为 0、可用大于总量）单测；在 `internal/ui/testdata/` 中补充了移动端断言与真实无头浏览器 CDP 渲染断言；新增 `internal/version/version_test.go`。

## 15. 验收记录（2026-08-27）
- **测试先行红灯验证**：修改前端测试后先触发了红灯报错（捕获到旧逻辑渲染出的 `(0%)` 与 `0.0 / 0 GiB`），证实测试有效拦截了原始缺陷。
- **无头浏览器端到端验收**：通过真实 `httptest` HTTP 服务与 Headless Chromium 运行 `browser-layout.test.mjs`，验证有效设备渲染 `50.0 / 100.0 GiB (50%)`，缺失设备渲染 `-`，且全局 Console 零异常。
- **跨平台编译验收**：Windows AMD64、Windows ARM64、Linux AMD64、Linux ARM64 交叉编译全部一次性通过。
- **全量回归验收**：全仓库执行 `go test -race ./...` 全部 PASS。
- **Diff 覆盖率验收**：执行 `./scripts/check-diff-coverage.sh HEAD 60`，Diff Coverage 为 100.0%（1/1 statements），达到并超过 60% 门禁要求。
