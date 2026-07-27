# Headscale-Admin-AE

[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Base](https://img.shields.io/badge/Base-headscale%20v0.28.0-326CE5)](https://github.com/juanfont/headscale/releases/tag/v0.28.0)
[![Backports](https://img.shields.io/badge/Backports-headscale%20v0.29.2-7C3AED)](https://github.com/juanfont/headscale/releases/tag/v0.29.2)
[![Tailscale Lib](https://img.shields.io/badge/tailscale.com-v1.96.5-4D7CFE)](https://github.com/tailscale/tailscale)
[![Database](https://img.shields.io/badge/Database-SQLite%20%7C%20PostgreSQL-4169E1)](#部署难度)

Headscale-Admin-AE 是基于官方 headscale 裂变的增强控制服务，服务于 ScaleForge 管理平台和 ScaleTail 客户端场景。它负责控制面、节点注册、网络地图、路由、ACL、DERP 配置和数据库扩展；图形化管理界面由 ScaleForge 提供。

仓库地址：[chen1749144759/Headscale-Admin-AE](https://github.com/chen1749144759/Headscale-Admin-AE)

## 版本定位

| 项目项 | 当前说明 |
|---|---|
| 裂变来源 | 基于官方 `juanfont/headscale v0.28.0` 的 AE 增强分支继续维护 |
| 当前对标 | 定向回补官方 headscale `v0.29.2` 中对本项目有价值的注册、重连、策略并发和稳定性修复 |
| 升级策略 | 不直接整仓升级到 v0.29.2，优先保留 AE 自定义能力，再按审计结果回补关键修复 |
| Go 版本 | `go.mod` 使用 Go `1.26.1` |
| Tailscale 依赖 | `tailscale.com v1.96.5` |
| 配套管理平台 | `ScaleForge` |
| 推荐客户端 | `ScaleTail` |
| 当前 Docker 镜像 | `chenzeshi/headscale-admin-ae:20260727-387705f` |

本项目仍保持 headscale 控制服务定位，二进制和 CLI 形态以 headscale 为核心；ScaleForge 通过共享数据库和 API 与它协同工作。

## 自实现功能

- 扩展 `users` 表，支持管理平台登录密码、角色、过期时间、启用状态、节点配额和路由权限。
- 支持数据库 ACL 模式，ScaleForge 可以在线读取、编辑和保存 ACL/HuJSON 策略。
- 支持节点在用户/分组之间迁移，并通知在线节点刷新网络地图。
- Docker 配置模板支持环境变量渲染，适合与 ScaleForge 一键部署。
- 保留并增强内置 DERP/DERP map 相关能力，方便私有化部署。
- 支持 PostgreSQL 和 SQLite 两种数据库。
- 启动时同步管理平台需要的自定义表和索引，降低 ScaleForge 首次启动缺表风险。
- 回补官方 headscale v0.29.1 中影响注册、重注册、预认证密钥、NodeStore、数据库迁移和 IP 分配稳定性的关键修复。
- 回补官方 headscale v0.29.2 的策略读锁/缓存并发、网络图异常节点隔离、无效 FQDN 启动体检、注册错误响应和 `/ts2021` WebSocket GET 兼容修复。

## 新增/扩展数据结构

### users 表扩展字段

| 字段 | 用途 |
|---|---|
| `password` | 管理平台登录密码哈希 |
| `expire` | 账户过期时间 |
| `cellphone` | 联系方式 |
| `role` | 管理平台角色 |
| `enable` | 账户启用状态 |
| `route` | 路由权限 |
| `node` | 节点配额 |

### 管理平台和观测表

| 表名 | 用途 |
|---|---|
| `acl` | 数据库 ACL/HuJSON 策略 |
| `log` | 管理平台操作日志 |
| `client_policies` | ScaleTail 客户端策略 |
| `client_policy_states` | 客户端策略应用状态 |
| `traffic_samples` | 原始流量采样 |
| `traffic_hourly` | 小时级流量聚合 |
| `traffic_daily` | 日级流量聚合 |
| `flow_summaries` | 请求/连接摘要 |
| `node_ip_observations` | 节点公网 IP 观测 |
| `security_events` | 安全事件 |
| `trusted_networks` | 可信网络 |
| `risk_rules` | 风险规则 |
| `client_releases` | ScaleTail 客户端版本、强制/建议更新策略及 OTA 哈希/签名元数据 |

这些表与 ScaleForge 后端 SQL 保持一致。Headscale-Admin-AE 负责在控制服务启动时兜底创建，ScaleForge 负责业务读写和页面展示。

## 对标官方 v0.29.2 的关键回补

- MachineKey 级注册互斥，降低并发注册导致重复节点的风险。
- NodeKey 与 MachineKey 归属校验，避免旧 NodeKey 被错误复用。
- 已存在节点重启时尽量复用原 NodeKey，避免误消耗新的预认证密钥。
- 过期节点重新注册时重新校验预认证密钥。
- 一次性预认证密钥使用逻辑更稳，降低并发重复消费风险。
- 未知预认证密钥按不存在处理，错误语义更清楚。
- NodeStore 写库失败时回滚内存状态，避免数据库和内存不一致。
- 数据库迁移补齐零值过期时间转 `NULL`、`tags='null'` 用户归属恢复、API key 主键查询等修复。
- IP 分配器补齐 `/32`、`/128` 等极小网段处理，避免异常前缀导致 panic。
- DERP map shuffle 前复制 region，避免修改共享配置。
- OIDC cookie 使用更安全的 SameSite 策略和更短名称。
- IPv4 `/32` 反向 DNS 生成逻辑补齐。
- 策略管理器读路径使用读锁，并将高并发缓存切换为并发 Map，降低网络图生成期间的锁竞争和重连风暴。
- 单个节点名称/FQDN 异常时记录告警并跳过该节点，避免一次坏数据拖垮整张网络图。
- 启动时只读扫描历史节点名称，明确提示需要重命名的节点，不在启动阶段擅自修改数据库。
- 网络图和节点注册失败会返回明确 HTTP 错误，避免客户端收到空 200 后持续 `unexpected EOF` 重试。
- `/ts2021` 同时接受 GET 和 POST，兼容 WebSocket/JS/WASM 控制客户端。

## 部署难度

| 场景 | 难度 | 说明 |
|---|---:|---|
| 随 ScaleForge Docker Compose 部署 | 中 | 推荐方式。配置集中，数据库、管理平台、控制服务一起启动。 |
| 单独部署 Headscale-Admin-AE | 中高 | 需要手动准备配置文件、数据库、证书/DERP、API key 和 systemd/Docker 运行环境。 |
| 从官方 headscale 迁移 | 高 | 需要谨慎处理数据库结构、ACL 模式、用户字段、预认证密钥和客户端重连状态。 |
| 继续合并官方新版本 | 高 | 需要逐项审计上游改动，避免破坏 AE 自定义数据库、Docker、DERP 和管理平台契约。 |

部署前必须确认：

- ScaleForge 和 Headscale-Admin-AE 使用同一个数据库。
- 数据库账号具备创建表、添加字段、创建索引的权限。
- `server_url` 必须与 ScaleTail 客户端连接页填写的控制服务器地址一致。
- 如果使用 DERP/STUN，需要确认公网端口、防火墙和域名解析。
- 如果启用数据库 ACL，配置文件中应使用对应策略模式。

## 本地构建

```bash
git clone https://github.com/chen1749144759/Headscale-Admin-AE.git
cd Headscale-Admin-AE
go build -trimpath -o headscale ./cmd/headscale
```

查看版本：

```bash
./headscale version
```

运行测试：

```bash
go test ./hscontrol/db
```

## Docker 部署与升级

推荐通过 ScaleForge 仓库的 Compose 文件部署，固定使用：

```dotenv
AE_VERSION=20260727-387705f
```

首次部署和升级步骤参见 [ScaleForge Docker Compose 文档](https://github.com/chen1749144759/ScaleForge#docker-compose-首次部署)。控制服务、ScaleForge 和 PostgreSQL 必须使用同一套数据库配置；不要额外启动一个指向不同数据库的 Headscale 实例。

单独验证镜像可执行文件：

```bash
docker pull chenzeshi/headscale-admin-ae:20260727-387705f
docker run --rm chenzeshi/headscale-admin-ae:20260727-387705f headscale version
```

生产升级时：

1. 先备份 PostgreSQL 和 Headscale 配置、状态目录。
2. 保留 PostgreSQL、Headscale 数据卷，不执行 `docker compose down -v`。
3. 更新 `AE_VERSION` 后执行 `docker compose pull headscale` 和 `docker compose up -d headscale`。
4. 确认 `curl -fsS http://127.0.0.1:8080/health` 返回成功。
5. 检查 `docker compose logs --tail=100 headscale`，确认数据库迁移、ACL、DERP 和服务 URL 没有报错。
6. 使用现有 ScaleTail 节点验证重连、节点列表、路由宣告和网络互通。

本项目会在启动时增量补齐 ScaleForge 配套表结构，但不会要求清空现有数据卷。数据库账号缺少 DDL 权限时，容器可能启动但新增平台能力不可用，因此健康检查之后仍需检查启动日志。

## 三件套关系

```text
ScaleTail 客户端
  |
  | Tailscale/headscale 控制协议
  v
Headscale-Admin-AE
  |
  | 共享数据库 + API
  v
ScaleForge 管理平台
```

Headscale-Admin-AE 是控制面；ScaleForge 是管理面；ScaleTail 是客户端。三者一起使用时，普通用户不需要直接操作 headscale CLI，也不需要在客户端 CMD 中手动执行连接命令。

## 当前已验证

- `go test ./hscontrol/db` 通过。
- PostgreSQL/SQLite 两套自定义表创建逻辑已核对。
- `flow_summaries` 旧表补列逻辑已核对。
- ScaleForge 新增的流量、策略、安全审计表已在本项目同步。

## 已知边界

- 当前不是官方 headscale v0.29.2 的整仓升级版本，而是 v0.28.0 AE 分支上的定向回补版本。
- 与 ScaleForge 强绑定的自定义表不属于官方 headscale 标准 schema。
- 继续追上游版本时必须先审计注册、数据库迁移、NodeStore、DERP、ACL 和 Docker 模板差异。

## 交流学习

欢迎加入 ScaleForge 交流群，一起交流自建 Headscale、ScaleTail、ScaleForge 的部署、使用和二次开发经验。

群号：`1041671099`

<img src="docs/images/scaleforge-qq-group.jpg" alt="ScaleForge 交流群" width="360">

## 打赏

如果这个项目帮你节省了部署和维护时间，可以请作者喝杯咖啡。打赏二维码维护在 ScaleForge 仓库中：

![打赏](https://raw.githubusercontent.com/chen1749144759/ScaleForge/main/docs/screenshots/donate.jpg)

感谢支持，项目会继续围绕自建 Headscale/ScaleTail 网络的易用性、稳定性和安全可视化迭代。

## 致谢

- [juanfont/headscale](https://github.com/juanfont/headscale)
- [tailscale/tailscale](https://github.com/tailscale/tailscale)
- [ScaleForge](https://github.com/chen1749144759/ScaleForge)
- [ScaleTail](https://github.com/chen1749144759/ScaleTail)
