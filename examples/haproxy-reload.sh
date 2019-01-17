#!/bin/bash
set -euo pipefail
IFS=$'\n\t'

HAPROXY_PID="$(pgrep -o -x haproxy || true)"
if [ -z "$HAPROXY_PID" ]; then
	haproxy -W -D -f ${HAP_CONFIG}
else
	kill -USR2 "$HAPROXY_PID"
fi
