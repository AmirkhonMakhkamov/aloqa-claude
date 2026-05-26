#!/usr/bin/env bash

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  echo "portable.sh must be sourced" >&2
  exit 2
fi

portable_timeout() {
  local seconds="$1"
  local command_status watch_pid command_pid did_timeout
  shift

  if command -v timeout >/dev/null 2>&1; then
    timeout "$seconds" "$@"
    return $?
  fi

  did_timeout=0
  "$@" &
  command_pid=$!
  (
    sleep "$seconds"
    did_timeout=1
    kill -TERM "$command_pid" 2>/dev/null || true
  ) &
  watch_pid=$!

  wait "$command_pid"
  command_status=$?

  kill -TERM "$watch_pid" 2>/dev/null || true
  wait "$watch_pid" 2>/dev/null || true

  if [ "$command_status" -eq 143 ] || [ "$did_timeout" -eq 1 ]; then
    return 124
  fi

  return "$command_status"
}
