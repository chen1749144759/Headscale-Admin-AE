[中文](#中文) | [English](#english)

---

![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat-square&logo=go&logoColor=white)
![Headscale Base](https://img.shields.io/badge/Headscale_Base-v0.28.0-326CE5?style=flat-square)
![Headscale Fixes](https://img.shields.io/badge/Headscale_Fixes-v0.29.1_Backports-326CE5?style=flat-square)
![License](https://img.shields.io/badge/License-BSD_3--Clause-green?style=flat-square)
![DB](https://img.shields.io/badge/Database-SQLite_%7C_PostgreSQL-4169E1?style=flat-square)

---

# 中文

## Headscale-Admin-AE

> 基于 headscale v0.28.0 的增强分支，为 Web 管理面板扩展了用户认证与权限字段。

Headscale-Admin-AE 是对官方 [headscale](https://github.com/juanfont/headscale) 控制服务器的定制修改版本（基于 v0.28.0），由 **runyf**（[Headscale-Admin-Pro](https://github.com/arounyf/Headscale-Admin-Pro) 原作者）完成核心改造。其目标是让 headscale 与 Web 管理面板能够**共享同一个数据库**，无需额外维护独立的用户系统。

## 当前对标版本

- 基础分支：官方 headscale `v0.28.0` 的 AE 定制分支。
- 本轮对标：官方 headscale `v0.29.1` 的注册、重注册、数据库迁移、DERP、OIDC 和 DNS 稳定性修复。
- 当前不是整仓升级到 headscale `v0.29.1`，仍保留管理面板数据库扩展、数据库策略模式、MoveNode API、内置 DERP 环境变量等本项目能力。
- Go 版本：`go.mod` 使用 Go `1.26.1`。
- Tailscale 依赖：当前仍为 `tailscale.com v1.96.5`。客户端 ScaleTail 已按 Tailscale `v1.98.5` 关键修复审计，两端继续通过标准 `tailcfg` 控制协议对接。

## 为什么需要这个分支

官方 headscale 的 `users` 表仅包含基础字段，没有密码、角色、到期时间等认证信息。Web 管理面板需要这些字段来实现用户登录和权限控制。

常见的做法是让管理面板维护一套独立的用户数据库，但这会带来数据同步问题。本分支选择**直接扩展 headscale 自身的 `users` 表**，使两者共用同一份数据，架构更简洁，维护成本更低。

## 核心修改

### 1. 扩展 `users` 表结构

在官方 `users` 表基础上新增以下字段：

| 字段 | 类型 | 说明 |
|------|------|------|
| `password` | TEXT | 用户登录密码（哈希存储） |
| `role` | TEXT | 用户角色（如 admin / user） |
| `expire` | DATETIME | 账户过期时间 |
| `enable` | BOOLEAN | 账户启用/禁用开关 |
| `node` | INTEGER | 节点配额限制 |
| `route` | TEXT | 路由权限控制 |

### 2. ACL 策略数据库模式

支持 `policy.mode: database` 配置项，将 ACL 规则存储在 `policies` 表（`data` TEXT 字段）中，不再强制依赖文件模式。

### 3. 数据库兼容性调整

针对管理面板的数据库访问需求进行了兼容性适配，确保 headscale 与管理面板能稳定地共享同一个 SQLite 或 PostgreSQL 数据库。

### 4. 完全 CLI 兼容

编译产物仍为 `headscale` 二进制文件，所有命令行参数和用法与官方版本保持一致。

### 5. 新增 MoveNode API

新增 `MoveNode` gRPC/REST 接口，支持将节点在不同用户（分组）之间迁移，**无需重启 Headscale 服务**。变更直接更新内存中的 NodeStore 快照和数据库，并自动通知所有在线节点刷新网络映射。

- **REST 端点**：`POST /api/v1/node/{node_id}/user`
- **请求体**：`{"user": "目标用户名"}`
- **响应**：返回迁移后的完整 Node 对象

涉及修改的文件：

| 文件 | 变更说明 |
|------|---------|
| `proto/headscale/v1/node.proto` | 新增 `MoveNodeRequest` / `MoveNodeResponse` 消息定义 |
| `proto/headscale/v1/headscale.proto` | 新增 `rpc MoveNode` 服务定义及 HTTP 路由映射 |
| `gen/go/headscale/v1/*.go` | 由 `buf generate` 自动生成的 gRPC/gateway 代码 |
| `hscontrol/state/state.go` | 新增 `State.MoveNode()` 方法，通过 `NodeStore.UpdateNode` 热更新内存 |
| `hscontrol/grpcv1.go` | 新增 `MoveNode` gRPC handler |

### 6. headscale v0.29.1 核心修复回补

本轮没有直接合并官方 `v0.29.1` 整仓代码，而是按本项目业务保留范围做了定向回补：

- 注册与重注册流程增加 MachineKey 级互斥，避免同一客户端并发注册导致重复节点或状态错乱。
- NodeKey/MachineKey 归属校验更严格，防止 NodeKey 被其他 MachineKey 复用，也能识别同一节点重启后的合法复连。
- 预认证密钥校验更稳：未知 key 视为不存在，过期节点重注册会重新校验 key，一次性 key 使用原子条件更新，避免重复消费。
- 同一节点用原 NodeKey 复连时可以复用已有记录；真正更换 key 或重新注册时再走新的校验和更新流程。
- NodeStore 更新后如果数据库写入失败会回滚内存快照，避免在线状态与数据库不一致。
- 数据库迁移补齐零值过期时间转 `NULL`、`tags='null'` 用户归属恢复、API key 主键查询等兼容修复。
- IP 分配补齐 /32、/128 等极小网段保护，随机 IP 生成补齐字节长度，避免异常网段下 panic。
- DERP map shuffle 前复制 region，避免修改共享结构；OIDC cookie 使用更稳的 SameSite 与短名称；IPv4 /32 反向 DNS 生成逻辑补齐。

## 版本兼容性

| AE 版本 | headscale 基础版本 | 对标修复 | Tailscale 依赖 | 兼容管理面板 |
|---------|-------------------|----------|----------------|-------------|
| v0.28.0-ae | v0.28.0 | AE 基础改造 | v1.96.5 | [Headscale-Admin-Reforged](https://github.com/chen1749144759/Headscale-Admin-Reforged) |
| 当前 main | v0.28.0 AE 分支 | headscale v0.29.1 核心稳定性修复回补 | v1.96.5 | [Headscale-Admin-Reforged](https://github.com/chen1749144759/Headscale-Admin-Reforged) |

## 安装

### 从源码构建

```bash
git clone https://github.com/chen1749144759/Headscale-Admin-AE.git
cd Headscale-Admin-AE
go build -o headscale ./cmd/headscale
```

### 使用方式

编译后的 `headscale` 二进制文件可直接替换官方版本，配置文件格式完全兼容：

```bash
# 与官方 headscale 用法一致
./headscale serve
./headscale users list
./headscale nodes list
```

## 配置

在标准 headscale 配置文件基础上，可启用数据库策略模式：

```yaml
policy:
  mode: database   # 使用数据库存储 ACL 规则（默认为 file）
```

其余配置项与官方 headscale v0.28.0 完全一致，请参阅 [官方文档](https://headscale.net/stable/)。

## 相关项目

| 项目 | 说明 |
|------|------|
| [headscale](https://github.com/juanfont/headscale) | 官方 headscale 开源控制服务器 |
| [Headscale-Admin-Pro](https://github.com/arounyf/Headscale-Admin-Pro) | 原始管理面板（runyf 开发） |
| [Headscale-Admin-Reforged](https://github.com/chen1749144759/Headscale-Admin-Reforged) | 配套 Web 管理面板 |

## 致谢

- [juanfont/headscale](https://github.com/juanfont/headscale) — 优秀的开源 Tailscale 控制服务器
- [arounyf](https://github.com/arounyf) (runyf) — headscale 数据库扩展改造的原始作者
- [Tailscale](https://tailscale.com/) — 现代化的 WireGuard 组网方案

## 许可证

本项目基于 [BSD 3-Clause License](LICENSE) 开源，与 headscale 保持一致。

---

# English

## Headscale-Admin-AE

> An enhanced fork of headscale v0.28.0 with extended user authentication and permission fields for web admin panel integration.

Headscale-Admin-AE is a modified version of the official [headscale](https://github.com/juanfont/headscale) control server (based on v0.28.0), with core modifications by **runyf** (original author of [Headscale-Admin-Pro](https://github.com/arounyf/Headscale-Admin-Pro)). It enables headscale and a web admin panel to **share a single database**, eliminating the need for a separate user management system.

## Current Upstream Targets

- Base branch: the AE customized branch based on official headscale `v0.28.0`.
- Current backport target: key registration, re-registration, database migration, DERP, OIDC, and DNS stability fixes from official headscale `v0.29.1`.
- This is not a full repository upgrade to headscale `v0.29.1`; the project keeps its admin-panel database extensions, database policy mode, MoveNode API, embedded DERP environment overrides, and other AE-specific behavior.
- Go version: `go.mod` uses Go `1.26.1`.
- Tailscale dependency: still `tailscale.com v1.96.5`. The ScaleTail client has been audited against key Tailscale `v1.98.5` fixes, and both sides continue to communicate through the standard `tailcfg` control protocol.

## Why This Fork

The official headscale `users` table only contains basic fields — no password, role, or expiration data. A web admin panel requires these fields to provide user login and access control.

A common approach is to maintain a separate user database for the admin panel, but this introduces data synchronization issues. This fork takes a different approach: **extend headscale's own `users` table directly**, so both systems share one data source. Simpler architecture, lower maintenance overhead.

## Key Modifications

### 1. Extended `users` Table

The following columns are added to the official `users` table:

| Column | Type | Description |
|--------|------|-------------|
| `password` | TEXT | User login password (hashed) |
| `role` | TEXT | User role (e.g., admin / user) |
| `expire` | DATETIME | Account expiration time |
| `enable` | BOOLEAN | Account enabled/disabled flag |
| `node` | INTEGER | Node quota limit |
| `route` | TEXT | Route permission control |

### 2. ACL Policy Database Mode

Supports `policy.mode: database` configuration, storing ACL rules in a `policies` table (`data` TEXT field) instead of requiring file-based policy management.

### 3. Database Compatibility

Includes compatibility adjustments so that headscale and the admin panel can reliably share the same SQLite or PostgreSQL database.

### 4. Full CLI Compatibility

The compiled binary is still named `headscale`. All command-line arguments and usage remain identical to the official version.

### 5. New MoveNode API

A new `MoveNode` gRPC/REST endpoint that allows moving nodes between users (groups) **without restarting the Headscale service**. Changes are applied directly to the in-memory NodeStore snapshot and the database, and all connected nodes are automatically notified to refresh their network maps.

- **REST endpoint**: `POST /api/v1/node/{node_id}/user`
- **Request body**: `{"user": "target_username"}`
- **Response**: Returns the complete Node object after migration

Modified files:

| File | Description |
|------|-------------|
| `proto/headscale/v1/node.proto` | Added `MoveNodeRequest` / `MoveNodeResponse` message definitions |
| `proto/headscale/v1/headscale.proto` | Added `rpc MoveNode` service definition with HTTP route mapping |
| `gen/go/headscale/v1/*.go` | Auto-generated gRPC/gateway code via `buf generate` |
| `hscontrol/state/state.go` | Added `State.MoveNode()` method with hot-update via `NodeStore.UpdateNode` |
| `hscontrol/grpcv1.go` | Added `MoveNode` gRPC handler |

### 6. headscale v0.29.1 Stability Backports

This round does not merge the entire official `v0.29.1` tree. Instead, it selectively backports the fixes that matter to this fork:

- Registration and re-registration now use a MachineKey-level lock to avoid duplicate nodes or inconsistent state during concurrent registration.
- NodeKey/MachineKey ownership checks are stricter, preventing NodeKey reuse by another MachineKey while still allowing valid restarts from the same node.
- Pre-auth key validation is safer: unknown keys are treated as missing, expired-node re-registration revalidates the key, and one-time key consumption uses an atomic conditional update.
- A same-node reconnect with the existing NodeKey can reuse the existing record; real key changes or re-registration paths still go through validation and update.
- NodeStore changes are rolled back if the database write fails, avoiding divergence between in-memory state and persistent state.
- Database migrations include zero expiry to `NULL`, `tags='null'` user ownership recovery, and explicit API key primary-key lookup fixes.
- IP allocation now handles tiny /32 and /128 prefixes and pads random IP bytes correctly to avoid panics on unusual prefixes.
- DERP region shuffle now works on cloned regions, OIDC cookies use safer SameSite and shorter names, and IPv4 /32 reverse-DNS generation is fixed.

## Version Compatibility

| AE Version | Headscale Base | Backported Fixes | Tailscale Dependency | Compatible Admin Panel |
|-------------|---------------|------------------|----------------------|------------------------|
| v0.28.0-ae | v0.28.0 | AE baseline changes | v1.96.5 | [Headscale-Admin-Reforged](https://github.com/chen1749144759/Headscale-Admin-Reforged) |
| current main | v0.28.0 AE branch | headscale v0.29.1 core stability fixes | v1.96.5 | [Headscale-Admin-Reforged](https://github.com/chen1749144759/Headscale-Admin-Reforged) |

## Installation

### Build from Source

```bash
git clone https://github.com/chen1749144759/Headscale-Admin-AE.git
cd Headscale-Admin-AE
go build -o headscale ./cmd/headscale
```

### Usage

The compiled `headscale` binary is a drop-in replacement for the official version. Configuration file format is fully compatible:

```bash
# Same usage as official headscale
./headscale serve
./headscale users list
./headscale nodes list
```

## Configuration

On top of the standard headscale configuration, you can enable database policy mode:

```yaml
policy:
  mode: database   # Store ACL rules in database (default: file)
```

All other configuration options are identical to official headscale v0.28.0. Refer to the [official documentation](https://headscale.net/stable/) for details.

## Related Projects

| Project | Description |
|---------|-------------|
| [headscale](https://github.com/juanfont/headscale) | Official open-source headscale control server |
| [Headscale-Admin-Pro](https://github.com/arounyf/Headscale-Admin-Pro) | Original admin panel by runyf |
| [Headscale-Admin-Reforged](https://github.com/chen1749144759/Headscale-Admin-Reforged) | Companion web admin panel |

## Credits

- [juanfont/headscale](https://github.com/juanfont/headscale) — The excellent open-source Tailscale control server
- [arounyf](https://github.com/arounyf) (runyf) — Original author of the headscale database extension modifications
- [Tailscale](https://tailscale.com/) — Modern WireGuard-based networking

## License

This project is licensed under the [BSD 3-Clause License](LICENSE), consistent with headscale.
