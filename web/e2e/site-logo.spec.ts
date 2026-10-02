import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// The operator's logo appears on the screens where it is a claim of ownership.
//
// Before this, an uploaded logo appeared in exactly one place: the header. A site that had white-labelled the product still met our
// headings everywhere they looked. It is now on the dashboard, the metrics view and the shadow report - and nowhere when nobody has
// uploaded one, which is the half that would be easy to get wrong and hard to notice.

const BRANDED = ["Dashboard", "Metrics", "Shadow"];

test("no logo is shown on an installation that has not uploaded one", async ({ page }) => {
  await page.goto("/");

  for (const tab of BRANDED) {
    await openTab(page, tab);

    // The header's own mark is excluded: that is BrandMark, which always draws something because a header needs a mark.
    const inBody = page.locator("main img[src*='/api/branding/logo']");
    await expect(inBody, `${tab} shows a logo when none was uploaded`).toHaveCount(0);
  }
});

test("an uploaded logo appears on the dashboard and the reporting views", async ({ page }) => {
  await page.goto("/");

  // A tiny valid PNG, uploaded the way a person would.
  const png = Buffer.from(
    "iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAYAAADED76LAAAAFUlEQVR42mP8z8Dwn4GBgYGJgYEBAAsGAQGmA5PAAAAAAElFTkSuQmCC",
    "base64",
  );

  await openTab(page, "Settings");
  const chooser = page.locator("input[type=file]").first();
  await chooser.setInputFiles({ name: "logo.png", mimeType: "image/png", buffer: png });

  // The upload is live everywhere without a reload, which is the claim the branding panel makes.
  await expect(page.locator("img[src*='/api/branding/logo']").first()).toBeVisible({ timeout: 15_000 });

  for (const tab of BRANDED) {
    await openTab(page, tab);
    await expect(
      page.locator("main img[src*='/api/branding/logo']").first(),
      `${tab} does not show the uploaded logo`,
    ).toBeVisible({ timeout: 10_000 });
  }

  // Put it back. One server serves the whole suite, so a logo left uploaded here would silently rebrand every spec that runs
  // afterwards - and the first test in this very file asserts that an unbranded installation shows nothing, which would then
  // depend on file ordering. Leaving state behind is how a suite starts passing or failing according to what ran before it.
  await openTab(page, "Settings");
  await page.getByRole("button", { name: "Remove", exact: true }).click();
  await expect(page.locator("main img[src*='/api/branding/logo']")).toHaveCount(0, { timeout: 15_000 });
});
