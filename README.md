# HomeAgent

HomeAgent 是一个基于 Go 的轻量级控制平面，用于接入家庭局域网中的可信设备，并根据设备 ACL 同步 SSH 公钥。MVP 使用 JSON 文件和系统自带的 OpenSSH 工具；不会存储设备私钥，也不会覆盖用户自行管理的 `authorized_keys` 条目。

## 构建

需要 Go 1.26 或更高版本。

```sh
go test ./...
go build -o bin/homeagent-server ./cmd/homeagent-server
go build -o bin/homeagent-agent ./cmd/homeagent-agent
```

为 Server 的下载目录构建各平台 Agent：

```sh
mkdir -p dist
GOOS=darwin GOARCH=arm64 go build -o dist/homeagent-agent-darwin-arm64 ./cmd/homeagent-agent
GOOS=darwin GOARCH=amd64 go build -o dist/homeagent-agent-darwin-amd64 ./cmd/homeagent-agent
GOOS=linux GOARCH=arm64 go build -o dist/homeagent-agent-linux-arm64 ./cmd/homeagent-agent
GOOS=linux GOARCH=amd64 go build -o dist/homeagent-agent-linux-amd64 ./cmd/homeagent-agent
GOOS=windows GOARCH=arm64 go build -o dist/homeagent-agent-windows-arm64.exe ./cmd/homeagent-agent
GOOS=windows GOARCH=amd64 go build -o dist/homeagent-agent-windows-amd64.exe ./cmd/homeagent-agent
```

## 运行 Server

```sh
HOMEAGENT_JOIN_TOKEN='replace-with-a-random-secret' \
homeagent-server serve \
  --listen :8080 \
  --data-dir "$HOME/Library/Application Support/HomeAgent" \
  --downloads-dir ./dist \
  --scripts-dir ./scripts
```

系统级安装建议使用 `/var/lib/homeagent` 作为 `--data-dir`。首次启动时，Server 会创建一个权限受限的 Ed25519 管理密钥。运行时配置可通过命令行参数或对应的 `HOMEAGENT_*` 环境变量传入；[configs/config.example.yaml](configs/config.example.yaml) 记录了部署配置值。

数据目录包含 `devices.json`、`acl.yaml`、`keys/admin_ed25519` 和 `ssh/known_hosts`。严禁复制或发布 `keys/admin_ed25519`。

ACL 文件是可选的；文件不存在时默认采用 `allow_all`：

```yaml
default_policy: deny
devices:
  ubuntu-server:
    allow:
      - macbook-roki
```

该映射表示“哪些来源设备可以通过 SSH 访问此目标设备”。HomeAgent 管理公钥独立于设备 ACL 条目，并始终保留。

## 接入设备

macOS 或 Linux：

```sh
curl -fsSL http://macmini.local:8080/install.sh -o /tmp/homeagent-install.sh
HOMEAGENT_SERVER=http://macmini.local:8080 \
HOMEAGENT_JOIN_TOKEN='replace-with-a-random-secret' \
sh /tmp/homeagent-install.sh
```

Windows PowerShell（请以管理员权限运行，因为 OpenSSH 服务和 `Program Files` 目录的修改可能需要提升权限）：

```powershell
$env:HOMEAGENT_SERVER="http://macmini.local:8080"
$env:HOMEAGENT_JOIN_TOKEN="replace-with-a-random-secret"
irm http://macmini.local:8080/install.ps1 | iex
```

Agent 会复用 `~/.ssh/id_ed25519`；如果该密钥不存在，则自动创建。Agent 只发送公钥。`apply-keys` 仅以原子方式更新以下标记限定的区块：

```text
# BEGIN HOMEAGENT MANAGED
# END HOMEAGENT MANAGED
```

## 日常操作

```sh
HOMEAGENT_JOIN_TOKEN=... homeagent-server devices --data-dir /var/lib/homeagent
HOMEAGENT_JOIN_TOKEN=... homeagent-server sync --data-dir /var/lib/homeagent
HOMEAGENT_JOIN_TOKEN=... homeagent-server sync --data-dir /var/lib/homeagent DEVICE_ID
HOMEAGENT_JOIN_TOKEN=... homeagent-server ssh-test --data-dir /var/lib/homeagent DEVICE_ID
```

HTTP 接口包括 `/health`、`/api/v1/bootstrap/admin-key`、设备注册/列表/查询/删除、单设备同步以及全局同步。所有 `/api/v1` 接口都需要使用 Join Token 作为 Bearer 凭据。设备信息持久化写入 Registry 后，Server 即返回注册响应；SSH 同步异步执行，因此单台设备离线不会导致新设备注册失败。

设备 Host Key 保存在 HomeAgent 独立的 `known_hosts` 文件中。已有 Host Key 不会被静默替换；Host Key 发生变化时，OpenSSH 校验会失败，并要求人工检查。
