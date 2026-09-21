# 用户列表 ID 稳定排序修复设计

| 维度 | 状态 | 说明 |
| --- | --- | --- |
| 设计状态 | 已审查 | 用户已确认按用户 ID 升序查询。 |
| 实施状态 | 已完成 | `ListUsers` 已在复制并脱敏记录后按 ID 升序排序。 |
| 验收状态 | 已通过 | 定向测试、全仓库 `-race` 回归和 Diff Coverage 门禁均已通过。 |

## 范围与契约

`GET /api/v1/users` 的用户数组由 `SessionManager.ListUsers` 提供。返回数组必须按 `User.ID` 的字典序升序排列；相同持久化状态下，重复查询必须返回相同顺序。

该排序只影响数组顺序，不修改用户、权限、登录状态或 API 字段。前端继续保留服务端返回顺序，不增加独立排序策略。

## 实施与验证

在持有 `SessionManager` 读锁期间复制并脱敏用户记录后，按 ID 升序排序。测试必须覆盖乱序插入、重复调用顺序稳定，以及密码哈希继续不出现在列表结果中。

验收证据：`go test ./internal/auth ./internal/version -count=1`、`go test -race ./...` 均通过；`./scripts/check-diff-coverage.sh HEAD 60` 报告 Diff Coverage 为 100.0%（2/2 statements）。
