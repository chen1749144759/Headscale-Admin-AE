# Headscale-Admin-AE

[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev/)
[![Headscale Base](https://img.shields.io/badge/Base-headscale%20v0.28.0-326CE5?style=flat-square)](https://github.com/juanfont/headscale/releases/tag/v0.28.0)
[![Backports](https://img.shields.io/badge/Backports-headscale%20v0.29.1-7C3AED?style=flat-square)](https://github.com/juanfont/headscale/releases/tag/v0.29.1)
[![Tailscale](https://img.shields.io/badge/tailscale.com-v1.96.5-4D7CFE?style=flat-square)](https://github.com/tailscale/tailscale)
[![Database](https://img.shields.io/badge/Database-SQLite%20%7C%20PostgreSQL-4169E1?style=flat-square)](#核心特性)
[![License](https://img.shields.io/badge/License-BSD--3--Clause-green?style=flat-square)](LICENSE)

Headscale-Admin-AE 是基于官方 [headscale](https://github.com/juanfont/headscale) 的增强控制服务器，服务于自建 Tailscale/Headscale 网络和 ScaleForge Web 管理面板场景。

它不是一个独立的 Web 面板。它负责运行控制面、注册客户端、下发网络地图、管理节点和路由；[ScaleForge](https://github.com/chen1749144759/ScaleForge) 负责提供图形化管理界面；[ScaleTail](https://github.com/chen1749144759/ScaleTail) 负责客户端连接和桌面端体验。

## 版本定位

| 项目项 | 当前状态 |
|--------|----------|
| 基础代码 | 官方 headscale `v0.28.0` |
| 当前对标 | 官方 headscale `v0.29.1` 的关键稳定性修复 |
| 升级方式 | 保留 AE 分支能力，手工审计并定向回补关键补丁 |
| Go 版本 | `go.mod` 使用 Go `1.26.1` |
| Tailscale 依赖 | `tailscale.com v1.96.5` |
| 配套管理面板 | ScaleForge |
| 推荐客户端 | ScaleTail，客户端核心已按 Tailscale `v1.98.5` 关键修复审计 |

请注意：当前 main 分支不是官方 headscale `v0.29.1` 的整仓升级版。它仍然以 `v0.28.0 AE` 为基础，保留管理面板数据库扩展、数据库 ACL、MoveNode、Docker 模板和内置 DERP 改造，同时回补官方 `v0.29.1` 中对本项目有价值的稳定性修复。

## 这个分支解决什么问题

官方 headscale 更偏向命令行和配置文件管理。ScaleForge 需要直接管理用户、节点、路由、ACL、预认证密钥和操作日志，因此服务端必须具备以下能力：

- 管理面板和 headscale 共用同一个数据库。
- `users` 表可以保存登录密码、角色、过期时间、启用状态、节点配额和路由权限。
- ACL 可以存入数据库，由管理面板在线编辑，而不是只依赖本地策略文件。
- 节点可以在不同用户或分组之间迁移，并立刻刷新在线客户端的网络地图。
- Docker 部署可以通过环境变量渲染配置，适合一键部署和升级。
- 客户端断线、重连、换 key、重复提交注册请求时，服务端状态不能错乱。

Headscale-Admin-AE 就是在这些目标下维护的控制服务器分支。

## 核心特性

### 1. 管理面板共享数据库

在官方 `users` 表基础上扩展管理字段，ScaleForge 可以直接读取和维护这些字段。

| 字段 | 类型 | 说明 |
|------|------|------|
| `password` | TEXT | 管理面板登录密码，通常为哈希值 |
| `role` | TEXT | 用户角色，例如 `admin` / `user` |
| `expire` | DATETIME | 账户过期时间 |
| `enable` | BOOLEAN | 账户启用或禁用 |
| `node` | INTEGER | 节点数量配额 |
| `route` | TEXT | 路由权限控制 |

项目还会维护管理面板需要的 `policies`、`acl`、`log` 等兼容结构，让控制服务器和管理后台可以共用同一套 SQLite 或 PostgreSQL 数据。

### 2. ACL 数据库模式

支持：

```yaml
policy:
  mode: database
```

开启后，ACL/HuJSON 策略从数据库读取，ScaleForge 可以在线编辑和保存策略。普通文件模式仍然保留，用于兼容官方 headscale 的使用习惯。

### 3. MoveNode 热迁移 API

新增节点迁移接口，可以把节点移动到另一个用户或分组，不需要重启 headscale。

```http
POST /api/v1/node/{node_id}/user
Content-Type: application/json

{"user": "目标用户名"}
```

服务端会同时更新数据库和内存中的 NodeStore，并通知在线节点刷新网络地图。这个能力主要给 ScaleForge 的“节点移动分组”功能使用。

### 4. Docker 和环境变量部署

`docker/config.yaml.tmpl` 支持通过环境变量渲染配置，适合和 ScaleForge 一起部署。

常用变量：

| 变量 | 作用 |
|------|------|
| `HEADSCALE_SERVER_URL` | 客户端连接使用的控制服务器地址 |
| `HEADSCALE_DNS_DOMAIN` | MagicDNS 基础域名 |
| `HEADSCALE_LOG_LEVEL` | 日志级别 |
| `DB_HOST` / `DB_PORT` / `DB_NAME` | PostgreSQL 地址和库名 |
| `DB_USER` / `DB_PASS` | PostgreSQL 用户和密码 |
| `DERP_DOMAIN` | 内置 DERP 对外域名或 IP |
| `DERP_PORT` | 内置 DERP/STUN 监听端口 |

模板默认开启 PostgreSQL、数据库策略模式和内置 DERP。

### 5. 内置 DERP 改造

当前分支保留官方内置 DERP 能力，并做了适合一键部署的调整：

- DERP 域名和端口可由环境变量控制。
- 允许在没有外部公开 DERP map 的情况下使用内置中继。
- 内置 region 会被加入当前 DERP map，便于客户端 NAT 穿透失败时自动回落中继。

### 6. 官方 headscale v0.29.1 关键修复回补

本轮对照官方 headscale `v0.29.1`，回补了影响注册、重连、数据库和网络稳定性的修复：

- MachineKey 级注册互斥，避免同一客户端并发注册造成重复节点。
- NodeKey 与 MachineKey 归属校验，防止旧 NodeKey 被其他机器复用。
- 已存在节点重启时可以复用原 NodeKey，不会错误消耗新的预认证密钥。
- 过期节点重新注册会重新校验预认证密钥。
- 一次性预认证密钥使用原子条件更新，避免并发重复消费。
- 未知预认证密钥按不存在处理，错误语义更清晰。
- NodeStore 更新后如果数据库写入失败，会回滚内存状态，避免内存和数据库不一致。
- 数据库迁移补齐零值过期时间转 `NULL`、`tags='null'` 用户归属恢复、API key 主键查询等修复。
- IP 分配器补齐 `/32`、`/128` 等极小网段处理，避免异常前缀导致 panic。
- DERP map shuffle 前复制 region，避免修改共享配置。
- OIDC cookie 使用更安全的 SameSite 策略和更短名称。
- IPv4 `/32` 反向 DNS 生成逻辑补齐。

## 本轮更新摘要

当前 main 分支相对远程旧版本已经包含以下更新：

| 类型 | 内容 |
|------|------|
| DERP | 支持通过环境变量配置 DERP 域名和端口 |
| DERP | 内置 DERP 可以在没有外部公开 DERP map 的情况下工作 |
| Docker | 完善 Docker 部署，兼容 PostgreSQL 建表和内置 entrypoint |
| 注册稳定性 | 回补 headscale `v0.29.1` 注册、重注册、预认证密钥和 NodeStore 修复 |
| 文档 | 重写 README，明确基础版本、对标版本、特性和配套项目 |

## 架构关系

```text
ScaleTail 客户端
  |
  | Tailscale/headscale 控制协议
  v
Headscale-Admin-AE
  |
  | 共享数据库 + HTTP/gRPC API
  v
ScaleForge 管理面板
```

Headscale-Admin-AE 保持 `headscale` 二进制和 CLI 形态，客户端仍按 headscale/Tailscale 协议接入；ScaleForge 通过共享数据库和 API 完成 Web 管理。

## 构建

### 本地构建

```bash
git clone https://github.com/chen1749144759/Headscale-Admin-AE.git
cd Headscale-Admin-AE

go build -trimpath -o headscale ./cmd/headscale
```

查看版本：

```bash
./headscale version
```

启动服务：

```bash
./headscale serve
```

### Docker 构建

```bash
docker build -t headscale-admin-ae:local .
```

运行时请挂载配置目录和数据目录，并按实际环境配置 `HEADSCALE_SERVER_URL`、数据库和 DERP 参数。

## 配置示例

最小配置仍遵循官方 headscale 配置格式。管理面板场景建议开启数据库策略模式：

```yaml
server_url: http://你的服务器IP:8080

listen_addr: 0.0.0.0:8080

noise:
  private_key_path: /var/lib/headscale/noise_private.key

database:
  type: postgres
  postgres:
    host: postgres
    port: 5432
    name: headscale_admin
    user: headscale_admin
    pass: your_password
    ssl: false

policy:
  mode: database

derp:
  server:
    enabled: true
    region_id: 999
    region_code: headscale
    region_name: Headscale Embedded DERP
    stun_listen_addr: 0.0.0.0:3478
    domain: 你的服务器IP或域名
    private: true
    verify_clients: false
```

完整配置请参考 [config-example.yaml](config-example.yaml) 和 [docker/config.yaml.tmpl](docker/config.yaml.tmpl)。

## 常用命令

```bash
# 创建用户
./headscale users create admin

# 创建预认证密钥
./headscale preauthkeys create --user admin --reusable --expiration 24h

# 查看节点
./headscale nodes list

# 查看路由
./headscale nodes list-routes

# 批准路由
./headscale nodes approve-routes --identifier <node-id> --routes 192.168.1.0/24

# 创建 API key，供 ScaleForge 调用
./headscale apikey create
```

## 与 ScaleForge 配套

ScaleForge 是推荐的 Web 管理面板。两者的职责分工如下：

| 项目 | 职责 |
|------|------|
| Headscale-Admin-AE | 控制服务器、节点注册、网络地图、DERP、路由、ACL 后端能力 |
| ScaleForge | Web 管理面板、用户登录、节点管理、ACL 编辑、部署向导、可视化运维 |
| ScaleTail | Windows 图形客户端、连接配置、仪表盘、LocalAPI 操作 |

推荐组合：

- 服务端控制面：Headscale-Admin-AE
- 服务端 Web 管理：ScaleForge
- Windows 客户端：ScaleTail

## 数据库升级注意

从官方 headscale 或旧 AE 分支切换前，请先备份数据库。

```bash
# PostgreSQL 示例
pg_dump -U headscale_admin headscale_admin > headscale_backup.sql
```

切换后建议先在测试环境确认：

- 管理员用户字段是否完整。
- 预认证密钥是否能创建和消费。
- 已有节点是否能正常在线。
- 断开后重新注册是否符合预期。
- 路由批准和 ACL 策略是否能被 ScaleForge 正常读取。

## 兼容性说明

| 能力 | 状态 |
|------|------|
| 官方 headscale CLI | 保持兼容 |
| 官方配置格式 | 保持兼容，并增加数据库策略和 AE 数据库扩展 |
| SQLite | 支持 |
| PostgreSQL | 支持，推荐生产环境使用 |
| Tailscale 官方客户端 | 支持按 headscale 方式接入 |
| ScaleTail 客户端 | 推荐 |
| ScaleForge 管理面板 | 推荐 |

## 相关项目

- [juanfont/headscale](https://github.com/juanfont/headscale)：官方 headscale 控制服务器。
- [arounyf/Headscale-Admin-Pro](https://github.com/arounyf/Headscale-Admin-Pro)：早期 Headscale 管理面板。
- [ScaleForge](https://github.com/chen1749144759/ScaleForge)：配套 Web 管理面板。
- [ScaleTail](https://github.com/chen1749144759/ScaleTail)：配套图形化客户端。
- [tailscale/tailscale](https://github.com/tailscale/tailscale)：Tailscale 客户端和网络核心。

## 许可证

本项目保持与 headscale 一致，使用 [BSD 3-Clause License](LICENSE)。
