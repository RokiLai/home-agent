# HomeAgent IPv6 地址上报与中心化 DDNS 同步落地方案

## 1. 背景与目标

家庭网络通过运营商 Prefix Delegation（PD）获得的 IPv6 前缀可能动态变化。终端设备在前缀变化后会生成新的 IPv6 地址，旧地址可能在一段过渡期内继续存在并逐步进入 deprecated 或失效状态。如果 DDNS 仍指向旧地址，外部访问将中断。

本方案将 `ipv6-device-registry` 中成熟的 IPv6 状态发现与收敛能力吸收到 HomeAgent，形成以下闭环：

```text
路由器 homeagent-agent 发现并上报当前有效 PD 前缀 ──┐
                                                    ↓
普通设备 homeagent-agent 上报完整 IPv6 地址快照 ──→ homeagent-server
                                                    ↓
                              地址快照 ∩ 当前有效 PD 前缀
                                                    ↓
                              Mac mini 直接更新 DDNS AAAA
                                                    ↓
                              保存 desired/applied 状态闭环
```

目标如下：

1. IPv6 地址或前缀变化后自动更新 DDNS，不依赖人工干预。
2. 使用 HomeAgent 现有设备身份、Agent daemon、认证、服务端注册表和控制台能力。
3. 保留原项目的全量快照、事件防抖、周期心跳、OpenWrt 前缀读取和前缀交集算法。
4. 在双前缀过渡、消息乱序、服务重启和临时故障下最终收敛到正确地址。
5. 路由器和普通设备 Agent 只报告事实，由 Server 决定 DNS 记录和发布地址。

## 2. 范围与非目标

### 2.1 本期范围

- macOS Agent 发现指定物理接口的稳定全局 IPv6 地址。
- Agent 启动、网络变化及周期心跳时向 HomeAgent Server 上报完整地址快照。
- OpenWrt 上的 HomeAgent Agent 查询实时 LAN IPv6 前缀并上报完整前缀快照。
- Server 关联设备地址和所属网络的路由器前缀，筛选有效候选地址。
- Mac mini 上的 Server 直接调用 DNS 服务商 API 更新 AAAA 记录。
- Server 记录 pending、syncing、synced、failed 状态并提供查询能力。
- IPv6 消失时经过宽限期再删除 AAAA 记录。

### 2.2 非目标

- 不实现 RFC 9686 DHCPv6 Address Registration。
- 不由 Agent 直接调用 DNS 服务商 API。
- 不由 Agent 指定或修改域名映射。
- 第一阶段不支持一个设备同时发布多个 AAAA 记录。
- 第一阶段不迁移原项目的 UDP 组播注册协议、独立设备 Secret、独立 daemon 和独立状态存储。

## 3. 原项目能力吸收决策

| 原能力 | 决策 | HomeAgent 落点 | 说明 |
|---|---|---|---|
| 完整地址集合比较 | 吸收 | Agent 地址监视组件 | 使用 level-triggered 全量快照，避免增量事件丢失和乱序 |
| macOS `scutil` 网络事件监听 | 改造后吸收 | `homeagent-agent` daemon | 纳入统一进程生命周期和日志体系 |
| 事件防抖、启动同步、周期心跳 | 吸收 | Agent 地址监视组件 | 防止前缀切换过程中连续无效更新 |
| `ifconfig` IPv6 标志过滤 | 改造后吸收 | 平台地址发现实现 | 保留规则，补充结构化字段和错误处理 |
| 地址规范化、去重、排序 | 吸收 | 公共网络地址领域逻辑 | 使用 `net/netip`，确保快照比较稳定 |
| `reported ∩ active prefixes` | 吸收 | Server 地址协调服务 | Server 使用路由器 Agent 上报的实时 PD 前缀统一计算 |
| OpenWrt `ubus` 前缀查询 | 改造后吸收 | 路由器 Agent PrefixProvider | 必须基于真实 OpenWrt 响应验证和构造测试替身 |
| 前缀 A → A+B → B 测试 | 吸收并扩展 | 跨组件测试 | 作为核心产品契约测试 |
| UDP 组播 Sender/Listener | 不吸收 | 由 HomeAgent HTTP 通道替代 | 避免维护第二套注册传输协议 |
| HMAC、timestamp、nonce 协议 | 不吸收 | 使用 HomeAgent Agent→Server 协议 | 原实现针对无连接 UDP，不再需要第二套协议 |
| 独立设备 Secret 和配置 | 不吸收 | 使用 HomeAgent 身份与配置 | 避免双重设备目录和凭据生命周期 |
| `/tmp` 每设备 JSON 权威存储 | 不吸收 | Server Registry 为权威状态 | 路由器 Agent 只读取并上报当前前缀事实 |
| 独立 CLI、launchd 和二进制 | 不吸收 | 使用 HomeAgent 安装与服务体系 | 减少部署单元和升级路径 |

原项目目前只实现地址注册、实时前缀校验和状态保存，没有实现 DDNS 更新或 desired/applied 收敛。因此 Server 端 DNS Publisher 与闭环调度属于新增能力。

## 4. 目标架构与职责

### 4.1 普通设备 Agent

Agent 只负责报告本机事实：

- 监听指定网络接口状态变化；
- 读取当前 IPv6 地址及状态；
- 排除不可用于 DDNS 的地址；
- 生成规范化完整快照；
- 地址变化时防抖上报；
- 周期强制上报用于状态续期；
- HTTP 失败时重试，但不阻塞现有 SSE 和 SSH 密钥同步能力。

Agent 不负责：

- 判断运营商当前有效 PD 前缀；
- 选择 DNS 记录名称；
- 连接 DNS 服务商；
- 宣称某地址已经发布成功。

### 4.2 路由器 Agent

路由器作为普通 HomeAgent 设备接入 Server，但额外报告其所在网络的前缀事实：

- 通过 OpenWrt `ubus` 查询当前 LAN `ipv6-prefix-assignment`；
- 只上报仍处于 preferred 状态的前缀及其生命周期；
- 使用全量快照、revision、变化防抖和周期心跳；
- 前缀变化时立即上报，不等待普通设备地址变化；
- 不接收其他设备地址，不保存设备注册表，也不持有 DNS 服务商凭据。

### 4.3 Server

Server 是期望状态和同步状态的权威来源：

- 验证设备身份和请求内容；
- 按 revision 防止旧快照覆盖新快照；
- 分别原子保存设备地址快照和路由器前缀快照；
- 根据 `network_id` 将设备与提供前缀的路由器关联；
- 计算设备候选地址与当前 preferred 前缀的交集；
- 根据服务端配置选择设备对应的 DNS 记录；
- 当 desired 状态变化时创建或合并同步任务；
- 通过 DNS Publisher 直接更新、查询或删除 AAAA 记录；
- 执行超时、重试和退避，并保存 selected/applied 地址和 DNS 结果；
- Server 重启后恢复未完成任务并继续收敛。

IPv6 地址上报必须使用独立接口，不能复用当前 `POST /api/v1/devices/register`。现有注册 handler 保存设备后会广播密钥更新并触发全量 SSH 同步，频繁网络心跳会造成无关副作用。

Server 部署在 Mac mini 上，DNS API 凭据只保存在该主机。路由器端不再保留独立 receiver、Registry 或 DDNS 服务。

## 5. 地址发现与选择规则

### 5.1 Agent 候选规则

候选地址必须满足：

- 是 IPv6 global unicast；
- 属于配置的物理接口；
- 接口处于 up 状态；
- 不是 loopback、link-local、multicast、unspecified 或 IPv4-mapped；
- 不是 temporary、deprecated、tentative 或 duplicated；
- 在系统能够提供生命周期时，preferred lifetime 大于零。

第一阶段延续原项目策略，不将 ULA 用于公网 DDNS。后续如果支持内网 DNS，应将 ULA 作为独立地址类型和发布策略，而不是混入公网 AAAA 记录。

Agent 上报全部符合条件的地址，不自行假定固定 `/64` 前缀，也不通过字符串截断判断前缀变化。经过规范化、去重和排序后的地址集合发生变化，即视为网络状态变化。

### 5.2 Server 最终选择规则

对于已关联路由器的网络，Server 计算：

```text
valid_addresses = reported_candidates ∩ active_preferred_lan_prefixes
```

其中 active prefix 必须来自关联路由器 Agent 上报的实时网络状态，不能由 Server 根据最终 IPv6 地址反向推断。未关联路由器的网络使用第 5.3 节的设备候选裁决模式。

若当前 applied 地址仍在有效集合中，Server 优先保持不变，避免双前缀过渡期产生 DNS 抖动；否则按规范化 IPv6 数值升序选取第一个稳定地址。后续可增加显式优先级，但变更规则时必须保持向后兼容并提供迁移策略。

### 5.3 设备期望地址更新修复（2026-10-08）

- 设计状态：用户已确认修订设计并明确授权实施（2026-10-08）。
- 实施状态：本地实现已通过质量门禁，候选 Server v0.6.56；生产未部署。
- 验收状态：本地范围、架构、模块、全量 race、Diff Coverage 80.4%（45/56）及 UTF-8/无 BOM/diff 检查通过；真实上一版本升级和生产验证未执行。
- 变更清单：`changes/fix-device-ipv6-desired-address.yaml`。

责任分类：现有设计仅定义路由器前缀裁决，未覆盖生产网络没有路由器关联的情形；实现允许首次无前缀选择候选，却不更新非空旧期望地址。修订本节补齐契约，实施通过回归测试防止旧地址持续显示已同步。

模式判定使用代码库已有信息：配置 `HOMEAGENT_DDNS_ROUTER_ID` 的 `HOMEAGENT_DDNS_NETWORK_ID`（默认 `home`），以及持久化路由器前缀状态中有 `RouterDeviceID` 的网络，均属于路由器关联模式。首次路由器前缀快照被接受后，该网络进入关联模式；空前缀、过期或重启不能将其降级为无关联模式。配置读取必须独立于旧 Cloudflare 发布器是否启用。除此之外的网络使用设备候选模式。不上线新的配置格式、公开路由或协议字段。

| 模式和输入 | 期望地址 | 域名调和行为 |
| --- | --- | --- |
| 无路由器关联，有有效设备候选 | 保持仍在候选中的旧期望地址，否则按 IPv6 数值升序选择 | 地址变化时经现有绑定任务更新 |
| 无路由器关联，候选为空 | 清空 | 无历史应用为 waiting_report，有历史应用为 stale；保留 DNS |
| 有路由器关联，有有效前缀和候选交集 | 保持仍在交集中的旧期望地址，否则确定性选择 | 地址变化时经现有绑定任务更新 |
| 有路由器关联，前缀缺失、空或过期，或无交集 | 清空 | 安全失败，不发布未裁决地址，保留 DNS |
| 前缀存储读取失败或期望状态落盘失败 | 请求失败，禁止按无关联模式回退 | 不产生错误发布 |
| 旧 revision 或相同 revision 不同内容 | 拒绝，维持原状态 | 无额外任务及发布副作用 |

每次接受有效设备快照（包括候选未变化的心跳）均重新计算期望地址；候选过滤沿用第 5.1 节，不能发布 temporary、deprecated、过期或非公网 IPv6。本修复不改变已部署的无地址 DNS 保留契约，不引入自动删除。`pending` 表示设备快照等待处理，绑定 `synced` 仅表示其最后一次 DNS 应用；期望地址为空时绑定必须通过现有调和转为 waiting_report 或 stale。

上报与前缀重算必须以设备 revision 校验或同一设备状态锁保护快照及期望地址更新，防止旧计算覆盖并发新上报；重启读取持久化快照并在下一次心跳或前缀更新恢复计算。前缀更新要重算同一网络全部设备，其他网络不能受影响。DNS 发布仍由现有绑定 revision 和可恢复任务隔离，不增加第二发布者。

审查结论：公开接口和外部依赖不变；服务端候选版本 v0.6.56，Agent 保持 v0.6.24。用户已确认模式判定、并发处理和测试策略并授权实施。设备快照与前缀重算共用服务端锁，保护候选与期望地址保存；不改变公开接口。

验证记录：新增 `internal/api/device_network_selection_test.go` 先在原实现运行，旧地址替换、空候选、临时地址、过期/空前缀用例失败；实现后真实 HTTP 并发设备/前缀上报、网络隔离、旧 revision 拒绝、持久化重启和无变化心跳恢复测试通过。`cmd/homeagent-server/domain_binding_e2e_test.go` 验证认证 HTTP 上报经实际地址解析器、持久化绑定调和和发布端口收敛，包含空地址无额外发布及发布失败保留旧应用地址的反例。测试替身仅为领域端口记录器，不模拟 Cloudflare 协议；协议 fixture 来源继续使用 Cloudflare 模块现有真实观察说明。

质量门禁证据：基线 `61123b3358b6c69eae1da074b70436efac27efc3`，`./scripts/check-change-scope.sh HEAD changes/fix-device-ipv6-desired-address.yaml` 全部通过，证据目录 `/var/folders/h6/f7zynzb567zcv4979jqzgwh80000gn/T/homeagent-quality-gate-3051464163`；复用该次全量 race 覆盖率执行 `./scripts/check-diff-coverage.sh HEAD 60`，结果 80.4%。范围仅 8 个文件，没有新增依赖或修改 Agent 版本。

外部假设审计：排查时已通过真实 Cloudflare API GET 确认 HTTP 200/success=true，`win.rokilai.online` 为 AAAA、TTL 1、proxied=false，仍指向 `240e:390:9a9:c220:4437:8ac7:efad:3032`；GET 未出现重定向。Windows 上报和内网 SSH 获取的当前稳定地址为 `240e:390:9ad:e4b0:6be4:de9e:64ef:1e56`，直接使用该 IPv6 登录成功，使用域名 SSH 超时。此次修复不修改 Cloudflare HTTP 协议，不用领域记录器代替真实服务验收。生产发布仍须独立授权，上一正式版本升级验收和新地址生产发布均尚未执行。

验收要求：测试先于实现，覆盖两种模式、旧地址失效与保留、候选为空、空/过期前缀、无交集、乱序和重复上报、并发上报与前缀重算、网络隔离及重启恢复。通过真实 HTTP 端口验证上报、持久化、绑定调和和发布请求；复用基于真实 Cloudflare 响应的测试替身并验证发布失败时不标记成功。完成范围、架构、模块测试、全量 race、Diff Coverage 60% 和编码/diff 检查。部署单独确认后，使用上一正式 Server v0.6.55 的真实升级入口验收，再核对 Windows 上报、Cloudflare AAAA、DNS 和 SSH 连通性；未部署前不声明生产故障修复完成。

## 6. 数据模型

### 6.1 Agent 上报地址

```go
type ReportedIPv6Address struct {
    Address        string     `json:"address"`
    Interface      string     `json:"interface"`
    PrefixLength   int        `json:"prefix_length,omitempty"`
    Temporary      bool       `json:"temporary"`
    Deprecated     bool       `json:"deprecated"`
    PreferredUntil *time.Time `json:"preferred_until,omitempty"`
    ValidUntil     *time.Time `json:"valid_until,omitempty"`
}
```

### 6.2 Server 设备网络状态

```go
type DeviceIPv6State struct {
    DeviceID          string                `json:"device_id"`
    NetworkID         string                `json:"network_id"`
    Revision          uint64                `json:"revision"`
    ObservedAt        time.Time             `json:"observed_at"`
    ReportedAddresses []ReportedIPv6Address `json:"reported_addresses"`
    DesiredAddress    string                `json:"desired_address,omitempty"`
    AppliedAddress    string                `json:"applied_address,omitempty"`
    AppliedRevision   uint64                `json:"applied_revision,omitempty"`
    SyncStatus        string                `json:"sync_status"`
    SyncError         string                `json:"sync_error,omitempty"`
    SyncUpdatedAt     time.Time             `json:"sync_updated_at,omitempty"`
}
```

### 6.3 Server 路由器前缀状态

```go
type ReportedIPv6Prefix struct {
    Prefix         string     `json:"prefix"`
    PreferredUntil *time.Time `json:"preferred_until,omitempty"`
    ValidUntil     *time.Time `json:"valid_until,omitempty"`
}

type RouterPrefixState struct {
    RouterDeviceID string               `json:"router_device_id"`
    NetworkID      string               `json:"network_id"`
    Revision       uint64               `json:"revision"`
    ObservedAt     time.Time            `json:"observed_at"`
    LastSeenAt     time.Time            `json:"last_seen_at"`
    Prefixes       []ReportedIPv6Prefix `json:"prefixes"`
}
```

Server 不能永久信任最后一次前缀上报。超过配置的多个心跳周期后，前缀状态标记为 stale：暂停发布新地址，不立即删除已应用的 AAAA，并发出可观测告警。

`SyncStatus` 取值：

- `pending`：Server 已接受新快照，等待同步；
- `syncing`：正在调用 DNS 服务商；
- `synced`：DNS 服务商确认 AAAA 已应用；
- `failed`：最近一次尝试失败，等待重试；
- `grace_period`：当前没有有效地址，处于 AAAA 删除宽限期。

设备原有 `addresses []string` 字段暂时保留，避免擅自改变公开接口。新的 IPv6 DDNS 状态使用独立结构和存储边界，确认兼容策略后再考虑统一数据模型。

## 7. HTTP 协议

### 7.1 Agent → Server：地址快照上报

```http
PUT /api/v1/devices/{device_id}/network-state
Authorization: Bearer <device-credential>
Content-Type: application/json
```

```json
{
  "revision": 18,
  "observed_at": "2026-08-17T12:00:00Z",
  "ipv6_addresses": [
    {
      "address": "2001:db8:1234:10::20",
      "interface": "en0",
      "prefix_length": 64,
      "temporary": false,
      "deprecated": false
    }
  ]
}
```

响应：

```json
{
  "accepted_revision": 18,
  "changed": true,
  "sync_status": "pending"
}
```

约束：

- path 中的设备 ID 必须与认证身份匹配；
- 请求体有明确大小上限；
- 拒绝未知字段和非法 IPv6；
- revision 小于当前值时返回冲突，不覆盖状态；
- revision 相同且快照相同时幂等成功；
- revision 相同但内容不同时拒绝；
- observed_at 仅用于观测，不作为覆盖顺序的唯一依据。

如果 HomeAgent 当前共享 join token 无法证明单设备身份，应先定义注册后设备凭据，再开放写入 path-scoped 设备状态的接口。

### 7.2 路由器 Agent → Server：前缀快照上报

路由器使用 HomeAgent Agent→Server 认证通道上报其负责网络的完整前缀快照：

```http
PUT /api/v1/devices/{router_device_id}/network-prefixes
Authorization: Bearer <device-credential>
Content-Type: application/json
```

```json
{
  "network_id": "home",
  "revision": 31,
  "observed_at": "2026-08-17T12:00:00Z",
  "prefixes": [
    {
      "prefix": "2001:db8:1234:10::/64",
      "preferred_until": "2026-08-18T12:00:00Z",
      "valid_until": "2026-08-19T12:00:00Z"
    }
  ]
}
```

设备凭据必须具有该 `network_id` 的路由器角色，普通设备不得上报网络前缀。前缀 revision 使用与设备地址快照相同的幂等和乱序规则。

### 7.3 Server → DNS 服务商：AAAA 更新

具体协议取决于 DNS 服务商，必须先通过真实请求验证查询、创建、更新、无变化和删除的完整行为。Server 内部使用统一业务接口：

```go
type DNSPublisher interface {
    GetAAAA(ctx context.Context, record string) ([]netip.Addr, error)
    UpsertAAAA(ctx context.Context, record string, address netip.Addr) error
    DeleteAAAA(ctx context.Context, record string) error
}
```

DNS 记录映射只能来自 Server 配置。任何 Agent 上报都不能指定 record、zone 或 DNS 服务商参数。

## 8. 状态机与同步流程

### 8.1 正常地址变化

1. Agent 发现候选集合从 A 变为 A+B。
2. 防抖结束后，上报 revision N+1 和完整集合 `[A, B]`。
3. Server 原子保存快照，将状态置为 `pending`。
4. Server 读取所属网络最新且未过期的路由器前缀快照。
5. Server 计算地址和前缀交集，优先保持仍然有效的 applied 地址，否则确定性选择新地址。
6. 后台 worker 将任务置为 `syncing` 并幂等调用 DNS Publisher。
7. DNS 服务商确认后，Server 写入 `applied` 并置为 `synced`。

### 8.2 前缀过渡 A → A+B → B

该流程必须同时由两类事件触发收敛：

- 普通设备 Agent 地址集合变化触发 Server 重新计算；
- 路由器 Agent 的 PD 前缀快照变化触发 Server 重新计算关联网络内的所有设备。

第二类触发不可省略。可能出现路由器已撤销 A，但终端尚未收到网络事件或仍保留 A 的情况。Server 必须依据新的前缀快照主动排除 A，并更新为 B。

路由器 Agent 上报空前缀或前缀状态过期时，Server 不得将未经验证的候选地址发布到 DNS。短时异常采用保守策略：保持当前 applied 记录并告警；超过明确配置的前缀失效宽限期后，再决定删除 AAAA。

### 8.3 IPv6 消失

1. Agent 上报空候选集合。
2. Server 进入 `grace_period`，保留现有 AAAA，例如 10 分钟。
3. 宽限期内若收到新有效地址，取消删除并正常同步。
4. 宽限期届满仍为空时，Server 通过 DNS Publisher 删除 AAAA。
5. DNS 服务商确认删除后，Server 清空 applied 地址并置为 `synced`。

宽限期必须可配置，避免网络重编号或唤醒过程中的短暂空窗导致 DNS 抖动。

## 9. 幂等、并发与故障恢复

- 每台设备的同步任务串行执行，不允许旧 revision 晚到后覆盖新 revision。
- Server 可以合并同一设备排队中的中间 revision，只需最终收敛到最新期望状态。
- Server 分别保存设备地址和路由器前缀的最新 revision，任一旧快照都不能覆盖新状态。
- DDNS 更新前先比较当前记录，值相同则返回 `unchanged`。
- Server 对网络错误、超时和 DNS 服务商 5xx/限流响应使用带抖动的指数退避。
- 认证失败、非法地址、未知映射等永久错误不得无限重试。
- Server 重启后扫描 `pending`、`syncing`、`failed` 状态并恢复任务；遗留的 `syncing` 按未完成处理。
- Agent 只有在 Server 确认接受快照后，才能将其记为“最后成功上报快照”。发送失败不能提前更新成功基线。

## 10. 安全设计

- 不在代码、示例配置、日志或测试 fixture 中保存真实 Token 和 DNS API Key。
- Agent 只能修改自身设备的网络状态。
- Server 控制设备到 DNS 记录的映射，Agent 请求不能覆盖该映射。
- 路由器使用 HomeAgent 设备凭据上报前缀，Server 必须校验其路由器角色和 `network_id` 权限。
- DNS API Token 仅保存在 Mac mini，并限制为所需 zone 和记录的最小权限。
- 所有请求限制正文大小、地址数量、超时和并发。
- 日志记录 device ID、revision、状态和错误类型，不记录认证凭据。
- 如果未来为 Agent 请求增加独立签名，签名内容必须采用确定性编码，并为防重放状态设置容量上限和清理策略。

## 11. 配置建议

服务端配置示例：

```yaml
ipv6_ddns:
  enabled: true
  empty_address_grace_period: 10m
  sync_timeout: 10s
  retry:
    initial: 2s
    maximum: 5m
  provider: cloudflare
  credentials_file: /path/to/restricted/ddns-credentials
  networks:
    home:
      router_device_id: openwrt-router-ab12cd34
      prefix_state_ttl: 15m
  devices:
    macbook-pro-ab12cd34:
      network_id: home
      record: macbook.example.com
      interface: en0
```

示例不得包含真实凭据。DNS 认证材料应通过 `0600` 权限文件、操作系统凭据设施或环境注入，不应直接放入普通配置文件。

## 12. 模块边界建议

建议新增独立业务模块，避免继续扩大现有 `device`、`daemon` 和 `api` 包的职责：

```text
internal/networkaddr/
  address.go              地址领域模型、规范化与选择
  provider.go             AddressProvider 接口
  provider_darwin.go      macOS 地址发现
  watcher.go              变化事件与防抖协调

internal/devicestate/
  service.go              地址/前缀快照校验、revision 与状态转换
  store.go                Server 权威状态存储接口

internal/ddns/
  service.go              desired/applied 收敛与重试调度
  publisher.go            DNSPublisher 业务接口

internal/prefixstate/
  service.go              网络与路由器关联、前缀 TTL 和地址交集
  provider.go             路由器 Agent PrefixProvider 契约

internal/ddns/providers/<provider>/
  client.go               DNS 服务商真实协议适配
```

模块只通过明确接口交互，不直接依赖其他模块的 `internal` 或 `infrastructure` 实现。迁移原项目代码时应复制并调整到 HomeAgent 的合法模块边界，而不是在 `go.mod` 中依赖 `ipv6-device-registry/internal/...`。

第一阶段不新增第三方依赖。现有 Go 标准库、`net/netip`、系统命令和 HomeAgent HTTP 能力足以实现基础版本。若后续引入 netlink 或 DNS 服务商 SDK，必须先说明必要性并评估跨平台、体积和维护成本。

## 13. 分阶段实施计划

### 阶段 0：真实协议验证与契约冻结

1. 确定目标 OpenWrt 版本、DNS 服务商及 Mac mini 到 DNS API 的连接条件。
2. 在真实路由器和 DNS 服务上观察并保存脱敏后的：
   - `ubus call network.interface.lan status` 完整结构；
   - 前缀 A、A+B、B 三阶段的字段变化；
   - DNS 查询、创建、更新、无变化和删除 AAAA 的完整请求、重定向、响应头与响应体；
   - 认证失败、无有效前缀、DNS 服务失败等反例。
3. 根据真实观察定义 PrefixSnapshot 与 DNSPublisher 契约、错误分类和 fixture。
4. 建立对应 `changes/<task>.yaml`，明确每阶段 `allowed_paths`。

阶段 0 未完成前，不实现 OpenWrt 前缀解析 fixture 和 DDNS fake server，防止测试替身根据预想或当前实现反向构造。

### 阶段 1：普通设备地址发现与上报

1. 迁移地址规范化、过滤和全量快照算法。
2. 迁移 macOS `scutil` 监听、防抖、轮询兜底和心跳。
3. 新增独立 network-state API，不触发 SSH 密钥同步。
4. 增加 revision、幂等和快照持久化。
5. 控制台只读展示 reported 状态。

### 阶段 2：路由器前缀上报与 Server 协调

1. 将经过真实验证的 OpenWrt `ubus` 前缀读取迁入路由器 Agent。
2. 新增 network-prefixes API、路由器角色权限和全量前缀快照存储。
3. 增加 `network_id` 映射、前缀 TTL 和 stale 安全策略。
4. 任一前缀快照变化时重新计算关联网络内全部设备。

### 阶段 3：Server DDNS 状态机

1. 增加 desired/applied 模型和任务恢复。
2. 实现按设备串行、任务合并、超时和指数退避。
3. 增加 IPv6 消失宽限期和删除意图。
4. 使用内存 fake Publisher 验证纯业务状态机；fake 从已冻结的业务契约构造。

### 阶段 4：DNS 服务商接入

1. 在 Mac mini 上实现经过真实响应验证的 DNS Publisher。
2. 实现查询、幂等 Upsert、删除、限流和错误分类。
3. 将凭据限制在目标 zone，并验证日志不会泄露认证信息。
4. 在真实双前缀切换环境执行端到端验收。

### 阶段 5：发布与迁移

1. 将独立 `mac-ipv6-agent` 的配置映射到 HomeAgent 配置。
2. 先部署兼容版本的 Server，再升级路由器 Agent 和普通设备 Agent。
3. 验证 HomeAgent 正常接管后，停止旧 UDP 注册服务。
4. 保留可回滚窗口，确认无旧进程继续发送后再清理旧安装项。
5. 涉及安装或升级时，按“上一正式版本 → 候选版本”从真实用户入口执行完整升级验收。

## 14. 测试与验收矩阵

### 14.1 地址领域测试

- 接受稳定 global unicast IPv6。
- 拒绝 IPv4、IPv4-mapped、loopback、link-local、multicast 和 unspecified。
- 拒绝 temporary、deprecated、tentative 和 duplicated 地址。
- 地址能够规范化、去重并确定性排序。
- 空集合合法并能表达 IPv6 消失。
- 非固定 `/64` 前缀不被错误截断或推断。

### 14.2 Agent 测试

- 启动时强制上报。
- 地址未变化时普通事件不重复上报。
- 心跳即使地址未变化也执行刷新。
- 多个连续网络事件只触发一次防抖上报。
- 上报失败不更新最后成功快照。
- daemon 退出能停止 watcher、timer 和后台任务，不遗留进程。
- 路由器 Agent 能正确上报 A、A+B、B 和空前缀全量快照。
- 路由器 Agent 上报失败时不更新最后成功快照，并按策略重试。

### 14.3 Server API 与状态机测试

- 设备不能写入其他设备状态。
- 旧 revision 不能覆盖新状态。
- 相同 revision、相同快照幂等成功。
- 相同 revision、不同快照安全失败。
- 新快照只触发 IPv6/DDNS 同步，不触发 SSH key SyncAll。
- 连续 revision 能合并并最终应用最新状态。
- Server 重启后恢复未完成任务。
- 永久错误不无限重试，临时错误按策略退避。
- IPv6 空窗在宽限期内不删除 AAAA，超时后才删除。

### 14.4 前缀协调与 DDNS 测试

- A：候选 A、有效前缀 A，发布 A。
- A+B：候选 A+B、有效前缀 A+B，当前 applied 地址仍有效时保持不变。
- B：普通设备尚未上报新快照，但路由器 Agent 只上报 B，Server 必须停止发布 A 并收敛到 B。
- 无交集：不得发布候选地址，进入明确的等待或宽限状态。
- 设备地址或路由器前缀的过期 revision 均不得覆盖新状态。
- 路由器前缀状态 stale 时不得发布未经验证的新地址，也不得立即删除现有 AAAA。
- 重复请求：不得产生不必要的 DDNS 更新。
- DDNS 更新失败：不得谎报 applied。
- DDNS 已更新但响应丢失：重试必须幂等并可恢复真实状态。

### 14.5 真实环境验收

- macOS 睡眠、唤醒和网络切换。
- 运营商前缀 A → A+B → B 的完整过程。
- OpenWrt 重启和 HomeAgent Server 重启。
- 路由器 Agent 临时离线后恢复，验证前缀 stale 策略。
- DNS 权威查询确认 AAAA 与 Server applied 状态一致。
- 从外部 IPv6 网络实际连接更新后的域名。

单元测试、mock、fake server 和覆盖率不能代替这些真实协议与真实网络验证。

## 15. 外部假设审计要求

实施和交付时必须记录：

- OpenWrt 版本、网络接口名称、HomeAgent Server 环境及 DNS 服务商；
- 普通设备 Agent 和路由器 Agent 到 Server、Server 到 DNS 的关键假设；
- 所有真实请求、响应、重定向、响应头或命令退出状态；
- fixture/mock 的真实来源、脱敏方式及与真实服务的已知差异；
- 前缀无交集、认证失败、旧 revision、DDNS 失败等关键反例；
- 上一正式版本到候选版本的安装、升级及回滚验证结果。

## 16. 完成标准

满足以下条件后，才能声明能力完成：

1. Agent 能在目标平台可靠发现地址变化并完成上报。
2. Server 能持久化并恢复 desired/applied 状态，不触发无关 SSH 同步。
3. Server 只发布属于路由器 Agent 当前有效 PD 前缀的稳定地址。
4. 前缀 A → A+B → B、短暂空窗和服务重启均能最终收敛。
5. DDNS 更新结果经过权威 DNS 查询和外部 IPv6 连接验证。
6. 真实协议、反例、测试替身差异和升级验收均有审计记录。
7. 变更范围、架构依赖、新模块测试、全量回归测试和 Diff Coverage 全部通过。
