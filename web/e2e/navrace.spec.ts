import { test, expect, type Page } from "@playwright/test";
import { openTab, groupButtons, inlineTabs } from "./nav";

// A reproduction of the shared cause behind the flaky-spec family, and a check that the fix closes it.
//
// Five specs had each failed once in a full run with none reproducing alone. The shape said one shared cause appearing in whichever
// test happened to be running, and the thing 61 of the 64 specs share is openTab.
//
// What was eliminated first, so this is not the only candidate left by assumption: a watchdog polled the server and the shared session
// every 100ms for a whole 19-minute run and found no fault in 11,000 samples - the API answered in 1.5ms and the page in 0.3ms
// throughout, so neither availability nor the session is the cause. Machine contention and accumulated server state had been ruled out
// earlier.
//
// What remains is the browser, and openTab counted twice without waiting. Locator.count() answers immediately with whatever is in the
// DOM at that instant. After clicking a group trigger the menu opens on a React state change, so counting its items in the next
// statement can observe an empty menu, press Escape, move to the next group, run out of groups, and report that the view is in no
// group at all.
//
// That failure mode is instant rather than slow, which matches the timings: the two most recent failures took 233ms and 1.8s against
// normal runtimes of 913ms and 3.2s for the same tests. Those were read as too fast to be a timeout, and they were - nothing was
// waiting.
//
// The reproduction throttles the CPU so the render after the click takes longer than the statement after it.

/** racyOpenTab is openTab as it was before the wait was added, kept here so the fix can be shown to change something. */
async function racyOpenTab(page: Page, label: string): Promise<void> {
  await expect(page.locator("nav[role=tablist]")).toBeVisible({ timeout: 30_000 });

  const inline = inlineTabs(page).filter({ hasText: new RegExp(`^${label}$`) });
  if (await inline.count()) {
    await inline.first().click();
    await expect(page.locator("[role=tab][aria-selected=true]")).toHaveText(label);
    return;
  }

  const groups = groupButtons(page);
  const count = await groups.count();
  expect(count, `"${label}" is not in the bar and there are no group menus to look in`).toBeGreaterThan(0);

  const tried: string[] = [];
  for (let i = 0; i < count; i++) {
    const trigger = groups.nth(i);
    tried.push(((await trigger.getAttribute("aria-label")) ?? "?").replace(/ views.*/, ""));

    await trigger.click();

    const exact = page.locator("[role=menu] [role=menuitem]").filter({
      hasText: new RegExp(`^${label}(\\n|$)`),
    });

    if ((await exact.count()) > 0) {
      await exact.first().click();
      await expect(page.locator("[role=tab][aria-selected=true]")).toHaveText(label);
      return;
    }

    await page.keyboard.press("Escape");
  }

  throw new Error(`"${label}" is in no group. Looked in: ${tried.join(", ")}`);
}

/** throttle slows the renderer so a state change takes longer than the statement that follows the click. */
async function throttle(page: Page, rate: number): Promise<void> {
  const session = await page.context().newCDPSession(page);
  await session.send("Emulation.setCPUThrottlingRate", { rate });
}

// A view that lives in a group menu rather than in the bar, since the race is in the menu path.
const GROUPED_VIEW = "Tables";

test("the pre-fix helper loses the race against a slow render", async ({ page }) => {
  test.setTimeout(120_000);

  await page.goto("/");
  await throttle(page, 40);

  // Several attempts, because a race does not have to lose every time to be the cause of a once-per-run failure. The assertion is
  // that it loses at least once - if the old code never failed here, the reproduction would be worthless and this test would say so
  // by failing.
  const failures: string[] = [];

  for (let attempt = 0; attempt < 4; attempt++) {
    await page.goto("/");

    try {
      await racyOpenTab(page, GROUPED_VIEW);
    } catch (error) {
      failures.push(String(error).split("\n")[0]);
    }
  }

  expect(
    failures.length,
    "the pre-fix helper never lost the race, so this reproduction does not demonstrate the cause and the fix is unproven",
  ).toBeGreaterThan(0);

  // The message it produces is the one worth recording, because it names a missing feature and sent the investigation to the specs
  // rather than to the helper.
  console.log(`pre-fix helper failed ${failures.length} of 4 attempts: ${failures[0]}`);
});

test("the fixed helper wins the same race", async ({ page }) => {
  test.setTimeout(120_000);

  await page.goto("/");
  await throttle(page, 40);

  // The same conditions that break the old code. It failed four out of four when this was written, so six clean passes here is a
  // real difference rather than luck.
  for (let attempt = 0; attempt < 6; attempt++) {
    await page.goto("/");
    await openTab(page, GROUPED_VIEW);
    await expect(page.locator("[role=tab][aria-selected=true]")).toHaveText(GROUPED_VIEW);
  }
});
