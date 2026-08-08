# 私有管理接口

Headscale-Admin-AE 不提供可从公网访问的 REST 管理 API 或远程 gRPC 管理端口。
公网 HTTP 监听器只承载 ScaleTail 控制协议、健康检查、版本、客户端更新检查和启用后的
内置 DERP。

## 接口边界

| 接口 | 传输 | 调用方 | 是否可公开 |
|---|---|---|---|
| `unix_socket` | Unix gRPC | 同主机受信任管理员 | 否 |
| `scaleforge.socket` | Unix HTTP | ScaleForge 后端 | 否 |
| `scaleforge.backend_socket` | Unix HTTP | Headscale 转发已认证节点请求到 ScaleForge | 否 |
| 主 HTTP 监听器 | HTTPS/Noise | ScaleTail 节点 | 仅公开控制协议 |

`scaleforge.socket` 由 Headscale 提供，并通过私有 gRPC gateway 调用本机
`unix_socket`。认证边界是 Unix socket 文件权限、容器用户组和挂载范围，不是共享
API Key。ScaleForge 浏览器会话使用单独的账户 session，不能直接访问该 socket。

## API Key 已禁用

- `headscale apikeys` CLI 命令已移除。
- `CreateApiKey`、`ExpireApiKey`、`ListApiKeys`、`DeleteApiKey` 均返回 gRPC
  `FailedPrecondition`。
- `/api/v1`、Swagger 和远程 gRPC 不挂载到公网路由。
- `HEADSCALE_CLI_API_KEY` 不属于支持的部署配置。

数据库模型、历史表和 protobuf 消息仍然存在，仅用于旧数据库迁移和保持已发布 schema
顺序稳定。保留这些结构不代表 API Key 功能可用。

## 本机管理

需要运行维护命令时，在 Headscale 主机或容器内通过 `unix_socket` 执行：

```bash
headscale users list
headscale nodes list
```

不要扩大 socket 权限到 `0777`，不要把 socket 放入所有容器共享的目录，也不要用
TCP 转发、socat 或反向代理把它暴露出去。

## ScaleForge 接入

组合部署应满足：

1. Headscale 与 ScaleForge 后端共享 `scaleforge.socket` 所在目录。
2. 只有两个服务的运行用户/组对 socket 具有读写权限。
3. 前端和公网代理都不能挂载或访问该目录。
4. `scaleforge.backend_socket` 使用独立路径，不能与另外两个 socket 重合。
5. Bootstrap 密码通过 secret 文件提供，不作为接口凭据复用。

任何需要新增的管理能力都应在私有 ScaleForge 接口中显式实现和授权，不应恢复 API
Key 或公网管理 API。
