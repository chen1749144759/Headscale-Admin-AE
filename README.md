[中文](#中文) | [English](#english)

---

![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go&logoColor=white)
![Headscale Base](https://img.shields.io/badge/Headscale_Base-v0.28.0-326CE5?style=flat-square)
![License](https://img.shields.io/badge/License-BSD_3--Clause-green?style=flat-square)
![DB](https://img.shields.io/badge/Database-SQLite_%7C_PostgreSQL-4169E1?style=flat-square)

---

# 中文

## Headscale-Admin-AE

> 基于 headscale v0.28.0 的增强分支，为 Web 管理面板扩展了用户认证与权限字段。

Headscale-Admin-AE 是对官方 [headscale](https://github.com/juanfont/headscale) 控制服务器的定制修改版本（基于 v0.28.0），由 **runyf**（[Headscale-Admin-Pro](https://github.com/arounyf/Headscale-Admin-Pro) 原作者）完成核心改造。其目标是让 headscale 与 Web 管理面板能够**共享同一个数据库**，无需额外维护独立的用户系统。

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

## 版本兼容性

| AE 版本 | headscale 基础版本 | 兼容管理面板 |
|---------|-------------------|-------------|
| v0.28.0-ae | v0.28.0 | [Headscale-Admin-Reforged](https://github.com/chen1749144759/Headscale-Admin-Reforged) |

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

## Version Compatibility

| AE Version | Headscale Base | Compatible Admin Panel |
|-------------|---------------|----------------------|
| v0.28.0-ae | v0.28.0 | [Headscale-Admin-Reforged](https://github.com/chen1749144759/Headscale-Admin-Reforged) |

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
