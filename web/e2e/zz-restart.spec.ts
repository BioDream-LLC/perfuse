import { test, expect, type Page } from "@playwright/test";
import { openTab } from "./nav";

// Named to run last: a restart stops every channel the earlier specs started from the console (as restarting the service
// would), and the specs after it would find nothing listening.
//
// A setting that applies at a restart can be applied from the console. Before the restart button, somebody had to reach the
// machine to restart the service, which made "everything from the browser" untrue for every start-up setting.
async function toggleAndRestart(page: Page) {
  const main = page.locator("main");
  await main.getByRole("button", { name: "Public health", exact: true }).click();
  const sw = main.getByRole("switch", { name: /Receive Reportability Responses/ });
  const before = await sw.getAttribute("aria-checked");
  await sw.click();
  await main.getByRole("button", { name: "Save changes" }).click();
  await expect(main).toContainText(/will not take effect until this server\s*restarts/, { timeout: 10_000 });

  await main.getByRole("button", { name: "Restart the server" }).click();
  await main.getByRole("button", { name: "Restart now" }).click();
  await expect(main.getByRole("status").filter({ hasText: "The server restarted" })).toBeVisible({ timeout: 60_000 });

  // Back with nothing pending: the restart read the saved value.
  await expect(main).not.toContainText(/will not take effect until this server\s*restarts/, { timeout: 10_000 });
  await main.getByRole("button", { name: "Public health", exact: true }).click();
  await expect(main.getByRole("switch", { name: /Receive Reportability Responses/ })).not.toHaveAttribute(
    "aria-checked",
    before ?? "",
  );
}

test("a setting that needs a restart is applied by restarting from the console", async ({ page }) => {
  test.setTimeout(180_000);
  await page.goto("/");
  await openTab(page, "Settings");
  await toggleAndRestart(page);
  // And back, so the server the rest of the suite uses is as it was.
  await toggleAndRestart(page);
});
