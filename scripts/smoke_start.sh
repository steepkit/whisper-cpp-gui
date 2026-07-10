#!/usr/bin/env bash

set -euo pipefail

if (( $# < 1 || $# > 2 )); then
  printf 'usage: %s BINARY [EXPECTED_VERSION]\n' "$0" >&2
  exit 2
fi

binary="$1"
expected_version="${2:-}"
startup_attempt_limit="${SMOKE_START_MAX_ATTEMPTS:-100}"
stop_attempt_limit="${SMOKE_STOP_MAX_ATTEMPTS:-50}"

for limit in "$startup_attempt_limit" "$stop_attempt_limit"; do
  if [[ ! "$limit" =~ ^[1-9][0-9]*$ ]] || (( limit > 1000 )); then
    printf 'smoke attempt limits must be integers from 1 to 1000\n' >&2
    exit 2
  fi
done

if [[ ! -x "$binary" ]]; then
  printf 'smoke test binary is not executable: %s\n' "$binary" >&2
  exit 1
fi

if [[ -n "$expected_version" ]]; then
  version_output="$("$binary" --version)"
  if [[ "$version_output" != "whisper-cpp-gui $expected_version" ]]; then
    printf 'unexpected version output: %s\n' "$version_output" >&2
    exit 1
  fi
fi

temp_dir="$(mktemp -d)"
home_dir="$temp_dir/home"
log_path="$temp_dir/application.log"
response_path="$temp_dir/index.html"
runtime_temp="$temp_dir/tmp"
pid=""

stop_child() {
  local child_pid="$pid"
  local attempt=0
  local status=0

  if [[ -z "$child_pid" ]]; then
    return 0
  fi
  if kill -0 "$child_pid" 2>/dev/null; then
    kill -TERM "$child_pid" 2>/dev/null || true
    while kill -0 "$child_pid" 2>/dev/null && (( attempt < stop_attempt_limit )); do
      attempt=$((attempt + 1))
      sleep 0.1
    done
    if kill -0 "$child_pid" 2>/dev/null; then
      kill -KILL "$child_pid" 2>/dev/null || true
      wait "$child_pid" 2>/dev/null || true
      pid=""
      return 1
    fi
  fi

  wait "$child_pid" 2>/dev/null || status=$?
  pid=""
  return "$status"
}

cleanup() {
  stop_child || true
  rm -rf "$temp_dir"
}
trap cleanup EXIT

fail_with_log() {
  local message="$1"

  stop_child || true
  printf '%s\n' "$message" >&2
  if [[ -f "$log_path" ]]; then
    LC_ALL=C tail -c 16384 "$log_path" >&2 || true
  fi
  exit 1
}

mkdir -p "$home_dir" "$runtime_temp"
HOME="$home_dir" \
  XDG_CACHE_HOME="$home_dir/.cache" \
  XDG_CONFIG_HOME="$home_dir/.config" \
  TMPDIR="$runtime_temp" \
  "$binary" --no-browser --port 0 >"$log_path" 2>&1 &
pid=$!

base_url=""
attempt=0
while (( attempt < startup_attempt_limit )); do
  if ! kill -0 "$pid" 2>/dev/null; then
    wait "$pid" 2>/dev/null || true
    pid=""
    fail_with_log 'application exited before the UI became reachable'
  fi

  base_url="$(awk '/^Open: http:\/\/127[.]0[.]0[.]1:[0-9]+\/$/ { print $2; exit }' "$log_path")"
  if [[ -n "$base_url" ]] &&
    http_status="$(curl --noproxy '*' --silent --show-error --connect-timeout 1 --max-time 2 \
      --output "$response_path" --write-out '%{http_code}' "$base_url")" &&
    [[ "$http_status" == "200" ]] &&
    grep -Fq '<main class="app-shell" id="app">' "$response_path"; then
    break
  fi
  base_url=""
  attempt=$((attempt + 1))
  sleep 0.1
done

if [[ -z "$base_url" ]]; then
  fail_with_log 'embedded UI did not become reachable'
fi

bootstrap_path="$(awk '/^Bootstrap file: / { sub(/^Bootstrap file: /, ""); print; exit }' "$log_path")"
if [[ -z "$bootstrap_path" || ! -f "$bootstrap_path" ]]; then
  fail_with_log 'private browser bootstrap file was not created'
fi

if ! stop_child; then
  fail_with_log 'application did not shut down cleanly after SIGTERM'
fi

if [[ -e "$bootstrap_path" ]]; then
  printf 'browser bootstrap file remains after shutdown: %s\n' "$bootstrap_path" >&2
  exit 1
fi
