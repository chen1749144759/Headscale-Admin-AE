# 内置 DERP

DERP 在节点无法建立直连时转发加密数据包。Headscale-Admin-AE 推荐使用控制面内置的
受控 DERP，不需要单独维护定制 DERP 仓库。

## 推荐配置

```yaml
derp:
  server:
    enabled: true
    region_id: 999
    region_code: scaletail
    region_name: ScaleTail Embedded DERP
    domain: derp.example.com
    stun_listen_addr: 0.0.0.0:3478
    verify_clients: true
    private_key_path: /var/lib/headscale/derp.key
  urls: []
  paths: []
  auto_update_enabled: false
```

## 强制规则

- DERP 域名必须使用客户端信任的 HTTPS 证书。
- `verify_clients` 必须为 `true`，未知或已失效节点不能使用中继。
- 只使用内置 DERP 时保持 `urls: []`、`paths: []` 和
  `auto_update_enabled: false`，避免外部 DERP map 改变信任边界。
- DERP 私钥必须持久化，并与 Noise 私钥使用不同文件和不同密钥。
- 不要把私钥写入镜像、Compose、环境变量、日志或 Git 仓库。
- 公网防火墙至少放行 DERP HTTPS 端口和 STUN `UDP/3478`。
- `region_id` 在整个 DERP map 中必须唯一。
- 反向代理必须支持长连接，并为 DERP 路径关闭不合理的短超时。

## 客户端校验

`verify_clients: true` 会根据 Headscale 当前节点状态决定是否允许连接。账户禁用、密码
到期或节点到期后，节点不能继续把 DERP 当成匿名公共中继。

## 可用性

只有一个内置 DERP 时，它是单点故障。DERP 不可用不会自动破坏已经建立的点对点
连接，但会影响无法直连的节点和新的 NAT 穿透。生产环境需要高可用时，部署多个受控
region，并逐个保持客户端校验、TLS、私钥隔离和监控。

## 验证清单

- Headscale 启动日志显示 DERP region 已加入 map。
- 公网可访问 DERP HTTPS 端口。
- `UDP/3478` 可达。
- 已登录 ScaleTail 的 netcheck 能看到目标 region。
- 未注册客户端无法使用中继。
- 重启容器后 DERP 公钥不变化，证明私钥卷已正确持久化。
