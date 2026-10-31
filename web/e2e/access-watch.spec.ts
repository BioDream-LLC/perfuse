import { test, expect, type Page } from "@playwright/test";

import { openTab } from "./nav";
import { anADT, sendMLLP } from "./mllp";

// Watching access to patient data, end to end: a person opens many patients' messages in a minute, the monitor sees it in
// the audit trail, the patient-data rule turns it into an alert, and the Alerts screen shows both the alert and what the
// monitor is holding.
//
// The rule is added through the rules editor and taken out again, because one server serves the whole suite and the
// alert-rules spec counts the rules it was written against.

async function openRules(page: Page) {
  await openTab(page, "Alerts");
  await expect(page.getByRole("heading", { name: "Alert rules" })).toBeVisible({ timeout: 20_000 });
  await expect(page.locator('select[id$="-kind"]').first()).toBeVisible({ timeout: 20_000 });
}

test("one person reading many patients' records quickly raises an alert", async ({ page }) => {
  test.setTimeout(180_000);

  // Twelve different messages, so there are twelve different records to read.
  for (let i = 0; i < 12; i++) await sendMLLP(anADT(`WATCH${String(i).padStart(4, "0")}`));
  await expect
    .poll(async () => (await (await page.request.get("/api/messages?limit=12")).json()).messages?.length ?? 0, { timeout: 20_000 })
    .toBe(12);

  await page.goto("/");
  await openRules(page);
  const before = await page.locator('select[id$="-kind"]').count();
  await page.getByRole("button", { name: "+ Add rule" }).click();
  const kind = page.locator('select[id$="-kind"]').nth(before);
  await kind.selectOption("phi-bulk-reads");
  // The kind's own default, and the unit said in words, from the catalogue the engine serves.
  await expect(page.locator(`#alert-rule-${before}-threshold`)).toHaveValue("10");
  await expect(page.getByText("different records in a minute").first()).toBeVisible();
  await page.getByRole("button", { name: "Save rules" }).click();
  await expect(page.getByRole("status")).toContainText(`Saved ${before + 1} rules`, { timeout: 20_000 });

  try {
    // Read as a person reads them: each one opened, each one audited.
    const list = (await (await page.request.get("/api/messages?limit=12")).json()).messages as { id: number }[];
    for (const m of list) expect((await page.request.get(`/api/messages/${m.id}`)).ok()).toBeTruthy();

    // What the monitor holds, on the screen, straight away.
    await page.reload();
    await openTab(page, "Alerts");
    const panel = page.getByTestId("access-watch");
    await expect(panel.getByRole("heading", { name: "Watching access to patient data" })).toBeVisible();
    await expect(panel.getByText(/admin read \d+ different records in a minute/)).toBeVisible({ timeout: 20_000 });

    // And as an alert, at the watcher's next reading (every thirty seconds), shown beside what the monitor holds.
    const summary = /admin read \d+ different records in a minute/;
    await expect
      .poll(async () => {
        const body = await (await page.request.get("/api/alerts")).json();
        return (body.firing as { summary: string }[]).some((a) => summary.test(a.summary));
      }, { timeout: 90_000, intervals: [5_000] })
      .toBe(true);
    await page.reload();
    await openTab(page, "Alerts");
    // The banner, the alert's card and the monitor's own panel each say it.
    await expect.poll(() => page.getByText(summary).count(), { timeout: 20_000 }).toBeGreaterThanOrEqual(3);
    // And the rule is listed by name among what is being watched.
    await page.getByText(/^What is being watched/).click();
    await expect(page.getByText("Somebody reading many patients' records quickly").first()).toBeVisible();
  } finally {
    await openRules(page);
    await page.getByRole("button", { name: `Delete rule ${before + 1}` }).click();
    await page.getByRole("button", { name: "Save rules" }).click();
    await expect(page.getByRole("status")).toContainText(`Saved ${before} rules`, { timeout: 20_000 });
  }
});
