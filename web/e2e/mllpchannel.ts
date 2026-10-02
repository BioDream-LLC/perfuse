import { createConnection, createServer } from "node:net";

/**
 * Standing up a channel of your own and sending traffic to it.
 *
 * Separate from mllp.ts, which sends to a listener the harness already has. These three are for a test that needs its own channel on
 * its own port: freePort asks the operating system rather than guessing, sendMLLPTo takes the port explicitly, and admitMessage is an
 * admission with a control ID the caller chooses so it can be found again.
 *
 * Extracted from livetraffic.spec.ts because importing a helper from a .spec.ts file re-registers that file's tests in the importer -
 * the profile spec quietly ran three tests for two, and the extra one was livetraffic's.
 *
 * Named apart from mllp.ts deliberately. Both had a sendMLLP, with different signatures, and merging them would have meant one caller
 * silently passing a message where a port was expected.
 */

export function freePort(): Promise<number> {
  return new Promise((res, rej) => {
    const srv = createServer();
    srv.on("error", rej);
    srv.listen(0, "127.0.0.1", () => {
      const addr = srv.address();
      if (addr && typeof addr === "object") {
        const p = addr.port;
        srv.close(() => res(p));
      } else {
        srv.close(() => rej(new Error("no port")));
      }
    });
  });
}

// MLLP framing: a start byte, the message, then an end sequence. Getting this wrong is the commonest mistake in a first HL7
// integration, so it is written out rather than hidden in a helper.
const VT = "\x0b";
const FS = "\x1c";
const CR = "\r";

/** sendMLLP delivers one framed message and resolves with the acknowledgement. */
export function sendMLLPTo(port: number, message: string, timeoutMs = 15_000): Promise<string> {
  return new Promise((resolve, reject) => {
    const sock = createConnection({ host: "127.0.0.1", port }, () => {
      sock.write(VT + message + FS + CR);
    });

    let buf = "";
    const timer = setTimeout(() => {
      sock.destroy();
      reject(new Error(`no acknowledgement within ${timeoutMs}ms. Received so far: ${JSON.stringify(buf)}`));
    }, timeoutMs);

    sock.on("data", (d) => {
      buf += d.toString("binary");
      // A complete frame has arrived once the end byte is seen.
      if (buf.includes(FS)) {
        clearTimeout(timer);
        sock.end();
        resolve(buf.replace(VT, "").replace(FS, "").replace(/\r$/, ""));
      }
    });

    sock.on("error", (e) => {
      clearTimeout(timer);
      reject(e);
    });
  });
}

/**
 * An ADT^A01 with a control id the test can search for.
 *
 * Written here rather than taken from a fixture so the control id is unique per run. Reusing one would make the message search
 * ambiguous the second time the suite runs against a persistent database.
 */
export function admitMessage(controlId: string): string {
  return [
    `MSH|^~\\&|SENDING|SITEA|PERFUSE|SITEB|20260823090000||ADT^A01|${controlId}|P|2.5.1`,
    "EVN|A01|20260823090000",
    "PID|1||MRN00042^^^SITEA^MR||Evrard^Camille^M||19620314|F|||12 Rue Lafayette^^Paris^^75009^FR",
    "PV1|1|I|ICU^04^01||||1234^Okafor^Ada|||MED||||||||V01",
  ].join(CR);
}
