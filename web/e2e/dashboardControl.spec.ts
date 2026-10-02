import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Starting and stopping a channel from the dashboard.
//
// The control sweep deliberately leaves these alone: pressing them mid-run stops the traffic every later
// section reads, and the failure then surfaces several sections away from its cause. So they are covered
// here instead, where the state can be put back afterwards.
//
// Worth its own test because the dashboard is where somebody goes when something is wrong, and stopping a
// channel is the first thing they will try. It is also the control most likely to be reached in a hurry, by
// somebody who has been woken up.

// The buttons name their channel, so no container filtering is needed.
//
// They did not before this test was written: five channels produced five buttons all announced as "Stop" or
// "Start" with nothing to distinguish them. Needing to address one unambiguously is what exposed it, and
// naming them is the fix a screen reader user needed anyway.

test("a channel can be stopped and started again from the dashboard", async ({ page }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`uncaught exception: ${e.message}`));
  page.on("response", (r) => {
    if (r.url().includes("/api/") && r.status() >= 500) {
      problems.push(`${r.status()} from ${new URL(r.url()).pathname}`);
    }
  });

  await page.goto("/");
  await openTab(page, "Dashboard");

  // The fixture channel that is running. Named explicitly rather than taken by position: a row order that
  // changes would otherwise silently move this test onto a different channel.
  const stop = page.getByRole("button", { name: "Stop labs", exact: true });
  await expect(stop, "a running channel offers no way to stop it from the dashboard").toBeVisible({
    timeout: 20_000,
  });

  await stop.click();

  // Stopping asks first, which is right: it stops a sending system being able to deliver. The dialog is part
  // of using this control, so the test goes through it rather than around it.
  const dialog = page.locator('[role="dialog"]');
  await expect(dialog, "stopping a channel did not ask for confirmation").toBeVisible({ timeout: 10_000 });
  await expect(dialog, "the confirmation does not name the channel being stopped").toContainText("labs");
  await dialog.getByRole("button", { name: /^Stop channel$/ }).click();

  // The outcome that can only follow the click: the same row now offers to start it. Asserting on the word
  // "stopped" would risk matching text that was already there.
  const start = page.getByRole("button", { name: "Start labs", exact: true });
  await expect(start, "after stopping, the row does not offer to start the channel again").toBeVisible({
    timeout: 30_000,
  });

  // Put it back, because everything else in the suite reads this channel's traffic. A test that leaves the
  // fixture worse than it found it turns into a dozen unrelated failures.
  await start.click();
  await expect(
    page.getByRole("button", { name: "Stop labs", exact: true }),
    "the channel could not be started again, so the fixture is left stopped for every later test",
  ).toBeVisible({ timeout: 30_000 });

  expect(problems, problems.join("\n")).toEqual([]);
});

test("the dashboard time ranges each redraw without breaking", async ({ page }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`uncaught exception: ${e.message}`));

  await page.goto("/");
  await openTab(page, "Dashboard");
  await page.waitForTimeout(1_500);

  // Each range refetches and redraws. A chart that divides by a zero-length window or indexes an empty
  // series breaks here and nowhere else, because the shortest range is the one most likely to be empty.
  for (const range of ["1h", "6h", "24h"]) {
    const button = page.getByRole("button", { name: new RegExp(`^${range}$`) });
    await expect(button, `the dashboard has no ${range} range`).toBeVisible({ timeout: 15_000 });
    await button.click();
    await page.waitForTimeout(700);
    await expect(page.locator("main"), `the dashboard is empty after choosing ${range}`).not.toBeEmpty();
  }

  expect(problems, problems.join("\n")).toEqual([]);
});
