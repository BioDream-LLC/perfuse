import { test, expect } from "@playwright/test";
import { openTab } from "./nav";
// The shared helper rather than a local copy. The two specs that kept their own were the two that failed on the flaky roll, because a
// copy does not inherit the waiting the shared one learned to do.
import { sendMLLP as send, waitForListener, freePort } from "./mllp";
import { readFileSync, writeFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));

// The flow map has to show a strand that is dark, and distinguish dark from never used.
//
// Both are drawn as a flat line at zero and they mean completely different things: one destination stopped working this morning, the other
// has never worked since the day somebody configured it. A map that renders them identically answers neither question, and the second one
// is the one nobody finds on their own - there is no traffic anywhere near it to draw the eye.

/** state reads the fixture server details the setup wrote. */
function state(): { url: string; dir: string } {
  return JSON.parse(readFileSync(join(here, ".state.json"), "utf8"));
}

/** send delivers one MLLP-framed message and resolves with the ACK. */

test("the flow map draws every strand and separates dark from never used", async ({ page }) => {
  test.setTimeout(180_000);

  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`uncaught: ${e.message}`));
  page.on("console", (m) => {
    if (m.type() === "error") problems.push(`console: ${m.text()}`);
  });

  const { dir } = state();
  // Asked of the operating system rather than named. A fixed number cannot be bound if anything already holds it, and a server left
  // behind by an interrupted run keeps its listeners - which fails this test with "bind: address already in use" and sends the reader
  // looking at the flow map.
  const port = await freePort();

  // A channel with two destinations that receive everything and one that receives nothing, because its filter never matches. The
  // never-used strand is the point of the test.
  writeFileSync(
    join(dir, "channels", "flowfeed.yaml"),
    [
      "name: flowfeed",
      "description: the feed the flow map test drives",
      "source:",
      "  type: mllp",
      `  listen: 127.0.0.1:${port}`,
      "destinations:",
      "  - name: archive",
      "    type: file",
      `    dir: ${join(dir, "flowout")}`,
      "  - name: pharmacy",
      "    type: file",
      `    dir: ${join(dir, "flowpharm")}`,
      "  - name: research",
      "    type: file",
      `    dir: ${join(dir, "flowresearch")}`,
      // Never matches, so nothing is ever sent here. This is the strand that must not be confused with one that stopped.
      "    filter: MSH-9.2 == 'NEVERMATCHES'",
      "",
    ].join("\n"),
  );

  // Restart the channel so the new file is running, then send real traffic through it.
  await page.goto("/");
  await openTab(page, "Channels");

  const api = async (path: string, body?: unknown) =>
    page.evaluate(
      async ([p, b]) => {
        const res = await fetch(p as string, {
          method: "POST",
          headers: { "Content-Type": "application/json", "X-Perfuse-Request": "1" },
          body: b ? JSON.stringify(b) : undefined,
        });

        return { status: res.status, text: await res.text() };
      },
      [path, body] as const,
    );

  const started = await api("/api/channels/flowfeed/start");
  expect(started.status, `the channel would not start: ${started.text}`).toBeLessThan(300);

  // Started is not listening. This sent immediately after the request returned and failed once in a full run at 233ms - the time it
  // takes to reach this line - because the listener had not bound yet.
  await waitForListener(port);

  const ack = await send(["MSH|^~\\&|SEND|SITEA|PERFUSE|SITEB|20260823140000||ADT^A01|FLOWMAP1|P|2.5.1", "PID|1||MRN9||Doe^Jane"].join("\r"), port);
  expect(ack, "the message was not accepted").toContain("AA");

  // Now read the map.
  await openTab(page, "Flow map");

  await expect(page.getByRole("heading", { name: "Flow map" })).toBeVisible();

  // The channel and all three destinations appear. A destination missing from the map is the failure this is guarding: the strand
  // with no traffic is the easiest one to leave out and the most important one to show.
  const main = page.locator("main");
  await expect(main).toContainText("flowfeed");
  await expect(main).toContainText("archive");
  await expect(main).toContainText("pharmacy");
  await expect(main).toContainText("research");

  // Scoped to this channel's card, not to the page.
  //
  // The first version of this searched the whole page for the phrase and passed - by finding it on a different channel from the
  // fixture, which happened to have a destination nothing had ever been sent to. The strand this test builds for the purpose was
  // reporting something else entirely, and a plant "fired" for the same wrong reason. An assertion that can be satisfied by a part
  // of the page the test did not create is not testing the thing it names.
  const card = main.locator("div", { has: page.getByText("flowfeed", { exact: true }) }).last();

  // research has a filter that never matches, so messages are recorded against it as filtered. That makes it a strand that has been
  // used and has sent nothing on - which is a different statement from never used, and the distinction is the point.
  const research = card.locator("div").filter({ hasText: /^research/ }).first();
  await expect(research, "the research strand does not report that nothing was sent on").toContainText(/filtered/);

  // And a filtered strand must not claim a delivery.
  await expect(research, "a strand that delivered nothing reports deliveries").not.toContainText(/[1-9]\d* delivered/);

  // The scrubber exists and covers more than one point, or there is nothing to drag.
  const scrubber = page.getByLabel("the moment in the window to show");
  await expect(scrubber).toBeVisible();
  const max = Number(await scrubber.getAttribute("max"));
  expect(max, "the window has only one point, so a scrubber cannot show a change over time").toBeGreaterThan(1);

  // Drag back to the start of the window. The traffic just sent is at the end, so the beginning must show it as absent - which is
  // the whole purpose: going back to a moment and seeing what was not running then.
  await scrubber.fill("0");
  await expect(main).toContainText("dark at this moment");

  // Selecting this channel explains it, from the same narration as the specification.
  //
  // Scoped to the flowfeed card. Taking the first "what does this do?" on the page clicked whichever channel sorted first, which in a
  // full suite run is a channel another spec created - so this passed alone and failed in the suite, and would have kept passing alone
  // forever. Two positional selectors in one session; both times the fix was to name the thing.
  await card.getByRole("button", { name: /what does this do/ }).click();
  await expect(main).toContainText("the feed the flow map test drives");
  await expect(main, "the narration does not describe the destinations").toContainText("It sends to 3 destinations");

  expect(problems, `the flow map logged errors: ${problems.join("; ")}`).toEqual([]);
});
