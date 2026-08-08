# Android 客户端

当前版本不支持 Android 客户端。

官方 Tailscale Android 客户端依赖浏览器认证或 auth key，而
Headscale-Admin-AE 已禁用 OIDC、浏览器批准和预认证 Key。只有实现 ScaleTail
账户密码 Noise 认证协议的客户端才能接入。

在 Android 版 ScaleTail 正式发布并完成同协议测试前，不要尝试恢复旧认证入口来绕过
这一限制。
