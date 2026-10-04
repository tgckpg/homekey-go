#!/bin/sh
set -eu
umask 077

# Explicit arguments are passed directly to the CLI (e.g. -status -state ...).
if [ "$#" -gt 0 ]; then
    exec /usr/local/bin/homekey "$@"
fi

set -- -state /data/state
if [ -f /data/options.json ]; then
    jq -e '
        type == "object" and
        (.name | type == "string" and length > 0) and
        (.serial | type == "string" and length > 0) and
        (.port | type == "number" and . == floor and . >= 1 and . <= 65535) and
        (.interface | type == "string") and
        (.finish | . == "silver" or . == "black" or . == "gold" or . == "tan") and
        (.pin | type == "string")
    ' /data/options.json >/dev/null || {
        echo 'Invalid Home Key Go options; check the app configuration.' >&2
        exit 1
    }
    name=$(jq -r '.name' /data/options.json)
    serial=$(jq -r '.serial' /data/options.json)
    port=$(jq -r '.port' /data/options.json)
    iface=$(jq -r '.interface' /data/options.json)
    finish=$(jq -r '.finish' /data/options.json)
    pin=$(jq -r '.pin' /data/options.json)
    set -- "$@" -name "$name" -serial "$serial" -addr ":$port" -finish "$finish"
    if [ -n "$iface" ]; then
        set -- "$@" -interface "$iface"
    fi
    if [ -n "$pin" ]; then
        set -- "$@" -pin "$pin"
    fi
fi

# exec lets the Go service receive SIGTERM and release its state lock.
exec /usr/local/bin/homekey "$@"
