# 服务端与设备 IPv6 纯文本接口与 ddns-go 解耦方案

## 1. 状态与范围

- 设计状态：已完成设计审查并获用户确认。
- 实施状态：已实施完成。
- 验收状态：变更范围、架构与版本依赖、全量 `-race` 回归与 Diff Coverage（84.7%）均已通过。
- 变更清单：`changes/server-ipv6-plaintext-endpoint.yaml`。

本方案旨在将 HomeAgent 专注于“**精准探测与拓扑裁决 IPv6 地址**”，对外提供标准、纯文本、无回环死锁的 HTTP 接口，供外部 `ddns-go` 专职调用以更新任意 DNS 服务商（Cloudflare、阿里云、腾讯云、华为云等），实现架构彻底解耦。

## 2. 问题与证据边界

在此前的架构中，服务端直接集成了 Cloudflare 更新逻辑，但在同机部署（中心宿主机 Mac mini 上同时运行 `homeagent-server` 与 `homeagent-agent`）以及依赖外部 `ddns-go` 时存在以下边界缺陷：
1. **死锁依赖**：服务端公网 IPv6 过去通过普通设备上报接口（`GET /api/v1/devices/{id}/ipv6`）提供给外部。当前缀突变导致公网域名失效时，同机 Agent 连不上公网 Server，该接口长期返回旧 IP，外部 `ddns-go` 误以为 IP 未变化，导致公网入口永远无法恢复；
2. **凭据强耦合**：服务端原生自举采集器（`AutoCollector`）仅在配置了 Cloudflare 凭据时才被装配；若用户希望完全由 `ddns-go` 推送第三方 DNS，HomeAgent 将因缺少 Cloudflare 配置而无法装配自举探测器；
3. **接口鉴权阻碍**：现有设备 IPv6 查询接口需要强制 Bearer Token，本地回环环境下的 `ddns-go` 难以配置动态凭据，Token 失效会导致更新中断。

## 3. 目标与非目标

目标：
1. 服务端无条件装配物理网卡与内核默认路由自动采集器，不依赖 Cloudflare 配置；
2. 提供服务端原生实时 IPv6 纯文本接口 `GET /api/v1/server/ipv6`，以 `text/plain` 单行输出当前物理网卡最优稳定全球单播 IPv6（GUA）；
3. 接口对本地回环地址（`127.0.0.1`、`::1`）免密直通，对非回环访问强制校验只读权限（支持 Header 或 `?token=xxx`）；
4. 优化设备 IPv6 纯文本接口 `GET /api/v1/devices/{id}/ipv6`，支持回环免密与 Query Token，输出与路由器有效前缀交集后的稳定地址；
5. 同机 `ddns-go` 可通过配置“通过接口获取”无锁拉取最新 IP，消除公网回环死锁。

非目标：
1. 不在 HomeAgent 内部适配除 Cloudflare 以外的其他 DNS 服务商 SDK；
2. 不替代 `ddns-go` 的 Webhook、多域名解析及 TTL 策略；
3. 不破坏现有 Web 控制台的配置自更新与预检闭环。

## 4. 公开契约与接口设计

### 4.1 服务端自身 IPv6 接口

- **路径**：`GET /api/v1/server/ipv6`
- **处理流程**：
  1. 鉴权：检查请求客户端 IP。若来源为本地回环（`127.0.0.1` 或 `::1`），直接放行；若为外部网络，验证用户会话或 `PermInstanceSettingsRead` 权限（支持 `Authorization: Bearer <token>` 或 `?token=xxx`）；
  2. 探测：调用注入的 `DefaultIPv6Detector`（即 `AutoCollector`）；
  3. 解析内核默认出站路由与物理网卡，筛选排除 ULA、link-local、temporary、deprecated 等无效地址；
  4. 响应：
     - 成功（HTTP 200）：`Content-Type: text/plain; charset=utf-8`，单行 IPv6 地址（如 `2001:db8:1234:5678::1\n`）；
     - 失败（HTTP 503）：`Content-Type: text/plain; charset=utf-8`，纯文本错误信息（如 `no valid global unicast ipv6 address found on interface`）。

### 4.2 设备 IPv6 接口优化

- **路径**：`GET /api/v1/devices/{id}/ipv6`
- **处理流程**：
  1. 鉴权：本地回环直接放行，外部网络校验 `PermDevicesRead` / Device Token（支持 Header 或 `?token=xxx`）；
  2. 获取设备网络状态，取与有效前缀交集后的 `DesiredAddress`；
  3. 响应：
     - 成功（HTTP 200）：`Content-Type: text/plain; charset=utf-8`，单行 IPv6 地址；
     - 未找到/未就绪（HTTP 404）：纯文本错误提示。

## 5. 状态与失败路径

| 场景 | 状态码 | 响应内容 | 影响范围 |
|---|---|---|---|
| 探测成功且有稳定 IPv6 | 200 OK | 单行 IPv6 | ddns-go 正常更新 |
| 本地无默认 IPv6 路由 / 断网 | 503 Service Unavailable | 纯文本错误 | ddns-go 拒绝解析为有效 IP，保持旧 DNS，不写脏数据 |
| 默认路由为虚拟接口（VPN 等） | 503 Service Unavailable | 纯文本错误 | 安全失败，不泄露或更新虚拟内网 IP |
| 外部未授权访问 | 401 Unauthorized | JSON/文本未授权 | 阻止外部未授权探测 |
| 设备离线或前缀过期 | 404 Not Found | 纯文本未找到 | ddns-go 跳过该记录 |

## 6. 兼容性与外部依赖

- 保持现有 Web 控制台 `GET/PUT /api/v1/server/network`、`candidates`、`validate` 接口完全兼容；
- 无新增外部依赖，完全复用 `net/netip`、`internal/servernetwork` 与 `internal/networkaddr`；
- 修改服务端核心逻辑，升级服务端版本号至 `v0.6.22`，客户端保持不变。

## 7. 验收与测试策略

1. 单元测试覆盖：
   - 回环免密请求（IPv4 回环与 IPv6 回环）；
   - 外部请求携带 Token（Header 与 Query 参数）；
   - 外部请求无 Token 拒绝（401）；
   - 探测成功与失败响应格式契约（`text/plain` 与换行符）；
   - `main.go` 在缺少 Cloudflare 配置时采集器的装配验证。
2. 全量 `-race` 回归与 Diff Coverage $\ge 60\%$。
