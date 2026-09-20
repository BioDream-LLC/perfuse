import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// A lock so two runs of this suite cannot overlap.
//
// Why. The suite shares three things between every test: one server, one signed-in session in .auth/admin.json, and one .state.json
// naming the server's pid and directory. None of that is per-run, so a second run starting while the first is going overwrites the state
// file, and then whichever teardown finishes first kills the other run's server and deletes the auth file underneath it.
//
// What that looks like from the outside is nothing like the cause. It produced a run of 279 failures whose first message was
// "Error reading storage state from ./e2e/.auth/admin.json: ENOENT", a run that stopped early reporting 217 passed, and two tests
// failing with "bind: address already in use" - none of which say that two runs are fighting. An hour went into reading them as defects.
//
// The lock holds a pid, so a stale file left by an interrupted run is recognised rather than becoming a permanent blockage: if the
// process it names is gone, the lock is taken over and said so. That distinction matters more than the lock itself, because a lock that
// can wedge the suite is worse than no lock at all - it is the kind of thing somebody deletes in frustration along with the safety it
// was providing.

const here = dirname(fileURLToPath(import.meta.url));
const LOCK_FILE = join(here, ".run.lock");

interface LockContents {
  pid: number;
  startedAt: string;
}

/** stillRunning reports whether a pid belongs to a live process. */
function stillRunning(pid: number): boolean {
  if (!pid || pid <= 0) return false;

  try {
    // Signal 0 checks for existence without touching the process.
    process.kill(pid, 0);

    return true;
  } catch {
    return false;
  }
}

/**
 * acquireRunLock claims the suite for this process, or throws saying who has it.
 *
 * Called at the top of the global setup, before a port is chosen or a server started, so a second run stops before it can damage the
 * first rather than part way through.
 */
export function acquireRunLock(): void {
  if (existsSync(LOCK_FILE)) {
    let held: LockContents | null = null;

    try {
      held = JSON.parse(readFileSync(LOCK_FILE, "utf8")) as LockContents;
    } catch {
      // An unreadable lock is treated as stale. A half-written file from a run killed mid-write is not a reason to refuse for ever.
      held = null;
    }

    if (held && stillRunning(held.pid)) {
      throw new Error(
        `another run of this suite is in progress (pid ${held.pid}, started ${held.startedAt}).\n` +
          `Two runs share one server, one signed-in session and one state file, so they corrupt each other in ways that read as\n` +
          `unrelated test failures. Wait for it to finish, or stop it and delete ${LOCK_FILE}.`,
      );
    }

    if (held) {
      console.log(`  taking over a lock left by pid ${held.pid}, which is no longer running`);
    }
  }

  mkdirSync(here, { recursive: true });
  writeFileSync(
    LOCK_FILE,
    JSON.stringify({ pid: process.pid, startedAt: new Date().toISOString() } satisfies LockContents, null, 2),
  );
}

/**
 * releaseRunLock gives the suite back.
 *
 * Only if this process holds it. A teardown that deleted the lock unconditionally would hand the suite to a second run that is already
 * waiting, which is the situation the lock exists to prevent.
 */
export function releaseRunLock(): void {
  if (!existsSync(LOCK_FILE)) return;

  try {
    const held = JSON.parse(readFileSync(LOCK_FILE, "utf8")) as LockContents;
    if (held.pid !== process.pid) {
      console.log(`  leaving the run lock alone: it belongs to pid ${held.pid}, not this process`);

      return;
    }
  } catch {
    // Unreadable, so nobody can be relying on it.
  }

  rmSync(LOCK_FILE, { force: true });
}
