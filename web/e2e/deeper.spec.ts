import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Drive deeper GUI surfaces that have never been exercised.
//
// These are not feature tests — they are "does it crash" tests. A panel that throws during render
// blanks the whole application with no message, which is how every nil-slice bug was invisible for
// months. This catches that class of failure on surfaces the tab sweep does not reach.

test.describe("deeper GUI paths", () => {
  test.setTimeout(120_000);

  test("command palette opens and closes with Escape", async ({ page }) => {
    await page.goto("/");
    await page.waitForTimeout(1000);

    // ⌘K or Ctrl+K opens the palette.
    await page.keyboard.press("Meta+k");
    await page.waitForTimeout(500);

    // Look for the palette — it should be a dialog or a prominent input.
    const palette = page.getByRole("dialog").or(page.locator("[data-palette]")).or(page.getByPlaceholder(/search|command|navigate/i));
    if (await palette.isVisible({ timeout: 3000 }).catch(() => false)) {
      await page.keyboard.press("Escape");
      await page.waitForTimeout(300);
    }
    // No crash is the assertion.
    await expect(page.locator("main")).toBeVisible();
  });

  test("channel history panel opens without crashing", async ({ page }) => {
    await page.goto("/");
    await openTab(page, "Channels");
    await page.waitForTimeout(1000);

    const historyBtn = page.getByRole("button", { name: "History" }).first();
    if (await historyBtn.isVisible({ timeout: 3000 }).catch(() => false)) {
      await historyBtn.click();
      await page.waitForTimeout(1000);
      await expect(page.locator("main")).toBeVisible();
    }
  });

  test("shadow tab opens without crashing", async ({ page }) => {
    await page.goto("/");
    await openTab(page, "Shadow");
    await page.waitForTimeout(1000);
    await expect(page.locator("main")).toBeVisible();
    await expect(page.locator("main")).not.toContainText("undefined");
  });

  test("contracts tab opens and renders", async ({ page }) => {
    await page.goto("/");
    await openTab(page, "Contracts");
    await page.waitForTimeout(1000);
    await expect(page.locator("main")).toBeVisible();
    await expect(page.locator("main")).not.toContainText("undefined");
  });

  test("alerts tab opens and shows no crashes", async ({ page }) => {
    await page.goto("/");
    await openTab(page, "Alerts");
    await page.waitForTimeout(1000);
    await expect(page.locator("main")).toBeVisible();

    // If there's an acknowledge button, click it.
    const ackBtn = page.getByRole("button", { name: /acknowledge|dismiss|clear/i }).first();
    if (await ackBtn.isVisible({ timeout: 2000 }).catch(() => false)) {
      await ackBtn.click();
      await page.waitForTimeout(500);
    }
    await expect(page.locator("main")).toBeVisible();
  });

  test("tables tab allows codeset editing without crash", async ({ page }) => {
    await page.goto("/");
    await openTab(page, "Tables");
    await page.waitForTimeout(1000);
    await expect(page.locator("main")).toBeVisible();

    // If there's an edit or view button on a table, try it.
    const editBtn = page.getByRole("button", { name: /edit|view|open/i }).first();
    if (await editBtn.isVisible({ timeout: 2000 }).catch(() => false)) {
      await editBtn.click();
      await page.waitForTimeout(1000);
      await expect(page.locator("main")).toBeVisible();
    }
  });
});
