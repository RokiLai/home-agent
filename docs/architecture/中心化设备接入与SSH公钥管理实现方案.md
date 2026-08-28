# HomeAgent 中心化设备接入与 SSH 公钥管理实现方案

## 1. 目标

在家庭局域网中，以 Mac mini 作为中心控制节点，实现：

1. 新设备通过一次安装命令自动加入 HomeAgent。
2. 新设备自动生成或复用 SSH Key。
3. 新设备将自己的 SSH 公钥注册到 Mac mini。
4. Mac mini 统一保存设备信息和公钥。
5. Mac mini 根据设备 ACL 自动将对应公钥分发到其他设备。
6. 新设备接入后，无需人工逐台修改 `authorized_keys`。
7. 支持 Mac、Linux、Windows。
8. 支持未来扩展：
   - Git 配置同步
   - 软件安装
   - AI Agent 安装
   - 配置文件同步
   - 远程命令执行
   - 设备发现
   - 指定设备之间通信

第一版只实现：

```text
设备注册
SSH Key 管理
authorized_keys 同步
设备 ACL
基础设备状态
```

不实现：

```text
MQ
复杂事件总线
Web UI
数据库
TLS PKI
自动服务发现
远程 Shell 管理平台
```

---

# 2. 整体架构

```text
                        Mac mini

                  ┌──────────────────┐
                  │ homeagent-server │
                  ├──────────────────┤
                  │ Device Registry  │
                  │ SSH Key Store    │
                  │ ACL              │
                  │ Sync Controller  │
                  └────────┬─────────┘
                           │
                    SSH 管理连接
                           │
          ┌────────────────┼────────────────┐
          │                │                │
          ▼                ▼                ▼
      MacBook           Ubuntu          Windows
   homeagent-agent   homeagent-agent   homeagent-agent
```

Mac mini 是：

```text
控制平面
配置源
设备注册中心
SSH 公钥管理中心
```

各设备负责：

```text
生成本机 SSH Key
注册设备
上报基本信息
接受 Mac mini SSH 管理
```

---

# 3. 技术选型

建议统一使用 Go。

原因：

```text
跨平台
单二进制
依赖少
资源占用低
Mac/Linux/Windows 均可运行
后续容易扩展
```

项目建议：

```text
homeagent/
├── cmd/
│   ├── homeagent-server/
│   └── homeagent-agent/
│
├── internal/
│   ├── device/
│   ├── registry/
│   ├── auth/
│   ├── acl/
│   ├── sshsync/
│   └── api/
│
├── scripts/
│   ├── install.sh
│   └── install.ps1
│
├── configs/
│   └── config.example.yaml
│
├── go.mod
└── README.md
```

第一版可以是：

```text
一个 Git 仓库
两个可执行文件
```

生成：

```text
homeagent-server
homeagent-agent
```

---

# 4. Mac mini 中心服务

运行：

```bash
homeagent-server
```

默认监听：

```text
0.0.0.0:8080
```

配置：

```yaml
server:
  listen: ":8080"

storage:
  data_dir: "/var/lib/homeagent"

ssh:
  private_key: "/var/lib/homeagent/keys/admin_ed25519"
  connect_timeout: "5s"

auth:
  join_token: "CHANGE_ME"

sync:
  enabled: true
```

Mac 开发阶段可以使用：

```text
~/Library/Application Support/HomeAgent/
```

正式运行建议：

```text
/var/lib/homeagent/
```

---

# 5. 数据目录

Mac mini：

```text
/var/lib/homeagent/
├── devices.json
├── acl.yaml
├── keys/
│   ├── admin_ed25519
│   └── admin_ed25519.pub
└── logs/
```

第一版不使用数据库。

---

# 6. Mac mini 管理 Key

首次启动：

```text
如果 admin_ed25519 不存在
        ↓
自动生成
```

执行等价于：

```bash
ssh-keygen \
  -t ed25519 \
  -f /var/lib/homeagent/keys/admin_ed25519 \
  -N ""
```

产生：

```text
admin_ed25519
admin_ed25519.pub
```

私钥只能存储在 Mac mini。

禁止：

```text
上传
同步
提交 Git
返回 API
复制给普通设备
```

权限：

```bash
chmod 600 admin_ed25519
chmod 644 admin_ed25519.pub
```

---

# 7. 设备模型

定义：

```go
type Device struct {
    ID         string    `json:"id"`
    Hostname   string    `json:"hostname"`
    OS         string    `json:"os"`
    Arch       string    `json:"arch"`
    SSHUser    string    `json:"ssh_user"`
    SSHPort    int       `json:"ssh_port"`
    PublicKey  string    `json:"public_key"`
    Addresses  []string  `json:"addresses"`
    LastSeenAt time.Time `json:"last_seen_at"`
    CreatedAt  time.Time `json:"created_at"`
    UpdatedAt  time.Time `json:"updated_at"`
}
```

示例：

```json
{
  "id": "laptop-example",
  "hostname": "Example-Laptop",
  "os": "darwin",
  "arch": "arm64",
  "ssh_user": "exampleuser",
  "ssh_port": 22,
  "public_key": "ssh-ed25519 AAAAC3...",
  "addresses": [
    "192.168.50.20",
    "fd00::20"
  ],
  "last_seen_at": "2026-08-16T17:00:00Z"
}
```

---

# 8. Device ID

不要直接把 IP 当设备 ID。

默认生成：

```text
hostname + machine_id
```

最终：

```text
SHA256(hostname + machine_id)
```

截取：

```text
16 个字符
```

例如：

```text
example-laptop-a18f92bd
```

machine ID 获取方式：

Linux：

```text
/etc/machine-id
```

macOS：

```bash
ioreg -rd1 -c IOPlatformExpertDevice
```

读取：

```text
IOPlatformUUID
```

Windows：

```powershell
Get-CimInstance Win32_ComputerSystemProduct
```

读取：

```text
UUID
```

---

# 9. 注册 API

接口：

```http
POST /api/v1/devices/register
```

Header：

```http
Authorization: Bearer <JOIN_TOKEN>
```

Request：

```json
{
  "id": "ubuntu-server-a83c9012",
  "hostname": "ubuntu-server",
  "os": "linux",
  "arch": "amd64",
  "ssh_user": "exampleuser",
  "ssh_port": 22,
  "public_key": "ssh-ed25519 AAAA...",
  "addresses": [
    "192.168.50.30"
  ]
}
```

Response：

```json
{
  "success": true,
  "device_id": "ubuntu-server-a83c9012"
}
```

注册成功后：

```text
1. 保存设备
2. 更新 addresses
3. 更新 public_key
4. 更新 last_seen_at
5. 触发 SSH Key Sync
```

---

# 10. JOIN TOKEN

必须存在基本注册认证。

配置：

```yaml
auth:
  join_token: "xxxxx"
```

Agent 请求：

```http
Authorization: Bearer xxxxx
```

Token 不负责设备间 SSH 身份认证。

它只负责：

```text
允许新设备加入 HomeAgent
```

Token 错误：

```http
401 Unauthorized
```

---

# 11. Agent

运行：

```bash
homeagent-agent join
```

参数：

```bash
homeagent-agent join \
    --server http://macmini.local:8080 \
    --token xxxx
```

流程：

```text
读取 hostname
      ↓
读取 OS / Arch
      ↓
计算 device ID
      ↓
读取 SSH 用户
      ↓
检查 ~/.ssh/id_ed25519
      ↓
不存在则生成
      ↓
读取 public key
      ↓
获取局域网地址
      ↓
调用 register API
      ↓
安装 HomeAgent Admin 公钥
      ↓
等待 Server SSH 测试
      ↓
注册完成
```

---

# 12. Agent SSH Key

默认优先复用：

```text
~/.ssh/id_ed25519
```

如果不存在：

```bash
ssh-keygen \
  -t ed25519 \
  -f ~/.ssh/id_ed25519 \
  -N ""
```

不允许 Agent 上传私钥。

只上传：

```text
~/.ssh/id_ed25519.pub
```

---

# 13. 首次建立中心管理权限

新设备需要将：

```text
HomeAgent Admin Public Key
```

加入：

```text
~/.ssh/authorized_keys
```

Server 提供：

```http
GET /api/v1/bootstrap/admin-key
```

需要：

```text
JOIN TOKEN
```

返回：

```json
{
  "public_key": "ssh-ed25519 AAAA... homeagent-admin"
}
```

Agent 将其写入：

```text
authorized_keys
```

---

# 14. authorized_keys 管理方式

严禁直接覆盖整个文件。

统一使用 Managed Block：

```text
# BEGIN HOMEAGENT MANAGED
ssh-ed25519 AAAA... laptop-example
ssh-ed25519 BBBB... ubuntu-server
# END HOMEAGENT MANAGED
```

例如原文件：

```text
ssh-ed25519 XXX... my-personal-key

# BEGIN HOMEAGENT MANAGED
ssh-ed25519 AAAA... device-a
# END HOMEAGENT MANAGED

ssh-ed25519 YYY... another-key
```

同步后：

```text
ssh-ed25519 XXX... my-personal-key

# BEGIN HOMEAGENT MANAGED
ssh-ed25519 AAAA... device-a
ssh-ed25519 BBBB... device-b
# END HOMEAGENT MANAGED

ssh-ed25519 YYY... another-key
```

禁止修改：

```text
Managed Block 外部内容
```

---

# 15. 公钥同步算法

设备注册后执行：

```text
Register(device D)

        ↓

Registry.Save(D)

        ↓

读取所有 devices

        ↓

读取 ACL

        ↓

计算每台设备 allowed public keys

        ↓

生成 authorized_keys Managed Block

        ↓

Mac mini SSH 到目标设备

        ↓

更新 Managed Block

        ↓

验证 authorized_keys
```

伪代码：

```go
func SyncAllDevices() error {
    devices := registry.List()

    for _, target := range devices {
        allowedDevices := acl.Resolve(target.ID, devices)

        keys := make([]string, 0)

        for _, d := range allowedDevices {
            keys = append(keys, d.PublicKey)
        }

        if err := sshSync.UpdateAuthorizedKeys(target, keys); err != nil {
            log.Error(err)
            continue
        }
    }

    return nil
}
```

---

# 16. ACL

配置：

```yaml
default_policy: allow_all
```

第一版默认：

```text
allow_all
```

即：

```text
所有已注册设备互相信任
```

设备数量较少时最简单。

完整配置：

```yaml
default_policy: deny

devices:

  laptop-example:
    allow:
      - ubuntu-server
      - windows-pc

  ubuntu-server:
    allow:
      - laptop-example

  windows-pc:
    allow:
      - laptop-example
```

语义：

```text
target device → 哪些设备可以 SSH 到 target
```

例如：

```yaml
ubuntu-server:
  allow:
    - laptop-example
```

表示：

```text
ubuntu-server authorized_keys
```

包含：

```text
laptop-example.public_key
```

---

# 17. ACL 默认模式

MVP：

```yaml
default_policy: allow_all
```

以后安全需求提高后切换：

```yaml
default_policy: deny
```

不要第一版就实现复杂：

```text
Group
Role
RBAC
ABAC
Policy Engine
```

---

# 18. SSH Push

Mac mini 使用：

```text
admin_ed25519
```

连接：

```text
ssh_user@device_ip
```

Linux/macOS：

```bash
ssh \
  -i /var/lib/homeagent/keys/admin_ed25519 \
  exampleuser@192.168.50.30
```

Windows：

```bash
ssh \
  -i /var/lib/homeagent/keys/admin_ed25519 \
  Administrator@192.168.50.40
```

使用系统 OpenSSH 即可。

第一版不要引入自定义 SSH 协议。

---

# 19. SSH Host Key

不要使用：

```text
StrictHostKeyChecking=no
```

长期方案。

HomeAgent 使用独立 known_hosts：

```text
/var/lib/homeagent/ssh/known_hosts
```

第一次加入设备时：

```bash
ssh-keyscan <IP>
```

将 Host Key 保存。

之后发生变化：

```text
拒绝连接
记录错误
要求人工确认
```

允许 Bootstrap 阶段明确执行：

```text
accept-new
```

但禁止永久禁用检查。

---

# 20. IP 地址选择

Agent 上报：

```text
全部局域网候选地址
```

包括：

```text
IPv4
IPv6
```

过滤：

```text
127.0.0.1
::1
169.254.0.0/16
fe80::/10
Docker bridge
VM bridge
```

第一版 Server 选择：

```text
优先 RFC1918 IPv4
```

例如：

```text
192.168.x.x
10.x.x.x
172.16-31.x.x
```

如果没有 IPv4：

```text
再尝试 IPv6
```

---

# 21. Address 探测

不要直接信任 Agent 上报地址一定可用。

Server 按顺序测试：

```text
TCP connect IP:SSH_PORT
```

例如：

```text
192.168.50.20:22
```

超时：

```text
1~3 秒
```

第一个连通地址作为：

```text
PreferredAddress
```

可以仅保存在运行时。

---

# 22. 心跳

Agent 后续增加：

```http
POST /api/v1/devices/heartbeat
```

Request：

```json
{
  "device_id": "ubuntu-server-a83c9012",
  "addresses": [
    "192.168.50.31"
  ]
}
```

更新：

```text
addresses
last_seen_at
```

第一版建议：

```text
60 秒
```

一次。

如果 Agent 暂时不做常驻进程，也可以第一版只实现：

```text
join
```

以后再增加 heartbeat。

---

# 23. 删除设备

API：

```http
DELETE /api/v1/devices/{id}
```

删除流程：

```text
删除 Registry 记录
      ↓
执行 SyncAllDevices
      ↓
从所有设备 authorized_keys
移除该设备 Public Key
```

保证设备删除后：

```text
无法继续 SSH 到其他 HomeAgent 设备
```

---

# 24. 更新公钥

如果某设备：

```text
id_ed25519
```

发生变化：

```text
Agent 再次 Register
```

Server 根据：

```text
Device ID
```

识别为同一设备。

更新：

```text
public_key
```

然后：

```text
SyncAllDevices()
```

旧 Key 自动从所有 Managed Block 删除。

---

# 25. devices.json

示例：

```json
{
  "devices": [
    {
      "id": "laptop-example-a18f92bd",
      "hostname": "Example-Laptop",
      "os": "darwin",
      "arch": "arm64",
      "ssh_user": "exampleuser",
      "ssh_port": 22,
      "public_key": "ssh-ed25519 AAAA...",
      "addresses": [
        "192.168.50.20"
      ],
      "created_at": "2026-08-16T09:00:00Z",
      "updated_at": "2026-08-16T09:00:00Z",
      "last_seen_at": "2026-08-16T09:00:00Z"
    }
  ]
}
```

写入使用：

```text
临时文件
        ↓
fsync
        ↓
atomic rename
```

防止程序异常导致 JSON 损坏。

---

# 26. HTTP API

第一版接口：

```text
GET    /health

GET    /api/v1/bootstrap/admin-key

POST   /api/v1/devices/register

GET    /api/v1/devices

GET    /api/v1/devices/{id}

DELETE /api/v1/devices/{id}

POST   /api/v1/devices/{id}/sync

POST   /api/v1/sync
```

后续：

```text
POST /heartbeat
```

---

# 27. health

```http
GET /health
```

返回：

```json
{
  "status": "ok"
}
```

---

# 28. 注册脚本

## macOS / Linux

使用：

```bash
curl -fsSL http://macmini.local:8080/install.sh | sh
```

实际为了传 Token，推荐：

```bash
HOMEAGENT_SERVER=http://macmini.local:8080 \
HOMEAGENT_JOIN_TOKEN=xxxx \
curl -fsSL http://macmini.local:8080/install.sh | sh
```

更建议：

```bash
curl -fsSL http://macmini.local:8080/install.sh -o /tmp/homeagent-install.sh

HOMEAGENT_SERVER=http://macmini.local:8080 \
HOMEAGENT_JOIN_TOKEN=xxxx \
sh /tmp/homeagent-install.sh
```

---

# 29. install.sh 职责

只做：

```text
识别 OS / Arch
下载对应 homeagent-agent
安装二进制
执行 join
```

例如：

```text
/usr/local/bin/homeagent-agent
```

不要在 Shell 中实现业务逻辑。

业务全部放到：

```text
homeagent-agent
```

---

# 30. Windows 安装

PowerShell：

```powershell
$env:HOMEAGENT_SERVER="http://macmini.local:8080"
$env:HOMEAGENT_JOIN_TOKEN="xxxx"

irm http://macmini.local:8080/install.ps1 | iex
```

安装：

```text
C:\Program Files\HomeAgent\homeagent-agent.exe
```

---

# 31. Windows SSH

要求 Windows 启用：

```text
OpenSSH Server
```

Agent 检测：

```powershell
Get-Service sshd
```

不存在：

```text
提示用户安装
```

存在但未启动：

```powershell
Start-Service sshd
Set-Service sshd -StartupType Automatic
```

是否自动开启 SSH Server：

```text
通过配置控制
```

MVP 可以自动启用。

---

# 32. Windows authorized_keys

普通用户：

```text
C:\Users\<user>\.ssh\authorized_keys
```

Administrator 可能使用：

```text
C:\ProgramData\ssh\administrators_authorized_keys
```

因此 Agent 必须判断：

```text
当前 SSH 用户
是否属于 Administrators
Windows OpenSSH 配置
```

不要假定永远是：

```text
~/.ssh/authorized_keys
```

---

# 33. Server 同步方式

建议 SSH 执行：

```text
homeagent-agent apply-keys
```

而不是 Server 自己写一套：

```text
Linux sed
macOS sed
Windows PowerShell
```

统一设计：

```text
Mac mini
    │
    │ ssh
    ▼
目标设备

homeagent-agent apply-keys
```

Server 将 Key 内容通过 stdin 传入。

例如：

```bash
cat keys.json | ssh device homeagent-agent apply-keys
```

这样：

```text
authorized_keys
```

具体路径和操作系统差异完全由 Agent 处理。

这是第一版中非常重要的职责边界。

---

# 34. apply-keys

输入：

```json
{
  "keys": [
    {
      "device_id": "laptop-example",
      "public_key": "ssh-ed25519 AAAA..."
    },
    {
      "device_id": "ubuntu-server",
      "public_key": "ssh-ed25519 BBBB..."
    }
  ]
}
```

执行：

```text
读取 authorized_keys
      ↓
找到 HOMEAGENT Managed Block
      ↓
替换 Block
      ↓
不存在则追加
      ↓
原子写入
      ↓
设置正确权限
```

---

# 35. 文件权限

Linux/macOS：

```bash
chmod 700 ~/.ssh
chmod 600 ~/.ssh/authorized_keys
```

Windows：

使用 PowerShell / Go Windows API 保证 OpenSSH 能正确读取。

不要直接套 Unix 权限逻辑。

---

# 36. 同步幂等性

必须满足：

连续执行：

```bash
homeagent-server sync
homeagent-server sync
homeagent-server sync
```

最终结果完全一致。

禁止：

```text
重复追加 Key
Managed Block 重复
文件不断增长
```

---

# 37. 并发控制

同一目标设备：

```text
同时只能执行一个 Sync
```

Server 使用：

```text
per-device mutex
```

避免：

```text
注册事件
heartbeat
手动 sync
```

同时修改同一设备。

---

# 38. Sync 失败处理

例如：

```text
ubuntu offline
```

不能导致：

```text
整个 SyncAllDevices 失败
```

结果：

```text
MacBook   OK
Ubuntu    FAILED
Windows   OK
```

日志：

```text
device_id
address
operation
error
```

以后 heartbeat 或下一次注册时：

```text
自动再次同步
```

---

# 39. 日志

统一结构化日志。

例如：

```json
{
  "level": "info",
  "event": "device_registered",
  "device_id": "ubuntu-server",
  "hostname": "ubuntu-server"
}
```

同步：

```json
{
  "level": "info",
  "event": "ssh_keys_synced",
  "target_device": "ubuntu-server",
  "key_count": 3
}
```

失败：

```json
{
  "level": "error",
  "event": "ssh_sync_failed",
  "target_device": "windows-pc",
  "error": "connection refused"
}
```

不要输出：

```text
private key
join token
完整 Authorization Header
```

---

# 40. Server CLI

支持：

```bash
homeagent-server serve
```

以及：

```bash
homeagent-server devices
```

输出：

```text
ID                HOSTNAME       ADDRESS          OS
laptop-example    Laptop         192.168.50.20    darwin
ubuntu-server     Ubuntu         192.168.50.30    linux
windows-pc        EXAMPLE-PC     192.168.50.40    windows
```

手动同步：

```bash
homeagent-server sync
```

指定：

```bash
homeagent-server sync ubuntu-server
```

---

# 41. Agent CLI

至少：

```text
homeagent-agent join

homeagent-agent info

homeagent-agent apply-keys
```

以后：

```text
homeagent-agent daemon
```

---

# 42. 注册完整时序

```text
New Device                       Mac mini
    │                                │
    │ GET admin-key                  │
    │───────────────────────────────>│
    │                                │
    │ admin public key               │
    │<───────────────────────────────│
    │                                │
    │ add authorized_keys            │
    │                                │
    │ POST /devices/register         │
    │ public key + IP + device info  │
    │───────────────────────────────>│
    │                                │
    │ save registry                  │
    │                                │
    │ SSH new-device                 │
    │<───────────────────────────────│
    │                                │
    │ execute apply-keys             │
    │<───────────────────────────────│
    │                                │
    │                                │──SSH──> Device A
    │                                │──SSH──> Device B
    │                                │──SSH──> Device C
    │                                │
    │ register success               │
    │<───────────────────────────────│
```

---

# 43. 一个重要调整

注册 API 不应该等待：

```text
所有设备同步成功
```

否则其中一台设备离线：

```text
新设备注册也失败
```

正确设计：

```text
保存注册信息
      ↓
立即返回注册成功
      ↓
异步触发本进程内 Sync
```

这里的“异步”仅指：

```text
Go goroutine / 内部任务
```

不引入：

```text
Redis
MQ
Kafka
RabbitMQ
```

进程重启后可以：

```text
启动时 SyncAllDevices()
```

恢复一致性。

---

# 44. Mac mini 自己也作为 Device

推荐 Mac mini 同样注册为：

```text
Device
```

它有两类 Key：

```text
admin key
```

用于：

```text
HomeAgent 管理其他设备
```

以及：

```text
normal device key
```

用于：

```text
Mac mini 作为普通设备参与 SSH
```

两者严格分开。

---

# 45. 安全边界

第一版虽然用于家庭网络，也必须遵守：

### Admin Private Key

只存在 Mac mini。

### Device Private Key

只存在对应设备。

### Join Token

只负责允许注册。

### authorized_keys

只修改 HomeAgent Managed Block。

### Server API

默认只监听：

```text
LAN
```

不要直接暴露公网。

---

# 46. API 网络

如果 Mac mini IP：

```text
192.168.50.10
```

则访问：

```text
http://192.168.50.10:8080
```

也可以使用：

```text
http://macmini.local:8080
```

第一版直接依赖系统：

```text
mDNS / Bonjour
```

解析 `.local`。

不需要自己实现服务发现。

---

# 47. 第一版不做自动发现

即：

```text
homeagent-agent
```

需要知道：

```text
http://macmini.local:8080
```

以后再实现：

```text
_homeagent._tcp.local
```

自动发现。

---

# 48. 第一阶段验收场景

至少准备：

```text
Mac mini
MacBook
Ubuntu
Windows
```

---

# 49. 测试场景 1：首台设备

MacBook：

```bash
homeagent-agent join
```

确认：

```text
MacBook 注册成功
Mac mini 能 SSH MacBook
```

---

# 50. 测试场景 2：第二台设备

Ubuntu：

```bash
homeagent-agent join
```

之后：

MacBook：

```bash
ssh ubuntu-server
```

Ubuntu：

```bash
ssh macbook
```

均无需密码。

---

# 51. 测试场景 3：新增 Windows

Windows 注册。

验证：

```text
MacBook → Windows
Ubuntu → Windows
Windows → MacBook
Windows → Ubuntu
```

根据 ACL 均可工作。

---

# 52. 测试场景 4：删除设备

执行：

```bash
homeagent-server device delete ubuntu-server
```

然后：

```text
ubuntu-server public key
```

必须从：

```text
MacBook
Windows
Mac mini
```

Managed Block 中删除。

---

# 53. 测试场景 5：设备离线

关闭 Windows。

注册新设备。

期望：

```text
MacBook sync OK
Ubuntu sync OK
Windows sync FAILED
```

但：

```text
新设备注册成功
```

Windows 恢复后：

```bash
homeagent-server sync windows-pc
```

同步成功。

---

# 54. 测试场景 6：重复注册

同一设备连续执行：

```bash
homeagent-agent join
homeagent-agent join
homeagent-agent join
```

结果：

```text
Registry 只有一个 Device
authorized_keys 不存在重复 Key
Managed Block 只有一个
```

---

# 55. 测试场景 7：保护用户 Key

设备原：

```text
ssh-ed25519 XXX personal
```

HomeAgent 同步后必须仍存在：

```text
ssh-ed25519 XXX personal
```

只能修改：

```text
BEGIN HOMEAGENT MANAGED
END HOMEAGENT MANAGED
```

之间内容。

---

# 56. 单元测试要求

至少覆盖：

```text
Device ID 生成
Registry Save
Registry Update
Registry Delete
JSON atomic write
ACL Resolve
Managed Block 创建
Managed Block 更新
Managed Block 删除
重复 Key 去重
非 Managed Key 保留
IP 地址过滤
IP 地址优先级
```

---

# 57. 集成测试

模拟：

```text
3 台 Device
```

执行：

```text
register A
register B
register C
```

验证：

```text
A authorized_keys = B + C
B authorized_keys = A + C
C authorized_keys = A + B
```

在：

```text
default_policy: allow_all
```

情况下成立。

---

# 58. 实现顺序

严格按照以下顺序实现。

## Phase 1：Registry

实现：

```text
Device Model
devices.json
Register
List
Delete
```

验收：

```bash
curl POST /register

curl GET /devices
```

---

## Phase 2：Agent Bootstrap

实现：

```text
device ID
ssh key detection
ssh-keygen
admin key install
register
```

验收：

```bash
homeagent-agent join
```

能够出现在：

```bash
homeagent-server devices
```

---

## Phase 3：SSH 管理连接

实现：

```text
admin key
SSH connect
known_hosts
address probe
```

验收：

Mac mini：

```bash
homeagent-server ssh-test DEVICE_ID
```

成功。

---

## Phase 4：Managed authorized_keys

实现：

```text
homeagent-agent apply-keys
```

包括：

```text
Linux
macOS
Windows
```

---

## Phase 5：自动同步

实现：

```text
SyncDevice
SyncAllDevices
```

注册后自动触发：

```text
SyncAllDevices
```

---

## Phase 6：ACL

实现：

```yaml
default_policy: allow_all
```

以及：

```yaml
devices:
```

覆盖规则。

---

## Phase 7：Installer

实现：

```text
install.sh
install.ps1
```

最终体验：

Mac/Linux：

```bash
curl ... | sh
```

Windows：

```powershell
irm ... | iex
```

---

# 59. MVP 完成标准

满足以下全部条件才算完成：

- [ ] Mac mini 可以运行 `homeagent-server`
- [ ] Server 首次启动自动生成 Admin SSH Key
- [ ] Mac Agent 可以注册
- [ ] Linux Agent 可以注册
- [ ] Windows Agent 可以注册
- [ ] Agent 自动生成或复用 SSH Key
- [ ] Agent 只上传 SSH Public Key
- [ ] Server 保存 Device Registry
- [ ] Server 可以通过 SSH 管理所有 Device
- [ ] Server 可以分发 Device Public Key
- [ ] 使用 Managed Block 管理 `authorized_keys`
- [ ] 不修改 Managed Block 外部内容
- [ ] 新设备加入后自动触发同步
- [ ] 删除设备后自动撤销 Key
- [ ] 重复同步结果幂等
- [ ] 单台设备离线不会影响其他设备
- [ ] 支持 `allow_all`
- [ ] 支持指定 Device ACL
- [ ] Mac mini Admin Key 和普通 Device Key 分离
- [ ] Host Key 检查不永久关闭
- [ ] Join Token 不出现在日志
- [ ] SSH Private Key 不出现在日志/API
- [ ] 相关核心逻辑具有测试

---

# 60. 明确禁止的实现

第一版禁止擅自加入：

```text
Redis
MySQL
PostgreSQL
SQLite
Kafka
RabbitMQ
NATS
etcd
Consul
Kubernetes
复杂 PKI
OAuth
OIDC
Web UI
gRPC
Service Mesh
```

除非现有设计无法满足必要需求，否则不要新增基础设施。

---

# 61. 编码要求

所有文件：

```text
UTF-8
无 BOM
```

保持：

```text
简单
可测试
跨平台
幂等
最小权限
```

---

# 62. AI 实施约束

实施 AI 必须：

1. 先输出计划创建的文件。
2. 按 Phase 顺序实现。
3. 不擅自扩大 MVP 范围。
4. 不为了“架构完整”引入新基础设施。
5. OS 差异集中在 Agent 内，不散落在 Server。
6. Server 不直接实现 Windows/Linux/macOS 三套 `authorized_keys` 修改脚本。
7. 所有 `authorized_keys` 更新必须幂等。
8. 不允许覆盖用户已有 SSH Key。
9. 不允许关闭 SSH Host Key 校验作为最终方案。
10. 不允许存储任何 Device Private Key。
11. 不允许将 Admin Private Key 发给 Agent。
12. 完成一个 Phase 后运行对应测试。
13. 最终必须运行完整测试。
14. 最终列出所有新增和修改文件。
15. 最终给出实际运行命令。

---

# 63. 最终用户体验

Mac mini：

```bash
homeagent-server serve
```

新 Mac/Linux：

```bash
curl -fsSL http://macmini.local:8080/install.sh -o /tmp/homeagent-install.sh

HOMEAGENT_SERVER=http://macmini.local:8080 \
HOMEAGENT_JOIN_TOKEN=xxxx \
sh /tmp/homeagent-install.sh
```

Windows：

```powershell
$env:HOMEAGENT_SERVER="http://macmini.local:8080"
$env:HOMEAGENT_JOIN_TOKEN="xxxx"

irm http://macmini.local:8080/install.ps1 | iex
```

加入完成后：

```text
新设备
   ↓
自动注册
   ↓
Mac mini 获得管理权限
   ↓
自动同步公钥
   ↓
按照 ACL 建立设备间 SSH 信任
```

以后增加新设备时：

```text
执行一次安装命令
```

即可完成接入。

---

# 64. 后续扩展方向

MVP 稳定后，再依次增加：

```text
Phase 8
Agent heartbeat

Phase 9
mDNS 自动发现 HomeAgent Server

Phase 10
设备在线状态

Phase 11
Git 配置同步

Phase 12
软件安装任务

Phase 13
配置文件分发

Phase 14
命令执行

Phase 15
AI Agent 自动安装
```

这些能力继续复用：

```text
Device Registry
+
SSH 管理通道
```

不要重建另一套设备管理体系。

---

# 65. 最终架构原则

HomeAgent 负责：

```text
谁是设备
设备在哪里
设备公钥是什么
谁允许访问谁
如何统一管理设备
```

SSH 负责：

```text
认证
加密
远程执行
文件传输
```

Agent 负责：

```text
处理操作系统差异
修改本机配置
提供统一设备执行接口
```

因此最终职责为：

```text
HomeAgent Server = 控制平面

HomeAgent Agent  = 设备适配层

SSH              = 底层安全执行通道
```

第一版应围绕这个边界实现，不增加额外架构。
