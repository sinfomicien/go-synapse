#!/bin/bash
set -euo pipefail
IFS=$'\n\t'

if [ ! -S "/haproxy-master.sock" ]; then
	haproxy -W -S /haproxy-master.sock -D -f ${HAP_CONFIG}
else
	echo "reload" | nc -U /haproxy-master.sock
fi
