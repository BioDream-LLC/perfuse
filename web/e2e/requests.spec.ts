import { test, expect } from "@playwright/test";
import { openTab, tabLabels } from "./nav";

// Whether any request the interface makes is refused by the server's own guards.
//
// This exists because the AI Mapper had never worked. It posted with a raw fetch that carried no
// X-Perfuse-Request header, so the server refused every request as cross-site, and the panel reported
// the refusal as an error - which read as a model that could not answer rather than as a request that
// never arrived. Nothing caught it because the tab rendered, the button was there, and the only
// assertion anyone had written matched text that was the default contents of a textarea.
//
// So this watches the network instead of the markup. A 401, 403 or 5xx to the interface's own API means
// the interface asked for something it is not allowed to ask for, or asked wrongly, and both are defects
// no matter how the panel presents them.

interface Refusal {
  tab: string;
  method: string;
  url: string;
  status: number;
}

test("no section makes a request the server refuses", async ({ page }) => {
  // Twenty-one sections, each given time for its own fetches to land. That does not fit the suite's
  // default per-test budget, and it failed intermittently as a timeout rather than as a refusal - which
  // read like a defect it had found.
  test.setTimeout(120_000);

  const refusals: Refusal[] = [];
  let current = "startup";

  page.on("response", (res) => {
    const url = new URL(res.url());
    if (!url.pathname.startsWith("/api/")) return;

    // Excluded, with reasons rather than by convenience:
    //
    //   404 - several panels probe for an optional feature and handle its absence.
    //   409 - starting a channel that is already running is a real conflict, not a broken request.
    //   503 - the server returns this for a subsystem that is not running, such as metrics on an
    //         instance started without an engine. It is a configuration state the interface handles,
    //         and it can appear briefly at startup before the engine is ready, which made an earlier
    //         version of this test flaky. A persistent one would show up as an empty panel elsewhere.
    //
    // What is left is the interesting set: not allowed, asked wrongly, or the server broke.
    const status = res.status();
    if (status === 401 || status === 403 || (status >= 500 && status !== 503)) {
      refusals.push({ tab: current, method: res.request().method(), url: url.pathname, status });
    }
  });

  await page.goto("/");

  const labels = await tabLabels(page);
  for (const label of labels) {
    current = label;
    await openTab(page, label);
    // Long enough for the panel's own fetches to land, including a debounced one.
    await page.waitForTimeout(700);
  }

  expect(
    refusals.map((r) => `${r.tab}: ${r.method} ${r.url} -> ${r.status}`),
    `The interface made requests the server refused:\n  ${refusals
      .map((r) => `${r.tab}: ${r.method} ${r.url} -> ${r.status}`)
      .join("\n  ")}\n\n` +
      `A 403 on a POST, PUT or DELETE usually means the X-Perfuse-Request header is missing, which ` +
      `happens when a component calls fetch directly instead of going through the api client.`,
  ).toEqual([]);
});

// The same watch, but while pressing the buttons - because the mapper's defect only appeared on a POST,
// and a POST only happens when somebody presses something.
test("no primary action in any section is refused by the server", async ({ page }) => {
  test.setTimeout(90_000);

  const refusals: Refusal[] = [];
  let current = "startup";

  page.on("response", (res) => {
    const url = new URL(res.url());
    if (!url.pathname.startsWith("/api/")) return;
    const status = res.status();
    if (status === 401 || status === 403 || (status >= 500 && status !== 503)) {
      refusals.push({ tab: current, method: res.request().method(), url: url.pathname, status });
    }
  });

  // One representative action per section that has one, named rather than discovered, so a section
  // whose button is renamed fails here instead of quietly going unexercised.
  const actions: { tab: string; button: RegExp }[] = [
    { tab: "FHIR lab", button: /^Convert to FHIR$/ },
    { tab: "Documents", button: /^Read the document$/ },
    { tab: "AI Mapper", button: /^Suggest mappings$/ },
    { tab: "Messages", button: /^Refresh$/ },
    { tab: "Queue", button: /^Refresh now$/ },
    { tab: "Activity", button: /^Refresh$/ },
    { tab: "Metrics", button: /^15 min$/ },
    { tab: "Flow map", button: /^Last 6 hours$/ },
  ];

  await page.goto("/");

  for (const { tab, button } of actions) {
    current = tab;
    await openTab(page, tab);

    const b = page.locator("main").getByRole("button", { name: button }).first();
    await expect(b, `${tab} no longer has a ${button} button`).toBeVisible({ timeout: 20_000 });
    await b.click();
    await page.waitForTimeout(1200);
  }

  expect(
    refusals.map((r) => `${r.tab}: ${r.method} ${r.url} -> ${r.status}`),
    `These actions were refused by the server:\n  ${refusals
      .map((r) => `${r.tab}: ${r.method} ${r.url} -> ${r.status}`)
      .join("\n  ")}`,
  ).toEqual([]);
});
