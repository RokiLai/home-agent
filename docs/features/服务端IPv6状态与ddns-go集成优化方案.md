# 服务端 IPv6 状态与 ddns-go 集成优化方案

## 1. 状态与范围

- 设计状态：已完成设计审查并获用户确认。
- 实施状态：已实施完成。
- 验收状态：变更范围、架构与版本依赖、全量 `-race` 回归与 Diff Coverage（94.9%）均已通过。
- 变更清单：`changes/simplify-server-ipv6-ddns-ui.yaml`。

本方案针对解耦外部 `ddns-go` 后的 Web 控制台“IPv6 DDNS”面板进行精简优化，消除候选网络探测对 Cloudflare Token 凭据的强耦合，移除无意义的手动域名输入与写权限表单，提供即开即用的 IPv6 状态感知与外部直通接口集成引导。

## 2. 问题与证据边界

### 2.1 证据与触发路径
- **用户现象**：在 Web 控制台设置页的“服务端 IPv6 DDNS 自举”面板中，点击「重新探测」按钮无响应，卡片底部显示 `not_configured`，网卡显示 `-`，IPv6 显示 `未解析`。
- **直接证据**：
  - 代码 `internal/api/api.go` 的 `getServerNetworkCandidates`：当 `s.ServerNetworkCoordinator == nil` 时直接返回 HTTP 501 `{"status": "error", "errors": ["not_configured"]}`；
  - `cmd/homeagent-server/main.go` 仅在检测到环境变量 `HOMEAGENT_CLOUDFLARE_TOKEN` 时才会初始化 `ServerNetworkCoordinator`；未配置 Cloudflare 时该协调器为 `nil`；
  - 前端 `internal/ui/static/js/settings.js` 中 `detectServerNetwork` 捕获到非 200 响应后仅将文本重新刷新为 `-`、`未解析`、`not_configured`，缺乏 Toast 提醒，导致用户体感“点击无反应”。

### 2.2 架构演进与冗余
- 此前已确定《服务端与设备IPv6纯文本接口与ddns-go解耦方案》，由 `ddns-go` 专职处理外部 DNS 服务商同步，HomeAgent 只需输出本机稳定公网 IPv6；
- 控制台设置页仍保留旧版“受管域名输入框”、“所有权确认勾选框”与“保存自举配置”按钮，不仅冗余而且误导用户。

## 3. 目标与非目标

### 目标：
1. 解除候选探测接口 `GET /api/v1/server/network/candidates` 对 `ServerNetworkCoordinator` 的强依赖；在缺少 Cloudflare 配置时，无缝使用 `s.ServerIPv6Collector` 进行本机网卡与 IPv6 探测；
2. 服务端状态接口 `GET /api/v1/server/network` 在无 Coordinator 时返回采集器探测状态，不再阻断前端展示；
3. 精简前端 `#settingsSecNetwork` 面板：
   - 移除受管域名、所有权确认勾选框与保存按钮；
   - 增强「重新探测」按钮交互与即时 Toast 反馈；
   - 新增外部 `ddns-go` 集成卡片，动态展示纯文本接口 URL（`/api/v1/server/ipv6`）并提供一键复制与使用引导；
4. 升级服务端版本号至 `v0.6.27`（客户端保持 `v0.6.16`）。

### 非目标：
1. 不破坏服务端现有 `PUT /api/v1/server/network` 及 `validate` 兼容性（保留 API 存在）；
2. 不引入新的外部依赖库。

## 4. 公开契约与状态设计

### 4.1 后端 API 契约
- **`GET /api/v1/server/network/candidates`**：
  - 若 `s.ServerNetworkCoordinator != nil`，维持既有逻辑；
  - 若 `s.ServerNetworkCoordinator == nil`：
    - 使用 `s.ServerIPv6Collector`（若为空则自动装配 `AutoCollector`）执行 `Detect(ctx, netip.Addr{})`；
    - 成功时返回 HTTP 200：
      ```json
      {
        "status": "ready",
        "resolved_interface": "en0",
        "resolved_address": "240e:...",
        "candidates": [{"interface": "en0", "is_default_route": true, "addresses": [{"address": "240e:..."}]}],
        "record_candidates": []
      }
      ```
    - 探测无有效地址时返回 HTTP 200 `{"status": "no_address", "errors": ["no valid global unicast ipv6 address found on interface"]}`；
    - 不再返回 HTTP 501 `not_configured`。

- **`GET /api/v1/server/network`**：
  - 若 `s.ServerNetworkCoordinator == nil`：
    - 返回 HTTP 200：
      ```json
      {
        "configured": true,
        "enabled": true,
        "status": "standalone_detector"
      }
      ```

### 4.2 前端容器与界面契约
- **容器层级**：`#pageSettings > .settings-container > #settingsSecNetwork.content-panel.settings-section`
- **面板标题**：`服务端 IPv6 与 DDNS 接口`
- **面板副标题**：`从宿主机物理网卡直接采集公网稳定 IPv6，为外部 ddns-go 提供本地免密直通接口，消除回环死锁`
- **状态徽章**：`#serverNetworkStatusBadge`
  - 探测成功：`badge badge-success`，文本为“探测正常”；
  - 无 IPv6：`badge badge-warning`，文本为“无有效 IPv6”；
  - 错误：`badge badge-danger`，文本为“探测失败”。
- **探测卡片**：
  - 默认路由接口：`#serverNetworkResolvedInterface`
  - 稳定公网 IPv6：`#serverNetworkResolvedAddress`
  - 说明文案：`#serverNetworkDetectionMessage`
  - 重新探测按钮：`#serverNetworkRedetectBtn`（点击触发 `detectServerNetwork`，成功弹出 Toast“网络探测完成”，失败弹出具体错误 Toast）。
- **外部集成卡片（新增）**：
  - 标题：`外部 ddns-go 同步接口`
  - 接口输入展示：`#serverIPv6EndpointInput`（只读，动态拼接当前 host 的 `/api/v1/server/ipv6`）
  - 复制按钮：`#copyServerIPv6EndpointBtn`（点击写入系统剪贴板，成功 Toast“已复制接口地址”）
  - 指引文案：`在同机运行的 ddns-go 中，添加需要更新的公网域名，IPv6 获取方式选择「通过接口获取」并填入此 URL 即可。`

## 5. 失败与异常路径

| 场景 | 后端行为 | 前端行为 | 影响边界 |
|---|---|---|---|
| 缺少 Cloudflare Token | 返回探测所得网卡与 IPv6 | 正常展示网卡与 IPv6，显示绿色徽章 | 彻底解除对 Cloudflare 依赖 |
| 本地无默认 IPv6 路由 | 返回 `status: no_address` | 地址显示“未解析”，黄色徽章，Toast 提示无有效 IPv6 | 安全失败，用户可检查网络 |
| 复制接口地址失败 | 无（前端本地交互） | Toast 提示“复制失败，请手动复制” | 兜底友好容错 |

## 6. 测试与验收策略

1. 单元测试：
   - 验证在 `ServerNetworkCoordinator == nil` 时，`GET /api/v1/server/network/candidates` 仍能探测返回 200 与正确的网络接口和地址；
   - 验证无有效地址时返回 `no_address` 状态；
   - 负例测试：验证在缺少 Collector 时自动装配并优雅处理错误。
2. 前端与集成测试：
   - `internal/ui/embed_test.go` 验证 `#settingsSecNetwork` 结构完整性、移除项与新增项的 DOM 契约断言；
   - 验证重新探测按钮与复制按钮的交互绑定与 Toast。
3. 全量门禁：
   - 全量 `-race` 回归；
   - Diff Coverage 不低于 60%；
   - `git diff --check` 零告警。
