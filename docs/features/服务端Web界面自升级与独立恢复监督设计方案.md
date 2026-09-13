# 服务端 Web 界面自升级与独立恢复监督设计方案

## 1. 文档状态与说明

| 阶段 | 状态 | 说明 |
| --- | --- | --- |
| 设计 | 审查通过 | 已完成设计审查并获用户确认实施授权 |
| 实施 | 实施完成 | 已装配 Runner、编写 Supervisor 脚本、补齐状态收敛逻辑及覆盖测试，服务端版本升级至 v0.6.24 |
| 验收 | 本地验收完成，生产待部署 | 全量 -race 回归与 Diff Coverage（86.4%）门禁均通过；生产环境宿主机部署与跨版本真实升级待部署执行 |

本文档旨在解决服务端（`homeagent-server`）通过 Web 控制台自升级时的安全闭环与高可用问题，建立宿主机环境下的独立恢复监督机制，使控制台升级按钮安全激活并具备防变砖能力。

## 2. 背景与直接证据

通过对当前代码库的全面排查，确认以下直接事实：

1. **前端界面已具备操作入口与轮询逻辑**：
   - 在 [`internal/ui/static/index.html`](file:///Users/roki/Projects/home-agent/internal/ui/static/index.html) 中存在升级按钮 `#serverUpgradeBtn` 与 Release 外链 `#serverReleaseLink`；
   - 在 [`internal/ui/static/js/settings.js`](file:///Users/roki/Projects/home-agent/internal/ui/static/js/settings.js) 中，`renderVersionStatus` 函数定义了按钮启用规则：
     ```javascript
     upgradeBtn.disabled = server.update_state !== 'update_available' || server.status !== 'available' || server.upgrade_supported !== true;
     upgradeBtn.title = server.upgrade_supported === true ? '' : '尚未安装独立恢复监督器，服务端自升级保持禁用';
     ```
   - `initVersionStatusEvents` 绑定了点击事件，包含双重二次确认弹窗、调用 `POST /api/v2/system/server-upgrades` 及后续基于 `monitorServerUpgradeOperation` 的状态轮询逻辑。
2. **后端核心原子升级引擎已实现**：
   - [`internal/serverupgrade/upgrade.go`](file:///Users/roki/Projects/home-agent/internal/serverupgrade/upgrade.go) 中的 `PerformServerSelfUpgrade` 实现了 Release 资产下载、SHA256 校验、`homeagent-server version` 冒烟预检、旧二进制备份（`.bak`）以及原子覆盖（`atomicReplace`）。
   - [`internal/serverupgrade/operation.go`](file:///Users/roki/Projects/home-agent/internal/serverupgrade/operation.go) 实现了持久化操作管理器 `OperationManager`，支持状态流转：`prepared` → `replacing` → `restarting` → `verifying` → `succeeded` / `failed` / `rolled_back`。
3. **主程序目前显式禁用了自升级入口**：
   - 在 [`cmd/homeagent-server/main.go`](file:///Users/roki/Projects/home-agent/cmd/homeagent-server/main.go) 初始化 `api.Server` 时，仅传入了 `ServerUpgradeOperations`，未挂载 `ServerUpgradeRunner`（保持为 `nil`）；
   - 根据 [`internal/api/api.go`](file:///Users/roki/Projects/home-agent/internal/api/api.go) 的 `GET /api/v2/system/version-status` 实现，`UpgradeSupported` 计算条件为：
     `UpgradeSupported: s.ServerUpgradeOperations != nil && s.ServerUpgradeRunner != nil`；
   - 因此系统当前返回的 `UpgradeSupported` 恒为 `false`，导致前端按钮置灰。
4. **架构设计规范要求强保障**：
   - 在已确立的 [《服务端与客户端独立版本及双通道升级设计方案》](file:///Users/roki/Projects/home-agent/docs/features/%E6%9C%8D%E5%8A%A1%E7%AB%AF%E4%B8%8E%E5%AE%A2%E6%88%B7%E7%AB%AF%E7%8B%AC%E7%AB%8B%E7%89%88%E6%9C%AC%E5%8F%8A%E5%8F%8C%E9%80%9A%E9%81%93%E5%8D%87%E7%BA%A7%E8%AE%BE%E8%AE%A1%E6%96%B9%E6%A1%88.md) 第 6.2 节中明确规定：
     > “任一受支持服务管理器没有可验证恢复主体时不得开放 Server 自升级。”
   - 服务端通常作为唯一控制中枢，单点变砖会导致用户彻底失联。必须提供宿主机层面的独立监督与回滚机制，方可开放 Web 触发入口。

## 3. 目标与非目标

### 3.1 目标

1. **补全宿主机独立恢复监督器**：基于 Linux `systemd`（及 macOS `launchd`）标准服务管理机制，设计轻量、高可靠的 Supervisor 包装器（Wrapper），捕获进程崩溃并在租约超时时自动回滚旧版本。
2. **在主程序中装配 Runner**：在 `cmd/homeagent-server/main.go` 中建立环境自检逻辑，在检测到受管监督环境可用时激活 `ServerUpgradeRunner`，使 `UpgradeSupported` 安全变为 `true`。
3. **确立端到端状态收敛**：跨越服务重启断线，Web 控制台能够通过幂等轮询准确获取最终升级结果（成功 `succeeded` 或失败并回滚 `rolled_back`）。
4. **提供防误触与并发排他保护**：严格限制仅 Owner 可发起，同一时刻只允许一个升级操作。

### 3.2 非目标

1. 不引入重型外部监控组件或第三方守护代理，保持单机极简依赖。
2. 不支持在 Docker 容器或只读根文件系统等无法就地替换二进制的环境中启用 Web 升级；该类环境继续保持 `UpgradeSupported: false`。
3. 不变更现有的 REST API 协议路径及字段定义，完全复用现有 `/api/v2/system/server-upgrades`。

## 4. 系统架构与关键链路

```text
[ 管理员在控制台点击“升级服务端” ]
                   │
                   ▼ (POST /api/v2/system/server-upgrades)
      [ Server: API 校验与排他锁检查 ]
                   │
                   ▼ (异步启动 ServerUpgradeRunner)
  1. 下载新版本 Release 二进制与 SHA256 校验和
  2. 校验文件完整性，执行预检: ./tmp-server version
  3. 写入事务持久化状态: replacing
  4. 备份当前正在运行的二进制为 homeagent-server.bak
  5. 原子替换可执行文件: mv tmp-server homeagent-server
  6. 写入事务持久化状态: restarting (附 60s 租约有效截止时间)
  7. 触发优雅停机回调 RestartCallback，进程正常退出
                   │
                   ▼
       [ 宿主机服务管理器: systemd 重启进程 ]
                   │
       ┌───────────┴─────────────────────────────┐
       │ 路径 A: 新版本正常启动                    │ 路径 B: 新版本崩溃 / 无法启动 / 租约超时
       ▼                                         ▼
[ 新 Server 加载事务文件 ]              [ 宿主机独立监督器 Supervisor ]
  ├─ 识别自身版本 == TargetVersion         ├─ 捕获崩溃退出码或 60s 健康检查超时
  ├─ 状态收敛并落盘为 succeeded             ├─ 将 homeagent-server.bak 强制覆盖还原
  ├─ 清理 .bak 备份文件                    ├─ 事务状态写入 rolled_back
  ▼                                        ├─ 重新拉起旧版本服务
[ Web 轮询感知 succeeded，提示升级成功 ]    ▼
                                   [ 旧 Server 启动并识别 rolled_back ]
                                   [ Web 轮询感知 rolled_back，告警提示已回滚 ]
```

## 5. 详细设计

### 5.1 宿主机独立恢复监督器（Supervisor / Watchdog）

为避免破坏单二进制交付的简洁性，恢复监督器采用**“启动包装脚本 + systemd 服务看门狗”**双重保险：

1. **启动包装脚本（`scripts/homeagent-server-runner.sh`）**：
   - 由 systemd 服务单元的 `ExecStart` 直接调用；
   - 托管拉起 `homeagent-server`，并在其退出后立即评估退出状态和事务日志：
     ```bash
     #!/bin/sh
     BIN_PATH="/usr/local/bin/homeagent-server"
     BAK_PATH="/usr/local/bin/homeagent-server.bak"
     UPGRADE_JOURNAL="/var/lib/homeagent/server-upgrades.json"

     "$BIN_PATH" "$@"
     EXIT_STATUS=$?

     # 若进程非正常退出（崩溃、Panic、依赖缺失等）
     if [ $EXIT_STATUS -ne 0 ] && [ -f "$UPGRADE_JOURNAL" ] && [ -f "$BAK_PATH" ]; then
       # 检查事务是否停留在 replacing 或 restarting 中
       if grep -Eq '"status":"(replacing|restarting)"' "$UPGRADE_JOURNAL"; then
         echo "警告: 检测到 HomeAgent 服务端在升级重启期间崩溃，执行原子回滚..." >&2
         cp -f "$BAK_PATH" "$BIN_PATH"
         chmod 755 "$BIN_PATH"
         sed -i 's/"restarting"/"rolled_back"/' "$UPGRADE_JOURNAL" 2>/dev/null || true
         sed -i 's/"replacing"/"rolled_back"/' "$UPGRADE_JOURNAL" 2>/dev/null || true
       fi
     fi

     exit $EXIT_STATUS
     ```
2. **systemd 服务单元适配（`homeagent-server.service`）**：
   - 注入环境变量 `HOMEAGENT_SUPERVISED=true`，作为主程序识别受管环境的凭据；
   - 设置 `Restart=always` 与 `RestartSec=3s`，保证进程退出后能够自动拉起；
   - 配置 `TimeoutStartSec=60s`，防止新版本进程挂死。

### 5.2 服务端主程序改动设计

1. **环境与监督器可用性自检**：
   - 在 [`cmd/homeagent-server/main.go`](file:///Users/roki/Projects/home-agent/cmd/homeagent-server/main.go) 初始化时，检查：
     1. 环境变量 `HOMEAGENT_SUPERVISED == "true"`；
     2. 当前运行的二进制所在目录具备写入与重命名权限；
     3. 运行环境非 Docker / 只读挂载容器。
   - 仅当上述条件满足时，装配 `ServerUpgradeRunner`；否则保持为 `nil`，界面按钮保持禁用并显示安全提示。
2. **装配 `ServerUpgradeRunner` 执行函数**：
   - 将 `serverupgrade.PerformServerSelfUpgrade` 封装为 runner 函数；
   - 设置 `RestartCallback`：在替换完成后，启动后台协程，延时 500ms 调用当前 `http.Server.Shutdown(ctx)`，使主监听循环正常退出，由外层 systemd 自动拉起新版本。
3. **跨重启状态自动收敛（`NewOperationManager` 增强）**：
   - 新版本启动时，`NewOperationManager` 扫描 `server-upgrades.json`：
     - 若存在 `status == restarting` 且 `target_version == current_version`，原子流转为 `succeeded` 并清理旧的 `.bak` 文件；
     - 若存在 `status == restarting` 且 `target_version != current_version`（说明未升级成功或已被回滚），标记为 `failed` 或 `rolled_back`。

### 5.3 完整状态流转与失败路径矩阵

| 阶段 | 初始状态 | 触发条件 | 目标状态 | 失败/异常处理路径 |
| --- | --- | --- | --- | --- |
| 1. 创建操作 | 空 | 管理员点击 Web 升级按钮 | `prepared` | 若已有未完成事务，拒绝并返回 `409 Conflict` |
| 2. 准备与校验 | `prepared` | 开始下载 Release 资产 | `prepared` | 网络中断/SHA256不匹配/冒烟失败：清除临时文件，置为 `failed` |
| 3. 二进制替换 | `prepared` | 校验与冒烟成功 | `replacing` | 备份旧二进制为 `.bak`，原子重命名覆盖可执行文件 |
| 4. 触发重启 | `replacing` | 替换成功 | `restarting` | 设置 60s 租约，调用平滑退出，等待服务管理器拉起 |
| 5. 新版本收敛 | `restarting` | 新进程正常启动，版本匹配 | `succeeded` | 写入事务终态，删除 `.bak` 备份文件 |
| 6. 异常回滚 | `restarting` | 新版本启动崩溃或超时 | `rolled_back` | 监督器自动还原 `.bak`，旧版本拉起并向前端返回回滚结果 |

### 5.4 Web 控制台前端体验契约

1. **防重复点击与确认**：
   - 用户点击后弹出双重 Modal 确认框，明确告知“服务即将重启，控制台将短暂断开”；
   - 确认后按钮立即进入 `disabled` 状态，文案切换为“升级中，等待服务重启...”。
2. **断线与静默重连轮询**：
   - 服务重启期间（通常为 1~5 秒），前端发出的轮询请求会遭遇网络错误（Failed to fetch / Connection Refused）；
   - [`settings.js`](file:///Users/roki/Projects/home-agent/internal/ui/static/js/settings.js) 的 `monitorServerUpgradeOperation` 保持捕获异常并安静重试，不弹出干扰性的全局网络报错；
   - 轮询超时时间设置为 90 秒；
   - 一旦获取到终态：
     - 若为 `succeeded`：弹出绿色 Toast 提示“服务端已升级至 {target_version}”，随后刷新版本状态或重新加载页面；
     - 若为 `failed` 或 `rolled_back`：弹出警告 Toast 提示“升级未完成已回滚至旧版本”，恢复升级按钮供管理员重试。

## 6. 测试与验证策略

1. **单测（Unit Tests）**：
   - 在 [`internal/serverupgrade`](file:///Users/roki/Projects/home-agent/internal/serverupgrade) 中增加针对 `OperationManager` 跨重启状态收敛的覆盖测试（测试 `restarting` -> `succeeded` 与 `rolled_back` 场景）；
   - 验证损坏资产、SHA256 校验失败及冒烟预检失败时的原子清理。
2. **集成与负例测试（Integration & Negative Tests）**：
   - 模拟服务管理环境，在独立测试用例中验证升级全流程；
   - 构造注入致命 Panic 的伪造新二进制，验证监督器（Runner 脚本）能否在进程崩溃后正确捕获、还原 `.bak` 并恢复旧版本运行，断言升级事务置为 `rolled_back` 且服务未挂死。
3. **完成门禁**：
   - 变更范围严格限制在授权路径；
   - 全量 `-race` 回归通过；
   - Diff Coverage 达到 60% 以上门禁。
