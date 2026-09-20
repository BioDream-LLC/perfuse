#!/usr/bin/env python3
"""Probe the fixture channel's MLLP listener for a whole end-to-end run.

The companion to scripts/e2e-watchdog.sh, which watches the HTTP server and the shared session and found no fault at all in
11,000 samples across a 19-minute run. That eliminated availability and authentication, and left the question of what else the
specs share.

Two of the five specs on the flaky roll send HL7 over MLLP - flowmap and synthesis - and those two are exactly the pair that
failed together on 17 September. A third failure of synthesis reproduced the error text: read ECONNRESET, a reset rather than a
timeout, which is why it took 1.8s against a normal 3.2s instead of hitting a limit.

A reset is what the listener produces in two places. It closes a connection it has accepted but will not serve when
max_connections is reached, and it closes every live connection when the channel stops. Closing a socket with unread data in
the receive buffer sends RST, and the sender sees ECONNRESET.

This connects and closes without sending a message, because injecting HL7 would corrupt the assertions of every test that
counts traffic. A connect is enough to tell a listening socket from a bouncing one:

  ok        connected and closed cleanly
  refused   nothing is listening - the channel is down at this instant
  reset     accepted and reset - the max_connections path, or a stop landing mid-connection

Usage: ./scripts/e2e-mllp-probe.py /tmp/mllp-probe.log
"""

import errno
import json
import os
import socket
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
STATE = os.path.join(HERE, "..", "web", "e2e", ".state.json")


def wait_for_state(timeout=180):
    """Wait for the global setup to publish the run's ports."""
    deadline = time.time() + timeout
    while time.time() < deadline:
        if os.path.exists(STATE):
            try:
                with open(STATE) as handle:
                    return json.load(handle)
            except (json.JSONDecodeError, OSError):
                pass
        time.sleep(0.5)
    return None


def server_alive(pid):
    try:
        os.kill(pid, 0)
    except OSError:
        return False
    return True


def probe(port):
    """One connect, classified. Returns a short word and the elapsed milliseconds."""
    started = time.time()
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.settimeout(3.0)
    try:
        sock.connect(("127.0.0.1", port))
        # Closed without writing. The listener sees EOF, which is a clean end rather than a fault.
        sock.close()
        return "ok", (time.time() - started) * 1000
    except ConnectionRefusedError:
        return "refused", (time.time() - started) * 1000
    except ConnectionResetError:
        return "reset", (time.time() - started) * 1000
    except socket.timeout:
        return "timeout", (time.time() - started) * 1000
    except OSError as err:
        if err.errno == errno.ECONNRESET:
            return "reset", (time.time() - started) * 1000
        return f"error:{err.errno}", (time.time() - started) * 1000
    finally:
        try:
            sock.close()
        except OSError:
            pass


def port_from_channel(path):
    """Read the listener port out of the fixture channel, without a YAML library."""
    try:
        with open(path) as handle:
            for line in handle:
                stripped = line.strip()
                # The channel writes an address rather than a bare port, as "listen: 127.0.0.1:52296".
                if stripped.startswith("listen:"):
                    return int(stripped.rsplit(":", 1)[1].strip())
    except (OSError, ValueError):
        return 0
    return 0


def main():
    log_path = sys.argv[1] if len(sys.argv) > 1 else "/tmp/mllp-probe.log"

    state = wait_for_state()
    if state is None:
        print(f"no state file at {STATE}: is the suite running?", file=sys.stderr)
        return 1

    # The MLLP port is exported by the setup as an environment variable for the tests, and not written to the state file, so it
    # is read from the fixture channel's configuration instead. The setup uses a known port rather than an OS-assigned one
    # precisely so that something can reach it.
    port = int(os.environ.get("PERFUSE_E2E_MLLP", "0"))
    if port == 0:
        # Read it from the fixture channel the setup wrote. The port is asked of the OS each run rather than fixed, so it cannot be
        # guessed, and the state file records the directory holding the channel.
        port = port_from_channel(os.path.join(state["dir"], "channels", "labs.yaml"))

    if port == 0:
        print("could not find the fixture channel's MLLP port", file=sys.stderr)
        return 1

    pid = state["pid"]
    print(f"probing MLLP on 127.0.0.1:{port}, server pid {pid}, logging to {log_path}", file=sys.stderr)

    with open(log_path, "w") as log:
        while server_alive(pid):
            word, millis = probe(port)
            log.write(f"{time.time():.3f} {word} {millis:.1f}ms\n")
            log.flush()
            time.sleep(0.5)

    print(f"server pid {pid} has gone, stopping", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
