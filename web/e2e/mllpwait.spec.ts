import { test, expect } from "@playwright/test";
import { createServer } from "node:net";
import { sendMLLP, waitForListener } from "./mllp";

// Guards on the MLLP helper's waiting, which is what the flaky-spec investigation ended at.
//
// Two of the five specs on the roll sent HL7 to a channel they had just started, without waiting for it to be listening. One sent
// immediately after the start request returned and failed once at 233ms; the other slept a fixed 1500ms first and failed once at 1.8s
// with read ECONNRESET. Both times are what you get by adding up the steps before the send, so in both cases the send was simply early.
//
// That the gap is real was measured rather than assumed: a probe connecting to the fixture channel every 500ms for a whole run caught
// the port refusing connections, and the server log put a stop at 05:03:08.735 and the listener rebinding at 05:03:09.156 - 421ms with
// nothing accepting. The probe's refusal was at 05:03:09.008, inside that window.
//
// These tests do not need a running Perfuse. They use a local socket server, because what is under test is the helper's willingness to
// wait rather than anything about a channel.

test("waitForListener gives up with a message naming the port", async () => {
  // A port with nothing on it. The failure has to name the port and say what it means, because the investigation this came from was
  // slowed by messages that described a missing feature instead of a timing problem.
  const deadPort = 59_999;

  const started = Date.now();
  let message = "";

  try {
    await waitForListener(deadPort, 1_000);
  } catch (error) {
    message = String(error);
  }

  expect(message, "waiting for a port nobody is listening on should fail").toContain(String(deadPort));
  expect(message, "the message should say what a caller is meant to conclude").toMatch(/not listening|does not wait|defect/i);

  // And it should give up near the deadline rather than hanging until the test timeout, where the cause is much harder to see.
  expect(Date.now() - started, "gave up far too late").toBeLessThan(5_000);
});

test("waitForListener returns as soon as a late listener comes up", async () => {
  // The positive control. Without it the test above would pass just as well against a helper that always threw.
  const port = 59_998;

  // Bound deliberately late, which is what a channel being started looks like from the outside.
  const server = createServer();
  const openAfter = setTimeout(() => server.listen(port, "127.0.0.1"), 600);

  try {
    const started = Date.now();
    await waitForListener(port, 10_000);
    const waited = Date.now() - started;

    expect(waited, "returned before the listener could possibly have been up").toBeGreaterThan(400);
    // The point of waiting rather than sleeping: it returns when the thing is ready, not when a guessed interval expires.
    expect(waited, "waited much longer than the listener took to appear").toBeLessThan(3_000);
  } finally {
    clearTimeout(openAfter);
    await new Promise((resolve) => server.close(resolve));
  }
});

test("a send retries a connection refused before anything was written", async () => {
  // The retry that makes the helper survive a listener still binding. Safe only because nothing has been sent yet - a retry after a
  // write could deliver the same admission twice, which would be a worse defect than the flake it was covering.
  const port = 59_997;

  const server = createServer((socket) => {
    // A minimal MLLP acknowledgement, framed the way the helper expects to read it.
    socket.on("data", () => {
      socket.write("\x0bMSH|^~\\&|R|R|S|S|20260918||ACK|1|P|2.5\rMSA|AA|1\r\x1c\r");
    });
  });

  const openAfter = setTimeout(() => server.listen(port, "127.0.0.1"), 700);

  try {
    // Sent while nothing is listening. Without the retry this rejects instantly with ECONNREFUSED, which is the shape of both
    // failures on the roll.
    const ack = await sendMLLP("MSH|^~\\&|SEND|SITE|PERFUSE|SITE|20260918||ADT^A01|RETRY1|P|2.5\rPID|1||MRN1||Doe^Jane\r", port);

    expect(ack, "no acknowledgement came back").toContain("MSA");
  } finally {
    clearTimeout(openAfter);
    await new Promise((resolve) => server.close(resolve));
  }
});
