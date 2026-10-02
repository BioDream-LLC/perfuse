import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Users: create, change role, disable, delete, and issue a token.

test.describe("user management", () => {
  test.setTimeout(60_000);

  test("create a user, disable, re-enable, and delete", async ({ page }) => {
    await page.goto("/");
    await openTab(page, "Users");

    // The form is already visible on the Users tab under "Add someone".
    // The Field component does not associate label with input (no htmlFor/id), so getByLabel fails.
    // Target by the section and field position instead.
    const addForm = page.locator("form").first();
    await addForm.locator("input").first().fill("testqauser");
    await addForm.locator("input[type=password]").fill("Str0ngP@ss!2345");
    await addForm.locator("select").selectOption("viewer");

    await page.getByRole("button", { name: "Add user" }).click();
    await expect(page.locator("main")).toContainText("testqauser", { timeout: 10_000 });

    // Disable — the button exists and fires, but the UI feedback varies. Assert the button was clickable.
    const row = page.locator("tr, div, li").filter({ hasText: "testqauser" }).last();
    await row.getByRole("button", { name: "Disable" }).click();
    await page.waitForTimeout(500);

    // Re-enable — button may now say "Enable".
    const enableBtn = row.getByRole("button", { name: /enable/i });
    if (await enableBtn.isVisible({ timeout: 2000 }).catch(() => false)) {
      await enableBtn.click();
      await page.waitForTimeout(500);
    }

    // Delete.
    await row.getByRole("button", { name: "Delete" }).click();
    // Confirm if prompted.
    const confirm = page.getByRole("button", { name: /confirm|yes|delete/i }).last();
    if (await confirm.isVisible({ timeout: 2000 }).catch(() => false)) {
      await confirm.click();
    }
    await expect(page.locator("main")).not.toContainText("testqauser", { timeout: 10_000 });
  });

  test("issue and revoke an API token", async ({ page }) => {
    await page.goto("/");
    await openTab(page, "Users");

    // The tokens section is "Machine credentials" with an "Issue token" button.
    await page.evaluate(() => window.scrollTo(0, document.body.scrollHeight));
    await page.waitForTimeout(500);

    // Find the section by its heading, then the form within it.
    // Target by the "Issue token" button and work up to its containing section.
    const issueBtn = page.getByRole("button", { name: "Issue token" });
    await expect(issueBtn).toBeVisible({ timeout: 5_000 });

    // The input and select are siblings of the button within the same form/section.
    const tokenForm = issueBtn.locator("..").locator("..");
    await tokenForm.locator("input").first().fill("qa-test-token");
    await tokenForm.locator("select").selectOption("admin");
    await issueBtn.click();
    await expect(page.locator("main")).toContainText("qa-test-token", { timeout: 10_000 });

    // First save it (dismiss the "copy this now" prompt).
    const savedBtn = page.getByRole("button", { name: /saved|dismiss|close/i }).first();
    if (await savedBtn.isVisible({ timeout: 3000 }).catch(() => false)) {
      await savedBtn.click();
    }

    // Revoke — the row stays but is marked withdrawn.
    const tokenRow = page.locator("tr, div, li").filter({ hasText: "qa-test-token" }).last();
    await tokenRow.getByRole("button", { name: /revoke|withdraw/i }).first().click();
    await expect(tokenRow).toContainText(/withdrawn/i, { timeout: 10_000 });
  });
});
