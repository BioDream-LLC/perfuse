import { test, expect } from "@playwright/test";

// Choosing a theme, and being allowed to keep it.
//
// The contrast sweep proves each theme is legible on every tab. This proves the switch works and that a choice survives, which is a
// different claim and the one a person actually experiences.

test("a theme can be chosen and survives a reload", async ({ page }) => {
  await page.goto("/");

  await page.getByLabel("Theme").selectOption("light");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");

  // The page background really changes. Asserting the attribute alone would pass if the CSS were never wired to it, which is the
  // most likely way for this to be broken and invisible.
  const light = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);

  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");

  await page.getByLabel("Theme").selectOption("midnight");
  const midnight = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  expect(midnight, "the two themes paint the same background, so the palette is not wired up").not.toBe(light);
});

test("every theme is offered", async ({ page }) => {
  await page.goto("/");

  const options = await page.getByLabel("Theme").locator("option").allTextContents();
  expect(options).toEqual(["Midnight", "Dark", "Light"]);
});
