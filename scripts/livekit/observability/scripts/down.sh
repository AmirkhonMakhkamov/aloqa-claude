#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
source "$script_dir/lib/env.sh"

cd "$ALOQA_LIVEKIT_OBSERVABILITY_ROOT"
docker compose -p "$OBSERVABILITY_PROJECT" -f docker-compose.observability.yml down -v
