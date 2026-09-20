import { readFileSync, rmSync, existsSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { releaseRunLock } from "./runlock";

// This package is ESM, so here does not exist and has to be derived.
const here = dirname(fileURLToPath(import.meta.url));

const STATE_FILE = join(here, ".state.json");

// Stops the server and removes its data.
//
// Unconditional cleanup, including on a failed run. A test server left listening holds a port and a database, and the next
// run picks a different port and passes while the stale one accumulates - so the leak stays invisible until somebody wonders
// why there are nine perfuse processes.
export default async function globalTeardown() {
  // The lock is released in the finally below, after the server is dead and its directory is gone.
  //
  // Last, not first. Releasing it early would let a waiting run start while this one is still killing a process and deleting a
  // database, which is the overlap the lock exists to prevent - it would hand over the keys while still inside the building.
  try {
    await stopTheServer();
  } finally {
    // Whatever happened, including a run that never got as far as a state file. A lock outliving its run refuses every later run, and
    // a safety measure that wedges the suite is one somebody deletes along with the safety it was providing.
    try {
      releaseRunLock();
    } catch {
      // Never let tidying up the lock be the reason a teardown fails.
    }
  }
}

// stopTheServer is the original teardown body, unchanged apart from being named.
async function stopTheServer() {
  if (!existsSync(STATE_FILE)) {
    return;
  }

  const { pid, dir } = JSON.parse(readFileSync(STATE_FILE, "utf8")) as {
    pid: number;
    dir: string;
  };

  if (pid) {
    try {
      process.kill(pid, "SIGTERM");
      // Give it a moment to close the database cleanly, then insist.
      await new Promise((r) => setTimeout(r, 1500));
      try {
        process.kill(pid, 0);
        process.kill(pid, "SIGKILL");
      } catch {
        // already gone, which is the expected case
      }
    } catch {
      // already gone
    }
  }

  if (dir && dir.includes("perfuse-e2e-")) {
    if (process.env.PERFUSE_E2E_KEEP) {
      // Kept for an investigation, which needs the server log and the database as they were left.
      //
      // Off by default and deliberately opt-in: a run that leaves its data behind every time fills a temporary directory with
      // databases nobody deletes, and the leak stays invisible for exactly as long as it takes to matter.
      console.log(`  kept for inspection: ${dir}`);

      rmSync(STATE_FILE, { force: true });

      return;
    }

    // The name check is deliberate. This is an unconditional recursive delete, and a corrupted state file naming "/" would
    // otherwise be obeyed.
    rmSync(dir, { recursive: true, force: true });
  }

  rmSync(STATE_FILE, { force: true });
  rmSync(join(here, ".auth"), { recursive: true, force: true });
}
