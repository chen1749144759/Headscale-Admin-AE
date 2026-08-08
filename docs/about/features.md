# 功能范围

Headscale-Admin-AE 是 ScaleTail/ScaleForge 专用控制面，不承诺兼容官方 headscale 的
全部认证和远程管理方式。

## 已支持

- ScaleForge 账户名/密码统一身份。
- 90 天密码有效期、账户禁用、账户到期和强制改密。
- ScaleTail 在 TS2021 Noise 会话内完成节点账户证明。
- PostgreSQL 与 SQLite 增量迁移。
- ACL、MagicDNS、子网路由、出口节点和网络地图。
- ScaleForge 私有 Unix socket 管理边界。
- 客户端上报、策略领取和更新检查的私有 socket 转发。
- 内置 DERP、STUN 与已注册客户端校验。
- 节点数量限制、注册并发保护和旧认证节点到期迁移。

## 明确不支持

- 预认证 Key / auth key。
- Headscale API Key。
- OIDC 与浏览器授权。
- Auth ID 手工注册/批准。
- 公网 REST 管理 API、Swagger 和远程 gRPC 管理。
- 账户认证节点的身份标签。
- 未实现 ScaleTail 账户协议的官方 Android、Apple 或 Windows 客户端。

历史数据库表、Go 类型和 protobuf 定义可能仍包含旧功能名称，它们只用于迁移和结构
兼容，不是可调用的产品能力。
