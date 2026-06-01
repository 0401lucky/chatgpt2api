#!/bin/sh
set -eu

CHATGPT2API_GO_PORT="${CHATGPT2API_GO_PORT:-8001}"
export CHATGPT2API_GO_PORT

chatgpt2api-go &
go_pid="$!"

nginx -g "daemon off;" &
nginx_pid="$!"

shutdown() {
    kill -TERM "$nginx_pid" 2>/dev/null || true
    kill -TERM "$go_pid" 2>/dev/null || true
    wait "$nginx_pid" 2>/dev/null || true
    wait "$go_pid" 2>/dev/null || true
}

trap shutdown INT TERM

while true; do
    if ! kill -0 "$go_pid" 2>/dev/null; then
        wait "$go_pid"
        exit "$?"
    fi
    if ! kill -0 "$nginx_pid" 2>/dev/null; then
        wait "$nginx_pid"
        exit "$?"
    fi
    sleep 1
done
