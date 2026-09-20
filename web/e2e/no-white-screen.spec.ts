import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Every view renders something, and nothing throws while it does.
//
// # Why this exists
//
// The settings screen crashed to a blank page for anybody who had not explicitly set a setting - which is every fresh installation.
// A nil Go slice reached the browser as null, the component called .includes on it, and React unmounted the tree.
//
// Twenty-six browser specs and a contrast sweep across twenty tabs did not notice, and the reason is worth writing down: the sweep
// opens every tab and measures the contrast of the text it finds. A blank page has no text, so it has no bad contrast, so it passes.
// Every check we had was about the quality of what rendered and none of them asked whether anything rendered at all.
//
// # What it asserts
//
// That each view produces a reasonable amount of text, and that the page reports no uncaught error while doing so. Both halves are
// needed: an error boundary could show a tidy apology and satisfy the first, and a component can render while logging an error that
// means half its content is missing.

const VIEWS = [
  "Dashboard", "Channels", "Messages", "Queue", "Alerts", "Metrics", "FHIR lab", "Documents",
  "Scripts", "Migrate", "Contracts", "Tables", "Fleet", "Flow map", "Playground", "Shadow",
  "Certificates", "Users", "Activity", "Settings",
];

test("no view crashes to a blank page", async ({ page }) => {
  test.setTimeout(180_000);

  const problems: string[] = [];

  page.on("pageerror", (e) => problems.push(`uncaught: ${e.message}`));
  page.on("console", (m) => {
    if (m.type() !== "error") return;

    // A failed request is the network being unavailable in a test harness, not a rendering fault. Anything else is a real complaint
    // from the application about its own state.
    const text = m.text();
    if (text.includes("Failed to load resource")) return;

    problems.push(`console: ${text}`);
  });

  await page.goto("/");

  for (const view of VIEWS) {
    problems.length = 0;

    await openTab(page, view);
    await page.waitForTimeout(700);

    const text = (await page.locator("main").innerText()).trim();

    // A crashed React tree leaves the shell and an empty main. The threshold is low on purpose: this is looking for nothing at all,
    // not judging how much a view should say.
    expect(text.length, `${view} rendered almost nothing - ${text.length} characters. This is what a crashed view looks like`).toBeGreaterThan(40);

    expect(problems, `${view} reported errors while rendering:\n${problems.join("\n")}`).toEqual([]);
  }
});
