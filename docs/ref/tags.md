# 身份标签

当前账户密码模式不支持节点身份标签。

- ScaleTail 账户节点始终归属于账户绑定的用户网络。
- 客户端携带 `--advertise-tags` 的账户注册请求会被拒绝。
- `SetTags` 管理 RPC 返回 `FailedPrecondition`。
- 预认证 Key、浏览器批准和手工注册不能用于创建 tagged node。

历史节点的 tags 字段、ACL 中的 tag 语法和相关 protobuf 仍可能存在，用于数据库迁移
和读取旧数据；它们不构成当前可用的节点注册方式。需要服务身份分组时，应在
ScaleForge 的账户/分组和 ACL 策略中建模，不要恢复旧认证入口。
