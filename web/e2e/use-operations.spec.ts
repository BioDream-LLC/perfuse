import { test, expect } from "@playwright/test";
import { openTab } from "./nav";
import { sendMLLP, anADT } from "./mllp";

// Using the operational sections against real traffic.
//
// These five are the ones an operator lives in, and all five are meaningless without messages. Until the
// fixture channel got a reachable port, none of them had ever been tested with data in them - so every
// assertion was really about the empty state. Each test here puts traffic through the channel first.

function watch(page: import("@playwright/test").Page): string[] {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`page error: ${e}`));
  page.on("console", (m) => {
    if (m.type() === "error") problems.push(`console: ${m.text()}`);
  });
  return problems;
}

/** seed puts n messages through the fixture channel and returns their control ids. */
async function seed(n: number, prefix: string): Promise<string[]> {
  const ids: string[] = [];
  for (let i = 0; i < n; i++) {
    const id = `${prefix}${i}`;
    await sendMLLP(anADT(id));
    ids.push(id);
  }
  return ids;
}

test("the dashboard shows traffic and its window switcher works", async ({ page }) => {
  const problems = watch(page);

  await seed(3, "DASH");

  await page.goto("/");
  await expect(page.locator("main")).toContainText("labs", { timeout: 20_000 });

  // Each window, with the counters still rendering. A window that renders nothing is the failure mode
  // here, because the query behind it changes per window.
  for (const w of [/^1h$/, /^6h$/, /^24h$/]) {
    await page.getByRole("button", { name: w }).click();
    await expect(page.locator("main")).toContainText("labs", { timeout: 15_000 });
  }

  // Pausing must actually stop the polling, which is the only thing the control is for.
  //
  // Which state it starts in is read rather than assumed. The setting persists, so a test that ran earlier and
  // left it paused would make this fail - and it did, for exactly that reason, when a sweep that operates every
  // control pressed it. A toggle test should assert that the state flips, not that it flips from one particular
  // starting point.
  const live = page.getByRole("button", { name: /^(Live|Paused)$/ });
  await expect(live).toBeVisible({ timeout: 10_000 });

  const before = (await live.innerText()).trim();
  const after = before === "Live" ? "Paused" : "Live";

  await live.click();
  await expect(
    page.getByRole("button", { name: after, exact: true }),
    `pressing ${before} did not change it to ${after}`,
  ).toBeVisible({ timeout: 10_000 });

  // And back, so this is a toggle rather than a one-way trip, and the next test finds it as it was.
  await page.getByRole("button", { name: after, exact: true }).click();
  await expect(page.getByRole("button", { name: before, exact: true })).toBeVisible({ timeout: 10_000 });

  expect(problems, problems.join("\n")).toEqual([]);
});

test("messages can be searched by every filter the form offers", async ({ page }) => {
  const problems = watch(page);

  const ids = await seed(2, "SEARCH");

  await page.goto("/");
  await openTab(page, "Messages");
  await page.getByRole("button", { name: "Refresh" }).click();

  await expect(page.locator("tbody")).toContainText(ids[0]!, { timeout: 20_000 });

  // Control ID, which is an exact match, so a wrong query returns nothing and a right one returns one.
  await page.getByLabel("Control ID", { exact: true }).fill(ids[0]!);
  await page.getByRole("button", { name: "Search", exact: true }).first().click();
  await expect(page.locator("tbody")).toContainText(ids[0]!, { timeout: 15_000 });
  await expect(page.locator("tbody"), "the search did not narrow the list").not.toContainText(ids[1]!);

  // A control id that does not exist must return nothing rather than everything, which is the failure
  // a filter quietly ignored would produce.
  await page.getByLabel("Control ID", { exact: true }).fill("NOSUCHCONTROLID");
  await page.getByRole("button", { name: "Search", exact: true }).first().click();
  await expect(page.locator("tbody")).not.toContainText(ids[0]!, { timeout: 15_000 });

  await page.getByLabel("Control ID", { exact: true }).fill("");

  // Type.
  await page.getByLabel("Type", { exact: true }).fill("ADT");
  await page.getByRole("button", { name: "Search", exact: true }).first().click();
  await expect(page.locator("tbody")).toContainText(ids[0]!, { timeout: 15_000 });

  await page.getByLabel("Type", { exact: true }).fill("ZZZ");
  await page.getByRole("button", { name: "Search", exact: true }).first().click();
  await expect(page.locator("tbody")).not.toContainText(ids[0]!, { timeout: 15_000 });
  await page.getByLabel("Type", { exact: true }).fill("");

  // Content search, which reads the stored payload rather than the metadata.
  await page.getByLabel("In the message", { exact: true }).fill("DOE");
  await page.getByRole("button", { name: "Search", exact: true }).first().click();
  await expect(page.locator("main")).not.toHaveText("", { timeout: 15_000 });
  await page.getByLabel("In the message", { exact: true }).fill("");

  // The channel select and the outcome select, both of which change the query.
  const channel = page.getByLabel("Channel", { exact: true });
  await channel.selectOption({ label: "labs" });
  await page.getByRole("button", { name: "Search", exact: true }).first().click();
  await expect(page.locator("tbody")).toContainText(ids[0]!, { timeout: 15_000 });

  await page.getByLabel("What happened to it", { exact: true }).selectOption({
    label: "could not be read as HL7",
  });
  await page.getByRole("button", { name: "Search", exact: true }).first().click();
  // These messages parse, so this outcome should exclude them.
  await expect(page.locator("tbody")).not.toContainText(ids[0]!, { timeout: 15_000 });

  expect(problems, problems.join("\n")).toEqual([]);
});

test("a message can be opened, read in every view, and followed through the channel", async ({ page }) => {
  const problems = watch(page);

  const ids = await seed(1, "DETAIL");

  await page.goto("/");
  await openTab(page, "Messages");
  await page.getByRole("button", { name: "Refresh" }).click();

  const row = page.locator("tbody tr").filter({ hasText: ids[0]! });
  await expect(row).toBeVisible({ timeout: 20_000 });
  await row.first().click();

  // Explained: the parsed structure, which is the view that needs the payload to have been stored.
  await page.locator("main").getByRole("button", { name: "Explained", exact: true }).click();
  await expect(page.locator("main"), "the parsed view shows no segments").toContainText("MSH", {
    timeout: 15_000,
  });
  await expect(page.locator("main")).toContainText("PID");

  // Expanding and collapsing, and showing the empty fields, which is how somebody finds a field that is
  // absent rather than wrong.
  for (const label of [/^Expand all$/, /^Collapse all$/, /empty fields$/]) {
    const b = page.locator("main").getByRole("button", { name: label });
    if (await b.count()) await b.first().click();
  }

  // Raw: the bytes as they arrived.
  await page.locator("main").getByRole("button", { name: "Raw", exact: true }).click();
  await expect(page.locator("main")).toContainText("MSH|", { timeout: 15_000 });

  // Why this happened: the trace, which compiles and runs the channel's own configuration.
  await page.locator("main").getByRole("button", { name: /Why this happened/i }).click();
  await expect(
    page.locator("main"),
    "the trace shows no stages, so it explained nothing",
  ).toContainText(/parse|filter|destination/i, { timeout: 20_000 });

  await page.locator("main").getByRole("button", { name: "Close", exact: true }).click();

  expect(problems, problems.join("\n")).toEqual([]);
});

test("the queue view filters and reports honestly when empty", async ({ page }) => {
  const problems = watch(page);

  await page.goto("/");
  await openTab(page, "Queue");

  // The fixture channel delivers to a local directory, so nothing should be stuck. An empty queue must
  // say so rather than render a blank panel.
  await expect(page.locator("main")).toContainText(/Nothing is waiting|waiting/i, { timeout: 20_000 });

  await page.getByRole("button", { name: "Refresh now" }).click();
  await expect(page.locator("main")).not.toHaveText("", { timeout: 15_000 });

  // Every state the filter offers, because each is a different query.
  const states = page.locator("main select").first();
  if (await states.count()) {
    for (const label of [
      "Waiting",
      "Given up on",
      "Abandoned",
      "Delivered late",
      "Every state",
    ]) {
      await states.selectOption({ label });
      await expect(page.locator("main")).not.toHaveText("", { timeout: 10_000 });
    }
  }

  expect(problems, problems.join("\n")).toEqual([]);
});

test("alerts lists what is being watched and can acknowledge anything firing", async ({ page }) => {
  const problems = watch(page);

  await page.goto("/");
  await openTab(page, "Alerts");

  // Either nothing is wrong, or something is. Both are valid; a blank panel is not.
  await expect(page.locator("main")).toContainText(
    /Nothing is wrong|Alerting is not enabled|rule/i,
    { timeout: 20_000 },
  );

  // The rules list is the useful part when nothing is firing: it says what would be noticed.
  const rules = page.locator("main summary").filter({ hasText: /What is being watched/ });
  if (await rules.count()) {
    await rules.first().click();
    await expect(page.locator("main"), "the rules list expanded to nothing").toContainText(/severity|rule|when/i, {
      timeout: 10_000,
    });
  }

  // If something is firing, acknowledging it must report that it worked.
  const ack = page.locator("main").getByRole("button", { name: "Acknowledge" });
  if (await ack.count()) {
    await ack.first().click();
    await expect(page.locator("main")).toContainText(/Acknowledged/, { timeout: 15_000 });
  }

  expect(problems, problems.join("\n")).toEqual([]);
});

test("metrics renders every window and lists what is collected", async ({ page }) => {
  const problems = watch(page);

  await seed(2, "METRIC");

  await page.goto("/");
  await openTab(page, "Metrics");

  for (const w of ["15 min", "1 hour", "6 hours"]) {
    await page.getByRole("button", { name: w }).click();
    await expect(page.locator("main")).not.toHaveText("", { timeout: 15_000 });
  }

  // The series list is how somebody checks a metric exists before building a dashboard on it.
  const everything = page.locator("main summary").filter({ hasText: /Everything being collected/ });
  await expect(everything).toBeVisible({ timeout: 15_000 });
  await everything.click();
  await expect(
    page.locator("main"),
    "the collected-series list is empty even though messages were handled",
  ).toContainText("perfuse_", { timeout: 15_000 });

  // Pausing, then resuming.
  const live = page.getByRole("button", { name: /^(Live|Paused)$/ });
  if (await live.count()) {
    await live.first().click();
    await expect(page.getByRole("button", { name: "Paused" })).toBeVisible({ timeout: 10_000 });
    await page.getByRole("button", { name: "Paused" }).click();
  }

  expect(problems, problems.join("\n")).toEqual([]);
});
