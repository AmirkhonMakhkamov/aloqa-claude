#!/usr/bin/env bash
set -euo pipefail

IFS='
	'

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
source "$script_dir/lib/env.sh"
source "$script_dir/lib/portable.sh"

cd "$ALOQA_LIVEKIT_OBSERVABILITY_ROOT"

"$script_dir/secret-guard.sh"

if ! docker network inspect "$LIVEKIT_NETWORK" >/dev/null 2>&1; then
  cat >&2 <<EOF
LiveKit Docker network "$LIVEKIT_NETWORK" was not found.
Start the dev SFU first:
  docker compose -f scripts/livekit/docker-compose.dev.yml up -d
If you use a custom compose project, export LIVEKIT_PROJECT and LIVEKIT_NETWORK.
EOF
  exit 2
fi

docker compose -p "$OBSERVABILITY_PROJECT" -f docker-compose.observability.yml pull
docker compose -p "$OBSERVABILITY_PROJECT" -f docker-compose.observability.yml up -d

portable_timeout 60 bash -c 'until "$1" --quiet; do sleep 2; done' _ "$script_dir/status.sh"

cat <<EOF
LiveKit observability is ready.
Prometheus: http://localhost:$PROM_PORT
Grafana:    http://localhost:$GRAFANA_PORT
Grafana login: $GRAFANA_ADMIN_USER / $GRAFANA_ADMIN_PASSWORD
Fallback dashboard: LiveKit / LiveKit Dev Fallback
EOF
