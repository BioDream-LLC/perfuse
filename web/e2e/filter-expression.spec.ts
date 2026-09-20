import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// A filter has to be reachable from the form on every format.
//
// The rule rows offer a closed list of HL7 v2 fields - MSH-9.1, PID-3.1, PV1-2. An X12 interchange has none of
// them, so on that format the rows can express nothing, and there was no free-text field. The expression editor
// could say anything, but it could only be left and never entered: the convert button clears it and nothing set
// it, so the only route in was to load YAML the rows could not represent.
//
// The result was that a person creating an X12, NCPDP, delimited or raw channel in the browser could not give it
// a filter at all, while the server accepts one - perfuse check reads back an X12 filter on REF02 without
// complaint. That is a feature reachable from YAML and not from the interface, which this project treats as a
// product bug rather than a limitation.
//
// The path-walking drift guard could not see it, because filter is expressible and it was the field's values
// that had no control. Same blind spot that let language: wasm through.
//
// So this asserts on the generated file, after operating the control: a filter naming an X12 element, typed by
// somebody who never touched YAML.

function watch(page: import("@playwright/test").Page): string[] {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`page error: ${e}`));
  page.on("console", (m) => {
    if (m.type() !== "error") return;
    if (/Failed to load resource/.test(m.text())) return;
    problems.push(`console: ${m.text()}`);
  });
  return problems;
}

async function openNewChannelForm(page: import("@playwright/test").Page) {
  await page.goto("/");
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();
  await page.getByRole("button", { name: "Start from nothing" }).click();
}

async function generated(page: import("@playwright/test").Page): Promise<string> {
  const preview = page.locator("pre").first();
  await expect(preview).toBeVisible({ timeout: 15_000 });
  return (await preview.textContent()) ?? "";
}

test("an X12 channel can be given a filter without touching YAML", async ({ page }) => {
  const problems = watch(page);
  await openNewChannelForm(page);

  await page.getByLabel("Channel name", { exact: true }).fill("x12-expression-filter");
  await page.getByLabel("Message format", { exact: true }).selectOption("x12");

  // The control that did not exist. Without it this format has no route to a filter.
  await page.getByRole("button", { name: "Write the filter as an expression" }).click();

  const box = page.getByLabel("Filter expression", { exact: true });
  await expect(box).toBeVisible();
  await box.fill('REF02 == "CLAIM123"');

  // The only assertion that means anything: the file the server would write.
  await expect
    .poll(async () => await generated(page), { timeout: 15_000 })
    .toContain('REF02 == "CLAIM123"');

  expect(problems).toEqual([]);
});

test("switching to an expression keeps the conditions already built", async ({ page }) => {
  const problems = watch(page);
  await openNewChannelForm(page);

  await page.getByLabel("Channel name", { exact: true }).fill("hl7-expression-filter");

  // Build a rule with the rows first, on the format the rows are for.
  await page.getByRole("button", { name: /Add a condition|\+ Add/ }).first().click();
  // Addressed by element as well as label: "Field" is not unique on this form, and the rule row's control is the
  // select. A label-only lookup found a different input and failed on the type rather than on the behaviour.
  await page.locator('select[aria-label="Field"]').first().selectOption("MSH-9.1");
  await page.locator('input[aria-label="Value"]').first().fill("ADT");

  await page.getByRole("button", { name: "Write the filter as an expression" }).click();

  // Seeded rather than blank. Discarding two conditions because somebody wanted to refine them would make the
  // button a trap, and the person who lost the work would not use it twice.
  const box = page.getByLabel("Filter expression", { exact: true });
  await expect(box).toBeVisible();
  await expect(box).toHaveValue(/MSH-9\.1/);
  await expect(box).toHaveValue(/ADT/);

  expect(problems).toEqual([]);
});

test("an X12 channel can be given a filter script, which the form used to refuse", async ({ page }) => {
  const problems = watch(page);
  await openNewChannelForm(page);

  await page.getByLabel("Channel name", { exact: true }).fill("x12-filter-script");
  await page.getByLabel("Message format", { exact: true }).selectOption("x12");

  // The form disabled these two fields on X12, NCPDP, delimited and SCRIPT, explaining that a filter script needs
  // the message as a tree and only HL7 has one. All four run both slots. perfuse check accepts a JavaScript filter
  // and transformer on an X12 channel and prints the channel back without complaint.
  //
  // So the assertion is that the field takes what is typed and it reaches the file. A disabled field would fail on
  // the fill, and a field that accepted text the emitter dropped would fail on the preview.
  const filter = page.getByLabel("Filter", { exact: true }).first();
  await expect(filter).toBeEnabled();
  await filter.fill("return msg.get('REF02') !== '';");

  await expect
    .poll(async () => await generated(page), { timeout: 15_000 })
    .toContain("REF02");

  expect(problems).toEqual([]);
});
