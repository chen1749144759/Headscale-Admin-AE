# 快速开始

本页适用于 Headscale-Admin-AE、ScaleForge 与 ScaleTail 组合部署。官方 headscale 的
OIDC、预认证 Key、浏览器批准和 API Key 教程不适用于本分支。

## 1. 准备服务端

- 为控制地址配置客户端信任的 HTTPS 证书。
- 准备 SQLite 或 PostgreSQL，并确保数据库账户具有迁移所需 DDL 权限。
- 为 Noise 私钥、DERP 私钥和数据库目录配置持久化卷。
- 为初始 ScaleForge 管理账户准备 secret 密码文件。
- 私有挂载三个不同的 Unix socket 路径，不要映射为 TCP 端口。

推荐通过 ScaleForge 仓库提供的 Docker Compose 一起部署。升级时保留数据库和密钥
数据卷，禁止执行 `docker compose down -v`。

## 2. 检查服务

```bash
curl -fsS https://control.example.com/health
```

随后检查 Headscale 启动日志，确认账户迁移、数据库表、ACL 和 DERP map 均无错误。

本机管理命令通过 Unix socket 执行：

```bash
headscale users list
headscale nodes list
```

远程 gRPC、REST 管理 API 和 API Key 均不受支持。

## 3. 创建账户

首次启动且账户表为空时，Headscale 会使用
`scaleforge.bootstrap_username` 和 `scaleforge.bootstrap_password_file` 创建首个
管理账户。Bootstrap 密码必须来自 secret 文件。

登录 ScaleForge 后：

1. 立即修改初始密码。
2. 创建普通账户或管理账户。
3. 将账户绑定到正确网络。
4. 按需配置账户到期时间和节点数量。

密码每 90 天必须更新一次。

## 4. 连接 ScaleTail

在新版 ScaleTail 中填写：

- HTTPS 控制服务器地址。
- 账户名。
- 账户密码。
- 设备名称及需要的路由选项。

客户端通过 Noise 会话直接完成账户证明，不会打开浏览器，也不需要管理员复制 Auth
ID 或生成预认证 Key。

## 5. 验证

- ScaleForge 中账户和节点归属正确。
- `headscale nodes list` 显示节点为在线。
- 节点注册方式为 `password`。
- 节点有效期不超过账户密码期限。
- 直连、DERP 回退、子网路由和 DNS 按部署策略工作。

旧节点升级后必须重新账户登录，不能继续使用旧 Key/OIDC/手工批准流程。
