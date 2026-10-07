# SSH 连接端口设置设计方案

## 状态与授权范围

- 设计状态：原方案、MySQL 兼容补充、设备级权限字段及客户端版本联动调整均已获用户确认。
- 实施状态：代码与本地质量门禁完成；本次提交目标为 dev，提交结果以 Git 记录为准。
- 验收状态：本地验收通过；未部署、未执行生产验证。
- 本轮已获清单范围内实施、本地测试及提交 dev 授权；部署另行确认。

## 问题与代码证据

设备使用自定义 SSH 端口时，控制台缺少编辑入口；复制内容依赖设备数据，无法纠正上报值与实际连接端口不一致的问题。

| 位置 | 已核实行为 |
| --- | --- |
| `internal/ui/static/js/devices/render.js`，设备卡与 SSH 分区 | 使用 `ssh_port`；非 22 端口生成 `ssh -p <端口> <用户>@<地址>` |
| 同文件，`section === 'settings'` | `.detail-section-settings` 内仅显示基本属性与危险操作，无端口编辑 |
| `internal/api/api.go`，`updateDeviceReq`、`patchDevice` | PATCH 仅接受 alias、mac、github_sync_enabled，并拒绝未知字段 |
| 同文件，`putDeviceFacts` | 读取设备后，以正数上报端口覆盖 SSHPort，再保存 |
| `internal/registry/registry.go`，Save、writeLocked | 注册表锁内保存；通过文件或 ControlPlaneService 持久化 |
| `internal/store/mysqlstore/controlplane.go` | 控制平面快照存储于 snapshot_json |
| `internal/sshsync/ssh.go` | 服务端 SSH 探测、主机密钥扫描和连接均使用 SSHPort |
| `cmd/homeagent-agent/main.go` | Agent 运行参数优先于本地配置，缺省端口为 22 |

现有代码证据证明入口与契约缺失；未查询具体生产设备，不能据此宣称某设备的实际上报端口错误。

## 功能边界与用户契约

在设备详情“设备设置”增加 SSH 连接端口设置，显示当前有效端口、最近上报端口与来源（客户端上报／手动设置）。支持保存手动端口及恢复客户端上报值。

端口必须为 1–65535 的整数。手动设置优先；恢复后立即采用最近有效上报值。修改影响卡片、详情页复制命令以及服务端主动 SSH 连接使用的目标端口，不修改设备 sshd、本地 Agent 配置或防火墙，不自动发起 SSH 同步或连通性探测。共享读权限用户仅能查看；编辑沿用现有设备更新权限。

保留现有复制格式：22 可省略 -p，自定义端口必须包含 -p。Agent 依赖本次修改的共享设备、注册表与存储代码，按已确认的版本联动调整从 v0.6.22 升至 v0.6.23；服务端从 v0.6.53 升至 v0.6.54。不修改 Agent 独立命令功能，不执行安装、发布或部署。

## 数据与公开契约

保留 Device.SSHPort／JSON ssh_port 表示有效连接端口，保持既有消费端语义。拟新增字段（属于本方案提出的新契约，并非当前已有字段）：

- SSHPortReported／ssh_port_reported：最近有效上报值，1–65535。
- SSHPortOverride／ssh_port_override：0 表示未设置，1–65535 表示手动设置。

GET 设备列表、详情和 PATCH 成功响应均返回上述字段及 can_edit_ssh_port。后者由当前请求主体通过现有授权器对 devices.update 的设备级判断计算；无设备级权限不能显示编辑入口，PATCH 仍独立鉴权；设备 Token 的事实上报响应中该值为 false，不授予编辑权限。来源由 override 是否为 0 显式映射为“客户端上报”或“手动设置”，不得透传协议状态。

扩展现有 PATCH /api/v1/devices/{id}：ssh_port_override 缺省表示不变，整数 0 表示恢复，1–65535 表示覆盖；null、负数、超范围、小数、字符串和布尔值均返回 400，全部字段不得部分提交。旧 PATCH 请求行为不变；事实上报接口不接受手动覆盖字段，设备 Token 不能写入覆盖值。

保留 Registry.UpdateDevice 现有签名。新增带端口设置的原子更新入口，旧入口委托兼容路径；不擅自改动其他公开签名。不新增外部依赖，不新建跨模块业务服务；端口规则属于既有设备领域。

## 状态矩阵与并发

以服务端设备 ID 隔离设置，不按 hostname 或 IP 隔离。

| 当前状态 | 事件 | 上报值 | 覆盖值 | 有效值 |
| --- | --- | --- | --- | --- |
| 自动 | 有效事实上报 R | R | 0 | R |
| 手动 M | 有效事实上报 R | R | M | M |
| 任意 | 保存 M | 保持 | M | M |
| 手动 | 恢复自动 | 保持 R | 0 | R |
| 任意 | 无效设置／持久化失败 | 保持 | 保持 | 保持 |
| 任意 | 无效事实端口 | 保持 | 保持 | 保持 |

事实端口 0／缺省沿用当前兼容行为，不更新端口；负数或大于 65535 返回 400，不能产生部分更新。有效值在同一锁内从最新记录计算，禁止以请求开始前读取的覆盖值覆盖当前记录。Save 接收的旧对象不得回写手动字段；设置必须仅经专用原子入口变更。

两个并发设置按锁内成功持久化顺序生效；设置与上报无论顺序如何，上报不清除手动值。恢复与在途上报最终取最新成功提交的上报值。写入失败回滚内存及所有端口字段，错误不返回成功。

前端保存期间禁用重复提交；完成后重新检查当前设备 ID、页面与请求上下文，切换设备后不向新页面写入旧结果。轮询不得覆盖正在编辑的输入；失败保留输入并显示错误，不显示成功。服务端返回成功后更新共享设备状态并刷新有效端口；复制按钮取最新状态，保存成功后立刻可复制新命令。

## 持久化与兼容

文件注册表、文件控制平面及 MySQL 控制平面快照必须保存三个端口字段并支持重启恢复。旧记录缺少新增字段时，reported 从原 ssh_port 初始化，override 为 0，有效端口保持不变；不得将旧自定义端口解释为手动覆盖。新设备从注册端口初始化 reported，不能由客户端注入 override。

重新注册或硬件身份恢复同一设备 ID 时保留手动设置，允许更新 reported。设备删除时一并删除端口设置，其他设备无副作用。

本方案不新增表。只读核查已确认：`cmd/homeagent-server/main.go` 启用 MySQL 时调用 `AutoMigrateFileStoreToMySQL`；`internal/store/migration.go` 经传统 `DeviceStore.SaveDevice` 导入设备。`internal/store/mysqlstore/mysqlstore.go` 的 GetDevice、ListDevices、SaveDevice 当前仅读写 ssh_port，因此该兼容路径必须一并适配。当前 AutoMigrate 仅执行 CREATE TABLE IF NOT EXISTS，不能为存量表补字段。

### MySQL 字段迁移与失败恢复

在既有 devices 表追加以下字段，保留原 ssh_port、主键、时间字段与列表排序契约：

```sql
ssh_port_reported INT NOT NULL DEFAULT 0,
ssh_port_override INT NOT NULL DEFAULT 0
```

reported 的数据库默认值 0 仅是旧写入方及迁移过程的兼容哨兵；领域与 API 返回值必须规范化为有效上报端口。override 为 0 表示自动模式。

新表 DDL 包含两列；存量表通过 INFORMATION_SCHEMA.COLUMNS 检查列是否存在，缺失时逐列 ALTER TABLE ADD COLUMN。多实例并发迁移时，仅在复查确认列已存在且类型、非空约束与默认值符合契约后，允许将重复列错误视为成功；不得忽略其他 DDL 错误或不兼容结构。

完成两列检查后，仅对 reported = 0 且原 ssh_port 在 1–65535、override = 0 的记录回填 reported = ssh_port。旧记录的 ssh_port 与其他属性保持不变。已有 reported 或手动覆盖不得被重复迁移重置。存在无法规范化的非法旧端口或字段结构不兼容时，返回明确错误并阻止存储初始化，不自动改成 22。

MySQL DDL 可能隐式提交，不承诺两次加列原子完成。任何步骤失败都返回错误；重启重新检查列并补齐剩余步骤，回填幂等。上线前备份数据库与控制平面快照；回退保留新增列，不自动 DROP COLUMN，防止丢失手动设置。

### 传统读写与导入契约

GetDevice、ListDevices 显式读取三列，并应用旧记录规范化；reported = 0、override = 0 时以合法 ssh_port 作为上报值。有效值始终按覆盖优先规则计算，非法字段组合安全失败。

SaveDevice 保留现有签名，规范化并校验三字段后在同一条 INSERT／ON DUPLICATE KEY UPDATE 中写入，不得仅存有效值；校验失败不能写入任何属性。该方法用于完整持久化，保留合法覆盖值；它与 Registry.Save 接收事实上报且保护手动字段的语义不同。

文件到 MySQL 导入沿用现有接口及迁移流程，源文件中的三个端口字段完整转存；旧文件自动模式与旧自定义端口不变。目标已有用户或设备时继续拒绝覆盖导入。数据库错误必须使导入返回失败，不能报告迁移成功；保留现有部分导入行为，不将本任务扩展为整体迁移事务重构。

补充允许路径：internal/store/mysqlstore/schema.go、internal/store/mysqlstore/mysqlstore.go、internal/store/mysqlstore/mysqlstore_test.go、internal/store/contract_test.go。仅修改端口字段适配及其测试，不改其他存储契约。旧服务端回退可能丢弃快照中的未知字段，因此回退需先停止写入并备份快照，不能承诺覆盖值无损保留。

## 界面结构与对齐契约

沿用已核实的 .detail-section-settings 容器，在基本设置之后、危险区域之前加入独立端口设置卡片。不使用未核实的既有元素 ID；新增元素标识由实施测试共同定义。

卡片内先显示有效值与来源，再显示上报值、带可见标签的数字输入和保存／恢复按钮，最后显示反馈。宽屏标签与输入对齐，窄屏纵向排列；在 375px、768px、1440px 视口下无横向页面溢出、遮挡或按钮重叠。输入与按钮支持键盘操作、可见焦点和错误关联；恢复按钮在自动模式禁用。轮询保留输入与焦点，不扰动危险区域操作。

## 测试策略与可追溯验收

先写测试，断言来自本方案；以下为验收策略，实际执行状态与证据单独记录于实施证据章节。

| 成功标准 | 必须执行的直接证据 |
| --- | --- |
| 自定义端口可设置、立即复制 | 真实 HTTP API 响应 → 前端解析 → 状态 → 渲染 → 点击 → 剪贴板内容断言；卡片和详情分别验证 2222 |
| 自动／手动／恢复符合矩阵 | 领域、注册表、API 测试覆盖 1、22、65535、覆盖期间持续上报及恢复最新值 |
| 无非法或越权写入 | null、类型错误、0 恢复、越界、混合字段失败、只读共享用户、跨设备 ID、设备 Token 反例；断言所有字段与持久状态不变 |
| 并发无覆盖与脏状态 | 可控阻塞交错覆盖设置／上报／恢复／重注册，-race；写入失败回滚、重启读取与两个设备隔离 |
| 保存失败及页面切换安全 | HTTP 拒绝、网络失败、切换设备、重复点击、轮询输入保留；断言反馈、最终状态和回调执行，无未捕获异常 |
| 持久化与存量兼容 | 旧 JSON、文件快照、MySQL 快照读写与重启；真实 MySQL 服务验证，不以 mock 代替数据库协议；覆盖旧表加列、旧端口 22／2222 回填、新表、重复迁移、中途仅一列存在后重启恢复、结构不兼容／非法旧值拒绝、覆盖值读写、旧与新文件导入及目标非空拒绝覆盖；逐字段断言无无关属性变化 |
| 主动 SSH 消费有效值 | SSH 探测／扫描／连接参数验证，手动端口变更不触发自动同步；不宣称修改后端口必定可达 |
| 布局与交互可靠 | 真实端口浏览器在三个视口测量边界、溢出、点击命中、键盘焦点，保存截图及断言输出 |

实施门禁：有效变更清单、范围检查、架构检查、新增功能测试、基于真实端口和协议的全量 go test -race ./...、./scripts/check-diff-coverage.sh HEAD 60、UTF-8 无 BOM、git diff --check。现有范围与架构脚本分别为 scripts/check-change-scope.sh 和 scripts/check-architecture-deps.sh，执行前核实参数。测试报告记录命令、结果与证据位置；任何必需能力不可用则阻断验收。

本任务不涉及安装、升级流程、外部 HTTP 服务或新增性能指标，不据此扩展到发布验收；部署必须另获明确授权。代码通过门禁后本地提交 dev，再单独说明部署版本、目标、影响、回退及生产验证计划。

## 设计审查与下一阶段门禁

自查已识别并纳入：客户端覆盖、Get／Save 在途竞态、旧字段迁移、主动 SSH 影响、只读权限及快照回退风险。原方案已获用户确认；MySQL 路径核查发现传统导入会丢失新增字段，已按用户授权补充本节及变更范围。MySQL 补充及 can_edit_ssh_port 契约已获用户确认，进入实施。

本次 MySQL 兼容补充、四个新增允许路径及设备级权限字段已获用户确认。原方案公开契约及主动 SSH 影响不变。后续发现还需超范围修改时仍须停止并重新确认。设计、实施与验收状态仅随真实授权和证据分别更新。

## 实施证据与完成门禁

2026-10-07：设备级权限字段与客户端版本联动调整已获用户确认；候选版本为服务端 v0.6.54、Agent v0.6.23。版本调整后的完整质量门禁已实际执行并通过，证据目录为 /private/tmp/homeagent-ssh-port-v0654-v0623-gate，基线提交为 824745e4278543ef9b810e00f36f02f230f3fb1d。

| 验证 | 实际结果与证据 |
| --- | --- |
| 基础功能与非法输入 | TestSSHPortNormalizationContract、TestSSHPortAPISettingsAndFacts：设置、上报、恢复、null／类型／范围错误及上报越权字段反例通过 |
| 权限 | TestSSHPortPermissionMatchesPATCH：read／operate／manage／viewer、不可见设备 404、拒绝写入无副作用通过 |
| 在途更新与回滚 | TestSSHPortOverrideSurvivesStaleFactsAndRestart、TestSSHPortConcurrentSettingsAndFailureRollback：旧对象不复活覆盖值、并发、设备隔离及真实文件写入失败回滚通过 |
| 存量及重启 | TestSSHPortLegacyRegistryMigration、TestSSHPortSnapshotRestart、TestSSHPortClaimRecoveryPreservesManualSetting：旧文件初始化、快照重启及硬件身份恢复通过 |
| 真实 MySQL | TestSSHPortMySQLMigrationAndRoundTrip、TestSSHPortMySQLLegacyDDLRecovery、TestStore_AutoMigrationFromFileStoreToMySQL：旧表／部分 DDL 恢复、幂等、不兼容结构／非法旧值拒绝、三字段读写、真实快照及文件导入通过 |
| 前端状态与失败 | ssh-port-settings.test.mjs：保存／恢复、错误反馈、重复点击、跨设备导航、旧列表响应及响应设备 ID 不匹配反例通过 |
| 真实浏览器 | TestSSHPortRealBrowserAndAPI：真实 API 登录会话、375／768／1440px 几何边界／溢出／命中／对齐、Tab 焦点、保存与恢复、两处清空剪贴板后点击复制、无 Console error／未捕获异常通过 |
| 全量回归 | 完整门禁实际执行 go test -count=1 -race -coverprofile=/private/tmp/homeagent-ssh-port-v0654-v0623-gate/coverage.out ./...，环境 GOFLAGS=-p=1，退出码 0；日志见证据目录 logs/regression.log。包串行用于隔离现有共享测试库的清理操作，并未跳过测试 |
| 新增主动 SSH 验收 | TestSSHPortActiveSyncUsesManualPort：真实 OpenSSH 探测／扫描／连接与 apply-keys stdin，通过；错误端口安全失败且无远端工作 |
| 差异覆盖率 | COVERAGE_PROFILE=/private/tmp/homeagent-ssh-port-v0654-v0623-gate/coverage.out ./scripts/check-diff-coverage.sh HEAD 60：88.5%（131/148），通过 |
| 范围、编码与空白 | 22 个文件均在 allowed_paths 内；UTF-8 无 BOM；git diff --check 与逐个新增文件检查零告警 |
| 集成质量门禁 | GOFLAGS=-p=1 ./scripts/quality-gate.sh --base HEAD --change changes/ssh-port-settings.yaml --result-dir /private/tmp/homeagent-ssh-port-v0654-v0623-gate：preflight、scope、static、frontend、module、regression、coverage 全部 passed；总体退出码 0，结果见 result.json |

### 真实协议与测试替身说明

本地测试 MySQL 服务实测为 8.0.46，TCP 端口为 13306；INFORMATION_SCHEMA 实测旧 ssh_port 为 int、NOT NULL、默认 22。迁移测试在独立临时数据库执行实际建表、加列、回填与重复迁移，完成后删除自身创建的数据库；传统 CRUD 与快照测试使用现有 homeagent_test 测试库。未操作生产数据库。

真实浏览器由 Chrome 启动隔离用户目录，对实际 Server.Handler 和文件注册表执行 GET／PATCH；Node 单元测试使用相同响应契约的 DOM 替身，只证明状态和逻辑，不用于证明几何布局。375／768／1440px 截图保存于系统临时目录 homeagent-ssh-port-<宽度>.png。

主动 SSH 使用实测 OpenSSH_10.3p1，并通过实际 TCP 与 x/crypto/ssh 协议端点交互。该端点仅接收临时生成的测试公钥及 apply-keys 命令、记录 JSON stdin，不模拟设备文件权限、sshd 配置或真实公钥安装；因此不宣称完成真实设备安装验收。没有 HTTP 重定向链；本任务不发布版本或执行升级。

浏览器直接发现并已回归修复两项相关问题：长所有者 ID 撑宽设置页（布局实现问题，局部换行修复）；详情复制按钮调用未暴露的 copyToClipboard（回调实现问题，改为专用回调，并以清空剪贴板后点击及零异常断言防止误通过）。无需修改跨任务规则。

### 客户端版本联动审查结论

直接证据：go list -deps ./cmd/homeagent-agent 包含 internal/device、internal/store、internal/registry；这些共享代码本次有修改。internal/qualitygate/clientver.go 将客户端依赖文件变更视为客户端行为变更，调整前门禁曾报错：候选 Agent v0.6.22 未高于基线 v0.6.22。调整至 v0.6.23 后，该门禁已实际通过，见证据目录 logs/static.log。

责任分类为设计对共享依赖与版本联动评估不完整。用户已明确确认调整为服务端 v0.6.54、Agent v0.6.23；版本源与测试路径在现有清单内，不弱化版本门禁。新增服务端设置字段在 Agent 默认注册对象中使用 omitempty，并有 TestSSHPortLegacyAgentPayloadOmitsServerSettings 验证，不向旧服务端发送零值设置字段。

本轮已按确认版本完成完整质量门禁；全部通过后提交 dev。本地提交证据以本次 Git 提交和文件清单为准。部署仍须单独授权；未部署并完成生产验证前，不宣称完整交付。

## 全部新增与修改文件

- `changes/ssh-port-settings.yaml`
- `docs/ui/SSH连接端口设置设计方案.md`
- `internal/api/api.go`
- `internal/api/api_test.go`
- `internal/device/device.go`
- `internal/device/device_test.go`
- `internal/registry/registry.go`
- `internal/registry/registry_test.go`
- `internal/store/contract_test.go`
- `internal/store/controlplane_service.go`
- `internal/store/controlplane_service_test.go`
- `internal/store/filestore/controlplane_test.go`
- `internal/store/mysqlstore/mysqlstore.go`
- `internal/store/mysqlstore/mysqlstore_test.go`
- `internal/store/mysqlstore/schema.go`
- `internal/ui/embed_test.go`
- `internal/ui/static/js/devices/actions.js`
- `internal/ui/static/js/devices/render.js`
- `internal/ui/static/style.css`
- `internal/ui/testdata/ssh-port-settings.test.mjs`
- `internal/version/version.go`
- `internal/version/version_test.go`
