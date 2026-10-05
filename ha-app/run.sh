#!/bin/sh
set -eu
umask 077
if [ "$#" -gt 0 ]; then
    exec /usr/local/bin/homekey "$@"
fi
exec /usr/local/bin/homekey -state /data/state -config /data/options.json -web-addr 0.0.0.0:8099 -ingress
