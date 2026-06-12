#!/usr/bin/env bash
# Seed a few image attachments into a channel so the in-app image lightbox can
# be tested manually. Posts one message per run and uploads sample images to it.
#
# Idempotent: if the sentinel showcase message already exists in the channel,
# the script skips re-seeding.
#
# Run scripts/seed.sh first (it creates the users / workspace / channels).
#
# Usage:
#   ./scripts/seed-images.sh                 # against http://localhost:8090
#   ALOQA_API=http://host:8090/api/v1 ./scripts/seed-images.sh
#   ALOQA_CHANNEL=random ./scripts/seed-images.sh   # target #random instead of #general
#
# Requires: bash 3.2+, curl, jq.

set -euo pipefail

API="${ALOQA_API:-http://localhost:8090/api/v1}"
WS_SLUG="${ALOQA_WS_SLUG:-aloqa-test}"
CHANNEL_SLUG="${ALOQA_CHANNEL:-general}"
EMAIL="${ALOQA_EMAIL:-bob@aloqa.test}"
PASSWORD="${ALOQA_PASSWORD:-BobPass123!}"
SENTINEL='📷 Lightbox test images'
IMAGE_COUNT="${ALOQA_IMAGE_COUNT:-4}"

for bin in curl jq; do
  command -v "$bin" >/dev/null || { echo "missing dependency: $bin" >&2; exit 1; }
done

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

log() { printf '\033[36m==\033[0m %s\n' "$*"; }
note() { printf '   %s\n' "$*"; }

log "logging in as $EMAIL"
TOKEN=$(curl -fsS -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg e "$EMAIL" --arg p "$PASSWORD" '{email:$e,password:$p}')" | jq -r '.access_token')
[[ -n "$TOKEN" && "$TOKEN" != "null" ]] || { echo "login failed (run scripts/seed.sh first)" >&2; exit 1; }

log "resolving workspace #$WS_SLUG"
WS_ID=$(curl -fsS "$API/workspaces" -H "Authorization: Bearer $TOKEN" \
  | jq -r --arg s "$WS_SLUG" '.[] | select(.slug == $s) | .id' | head -1)
[[ -n "$WS_ID" ]] || { echo "workspace $WS_SLUG not found" >&2; exit 1; }
note "workspace: $WS_ID"

log "resolving channel #$CHANNEL_SLUG"
CHANNEL_ID=$(curl -fsS "$API/workspaces/$WS_ID/channels" -H "Authorization: Bearer $TOKEN" \
  | jq -r --arg s "$CHANNEL_SLUG" '.[] | select(.slug == $s or .name == $s) | .id' | head -1)
[[ -n "$CHANNEL_ID" ]] || { echo "channel $CHANNEL_SLUG not found" >&2; exit 1; }
note "channel: $CHANNEL_ID"

# Idempotency: bail out if a previous run already seeded the showcase message.
if curl -fsS "$API/workspaces/$WS_ID/channels/$CHANNEL_ID/messages?limit=50" \
  -H "Authorization: Bearer $TOKEN" \
  | jq -e --arg s "$SENTINEL" '[.items[]? | select(.content == $s)] | length > 0' >/dev/null; then
  note "showcase message already present — skipping"
  exit 0
fi

log "posting showcase message"
MESSAGE_ID=$(curl -fsS -X POST "$API/workspaces/$WS_ID/channels/$CHANNEL_ID/messages" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg c "$SENTINEL" '{content:$c}')" | jq -r '.id')
[[ -n "$MESSAGE_ID" && "$MESSAGE_ID" != "null" ]] || { echo "message create failed" >&2; exit 1; }
note "message: $MESSAGE_ID"

log "uploading $IMAGE_COUNT sample images"
for i in $(seq 1 "$IMAGE_COUNT"); do
  # Distinct landscape images; picsum gives a stable image per seed.
  src="$TMP/sample-$i.jpg"
  if ! curl -fsSL "https://picsum.photos/seed/aloqa-$i/1200/800.jpg" -o "$src"; then
    echo "failed to download sample image $i (no network?)" >&2
    exit 1
  fi
  code=$(curl -sS -o "$TMP/resp" -w '%{http_code}' \
    -X POST "$API/workspaces/$WS_ID/channels/$CHANNEL_ID/messages/$MESSAGE_ID/attachments" \
    -H "Authorization: Bearer $TOKEN" \
    -F "file=@$src;type=image/jpeg;filename=sample-$i.jpg")
  if [[ "$code" != "200" && "$code" != "201" ]]; then
    echo "attachment upload $i failed ($code): $(cat "$TMP/resp")" >&2
    exit 1
  fi
  note "uploaded sample-$i.jpg"
done

log "done — open #$CHANNEL_SLUG and click an image to test the lightbox"
