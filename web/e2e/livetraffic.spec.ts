import { test, expect } from "@playwright/test";
import { openTab } from "./nav";
import { admitMessage, freePort, sendMLLPTo } from "./mllpchannel";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

// Real traffic through a running channel, then check the interface reflects it.
//
// Everything else tested so far reads panels or converts a pasted message. This is the product's actual job: a hospital system opens
// a socket, sends an HL7 message, and expects an acknowledgement. Then the console has to show that it happened - because an
// interface engine whose console disagrees with what it did is worse than one with no console, since somebody will trust it.

/** freePort asks the operating system for a port rather than guessing one and colliding with the developer's own services. */
test("a message sent over MLLP is acknowledged and appears in the console", async ({ page }) => {
  test.setTimeout(120_000);

  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`uncaught: ${e.message}`));
  page.on("response", (r) => {
    if (r.status() >= 500) problems.push(`${r.status()} ${new URL(r.url()).pathname}`);
  });

  const port = await freePort();
  const outDir = mkdtempSync(join(tmpdir(), "perfuse-live-"));
  const name = "live-adt";
  const controlId = `MSG${Date.now()}`;

  // Created through the same endpoint the builder posts to, so this exercises the real write path rather than a file dropped on disk
  // behind the server's back.
  const yaml = [
    `name: ${name}`,
    "source:",
    "  type: mllp",
    `  listen: 127.0.0.1:${port}`,
    "destinations:",
    "  - name: archive",
    "    type: file",
    `    dir: ${outDir}`,
    "",
  ].join("\n");

  await page.goto("/");

  const created = await page.request.post("/api/channels", {
    headers: { "X-Perfuse-Request": "1" },
    data: { yaml },
  });
  expect(created.ok(), `creating the channel failed: ${created.status()} ${await created.text()}`).toBe(true);

  const started = await page.request.post(`/api/channels/${name}/start`, {
    headers: { "X-Perfuse-Request": "1" },
  });
  expect(started.ok(), `starting the channel failed: ${started.status()} ${await started.text()}`).toBe(true);

  // The listener binds asynchronously, so connecting immediately can race it.
  let ack = "";
  let lastErr: unknown = null;
  for (let attempt = 0; attempt < 20; attempt++) {
    try {
      ack = await sendMLLPTo(port, admitMessage(controlId));

      break;
    } catch (e) {
      lastErr = e;
      await new Promise((r) => setTimeout(r, 500));
    }
  }
  expect(ack, `never got an acknowledgement. Last error: ${String(lastErr)}`).not.toBe("");

  // AA is an accept. The engine's rule is that delivered, filtered and queued are all AA, and only a partial or outright failure is
  // AE - so anything else here means the message did not get where it was going.
  expect(ack, `the acknowledgement was not an accept:\n${ack}`).toMatch(/\|AA\|/);

  // The acknowledgement must refer to the message it is acknowledging. An ACK carrying the wrong control id is worse than none,
  // because the sender marks the wrong message as delivered.
  expect(ack, "the acknowledgement does not echo the control id").toContain(controlId);

  // Now the console. This is the half that matters for today's work: the engine did the right thing, and the question is whether the
  // interface says so.
  await page.reload();
  await openTab(page, "Messages");

  // Polled rather than read once: the message store is written after the acknowledgement, so a single read can be too early.
  await expect(async () => {
    await page.getByRole("button", { name: "Refresh" }).first().click();
    await expect(page.locator("main")).toContainText(controlId, { timeout: 3000 });
  }).toPass({ timeout: 45_000 });

  // And the dashboard counted it. A console showing the message in one place and a zero in another is the kind of contradiction that
  // makes people stop believing the whole page.
  await openTab(page, "Dashboard");
  await expect(async () => {
    const text = ((await page.locator("main").textContent()) ?? "").replace(/\s+/g, " ");
    // "Messages, 24h" followed by a zero would mean the dashboard disagrees with the message list.
    const m = text.match(/Messages, 24h\s*(\d+)/);
    expect(m, `could not find the message count on the dashboard: ${text.slice(0, 200)}`).not.toBeNull();
    expect(Number(m?.[1] ?? 0), "the dashboard reports no messages although one was delivered").toBeGreaterThan(0);
  }).toPass({ timeout: 30_000 });

  expect(problems, `problems while running real traffic:\n${problems.join("\n")}`).toEqual([]);
});
