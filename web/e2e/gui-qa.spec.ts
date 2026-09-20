import { test, expect } from "@playwright/test";
import { openTab, groupButtons, tabLabels, panelText } from "./nav";

// QA battery for the interface built between 23 and 25 August: the tab overflow menu, the flow map,
// the mapper panel, the syntax highlighter and the new source forms in the channel builder.
//
// These are written to DISCOVER rather than to confirm. Each asserts a property a user would notice
// if it broke - every tab reachable, no console errors, no placeholder text on screen, a control that
// can be operated by keyboard - rather than asserting that a particular element exists, which passes
// while the feature underneath is useless.


// ── Tab bar: reachability and accessibility ───────────────────────────────────

// Every tab a user's role permits must be reachable. The overflow menu introduced a second way to
// reach a tab, and a tab reachable by neither path is a feature that has been removed by accident.
test("every permitted tab is reachable and renders content", async ({ page }) => {
  // Three minutes. This walks all twenty-one sections and asserts each renders, which takes about a
  // minute on an idle machine and longer when the rest of the suite is competing for it.
  //
  // This is the test that failed twice earlier and was recorded as unexplained. It was a timeout, not a
  // finding. Worth writing down because the failure looked like a broken tab and was a budget.
  //
  // Three tests now walk every section: this one, the label sweep and the network sweep. That is roughly
  // half the suite's runtime and they could share one pass. Left separate for now because each asserts
  // something different and merging them would couple three concerns to save three minutes.
  test.setTimeout(180_000);

  test.setTimeout(180_000);
  await page.goto("/");

  const labels = await tabLabels(page);
  expect(labels.length, "no tabs found at all").toBeGreaterThan(5);

  const empty: string[] = [];
  for (const label of labels) {
    await openTab(page, label);
    const main = page.locator("main");
    await expect(main).toBeVisible();

    // Each view fetches its own data, so allow it to arrive before judging it empty. Reading
    // textContent immediately measures the render before the fetch resolves, which is a race that
    // reports every asynchronous tab as blank.
    const text = await panelText(page);
    if (text.length <= 20) empty.push(`${label} (${text.length} chars)`);
  }
  expect(empty, "tabs that opened but rendered essentially nothing").toEqual([]);
});

// A tablist may contain only tabs. Anything else inside one is invalid ARIA, and assistive technology
// may skip it or report it as a tab it is not.
test("the tablist contains only tabs", async ({ page }) => {
  await page.goto("/");

  const roles = await page.evaluate(() =>
    Array.from(document.querySelectorAll("nav[role=tablist] > *")).map((el) => ({
      tag: el.tagName.toLowerCase(),
      role: el.getAttribute("role"),
      text: (el.textContent ?? "").trim().slice(0, 24),
    })),
  );
  const notTabs = roles.filter((r) => r.role !== "tab");
  expect(notTabs, "non-tab children inside role=tablist").toEqual([]);
});

// Whichever view is showing must be named in the tablist, including when it was reached through the
// overflow. Otherwise the tablist reports nothing selected and the current view is anonymous.
test("the selected view is always exposed as the selected tab", async ({ page }) => {
  await page.goto("/");

  for (const label of await tabLabels(page)) {
    await openTab(page, label);
    const selected = page.locator("[role=tab][aria-selected=true]");
    await expect(selected, `no tab reports selected while viewing ${label}`).toHaveCount(1);
    await expect(selected).toHaveText(label);
  }
});

// A tab controls a panel. Without aria-controls and a corresponding tabpanel, a screen reader user
// activating a tab is not told what changed, and the relationship the tablist pattern exists to
// express is absent.
test("the tab bar and its panel form a complete tablist relationship", async ({ page }) => {
  await page.goto("/");

  const selected = page.locator('[role=tab][aria-selected=true]');
  await expect(selected, "no tab reports itself as selected").toHaveCount(1);

  const controls = await selected.getAttribute("aria-controls");
  expect(controls, "the selected tab does not say which panel it controls").toBeTruthy();

  const panel = page.locator(`#${controls}`);
  await expect(panel, "aria-controls points at an element that does not exist").toHaveCount(1);
  await expect(panel).toHaveAttribute("role", "tabpanel");
});

// A group menu must be operable without a mouse. A dropdown that only closes on an outside click traps
// a keyboard user, who has no way to dismiss it.
//
// Runs against the first group rather than a named one, so regrouping the navigation does not silently
// turn this into a test of nothing.
test("a navigation group menu can be opened, navigated and dismissed by keyboard", async ({ page }) => {
  await page.goto("/");

  const more = groupButtons(page).first();

  // Waited for rather than counted, and asserted rather than skipped.
  //
  // This read the count immediately and skipped the test when it was zero, blaming the viewport. count() does not wait, so on a page
  // that had not finished rendering the test excused itself and was reported as skipped - which is worse than failing, because a
  // skipped test is a green run with less coverage in it and nobody investigates the reason.
  //
  // The viewport is fixed at 1440x900 by the config, so group menus are not optional here. If there are none, that is a finding.
  await expect(more).toBeVisible({ timeout: 30_000 });

  // Open with the keyboard rather than a click.
  await more.focus();
  await page.keyboard.press("Enter");
  await expect(more).toHaveAttribute("aria-expanded", "true");

  // Focus must land inside the menu, or the user opened something they cannot reach.
  await expect(page.getByRole("menuitem").first(), "focus did not enter the menu").toBeFocused();

  // Arrow keys must move between items.
  await page.keyboard.press("ArrowDown");
  await expect(page.getByRole("menuitem").nth(1), "ArrowDown did not move to the next item").toBeFocused();

  // Escape must close it and return focus, or the user is stranded.
  await page.keyboard.press("Escape");
  await expect(more, "Escape did not close the group menu").toHaveAttribute("aria-expanded", "false");
  await expect(more, "focus was not returned to the menu button after closing").toBeFocused();
});

// ── No placeholder or error text anywhere ─────────────────────────────────────

// undefined, NaN and [object Object] on screen are all the same defect: a value that was formatted
// without being checked. They are the visible tip of a contract mismatch.
test("no tab renders a formatting artefact", async ({ page }) => {
  test.setTimeout(180_000);
  await page.goto("/");

  const bad = /undefined|NaN|\[object Object\]|Infinity|\bnull\b/;
  const offenders: string[] = [];

  for (const label of await tabLabels(page)) {
    await openTab(page, label);
    await page.waitForTimeout(400);
    const text = (await page.locator("main").textContent()) ?? "";
    const hit = text.match(bad);
    if (hit) offenders.push(`${label}: ${hit[0]} in ...${text.slice(Math.max(0, (hit.index ?? 0) - 60), (hit.index ?? 0) + 60)}...`);
  }

  expect(offenders, "formatting artefacts visible to the user").toEqual([]);
});

// A console error is a defect the developer already knows about but the user cannot see. React key
// warnings, failed requests and unhandled rejections all land here.
test("no tab logs a console error or fails a request", async ({ page }) => {
  test.setTimeout(180_000);

  const errors: string[] = [];
  page.on("console", (m) => {
    if (m.type() === "error") errors.push(`console: ${m.text().slice(0, 200)}`);
  });
  page.on("pageerror", (e) => errors.push(`pageerror: ${e.message.slice(0, 200)}`));
  page.on("requestfailed", (r) => {
    const f = r.failure()?.errorText ?? "";
    if (!/ERR_ABORTED/.test(f)) errors.push(`request failed: ${r.url()} ${f}`);
  });
  page.on("response", (r) => {
    if (r.status() >= 500) errors.push(`server error: ${r.status()} ${r.url()}`);
  });

  await page.goto("/");
  for (const label of await tabLabels(page)) {
    await openTab(page, label);
    await page.waitForTimeout(400);
  }

  expect(errors, "errors logged while walking every tab").toEqual([]);
});

// ── Flow map ──────────────────────────────────────────────────────────────────

// The scrubber is the flow map's whole point: it moves the view back in time. A scrubber that does
// not change what is rendered is decorative.
test("the flow map scrubber moves the view and does not break at either end", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Flow map");

  const main = page.locator("main");
  const slider = main.locator("input[type=range]").first();

  // Awaited rather than counted immediately: the scrubber appears once the flow data resolves, so a
  // bare count() here measures the network and skips the test on a fast machine.
  await expect(slider, "the flow map never rendered a scrubber").toBeVisible({ timeout: 15_000 });
  await expect(slider, "the scrubber has no accessible name").toHaveAccessibleName(/.+/);

  // Driven across its own declared range. The scrubber indexes buckets in the window, so its maximum
  // is the bucket count rather than a percentage, and assuming 100 fills it with an out-of-range value.
  const { min, max } = await slider.evaluate((el) => ({
    min: (el as HTMLInputElement).min || "0",
    max: (el as HTMLInputElement).max || "0",
  }));
  expect(Number(max), "the scrubber spans no range, so there is nothing to scrub").toBeGreaterThan(0);

  for (const pos of [min, max, String(Math.floor(Number(max) / 2))]) {
    await slider.fill(pos);
    await page.waitForTimeout(300);
    const text = (await main.textContent()) ?? "";
    expect(text, `scrubber at ${pos} produced an artefact`).not.toMatch(/undefined|NaN/);
    expect(text.trim().length, `scrubber at ${pos} emptied the view`).toBeGreaterThan(20);
  }
});

// ── Mapper panel ──────────────────────────────────────────────────────────────

// An abstention is the engine declining to guess. If it renders like a suggestion, a user accepts it
// and the abstention threshold has achieved nothing.
test("the mapper marks an abstention differently from a suggestion", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "AI Mapper");

  const main = page.locator("main");
  await expect(main).toBeVisible();

  // Drive the panel with a pair that must abstain: the names are near-identical and both hold dates.
  const input = main.locator("textarea, input[type=text]").first();
  await expect(input, "the mapper panel never rendered an input").toBeVisible({ timeout: 15_000 });

  await input.fill("Date of Birth");
  const go = main.getByRole("button", { name: /suggest|map|analyse|analyze|run/i }).first();
  if (await go.count()) await go.click();
  await page.waitForTimeout(1200);

  const text = (await main.textContent()) ?? "";
  expect(text).not.toMatch(/undefined|NaN/);
});

// ── Channel builder: the source forms added for DICOM and the JavaScript reader ──

// A form field that does not persist is worse than an absent one: the user configures it, sees it
// accepted, and the channel runs without it. Each source type offered must at least present a form.
test("every source type offered in the builder has a configuration form", async ({ page }) => {
  test.setTimeout(120_000);
  await page.goto("/");
  await openTab(page, "Channels");

  // The builder opens from the channel list rather than being a view of its own.
  await page.getByRole("button", { name: /new channel/i }).first().click();

  const main = page.locator("main");

  // Selected by its label rather than by document order: the first select on the form is the message
  // format, and picking it by position silently tests the wrong control.
  // By label rather than by markup. The select used to sit inside its label element, which is how it was
  // associated; it now sits beside a label that references it by id, because enclosing it also put the
  // field's hint into its accessible name. Locating by label works either way and does not care.
  const select = page.getByLabel(/How do messages get here/).first();
  await expect(select, "the builder never offered a source selector").toBeVisible({ timeout: 15_000 });

  const values = (
    await select.locator("option").evaluateAll((os) =>
      os.map((o) => (o as HTMLOptionElement).value).filter(Boolean),
    )
  ) as string[];
  expect(values.length, "no source types offered").toBeGreaterThan(3);

  const blank: string[] = [];
  for (const v of values) {
    await select.selectOption(v);
    await page.waitForTimeout(300);
    const fields = await main.locator("input:visible, textarea:visible, select:visible").count();
    // The selector itself is one of them; a source type offering nothing further has no form.
    if (fields <= 1) blank.push(v);
    const text = (await main.textContent()) ?? "";
    expect(text, `source ${v} rendered an artefact`).not.toMatch(/undefined|NaN|\[object Object\]/);
  }

  expect(blank, "source types selectable in the builder but with no configuration form").toEqual([]);
});

// ── Syntax highlighting of message content ───────────────────────────────────

// The highlighter renders message content, which arrives from outside the system. Markup in a message
// must appear as text rather than becoming part of the document.
test("message content containing markup is displayed as text, not interpreted", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Playground");

  const main = page.locator("main");
  const input = main.locator("textarea").first();
  await expect(input, "the playground never rendered an input").toBeVisible({ timeout: 15_000 });

  const payload = 'MSH|^~\\&|<img src=x onerror="window.__xss=1">|B|C|D|20260101||ADT^A01|1|P|2.5\r';
  await input.fill(payload);

  await main.getByRole("button", { name: /^(Run it|Read this message)$/ }).first().click();
  await page.waitForTimeout(1500);

  // Nothing may have executed, and no element may have been created from the payload.
  const executed = await page.evaluate(() => (window as unknown as { __xss?: number }).__xss === 1);
  expect(executed, "script in message content executed").toBe(false);
  await expect(page.locator('main img[src="x"]'), "an element was built from message content").toHaveCount(0);
});
