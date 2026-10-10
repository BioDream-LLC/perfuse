#!/bin/sh
# strongswan-down.sh - removes what strongswan-up.sh started.
docker rm -f perfuse-swan-ours perfuse-swan-theirs >/dev/null 2>&1
docker network rm perfuse-vpn-test >/dev/null 2>&1
rm -rf "${TMPDIR:-/tmp}/perfuse-strongswan"
true
