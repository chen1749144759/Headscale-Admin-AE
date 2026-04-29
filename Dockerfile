# ====== 第一阶段：编译 ======
FROM golang:1-bookworm AS builder

ARG VERSION=dev

WORKDIR /src
COPY . .

RUN go build -trimpath \
    -ldflags="-s -w -X github.com/juanfont/headscale/hscontrol/types.Version=${VERSION}" \
    -o /out/headscale ./cmd/headscale

# ====== 第二阶段：运行 ======
FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates curl gettext-base && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/headscale /usr/local/bin/headscale

RUN mkdir -p /etc/headscale /var/lib/headscale

# 复制入口脚本和配置模板
COPY docker/entrypoint.sh /entrypoint.sh
COPY docker/config.yaml.tmpl /etc/headscale/config.yaml.tmpl
RUN chmod +x /entrypoint.sh

# 主服务端口（Noise 协议 + HTTP API）
EXPOSE 8080
# gRPC 端口
EXPOSE 50443
# STUN 端口（内嵌 DERP）
EXPOSE 3478/udp
# Metrics 端口（可选）
EXPOSE 9090

ENTRYPOINT ["/entrypoint.sh"]
