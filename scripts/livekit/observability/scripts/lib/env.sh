#!/usr/bin/env bash

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  echo "env.sh must be sourced" >&2
  exit 2
fi

aloqa_livekit_repo_root="$(git rev-parse --show-toplevel 2>/dev/null)" || {
  echo "env.sh: run from inside the aloqa-claude repository" >&2
  return 3
}

aloqa_livekit_repo_root="$(cd "$aloqa_livekit_repo_root" && pwd -P)"
aloqa_livekit_repo_leaf="$(basename "$aloqa_livekit_repo_root")"

export ALOQA_LIVEKIT_REPO_ROOT="${ALOQA_LIVEKIT_REPO_ROOT:-$aloqa_livekit_repo_root}"
export ALOQA_LIVEKIT_OBSERVABILITY_ROOT="${ALOQA_LIVEKIT_OBSERVABILITY_ROOT:-$ALOQA_LIVEKIT_REPO_ROOT/scripts/livekit/observability}"
export LIVEKIT_COMPOSE="${LIVEKIT_COMPOSE:-$ALOQA_LIVEKIT_REPO_ROOT/scripts/livekit/docker-compose.dev.yml}"
export LIVEKIT_PROJECT="${LIVEKIT_PROJECT:-$aloqa_livekit_repo_leaf}"
export LIVEKIT_SERVICE="${LIVEKIT_SERVICE:-livekit-server}"
export LIVEKIT_NETWORK="${LIVEKIT_NETWORK:-${LIVEKIT_PROJECT}_default}"
export OBSERVABILITY_PROJECT="${OBSERVABILITY_PROJECT:-livekit-observability}"
export INVENTORY_PROJECT="${INVENTORY_PROJECT:-livekit-inventory}"
export PROM_PORT="${PROM_PORT:-9090}"
export GRAFANA_PORT="${GRAFANA_PORT:-3001}"
export GRAFANA_ADMIN_USER="${GRAFANA_ADMIN_USER:-admin}"
export GRAFANA_ADMIN_PASSWORD="${GRAFANA_ADMIN_PASSWORD:-admin}"
export LK_API_KEY="${LK_API_KEY:-APIaloqaDev}"
export LK_API_SECRET="${LK_API_SECRET:-aloqa-livekit-development-secret-32bytes-min}" # guard:bootstrap-only
