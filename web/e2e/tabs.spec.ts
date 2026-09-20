import { test, expect, Page, ConsoleMessage } from "@playwright/test";
import { openTab, tabLabels } from "./nav";

// Every tab, visited and checked.
//
// This is the test that could not exist before: it opens the real GUI served by the real binary and looks for the failures
// that no unit test can see - a tab that renders nothing, a request that 500s, a React error boundary, a console exception
// from a contract that drifted between the Go API and the TypeScript that reads it.
//
// The nineteen names are written out rather than read from the running page on purpose. Reading them from the page would make
// the test agree with whatever the page happens to show, so a tab that silently disappeared would still pass. This is the same
// drift-guard argument as the resource-type registry in the FHIR server.
const TABS = [
  "Dashboard",
  "Channels",
  "Messages",
  "Queue",
  "Alerts",
  "Metrics",
  "FHIR lab",
  "Documents",
  "Scripts",
  "Migrate",
  "Contracts",
  "Tables",
  "Fleet",
  "TEFCA",
  "Flow map",
  "Playground",
  "Shadow",
  "Certificates",
  "Users",
  "Activity",
  "Settings",
] as const;

/** collected watches for the failures a human would notice only by accident. */
function collected(page: Page) {
  const consoleErrors: string[] = [];
  const pageErrors: string[] = [];
  const badResponses: string[] = [];

  page.on("console", (m: ConsoleMessage) => {
    if (m.type() === "error") {
      const text = m.text();
      // A favicon 404 is noise on a page served from an embedded filesystem and says nothing about the interface.
      if (text.includes("favicon")) return;
      consoleErrors.push(text);
    }
  });

  // An uncaught exception in a render is the single most likely way a tab shows a blank panel, and nothing else reports it.
  page.on("pageerror", (e) => pageErrors.push(e.message));

  page.on("response", (r) => {
    if (r.status() >= 500) {
      badResponses.push(`${r.status()} ${r.request().method()} ${new URL(r.url()).pathname}`);
    }
  });

  return { consoleErrors, pageErrors, badResponses };
}

test.describe("the web interface", () => {
  test("signs in and shows the tab bar", async ({ page }) => {
    const found = collected(page);

    await page.goto("/");
    await expect(page.locator("nav button").first()).toBeVisible();

    // Every tab an administrator should see is actually offered. An administrator sees all of them, so a missing name here is
    // either a routing regression or a role check that became stricter than intended.
    //
    // Asked of the reachable set rather than of the nav element, because the bar shows the first seven
    // inline and puts the rest behind a menu button that deliberately sits outside the tablist. A view
    // is offered if a user can get to it, not if it happens to be rendered in one particular element.
    const reachable = new Set(await tabLabels(page));
    const missing = TABS.filter((label) => !reachable.has(label));
    expect(missing, "tabs an administrator should see but cannot reach").toEqual([]);

    expect(found.pageErrors, "uncaught exceptions on first load").toEqual([]);
  });

  for (const label of TABS) {
    test(`the ${label} tab opens and renders`, async ({ page }) => {
      const found = collected(page);

      await page.goto("/");
      await openTab(page, label);

      // Something has to appear. A tab that swaps in an empty div passes any "no error" assertion, which is how a blank
      // panel survives every check that does not look.
      //
      // Polled rather than read once, because most tabs render their content after a fetch resolves. Reading immediately
      // measured how fast the network was, not whether the tab works - and it failed five tabs for that reason on the first
      // run. A genuinely empty panel still fails, it just takes ten seconds to say so.
      const panel = page.locator("main").first();
      const target = (await panel.count()) > 0 ? panel : page.locator("body");

      await expect
        .poll(
          async () => ((await target.innerText()) || "").trim().length,
          {
            message: `the ${label} tab never rendered any text`,
            // Thirty seconds rather than ten.
            //
            // Every section fetches on mount, and this test asserts only that something rendered. Ten seconds
            // was ample when the suite was a hundred and twenty tests; it is not always ample now that the
            // suite is a hundred and sixty and drives real traffic through the same server, because the
            // server answering these fetches is also handling everything else the run is doing.
            //
            // Raised deliberately and with evidence rather than to make a failure go away: two different tests
            // failed on two consecutive full runs and both passed alone in a few seconds, which is load rather
            // than a fault. A previous session recorded an "unexplained" intermittent failure here that turned
            // out to be exactly this.
            timeout: 30_000,
          },
        )
        .toBeGreaterThan(20);

      // React's error boundary and the app's own ErrorBox both surface here.
      await expect(
        page.getByText(/something went wrong|unexpected error|cannot read propert/i),
        `the ${label} tab shows an error`,
      ).toHaveCount(0);

      expect(found.pageErrors, `uncaught exceptions on the ${label} tab`).toEqual([]);
      expect(found.badResponses, `server errors while opening the ${label} tab`).toEqual([]);
      expect(found.consoleErrors, `console errors on the ${label} tab`).toEqual([]);
    });
  }
});
