#!/usr/bin/env bash
set -euo pipefail

IFS='
	'

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
source "$script_dir/lib/env.sh"

cd "$ALOQA_LIVEKIT_OBSERVABILITY_ROOT"

is_placeholder_value() {
  case "$1" in
    ""|\$\{*|"$"*|"<"*">"|CHANGEME|REPLACE_ME)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

is_high_entropy_value() {
  local value="$1"

  case "$value" in
    eyJ*.*.*)
      return 0
      ;;
    http://*|https://*|ws://*|wss://*)
      return 1
      ;;
  esac

  case "$value" in
    *" "*|*"["*|*"]"*|*"{"*|*"}"*|*"("*|*")"*|*","*)
      return 1
      ;;
  esac

  if [ "${#value}" -ge 24 ] && printf '%s' "$value" | grep -Eq '[A-Z]' && printf '%s' "$value" | grep -Eq '[a-z]' && printf '%s' "$value" | grep -Eq '[0-9]'; then
    return 0
  fi

  if [ "${#value}" -ge 32 ] && printf '%s' "$value" | grep -Eq '^[A-Za-z0-9+/=]+$'; then
    return 0
  fi

  return 1
}

reason_for_line() {
  local line="$1"
  local key value

  case "$line" in
    *"# guard:bootstrap-only"*|*"# guard:config-key"*)
      return 1
      ;;
    *"eyJ*.*.*)"*)
      return 1
      ;;
    *eyJ*.*.*)
      printf 'jwt-shaped token'
      return 0
      ;;
  esac

  if [[ "$line" == *:* || "$line" == *=* ]]; then
    key="$(printf '%s' "$line" | LC_ALL=C sed -E 's/[:=].*$//' | tr '[:upper:]' '[:lower:]' | tr -d ' "	')"
    value="$(printf '%s' "$line" | LC_ALL=C sed -E 's/^[^:=]*[:=][[:space:]]*//' | LC_ALL=C sed -E 's/[[:space:]]+#.*$//' | tr -d '"')"
    value="${value//\'/}"
    case "$key" in
      ""|*[!a-z0-9_.-]*)
        return 1
        ;;
    esac
    case "$key" in
      *api_secret*|*api_key*|*password*|*passwd*|*token*|*jwt*|*bearer*|*private_key*|*client_secret*|*webhook_secret*)
        if ! is_placeholder_value "$value" && is_high_entropy_value "$value"; then
          printf 'secret-looking value for key %s' "$key"
          return 0
        fi
        ;;
    esac
  fi

  return 1
}

failed=0
while IFS= read -r file; do
  line_no=0
  while IFS= read -r line || [ -n "$line" ]; do
    line_no=$((line_no + 1))
    reason="$(reason_for_line "$line" || true)"
    if [ -n "$reason" ]; then
      printf '%s:%s: %s\n' "$file" "$line_no" "$reason" >&2
      failed=1
    fi
  done < "$file"
done <<EOF
$(find . -type f \
  ! -path './.state/*' \
  ! -path './secrets/*' \
  ! -path '*/__pycache__/*' \
  ! -name '*.pyc' \
  ! -name '*.png' \
  ! -name '*.jpg' \
  ! -name '*.jpeg' \
  ! -name '*.gif' \
  | sort)
EOF

exit "$failed"
