import { test, expect } from "@playwright/test";
import { openTab } from "./nav";
// The shared helper rather than a local copy, for the reason recorded in flowmap: the two specs keeping their own were the two that
// failed, because a copy does not inherit the waiting the shared one learned to do.
import { sendMLLP as send, waitForListener, freePort } from "./mllp";
import { readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));

function state(): { url: string; dir: string } {
  return JSON.parse(readFileSync(join(here, ".state.json"), "utf8"));
}


// The synthesis must produce a channel that reflects what actually arrived, not what somebody guessed.
//
// Driven end to end: send messages with specific codes, profile them, synthesise, and assert the codes appear in the
// generated YAML with blank targets. A synthesis that makes up what a code should become is more dangerous than one that
// leaves it blank - because somebody accepts it.
test("synthesising a channel from traffic produces mapping stubs with the codes actually sent", async ({ page }) => {
  test.setTimeout(120_000);

  const { dir } = state();
  // Asked of the operating system rather than named. A fixed number cannot be bound if anything already holds it, and a server left
  // behind by an interrupted run keeps its listeners - which fails this test with "bind: address already in use" and sends the reader
  // looking at the flow map.
  const port = await freePort();

  // Write a channel that stores messages (setup already does one called labs).
  const { writeFileSync } = await import("node:fs");
  writeFileSync(
    join(dir, "channels", "synthfeed.yaml"),
    [
      "name: synthfeed",
      "source:", "  type: mllp", `  listen: 127.0.0.1:${port}`,
      "destinations:", "  - name: archive", "    type: file", `    dir: ${join(dir, "synthout")}`,
      "",
    ].join("\n"),
  );

  await page.goto("/");
  await page.evaluate(
    () => fetch("/api/channels/synthfeed/start", { method: "POST", headers: { "X-Perfuse-Request": "1" } }),
  );

  // Waited for rather than slept through. This was waitForTimeout(1500), a guess at how long a channel takes to bind, and the test
  // failed once in a full run at 1.8s with read ECONNRESET - which is what the steps above add up to, so the send happened before the
  // listener was accepting. A fixed sleep is a clock the test does not control.
  await waitForListener(port);

  // At least 20 messages so the profiler will make suggestions.
  for (let i = 0; i < 25; i++) {
    const sex = i % 3 === 0 ? "M" : i % 3 === 1 ? "F" : "U";
    const msg = [
      `MSH|^~\\&|SEND|SITEA|PERFUSE|SITEB|20260824||ADT^A01|SYN${i}|P|2.5.1`,
      `PID|1||MRN${i}^^^SITEA^MR||Doe^Jane||19800101|${sex}`,
      "PV1|1|I",
    ].join("\r");
    await send(msg, port);
  }

  await openTab(page, "Channels");
  await page.waitForTimeout(500);

  // The channel card has a "Feed" button that opens the profile panel.
  // The channel name is in an h2. The card is the .card ancestor.
  const heading = page.getByRole("heading", { name: "synthfeed", level: 2 });
  await expect(heading).toBeVisible({ timeout: 10_000 });
  const card = page.locator(".card").filter({ has: heading });
  await card.getByRole("button", { name: "Feed" }).click();
  await page.getByRole("button", { name: "Profile this feed" }).click();

  // Wait for the profile to appear.
  await expect(page.locator("main")).toContainText("messages read of", { timeout: 30_000 });

  // Synthesise.
  await page.getByRole("button", { name: "Synthesise a channel from this" }).click();
  await expect(page.locator("pre"), "the synthesis did not appear").toBeVisible({ timeout: 30_000 });

  const yaml = (await page.locator("pre").textContent()) ?? "";

  // The codes actually sent appear with blank targets.
  expect(yaml, "M is missing from the mapping").toContain('M: ""');
  expect(yaml, "F is missing from the mapping").toContain('F: ""');
  expect(yaml, "U is missing from the mapping").toContain('U: ""');

  // The filter covers the message type sent.
  expect(yaml, "the filter does not cover ADT^A01").toContain("ADT^A01");

  // It says review.
  expect(yaml, "the file does not warn it needs review").toContain("review every line");

  // There is evidence beside an entry.
  expect(yaml, "entries lack evidence").toMatch(/seen \d+ times/);
});
