import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Settings: verify the tab loads, shows startup info, and if editable, round-trips a change.

test("the Settings tab shows server configuration and startup info", async ({ page }) => {
  test.setTimeout(60_000);

  await page.goto("/");
  await openTab(page, "Settings");

  const main = page.locator("main");

  // The startup section always appears — it shows how the server was started.
  await expect(main).toContainText("How this server was started", { timeout: 10_000 });

  // Key startup facts that the E2E server always has.
  await expect(main).toContainText(/address|listen/i);
  await expect(main).toContainText(/database/i);

  // If there is an editable settings file, exercise the write path.
  const textarea = main.locator("textarea").first();
  if (await textarea.isVisible({ timeout: 3000 }).catch(() => false)) {
    const disabled = await textarea.isDisabled();
    if (!disabled) {
      // Add a harmless comment and save.
      const original = (await textarea.inputValue()) ?? "";
      const modified = original + "\n# qa-settings-test\n";
      await textarea.fill(modified);

      await expect(main).toContainText("Unsaved changes");

      await main.getByRole("button", { name: "Save", exact: true }).first().click();
      await expect(main).toContainText("Saved", { timeout: 10_000 });

      // Reload the page and confirm it persisted.
      await page.reload();
      await openTab(page, "Settings");
      const reloaded = main.locator("textarea").first();
      await expect(reloaded).toHaveValue(/qa-settings-test/, { timeout: 10_000 });

      // Restore original.
      await reloaded.fill(original);
      await main.getByRole("button", { name: "Save", exact: true }).first().click();
      await expect(main).toContainText("Saved", { timeout: 10_000 });
    }
  }
});
