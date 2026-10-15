import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// CMS Aligned Network readiness: the 26 criteria, each read from what this test server has on.

test("the readiness screen lists the 26 criteria and reads them from what is on", async ({ page }) => {
  const faults: string[] = [];
  page.on("pageerror", (e) => faults.push(e.message));
  await page.goto("/");
  await openTab(page, "CMS Aligned Network");
  const screen = page.getByTestId("aligned");
  await expect(screen.locator("li[data-testid^=aligned-]")).toHaveCount(26);
  // The harness serves FHIR with an XDS registry, so the record locator is on; quality gap queries are not built.
  await expect(page.getByTestId("aligned-16")).toContainText("in effect");
  await expect(page.getByTestId("aligned-8")).toContainText("Not built");
  await expect(page.getByTestId("aligned-26")).toContainText("Yours to do");
  // Filtering by a status keeps only those.
  await page.getByRole("button", { name: /^Not built: \d+$/ }).click();
  const shown = await screen.locator("li[data-testid^=aligned-]").count();
  expect(shown).toBeGreaterThan(0);
  expect(shown).toBeLessThan(26);
  await expect(page.getByTestId("aligned-26")).toHaveCount(0);
  await page.getByRole("button", { name: /^All 26$/ }).click();
  await expect(screen.locator("li[data-testid^=aligned-]")).toHaveCount(26);
  expect(faults, faults.join("\n")).toEqual([]);
});
