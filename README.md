# HomeAgent

轻量级局域网设备管理与 SSH 公钥分发控制平面。基于 **SSE 实时下行控制流 + 客户端 ACK 收敛 + SSH 兜底同步** 双轨架构，自动化管理可信设备间的 SSH 访问权限。

> 🔒 **安全承诺**：不收集/存储设备私钥；仅以原子方式更新 `authorized_keys` 中的独立受管标记区块，绝不破坏用户已有密钥。

---

## ✨ 核心特性

- ⚡ **毫秒级实时下发**：基于 SSE 实时下发密钥与同步指令，客户端自动 ACK 闭环收敛。
- 🛡️ **跨平台系统服务**：一键安装为原生自启服务（macOS `launchd`、Linux `systemd`、OpenWrt `procd`、Windows Service）。
- 🖥️ **内置 Web 控制台**：内嵌轻量 Dashboard，实时展示拓扑、状态流日志与一键同步。
- 🎯 **精细化 ACL 控制**：声明式 YAML 规则，按需控制设备间单向/双向 SSH 授权。

---

## 🚀 快速上手

### 1. 启动 Server

```sh
# 运行 Server（首次启动需指定管理员密码）
HOMEAGENT_ADMIN_PASSWORD="<设置您的管理员密码>" \
HOMEAGENT_DATA_DIR="/var/lib/homeagent" \
homeagent-server serve --listen :8080
```
> 启动后浏览器访问 `http://<server-ip>:8080/` 登录 Web 控制台并生成短期 Claim Token。

### 2. 安装并接入 Agent

**第一步：安装 Agent 二进制**

- **macOS / Linux / OpenWrt**:
  ```sh
  curl -fsSL https://raw.githubusercontent.com/RokiLai/home-agent/main/scripts/install.sh | sh
  ```
- **Windows (PowerShell 管理员)**:
  ```powershell
  irm https://raw.githubusercontent.com/RokiLai/home-agent/main/scripts/install.ps1 | iex
  ```

**第二步：认领设备并启动自启服务**

```sh
homeagent-agent claim --claim-token "<claim-token>"
homeagent-agent service install
```
> 💡 若连接自建私有服务端，可在 claim 时附加 `--server "https://<your-server-domain>"` 参数。此外，在 Web 控制台的“添加设备”弹窗中也支持复制全自动一步接入命令。

---

## ⚙️ ACL 访问控制

在 Server 数据目录下配置 `acl.yaml`（未配置时默认 `allow_all`）：

```yaml
default_policy: deny
devices:
  ubuntu-server:
    allow:
      - laptop-example  # 仅允许 laptop-example 访问 ubuntu-server
```

---

## 🛠️ 运维速查

### Server 运维命令
```sh
# 设备列表与状态
homeagent-server devices --data-dir /var/lib/homeagent

# 手动触发全量 / 单设备推送
homeagent-server sync --data-dir /var/lib/homeagent [DEVICE_ID]

# 向指定设备下发远程关机控制指令
homeagent-server shutdown <DEVICE_ID|ALIAS>

# 测试到指定设备的 SSH 连通性
homeagent-server ssh-test --data-dir /var/lib/homeagent DEVICE_ID
```

### Agent 守护进程管理
```sh
homeagent-agent service status               # 查看状态
homeagent-agent service start|stop|restart   # 服务启停
homeagent-agent service uninstall            # 卸载系统服务
```

---

## 📦 编译构建与质量门禁 (Go 1.26+)

```sh
go test ./...                                           # 单元测试
go build -o bin/homeagent-server ./cmd/homeagent-server # Server
go build -o bin/homeagent-agent ./cmd/homeagent-agent   # Agent

# 运行本地质量门禁流水线
./scripts/quality-gate.sh --base origin/main --diff-coverage 60
```

---

## 📖 深入文档

- [个人公网服务端部署方案](docs/deployment/个人公网服务端部署方案.md)
- [公网访问认证与设备认领设计方案](docs/deployment/公网访问认证与设备认领设计方案.md)
- [SSE 推送控制平面架构设计](docs/architecture/SSE推送控制平面架构设计.md)
- [设备远程关机设计方案](docs/features/设备远程关机设计方案.md)
- [设备网络唤醒设计方案](docs/features/设备网络唤醒设计方案.md)
- [IPv6 与 DDNS 同步集成方案](docs/features/IPv6与DDNS同步集成方案.md)
- [Agent 自升级设计方案](docs/features/Agent自升级设计方案.md)
- [配置文件示例](configs/config.example.yaml)
