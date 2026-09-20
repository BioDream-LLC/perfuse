import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// No tab may render a JavaScript artefact as text.
//
// The dashboard's first tile read "undefined/undefined" - the most looked-at number in the product. The REST status endpoint carried
// channelsTotal and channelRunning and the server-sent-event payload did not, and the dashboard reads both into one variable, so the
// tile was right for two seconds and wrong for as long as the page stayed open afterwards.
//
// Nothing caught it. The tab sweep asks whether a panel rendered, and it did. The null guards check that a JSON array is never null,
// and these were absent fields rather than null ones. The interface cast the event payload to the shared type on arrival, so
// TypeScript was satisfied. Only looking at the screen found it.
//
// "undefined" reaching a user is always a defect: either a field was renamed, or a payload is missing something, or an optional value
// needed a fallback. There is no case where it is the intended output.

const TABS = [
  "Dashboard", "Channels", "Messages", "Queue", "Alerts", "Metrics", "FHIR lab", "Documents",
  "Scripts", "Migrate", "Contracts", "Tables", "Fleet", "Flow map", "Playground", "Shadow", "Certificates",
  "Users", "Activity", "Settings",
];

// Artefacts of a value that was not what the code assumed. Deliberately not including "null", which appears legitimately in
// documentation text on several panels, or "Infinity", which a rate can honestly be.
//
// Matched as substrings, without word boundaries, and that detail is the whole test.
//
// The first version used /\bundefined\b/ and passed against a dashboard that was, at that moment, displaying the word. textContent
// concatenates adjacent elements with no separator, so the tile reads "0/undefinedall running" - and \b after the "d" cannot match
// when the next character is a letter. The check was unable to fail, and I only found out by planting the original bug and demanding
// the failure.
const ARTEFACTS = [/undefined/, /NaN/, /\[object Object\]/];

test("no tab renders undefined, NaN or [object Object]", async ({ page }) => {
  test.setTimeout(180_000);

  await page.goto("/");

  const failures: string[] = [];

  for (const tab of TABS) {
    await openTab(page, tab);

    // Long enough to pass the first live update. The original bug was correct on first paint and wrong two seconds later, so
    // reading immediately after the click would have missed it - which is exactly what the earlier sweep did.
    await page.waitForTimeout(3000);

    const text = ((await page.locator("main").textContent()) ?? "").replace(/\s+/g, " ");

    for (const pattern of ARTEFACTS) {
      const m = text.match(pattern);
      if (!m) continue;

      // A little context, so the report says where to look.
      const at = text.indexOf(m[0]);
      const context = text.slice(Math.max(0, at - 60), at + 60);
      failures.push(`${tab}: ${JSON.stringify(m[0])} in "...${context}..."`);
    }
  }

  expect(
    failures,
    `these tabs render a JavaScript artefact where a value should be:\n  ${failures.join("\n  ")}`,
  ).toEqual([]);
});
