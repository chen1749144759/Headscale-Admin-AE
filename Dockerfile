FROM golang:1.26.5-bookworm AS builder

ARG VERSION=dev

WORKDIR /src
COPY . .

RUN go build -trimpath \
    -ldflags="-s -w -X github.com/juanfont/headscale/hscontrol/types.Version=${VERSION}" \
    -o /out/headscale ./cmd/headscale

FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates curl gettext-base gosu && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/headscale /usr/local/bin/headscale

RUN groupadd --gid 10101 scaleforge && \
    groupadd --gid 10102 headscale && \
    useradd --uid 10002 --gid 10102 --groups 10101 --no-create-home --shell /usr/sbin/nologin headscale && \
    mkdir -p /etc/headscale /var/lib/headscale /var/lib/headscale-config

COPY docker/entrypoint.sh /entrypoint.sh
COPY docker/config.yaml.tmpl /etc/headscale/config.yaml.tmpl
RUN chmod +x /entrypoint.sh

EXPOSE 8080
EXPOSE 3478/udp
EXPOSE 9090

ENTRYPOINT ["/entrypoint.sh"]
