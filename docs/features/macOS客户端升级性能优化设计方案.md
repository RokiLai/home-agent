# macOS 客户端升级性能优化设计方案

## 1. 文档状态

| 阶段 | 状态 | 说明 |
| --- | --- | --- |
| 设计 | 评审通过 | 权限模型、协议迁移、跨层状态映射和独立回滚执行者已定案；真实 Apple 与性能证据按阶段启用门禁采集 |
| 实施 | 阶段 A 完成 | 阶段 A 实施完成：分阶段耗时基线、v2 基础能力（归档、清单验证、事务日志、恢复器、Fenced 两阶段收敛、UpgradePlan 两跳编排）与协议铰接完成，v2 投递保持默认关闭 |
| 验收 | 阶段 A 通过 | 阶段 A 单元测试、Diff Coverage（74.8%）与全量 -race 回归已全部通过；阶段 B 真实外部环境验证待开始 |

### 1.1 设计审查结论

本轮技术评审已关闭权限模型、跨层状态映射、旧 Agent 协议迁移和独立回滚执行者四项设计阻断。用户确认后正式实施第 4.3 节的阶段 A；阶段 B 必须先产生第 12.2 节的真实启用证据。

## 2. 背景与问题定义

现有带内自升级在 macOS 与 OpenWrt 上共享下载、SHA256 校验、`info` 冒烟、二进制替换、ACK、进程重启和 Facts 收敛主链。macOS 还在替换后同步执行：

```text
codesign -s - --force --deep <HomeAgent.app>
```

当可执行文件位于 `.app` 内时，当前实现会对整个 App Bundle 做设备端 ad-hoc 重签名，该步骤处于替换成功 ACK 之前的同步关键路径。OpenWrt 没有此阶段，因此不能仅根据设备硬件性能解释两者差异。

当前 `applyDarwinCodesign` 的返回错误被替换函数忽略，因此该步骤虽然会增加 ACK 前耗时，却不是成功门禁：签名失败仍可能继续上报 `upgraded`。基线必须同时记录耗时、退出码和“签名失败但升级回执成功”的现状风险。

管理端当前每 2 秒查询命令和设备状态，只有在命令成功且新进程 Facts 报告目标版本后才显示收敛。因此用户看到的“升级时间”混合了下载、验证、安装、重启、重连、Facts 上报及最多 2 秒的前端观测延迟。

### 2.1 直接代码证据

- `internal/daemon/selfupgrade.go` 在下载后执行文件 `Sync`、SHA256、最长 5 秒 `info` 冒烟、原子替换与 macOS `codesign --deep`。
- `internal/daemon/client.go` 在替换完成后发送 `upgraded` ACK，500 毫秒后退出旧进程，由服务管理器重启。
- `internal/daemon/service.go` 为 macOS 使用 `launchd` 的 `KeepAlive` 和 `RunAtLoad`，为 OpenWrt 使用 `procd` 的 `respawn`。
- `internal/ui/static/js/devices/actions.js` 每 2 秒轮询命令和设备版本，总收敛超时为 15 分钟。
- `scripts/install.sh` 已警告：无可验证发布身份的 macOS App 在升级后可能需要重新授予本地网络权限。

### 2.2 已知与未知

| 类型 | 内容 |
| --- | --- |
| 已知 | macOS 存在 OpenWrt 没有的设备端整包重签名路径 |
| 已知 | 页面完成态以新进程版本 Facts 为证据，不以下发或文件替换为证据 |
| 未知 | 实际慢主要发生于下载、`fsync`、冒烟、签名、`launchd` 重启还是新进程重连 |
| 未知 | 当前生产 App 的 Developer ID、公证、stapling、指定要求和本地网络权限身份是否稳定 |
| 未知 | 同一卷上目录换名的原子语义、运行中 App 替换及失败回滚在目标 macOS 版本上的真实行为 |

本文档不将“设备端重签名是唯一瓶颈”宣称为已定位根因。实施前必须用分阶段时序证据闭合“耗时证据 → 执行路径 → 用户等待 → 优化结果”。

## 3. 目标与非目标

### 3.1 目标

1. 准确量化 macOS 升级各阶段耗时，使安装慢与运行收敛慢可以区分。
2. 在不降低制品完整性、签名、公证、本地网络权限和回滚保护的前提下，移除设备端整包 `codesign --deep` 的同步关键路径。
3. 使“指令已接受”、“制品已安装”和“新进程已收敛”成为独立、可追踪状态。
4. 从上一正式版本经真实用户入口升级候选版本，并验证失败时保留可启动旧版本。
5. 保持 Linux、OpenWrt 和 Windows 现有升级语义不变，除非后续设计审查明确将通用协议升级纳入范围。

### 3.2 非目标

- 不在本任务中优化 macOS 系统软件更新或其他应用的升级。
- 不通过跳过 SHA256、身份冒烟、签名验证或新进程 Facts 收敛来缩短表面时间。
- 不把“文件替换成功”重定义为“新版本正在运行”。
- 不在本阶段引入 Sparkle 或其他第三方更新框架；若真实协议验证证明自研整包替换无法满足安全与原子性，再单独提案并说明新外部依赖的必要性。
- 协议 v2 首期不支持 Agent 已回执 `accepted` 后的任务取消，与现有 Command 对 `accepted` 状态拒绝取消的契约保持一致；如需放开，必须另行补齐状态、时点、幂等和清理契约。

## 4. 功能边界与总体方案

### 4.1 目标架构

以下架构是“分阶段基线证明设备端重签名值得移出关键路径”后才采用的候选方案，不是已批准实施的架构。若基线显示主要耗时在下载、重启或重连，设计审查必须重新评估是否仍需整包改造，不得用未达成的 50% 性能目标倒推实施结论。

候选方案将 macOS 制品改为一个已在发布环境完成 Developer ID 签名、公证和 stapling 的完整 `HomeAgent.app` 归档。Agent 只验证发布身份并替换完整 App，不在用户设备上重新签名。

```text
发布流水线
  构建 HomeAgent.app
    → Developer ID 签名
    → codesign 严格验证
    → Apple 公证与 stapling
    → 固定格式归档
    → SHA256 与带签名的升级清单

macOS Agent
  接收指令
    → 下载到持久安装目录同卷临时路径
    → SHA256
    → 解压与路径安全检查
    → codesign/spctl/stapler 与静态 upgrade-info 验证
    → 将候选 App 写入新 release 目录
    → 首次迁移由恢复器切换 LaunchAgent；后续升级切换 current 软链接
    → 旧进程写 handoff_requested 后退出
    → Recovery 切换路径并由 launchd 启动新进程
    → Recovery 持久化 installed 阶段证据与待重放 ACK
    → 新进程重放 installed ACK
    → 新进程上报带 fence 的 Facts 并完成两阶段收敛提交
    → Server 标记 converged
    → 延迟清理旧 App 备份
```

### 4.2 为什么不只替换 App 内部二进制

替换已签名 Bundle 内部二进制会改变已签名内容。即使只对新二进制做 ad-hoc 签名，也不能在未验证前假定外层 Bundle 签名、指定要求、公证票据和系统权限身份仍有效。因此本方案选择“发布阶段签名整包，设备端验证整包，安装阶段替换整包”。

### 4.3 分阶段实施边界

| 阶段 | 可修改范围 | 启动条件 | 完成证据 |
| --- | --- | --- | --- |
| A：基线与协议铰接 | 现有单二进制升级增加分阶段耗时；实现但默认关闭整包验证、事务、恢复器、Upgrade Progress 和两跳编排的完整 v2 代码；只有包含全部代码的铰接 Agent 才上报 `control_protocols: [1,2]` | 用户对本设计确认并授权实施 | 旧协议回归、上一正式版本到铰接版本真实升级、v2 关闭负例、分阶段基线 |
| B：签名整包切换 | 不再新增升级引擎能力，只在真实门禁通过后开启签名 App 整包投递并完成 UI 发布验收 | 第 12.2 节所有真实启用门禁通过 | 上一正式版本经铰接版本自动到候选版本、Apple 身份、回滚与性能验收 |

管理员仍只发起一次升级操作。对仅支持 v1 的 macOS Agent，Server 将外层操作拆为“旧协议升级至铰接版本 → 铰接版本上报 v2 能力 → 投递最终候选版本”两个独立子 Command。外层操作按子 Command 独立终态聚合，铰接失败时不得创建第二跳。Linux、OpenWrt 和 Windows 继续使用 v1。

两跳编排使用独立持久化 `UpgradePlan`，不从最近 Command 反向猜测关联。`UpgradePlan` 至少包含 `plan_id`、`device_id`、`requested_by`、`target_version`、可空的 `target_manifest_digest`、可空的 `bridge_version`、`bridge_command_id`、`target_command_id`、`stage`、`created_at`、`updated_at` 和 `revision`。阶段全集为 `bridge_pending|bridge_running|capability_wait|target_pending|target_running|succeeded|failed|canceled`。同设备同时只允许一个非终态 Plan。

Plan 创建与恢复只使用冻结快照，按下表唯一选择路径；恢复时设备能力或安全模式变化不得改写既定路径，只能验证变化是否满足下一步前置条件。开关关闭时不创建任何以最终候选为目标的 macOS Plan；仅允许运维显式创建“部署铰接版本”的单跳 v1 Plan，该 Plan 的 `target_version=bridge_version`、`target_manifest_digest=null`，终态止于 bridge，不隐含未来 target。开关开启时必须先存在已验证并冻结的目标清单：v1-only 设备从 `bridge_pending` 开始；v2-capable/unlocked 与 v2-locked 设备都从 `target_pending` 开始，前者由 Agent 验证完整可接受清单后锁定。当前版本已满足第 5.1 节 `skipped` 条件的设备也创建 `target_pending`，由唯一 target Command 返回 `skipped`。

| 创建时能力 | 安全模式 | v2 开关 | 当前版本 | 初始阶段与冻结内容 |
| --- | --- | --- | --- | --- |
| v1-only | `unlocked` | 关闭 | 任意 | 仅显式铰接部署：`bridge_pending`，冻结 v1 URL/SHA；否则拒绝创建 |
| v1-only | `unlocked` | 开启 | 任意 | `bridge_pending`，同时冻结 bridge URL/SHA 与最终目标清单 |
| v2-capable | `unlocked` | 关闭 | 任意 | 拒绝最终候选 Plan；可创建显式铰接部署 |
| v2-capable | `unlocked` | 开启 | 未满足/满足 skipped | `target_pending`，冻结目标清单 |
| v2-capable | `v2_locked` | 关闭 | 任意 | `v2_upgrade_temporarily_unavailable`，不创建 Plan/Command |
| v2-capable | `v2_locked` | 开启 | 未满足/满足 skipped | `target_pending`，冻结目标清单 |

Server 仅在 bridge Command `succeeded`、设备 Facts 已上报 v2 能力、`upgrade_security_mode` 为 `unlocked|v2_locked` 且当前运行版本等于冻结的 `bridge_version` 时，将两跳 Plan CAS 转为 `target_pending`。所有 v2 路径使用 `plan_id + ":target"` 创建唯一 target Command；bridge 使用 `plan_id + ":bridge"`。Server 重启后按冻结路径和显式关联恢复，不重新路由或重复创建。

现有单设备升级端点以兼容方式增加 `plan_id`、`plan_stage`、`bridge_command_id` 和 `target_command_id`；对 v1 非 macOS 设备可以用单子 Command 的 Plan 表达。批量升级为每台设备创建独立 Plan，聚合结果只来自每个 Plan 的终态。

`POST /api/v1/devices/{id}/upgrade` 的 `Idempotency-Key` 在 UpgradePlan 层生效：同用户、同设备、同幂等键和同规范化请求返回原 Plan，同键不同请求返回 `409`。创建 Plan 时将 bridge/target 的版本、URL、SHA256、manifest digest、请求摘要和当时的能力/安全模式快照全部冻结，恢复时不重新从可变配置推导。Plan 先以 pending 状态持久化，再使用 `plan_id + ":bridge"` 或 `plan_id + ":target"` 创建幂等子 Command 并回填 ID；这是唯一创建协议，不保留“同库原子事务”替代语义。启动扫描可以根据固定幂等键补齐子 Command 或回填，不得出现无法归属的孤儿 Command。

增加 `GET /api/v1/upgrade-plans/{plan_id}` 和按设备过滤的 `GET /api/v1/upgrade-plans`；UI 以 Plan 为用户操作对象，以子 Command/Progress 展示阶段。Plan 只有在子 Command 尚未投递时可转为 `canceled`；已投递即使 `accepted` ACK 尚未到达，取消也返回 `409` 且无副作用，因为 v2 首期没有设备端撤销握手。

| 子 Command 结果 | Plan 转移 |
| --- | --- |
| bridge-only（`target_manifest_digest=null`）的 bridge `succeeded` 且 Facts 匹配 | `succeeded` |
| bridge `succeeded` 且能力/Facts 匹配 | `capability_wait -> target_pending` |
| bridge `failed/timed_out/canceled/interrupted/legacy_untracked` | `failed`，不创建 target |
| target `succeeded` 且 fenced convergence committed | `succeeded` |
| target `succeeded` 且 Progress 为 `skipped`，同一 ACK 回显的 confirmed manifest/sequence/Bundle 摘要与冻结目标及当前 Facts 匹配 | `succeeded` |
| target `canceled` | 仅未投递时与 Plan 同时转 `canceled`；已投递出现该状态视为协议错误并转 `failed` |
| target `failed` | `failed` |
| target `timed_out/interrupted` | `failed`，保留 `late_device_outcome` 但不追认 Plan 成功 |
| 子 Command 未投递时用户取消 | `canceled` |
| Plan 30 分钟到期且子 Command 未投递 | `failed`，不投递新 Command |
| Plan 30 分钟到期且子 Command 已投递 | Plan `failed`，Command/设备事务按自身 deadline 收尾，结果只记入 late outcome |

Server 启动时现有“全部非终态 Command 转 `interrupted`”逻辑必须改为：存在有效非终态 UpgradePlan 且 `protocol=2, kind=upgrade` 的 Command 不做通用中断，由 Plan Coordinator 根据 Convergence Record、Progress、Facts 和 deadline 恢复；其他 Command 保持现有中断语义。豁免判定只使用持久化 Plan ID 与 Command ID 显式关联，不按 kind 宽泛豁免。

安全开关从开转关时，以持久化 `dispatch_intent`（对应现有 `dispatching`）为不可逆边界，而不是以发布返回或 `dispatched` 为边界。intent 是 outbox record，必须原样保存最终 SSE data 的 UTF-8 字节（含完整严格清单、全部 signatures、Recovery/identity、URL query 和 Command envelope）、字节长度与 SHA256；禁止从发布配置重建或对 URL 脱敏后替代原文。日志展示另用脱敏投影。写 intent 并 `fsync` 后才可发布；关闭开关或重启都按原字节和同 Command ID 幂等重放并接收晚到结果，直到安全终态。无 intent 的 target 不得首次发布，Plan 转 `failed(v2_disabled_before_target_dispatch)`。outbox 至 Plan 和 Command 均终态且设备事务关闭后才可删除；同 ID 不同字节拒绝。

## 5. 公开契约

本节定义协议 v2 的稳定边界。真实制品才能确定的 Team ID、Bundle ID 和指定要求不是代码常量，它们由发布流水线从已签名制品导出并写入受签名清单；Agent 同时校验清单值与本地 Apple 工具实测值。

### 5.1 升级制品清单

macOS 升级不得继续只依赖一个可执行文件 URL。Server 向 Agent 解析后的升级负载至少包含：

```json
{
  "protocol": 2,
  "target_version": "vX.Y.Z",
  "minimum_source_version": "vX.W.Z",
  "release_sequence": 42,
  "issued_at": 1787932800,
  "expires_at": 1788019200,
  "artifact": {
    "format": "macos-app-archive-v2",
    "url": "https://example.invalid/downloads/HomeAgent-darwin-arm64.zip",
    "sha256": "<64 lowercase hex>",
    "size_bytes": 12345678,
    "running_bundle_digest": "<64 lowercase hex>"
  },
  "recovery": {
    "format": "macos-recovery-binary-v1",
    "url": "https://example.invalid/downloads/homeagent-recovery-darwin-arm64",
    "sha256": "<64 lowercase hex>",
    "size_bytes": 123456,
    "designated_requirement": "<verified recovery designated requirement>"
  },
  "identity": {
    "component": "homeagent-agent",
    "os": "darwin",
    "arch": "arm64",
    "bundle_id": "<verified bundle identifier>",
    "team_id": "<verified Team ID>",
    "designated_requirement": "<verified designated requirement>"
  },
  "force": false,
  "signatures": [
    {"key_id": "release-2026-a", "signature": "<base64 Ed25519 signature>"}
  ]
}
```

`bundle_id`、Team ID 和两个 `designated_requirement` 的具体值必须由真实签名制品导出，不得根据现有常量名或语义推测。清单使用 `protocol=2`，签名算法固定为 Ed25519，签名对象为下列字段按表中顺序进行的长度前缀编码：`protocol`、`target_version`、`minimum_source_version`、`release_sequence`、`issued_at`、`expires_at`、App `format`、App `url`、App `sha256`、App `size_bytes`、`running_bundle_digest`、Recovery `format`、Recovery `url`、Recovery `sha256`、Recovery `size_bytes`、Recovery `designated_requirement`、`component`、`os`、`arch`、`bundle_id`、`team_id`、App `designated_requirement`、`force`。整数使用 8 字节大端无符号编码，布尔值使用单字节 `0x00`/`0x01`，字符串使用“4 字节大端长度 + UTF-8 字节”，时间使用 Unix 秒整数；`signatures` 不进入签名输入。测试固件必须包含一组固定字节与签名向量。

清单 JSON 是严格 schema，Agent 和 Server 都必须拒绝未知字段、重复键、缺失字段、非规范数值、非 UTF-8 和 trailing token；“至少包含”仅表示上层 SSE 仍可有与升级清单分离的 Command 元数据，不允许清单本身扩展未签名行为字段。`manifest_digest` 固定为上述签名输入字节的 SHA256，不包含 JSON 排版或 `signatures`；Server、Agent、UpgradePlan 和事务记录均使用该值。每枚内置公钥带不可变的 `key_id` 与 `key_set_id`；同一集合内只按不同 `key_id` 计数，重复 `key_id` 或任一非规范 Base64 签名均拒绝整份清单。语法有效但不属于任何内置集合的 `key_id` 不计数且不导致整份拒绝。

普通 Agent 只激活一个 3-key 集合，清单必须满足该集合独立的 2-of-3。轮换采用两个版本：过渡 Agent 内置 `old`、`new` 两个具名集合并要求清单同时满足 `old` 2-of-3 与 `new` 2-of-3，计数绝不能合并成 2-of-6；其后的退役 Agent 仍由双集合门限签发，但安装后仅激活 `new` 并删除 `old`。集合成员、激活阶段和所需集合列表编译进 Agent，不从清单动态导入；重叠期只允许 `old -> old+new -> new`，不得跳步或回退。单枚密钥失陷时用同集合其余两枚完成上述轮换；两枚及以上同时失陷超出带内恢复边界，必须停止 v2 投递并人工重建信任。

Agent 拒绝过期清单、未到签发时间超过 5 分钟的清单、低于本地已确认 `release_sequence` 的清单，以及当前版本低于 `minimum_source_version` 的直接整包升级。版本必须是 `vMAJOR.MINOR.PATCH` 的规范 SemVer，不接受预发布或构建后缀，按三个无符号整数比较。验签后进入安装时将 `(release_sequence, manifest_digest)` 持久化为 provisional；回滚或替换前失败删除 provisional，只有第 5.3.1 节精确 commit 获得 committed 回执后才提交 confirmed。同序号不同清单摘要永久拒绝；`force` 只允许重装同版本、同序号、同清单摘要，不能绕过验签、时间、最低来源版本或防降级规则。`skipped` 仅在运行版本、confirmed 序号、清单摘要和 Bundle 摘要全部相等且 `force=false` 时成立。

防降级存储分为不可因回滚删除的 append-only `seen_releases` 和当前 `provisional`/`confirmed`。首次验证某序号时先将 `(release_sequence, manifest_digest)` 追加到 `seen_releases` 并 `fsync`；同序号已存在不同摘要时永久拒绝。安装前再写 provisional；回滚只清除 provisional，不删除 seen tombstone。提交 confirmed 与事务关闭的唯一条件，是同一 fence 的 `commit_ready` 获得带完整 committed confirmation 的 `200`，或 Command 为 `timed_out|interrupted` 时获得语义相同的 `202`；两者分别写 `closed`、`closed_late`。任何 Facts 响应、prepared 响应或其他阶段 `2xx` 均不得提交。

### 5.1.1 v1 信任引导与 v2 锁定

上一正式版本的 v1 升级只校验由现有 Server 同时提供的 URL 与 SHA256，没有内置独立发布信任根。因此 v1 远程铰接只适用于继续信任现有 Server 和下载源的兼容迁移，不能被宣称为抗控制面单点篡改的安全引导。需要建立该安全边界的用户必须通过真实本地安装入口安装 Developer ID 签名、公证和 stapled 的铰接 App，由 macOS Gatekeeper 与本地安装器校验 Apple 身份后再启用 v2。发布验收必须分别记录兼容引导和安全引导，不混合其威胁声明。

铰接 Agent 在验证第一份满足 2-of-3 的清单后，先持久化不可由远程命令清除的 `upgrade_security_mode=v2_locked` 和清单 seen tombstone，再执行整包安装。锁定后 macOS Agent 对任何 v1/legacy upgrade payload 返回 `upgrade_protocol_downgrade_rejected`，即使 v2 安装后续失败或回滚也不解锁。解锁只允许通过用户本地执行、经 Apple 验证的显式重安装/恢复流程，不存在 Server 或 SSE 开关。Server 在设备上报 `v2_locked` 后也必须拒绝为它创建 v1 macOS 升级 Command；Agent 本地拒绝是最终安全边界。

`running_bundle_digest` 固定为对“规范解包视图”计算 SHA256：发布端在签名和 stapling 完成后先按归档规则归一化，Agent 解包也得到同一视图。相对路径按规范 UTF-8 字节升序；目录类型 `0x01`、普通文件 `0x02`；每项编码“类型 + 4 字节大端路径长度 + 路径 + 4 字节大端规范 mode + 8 字节大端长度 + 文件内容”。目录 mode 固定 `0755`、长度 0；普通文件若源文件任一执行位存在则规范为 `0755`，否则为 `0644`。ZIP 外部属性只允许并保存这三个值，解包器忽略 umask 后显式设置；禁止其他权限位、链接和特殊文件。发布端对规范视图计算，Agent 在解包后和启动时重算，并执行完整 `codesign --verify --deep --strict`；任一不同均拒绝。

### 5.2 升级阶段与终态

Agent 与 Server 必须使用显式适配的内部状态，不得将 Apple 工具返回文本直接透传为产品状态。

| 阶段 | 含义 | 是否终态 | 服务端可信证据 |
| --- | --- | --- | --- |
| `accepted` | Agent 已验证指令可开始执行 | 否 | Agent ACK |
| `downloading` | 正在下载制品 | 否 | 带命令 ID 的阶段事件 |
| `verifying` | 正在验证哈希、归档、签名、公证和候选身份 | 否 | 带命令 ID 的阶段事件 |
| `installing` | 正在备份旧 App 并替换受管 App | 否 | 带命令 ID 的阶段事件 |
| `installed` | 候选 App 已落到受管路径，旧 App 备份仍在 | 否 | 持久化安装交易记录 + Agent ACK |
| `restarting` | 旧进程已请求退出，等待受管服务拉起 | 否 | 新进程重放 `installed` 后分配的显式 progress；断连本身不是阶段证据 |
| `converged` | 新进程认证成功并上报目标版本和本次交易标识 | 是，成功 | 新进程 Facts，不是旧进程 ACK |
| `skipped` | 已运行目标版本且未强制替换 | 是，成功 | Agent ACK + 已存在运行版本 Facts |
| `rolled_back` | 进入 `installing` 后已备份或更改受管 App，随后失败且已完整恢复旧 App；包括替换前崩溃恢复、替换失败恢复和新 App 未收敛恢复 | 是，失败 | 回滚记录 + 旧 App 路径/身份证据；已启动路径还需旧版本 Facts |
| `failed` | 在备份或修改受管 App 前安全失败，或进入 `installing` 后恢复也失败 | 是，失败 | 结构化错误码与阶段 |

`installed` 不得投影为总体升级成功。批量结果必须将 `converged`、`skipped`、`rolled_back` 和 `failed` 独立计数；只有 `converged` 计入“已升级”，`skipped` 计入“无需变更”，不得混入已升级数。

### 5.3 阶段事件、ACK 与 Facts 关联

协议 v2 沿用已验证的 `POST /api/v1/devices/{id}/ack`，在 `AckRequest` 增加可选 `sequence`、`phase`、`occurred_at`、`phase_result` 和 `upgrade_transaction_id`。`status=progress` 只允许 `module=upgrade`、`protocol=2` 使用；Command 未终止时它更新 Upgrade Progress 投影且不调用 `Command.Finish`。Command 已为 `timed_out`、`canceled` 或 `interrupted` 时，Server 以 `202` 持久化晚到阶段和摘要供审计，不改变 Command 终态或已投影的用户结果；其他终态收到不同摘要返回 `409`。现有 v1 字段和语义保持不变。

- 旧 Agent、新 Agent 和独立恢复者共享持久化事务中的下一 `sequence`；每次分配和阶段持久化必须串行化。每个阶段事件至少包含 `command_id`、单调递增的 `sequence`、`phase`、发生时间和脱敏结果；Server 按 `(command_id, sequence)` 幂等去重并拒绝回退。
- Recovery 完成切换与新 job bootstrap 后，必须在同一 journal 提交中写入 `installed` 与待重放 ACK；它不连网。新 Agent 启动后先重放 `installed`，再分配 `restarting` 并上报 Facts，不重复安装。旧 Agent 在 `handoff_requested` 后不得宣称 `installed`。Server 必须允许幂等补入 `installed -> restarting -> converged`，但不得跳过序号校验。
- 新进程 Facts 增加可选 `upgrade_transaction_id`、`upgrade_fence_revision`、`upgrade_fence_token`、`upgrade_release_sequence`、`confirmed_manifest_digest`、`running_bundle_digest` 和 `upgrade_security_mode`。`skipped` ACK 也必须回显 confirmed manifest/sequence/Bundle 摘要，Server 与同次认证 Facts 交叉验证。无活动 v2 事务时，不论是否 locked，Agent 都可在旧 Server 对新字段返回明确 unknown-field `400` 后仅移除这些扩展字段重试常规 Facts，以维持设备在线；该路径不得发送 upgrade Progress、不得改变任何升级/安全状态，legacy `2xx` 永远不是升级确认证据。存在活动 v2 事务时禁止降级，阶段 B 启用与 Server 发布必须绑定为不可拆分门禁：部署前证明所有可回滚 Server 版本均理解 v2 Facts/ACK，发布后禁止回滚到更旧版本。
- UI 从现有命令查询获取 `progress`，以现有 `.device-card[data-id]` 为容器，在现有 `.btn-menu-upgrade[data-id]` 操作入口所属卡片内渲染 `.upgrade-progress`。`state.upgradingDevices` 从单纯 `Set` 迁移为以设备 ID 为键的阶段快照 Map；迁移期保留等价 `has(deviceID)` 判定。这些选择器直接来自现有设备卡片和升级按钮实现。

v2 ACK/Facts wire JSON 使用严格 schema：拒绝未知/重复字段、非 UTF-8、trailing token 和数值字符串。`sequence, occurred_at, upgrade_fence_revision, upgrade_release_sequence` 为 JSON 非负整数且不超过 `2^53-1`，`occurred_at` 是 Unix 毫秒；ID、phase、status、version、security mode 为 UTF-8 字符串；所有 SHA256/fence digest/manifest/Bundle digest 为 64 位小写 hex；原始 `upgrade_fence_token` 为无 padding 的 Base64URL，解码后恰 32 字节。`phase_result` 必须是下列唯一子 schema之一：`skipped={confirmed_manifest_digest:hex64, confirmed_release_sequence:uint, running_bundle_digest:hex64}`；`commit_ready={fence_digest:hex64, facts_digest:hex64, server_nonce:base64url32}`；`rolled_back|failed={error_code:string, rollback_complete:bool, previous_version:string}`；其他 progress 为 `{detail_code:string}`，不得携带自由 map。Facts 的 v2 字段类型遵循同一规则，`upgrade_confirmation` 严格为 `{state:"prepared"|"committed", command_id:string, fence_revision:uint, fence_digest:hex64, facts_digest:hex64, server_nonce:base64url32}`。协议测试必须提供每类完整请求/响应原始 JSON 固件及 unknown field、错误 hex/Base64、越界整数、错误子 schema 反例。

#### 5.3.1 fenced 收敛提交

1. 恢复器在 `local_ready` 后持锁生成稳定的 `fence_revision`（等于本事务首次写 fence 时的 `journal_revision`）和 256-bit 随机 `upgrade_fence_token`；后续记录继续递增 `journal_revision`，但整个收敛提交期间 `fence_revision` 不变。
2. 新 Agent 发送带上述字段的 Facts。`facts_digest` 输入依次为：`device_id`、`command_id`、`transaction_id`、`target_version`、`upgrade_security_mode` 使用“4 字节大端长度 + UTF-8”；`fence_revision`、`release_sequence` 使用 8 字节大端无符号整数；`SHA256(fence_token)`、`manifest_digest`、`running_bundle_digest` 均由 64 位小写十六进制解码为恰好 32 个原始字节后直接拼接。对整体取 SHA256；测试必须固定完整输入字节和输出摘要向量。不含易变 Facts。Server 以 `(command_id, fence_revision, fence_digest)` 为唯一键：首次匹配时持久化唯一 Convergence Record 与随机 `server_nonce`，相同 facts digest 重放返回原 prepared 对象，不同 facts digest 返回 `409`。返回 `upgrade_confirmation={state:"prepared", command_id, fence_revision, fence_digest, facts_digest, server_nonce}`；无该对象的普通 `2xx` 不推进事务。
3. Agent 验证确认对象后持锁写入 `commit_ready`。`commit_ready` 是本地不可回滚栅栏：恢复器从此不得恢复旧 App；若新进程死亡，恢复器重启同一候选 App 使其继续提交，无法继续时保留事务并进入 `manual_recovery_required`，不产生与 Server 成功相矛盾的回滚。
4. Agent 使用下一 `sequence` 发送 `status=progress, phase=commit_ready`，并回显稳定的 fence revision、fence digest、facts digest 和 server nonce。Server 先持久化该阶段，再按第 5.3.3 节的可恢复顺序将 Progress 转为 `converged` 并 `Command.Finish(succeeded)`，返回 `upgrade_confirmation={state:"committed", ...}`。
5. Agent 持久化 committed 回执后提交 confirmed 防降级状态并关闭事务。若回执丢失，同一 commit 重放必须返回相同 committed 对象。

恢复器只允许在 `commit_ready` 之前回滚，且每次回滚前必须持锁重读最新 `journal_revision`、稳定 `fence_revision` 和 fence token。Agent 收到 prepared 后也必须先持锁确认状态仍为 `awaiting_control`、fence 未变且进程身份有效，才可写 `convergence_prepared`/`commit_ready`；若已写 process-lost/rollback，延迟响应只能丢弃。只有 `commit_ready` 的 committed `200`，或 Command 为 `timed_out|interrupted` 时针对同一 commit 返回完整 committed confirmation 的 `202`，才可提交 confirmed/`closed_late`；其他 progress `202` 只是传输审计确认。

#### 5.3.2 跨层状态映射

| Agent 阶段 | Command `Status` | Upgrade Progress `phase` | UI 结果 |
| --- | --- | --- | --- |
| `accepted` 至 `restarting` | `accepted` | 保存当前阶段、序号和耗时 | 显示当前阶段，不显示成功 |
| `converged` | `succeeded` | `converged` | 已升级 |
| `skipped` | `succeeded` | `skipped` | 无需变更 |
| `rolled_back` | `failed` | `rolled_back` | 升级失败，已恢复 |
| 替换前 `failed` | `failed` | `failed` | 升级失败，原 App 未变 |
| `failed` + `code=rollback_failed` | `failed` | `failed` | 需要人工恢复 |
| Server 超时 | `timed_out` | 保留超时时的最后阶段，后续事件另存审计 | 超时，不追认成功 |

Upgrade Progress 是 Command 模块公开端口后的升级专用投影，以 `command_id` 唯一索引持久化，不将 Apple 状态加入通用 Command 枚举。`GET /api/v1/commands` 和 `GET /api/v1/commands/{id}` 在 upgrade Command 中增加可选 `progress`；旧客户端忽略该字段。Server 对 `(command_id, sequence)` 执行幂等去重：同序号同摘要返回 `200`，同序号不同摘要或阶段回退返回 `409`。

`accepted`、中间阶段、回滚和失败均经 ACK v2 端点上报。中间阶段使用 `status=progress`；`rolled_back` 和替换前失败使用 `status=failed`，其 `phase` 与 `error_code` 决定 Progress 终态。`converged` 不接受旧进程的成功 ACK，只能由 fenced `commit_ready` 提交。

#### 5.3.3 Server 可恢复提交顺序

现有 Registry、Progress 和 Command Repository 无跨库原子事务，因此 Server 使用以 `(command_id, fence_revision, fence_digest)` 为唯一键、并固化 `facts_digest, server_nonce` 的 Convergence Record 作为 saga journal。Facts 阶段按“持久化设备 Facts → 持久化 `prepared` Convergence Record”；commit 阶段按“CAS Record 为 `commit_received` → Progress `converged` → Command `succeeded` → Record `committed`”。启动时扫描非终态 Record，仅补齐后续步骤；从不根据普通 Facts 或断连猜测提交。Command 已为 `timed_out/interrupted` 时不改变其终态，Record 仍可转为 `committed_late` 并生成带 committed 确认对象的 `202`。

### 5.4 候选身份冒烟

协议 v2 不在安装前执行候选代码。发布流水线调用构建产物的 `info`，将规范化后的 `agent_version`、`os`、`arch`、`component` 和可执行文件 SHA256 写入 `HomeAgent.app/Contents/Resources/upgrade-info.json`，然后再对整个 App 签名、公证和 stapling。该文件因此同时受 App 签名和升级清单的 `running_bundle_digest` 约束。

Agent 在 SHA256、清单签名、Apple 签名和公证验证全部通过后，只读取该 JSON 和 `Info.plist`，并与清单的版本、平台、架构、组件、Bundle ID、Team ID、指定要求及可执行文件摘要交叉比对。非 JSON、缺少字段或任一身份不匹配均在替换前安全失败。安装后的可执性由新进程真实启动与 Facts 收敛证明，启动失败由独立恢复器回滚。这保留了身份预检，同时消除“如何将未信任候选程序可验证地禁网和禁写”的未决执行机制。

### 5.5 结构化错误

错误至少包含 `command_id`、`phase`、`code` 和经脱敏的 `message`。错误码至少覆盖：

- `artifact_download_failed`
- `artifact_size_mismatch`
- `artifact_hash_mismatch`
- `archive_unsafe_path`
- `archive_invalid_layout`
- `signature_invalid`
- `notarization_invalid`
- `identity_mismatch`
- `candidate_smoke_failed`
- `install_backup_failed`
- `install_replace_failed`
- `restart_not_converged`
- `rollback_failed`

失败时必须记录失败阶段且不得输出 Token、签名私钥、完整环境变量或用户目录中的其他凭据。

## 6. 安装事务、持久化与回滚

### 6.1 全局唯一业务标识

每次升级使用 Server 签发的 `command_id` 作为全局唯一交易标识。Agent 的下载目录、事务记录、阶段事件、新进程确认和回滚结果均与该 ID 绑定，不得以目标版本号隔离并发任务。

### 6.2 持久化事务矩阵

事务使用 schema v1 的 append-only journal。文件头为 8 字节 ASCII `HAUPJNL1`；每条使用大端编码：`uint32 record_length | uint64 generation | uint64 journal_revision | 32-byte previous_record_hash | uint16 record_tag | uint32 payload_length | payload | uint32 CRC32C`。`record_length` 覆盖其后到 CRC 末尾，CRC32C 覆盖 `generation` 至 payload 末尾，previous hash 是前一完整记录从 `record_length` 到 CRC 的 SHA256；每 generation 首条连接 snapshot 的 tail hash，无 snapshot 时为零。`journal_revision` 从 1 严格加一；稳定的 `fence_revision` 是 payload 字段，不随记录递增。单条上限 1 MiB。

payload 使用确定 TLV：字段严格按递增 `uint16 field_id` 排列，每项为 `field_id | uint8 type | uint32 length | value`；类型仅为 `u64(1)`、`bytes(2)`、`utf8(3)`、`bool(4)`，禁止重复、未知或乱序字段。`record_tag` 固定为 `1 transaction_created, 2 phase, 3 side_effect_intent, 4 side_effect_done, 5 pending_event, 6 event_delivered, 7 fence_created, 8 convergence_prepared, 9 commit_ready, 10 committed, 11 rollback, 12 security_update, 13 terminal`。

field ID 与类型冻结为：`1 command_id:utf8, 2 transaction_id:utf8, 3 state:u64, 4 switch_mode:u64, 5 deadline:u64, 6 continuous_start:u64, 7 boot_session:utf8, 8 sequence:u64, 9 event_phase:u64, 10 event_digest:bytes32, 11 pid:u64, 12 pid_start:u64, 13 actual_path:utf8, 14 old_plist:bytes, 15 new_plist:bytes, 16 old_link:utf8, 17 new_link:utf8, 18 side_effect:u64, 19 release_sequence:u64, 20 manifest_digest:bytes32, 21 bundle_digest:bytes32, 22 recovery_digest:bytes32, 23 fence_revision:u64, 24 fence_token_ref:utf8, 25 fence_digest:bytes32, 26 server_nonce:bytes32, 27 facts_digest:bytes32, 28 seen_digest:bytes32, 29 provisional:bool, 30 confirmed:bool, 31 security_mode:u64, 32 error_code:utf8, 33 recovery_cdhash:bytes, 34 recovery_requirement:utf8, 35 recovery_team:utf8, 36 pending:bool, 37 occurred_at:u64, 38 phase_result:bytes, 39 target_version:utf8, 40 artifact_url:utf8, 41 artifact_sha256:bytes32, 42 artifact_size:u64, 43 candidate_path:utf8, 44 collection:bytes`。u64/布尔/UTF-8 规则同上，bytes32 必须恰为 32 字节。

枚举各自独立固定：`switch_mode: legacy_migration=1, release_switch=2`；`security_mode: unlocked=1, v2_locked=2`；`side_effect: stage_release=1, install_recovery=2, bootstrap_recovery=3, bootout_old_job=4, write_plist=5, switch_current=6, bootstrap_new_job=7, rollback_link=8, rollback_plist=9, bootstrap_old_job=10`；`event_phase: accepted=1, downloading=2, verifying=3, installing=4, installed=5, restarting=6, commit_ready=7, converged=8, skipped=9, rolled_back=10, failed=11`；`state: accepted=1, downloading=2, verifying=3, installing=4, installed_recovery=5, recovery_ready=6, handoff_requested=7, wrote_new_plist=8, switched_current=9, booted_new_job=10, restarted_job=11, installed=12, local_ready=13, awaiting_control=14, convergence_prepared=15, commit_ready=16, committed=17, committed_late=18, control_rejected=19, control_process_lost=20, rolled_back=21, rollback_failed=22, closed=23, closed_late=24, manual_recovery_required=25`。不允许按源码顺序推导，测试向量覆盖每个值。

必需字段集合为：所有 tag 含 `1..7`；tag 1 另含 `19..22,28,29,31,39..43`；tag 2 含 `8,9`；tag 3/4 含 `18` 及副作用对应的 `14..17,43`；tag 5/6 含 `8,9,10,36,37,38`，从而可重建完整 ACK；tag 7 含 `11..13,23..25`；tag 8 含 `23,25..27`；tag 9 含 `8,23,25..27`；tag 10 含 `8,19..21,23,25..27,30`；tag 11 含 `8,18,32` 及补偿字段；tag 12 含 `19,20,28..31`；tag 13 含 `8,9,32,29,30`。Recovery ready 作为 tag 2 phase 必须附 `11..13,20,22,33..35`。

snapshot 的 field 44 是集合容器，可出现三次且仅在 snapshot 中例外允许重复；其 value 为 `collection_kind:u8 | count:u32 | repeated(item_length:u32 | item_TLV)`，item_TLV 仍要求递增、无重复。`kind=1 seen_releases` 的 item 为 `release_sequence:u64 + manifest_digest:bytes32`，按 sequence 升序且不得重复；`kind=2 pending_events` 保存 tag 5 的全部字段，按 sequence 升序；`kind=3 completed_side_effects` 保存 tag 4 的全部字段及补偿值，按 journal revision 升序。snapshot 标量保存当前状态的所有非空字段；三个集合即使为空也必须出现。压缩后不得只保留集合摘要。

新记录必须一次完整写入、对 journal `fsync`，首次创建时还必须对父目录 `fsync`，然后才执行对应副作用。启动时顺序验证 generation、revision、哈希链和 CRC；只允许忽略文件末尾长度不足的单条未完成写入，中间损坏、链不连续、未知 schema 或最后完整记录无法确定补偿时，必须停止所有前向和自动删除操作，保留新旧制品并进入 `manual_recovery_required`。不根据当前文件布局猜测成功子步。

周期性压缩时将 generation 加一。snapshot 字节格式固定为 `8-byte HAUPSNP1 | uint64 generation | uint64 last_journal_revision | 32-byte source_tail_hash | uint32 payload_length | 完整状态TLV | uint32 CRC32C`，CRC 覆盖 generation 至 payload；完整状态使用上述字段表且必须包含所有安全状态、事务字段、待重放事件和副作用补偿值。先写同目录临时文件、`0600`、`fsync`、原子 rename、父目录 `fsync`，再创建同 generation 新 journal；其首条连接 source tail hash，完整写入并 `fsync` 后才删除旧 generation。只有 magic、长度、CRC、必需字段和首链全部有效才称 snapshot 完整；新 snapshot 存在但新 journal 缺失/空白时继续上一完整 generation，不按修改时间选择。

`upgrade_security_mode`、`seen_releases`、provisional、confirmed 和当前事务共享该用户唯一 security journal 与一把排他锁，不分布在无法原子协调的文件中。只有严格 schema、当前 Agent 要求的每个 key set 均独立满足 2-of-3、时间窗口、minimum source、seen conflict 和平台身份全部通过的“完整可接受清单”才可用一条 journal record 同时写入 `v2_locked + seen tuple + provisional + transaction_created`。该记录未完成则按尾部半写规则全部不生效；记录完成后即使尚未执行副作用，锁定和 seen 也按保守语义生效。provisional 提交 confirmed 与事务 `closed/closed_late` 同样使用单条 journal record，不存在一部分已关闭、另一部分未确认的状态。

| 当前阶段 | 触发 | 动作 | 持久化后的下一阶段 |
| --- | --- | --- | --- |
| 无 | 接收新命令 | 建立隔离目录和事务记录 | `accepted` |
| `accepted` | 开始下载 | 写入预期身份、URL 归一化结果、SHA256 和大小 | `downloading` |
| `downloading` | 下载与 `fsync` 完成 | 写入实际大小和哈希 | `verifying` |
| `verifying` | 所有验证通过 | 写入候选 App 路径和实测身份 | `installing` |
| `installing` | 将新 App 写入不可变 release 目录 | 对 release 与父目录 `fsync`，记录新旧路径、plist/软链接摘要与版本 | `installing` |
| `installing` | 恢复器按 switch mode 完成 plist 或 `current` 切换 | 恢复器原子写入 `installed` 与待重放事件并对受管父目录 `fsync` | `installed` |
| `installing` | 任一切换子步失败或崩溃 | 由独立恢复器按 journal 恢复旧 plist/job 或旧 `current`/job | 恢复完整为 `rolled_back`；否则为 `failed` 且 `code=rollback_failed` |
| `installed` | 新 job 已 bootstrap | 保留备份，由新进程重放 `installed` 后写 `restarting` | `restarting` |
| `restarting` | fenced commit 获得 committed 回执 | 原子提交 confirmed 并延迟清理备份 | `converged` |
| `restarting` | 超时或新进程反复崩溃 | 恢复旧 App，重启并等待旧版本 Facts | 回滚成功为 `rolled_back`；回滚失败为 `failed` 且 `code=rollback_failed` |

对任何跨异步 I/O 的返回，执行下一步前必须重新读取事务记录，检查 `command_id`、当前阶段、目标版本、受管路径和当前活动任务仍与前置条件一致。

事务还必须包含 `switch_mode` 和安装子步。`legacy_migration` 依次为 `staged_release -> installed_recovery -> recovery_ready -> handoff_requested -> wrote_new_plist -> booted_new_job -> installed -> local_ready -> awaiting_control -> convergence_prepared -> commit_ready -> committed -> closed`；`release_switch` 将 plist 子步替换为 `switched_current -> restarted_job`。每个子步先写意图、旧值、新值、摘要和补偿动作并 `fsync`，再执行副作用并写完成记录。恢复器完成切换和 bootstrap 后负责写 `installed + pending ACK`，因此旧 Agent 只需在 handoff 前退出，新 Agent 重放后再写 `restarting`。回滚按已完成子步逆序补偿；软链接与 plist 仅保证单文件原子，整体一致性由 journal 保证。

| 本地事务状态 | 唯一合法后继 | 崩溃恢复动作 |
| --- | --- | --- |
| `installed_recovery` | `recovery_ready` | 重新验证/加载恢复器，未 ready 不切换主 job |
| `recovery_ready` | `handoff_requested` | 旧 Agent 可重试写 handoff，恢复器不前进 |
| `handoff_requested` | `wrote_new_plist` 或 `switched_current` | 重启的旧 Agent 只验证 journal 后退出；恢复器 bootout 旧 job 并执行前向交接 |
| `wrote_new_plist` | `booted_new_job` | 恢复器验证 plist，未 bootstrap 则重试；失败补偿旧 plist/job |
| `switched_current` | `restarted_job` | 恢复器验证软链接，未重启则重试；失败恢复旧链接/job |
| `booted_new_job`/`restarted_job` | `local_ready` | 预算内等待，超时回滚 |
| `local_ready` | `awaiting_control` | 恢复器验证 PID/身份并分配 fence |
| `awaiting_control` | `control_rejected`/`control_process_lost`/`convergence_prepared` | 临时网络故障保持；进程丢失或确定拒绝则回滚 |
| `convergence_prepared` | `commit_ready` 或 `control_process_lost` | 每次调度复核进程；未写 commit fence 前可重放或回滚 |
| `commit_ready` | `committed`/`committed_late` | 禁止回滚，重启同一候选继续幂等 commit；无法继续转 `manual_recovery_required` |
| `control_rejected`/`control_process_lost` | `rolled_back`/`rollback_failed` | 按 switch mode 执行逆序补偿 |
| `committed`/`committed_late` | `closed`/`closed_late` | 提交 confirmed，延迟清理旧的用户受管 release |
| `rolled_back`/`rollback_failed`/`closed`/`closed_late`/`manual_recovery_required` | 无自动前向转移 | 只重放待确认结果或保留诊断 |

### 6.3 并发与重启恢复

- 每台设备同时只允许一个升级事务进入 `downloading` 及之后阶段；重复的同一 `command_id` 幂等返回已持久化状态。
- 不同 `command_id` 在旧任务未终止时必须显式拒绝，不得覆盖临时目录、备份或事务记录。
- 新进程启动时先读取未完成事务，再建立 SSE 并执行其他副作用；只有受管路径、实际运行路径、版本和候选身份一致时才可收敛。
- 崩溃发生在备份后但新 App 替换前时，恢复旧 App；发生在新 App 替换后时，先尝试由新进程确认，超时后由独立恢复入口回滚。
- 不得假定“新 Agent 能启动”来实现失败回滚。回滚唯一裁决者为第 6.4 节的独立恢复器；Server 超时只改变服务端 Command 状态，不直接操作设备文件。

### 6.4 独立恢复器契约

阶段 A 实现并在 v2 开关下默认禁用与主 App 分离的 `homeagent-recovery` 可执行文件和用户级 LaunchAgent `com.homeagent.recovery`。恢复器必须与 App 由同一 Team ID 签名、使用独立指定要求、开启 hardened runtime 并单独完成 Apple 公证，安装在稳定、用户可写但仅当前用户可修改的受管目录，不放入待替换 App 内。铰接 Agent 在切换主 App 前先对恢复器执行 `codesign --verify --strict --verbose=4`、`codesign -d --requirements :- --verbose=4` 和 `spctl --assess --type execute --verbose=4`，交叉验证 Team ID、指定要求和清单 SHA256，再原子安装对应 LaunchAgent。

恢复器 LaunchAgent 使用 `RunAtLoad=true` 和 `StartInterval=10`，不使用 `KeepAlive`。主 Agent 与恢复器在读写事务、plist 或 `current` 前必须持有同一个跨进程排他文件锁；未获锁的恢复器当次直接退出。fence 前按 `(command_id, side_effect, intent journal_revision)` 幂等，fence 后按 `(command_id, fence_revision)` 幂等。原始 32-byte fence token 不写 journal/snapshot，而直接保存为 macOS Keychain generic-password item：service 固定 `com.homeagent.upgrade-fence-v1`，account 为 transaction ID，访问控制仅允许主 Agent 与 Recovery 的指定要求且禁止 iCloud 同步；field 24 保存 account 引用，field 25 保存 token SHA256。写 tag 7 前必须先成功写 item；关闭事务后删除。item 缺失、长度非 32 或 ACL 不匹配时进入 `manual_recovery_required`。恢复器先 bootout 旧 label、等待记录 PID 退出，再改 plist/软链；超时记录 `rollback_failed`。

恢复器同时是首次迁移和后续切换的唯一前向交接执行者。旧 Agent 先安装并 `bootstrap` 清单指定 Recovery；Recovery 启动后必须对自身实际可执行文件流式计算 SHA256、验证 code object 的 cdhash/指定要求/Team ID，并将 PID、PID 启动时间、实际路径、boot session、Recovery SHA256、cdhash、manifest digest 和 journal revision 写入 `recovery_ready`。旧 Agent 持锁逐项核对这些值与本次清单和进程实际映像，全部匹配才写 `handoff_requested` 并退出；已有旧 Recovery 即使签名相同但摘要或清单不符也不能握手。KeepAlive 重拉的旧 Agent 看到 handoff 后立即退出。Recovery 随后 bootout 旧 job、等待记录 PID 全部退出、切换路径/plist、bootstrap 新 job，并原子写 `installed + pending ACK`；失败则按日志补偿并 bootstrap 旧 job。

恢复器只读取权限为 `0600` 的升级事务文件，不读取设备 Token，不连网。事务记录启动时所在的 boot session ID、`mach_continuous_time` 起点和启动预算；同一 boot session 使用包含睡眠时间的 continuous time 判定到期，检测到重启且尚无 `local_ready` 时立即执行回滚，不用可回拨墙钟延长预算。新 Agent 必须在完成本地配置读取、设备身份读取、运行路径、版本、完整 Apple 签名、Bundle 摘要和 `command_id` 自检后，原子写入一次性 `local_ready` 证据。

恢复器复核上述静态身份和新进程 PID 的实际可执行路径后，将事务转为 `awaiting_control`，但不清理旧 App。新 Agent 上报 Facts 后只有收到精确 prepared confirmation 才写 `convergence_prepared`；普通 `2xx` 不改变状态。连续 3 次获得能够证明协议或身份不兼容的 `400/401/403/404/409`，每次间隔 5 秒，写 `control_rejected` 并由恢复器在 commit fence 前回滚。连接失败、超时、`408`、`425`、`429` 或 `5xx` 保留 `awaiting_control` 并有界退避，拒绝新升级。

恢复器在每次 10 秒调度中都重新验证 `local_ready` 记录的 PID 仍存活、PID 启动时间未变、实际可执行路径与签名身份仍匹配。在 `awaiting_control` 或 `convergence_prepared` 且尚无 `commit_ready` 时发现进程死亡或身份改变，立即持锁写 `control_process_lost` 并回滚；不因网络暂时不可用或延迟 prepared 响应忽略本地进程死亡。

若 `startup_deadline` 前未出现有效 `local_ready`，恢复器恢复旧 App 或旧受管路径、恢复 Agent LaunchAgent 的 `ProgramArguments`、重新 bootstrap，再写入 `rolled_back`。恢复失败时保留新旧 App、事务文件和脱敏诊断，不循环重试覆盖证据。旧 App 备份只有在精确 committed 回执已持久化并提交 confirmed 后才可延迟清理。

恢复器只能由已受信铰接 Agent 或已验证的新恢复器原子替换。恢复器不连网，因此它可持久化 `installed`、`rolled_back` 或 `rollback_failed` 及其带下一 `sequence` 的待重放事件，但绝不自行发送；启动后的 Agent 在处理新指令前验证事务摘要并向 ACK v2 端点幂等重放。卸载顺序为先停止主 Agent，再停止恢复器，最后仅在无未完成事务时删除受管制品。

## 7. 路径迁移与存量安装兼容

仓库已确认现行默认 App 为 `/Applications/HomeAgent.app`，可执行文件为其 `Contents/MacOS/homeagent-agent`，用户 LaunchAgent 的 `ProgramArguments[0]` 指向该可执行文件。安装脚本在目标不可写时使用 `sudo`，因此阶段 B 不得假定运行中的用户 Agent 有权移动该 App。

新受管根目录按用户计算为 `$HOME/Library/Application Support/HomeAgent/runtime`，不依赖未解析环境变量执行文件操作；主 App 存放为 `releases/<release_sequence>/HomeAgent.app`，当前版本由同目录原子替换的 `current` 软链接指向，Agent LaunchAgent 指向 `runtime/current/HomeAgent.app/Contents/MacOS/homeagent-agent`。恢复器位于 `$HOME/Library/Application Support/HomeAgent/recovery/homeagent-recovery`。全部目录必须归当前用户所有，受管根、事务目录和恢复目录权限为 `0700`，文件默认 `0600`，可执行文件为 `0700`。

第一次 v2 升级不移动、覆盖或删除 `/Applications/HomeAgent.app`。铰接 Agent 将新签名 App 安装到用户受管路径，验证恢复器后原子替换用户 LaunchAgent 的 `ProgramArguments`。若新 App 不收敛，恢复器将 `ProgramArguments` 恢复为事务中记录的旧绝对路径。旧 App 只在后续显式卸载或重装流程中经权限确认处理，带外部文件的未识别 App 绝不删除。

安装预检必须显式识别上述现行路径、新 `runtime/current` 目标及通过历史正式版本 fixture 盘点的其他受管目标，允许将旧受管软链和 LaunchAgent 原子更新到新目标；真实用户外部文件和未识别目标必须拒绝覆盖。

跨版本回归必须同时覆盖：

1. 全新安装；
2. 从上一正式版本就地升级；
3. 保留上一正式版本旧受管软链的重装，验证软链、LaunchAgent 与配置无损迁移。

## 8. 可观测性与用户交互

### 8.1 阶段耗时

每个升级事务至少记录下列单调时钟耗时，日志与状态事件均携带 `command_id`：

- 等待指令到开始下载；
- 连接、首字节、下载和下载文件 `fsync`；
- SHA256、解压和父目录 `fsync`；
- 签名、公证票据、Gatekeeper、静态 `upgrade-info.json` 及完整 Bundle 摘要验证；
- 旧 App 备份、新 App 替换和安装目录 `fsync`；
- `installed` ACK 发送；
- 旧进程退出到新进程启动；
- 新进程启动到认证、SSE 连接和 fenced convergence 获得 committed 回执；
- Server 收敛到 UI 可见状态更新。

日志不得包含带查询凭据的完整 URL；对 URL 至少去除 userinfo、query 和 fragment。

### 8.2 界面契约

设备项不再以单一“升级中”包含所有阶段，而是显示当前阶段和持续时间。`installed` 只能显示“已安装，等待新版本上线”，`converged` 才能显示“升级完成”。失败状态显示失败阶段和脱敏原因，不得仅显示“超时”。

若保留轮询，UI 观测延迟上界必须在契约中显示计入；若改为事件推送，必须处理断线重连、事件去重、顺序和丢失后快照补偿，不得因前端丢失事件而永久卡在中间态。

### 8.3 超时与继续执行契约

| 边界 | 预算 | 到期行为 |
| --- | --- | --- |
| 单制品下载 | 120 秒 | 替换前 `failed` |
| 解包与所有静态/Apple 验证 | 120 秒 | 替换前 `failed` |
| 持久化与路径/plist 切换 | 30 秒 | 按事务子步补偿 |
| 新进程 `local_ready` | 60 秒，使用 continuous time | 恢复器回滚 |
| 确定性控制面拒绝 | 3 次，间隔 5 秒 | 恢复器回滚 |
| Command 从 `accepted` 到 Facts 收敛 | 固定 15 分钟 | Server 标记 `timed_out`，设备事务继续至本地安全终态 |
| UpgradePlan 两跳总预算 | 固定 30 分钟 | Plan `failed` 且不创建新子 Command |

Progress 事件不延长 Command 或 Plan 的固定 deadline。Server 超时不是设备文件取消信号；Agent 和恢复器按本地事务继续到 `committed_late`、`rolled_back`、`rollback_failed`、`awaiting_control` 或 `manual_recovery_required`，期间拒绝其他升级。只有 `timed_out|interrupted` 后晚到的同一 `commit_ready` 获得完整 committed confirmation 的 `202`，Agent 才原子提交 confirmed 并写 `closed_late`；Command/UI 保留原终态，Server 的独立 `late_device_outcome` 记录实际设备结果。首次迁移保留的 `/Applications/HomeAgent.app` 不由自动清理处理。

## 9. 性能契约与测量方法

正式预算必须在实施前用第 9.1 节的基线证据确认，不得根据开发机的单次最佳结果制定。候选验收门禁如下：

| 指标 | 候选门禁 | 验证方式 |
| --- | --- | --- |
| 设备端整包重签名 | 升级关键路径不得执行 `codesign --sign` | 进程调用审计 + 真实升级日志 |
| 下载完成至切换开始 | 候选版本 p95 不高于基线的 50%，且不得高于审查后确认的绝对预算 | 物理锚点为“最后下载字节落盘”到“第一次停止/退出旧 job 的副作用开始”；v1、v2 使用同一锚点，同一真实 Mac 各 20 次 |
| 切换开始至目标版本在线 | p95 不得退化超过基线 10%，且必须低于审查后确认的收敛预算 | 物理锚点为“第一次停止/退出旧 job 的副作用开始”到“重启后目标版本的认证 Facts 被 Server 持久化”；v1、v2 都新增只读测量关联，不以升级 ACK 代替 Facts，真实 launchd/端口各 20 次；v2 fenced commit 另报但不与 v1 混算 |
| UI 可见延迟 | 保留 2 秒轮询时，单次延迟不得超过 2 秒 + 两次状态 API 的实测串行耗时；若改事件推送，必须在审查时以实测消息处理预算取代该上界；p95 不得退化超过基线 10% | 真实浏览器布局与网络记录，20 次 |
| 制品大小 | 归档字节数必须记录；后续 v2 版本只与上一正式 v2 的同 `macos-app-archive-v2` 格式比较，增长超过 10% 时阻断并审查；首次 v1 二进制到 App ZIP 只报告绝对字节数，不用非同格式的 10% 比例 | CI 制品统计 |
| 首次两跳迁移总时间 | 从用户提交到候选版本 UI 可见的 p95 不得高于旧协议单次升级基线的 200%，且低于审查后绝对预算 | 上一正式版本开始的真实两跳路径，20 次 |
| 后续 v2 整包升级总时间 | 从用户提交到 UI 可见的 p95 不得高于旧协议基线的 75%，且低于审查后绝对预算 | 已安装铰接版本的真实用户路径，20 次 |

### 9.1 基线协议

1. 选定一台专用 Mac，记录型号、CPU、内存、macOS 版本、文件系统、空闲空间、电源状态和 FileVault 状态。
2. 固定上一正式版本、铰接版本、候选制品、Server 端口、本地网络和存储位置。上一正式版本的黑盒总时间必须使用未修改的正式制品；阶段归因另使用与该 tag 源码一致、仅增加时序埋点的实验制品，两组证据分开报告，不用实验制品代替正式基线。
3. 基线与候选版本均固定执行 2 次不计统计的暖机和 20 次正式尝试。每次尝试前都从同一已验证 APFS 快照或真实安装入口恢复源版本、release sequence、受管路径、软链接、LaunchAgent、事务、缓存和 Server 设备事实，然后验证源版本 Facts；不允许第二次起命中 `skipped`。不删除慢样本，不补跑失败样本；延迟分位数仅基于成功尝试，失败率基于全部 20 次尝试。
4. 使用单调时钟采集阶段时间，同时保留可关联的墙钟时间线。
5. p50 和 p95 使用 nearest-rank 方法：对 n 个有效样本升序排列后取第 `ceil(p*n)` 个；20 个样本的 p95 为第 19 个。输出原始样本、p50、p95、max、失败率及各阶段占比；失败样本不进入分位数但任一非注入失败均阻断验收，不以平均值代替尾延迟。

## 10. 安全、外部协议与失败路径

### 10.1 威胁模型

本设计防护网络劫持、下载服务或控制面单点篡改、错误制品、归档逃逸、重放/降级和升级中崩溃。已取得当前 macOS 用户会话代码执行权的攻击者不在本任务的完整性边界内：现行用户 LaunchAgent plist 和其中的设备 Token 已由同一用户可读写，同用户攻击者无需修改 `/Applications` 中的二进制即可将 `ProgramArguments` 指向其他程序并窃取设备身份。因此将运行制品放入用户专属 `0700` 目录不扩大当前有效信任边界。

阶段 B 仍必须在每次安装、启动、`local_ready` 和清理前重新验证发布清单、完整 Apple 签名和 Bundle 摘要，以防止非对抗性本地损坏、权限配置错误和被其他低权限用户替换。如产品后续要把当前登录用户纳入对抗性边界，必须另行设计 root-owned 特权 Helper 和 Token 迁移，不得宣称本方案已覆盖。

### 10.2 归档安全

制品格式更名为 `macos-app-archive-v2`，由仓库内单一 Go 打包/解包实现使用标准 ZIP 生成和提取，不把已预检的归档交给语义不同的 `ditto`。打包器只接受普通文件和目录，拒绝软链接、硬链接、AppleDouble、资源分支、ACL、需要保留的 xattr 及其他特殊类型；目录 mode 规范为 `0755`，普通文件按源执行位规范为 `0755` 或 `0644`，ZIP 外部属性必须精确记录规范值，解包后显式设置同值。Bundle digest 只对该规范解包视图计算。若真实 stapled App 依赖被拒绝属性或其他 mode，阶段 B 门禁必须失败并修订归档版本。

- 解压前验证声明大小与实际下载大小；App ZIP 压缩大小不得超过 256 MiB，解压总大小不得超过 512 MiB，文件数不得超过 20,000，单文件不得超过 128 MiB，UTF-8 路径不得超过 1,024 字节。Recovery 制品不得超过 32 MiB。
- 拒绝绝对路径、`.`、`..`、反斜线分隔、NUL、非 UTF-8、Unicode NFC 规范化后变化、APFS 大小写折叠后重复、规范化后重复、ZIP 中央目录与本地头不一致、链接、设备文件、FIFO 与多顶层布局。
- 只允许唯一顶层 `HomeAgent.app`；实际名称若不同，必须由仓库和真实制品证据修订契约。
- 临时目录权限必须禁止其他用户写入；解包器使用目录文件描述符相对操作和 no-follow 语义逐项创建，解压过程对实际写出字节、文件数和单文件同步实施硬上限，超限立即关闭并删除未发布 staging。不得跟随指向受管目录外的链接执行替换。

下载最多跟随 5 次重定向，初始 URL 和每个 `Location` 都必须是不含 userinfo 的 HTTPS URL，且 hostname 必须匹配 Server 本地配置的 `artifact_hosts` 精确允许列表；自托管内网制品也必须先显式列入，不根据 DNS 结果自动放行。每次重定向重新校验允许列表，禁止降级到 HTTP 或其他 scheme，从而防止受签名但配置错误的 URL 把 Agent 变为任意 SSRF 客户端。App 与 Recovery URL 使用完全相同的规则。

每次连接超时 5 秒、TLS 握手超时 5 秒、响应头超时 10 秒、首字节超时 15 秒，单制品整体下载超时 120 秒，解包超时 60 秒。最终响应必须为 `200` 且 `Content-Encoding` 必须缺失或为 `identity`，哈希与长度均针对 HTTP body 原始字节。存在 `Content-Length` 时必须与清单大小相等，不存在时仍按清单大小进行流式硬上限截断；响应超出一字节即失败。Range 断点续传不在 v2 首期范围，Agent 不发送 Range，收到 `206` 安全失败。

### 10.3 Apple 外部行为验证

实施前必须在受支持的真实 macOS 版本上，对真实候选 App 执行并保留完整命令、退出码和脱敏输出：

- `codesign --verify --deep --strict --verbose=4 <App>`；
- `codesign -d --requirements :- --verbose=4 <App>` 导出指定要求；
- `spctl --assess --type execute --verbose=4 <App>`；
- `xcrun stapler validate <App>`；
- 断网后重复签名验证、stapled ticket 验证与候选身份验证，确认升级不依赖安装时访问 Apple 网络服务；
- 升级前后查询本地网络权限表现，通过真实局域网 HTTP/SSE 连接确认不再弹出或静默阻断。

这些命令的文档语义不能代替真实制品观察。fixture 和 fake 必须根据上述真实输出构造，注明观察的 macOS 版本、制品摘要和与真实 Apple 工具的差异。

### 10.4 关键反例

- 制品 SHA256 正确，但签名的 Team ID 或指定要求不符：替换前拒绝，原 App 不变。
- App 签名正确，但公证票据丢失或无效：按审查后的离线策略安全失败，不降级为 ad-hoc 签名。
- 归档包含逃逸路径或指向 App 外部的链接：解压阶段拒绝，不在受管目录留下文件。
- 新 App 替换后无法启动：不依赖新 App 执行回滚，旧 App 恢复并重新上报旧版本。
- 两个升级命令重叠：第二个指令被结构化拒绝，不修改第一个任务的文件或事务记录。
- Server 在 `installed` ACK 后不可用：新进程保留未收敛事务，恢复连接后幂等上报，不重复替换。
- UI 丢失中间事件：重连后从服务端快照恢复真实阶段，不显示虚假成功或永久忙碌。

## 11. 测试与验收策略

### 11.1 契约与单元测试

- 清单格式、大小、SHA256、签名身份和平台适配正反例。
- 归档路径穿越、链接逃逸、设备文件、超大解压和非唯一 App 根目录拒绝。
- 完整状态矩阵、非法跃迁、重复命令、并发命令与重启恢复；首期不支持的安装中取消必须显式拒绝且无副作用。
- 每个失败阶段必须定义允许保留集合。可恢复阶段仅允许保留事务记录、旧 App 备份和必要诊断摘要；其他临时制品、多余替换和多余 ACK 必须有负例断言。回滚失败不得删除人工恢复所需材料。
- 批量结果从每台设备的独立终态聚合，`accepted`、`installed` 或任务创建不能增加成功数。

### 11.2 进程级与真实协议测试

- 使用真实端口运行 Server、制品下载、HTTP/SSE、ACK、`launchd` 重启与 Facts 上报闭环。
- 下载中断、响应头或长度错误、超时、HTTP 重定向、服务端重启和 SSE 重连必须基于真实请求链构造 fixture，不得根据候选实现反推。
- 使用同一卷与跨卷临时目录验证替换语义；不能证明原子性时必须在修改受管 App 前安全失败。
- 真实浏览器验证阶段文案、最终状态可见、关键入口可交互、断线恢复及全执行路径无未捕获异常；DOM 文本测试不替代浏览器布局和点击命中验收。

### 11.3 真实 macOS 跨版本验收

验收必须使用专用测试 Mac，不得未经单独授权操作日常生产设备。

1. 通过真实安装入口安装上一正式版本，确认本地网络权限、LaunchAgent、软链、配置和设备凭据。
2. 通过真实管理 API 或 Web 入口下发升级，不调用内部替换函数代替。
3. 记录全部阶段耗时、重定向和响应链、制品摘要、Apple 验证输出、安装事务及最终 Facts。
4. 在不再执行设备端整包签名的情况下，确认 App 签名与公证仍有效，局域网连接不需重新授权。
5. 注入候选 App 无法启动故障，确认独立恢复入口回滚到旧 App，旧版本 Facts 恢复且无凭据和配置损失。
6. 按第 9 节执行基线与候选版本的 20 次性能对比。
7. 执行全新安装、就地升级和带旧软链重装三条用户路径。

### 11.4 通用完成门禁

实施阶段还必须执行项目级门禁：允许路径和变更范围、架构依赖、新模块测试、基于真实端口和协议的全量 `-race` 回归、Diff Coverage 不低于 60%、UTF-8 无 BOM、`git diff --check` 和变更文件清单。修改客户端时必须升级客户端版本号。

## 12. 阶段启用门禁与决策记录

### 12.1 设计闭环记录

- [x] 权限模型：整包安装转入当前用户专属受管路径，旧 `/Applications` App 不由带内升级覆盖或删除。
- [x] 回滚执行者：使用与主 App 分离的签名恢复器和独立用户 LaunchAgent。
- [x] 状态映射：通用 Command 保留现有枚举，升级阶段进入以 `command_id` 索引的 Upgrade Progress 投影。
- [x] 旧 Agent 兼容：管理员一次操作由 Server 编排为 v1 铰接升级与 v2 整包升级两个子 Command。
- [x] 清单信任：固定 Ed25519、长度前缀编码、内置双公钥轮换、时间窗口和持久化发布序号防降级。
- [x] 候选身份：改为签名前生成、随 App 签名的静态 `upgrade-info.json`，设备端不执行待安装代码。
- [x] 归档格式：固定为同一 Go 实现生成和解包的 `macos-app-archive-v2` ZIP，禁止特殊文件与外部解压语义分歧。

### 12.2 阶段 B 真实启用门禁

以下项目是启用已定案阶段 B 的证据门禁，不是尚未定义的设计决策。任一未通过时，阶段 A 可交付，但 Server 必须保持 v2 整包投递开关关闭。

Server 配置项 `macos_app_upgrade_v2.enabled` 默认为 `false`，不得由远程控制负载、环境自动检测或 Agent 能力上报自行开启。关闭时 Server 可记录 v2 能力：对 `unlocked` 设备可投递 v1 铰接升级，对 `v2_locked` 设备必须返回 `v2_upgrade_temporarily_unavailable` 且不创建任何 v1 Command，不得用关闭开关绕过设备本地锁。开启时 Server 必须同时加载可验证签名的铰接与目标清单；任一清单缺失或无效时启动失败，不允许退回到无清单整包投递。

- [ ] 用真实升级日志完成现状分阶段基线，并写入绝对性能预算。
- [ ] 确定受支持 macOS 版本、CPU 架构与专用验收设备。
- [ ] 从上一正式版本真实安装盘点 Bundle、LaunchAgent、所有受管路径与所有权。
- [ ] 用真实发布 App 验证 Developer ID、Team ID、Bundle ID、指定要求、公证、stapling 和断网验证。
- [ ] 确认签名 App 身份和本地网络权限在旧路径到新受管路径切换前后稳定。
- [ ] 证明 `macos-app-archive-v2` Go 打包/解包往返保留签名、公证票据和所需文件模式，且真实 App 不依赖被格式拒绝的扩展属性。
- [ ] 记录真实下载服务的全部 HTTP 重定向、响应头、长度、缓存和 Range 行为，并由此构造 fixture 与反例。
- [ ] 在真实文件系统上验证受管目录原子切换、LaunchAgent 重载、恢复器超时和回滚。
- [ ] 用上一正式版本验证“一次用户操作 → v1 铰接 → v2 整包 → Facts 收敛”完整路径。

### 12.3 已定决策

| 决策 | 结论 | 理由 |
| --- | --- | --- |
| 完成语义 | 只有新进程 Facts 证明目标版本运行才是升级成功 | 保留现有健康收敛契约，避免文件替换误报成功 |
| 签名位置 | 发布环境签名、公证和 stapling，设备端只验证 | 从关键路径移除设备端整包重签名，同时不降低身份校验 |
| 安装单位 | 替换整个 App Bundle，恢复器作为独立制品 | 避免替换内部二进制后破坏外层签名契约 |
| 状态展示 | 拆分 `installed` 与 `converged` | 区分安装性能和重启收敛，不制造虚假完成态 |
| 第三方框架 | 首期不引入 | 先验证现有架构下的完整 App 替换，避免未证明必要性的新依赖 |

## 13. 成功标准与可追溯证据

| 成功标准 | 直接验证手段 | 待产生证据 |
| --- | --- | --- |
| 每个阶段耗时可独立定位 | 同一 `command_id` 串联 Agent、Server 和 UI 时间线 | 原始日志、状态快照和网络记录 |
| 设备端不在升级关键路径重签名 | 审计无 `codesign --sign`，只有验证命令 | 进程调用审计与升级日志 |
| 签名、公证和本地网络权限不退化 | 升级前后 Apple 工具验证与真实局域网连接 | 脱敏命令输出、Console/Network 记录和权限交互录屏 |
| 安装阶段达到性能预算 | 基线与候选各 20 次 Benchmark | 原始样本和 p50/p95/max 汇总 |
| 新 App 启动失败可回滚 | 在候选 App 替换后注入启动失败 | 事务时间线、恢复后路径与旧版本 Facts |
| 不产生虚假批量成功 | 混合收敛、失败、超时与回滚设备的批量负例 | 每设备终态与聚合结果 |
| 跨版本和重装兼容 | 全新安装、上一正式版本就地升级、带旧软链重装 | 版本、路径、软链、配置、凭据和 Facts 记录 |

## 14. 外部假设审计记录模板

每次真实协议与升级验收在本节追加或引用可追溯证据，不得在未执行时预填通过：

| 项目 | 记录内容 |
| --- | --- |
| 外部服务 | Apple 签名/公证/Gatekeeper，候选制品托管和 HTTP 下载服务 |
| 关键假设 | 待验证 |
| 真实请求结果 | 未执行 |
| 完整重定向/响应链 | 未执行 |
| 测试替身来源及差异 | 未构造；必须从真实观察派生 |
| 关键反例测试 | 未执行 |
| 上一正式版本升级验收 | 未执行 |

### 14.1 当前外部观察

2026-08-29 以真实 HTTPS 请求访问 `GET https://api.github.com/repos/RokiLai/home-agent/releases/latest`，最终返回 `404 Not Found`，响应中未提供 Release 制品或重定向。该结果只能证明当次公开 API 请求未取得 latest release，不能推断是“无 Release”、“仓库可见性限制”或其他原因，也不能提供 Developer ID、公证或下载响应链证据。因此第 12.2 节的真实制品与下载协议门禁仍保持未通过，v2 整包开关必须默认关闭。
