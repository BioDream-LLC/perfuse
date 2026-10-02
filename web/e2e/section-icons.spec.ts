import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Icons appear on the sections themselves, not only in the navigation.
//
// Seventy-nine icons existed and four files used them, all navigation - so the icons a person saw while choosing where to go
// disappeared the moment they arrived.
//
// Written as one walk rather than a test per view on purpose. Several views show no sections until they have data or a selection, and
// a per-view assertion either fails on those or gets weakened until it proves nothing. Totalling the headings and requiring that none
// of them lacks an icon says the real thing, and the count assertion keeps it honest if the markup changes and the walk stops finding
// anything.
test("every section heading in the interface carries an icon", async ({ page }) => {
  const views = ["Dashboard", "Channels", "Messages", "Metrics", "Queue", "Settings", "Users"];

  let seen = 0;
  const naked: string[] = [];

  for (const view of views) {
    await page.goto("/");

    try {
      await openTab(page, view);
    } catch {
      continue; // A view this build does not present is not this test's business.
    }

    const headings = page.locator("section.card h2");
    const count = await headings.count();

    for (let i = 0; i < count; i += 1) {
      const heading = headings.nth(i);
      const text = ((await heading.textContent()) || "").trim();
      seen += 1;

      // The icon sits beside the heading in the same row, hidden from screen readers because the heading already names the
      // section - announcing both would read every section twice.
      const row = heading.locator("xpath=../..");
      if ((await row.locator('[aria-hidden="true"] svg').count()) === 0) {
        naked.push(`${view}: ${text}`);
      }
    }
  }

  // Nine or so, across the views that show sections without needing data or a selection. That is a smoke test rather than coverage,
  // and deliberately so: the mapping for all fifty-three sections is checked in SectionIcons.test.ts and the rendering in
  // SectionIconRender.test.tsx, both of which are cheaper and see everything. What only a browser can add is that the whole
  // arrangement survives a real render, and for that a handful is enough. The floor guards against the walk silently finding
  // nothing after a markup change.
  expect(seen, "no section headings were found in any view, so this test proves nothing").toBeGreaterThanOrEqual(6);
  expect(naked, `these section headings have no icon:\n${naked.join("\n")}`).toEqual([]);
});
