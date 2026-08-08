#!/bin/sh
set -eu

config_template="/etc/headscale/config.yaml.tmpl"
config_output="${HEADSCALE_CONFIG_OUT:-/var/lib/headscale-config/config.yaml}"

render_yaml_list() {
  input="$1"
  fallback="$2"
  if [ -z "$input" ]; then input="$fallback"; fi
  printf '%s' "$input" | awk -v RS='[,;]' '
    {
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", $0)
      if ($0 != "") printf "      - %s\n", $0
    }
  '
}

HEADSCALE_MAGIC_DNS="${HEADSCALE_MAGIC_DNS:-true}"
HEADSCALE_DNS_OVERRIDE_LOCAL="${HEADSCALE_DNS_OVERRIDE_LOCAL:-true}"
HEADSCALE_DNS_GLOBAL_YAML="$(render_yaml_list "${HEADSCALE_DNS_GLOBAL:-}" "1.1.1.1,8.8.8.8")"
HEADSCALE_DNS_SEARCH_YAML=""
if [ -n "${HEADSCALE_DNS_SEARCH_DOMAINS:-}" ]; then
  HEADSCALE_DNS_SEARCH_YAML="  search_domains:
$(render_yaml_list "$HEADSCALE_DNS_SEARCH_DOMAINS" "")"
fi
export HEADSCALE_MAGIC_DNS HEADSCALE_DNS_OVERRIDE_LOCAL
export HEADSCALE_DNS_GLOBAL_YAML HEADSCALE_DNS_SEARCH_YAML

for socket_dir in \
  /var/run/scaleforge \
  /var/run/scaleforge/control \
  /var/run/scaleforge/client
do
  if [ -L "$socket_dir" ]; then
    echo "[entrypoint] refusing symbolic-link socket directory: $socket_dir" >&2
    exit 1
  fi
done

install -d -m 0750 -o headscale -g headscale /var/lib/headscale "$(dirname "$config_output")"
install -d -m 0770 -o headscale -g headscale /var/run/headscale
install -d -m 2750 -o headscale -g scaleforge /var/run/scaleforge/control
install -d -m 2750 -o headscale -g scaleforge /var/run/scaleforge/client

temporary_config="${config_output}.tmp"
envsubst < "$config_template" > "$temporary_config"
if ! grep -q '^scaleforge:' "$temporary_config"; then
  echo "[entrypoint] rendered config has no scaleforge section" >&2
  rm -f "$temporary_config"
  exit 1
fi
chown headscale:headscale "$temporary_config"
chmod 0600 "$temporary_config"
mv -f "$temporary_config" "$config_output"

exec gosu headscale headscale serve -c "$config_output"
