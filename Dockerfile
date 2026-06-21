FROM golang:1-bookworm AS builder

ARG VERSION=dev

WORKDIR /src
COPY . .

RUN go build -trimpath \
    -ldflags="-s -w -X github.com/juanfont/headscale/hscontrol/types.Version=${VERSION}" \
    -o /out/headscale ./cmd/headscale

FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates curl gettext-base && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/headscale /usr/local/bin/headscale

RUN mkdir -p /etc/headscale /var/lib/headscale

COPY docker/entrypoint.sh /entrypoint.sh
COPY docker/config.yaml.tmpl /etc/headscale/config.yaml.tmpl
RUN chmod +x /entrypoint.sh

EXPOSE 8080
EXPOSE 50443
EXPOSE 3478/udp
EXPOSE 9090

ENTRYPOINT ["/entrypoint.sh"]
