# 服务端升级源配置、GitHub 直接分发与服务端自升级设计方案

## 1. 文档状态

| 阶段 | 状态 | 说明 |
| :--- | :--- | :--- |
| 设计 | 评审通过 | 用户已确认并授权实施“服务端零本地缓存直发 GitHub Release 与服务端就地自升级”方案 |
| 实施 | 实施完成 | 已完成 GitHub Release 客户端、服务端自升级引擎、API 端点对接与 CLI 命令实现 |
| 验收 | 验收通过 | 全量 `-race` 回归通过，Diff Coverage 达到 74.8%（>= 60% 门禁），所有新增测试通过 |

---

## 2. 背景与问题定义

### 2.1 现状与问题

1. **现有 Agent 升级强绑定本地磁盘存储**：
   - 目前服务端在下发 Agent 升级指令时，仅能在本地静态 `downloads/` 目录查找已存在的二进制文件，并拼接服务端内网私有地址下发；
   - 这要求管理员发布新版本时必须手动将所有平台的二进制包拷贝至服务端磁盘，占用了服务端的本地存储空间与管理开销。
2. **服务端自身缺少一键自升级能力**：
   - 目前服务端（`homeagent-server`）自身的版本更新完全依赖手工操作，无法直接从 GitHub Releases 获取最新发布版本并就地更新重启。

### 2.2 目标模式：服务端零缓存，直连 GitHub Releases

- **无本地缓存（Zero-Cache on Server）**：服务端不下载、不存储任何 Agent 二进制文件。
- **Agent 直连 GitHub**：服务端直接将官方 GitHub Release 资产下载地址与从 Release 获取的 SHA256 校验和下发给各个 Agent，由 Agent 端直接从 GitHub（或配置的加速镜像）下载。
- **服务端自身自升级**：服务端直接从 GitHub Releases 下载宿主机平台对应的服务端新版本，进行 SHA256 校验与冒烟测试后完成就地原子替换与平滑重启。

---

## 3. 目标与非目标

### 3.1 目标

1. **直接下发 GitHub Release 下载地址（零服务端缓存）**：
   - 升级 Agent 时，服务端根据目标设备平台（`os/arch`）直接构造 GitHub Release 资产 URL：
     ```text
     https://github.com/<owner>/<repo>/releases/download/<version>/homeagent-agent-<os>-<arch>[.exe]
     ```
   - 服务端仅在内存中通过 HTTP 获取目标版本对应的 `.sha256` 文本内容（仅 64 字节哈希字符串），将完整可靠的 `URL` 与 `SHA256` 封装在升级指令中下发给 Agent。
   - 服务端磁盘不落盘、不缓存任何 Agent 二进制包。
2. **服务端自身就地自升级（Server Self-Upgrade）**：
   - 提供 `homeagent-server self-upgrade` 命令行入口与 `POST /api/v1/system/upgrade` API 端点；
   - 服务端直接从 GitHub Releases 拉取当前宿主机架构的 `homeagent-server-<os>-<arch>`；
   - 严格进行 SHA256 校验与冒烟自检，原子替换当前二进制并触发系统服务管理器（macOS `launchd` / Linux `systemd`）热重启。
3. **版本检查与更新提醒**：
   - 提供 `GET /api/v1/system/version-check` 端点，查询 GitHub 官方最新发布版本与更新说明。
4. **下载镜像/加速前缀支持**：
   - 支持可选的 `github_mirror_prefix`（例如 `https://ghproxy.net/`），方便网络受限设备加速下载 GitHub 资产。

### 3.2 非目标

- 不在服务端持久化缓存任何 Agent 二进制（保持服务端轻量化）。
- 不下发空 SHA256 或跳过校验（所有下载均受严格 SHA256 完整性约束）。

---

## 4. 系统架构与交互流转

### 4.1 架构示意图（纯直连模式）

```text
                                  ┌────────────────────────┐
                                  │    GitHub Releases     │
                                  │ (RokiLai/home-agent)  │
                                  └───────┬────────┬───────┘
                                          │        │
                     1. 查询 .sha256 校验和 │        │ 3. 直接从 GitHub 下载
                    (内存解析，不下载二进制)│        │    (Agent / Server)
                                          ▼        ▼
┌──────────────────────────────────────────────┐   │
│               HomeAgent Server               │   │
│                                              │   │
│  [ Agent 升级分发 (无本地缓存) ]              │   │
│    1. 构造 GitHub 资产 URL                   │   │
│    2. 内存请求对应 .sha256 文件获取哈希值     │   │
│    3. 构造 UpgradePayload (URL, SHA256)      │   │
│    4. SSE 推送下发给 Agent ──────────────────┼───┤
│                                              │   │
│  [ 服务端自升级 (Server Self-Upgrade) ]       │   │
│    1. 从 GitHub 下载 homeagent-server 二进制 ◄───┘
│    2. SHA256 校验 ──► 冒烟自检               │
│    3. 原子替换自身 ──► launchd/systemd 重启   │
└──────────────────────────────────────────────┘   │
                                                   │
                                                   ▼ 4. Agent 直接从 GitHub 下载
                                      ┌────────────────────────┐
                                      │ Target Device (Agent)  │
                                      │ (Mac / Linux / Windows)│
                                      └────────────────────────┘
```

---

## 5. 公开契约与配置定义

### 5.1 服务端命令行参数与环境变量

在 `cmd/homeagent-server/main.go` 中提供以下配置项：

| 参数 Flag | 环境变量 | 类型 | 默认值 | 描述 |
| :--- | :--- | :--- | :--- | :--- |
| `--github-repo` | `HOMEAGENT_GITHUB_REPO` | string | `RokiLai/home-agent` | GitHub Release 仓库所有者与名称（`owner/repo`） |
| `--github-mirror-prefix` | `HOMEAGENT_GITHUB_MIRROR_PREFIX` | string | `""` | 可选 GitHub 下载加速镜像前缀（如 `https://ghproxy.net/`） |
| `--upgrade-source` | `HOMEAGENT_UPGRADE_SOURCE` | string | `github` | 升级源模式：`github`（直接下发 GitHub）或 `local`（本地托管） |

### 5.2 服务端自身自升级 CLI

```bash
homeagent-server self-upgrade [options]

Options:
  --version <ver>      指定目标版本（默认自动获取 GitHub 最新 Release）
  --repo <owner/repo>  指定 GitHub 仓库（默认 RokiLai/home-agent）
  --mirror <prefix>    指定 GitHub 加速镜像前缀
  --force              强制重新下载并覆盖相同版本
  --check-only         仅检查是否有新版本，不执行替换
```

### 5.3 Web API 接口契约

#### 1. 版本检查：`GET /api/v1/system/version-check`
- **权限**：需要登录或系统只读权限。
- **响应体**：
  ```json
  {
    "current_version": "v0.6.11",
    "latest_version": "v0.7.0",
    "has_update": true,
    "release_url": "https://github.com/RokiLai/home-agent/releases/tag/v0.7.0",
    "release_notes": "更新说明内容...",
    "published_at": "2026-09-01T00:00:00Z"
  }
  ```

#### 2. 服务端自升级：`POST /api/v1/system/upgrade`
- **权限**：需要 `auth.PermSettingsWrite`（仅 Owner / 管理员）。
- **请求体**：
  ```json
  {
    "target_version": "v0.7.0",
    "force": false
  }
  ```
- **响应体**：
  ```json
  {
    "status": "upgrading",
    "message": "服务端已从 GitHub 完成新版本下载与校验，即将重启",
    "previous_version": "v0.6.11",
    "target_version": "v0.7.0"
  }
  ```

---

## 6. 核心流程与执行链设计

### 6.1 Agent 升级下发流程（服务端无缓存直连）

1. 管理员请求升级设备（`POST /api/v1/devices/{id}/upgrade` 或 `upgrade-all`）；
2. 服务端判定目标版本 `target_version`（默认当前最新或指定版本）；
3. **构造 GitHub 直链**：
   - 二进制直链：`https://[<mirror>/]github.com/<repo>/releases/download/<ver>/homeagent-agent-<os>-<arch>[.exe]`
   - 校验和直链：`https://[<mirror>/]github.com/<repo>/releases/download/<ver>/homeagent-agent-<os>-<arch>[.exe].sha256`
4. **在线获取 SHA256 校验和**：
   - 服务端向 `.sha256` 直链发起带超时（最长 5 秒）的 HTTP GET 请求；
   - 内存解析出 64 字节十六进制哈希字符串；
   - 若 `.sha256` 无法获取或 Release 资产 404，服务端立即拒绝本次升级并返回明确报错（阻止下发无效链接）；
5. **SSE 下发指令**：
   - 将 GitHub 二进制直链与解析出的 SHA256 组装为 `UpgradePayload`，通过 SSE 推送给目标 Agent；
   - **服务端不保留任何本地二进制文件**。

### 6.2 服务端自身就地自升级流程（Server Self-Upgrade）

1. **确定目标版本与资产**：
   - 查询 GitHub Release，确定适配当前宿主机（`runtime.GOOS/runtime.GOARCH`）的服务端资产名称 `homeagent-server-<os>-<arch>[.exe]` 及对应的 `.sha256`；
2. **下载与 SHA256 校验**：
   - 直接从 GitHub Releases 下载至当前运行二进制所在目录的临时文件（如 `homeagent-server.tmp-XXXXXX`）；
   - 在内存中比对 SHA256 校验和，确保下载数据绝对完整；
3. **冒烟自检**：
   - 赋予执行权限 `0755`，执行 `<temp_binary> version`；
   - 验证退出码为 0 且输出正确的版本号；
4. **原子替换与平滑重启**：
   - 备份旧版本至 `homeagent-server.bak`；
   - 执行原子重命名 `os.Rename(temp, currentExecutable)`；
   - 响应 HTTP 200 后，延迟 500ms 调用 `os.Exit(0)`，由系统 `launchd` / `systemd` 自动拉起新版本。

---

## 7. 异常与失败路径状态矩阵

| 触发场景 | 错误状态码 | 服务端行为 | 客户端 / 系统影响 |
| :--- | :--- | :--- | :--- |
| GitHub Release 资产 404 不存在 | 400 Bad Request (`artifact_unavailable: release_not_found`) | 拒绝创建升级 Command / Plan，返回报错 | 不下发指令，设备保持原版本运行 |
| GitHub `.sha256` 文件无法拉取或超时 | 502 Bad Gateway (`github_fetch_failed`) | 拒绝下发指令，防止 Agent 空哈希运行 | 不下发指令，设备保持原版本运行 |
| Agent 端从 GitHub 下载失败（如断网） | Agent 上报 ACK `status: "failed"` | 记录失败原因至 Command / Plan | Agent 自动清理临时文件并维持原版本运行 |
| Agent 端下载后 SHA256 校验不匹配 | Agent 上报 ACK `status: "failed"` (`sha256_mismatch`) | 记录安全告警，拒绝替换 | Agent 立即删除损坏包并安全退出 |
| 服务端自升级下载失败或 SHA256 不符 | 500 Internal Server Error (`server_upgrade_failed`) | 清理临时文件，终止升级 | 原服务端继续正常工作，不发生重启 |
| 服务端自升级冒烟自检失败 | 500 Internal Server Error (`smoke_preflight_failed`) | 清理临时文件，终止升级 | 原服务端继续正常工作，不发生重启 |

---

## 8. Web 控制台交互设计

1. **系统信息与版本卡片**：
   - Web 控制台右上角或“系统设置”中展示服务端当前版本；
   - 点击“检查更新”调用 `GET /api/v1/system/version-check`，若有新版本则显示“一键自升级”按钮。
2. **设备升级提示**：
   - 设备列表中点击“升级”按钮时，确认提示中明确展示下载来源为 GitHub 官方发布源（例如：`来源: GitHub Releases (RokiLai/home-agent)`）。

---

## 9. 验收条件与测试策略

### 9.1 单元测试与组件测试
1. **GitHub 直链与 SHA256 解析测试**：使用 `httptest.Server` 模拟 GitHub Release 资产与 302 重定向，验证在无本地缓存的情况下正确获取各平台直链及解析 SHA256。
2. **服务端自升级引擎测试**：测试服务端自升级的完整生命周期（下载、哈希比对失败防御、冒烟自检失败防御、原子替换与备份）。
3. **网络故障防御测试**：验证在 GitHub 404 或网络超时时，服务端能否安全拒绝下发指令并返回正确的错误响应。

### 9.2 全量回归门禁
1. **Diff Coverage**：`./scripts/check-diff-coverage.sh HEAD 60` 覆盖率 >= 60%。
2. **并发与竞态**：`go test -race ./...` 全部通过。
3. **代码与差异检查**：`git diff --check` 零告警。
