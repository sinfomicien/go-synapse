#!/bin/bash
haproxy -f ./hap.config  -p ./haproxy.pid -D  $([ -f ./haproxy.pid ] && echo -sf $(cat ./haproxy.pid))
