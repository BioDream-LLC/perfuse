#!/usr/bin/env bash
# Poll the end-to-end server for the whole of a run, so a transient fault is visible even when no test fails.
#
# Why this exists. Five specs have each failed once in a full run and none reproduces alone, which points at one shared cause
# appearing in whichever test happens to be running rather than at five flaky tests. Two of the five failed in 233ms and 1.8s
# against a twenty-minute suite, and a failure that fast is not a timeout - it is something not being ready.
#
# Chasing the specs was never going to work. What this does instead is watch the thing they share. The suite runs with one worker
# and one signed-in session established once by the global setup, so the server and that session are the shared state. If either
# blinks, this records when - and a blip appears in a passing run too, which turns a one-in-three flake into something measurable
# on every run.
#
# Usage: start it after the suite is running, and it discovers the port from the setup's own state file.
#
#   ./scripts/e2e-watchdog.sh /tmp/watchdog.log

set -uo pipefail

LOG="${1:-/tmp/e2e-watchdog.log}"
STATE="$(dirname "$0")/../web/e2e/.state.json"
AUTH="$(dirname "$0")/../web/e2e/.auth/admin.json"

# Wait for the global setup to publish the port. The suite spends its first half-minute building fixtures.
for _ in $(seq 1 120); do
  [ -f "$STATE" ] && break
  sleep 1
done

if [ ! -f "$STATE" ]; then
  echo "no state file at $STATE: is the suite running?" >&2
  exit 1
fi

URL="$(python3 -c "import json,sys; print(json.load(open('$STATE'))['url'])")"
PID="$(python3 -c "import json,sys; print(json.load(open('$STATE'))['pid'])")"

# The session the whole suite shares. Polling an authenticated endpoint checks the session as well as the socket, because a
# session that stopped working would fail tests instantly in exactly the way observed.
COOKIE="$(python3 - "$AUTH" <<'PY'
import json, sys
state = json.load(open(sys.argv[1]))
for c in state.get("cookies", []):
    if c["name"] == "perfuse_session":
        print(f"perfuse_session={c['value']}")
        break
PY
)"

if [ -z "$COOKIE" ]; then
  echo "no session cookie in $AUTH" >&2
  exit 1
fi

echo "watching $URL pid=$PID every 500ms, logging to $LOG" >&2
: > "$LOG"

# Every sample is written, not only the failures. A run with no blip at all is a real result: it rules out the server and the
# session and moves the search to the browser.
while kill -0 "$PID" 2>/dev/null; do
  now="$(python3 -c 'import time; print(f"{time.time():.3f}")')"

  # Two probes per sample, because they fail differently. The API call needs the session; the static page needs only the server,
  # so an authentication fault and an availability fault can be told apart afterwards.
  api="$(curl -s -o /dev/null -w '%{http_code}:%{time_total}' -m 5 -H "Cookie: $COOKIE" "$URL/api/channels" 2>/dev/null || echo "000:0")"
  page="$(curl -s -o /dev/null -w '%{http_code}:%{time_total}' -m 5 "$URL/" 2>/dev/null || echo "000:0")"

  echo "$now api=$api page=$page" >> "$LOG"

  # Half a second rather than a tenth. At 100ms this spawned twenty curl processes a second and stretched a nineteen-minute run to
  # twenty-eight, which failed two tests on timeouts that had nothing wrong with them - the instrument changed the thing it measured.
  sleep 0.5
done

echo "server pid $PID has gone, stopping" >&2
