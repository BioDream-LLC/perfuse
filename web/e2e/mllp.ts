import { connect, createServer } from "node:net";

// Sending the fixture channel a real message.
//
// The Messages tab, message detail and the trace panel all need a stored message to be anything other
// than an empty state, and until now the fixture channel listened on an OS-assigned port so nothing
// could reach it. Every assertion about those views was really an assertion about the placeholder row.

const START = 0x0b; // MLLP start block
const END = 0x1c; // MLLP end block
const CR = 0x0d;

/** anADT is a well-formed admission, so the views under test get a message that parses. */
export function anADT(controlID = "E2E00001"): string {
  return [
    `MSH|^~\\&|LAB|HOSP|EHR|HOSP|20260825120000||ADT^A01|${controlID}|P|2.5`,
    "EVN|A01|20260825120000",
    "PID|1||12345^^^HOSP^MR||DOE^JOHN^A||19800101|M|||1 High St^^Springfield^IL^62701",
    "PV1|1|I|WARD^1^01||||1234^Smith^John|||MED||||||||V01",
  ].join("\r");
}

/**
 * waitForListener waits until something accepts a connection on port.
 *
 * Starting a channel and sending to it are two separate things, and the gap between them is real. The server log for a restart of the
 * fixture channel shows the listener rebinding 421ms after the stop, and a probe connecting every 500ms for a whole run caught the
 * port refusing connections inside exactly that window.
 *
 * A test that starts a channel and then sends without waiting is reading a clock it does not control. Both specs that failed on the
 * flaky roll did that - one sent immediately after the start request returned, the other slept a fixed 1500ms first - and their
 * failure times, 233ms and 1.8s, are each what you get by adding up the steps before the send.
 *
 * Connecting is the only honest test of readiness, because accepting connections is the thing being waited for.
 */
export async function waitForListener(port: number, timeoutMs = 20_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let lastError = "never attempted";

  while (Date.now() < deadline) {
    const reached = await new Promise<boolean>((resolve) => {
      const sock = connect({ host: "127.0.0.1", port });
      const give = (ok: boolean, why?: string) => {
        if (why) lastError = why;
        sock.destroy();
        resolve(ok);
      };

      sock.setTimeout(1_000, () => give(false, "connect timed out"));
      sock.on("connect", () => give(true));
      sock.on("error", (err) => give(false, String(err)));
    });

    if (reached) return;

    await new Promise((r) => setTimeout(r, 100));
  }

  throw new Error(
    `nothing accepted a connection on 127.0.0.1:${port} within ${timeoutMs}ms. Last attempt: ${lastError}. ` +
      `A channel that was started but is not listening is a real defect; so is a test that starts one and does not wait.`,
  );
}

/**
 * sendMLLP delivers one HL7 v2 message to the fixture channel and waits for the acknowledgement.
 *
 * Waits for the ack rather than just writing and closing, because the channel records the message as
 * part of handling it. Returning early would race the assertion that follows.
 */
export function sendMLLP(message: string, port?: number): Promise<string> {
  const target = port ?? Number(process.env.PERFUSE_E2E_MLLP);
  if (!target || Number.isNaN(target)) {
    return Promise.reject(
      new Error(
        "PERFUSE_E2E_MLLP is not set, so there is no channel to send to. The global setup exports it.",
      ),
    );
  }

  return attempt(target, message, Date.now() + 20_000);
}

/**
 * attempt sends once, retrying only a connection that was refused or reset before anything was written.
 *
 * Retrying a connect is safe because no message has left yet. Retrying after a write is not, and that distinction is the whole reason
 * this is separated out: a duplicate admission delivered because a test retried blindly would be a far worse defect than the flake it
 * was papering over.
 */
function attempt(target: number, message: string, deadline: number): Promise<string> {
  return new Promise((resolve, reject) => {
    const sock = connect(target, "127.0.0.1");
    // Set when the connection opens rather than after the write returns. By the time an error arrives the bytes may already have
    // left, and the conservative reading is the safe one: treat the message as sent and refuse to retry.
    let written = false;
    const chunks: Buffer[] = [];
    // A message that is never acknowledged must fail the test rather than hang it until the suite
    // timeout, where the cause is much harder to see.
    const timer = setTimeout(() => {
      sock.destroy();
      reject(new Error(`no MLLP acknowledgement from 127.0.0.1:${target} within 10s`));
    }, 10_000);

    sock.on("error", (err) => {
      clearTimeout(timer);

      // A channel that is still binding refuses or resets the connection. That is worth waiting out rather than failing, but only
      // before anything has been sent - after a write, a retry could deliver the same message twice.
      const code = (err as NodeJS.ErrnoException).code;
      const stillComingUp = code === "ECONNREFUSED" || code === "ECONNRESET";

      if (!written && stillComingUp && Date.now() < deadline) {
        setTimeout(() => attempt(target, message, deadline).then(resolve, reject), 100);
        return;
      }

      reject(err);
    });

    sock.on("connect", () => {
      written = true;
      sock.write(Buffer.concat([Buffer.from([START]), Buffer.from(message, "utf8"), Buffer.from([END, CR])]));
    });

    sock.on("data", (d: Buffer) => {
      chunks.push(d);
      const all = Buffer.concat(chunks);
      // The ack is complete once the end block and its carriage return have arrived.
      if (all.includes(END)) {
        clearTimeout(timer);
        sock.end();
        resolve(all.toString("utf8").replace(/[\x0b\x1c]/g, ""));
      }
    });
  });
}

/**
 * freePort asks the operating system for an unused port rather than picking one.
 *
 * Two specs used to name a port outright - 19871 and 19891 - and a channel cannot start if anything already holds the number. That
 * happens more easily than it sounds: a server left behind by an interrupted run keeps its listeners, and the next run then fails with
 * "bind: address already in use" on a test that has nothing wrong with it. It cost an hour of looking at the wrong thing.
 *
 * There is a small race between closing this socket and the channel binding it, and it is accepted here for the same reason the global
 * setup accepts it: the alternative is a fixed number, which is not a smaller risk but a permanent one.
 */
export function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const probe = createServer();

    probe.on("error", reject);
    probe.listen(0, "127.0.0.1", () => {
      const address = probe.address();

      if (address && typeof address === "object") {
        const { port } = address;
        probe.close(() => resolve(port));

        return;
      }

      probe.close(() => reject(new Error("the operating system did not report a port")));
    });
  });
}
