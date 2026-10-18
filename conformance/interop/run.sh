#!/bin/sh
# Builds the reference implementations Perfuse's interoperability tests run against, and runs those tests.
#
#   sh conformance/interop/run.sh          build the images and run every interop test
#   sh conformance/interop/run.sh build    build the images only, and print the variables to set
#
# Needs Docker. Each test skips without its variable, so a plain `go test ./...` needs none of this.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
work=${PERFUSE_INTEROP_DIR:-"$HOME/.pftest"}
mkdir -p "$work/febrl"

docker build -q -t pf-pyas2lib "$here/pyas2lib" >/dev/null
docker build -q -t pf-pydifact "$here/pydifact" >/dev/null
docker build -q -t pf-febrl "$here/febrl" >/dev/null
docker run --rm -v "$work/febrl:/work" pf-febrl
if ! docker ps --format '{{.Names}}' | grep -qx pf-greenmail; then
	docker rm -f pf-greenmail >/dev/null 2>&1 || true
	# GreenMail 2.1.3, with the two accounts the mail tests sign in as.
	docker run -d --name pf-greenmail -p 3025:3025 -p 3143:3143 -p 3993:3993 -p 3110:3110 -p 3995:3995 \
		-e GREENMAIL_OPTS='-Dgreenmail.setup.test.all -Dgreenmail.hostname=0.0.0.0 -Dgreenmail.users=feed:feedpass@lab.example,pop:poppass@lab.example -Dgreenmail.users.login=local_part' \
		greenmail/standalone:2.1.3 >/dev/null
	sleep 5
fi

vars="PERFUSE_TEST_PYAS2LIB=pf-pyas2lib PERFUSE_TEST_PYDIFACT=pf-pydifact PERFUSE_TEST_FEBRL=$work/febrl/dataset3.csv PERFUSE_TEST_GREENMAIL=127.0.0.1"
if [ "${1:-}" = build ]; then
	echo "$vars"
	exit 0
fi
cd "$root"
# shellcheck disable=SC2086
env $vars go test -count=1 -run 'PyAS2Lib|Pydifact|FEBRL|GreenMail' ./internal/as2 ./internal/edifact ./internal/engine ./internal/mpi ./internal/mailbox
