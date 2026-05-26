#!/usr/bin/env bash
set -euo pipefail

IFS='
	'

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
source "$script_dir/lib/env.sh"

quiet=0
json=0

for arg in "$@"; do
  case "$arg" in
    --quiet)
      quiet=1
      ;;
    --json)
      json=1
      ;;
    *)
      echo "status.sh: unknown argument: $arg" >&2
      exit 3
      ;;
  esac
done

http_get() {
  local url="$1"
  if command -v curl >/dev/null 2>&1; then
    curl -fsS "$url"
    return $?
  fi
  wget -qO- "$url"
}

http_get_grafana() {
  local url="$1"
  if command -v curl >/dev/null 2>&1; then
    curl -fsS -u "$GRAFANA_ADMIN_USER:$GRAFANA_ADMIN_PASSWORD" "$url"
    return $?
  fi
  wget -qO- --user="$GRAFANA_ADMIN_USER" --password="$GRAFANA_ADMIN_PASSWORD" "$url"
}

check_prometheus_ready() {
  http_get "http://localhost:$PROM_PORT/-/ready" >/dev/null 2>&1
}

check_grafana_ready() {
  http_get_grafana "http://localhost:$GRAFANA_PORT/api/health" 2>/dev/null | grep -q '"database"[[:space:]]*:[[:space:]]*"ok"'
}

check_livekit_target_up() {
  http_get "http://localhost:$PROM_PORT/api/v1/query?query=up%7Bjob%3D%22livekit%22%7D" 2>/dev/null | grep -q '"1"'
}

check_fallback_dashboard_loaded() {
  http_get_grafana "http://localhost:$GRAFANA_PORT/api/search?folderIds=*&query=LiveKit" 2>/dev/null | grep -q 'LiveKit Dev Fallback'
}

run_check() {
  local name="$1"
  local fn="$2"
  if "$fn"; then
    [ "$quiet" -eq 1 ] || [ "$json" -eq 1 ] || printf 'OK   %s\n' "$name"
    return 0
  fi
  [ "$quiet" -eq 1 ] || [ "$json" -eq 1 ] || printf 'FAIL %s\n' "$name"
  return 1
}

prometheus_ready=false
grafana_ready=false
livekit_target_up=false
fallback_dashboard_loaded=false

if run_check prometheus-ready check_prometheus_ready; then prometheus_ready=true; fi
if run_check grafana-ready check_grafana_ready; then grafana_ready=true; fi
if run_check livekit-target-up check_livekit_target_up; then livekit_target_up=true; fi
if run_check fallback-dashboard-loaded check_fallback_dashboard_loaded; then fallback_dashboard_loaded=true; fi

overall_ok=false
if [ "$prometheus_ready" = true ] && [ "$grafana_ready" = true ] && [ "$livekit_target_up" = true ] && [ "$fallback_dashboard_loaded" = true ]; then
  overall_ok=true
fi

if [ "$json" -eq 1 ]; then
  printf '{"prometheus_ready":%s,"grafana_ready":%s,"livekit_target_up":%s,"fallback_dashboard_loaded":%s,"overall_ok":%s}\n' \
    "$prometheus_ready" "$grafana_ready" "$livekit_target_up" "$fallback_dashboard_loaded" "$overall_ok"
fi

[ "$overall_ok" = true ] || exit 1
