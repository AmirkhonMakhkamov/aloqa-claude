#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
source "$script_dir/lib/env.sh"

run_lk() {
  docker run --rm \
    --network "$LIVEKIT_NETWORK" \
    -e "LIVEKIT_URL=ws://$LIVEKIT_SERVICE:7880" \
    -e "LIVEKIT_API_KEY=$LK_API_KEY" \
    -e "LIVEKIT_API_SECRET=$LK_API_SECRET" \
    livekit/livekit-cli:v2.0.0 "$@"
}

usage() {
  cat <<'EOF'
usage: lk-runbook.sh <command>

commands:
  rooms
  participants <room>
  token <room> <identity>
  publish-test <room>
  webhook-tail
EOF
}

command="${1:-help}"
case "$command" in
  rooms)
    run_lk room list
    ;;
  participants)
    room="${2:-}"
    [ -n "$room" ] || { usage >&2; exit 2; }
    run_lk room participants "$room"
    ;;
  token)
    room="${2:-}"
    identity="${3:-}"
    [ -n "$room" ] && [ -n "$identity" ] || { usage >&2; exit 2; }
    run_lk token create --room "$room" --identity "$identity" --join --can-publish --can-subscribe
    ;;
  publish-test)
    room="${2:-}"
    [ -n "$room" ] || { usage >&2; exit 2; }
    run_lk load-test --room "$room" --publishers 1 --duration 30s
    ;;
  webhook-tail)
    docker compose -p "$LIVEKIT_PROJECT" -f "$LIVEKIT_COMPOSE" logs -f "$LIVEKIT_SERVICE" | grep -E "webhook|http|participant|room"
    ;;
  help|--help|-h)
    usage
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac
