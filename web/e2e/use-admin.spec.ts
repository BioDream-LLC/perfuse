import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Using the administrative sections: accounts, tokens, activity and settings.
//
// These are the ones with consequences. Creating a user, changing a role, issuing a token and revoking
// it all change who can do what, and none of it had been driven through the interface. A test that only
// loads the tab proves the table renders.

function watch(page: import("@playwright/test").Page): string[] {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`page error: ${e}`));
  page.on("console", (m) => {
    if (m.type() === "error") problems.push(`console: ${m.text()}`);
  });
  return problems;
}

function uniqueName(prefix: string): string {
  return `${prefix}${Date.now().toString(36)}${Math.floor(Math.random() * 1e4)}`;
}

test("a user can be created, have their role changed, be disabled and deleted", async ({ page }) => {
  const problems = watch(page);
  const username = uniqueName("e2euser");

  await page.goto("/");
  await openTab(page, "Users");

  await page.getByLabel("Username", { exact: true }).fill(username);
  await page.getByLabel("Password", { exact: true }).fill("a-long-enough-password-1234");
  await page.getByLabel("Role", { exact: true }).first().selectOption({ label: "Viewer" });
  await page.getByRole("button", { name: "Add user" }).click();

  const row = page.locator("tbody tr").filter({ hasText: username });
  await expect(row, "the new user is not listed").toBeVisible({ timeout: 20_000 });

  // The role selector on the row applies immediately, which is worth checking because it is the one
  // control here with no save button to confirm it worked.
  await row.locator("select").selectOption("editor");
  await page.reload();
  await openTab(page, "Users");
  const again = page.locator("tbody tr").filter({ hasText: username });
  await expect(again.locator("select"), "the role change did not survive a reload").toHaveValue(
    "editor",
    { timeout: 20_000 },
  );

  // Disable, then enable, so the button's two states are both exercised.
  await again.getByRole("button", { name: "Disable" }).click();
  await expect(
    page.locator("tbody tr").filter({ hasText: username }).getByRole("button", { name: "Enable" }),
  ).toBeVisible({ timeout: 20_000 });
  await page.locator("tbody tr").filter({ hasText: username }).getByRole("button", { name: "Enable" }).click();
  await expect(
    page.locator("tbody tr").filter({ hasText: username }).getByRole("button", { name: "Disable" }),
  ).toBeVisible({ timeout: 20_000 });

  // Delete, through the confirmation.
  await page.locator("tbody tr").filter({ hasText: username }).getByRole("button", { name: "Delete" }).click();
  await expect(page.getByText(`Delete ${username}?`)).toBeVisible({ timeout: 10_000 });
  await page.getByRole("button", { name: "Delete user" }).click();
  await expect(page.locator("tbody")).not.toContainText(username, { timeout: 20_000 });

  expect(problems, problems.join("\n")).toEqual([]);
});

test("a short password is refused with a reason", async ({ page }) => {
  const problems = watch(page);

  await page.goto("/");
  await openTab(page, "Users");

  await page.getByLabel("Username", { exact: true }).fill(uniqueName("e2eshort"));
  await page.getByLabel("Password", { exact: true }).fill("short");
  await page.getByRole("button", { name: "Add user" }).click();

  // The hint says at least twelve characters, so refusing is correct. The property worth checking is
  // that it says so rather than failing quietly.
  await expect(page.locator("main"), "a five-character password was accepted or refused silently").toContainText(
    /12 characters|too short|at least/i,
    { timeout: 15_000 },
  );

  expect(problems.filter((p) => !/40[03]|Failed to load resource/.test(p))).toEqual([]);
});

test("an API token can be issued, shown once, and revoked", async ({ page }) => {
  const problems = watch(page);
  const label = uniqueName("e2e-token-");

  await page.goto("/");
  await openTab(page, "Users");

  await page.getByLabel("What is it for", { exact: true }).fill(label);
  await page.getByLabel("Role", { exact: true }).last().selectOption({ label: "viewer — read only" });
  await page.getByRole("button", { name: "Issue token" }).click();

  // The token itself is shown once. If that banner never appears the feature is unusable, because there
  // is no second chance to read it.
  await expect(page.locator("main"), "the issued token was never shown").toContainText(
    `Token issued for ${label}`,
    { timeout: 20_000 },
  );

  await page.getByRole("button", { name: "I have saved it" }).click();

  const row = page.locator("tbody tr").filter({ hasText: label });
  await expect(row).toBeVisible({ timeout: 15_000 });

  await row.getByRole("button", { name: "Revoke" }).click();
  await expect(
    page.locator("tbody tr").filter({ hasText: label }),
    "a revoked token is not marked as withdrawn",
  ).toContainText(/withdrawn/i, { timeout: 20_000 });

  expect(problems, problems.join("\n")).toEqual([]);
});

test("the activity log records what was just done", async ({ page }) => {
  const problems = watch(page);
  const username = uniqueName("e2eaudit");

  // Do something auditable first, so this is a test of the log rather than of the empty state.
  await page.goto("/");
  await openTab(page, "Users");
  await page.getByLabel("Username", { exact: true }).fill(username);
  await page.getByLabel("Password", { exact: true }).fill("a-long-enough-password-1234");
  await page.getByRole("button", { name: "Add user" }).click();
  await expect(page.locator("tbody tr").filter({ hasText: username })).toBeVisible({ timeout: 20_000 });

  await openTab(page, "Activity");
  await page.getByRole("button", { name: "Refresh" }).click();

  await expect(
    page.locator("main"),
    "creating a user was not recorded in the activity log",
  ).toContainText(username, { timeout: 20_000 });

  // Newest first. The entry just made must be near the top, not buried.
  //
  // Scoped to the table with the audit columns rather than the first tbody on the page. Activity now also carries the friction report,
  // which has a table of its own above this one, and an unscoped "first row" silently became a row of that instead - the assertion
  // then failed saying the newest entry was not first, which sends the next reader to the audit ordering code.
  const auditTable = page.locator("table").filter({ has: page.getByRole("columnheader", { name: "Who" }) });
  const firstRow = auditTable.locator("tbody tr").first();
  await expect(firstRow, "the newest entry is not first").toContainText(/user|admin/i, {
    timeout: 15_000,
  });

  // Clean up.
  await openTab(page, "Users");
  await page.locator("tbody tr").filter({ hasText: username }).getByRole("button", { name: "Delete" }).click();
  await page.getByRole("button", { name: "Delete user" }).click();

  expect(problems, problems.join("\n")).toEqual([]);
});

test("a setting can be changed, saved, and comes back after a reload", async ({ page }) => {
  const problems = watch(page);

  await page.goto("/");
  await openTab(page, "Settings");

  // Search for a setting rather than hunting through groups, which is what the search is for.
  await page.getByLabel("Search all settings", { exact: true }).fill("tagline");

  const tagline = page.getByLabel(/tagline/i).first();
  await expect(tagline, "the tagline setting was not found by search").toBeVisible({ timeout: 20_000 });

  const value = `Integration for ${Date.now().toString(36)}`;
  await tagline.fill(value);

  // The save bar only appears once something is dirty, which is itself worth asserting.
  const save = page.getByRole("button", { name: "Save changes" });
  await expect(save, "no save bar appeared after editing a setting").toBeVisible({ timeout: 10_000 });
  await save.click();

  await expect(page.locator("main")).toContainText(/Saved 1 setting|Nothing changed/, {
    timeout: 20_000,
  });

  // The point of saving is that it persists.
  await page.reload();
  await openTab(page, "Settings");
  await page.getByLabel("Search all settings", { exact: true }).fill("tagline");
  await expect(page.getByLabel(/tagline/i).first(), "the saved value did not survive a reload").toHaveValue(
    value,
    { timeout: 20_000 },
  );

  // Put it back, so the sign-in page is not left branded for later tests.
  await page.getByLabel(/tagline/i).first().fill("");
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.locator("main")).toContainText(/Saved|Nothing changed/, { timeout: 20_000 });

  expect(problems, problems.join("\n")).toEqual([]);
});

test("discarding an edit leaves the setting alone", async ({ page }) => {
  const problems = watch(page);

  await page.goto("/");
  await openTab(page, "Settings");
  await page.getByLabel("Search all settings", { exact: true }).fill("tagline");

  const tagline = page.getByLabel(/tagline/i).first();
  await expect(tagline).toBeVisible({ timeout: 20_000 });
  const before = await tagline.inputValue();

  await tagline.fill("this should not be kept");
  await page.getByRole("button", { name: "Discard" }).click();

  await expect(tagline, "Discard did not restore the previous value").toHaveValue(before, {
    timeout: 15_000,
  });

  expect(problems, problems.join("\n")).toEqual([]);
});

test("the advanced settings toggle reveals more than the default view", async ({ page }) => {
  const problems = watch(page);

  await page.goto("/");
  await openTab(page, "Settings");

  const countControls = async () =>
    page.locator("main input, main select, main textarea").count();

  const before = await countControls();
  await page.getByLabel("Show advanced settings", { exact: true }).check();
  await page.waitForTimeout(500);
  const after = await countControls();

  expect(
    after,
    "showing advanced settings revealed nothing, so the checkbox does nothing",
  ).toBeGreaterThan(before);

  await page.getByLabel("Show advanced settings", { exact: true }).uncheck();
  expect(problems, problems.join("\n")).toEqual([]);
});
