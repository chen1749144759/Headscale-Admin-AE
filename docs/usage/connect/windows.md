# 连接 Windows 节点

Windows 节点必须使用与服务端版本匹配的 ScaleTail 客户端。官方 Tailscale Windows
客户端不支持本分支的账户密码协议。

## 登录

1. 安装 ScaleTail，并确认 `ScaleTail` 系统服务正在运行。
2. 打开 ScaleTail 仪表盘的服务端设置。
3. 填写可信的 HTTPS 控制服务器地址、账户名和密码。
4. 按需设置设备名称、接受路由和 DNS。
5. 点击连接，等待页面显示节点在线。

登录直接通过本地服务和加密 Noise 会话完成，不会打开浏览器，不需要 Auth ID、预认证
Key 或 CMD 命令。

## 密码到期

管理员发放的初始密码只用于首次身份确认。ScaleTail 检测到初始密码或 90 天密码
已过期时，会在客户端内要求设置新密码，成功后自动继续连接，不需要用户登录
ScaleForge。修改密码会撤销旧管理会话，并使后续节点控制会话重新证明账户身份。

## 排查

- `/health` 必须通过 HTTPS 正常访问。
- 客户端系统时间应准确。
- ScaleTail 与 Headscale-Admin-AE 应使用同一发布批次的账户协议。
- ScaleForge 中账户必须启用、未到期并已绑定网络。
- 服务端日志不应出现 `password_expired`、`account_disabled`、
  `network_not_assigned` 或 `node_limit_reached`。

不要用旧版预认证 Key、官方 Tailscale 浏览器登录或手工注册作为兜底。
