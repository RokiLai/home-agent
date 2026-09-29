# 设备域名绑定 Owner 权限与操作入口缺失修复设计方案

## 1. 文档状态

| 阶段 | 状态 | 说明 |
|---|---|---|
| 设计 | 已确认 | 2026-09-29 用户确认按本文执行 |
| 实施 | 进行中 | `v0.6.47` 已部署；生产恢复发现禁用绑定缺少 DNS 记录时无法重新启用，正在修复 |
| 验收 | 进行中 | 本地质量门禁与 `v0.6.47` 部署通过；生产绑定恢复尚未通过 |

对应设计变更清单为 `changes/fix-domain-binding-owner-permission-ui-design.yaml`，实施变更清单为 `changes/fix-domain-binding-owner-permission-ui.yaml`。

## 2. 背景与直接证据

### 2.1 用户现象

系统中存在两个角色均为 `owner` 的账号。设备 `xiaoqiang-7d87e3ec` 归属于其中一个 Owner，而另一个 Owner 在设备详情页为该设备创建 `router.rokilai.online` 绑定后，接口先返回 `enabled / waiting_report`，后台调和随后将绑定自动转为 `disabled / waiting_report`，Cloudflare 未创建 AAAA 记录。

设备详情页对 `disabled` 绑定只显示状态，不提供重新启用、禁用或删除入口，用户无法从 Web 完成恢复。

### 2.2 生产证据

2026-09-29 的只读生产核验确认：

- Cloudflare 配置状态为 `ready`，不是凭据、Zone 或发布器未配置；
- 设备已上报可裁决 IPv6，`desired_address` 为 `240e:390:9a9:c220::1`，不是缺少地址；
- 初始绑定 `binding-1790612596671597000` 的创建用户为 `roki`，设备当时 `owner_user_id` 为空，后台将绑定从 revision `1` 自动推进为 revision `2` 的 `disabled / waiting_report`；
- 修正设备归属并删除旧绑定后，使用另一个 Owner 账号 `admin` 创建的新绑定 `binding-1790654772331697000` 返回 `enabled / waiting_report`，但绑定 Owner 与设备 Owner 不同，仍会命中同一后台权限拒绝路径；
- 删除绑定只产生控制面墓碑，不会删除 Cloudflare DNS 记录；本次预检确认 Cloudflare 中不存在 `router.rokilai.online` 记录。

会话 Cookie、密码、Cloudflare Token 和完整认证响应不得写入本文、日志或测试夹具。

### 2.3 代码执行链证据

公开请求鉴权与后台调和使用了不同授权规则：

1. `internal/auth/authorizer.go` 对设备资源明确规定：活跃 `Owner` 只要目标设备存在且角色包含所需权限，即可访问和操作全部设备，不要求成为设备所有者或持有设备 Grant。
2. `internal/api/domain_binding_handlers.go` 在创建绑定时把当前会话用户 ID 保存为绑定 `owner_user_id`。
3. `cmd/homeagent-server/main.go` 的 `bindingSourceResolver.DesiredIPv6` 不读取绑定 Owner 的账号角色，只检查该用户是否为设备所有者，或是否持有包含 `devices.update` 的设备 Grant。
4. `internal/domainbinding/coordinator.go` 把上述拒绝映射为 `ErrSourceUnauthorized`，并立即将启用绑定转为 `disabled`。
5. `internal/ui/static/js/devices/actions.js` 只为 `observing` 状态渲染“确认接管”，未为 `enabled`、`disabled` 或失败状态提供禁用、重新启用和删除入口；后端对应接口已经存在。

因此根因不是 Cloudflare 或设备上报失败，而是后台地址源权限未复用统一 RBAC 契约，加上 Web 生命周期操作入口不完整。

## 3. 责任分类

| 分类 | 结论 | 修改载体 |
|---|---|---|
| 设计契约 | 原 DDNS 设计只描述“绑定 Owner 失去设备权限后自动禁用”，未明确系统 Owner 的全局设备权限优先级 | 本文补充完整权限矩阵、状态和恢复契约 |
| 实现偏差 | API Authorizer 允许 Owner 管理全部设备，后台 Resolver 却按普通用户设备范围判定 | 修复后台权限解析并复用统一语义 |
| 测试缺失 | 未覆盖“两个系统 Owner、设备归属不同、非设备 Owner 创建绑定”的正例 | 增加 Resolver、协调器与 API 组合回归测试 |
| UI 缺失 | 后端已有 enable、disable、delete 接口，设备详情页未暴露完整生命周期操作 | 补齐状态相关按钮、确认与错误反馈测试 |

本问题不需要修改 `AGENTS.md`：现有规则已要求状态矩阵、授权契约、负例和真实执行链验证，缺口属于本功能设计、实现和测试范围。

## 4. 目标与非目标

### 4.1 目标

1. 使设备域名绑定后台调和与公开 API 使用一致的角色和设备范围语义。
2. 活跃系统 `owner` 可为任意存在且可用的设备创建并持续调和域名绑定，不因设备 `owner_user_id` 不同而被自动禁用。
3. 非 Owner 用户仍必须是设备所有者，或持有满足 `devices.update` 的设备 Grant；撤销权限后现有安全禁用行为保持不变。
4. 设备详情页提供启用、禁用和删除绑定的完整操作入口，所有写操作基于绑定 revision，避免覆盖并发变化。
5. 禁用和删除操作明确提示不会删除 Cloudflare AAAA 记录。
6. 为本次生产异常提供可回滚、可追溯的数据恢复步骤。

### 4.2 非目标

1. 不改变 Cloudflare Token、Zone、TTL、代理状态或 DNS 记录删除策略。
2. 不把设备所有权概念删除或合并到角色；设备所有权仍用于普通用户归属、共享、审计和转移。
3. 不允许 `admin` 或 `viewer` 仅凭角色绕过设备所有权和 Grant。
4. 不自动迁移全部历史设备归属，也不自动删除现有绑定墓碑。
5. 不修改域名、IPv6 选择、一个绑定一个 AAAA、受管后缀和 CNAME 排除契约。

## 5. 授权契约

### 5.1 角色与设备范围矩阵

后台调和必须根据绑定保存的 `owner_user_id` 查询当前账号状态和角色，再判定是否允许读取设备地址源：

| 绑定 Owner 状态 | 设备状态 | 设备关系 | 结果 |
|---|---|---|---|
| 活跃 `owner` | 存在 | 任意归属 | 允许调和 |
| 活跃 `admin` | 存在 | 设备所有者 | 允许调和 |
| 活跃 `admin` | 存在 | 持有满足 `devices.update` 的 Grant | 允许调和 |
| 活跃 `admin` | 存在 | 无所有权且无足够 Grant | 拒绝并自动禁用 |
| 活跃 `viewer` | 存在 | 任意 | 拒绝并自动禁用 |
| 账号不存在或已禁用 | 存在 | 任意 | 拒绝并自动禁用 |
| 任意账号 | 设备不存在 | 任意 | 拒绝并自动禁用 |
| `legacy-admin` 兼容绑定 | 存在 | 兼容路径 | 保持既有兼容行为，不扩大新建入口 |

系统 Owner 的允许条件必须同时满足：账号仍存在、状态为 active、角色仍为 `owner`。不得仅凭绑定中历史保存的用户 ID 或创建时角色永久授权。

### 5.2 统一授权来源

后台 Resolver 不得自行复制一套静态角色矩阵。组合根应向 Resolver 注入能够读取当前用户状态和角色的只读接口，并把判断适配为与 `auth.Authorizer` 一致的领域结果。若直接调用 Authorizer，需要构造 `devices.update + DeviceResource(device_id)` 的授权请求；若为避免循环依赖使用窄接口，测试必须证明其结果与 Authorizer 对同一输入一致。

用户查询失败、会话存储不可用或角色值异常时必须安全失败，不得把未知用户视为 Owner。安全失败映射为 `ErrSourceUnauthorized`，由现有协调器自动禁用绑定，不向 Cloudflare发起写请求。

### 5.3 所有权与系统 Owner 的边界

设备 `owner_user_id` 仍是设备归属主键，不因系统 Owner 的全局操作能力而被自动改写。系统 Owner 创建绑定时，绑定 `owner_user_id` 记录实际操作者，便于审计和后续权限撤销；后台通过当前角色判断其是否仍可访问设备源。

角色降级、账号禁用或删除后，下一轮调和必须重新授权。若该用户不再拥有设备范围权限，绑定自动进入 `disabled / waiting_report`，已有 Cloudflare AAAA 保持不动。

## 6. 状态与失败路径

### 6.1 状态矩阵

| 事件 | 前置状态 | 目标状态 | DNS 副作用 |
|---|---|---|---|
| 活跃 Owner 创建不存在的域名 | 无活动绑定 | `enabled / waiting_report`，随后进入 `syncing / synced` | 创建 AAAA |
| 活跃 Owner 创建已有记录 | 无活动绑定 | `observing / waiting_report` | 只读观察，不写 DNS |
| 非 Owner 且无设备权限 | `enabled` | `disabled / waiting_report` | 不创建、不更新、不删除 DNS |
| 点击禁用 | `enabled`、`observing` 或失败态 | `disabled / waiting_report` | 停止后续写入，不删除 DNS |
| 点击重新启用 | `disabled` | `enabled / waiting_report` | 重新执行授权和地址裁决后调和 |
| 点击删除 | 非 `deleted` | `deleted / waiting_report` | 保留 DNS，释放 FQDN 供重建 |
| revision 已变化 | 任意可写状态 | 保持原状态，返回 `409 conflict` | 零 DNS 副作用 |
| Cloudflare 失败 | `enabled / syncing` | 按既有错误分类进入 `failed` 或有界重试 | 遵循既有发布器契约 |

重新启用必须沿用现有 `publisher_disabled_confirmed=true` 契约。界面确认文案必须提醒：启用前需保证其他发布者已停止管理该 FQDN，避免双写。

### 6.2 并发与异步校验

所有按钮必须使用当前绑定 revision。API 返回后重新加载整个绑定列表和控制面 revision，不得在前端本地推测成功状态。后台在 Cloudflare I/O 完成后继续沿用绑定 revision 校验，旧任务不得覆盖禁用、删除、角色降级或权限撤销后的新状态。

批量或列表刷新结果必须来自每个绑定的独立最终状态，不得因请求受理成功显示为 `synced`。

## 7. Web 交互契约

### 7.1 行操作

设备详情页每条绑定按状态显示以下入口：

| 状态 | 主操作 | 次操作 |
|---|---|---|
| `observing` | 确认接管 | 删除 |
| `enabled`、`syncing`、`synced`、`failed`、`stale`、`waiting_report` | 禁用 | 删除 |
| `disabled` | 重新启用 | 删除 |
| `deleted` | 不展示在默认列表 | 无 |

状态判断以 `config_state` 为主，`runtime_state` 只用于展示运行结果。不得因 `waiting_report` 同时出现在多种配置状态而渲染错误按钮。

### 7.2 确认与反馈

- 重新启用：提示“确认其他 DDNS 发布者已停止管理该域名；启用后 HomeAgent 将创建或更新 AAAA”。
- 禁用：提示“停止后续同步，但不会删除 Cloudflare AAAA”。
- 删除：提示“从 HomeAgent 移除绑定，但不会删除 Cloudflare AAAA；同名域名之后可重新绑定”。
- 成功：重新加载列表，显示服务端返回的实际状态。
- `409 conflict`：提示状态已变化并自动刷新，不重复提交旧 revision。
- `404`：提示绑定已被删除或不属于当前设备并刷新列表。
- `401/403`：提示会话或权限失效，不修改本地状态。
- 网络与 `5xx`：保留当前状态并允许用户重试，不宣称操作成功。

按钮必须使用 `<button type="button">`，可由键盘聚焦和触发；危险操作使用明确文字，不只使用颜色或图标表达。

### 7.3 布局约束

绑定行保持现有 `detail-row` 容器层级：左侧为 FQDN 与状态，右侧为操作组。操作组在窄屏允许换行，不得造成页面横向溢出；按钮点击区域不得互相遮挡。浏览器验收必须在桌面和移动端验证按钮可见、可点击、确认取消零请求、确认后回调执行且列表刷新。

## 8. 公开接口与兼容性

不新增或修改后端公开路由，复用：

- `POST /api/v1/devices/{id}/domain-bindings/{binding_id}/enable`
- `POST /api/v1/devices/{id}/domain-bindings/{binding_id}/disable`
- `DELETE /api/v1/devices/{id}/domain-bindings/{binding_id}`

请求继续使用 `{ "expected_revision": number }`；启用额外使用 `publisher_disabled_confirmed: true`。成功响应继续返回完整绑定对象，错误码沿用现有 `400/404/409/412/502/503` 映射。

旧 Agent、现有绑定、Cloudflare 配置和数据库 schema 均无需迁移。权限修复部署后，已因该缺陷进入 `disabled` 的绑定不会自动启用，必须由管理员显式重新启用或删除重建，避免无确认恢复 DNS 写入。

## 9. 实施边界

预计实施文件范围：

- `cmd/homeagent-server/main.go`：组合根注入用户角色读取能力，修正设备地址源授权适配；
- `cmd/homeagent-server/*_test.go`：Owner、非 Owner、禁用账号和授权撤销测试；
- `internal/ui/static/js/devices/actions.js`：完整绑定行操作及 API 调用；
- `internal/ui/testdata/domain-binding.test.mjs`：响应到渲染、事件回调、确认、请求和刷新测试；
- 如布局确需调整，仅修改现有设备详情相关样式及对应真实浏览器测试；
- `internal/version/version.go` 与测试：仅升级 Server 版本；
- 独立实施变更清单。

若实现必须修改上述范围之外的公开授权接口、存储 schema 或设计文档，必须停止并重新审查，不得直接扩展范围。

## 10. 测试策略

### 10.1 后端测试

1. 活跃系统 Owner、设备归属其他用户：允许解析目标 IPv6。
2. 两个活跃系统 Owner，绑定创建者不是设备所有者：允许持续调和并创建 AAAA。
3. Owner 角色降级为 Admin 且无 Grant：下一轮返回 `ErrSourceUnauthorized`，绑定自动禁用，Cloudflare 零写入。
4. Owner 账号被禁用或删除：安全失败并自动禁用，Cloudflare 零写入。
5. Admin 是设备所有者：允许调和。
6. Admin 持有满足 `devices.update` 的 Grant：允许调和。
7. Admin、Viewer 无足够 Grant：拒绝并自动禁用。
8. 用户查询失败、设备不存在、角色异常：拒绝且无 DNS 副作用。
9. 角色或授权在异步 Cloudflare I/O 期间变化：旧任务不得覆盖新状态。

### 10.2 前端测试

使用真实 API 响应字段验证：

1. 每种 `config_state` 渲染正确按钮，`deleted` 不显示；
2. 启用、禁用、删除的取消确认均产生零请求；
3. 确认后请求方法、URL、revision 和启用确认字段准确；
4. 成功响应后执行列表刷新并显示服务端状态；
5. `401/403/404/409/5xx` 不误报成功，`409/404` 触发刷新；
6. 所有事件处理路径无未捕获异常；
7. 桌面、移动端和 200% 文本缩放下无横向溢出、遮挡，操作按钮可点击且焦点可见。

### 10.3 回归与门禁

- 变更范围和架构依赖检查；
- 前端语法与目标模块测试；
- 基于真实端口和协议的全量 `go test -race ./...`；
- `./scripts/check-diff-coverage.sh HEAD 60`，Diff Coverage 不低于 60%；
- UTF-8 无 BOM、`git diff --check` 和变更文件清单；
- Server 版本升级，Agent 版本不变。

## 11. 生产恢复与验收

### 11.1 本地实施证据

2026-09-29 本地验证结果：

- Resolver 授权矩阵定向测试通过，覆盖跨设备系统 Owner、设备所有者、具备 Grant 的 Admin、无 Grant Admin、Viewer、禁用 Owner、缺失用户、缺失设备和 `legacy-admin` 兼容路径；
- 设备域名绑定前端测试通过，覆盖列表响应解析、状态按钮渲染、启用/禁用/删除回调、revision 请求体、取消零请求和 `409` 错误反馈；
- `go test -count=1 -race ./...` 全量回归通过；
- `check-change-scope` 的范围、静态架构、前端语法、模块测试和全量回归门禁通过；
- Diff Coverage 为 `72.7%`，高于 `60%` 门槛；
- 独立 Chrome 布局套件存在与本次变更无关的登录认证夹具失败，现象为 `loginOverlay` 未隐藏及后续数据夹具未加载。本次新增绑定交互测试全部通过，但部署前仍需通过真实页面完成按钮可见性和点击验收。
- `v0.6.47` 部署后，`router.rokilai.online` 绑定保持 `disabled / waiting_report` revision `2`。重新启用接口在 Cloudflare 尚无同名记录时返回 `500` 且绑定状态不变。直接证据显示 `Service.Enable` 对所有状态统一要求 Provider 记录存在，而第 6.1 节契约要求禁用绑定可重新进入调和并在记录不存在时创建 AAAA；修复必须仅对 `observing` 接管路径保留记录存在检查，并增加正反例测试。

### 11.2 生产恢复步骤

修复发布并部署后，`router.rokilai.online` 按以下顺序恢复：

1. 读取并记录设备所有者、绑定 Owner、绑定 revision、Cloudflare 同名记录和期望 IPv6；
2. 保持设备当前归属不变，由任一活跃系统 Owner 显式重新启用或删除重建绑定；
3. 验证绑定不再因 Owner 与设备所有者不同而自动禁用；
4. 等待 `enabled / synced`，确认 `desired_ipv6`、`provider_ipv6` 与 `last_applied_ipv6` 一致；
5. 通过 Cloudflare API 验证记录 ID、AAAA、TTL 和代理状态；
6. 通过两个权威服务器、`1.1.1.1`、`8.8.8.8` 验证解析收敛；
7. 至少观察两个调和周期，确认状态和记录稳定；
8. 使测试会话失效，不保留会话 Cookie 或临时凭据。

若创建或同步失败，回滚为禁用 HomeAgent 绑定并保留现有 DNS；不得自动删除记录。若记录尚未创建，回滚只需删除或保持禁用绑定，不引入其他发布者，直到根因明确。

## 12. 成功标准

1. 活跃系统 Owner 可为任意存在设备创建并持续同步域名绑定，无需转移设备所有权或添加设备 Grant。
2. 非 Owner 的设备范围约束和权限撤销自动禁用行为不退化。
3. Web 可完成观察接管、重新启用、禁用和删除，所有操作带确认、revision 与真实错误反馈。
4. 禁用和删除不删除 Cloudflare AAAA，文案、实现和测试一致。
5. `router.rokilai.online` 在真实生产环境达到 `enabled / synced`，Cloudflare 与公共 DNS 返回设备裁决 IPv6。
6. 所有质量门禁、发布验收和生产验证完成前，设计、实施和验收状态不得标记为完成。
