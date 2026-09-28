# 设备域名绑定与 IPv6 DDNS 协调设计方案

## 1. 状态与范围

- 设计状态：领域模型、控制平面、Cloudflare 直连发布器和本机部署方案已完成审查与实施；存量生产域名迁移仍受第 8、9.2 和 10 节门禁约束。
- 实施状态：第一阶段已以 `4dad89c` 提交，第二阶段已以 `c174d05` 提交，第三阶段已以 `1aa3141` 提交；重新启用调和修复已以 `db99700` 提交。本机 Server 已从 `v0.6.35` 分阶段部署至 `v0.6.39`，首个存量域名 `clash.rokilai.online` 当前由 `server/local-server` 绑定接管，ddns-go 不再管理该域名。
- 验收状态：第一、二阶段本地质量门禁均已通过；第三阶段变更范围、架构依赖、模块测试、全量 `-race` 回归和 Diff Coverage `60.2%` 已通过；重新启用修复的同类门禁全部通过，Diff Coverage 为 `100%`。真实账户 Token 已验证 Zone/DNS 读写、PATCH 属性保留、权威 DNS 与公共递归 DNS 收敛以及临时记录清理；本机 Server `v0.6.39` 的 LaunchAgent、`/health`、配置权限和启动日志验证通过，`clash.rokilai.online` 已完成“HomeAgent 接管 → 禁用并恢复 ddns-go → 移除 ddns-go 并以相同 IPv6 再次接管”的生产闭环，最终为 `enabled/synced` revision `8`。Agent `v0.6.20` 跨平台部署及其余存量域名迁移仍未执行，不得视为全部发布完成。
- 变更清单：设计为 `changes/device-domain-ddns-design.yaml`；第一阶段为 `changes/device-domain-ddns-phase1.yaml`；第二阶段为 `changes/device-domain-ddns-phase2.yaml`；第三阶段为 `changes/device-domain-ddns-phase3.yaml`；重新启用调和修复为 `changes/domain-ddns-reenable-reconcile.yaml`。

本方案为 HomeAgent 服务端自身和已认领设备建立由 Web 控制台管理员配置的受管域名。同一地址源可绑定多个 FQDN，每个绑定仍只对应一个 FQDN 和一个期望 IPv6。设备仅在正常注册、启动和周期事实同步中上报硬件指纹与当前 IPv6 地址；服务端以已保存的“地址源—域名”和“硬件—设备”绑定为准，通过最小权限 Cloudflare API Token 直接创建或更新 AAAA 记录。客户端不保存、上报或决定域名，`ddns-go` 仅继续管理尚未迁移的存量记录。

本方案已按独立变更清单分阶段实现接口、存储、Cloudflare 发布器和调和器；不自动修改 `ddns-go` 配置或迁移生产 DNS 记录。实施保留既有 IPv6 上报字段和未绑定设备的语义，并新增硬件指纹上报；已安装客户端通过常规升级和后续事实同步补齐该数据，不要求重装或重新认领。

## 2. 背景、问题与目标

家庭网络的 IPv6 前缀可能变化。设备的 IPv6 虽会随 Agent 上报收敛，但管理员仍需在 DNS 服务商、`ddns-go` 和设备之间手工维护“哪个域名指向哪个设备”的映射，容易发生漏改、指向错误设备或留下旧地址。

目标：

1. 管理员能在 Web 控制台为服务端自身或某一设备设置、列出或移除一个或多个完整域名。
2. 每台设备在正常运行时上报硬件指纹和 IPv6 地址快照；地址变化后服务端以绑定 revision 创建可恢复调和任务并直接更新 Cloudflare AAAA。
3. 服务端是“地址源—域名”“硬件—设备”绑定和地址选择的唯一权威；客户端不能取得写入、改名或选择任意域名的权限。
4. HomeAgent 仅保存作用于指定 Zone、具备最小 DNS 读写权限的 Cloudflare API Token；不得使用 Global API Key。
5. 无有效 IPv6、硬件身份冲突、设备离线、Cloudflare API 故障或 DNS 失败时安全失败，不把错误地址写入 DNS。

非目标：

- 未在 Web 绑定域名的服务端或设备地址源不创建或更新任何 Cloudflare 记录。
- 不为设备自动生成、购买或验证公网域名所有权。
- 不支持一个绑定多个 AAAA 地址、IPv4 `A` 记录或泛域名；均留待后续设计。
- 不把“DNS 更新成功”视作端口、防火墙、运营商或目标服务可达的证明。
- 不改写《IPv6与DDNS同步集成方案》中路由器前缀与设备地址交集的地址裁决规则。

## 3. 现有能力与架构边界

已存在的 `PUT /api/v1/devices/{device_id}/network-state` 由 Agent 以全量 IPv6 地址快照上报；服务端根据关联路由器的有效前缀计算 `DesiredAddress`，并通过 `GET /api/v1/devices/{id}/ipv6` 输出单行稳定地址。服务端自身已通过 `internal/servernetwork.AutoCollector` 从物理网卡和内核默认路由选择稳定全局单播 IPv6，并通过 `GET /api/v1/server/ipv6` 输出单行地址。已存在的 `ddns-go` 集成方式是从这两类纯文本接口拉取地址后更新 DNS。

因此本方案不允许客户端直接访问 DNS 服务商，也不允许 HomeAgent 读写 `ddns-go` 的含凭据配置、模拟 Web 登录或依赖未公开的热更新语义。新增集成边界是 HomeAgent 内部的 Cloudflare 发布器；它只接收领域层已裁决的记录标识、FQDN、IPv6、revision 与保留属性，不参与设备地址选择。Cloudflare 凭据由服务端安全配置并仅在发布器内部使用。

```text
Web 管理员
  └─ 绑定 source_type + source_id 与 FQDN
       ↓
HomeAgent Server（硬件身份、域名绑定、地址裁决、状态与调度）
  ↑                              ↓
Agent 上报硬件指纹 + IPv6    Cloudflare 发布器
                                 ↓
                         Cloudflare DNS AAAA
```

服务端自举与设备域名绑定统一为不同地址源，但同一 FQDN 在全实例只能属于一个有效绑定和一个发布者。旧的 HomeAgent 服务端直接 DNS 自更新、`ddns-go` 静态配置与新的 Cloudflare 发布器不得同时管理同一 FQDN。

## 4. 数据与授权契约

### 4.1 地址源与域名绑定

服务端持久化以下逻辑实体，`binding_id` 和规范化 `fqdn` 均为全实例唯一；同一 `source_type + source_id` 可拥有多个绑定：

| 字段 | 约束 |
| --- | --- |
| `source_type` | 仅为 `server` 或 `device`，创建后不可修改 |
| `source_id` | `server` 时固定为 `local-server`；`device` 时为已存在设备的 `device_id` |
| `binding_id` | 创建后不变的绑定标识，用于审计和避免改绑时混淆旧任务 |
| `fqdn` | 规范化的小写 ASCII 完整域名，去除末尾点；必须位于实例配置的允许后缀内 |
| `enabled` | 仅启用的绑定可触发 DDNS 调和 |
| `revision` | 每次创建、改绑、启停或删除递增，用于使在途任务失效 |
| `provider_record_id`、`ttl`、`proxied` | 迁移或首次创建时确认的 Cloudflare 记录身份与需保留属性 |
| `status`、`last_error`、`updated_at` | 绑定与 Cloudflare 调和的可观测状态 |

管理员仅可通过 Web 控制台创建、修改或删除绑定。当前实例受管后缀固定为 `rokilai.online`，允许根域名 `rokilai.online`，暂无额外保留或禁止管理的 FQDN。创建或改绑时必须校验 IDNA 转换结果、总长度与标签长度、受管后缀的标签边界、全实例规范化 FQDN 的唯一索引和当前用户对地址源的管理权限。`server` 源仅允许具有实例设置管理权限的用户操作；`device` 源要求用户对该设备具有管理权限。`example.com.evil`、`rokilai.online.evil` 或 `fake-rokilai.online` 必须拒绝；后续增加保留域名须通过显式配置和设计审查，不得硬编码在页面。

`server` 源的期望 IPv6 必须直接来自现有 `AutoCollector` 的当前成功探测结果，不得从公网 DNS、服务端入口域名或 Agent 回连结果反推，以避免自举回环。`device` 源的期望 IPv6 必须来自设备地址与有效路由器前缀的裁决结果。两种源都不得回退到未裁决、已过期或从 DNS 读取的地址。

删除或禁用绑定时先在同一持久化事务内递增 revision 并使在途任务失效，停止后续 Cloudflare 写入，同时保留 DNS 中现有 AAAA 记录，不自动删除。管理员须在后续明确的 DNS 清理流程中确认删除。改绑采用单一事务：失效旧 `binding_id`、释放 FQDN 唯一索引、创建新 `binding_id`，再由新设备的有效报告调和；不得只靠两个设备分别的 revision 推断原子性。

### 4.2 硬件身份上报与重装恢复

`device_id` 仍是当前请求鉴权、设备授权和 `device` 地址源绑定的业务主键；`device_token` 仍是设备凭据。硬件指纹不是登录凭据，也不替代 `device_token`，而是用于把同一物理设备的重新认领重新关联至既有 `device_id`。

Agent 在首次支持该能力的版本中，于注册、启动后的事实同步和周期事实同步中发送版本化硬件身份来源。新安装 Agent 的 `claim` 请求也携带同一结构，用于查找既有身份。服务端仅接受来自已认证 `device_id + device_token` 的同步或经 TLS 保护的 Claim 请求，并用服务端私密密钥对原始值做 HMAC 后持久化 `hardware_fingerprint`；不得保存或在 API、日志、页面中回显原始硬件标识。

| 平台 | 上报来源 | 规则 |
| --- | --- | --- |
| macOS | `IOPlatformUUID` | 作为平台 UUID 候选 |
| Windows | `Win32_ComputerSystemProduct.UUID` | 作为平台 UUID 候选 |
| Linux | `/sys/class/dmi/id/product_uuid` | 作为 DMI 平台 UUID 候选 |
| 所有平台 | 不可用、空值、已知占位值或虚拟机重复值 | 不建立自动硬件绑定，要求管理员处理 |

Linux `/etc/machine-id`、MAC 地址、主机名和 IP 地址可作为诊断事实，但不得用作自动恢复身份的硬件指纹；它们可能在重装、克隆或网络变化后改变。指纹记录还须保存算法版本、来源类型、首次/最近观察时间和冲突状态。

| 情况 | 服务端行为 | 域名/DDNS 副作用 |
| --- | --- | --- |
| 已认证既有设备首次上报可用指纹 | 原子绑定指纹与当前 `device_id` | 维持既有绑定与调和 |
| 同一设备再次上报相同指纹 | 更新最近观察时间 | 无额外副作用 |
| 指纹已绑定其他在线设备，或来源标记为重复 | 标记 `hardware_conflict` 并拒绝自动合并 | 停止受影响绑定的调和，保留 DNS 记录 |
| 重新认领时指纹匹配既有离线设备且 Claim Token 所属用户一致 | 复用既有 `device_id`，轮换 `device_token`，更新事实 | 原域名绑定和 DDNS 记录连续有效 |
| 指纹匹配但所有者不一致、设备仍在线或需转移所有权 | 要求管理员在 Web 中明确确认 | 不自动继承或写入 |
| 指纹缺失或不可信 | 按新设备认领 | 不继承任何既有域名 |

重新认领时，服务端必须先以硬件指纹、所有权、设备在线性和冲突状态完成判定，再消耗 Claim Token 和签发凭据。匹配既有设备时旧 `device_token` 在新 Token 成功持久化后立即失效；失败时必须保留原设备、域名绑定和旧凭据。主板更换、虚拟机克隆或硬件指纹变化时，不自动迁移域名，由管理员执行明确的设备身份迁移。

现有已认领设备无需重装：服务端先支持可选指纹字段，升级后的 Agent 在下一次正常事实同步补齐指纹。尚未升级或无可用指纹的设备继续按现有 `device_id` 工作；它们的域名可继续同步，但在尚未建立硬件绑定前，重新认领不会自动恢复原身份。

2026-09-26 已对目标环境执行只读、脱敏的硬件身份来源验证，未记录原始 UUID 或可逆值：当前 MacMini 与 `macbook-pro-8-local-0e93c101` 均可由普通 SSH/运行用户读取格式有效的 `IOPlatformUUID`，两者不重复且同一运行周期重复读取稳定；`rokilai-914dcd5c` 可由现有 Agent 服务账户读取格式有效的 `Win32_ComputerSystemProduct.UUID`，与两台 Mac 不重复且重复读取稳定；Linux/OpenWrt 设备 `xiaoqiang-7d87e3ec` 不提供 `/sys/class/dmi/id/product_uuid`，重复检查结果一致，必须按“无可信硬件指纹”安全处理。跨重启稳定性因会造成业务中断未在本次审查中执行，移入发布验收门禁；虚拟机克隆、重复值和占位值使用明确负例验证，不以制造真实克隆替代安全失败测试。

2026-09-27 已以本地 Git 正式基线提交 `7c1fa6f` 构建真实 `v0.6.17` Agent 子进程，通过真实 HTTP/SSE 管理入口下发升级，验证下载、SHA256 校验、候选二进制版本检查、原子替换、进程退出及候选 `v0.6.18` 重启后自主上报硬件身份与 Runtime Facts；测试 `TestP1_RealBinaryUpgrade_v0617_ToCandidate` 在 `-race` 下通过。该证据覆盖 Agent 本地真实升级路径，不替代发布后公开服务冒烟、Server `v0.6.35 → v0.6.36` 真实进程升级或三平台生产升级验收。

### 4.3 原子持久化与恢复契约

当前公开 `store.DeviceStore` 与 `store.EnrollmentStore` 仅提供单对象 CRUD，文件实现分别写入设备和认领状态，MySQL 实现也直接对单表执行语句；它们不能证明 Claim Token、设备凭据、硬件指纹、域名绑定和调和任务的跨实体原子性。实施时须在公开 `internal/store` 边界新增独立的控制面事务仓储，不得让业务层直接依赖 `filestore`、`mysqlstore`、`sql.Tx` 或文件路径。

控制面事务以不可变命令输入和单一结果输出表达，至少覆盖以下业务原子操作：

| 原子操作 | 同一提交内必须完成 | 任一步失败后的保证 |
| --- | --- | --- |
| 认领新设备 | 校验并扣减 Claim Token、创建设备、保存新设备凭据哈希、建立可用硬件指纹唯一关系 | Claim Token 使用次数、设备、凭据和指纹均保持提交前状态 |
| 恢复既有设备 | 校验所有者、离线性和指纹，扣减 Claim Token，轮换设备凭据哈希并更新时间 | 旧凭据继续有效，Claim Token 不消耗，原域名绑定不变 |
| 记录硬件指纹 | 校验设备与算法版本，建立或刷新唯一关系，更新冲突状态 | 不产生重复指纹归属或半更新冲突状态 |
| 创建域名绑定 | 校验规范化 FQDN 唯一性、地址源和期望 revision，创建绑定并推进源版本 | 不占用 FQDN，不创建调和任务 |
| 改绑域名 | 失效旧 `binding_id`、释放并重新占用 FQDN、创建新绑定与初始状态 | 旧绑定保持完整有效，不出现双归属或无归属间隙 |
| 禁用或删除绑定 | 推进绑定 revision、更新配置状态并取消未开始任务 | 失败时旧绑定与旧任务状态均不改变；不删除 DNS |
| 创建或合并调和任务 | 比较源、网络、裁决与绑定 revision，保存唯一待执行任务 | 不产生同一绑定的重复有效任务 |
| 完成调和任务 | 以 `binding_id + binding_revision + desired_ipv6 + task_revision` 条件更新结果 | 条件不匹配返回冲突，旧结果不得覆盖新状态 |

所有修改命令必须携带调用方已读取的期望 revision；仓储以比较并交换语义提交，revision 不匹配统一返回冲突且无副作用。FQDN 规范化值、有效硬件指纹、单绑定有效调和任务必须由持久化唯一约束保证，不能只依靠进程内预检。业务错误与存储错误必须分离：不存在、revision 冲突、唯一性冲突、权限前置条件失效和持久化失败均映射为稳定内部错误，禁止通过字符串匹配数据库错误决定业务状态。

文件存储采用 `${HOMEAGENT_DATA_DIR}/control-plane.json` 作为上述事务实体的单一权威快照，至少包含 schema version、devices、claim tokens、credential hashes、hardware fingerprints、domain bindings、reconcile tasks 与各自 revision。迁移完成后，事务内实体不得继续分别写入旧 `devices.json`、`enrollment.json` 或新建多个相互依赖文件。提交顺序固定为：持有单进程写锁与跨进程独占锁 → 深拷贝当前快照 → 在副本执行命令和全部约束校验 → 写同目录临时文件并以 `0600` 创建 → `fsync` 临时文件 → 原子 `rename` 覆盖权威文件 → `fsync` 父目录 → 最后替换内存快照。编码、写入、同步或重命名任一步失败时丢弃副本并保留旧内存与旧权威文件；进程崩溃后只读取最后一次成功重命名的完整快照，清理未提交临时文件。无法取得跨进程独占锁时拒绝第二个写实例启动。

MySQL 存储在一个数据库事务内执行同一业务命令：对 Claim Token、设备、指纹、绑定和任务的现存行使用锁定读取，对可能不存在但受唯一约束保护的实体依靠唯一索引与条件更新防止幻读；提交前再次验证期望 revision。死锁或可重试事务冲突由仓储执行有界重试，业务唯一性或 revision 冲突不得自动改写输入后重试。事务提交结果不确定时必须重新按业务标识读取并核对 revision，不得直接报告成功或重复扣减 Token。

文件与 MySQL 实现必须运行同一套公开仓储契约测试，断言返回值和提交后的完整快照，而不是只断言错误。测试至少覆盖：每项原子操作成功、每个持久化步骤故障注入、重复 FQDN、重复指纹、Claim Token 并发双花、改绑并发、任务合并、旧任务回写、提交结果不确定、进程重启恢复、旧文件迁移中断和第二写实例拒绝。负例必须证明 Claim Token、旧设备凭据、绑定、任务和 revision 无多余变化。

存量文件首次迁移须在未启动业务写入前执行：读取并校验旧文件 → 构造新快照 → 写入并同步 `control-plane.json` → 重新读取校验实体数量与关键哈希 → 记录迁移完成标记。迁移失败时保留旧文件并拒绝启动写服务；迁移成功后旧文件只保留只读回滚副本，不允许双写。MySQL schema 升级同样先建立新表、唯一索引与 revision 字段，再在事务中回填并校验，失败时不得切换运行路径。

## 5. 调和状态机与并发

每个 `fqdn` 在 HomeAgent 内独立串行调和。任务携带 `binding_id`、绑定 `revision`、设备网络状态 revision、路由器前缀/地址裁决版本、Cloudflare 记录 ID、期望 IPv6 与需保留的 TTL/代理属性。Cloudflare API 返回后必须重新核对这些版本与期望值；不匹配时丢弃结果且不覆盖新状态。

HomeAgent 为每个受管 `binding_id` 持久化最后调和 revision、最后应用 IPv6、运行状态、错误分类和重试时间，重启后恢复未完成任务并按最新状态重新校验。重试采用有界指数退避与抖动；鉴权失败、Zone 越界、记录身份冲突和权限不足不得无限重试，必须进入可观测失败状态。

| 条件或事件 | 状态 | 动作 |
| --- | --- | --- |
| 迁移绑定只读观察 | `observing` | 读取并展示差异，禁止写入 |
| 绑定未启用 | `disabled` | 不调和 |
| 未收到一致的有效上报 | `waiting_report` | 保留已有 DNS 记录，等待上报 |
| 已得到新的期望 IPv6 | `pending` | 合并重复任务 |
| Cloudflare 发布器正在写入 | `syncing` | 单域名仅一个在途操作 |
| Cloudflare API 与权威 DNS 查询均确认目标地址 | `synced` | 持久化已应用地址与时间 |
| Cloudflare API 拒绝、超时、限流或 DNS 未收敛 | `failed` | 分类记录错误，按类别重试或阻断 |
| 设备离线或有效前缀过期 | `stale` | 停止更新，保留最后确认记录并告警 |

同一设备域名被改绑、禁用或删除时，旧任务通过绑定 revision 与 `binding_id` 联合失效。改绑遵循第 4.1 节的单一事务；在新设备产生有效报告前仅可为 `waiting_report`，不得创建 Cloudflare 写入任务。

已同步绑定被禁用后再次启用时，即使当前期望 IPv6 与 `last_applied_ipv6` 相同，也必须重新创建或复用当前 revision 的调和任务，并在 Cloudflare API 与权威 DNS 再次确认后恢复为 `synced`。`last_applied_ipv6` 只表示历史成功结果，不得作为跳过重新启用调和的充分条件；重复运行由任务幂等键合并，不得产生并行写入。

绑定配置状态与 DDNS 运行状态分开保存：前者为 `observing/enabled/disabled/deleted`，后者仅可为 `waiting_report/pending/syncing/synced/failed/stale`；`observing` 不得产生运行任务。设备被删除、权限撤销或绑定所有者失去管理权限时，按禁用流程立即停止 Cloudflare 调和并保留 DNS 记录，审计事件不得泄露其他用户的 FQDN 或地址。

## 6. Cloudflare 集成契约

目标部署保持 HomeAgent Server 运行在宿主机，由 HomeAgent 直接调用 Cloudflare API 管理已迁移绑定。保留现有 `GET /api/v1/server/ipv6`、`GET /api/v1/devices/{id}/ipv6` 及 `ddns-go` 静态域名 + URL 模式，不修改旧协议的路径、响应格式或鉴权语义；它们仅服务尚未迁移的域名和回滚。

### 6.1 凭据与权限边界

- 仅支持 Cloudflare API Token，不接受 Global API Key、账号密码或会话 Cookie。
- Token 仅授予目标 Zone 的 `Zone:Read` 与 `DNS:Edit`，资源范围必须限制到明确 Zone；若真实 API 验证表明读取记录不需要 `Zone:Read`，实施时进一步缩减权限。
- Token 以明文 Secret 写入 `${HOMEAGENT_DATA_DIR}/cloudflare-ddns.json`，方式与当前 ddns-go 的受限配置文件相同；Linux 生产部署的 `HOMEAGENT_DATA_DIR` 为 `/var/lib/homeagent/data` 时，对应路径为 `/var/lib/homeagent/data/cloudflare-ddns.json`，当前 macOS 宿主机实际路径为 `~/Library/Application Support/HomeAgent/data/cloudflare-ddns.json`。该文件不得提交 Git，所有者必须是 HomeAgent 运行用户，权限必须严格为 `0600`，部署备份与故障采集均须按 Secret 处理。页面、API、日志、审计事件与错误信息均不得回显明文。
- 配置文件格式固定为 JSON，顶层字段为 `api_token`、`zone_id` 和 `managed_suffix`；`managed_suffix` 必须等于 `rokilai.online`。仓库内如提供示例，只能使用明显占位符，不得包含真实 Token、Zone ID 或可用凭据。
- 真实 Token 后置到生产协议验证和部署阶段提供，不阻塞领域模块、配置解析器、Cloudflare 测试替身及页面状态的编码。配置文件不存在或 `api_token` 为空时，服务其他功能正常启动，Cloudflare DDNS 保持禁用并显示“未配置”，不得创建调和任务或访问 Cloudflare。
- 配置 JSON 无效、必填字段缺失、后缀不匹配、文件所有者错误或权限宽于 `0600` 时，Cloudflare DDNS 必须安全禁用并给出不含 Secret 的诊断；不得影响设备管理等无关功能，也不得回退到 ddns-go 配置、环境变量或数据库读取 Token。
- Token 更新只允许整体替换配置值并重启服务生效；启动时先验证配置完整性，但 Cloudflare 暂时不可达不得清空旧配置或删除 DNS。轮换时先写入并验证新 Token，再撤销旧 Token；失败则恢复原配置文件并重启。
- 配置须显式声明 `rokilai.online` 对应的 Zone ID；受管后缀固定为 `rokilai.online` 且允许根域名。FQDN 必须同时通过后缀标签边界校验和 Cloudflare 返回的 Zone 归属校验，不得从 `public_url` 自动推断。
- 发布器只能接收领域层已裁决的数据，不得读取未裁决设备地址、从 DNS 反推期望地址或自行决定域名归属。

### 6.2 记录发现与写入

首次创建或迁移绑定时，HomeAgent 通过 Zone ID 与规范化 FQDN 查询 AAAA：不存在时仅在管理员明确确认后创建；恰有一条时保存其记录 ID、TTL 与 `proxied`；存在多条 AAAA、同名 CNAME、Zone 不一致或记录身份发生变化时拒绝写入并要求人工处理。

更新请求必须以已保存的记录 ID 为目标，只改变 AAAA `content`；默认保留原 TTL、`proxied` 及 Cloudflare 支持的其他非地址属性。API 成功只表示服务商受理，HomeAgent 还须重新读取记录并查询权威 DNS，二者都与当前期望 IPv6 一致后才能标记 `synced`。权威查询超时、多个 AAAA 残留、DNSSEC 错误或 API 读取不一致均不得误报成功。

Cloudflare 返回 `401/403`、Zone 越界、记录 ID 不存在、同名冲突、限流、`5xx`、超时或格式异常时，发布器必须映射为内部错误枚举。限流、`5xx` 和网络超时可按有界退避重试；凭据、权限、Zone 与记录身份错误必须阻断自动重试并提示管理员。完整上游响应、Token 和无关记录不得持久化或写入日志。

### 6.3 观察模式与启用

新建的存量迁移绑定首先进入 `observing`：计算期望 IPv6，读取 Cloudflare 当前 AAAA 与属性并展示差异，但禁止任何 DNS 写入。只有管理员确认地址源、现有发布者已停用、期望地址与记录身份无冲突后，才能原子切换为 `enabled/pending` 并创建首次调和任务。

观察模式不是同步成功，也不得通过“当前值碰巧一致”自动启用。启用时须再次校验绑定 revision、Cloudflare 记录 ID、当前记录内容和发布者排他状态，避免确认后到写入前发生变化。

### 6.4 ddns-go 兼容边界

当前 `ddns-go v6.17.5` 继续运行并管理未迁移域名，不需要 Fork、脚本导入、模拟登录或配置热更新。HomeAgent 不读取或改写其含凭据 YAML；迁移操作由管理员按第 8 节逐条从 ddns-go 静态配置移除域名。

同一 FQDN 不得同时存在于启用的 HomeAgent 绑定、`ddns-go` 静态配置或旧 HomeAgent DNS 自更新配置中。由于 HomeAgent 无稳定 API 自动确认 ddns-go 配置已移除，启用前必须显示人工排他确认，并以迁移清单、配置快照和首次同步证据形成审计链。ddns-go 管理端口 `9876` 当前发布到全部 IPv4/IPv6 接口的风险独立处理，不作为本功能读取或写入 ddns-go 的理由。

### 6.5 真实协议验证

Cloudflare 发布器编码前直接使用生产 Cloudflare Zone `rokilai.online` 和最小权限 Token 验证真实 API；该门禁不阻塞不依赖 Cloudflare 响应结构的领域模型、配置解析、状态机、持久化接口和页面开发。真实验证第一阶段仅操作该 Zone 下专用临时记录 `ddns-test.rokilai.online`，验证 Token、Zone/记录查询、创建、更新、记录属性保留与权威 DNS 收敛后删除临时记录；鉴权失败、权限不足、冲突和异常响应优先通过无写入请求或基于真实证据的测试替身验证，不得为制造错误而扩大 Token 权限或破坏其他记录。第二阶段选择一个由管理员确认的低风险存量域名，先保存 Cloudflare 记录与 ddns-go 配置快照，再演练单条接管和回滚。测试替身必须基于真实响应构造并记录差异；递归缓存只用于传播观察，严禁批量写入或删除生产记录。

2026-09-28 真实观察结论：新版账户 API Token 的有效性端点为 `GET /accounts/{account_id}/tokens/verify`；旧的 `GET /user/tokens/verify` 对有效账户 Token 返回 `401 / code 1000`，不得据此判定凭据无效。`GET /zones/{zone_id}`、按名称查询 AAAA、创建、按记录 ID `PATCH` 和删除均返回 `HTTP/2 200` 与 `success=true`；仅 PATCH `content` 时 TTL、`proxied` 与 `comment` 保持不变。对从未查询过的唯一临时名称，静默等待 10 分钟后，Cloudflare 两个权威服务器及 `1.1.1.1`、`8.8.8.8` 均返回更新后的 IPv6，随后删除并确认 API 记录数为零。若在记录创建前或刚创建后查询不存在的名称，权威节点可能按 SOA negative TTL（本次为 1800 秒）继续返回缓存的 `NXDOMAIN`；因此新建记录和既有记录更新均不得立即以单次权威查询判失败，首次权威校验须延迟并采用有界重试，API 成功期间保持 `syncing` 而非误报 `failed` 或 `synced`。

2026-09-28 首个存量域名迁移与回滚结论：原计划候选 `direct-manga.rokilai.online` 实际为指向根域的 `CNAME`，不符合单条 AAAA 接管契约，预检阶段即排除；改用管理员确认的 `clash.rokilai.online`。首次迁移前分别保存 ddns-go 配置和 Cloudflare 记录快照，仅从 ddns-go 移除该域名并重启容器，再将已观察绑定由 `observing` 启用。首次回滚后的再次接管曾因期望 IPv6 等于历史 `last_applied_ipv6` 而停留 `waiting_report`，验证终止后立即禁用 HomeAgent 并恢复 ddns-go，未发生双写或 DNS 地址变化。Server `v0.6.39` 修复部署后重新执行完整闭环：首次接管达到 `enabled/synced` revision `6`；禁用至 revision `7`、恢复 ddns-go 并确认 DNS 不变；再次从 ddns-go 移除域名后，以相同 IPv6 启用并达到 `enabled/synced` revision `8`。最终 Cloudflare 记录 ID `8acbd304ece66c7efa6cbcf81d46bef9`、类型 `AAAA`、TTL `1`（自动）和 `proxied=false` 保持不变，期望、提供商和最后应用 IPv6 均为 `240e:390:9a9:c220:c72:59e9:1d1d:24d5`；`diana.ns.cloudflare.com`、`matt.ns.cloudflare.com`、`1.1.1.1` 与 `8.8.8.8` 返回一致。ddns-go 最终不包含该域名，临时管理员密码与会话均已清理，脱敏证据保存在宿主机受限目录 `backups/ddns-reenable-validation-clash-20260928124300`。该单域名的接管、回滚和相同地址再次接管验收通过。

## 7. Web 界面契约

设备详情或设备列表的管理员操作入口提供该设备的“DDNS 域名”多绑定列表；服务端 IPv6 设置页提供 `local-server` 源的多绑定列表。每个条目必须显示 FQDN、地址源类型、管理模式、状态、期望 IPv6、Cloudflare 当前 IPv6、TTL、代理状态、最近同步时间与错误原因；硬件身份仅在设备源中显示“已绑定 / 未绑定 / 冲突”和来源类型，绝不显示原始值或指纹。

保存动作采用“输入 FQDN → 只读预检 → 明确确认 → 原子保存绑定”的顺序。预检校验地址源权限、域名规范化、允许后缀、占用、保留记录、Cloudflare Zone 与现有记录身份；它不修改 HomeAgent、ddns-go 或 DNS。存量记录保存后先进入 `observing`，新记录经明确创建确认后才可为 `pending`；未形成有效裁决的 `device` 源为 `waiting_report`。不得因为配置已保存或当前地址碰巧一致就显示“已同步”。

禁用和删除动作需解释“DNS AAAA 不会自动删除”；禁用应立即阻止新调和。非管理员只可读取其被授权设备的状态，不可看见或修改其他设备绑定。

## 8. 失败、兼容与迁移

- 旧 Agent 不携带硬件指纹时：维持既有 `device_id`、IPv6 上报和 DDNS 调和；升级后经正常事实同步补齐，不要求重装或重新认领。
- 硬件指纹与既有设备冲突：不合并设备、不迁移域名、不轮换 Token；仅显示受权限保护的冲突状态并等待管理员处理。
- Cloudflare API 不可用、限流或暂时失败：服务端保留期望状态与最后确认 AAAA，按错误类别有界重试，不降级为客户端直连 DNS。
- Cloudflare 凭据、权限、Zone 或记录身份错误：停止自动写入并要求管理员处理，不自动删除或重建记录。
- 同一 FQDN 已由服务端自更新或其他发布者管理：预检拒绝创建，避免双写。
- 服务端自身 IPv6 探测失败：所有 `server/local-server` 绑定发布为 `stale` 且不携带 IPv6，保留上次已确认 DNS 记录。
- 未迁移域名：继续使用原有 `ddns-go` 静态域名 + 单行 IPv6 URL，行为不变。
- 回滚：先禁用对应 HomeAgent 绑定并等待在途操作失效，再由管理员恢复此前已验证的 `ddns-go` 静态配置；现有 DNS 记录保持不动。

### 8.1 存量迁移基线

2026-09-24 从本机运行中的 `ddns-go v6.17.5` 配置只读提取到以下域名；未记录凭据或 URL 查询参数。该列表仅证明当前配置存在，不证明最终地址源归属：

| 当前配置分组 | FQDN | 迁移前待确认地址源 |
| --- | --- | --- |
| MacMini IPv6 | `clash.rokilai.online` | `server/local-server`（已确认） |
| MacMini IPv6 | `direct-manga.rokilai.online` | `server/local-server`（已确认） |
| MacMini IPv6 | `files.rokilai.online` | `server/local-server`（已确认） |
| MacMini IPv6 | `homeagent.rokilai.online` | `server/local-server`（已确认） |
| MacMini IPv6 | `mac.rokilai.online` | `server/local-server`（已确认） |
| MacMini IPv6 | `manga.rokilai.online` | `server/local-server`（已确认） |
| MacMini IPv6 | `renthub-api.rokilai.online` | `server/local-server`（已确认） |
| MacMini IPv6 | `renthub.rokilai.online` | `server/local-server`（已确认） |
| MacMini IPv6 | `rokilai.online` | `server/local-server`（已确认） |
| MacBookPro IPv6 | `mbp.rokilai.online` | `device/macbook-pro-8-local-0e93c101`（已确认） |
| WindowsPC IPv6 | `win.rokilai.online` | `device/rokilai-914dcd5c`（已确认） |

MacMini 分组的 9 个域名已由管理员明确归属 `server/local-server`；该结论不得因主机名、当前 IPv6 或后续设备认领自动改变。`mbp.rokilai.online` 已归属 `device/macbook-pro-8-local-0e93c101`，`win.rokilai.online` 已归属 `device/rokilai-914dcd5c`。所有存量域名还须逐条补齐：Cloudflare Zone ID、记录 ID、原 AAAA、TTL、`proxied`、当前唯一发布者、迁移操作者、迁移时间与回滚配置快照。

### 8.2 逐域名迁移步骤

1. 备份当前 `ddns-go` 配置并记录 Cloudflare 实际 AAAA、记录 ID、TTL、代理状态和权威 DNS 结果。
2. 在 HomeAgent 创建 `observing` 绑定，确认目标地址源和已裁决 IPv6；该步骤禁止 DNS 写入。
3. 比对 HomeAgent 期望值、Cloudflare API 当前值、权威 DNS 与 ddns-go 当前来源；存在多 AAAA、CNAME 或归属不明时停止迁移。
4. 从 ddns-go 或旧 HomeAgent 发布配置移除该单个 FQDN，并确认旧发布者不再管理它；不得批量先删后迁。
5. 在 HomeAgent 再次执行排他预检后启用绑定，立即调和一次并验证 Cloudflare API 与权威 DNS 均已收敛。
6. 单条成功后记录证据再迁移下一条；失败时禁用 HomeAgent 绑定、保持 DNS 当前记录并恢复该域名的原 ddns-go 配置。

首个低风险迁移与回滚演练域名固定为 `direct-manga.rokilai.online`。演练前必须再次确认它不是当时唯一关键入口，并完整保存其 Cloudflare 记录与 ddns-go 配置；若运行时业务重要性已变化，停止演练并重新完成设计确认，不得现场替换为其他域名。

## 9. 设计审查结论与实施门禁

### 9.1 已确认的代码库证据

| 领域 | 直接证据 | 结论 |
| --- | --- | --- |
| 现有 DDNS | `internal/ddns` 通过 `DNSPublisher` 直接写设备 DNS，并在宽限期后删除 AAAA；`internal/servernetwork` 可直接更新服务端自身记录；二者都可与外部 `ddns-go` 重叠 | 新 Cloudflare 发布器必须复用公开发布边界或替换旧路径，服务端与设备记录都必须逐条显式迁移且不能双写 |
| IPv6 输出 | `getDeviceIPv6Text` 在 `DesiredAddress` 为空时回退到第一个 `ReportedAddress` | 该回退绕过路由器前缀裁决，必须先以负例测试锁定为非 200 安全失败 |
| 网络状态持久化 | 组合根以 `devicestate.NewService(nil)` 和 `prefixstate.NewService(nil)` 启动 | 当前为进程内状态，不满足重启恢复与在途任务版本校验 |
| 设备存储 | 公开 `store.DeviceStore` 仅有单对象 CRUD；文件 Registry 与 MySQL 库均无设备、指纹、绑定和 Claim Token 的跨实体事务接口 | 不能依靠多次 `Save` 实现改绑或重新认领原子性 |
| Claim 顺序 | `claimDevice` 先调用 `ConsumeClaimToken`，之后才解析请求、生成凭据和保存设备 | 与“持久化失败不消耗 Claim Token、不使旧 Token 失效”冲突，需先建立原子 Claim 仓储契约 |
| Agent 上报 | 已有启动与周期 Facts 上报、IPv6 快照 revision 持久化与冲突恢复 | 可扩展版本化硬件身份字段，但必须保持旧 Server/Agent 兼容 |
| UI 扩展点 | 设备页面已拆分为 `internal/ui/static/js/devices` 模块，服务端 IPv6 设置已有独立区域，API 调用经 `apiFetch` | 设备源绑定属于设备模块；服务端源绑定位于服务端 IPv6 设置页的独立列表模块，不与旧的探测或手工输入控件混用 |
| 版本 | 第一阶段候选 Server `v0.6.36`，Agent `v0.6.18`；第二阶段候选 Server `v0.6.37`，Web 客户端门禁对应 Agent `v0.6.19`；第三阶段 Server `v0.6.38`，Agent `v0.6.20`；重新启用调和缺陷候选 Server `v0.6.39` | 两个组件按变更分别升版；通过验收前不标记发布完成 |

### 9.2 审查未通过的阻断项

运行时 Secret 的配置契约已经确定：真实 Token 后置到生产协议验证和部署阶段，不再作为编码启动条件；进入真实验收前仍须完成第 6.1 节规定的宿主机权限、读取、轮换回滚、备份排除与日志脱敏验证。

1. **Cloudflare 发布器实施门禁**：生产 Zone `rokilai.online`、临时记录 `ddns-test.rokilai.online` 和最小权限 Token 已选定，但仍须在 Cloudflare 发布器编码前实际执行第 6.5 节协议链，并据此定义错误映射、属性保留与测试替身；不阻塞第 9.3 节第一、二阶段中不依赖真实协议的工作。
2. **存量唯一发布者尚未确认**：受管后缀、保留规则及全部 11 个存量域名的目标地址源均已确定；实际迁移前仍须逐条确认当前唯一发布者并证明同一 FQDN 不会由 ddns-go、旧 HomeAgent 发布路径与新 Cloudflare 发布器双写。
3. **其余验收参数未定案**：生产 Zone、临时 FQDN、低风险存量演练域名 `direct-manga.rokilai.online` 及三平台当前运行周期的硬件身份读取结果已确定，但权威 DNS 查询入口、TTL/收敛超时、DNSSEC 判定、macOS/Windows UUID 跨重启稳定性与上一正式版本升级验收入口仍需明确。

### 9.3 解阻后的实施分期

1. 先按第 4.3 节实现控制面事务仓储与迁移、修正 IPv6 端点安全失败，完成硬件身份采集与向后兼容上报；本阶段不得调用或假设 Cloudflare API。
2. 再实现域名绑定领域模块、权限 API、配置文件解析与安全禁用、观察模式、状态机、恢复队列和设备页面；Cloudflare 边界仅使用由第 6 节契约定义的抽象接口与协议无关假实现，不得根据推测构造 Cloudflare 响应测试替身。
3. 提供真实 Token 并完成第 6.5 节协议观察后，基于真实证据实现 Cloudflare 发布器、错误映射、记录属性保留和权威 DNS 验证，通过安全审查后按第 8 节逐域名迁移并完成跨版本验收。

每一期必须使用独立变更清单限定 `allowed_paths` 并单独获得实施确认。第一、二阶段可在各自依赖的设计阻断关闭后开始，不要求提前提供真实 Cloudflare Token；第三阶段及任何真实 DNS 写入必须关闭本节全部门禁并另行获得外部状态变更确认。本设计审查清单本身不得扩展到代码路径。

## 10. 验收与测试策略

实施任务的测试必须先于实现，并至少覆盖：

1. Web 中 `server/device` 两种地址源绑定的创建、多域名列表、规范化、允许后缀、权限、排他和保留名称拒绝；
2. 未绑定域名的服务端或设备地址源无论探测/上报何种 IPv6，均不得创建或更新 Cloudflare 记录；
3. IPv6 地址快照经路由器前缀交集后的期望地址选择，以及双前缀、乱序和重启恢复；
4. 已认证设备首次/周期硬件指纹上报、无指纹兼容、重复指纹冲突、虚拟机克隆和原始硬件标识不落盘/不回显；发布验收须在维护窗口重启目标 macOS 与 Windows 设备，证明重启前后算法版本、来源类型和 HMAC 指纹一致，Linux/OpenWrt 无 DMI UUID 时继续稳定返回“无可信硬件指纹”；
5. 同硬件重新认领保留原 `device_id`、域名绑定与状态并轮换 Token；不同所有者、仍在线设备、指纹变化和持久化失败不得错误继承身份或使旧 Token 失效；
6. 改绑、禁用、删除、revision 倒退、在途 Cloudflare I/O 返回和并发上报不允许旧任务覆盖新绑定；
7. 已同步绑定禁用后以相同 IPv6 重新启用，必须重新调和并恢复 `synced`，且仅产生一次有效发布；
8. 基于 Cloudflare 真实 HTTP 观察构造的替身测试，覆盖成功、401/403/404/429/500、超时、异常响应、无效 IPv6、重复 FQDN、多 AAAA、CNAME 冲突、记录 ID 变化、属性保留和权威 DNS 不符的反例；
9. 使用专用测试域名执行真实 Cloudflare 端到端验收：上一正式版本升级后分别为 `server` 与 `device` 源创建多域名绑定，验证观察模式、两类地址变化、无地址安全失败、TTL/代理属性保留与 AAAA 权威记录收敛；再以单个存量域名演练 ddns-go 退出、HomeAgent 接管、回滚和相同地址再次接管。若目标设备已具备明确的端口、防火墙和运营商入站前提，可另做外网访问烟测；该烟测不作为 DDNS 功能成功的证明；
10. 变更范围、架构依赖、全量 `-race` 回归和 Diff Coverage 不低于 60%。

验收记录必须分别列出 Cloudflare API 与权威 DNS 响应链、Token 实际权限、记录属性前后值、测试替身差异、关键反例、存量域名迁移/回滚证据和真实端到端结果；未执行的真实环境步骤不得标记为通过。
