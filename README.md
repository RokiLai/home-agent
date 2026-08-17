# HomeAgent

HomeAgent 是一个基于 Go 的轻量级局域网设备管理与控制平面，用于接入家庭/办公局域网中的可信设备，并根据 ACL 策略自动分发和同步 SSH 公钥。

HomeAgent 采用 **SSE 实时下行控制平面** + **客户端自动 ACK 收敛** + **SSH 兜底同步** 的双轨架构；不会存储设备私钥，也不会覆盖用户自行管理的 `authorized_keys` 条目。

---

## 核心特性

- ⚡ **SSE 实时控制平面**：Server 通过 Server-Sent Events (SSE) 毫秒级下发公钥更新与同步指令，Agent 毫秒级响应并即时完成本地密钥应用。
- 🔄 **ACK 状态闭环收敛**：Agent 应用成功后自动上报 ACK，Server 实时记录各设备同步收敛状态，并在控制台动态呈现。
- 🖥️ **内嵌 Web 控制台**：Server 自带内嵌轻量级 Dashboard，直观展示网络拓扑、实时事件流日志、在线/离线状态与一键同步触发。
- 🛡️ **跨平台系统服务**：Agent 支持一键安装为操作系统原生自启服务（macOS `launchd`、Linux `systemd`、OpenWrt `procd`、Windows `Windows Service`）。
- 🔒 **安全与无侵入**：仅同步公开密钥，私钥保留在各设备本地；仅以原子方式更新 `BEGIN/END HOMEAGENT MANAGED` 标记区块，不影响已有 SSH 密钥。

---

## 快速构建

需要 Go 1.26 或更高版本。

```sh
# 运行单元测试
go test ./...

# 构建本地二进制
go build -o bin/homeagent-server ./cmd/homeagent-server
go build -o bin/homeagent-agent ./cmd/homeagent-agent
```

为 Server 分发目录构建各平台 Agent：

```sh
mkdir -p dist
GOOS=darwin GOARCH=arm64 go build -o dist/homeagent-agent-darwin-arm64 ./cmd/homeagent-agent
GOOS=darwin GOARCH=amd64 go build -o dist/homeagent-agent-darwin-amd64 ./cmd/homeagent-agent
GOOS=linux GOARCH=arm64 go build -o dist/homeagent-agent-linux-arm64 ./cmd/homeagent-agent
GOOS=linux GOARCH=amd64 go build -o dist/homeagent-agent-linux-amd64 ./cmd/homeagent-agent
GOOS=windows GOARCH=arm64 go build -o dist/homeagent-agent-windows-arm64.exe ./cmd/homeagent-agent
GOOS=windows GOARCH=amd64 go build -o dist/homeagent-agent-windows-amd64.exe ./cmd/homeagent-agent
```

---

## 运行 Server

```sh
HOMEAGENT_JOIN_TOKEN='replace-with-a-random-secret' \
homeagent-server serve \
  --listen :8080 \
  --data-dir "$HOME/Library/Application Support/HomeAgent" \
  --downloads-dir ./dist \
  --scripts-dir ./scripts
```

> **提示**：生产或长期运行建议使用 `/var/lib/homeagent` 作为 `--data-dir`。首次启动时，Server 会在数据目录下自动生成 Ed25519 管理密钥。配置文件模板请参考 [configs/config.example.yaml](configs/config.example.yaml)。

### 访问内嵌 Web 控制台
Server 启动后，直接在浏览器中打开：
```text
http://<server-ip>:8080/
```
在控制台顶部输入 `HOMEAGENT_JOIN_TOKEN` 即可连接实时 SSE 流，查看设备状态、网络拓扑图与下发事件日志。

---

## 接入设备

### 1. macOS / Linux / OpenWrt
在目标设备上执行一键安装脚本（脚本会自动下载对应架构的 Agent 二进制，并注册拉起开机自启系统服务）：

```sh
curl -fsSL http://<server-ip>:8080/install.sh -o /tmp/homeagent-install.sh
HOMEAGENT_SERVER=http://<server-ip>:8080 \
HOMEAGENT_JOIN_TOKEN='replace-with-a-random-secret' \
sh /tmp/homeagent-install.sh
```

### 2. Windows
以**管理员权限**打开 PowerShell 执行：

```powershell
$env:HOMEAGENT_SERVER="http://<server-ip>:8080"
$env:HOMEAGENT_JOIN_TOKEN="replace-with-a-random-secret"
irm http://<server-ip>:8080/install.ps1 | iex
```

---

## Agent 系统服务管理

Agent 内置跨平台服务管理子命令（`service`），支持通过命令行直接控制守护进程生命周期：

```sh
# 查看当前服务运行状态
homeagent-agent service status

# 启动 / 停止 / 重启服务
homeagent-agent service start
homeagent-agent service stop
homeagent-agent service restart

# 安装为系统开机自启服务
homeagent-agent service install --server http://<server-ip>:8080 --token <join-token>

# 卸载系统服务
homeagent-agent service uninstall

# 在前台直接以守护模式运行（用于调试）
homeagent-agent service run --server http://<server-ip>:8080 --token <join-token>
```

---

## ACL 权限控制

ACL 配置文件 `acl.yaml` 存放于 Server 的数据目录下（可选，不存在时默认采用 `allow_all`）：

```yaml
default_policy: deny
devices:
  ubuntu-server:
    allow:
      - macbook-roki
```

该配置表示：“仅允许 `macbook-roki` 通过 SSH 访问 `ubuntu-server`”。HomeAgent 管理公钥始终保留，不受 ACL 条目限制。

---

## CLI 日常运维

```sh
# 列出所有已接入设备及其同步状态
HOMEAGENT_JOIN_TOKEN=... homeagent-server devices --data-dir /var/lib/homeagent

# 手动触发向所有设备推送最新公钥配置
HOMEAGENT_JOIN_TOKEN=... homeagent-server sync --data-dir /var/lib/homeagent

# 触发向指定设备推送公钥配置
HOMEAGENT_JOIN_TOKEN=... homeagent-server sync --data-dir /var/lib/homeagent DEVICE_ID

# 测试 Server 到指定设备的 SSH 连通性
HOMEAGENT_JOIN_TOKEN=... homeagent-server ssh-test --data-dir /var/lib/homeagent DEVICE_ID
```

---

## 深入设计与文档

详细的 SSE 实时控制平面协议、跨平台守护进程状态流转图与安全审计说明，请参阅本地架构设计文档：
- `docs/sse-push-control-plane-architecture.md`
