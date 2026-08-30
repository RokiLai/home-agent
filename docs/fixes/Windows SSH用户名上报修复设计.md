# Windows SSH 用户名上报修复设计

## 1. 文档状态

| 阶段 | 状态 | 说明 |
|---|---|---|
| 设计 | 已确认 | 设计审查通过，功能边界、持久化模型、服务端防线与测试契约均已确认 |
| 实施 | 已完成 | 完成配置模型扩展、认领/安装参数透传、采集拦截、Server 保护及版本升级 |
| 验收 | 自动化通过 | 全量 -race 回归、单元测试、Diff Coverage (74.6%) 及质量门禁已全部通过，等待 Windows 实机验收 |

## 2. 问题与直接证据

Windows 设备页面当前显示 SSH 用户名 `ROKILAI$`，但实际可登录账户为 `Administrator`。

已确认的数据链路与代码证据如下：

1. Windows Agent 以后台服务运行（由 SCM 以 `LocalSystem` 身份拉起）。
2. 周期 Facts 上报定期调用 `collectDeviceFacts()`，当前无参硬编码调用 `localDevice("", 22)`。
3. `localDevice` 在参数为空时调用 `user.Current()`；Windows 服务身份由此解析为机器账户 `ROKILAI$`（`NT AUTHORITY\SYSTEM` 或域/工作组机器标识）。
4. Agent 通过 `PUT /api/v1/devices/{id}/facts` 上报包含 `ssh_user: "ROKILAI$"` 的 Payload。
5. Server 端的 `putDeviceFacts` 无条件直接使用上报值覆盖设备记录中的 `ssh_user`（`d.SSHUser = req.SSHUser`）。
6. Web 页面 `render.js` 读取该字段生成 `ssh <ssh_user>@<address>`，最终展示错误命令。

根因是 Agent 将后台服务进程身份错误地等同于远程 SSH 登录身份，且服务端缺乏对机器账户的防御性过滤。该问题不影响 Wake-on-LAN（WOL 使用 MAC 地址，与 SSH 用户名无关）。

## 3. 修复目标与非目标

### 3.1 目标

- 将 SSH 用户名与端口作为显式、可持久化的设备配置，不再从 Windows 服务运行身份动态推断。
- Windows 安装或交互式认领时能够记录并持久化实际 SSH 登录用户（例如 `Administrator`）。
- 周期 Facts 上报复用持久化值；若未配置且处于服务环境，严禁上报以 `$` 结尾的机器账户。
- 服务端增加防护，防止空值覆盖已有合法值，并拒绝 Windows 机器账户污染。
- 保持现有 `ssh_user` JSON 字段及 SSH 命令格式兼容。

### 3.2 非目标

- 不改变 WOL、SSH 密钥分发或 Windows OpenSSH Server 的行为。
- 不自动创建、重命名或启用 Windows 本地用户。
- 不根据主机名猜测管理员账户名称。
- 不在本任务中调整非 Windows 平台的默认 SSH 用户策略。

## 4. 架构与数据流契约

### 4.1 Agent 设备配置持久化模型 (DeviceConfigFile)

在 `cmd/homeagent-agent/main.go` 中的 `DeviceConfigFile` 结构体扩充 SSH 配置字段：

```go
type DeviceConfigFile struct {
	ServerURL   string   `json:"server_url"`
	ServerURLs  []string `json:"server_urls,omitempty"`
	DeviceID    string   `json:"device_id"`
	DeviceToken string   `json:"device_token"`
	SSHUser     string   `json:"ssh_user,omitempty"`
	SSHPort     int      `json:"ssh_port,omitempty"`
}
```

- 序列化与反序列化均使用 `omitempty`，确保对存量缺少该字段的 `device.json` 完全向后兼容。

### 4.2 认领与安装链路 (Claim & Install)

1. **`claim` 命令**：
   - 接收 `--ssh-user`（默认空）与 `--ssh-port`（默认 22）。
   - 若未提供 `--ssh-user`，交互执行时由 `localDevice` 探测当前运行用户（如交互管理员 `Administrator`）。
   - 认领成功后，将确定的 `SSHUser` 与 `SSHPort` 写入 `device.json` 持久化保存。
2. **`scripts/install.ps1` 脚本**：
   - 支持读取环境变量 `$env:HOMEAGENT_SSH_USER` 与 `$env:HOMEAGENT_SSH_PORT`。
   - 若环境变量存在，在调用 `claim` 时显式追加 `--ssh-user` 和 `--ssh-port` 参数。
   - 若未提供环境变量，交互式执行时由 `claim` 自动捕获当前执行管理员的身份并落盘。

### 4.3 周期 Facts 上报与数据采集链路 (Facts Reporting)

1. **数据传递**：
   - `runDaemonWithContext` 读取 `device.json` 获取 `devCfg.SSHUser` 和 `devCfg.SSHPort`，并传递给 `startDeviceFactsReporter` 与 `collectDeviceFacts`。
2. **采集回退与服务账户拦截规则**：
   - 若配置了显式 `SSHUser`，直接使用该值。
   - 若未配置 `SSHUser`（如老版本升级存量）：
     - 尝试探测当前用户；
     - 若探测到的用户名以 `$` 结尾（Windows 机器账户）或属于已知系统服务账户（`SYSTEM`、`LOCAL SYSTEM`、`NETWORK SERVICE`），则将其置为空字符串 `""`，严禁作为 SSH 用户上报。
3. **Payload 结构**：
   - 保持既有 `ssh_user` 字段不变；当置空时上报 `""`，由服务端保护既有合法数据。

### 4.4 服务端事实更新与防污染保护契约 (Server Facts Protection)

在服务端 `internal/api/api.go` 的 `putDeviceFacts` 接口处理中增加数据校验与保护：

1. **空值保护**：若 `req.SSHUser == ""`，保持数据库与内存中既有 `d.SSHUser` 不变，禁止空值抹除。
2. **机器账户过滤**：若 `strings.HasSuffix(req.SSHUser, "$")`，判定为非法机器账户，忽略该字段的更新并记录 Warning 日志，保护已有数据不被未升级的旧版 Agent 污染。
3. **合法更新**：仅当 `req.SSHUser != ""` 且非机器账户时，更新 `d.SSHUser = req.SSHUser`。
4. **端口更新**：当 `req.SSHPort > 0` 时更新 `d.SSHPort`，否则保留既有端口。

## 5. 状态流转与失败路径

| 场景 | 输入/前置状态 | 预期处理与状态流转 | 最终结果 |
|---|---|---|---|
| Windows 全新安装与认领 | 交互式执行 `install.ps1`，当前用户为 `Administrator` | `claim` 捕获 `Administrator` 并写入 `device.json` 的 `ssh_user` | `device.json` 包含 `ssh_user: "Administrator"`，服务端记录为 `Administrator` |
| Windows 安装指定用户 | 指定 `$env:HOMEAGENT_SSH_USER = "customuser"` | `install.ps1` 传递 `--ssh-user customuser` 给 `claim` 并持久化 | `device.json` 与服务端记录均为 `customuser` |
| Windows SCM 服务周期上报 | 服务以 `LocalSystem` 身份运行，`device.json` 中配置了 `Administrator` | `collectDeviceFacts` 读取配置中的 `Administrator` 进行上报 | 上报并在服务端维持 `Administrator`，不受服务身份影响 |
| 存量旧版配置升级（缺少 `ssh_user`） | `device.json` 无 `ssh_user` 字段，服务以 `LocalSystem` 运行 | `collectDeviceFacts` 检测到机器账户 `ROKILAI$`，自动拦截并上报空值 `""` | 服务端空值保护生效，保留服务端原有正确用户名，不被污染 |
| 旧版未修复 Agent 上报机器账户 | 旧版 Agent 发送 `ssh_user: "ROKILAI$"` | 服务端 `putDeviceFacts` 命中 `$` 过滤规则，拒绝覆盖 | 服务端保留原有值并记录日志，设备记录不受破坏 |
| 服务重启或系统重启 | 操作系统重启后服务重新拉起 | 服务启动时重新加载 `device.json` 维持持久化配置 | SSH 用户名状态持续稳定一致 |

## 6. 兼容性与存量修复

### 6.1 配置文件向后兼容

- `DeviceConfigFile` 扩充字段采用可选（`omitempty`）定义，读取旧版本 `device.json` 时缺失字段默认解析为空值或 0，不破坏现有序列化兼容性。

### 6.2 存量受污染设备纠正方案

对于已经在服务端被写入 `ROKILAI$` 的存量 Windows 设备，提供以下纠正途径：

1. **途径 A（自动修复）**：在目标 Windows 机器上的 `C:\ProgramData\HomeAgent\device.json` 中添加 `"ssh_user": "Administrator"` 并重启服务，Agent 下一次 Facts 上报即会自动将服务端数据纠正为 `Administrator`。
2. **途径 B（重新认领）**：在目标机器以管理员身份重新运行 `homeagent-agent claim --server <URL> --claim-token <TOKEN>`，将自动纠正本地配置与服务端状态。

## 7. 测试与验收策略

实施前先编写失败测试，断言必须源自上述契约（包含正例与负例断言）：

### 7.1 单元测试与负例契约断言

1. **配置文件读写测试**：
   - 验证 `saveDeviceConfig` 与 `loadDeviceConfig` 对包含 `ssh_user` 和 `ssh_port` 的完整数据能准确持久化与回读。
   - 验证读取不包含 `ssh_user` / `ssh_port` 的旧版 JSON 配置时正常解析且不报错（向后兼容）。
2. **Agent Facts 采集与拦截测试**：
   - 正例：显式配置 `ssh_user: "Administrator"` 时，`collectDeviceFacts` 返回 `SSHUser == "Administrator"`。
   - 负例（模拟服务身份）：当配置为空且系统身份为 `ROKILAI$` 时，断言 `collectDeviceFacts` 返回的 `SSHUser == ""`，严禁包含 `$` 结尾的机器账户。
3. **Server `putDeviceFacts` 契约断言**：
   - 正例：上报 `ssh_user: "Administrator"`，断言设备 `d.SSHUser` 更新为 `Administrator`。
   - 负例（防污染）：上报 `ssh_user: "ROKILAI$"`，断言设备既有 `d.SSHUser` 保持原值不变，且记录 Warning 日志。
   - 负例（防清空）：上报 `ssh_user: ""`，断言设备既有 `d.SSHUser` 保持原值不变，不被抹除。
4. **Claim 流程断言**：
   - 验证 `claim` 成功后持久化生成的 `device.json` 中包含准确的 `ssh_user`。

### 7.2 API 与页面链路断言

1. **前端渲染断言**：
   - 基于真实 API 数据模型验证 `internal/ui/static/js/devices/render.js`：
     - 当 `d.ssh_user = "Administrator"` 时，生成的 SSH 命令精确断言为 `ssh Administrator@<ip>`（默认端口 22）或 `ssh -p <port> Administrator@<ip>`。
     - 断言全执行路径无未捕获异常。

### 7.3 Windows 实机验收步骤

1. 在 Windows 实机以管理员身份运行 `install.ps1` 完成安装。
2. 确认 `C:\ProgramData\HomeAgent\device.json` 中正确持久化了 `Administrator`。
3. 检查 Windows 服务（`HomeAgent`）在 `LocalSystem` 下启动运行。
4. 观察 Web 页面设备卡片，确认 SSH 命令展示为 `ssh Administrator@<ip>` 而非 `ROKILAI$`。
5. 复制页面生成的 SSH 命令在控制台执行，验证能成功进行 SSH 登录。

### 7.4 全量门禁

- 执行变更范围检查与架构依赖检查。
- 执行全量 `-race` 回归测试套件。
- 执行 `./scripts/check-diff-coverage.sh HEAD 60`，确保 Diff Coverage ≥ 60%。
- 执行 `git diff --check` 确保零告警。

## 8. 预计实施范围

待用户明确授权进入实施阶段后，变更范围将包含：

- `changes/fix-windows-ssh-user.yaml`（允许修改路径扩展）
- `docs/fixes/Windows SSH用户名上报修复设计.md`（设计文档）
- `cmd/homeagent-agent/main.go` 与 `cmd/homeagent-agent/main_test.go`
- `internal/api/api.go` 与 `internal/api/api_test.go`
- `scripts/install.ps1`
- `internal/ui/static/js/devices/render_test.js`（或对应前端测试）
