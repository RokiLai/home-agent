# Cloudflare 测试替身来源

这些 JSON 夹具源自 2026-09-28 对生产 Zone `rokilai.online` 的真实只读及专用临时记录请求，已移除 Token、账户 ID、Zone ID、记录 ID、Ray ID 和时间戳等实例标识。

- 账户 Token 使用 `/accounts/{account_id}/tokens/verify`；旧 `/user/tokens/verify` 对账户 Token 返回 `401 / code 1000`。
- DNS 记录成功响应保留真实字段结构；测试中的域名、记录 ID 与 IPv6 使用不可用示例值。
- `401/429` 夹具保持 Cloudflare 的 `success/errors/messages/result` 外形，错误内容不由当前实现反向构造。
- 本地 HTTP 替身不模拟 Cloudflare 边缘传播、Anycast 或 TLS；权威 DNS 收敛由独立解析器替身及真实验收覆盖。
