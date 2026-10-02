import { test, expect, type Page } from "@playwright/test";
import { openTab } from "./nav";

/**
 * Uses the alert rules editor, which is the interface for the one thing an operator changes most in the first weeks of a deployment.
 *
 * # What these tests are guarding
 *
 * The rules were editable before this screen existed - as a YAML text box on the settings page. So "can they be edited" is not the
 * question. The question is whether they can be edited by somebody who does not already know the file format, and every assertion here
 * is about that: a kind chosen from a list rather than spelled, a threshold labelled with its unit, a share shown as a percentage, and a
 * saved change that comes back after a reload.
 *
 * The percentage conversion gets its own test because it is the reason the screen is worth having. A share is stored as a fraction, so
 * five percent is 0.05, and somebody typing 5 into a raw file gets five hundred percent - a rule that loads, validates, reports nothing
 * wrong, and can never fire. That failure is silent and in the direction of not being told about problems.
 */

async function openRulesEditor(page: Page) {
  await openTab(page, "Alerts");

  // The editor is below the firing alerts and the watched-rules table, and it fetches its own catalogue, so it is not there the instant
  // the tab opens.
  await expect(page.getByRole("heading", { name: "Alert rules" })).toBeVisible({ timeout: 20_000 });
  await expect(page.locator('select[id$="-kind"]').first()).toBeVisible({ timeout: 20_000 });
}

function watchForFaults(page: Page): string[] {
  const faults: string[] = [];

  page.on("pageerror", (e) => faults.push(`uncaught: ${e.message}`));
  page.on("console", (m) => {
    if (m.type() !== "error") return;

    const text = m.text();
    if (text.includes("Failed to load resource") || text.includes("net::ERR")) return;

    faults.push(`console: ${text}`);
  });

  return faults;
}

test("the rules on the screen are the rules in the file", async ({ page }) => {
  await page.goto("/");
  await openRulesEditor(page);

  // Three rules are in the fixture's file. Counting them proves the editor is reading the server rather than showing a template.
  await expect(page.locator('select[id$="-kind"]')).toHaveCount(3);

  // And the fixture's disabled rule has to arrive disabled. A form that reads a rule and quietly loses its disabled flag would turn an
  // alert somebody deliberately switched off back on, and nothing on screen would say so.
  const switches = page.getByRole("checkbox", { name: "Checking this rule" });
  await expect(switches).toHaveCount(3);
  await expect(
    switches.nth(2),
    "the third rule is disabled in the file and arrived switched on, so saving would silently re-enable it",
  ).not.toBeChecked();
});

test("every kind the engine can evaluate is offered by name", async ({ page }) => {
  await page.goto("/");
  await openRulesEditor(page);

  const options = await page
    .locator('select[id$="-kind"]')
    .first()
    .locator("option")
    .evaluateAll((os) => os.map((o) => ({ value: (o as HTMLOptionElement).value, label: (o as HTMLOptionElement).textContent ?? "" })));

  // Eleven kinds are evaluated. The number is asserted rather than the list so that adding a kind fails here and gets a label, instead
  // of appearing as a bare identifier or not at all - which is how below-rhythm stayed unreachable for so long.
  expect(options.length, `the editor offers ${options.length} kinds:\n${options.map((o) => o.value).join("\n")}`).toBe(11);

  // below-rhythm in particular, because it had an evaluator and tests and could not be configured at all until now. It is the only rule
  // that can report a feed going quiet when that feed is legitimately silent most of the week.
  const rhythm = options.find((o) => o.value === "below-rhythm");
  expect(rhythm, "below-rhythm is not offered, so the one rule that catches a stopped lab feed is still unreachable").toBeTruthy();

  // Offered by name, not by identifier. A list of slugs is a list somebody has to already understand.
  for (const o of options) {
    expect(o.label, `${o.value} is shown as its identifier rather than a name`).not.toBe(o.value);
    expect(o.label.length, `${o.value} has no readable label`).toBeGreaterThan(3);
  }
});

test("a share is edited as a percentage and stored as a fraction", async ({ page }) => {
  await page.goto("/");
  await openRulesEditor(page);

  // The first fixture rule is error-rate at 0.1, so the box must say 10 and not 0.1.
  const threshold = page.locator("#alert-rule-0-threshold");
  await expect(threshold, "the error-rate threshold is not shown as a percentage").toHaveValue("10");

  // The label has to name the unit. Without it this is a bare number box on a field whose meaning changes per kind.
  await expect(page.locator('label[for="alert-rule-0-threshold"]')).toContainText("%");

  // And it must say which way the rule fires, because the number means the opposite for the silence rules.
  await expect(page.locator("main")).toContainText(/Fires when the measurement rises above/);

  await threshold.fill("25");

  await page.getByRole("button", { name: "Save rules" }).click();
  await expect(page.getByRole("status")).toContainText(/Saved 3 rules/, { timeout: 20_000 });

  // Reloaded, because the point is what reached the file. Twenty-five percent has to come back as 25 - which it only can if it was
  // stored as 0.25 and converted both ways.
  await page.reload();
  await openRulesEditor(page);
  await expect(
    page.locator("#alert-rule-0-threshold"),
    "a percentage was saved and came back as something else, so the conversion is wrong in one direction",
  ).toHaveValue("25");

  // Put it back, since one server serves the whole suite.
  await page.locator("#alert-rule-0-threshold").fill("10");
  await page.getByRole("button", { name: "Save rules" }).click();
  await expect(page.getByRole("status")).toBeVisible({ timeout: 20_000 });
});

test("a rule is added, saved, and still there after a reload", async ({ page }) => {
  const faults = watchForFaults(page);

  await page.goto("/");
  await openRulesEditor(page);

  const before = await page.locator('select[id$="-kind"]').count();

  await page.getByRole("button", { name: "+ Add rule" }).click();
  await expect(page.locator('select[id$="-kind"]')).toHaveCount(before + 1);

  // Set it to the rule that could not be configured before this work, so this test also covers that fix end to end.
  const newKind = page.locator('select[id$="-kind"]').nth(before);
  await newKind.selectOption("below-rhythm");

  // Changing the kind must reset the threshold to that kind's default, and the default for a share must be a share. Carrying 500 over
  // from a queue rule would leave fifty thousand percent in the box: valid, saveable and unable to fire.
  await expect(
    page.locator(`#alert-rule-${before}-threshold`),
    "switching to a share-based kind kept a threshold that is not a share",
  ).toHaveValue("80");

  await page.getByRole("button", { name: "Save rules" }).click();
  await expect(page.getByRole("status")).toContainText(/Saved 4 rules/, { timeout: 20_000 });

  await page.reload();
  await openRulesEditor(page);
  await expect(
    page.locator('select[id$="-kind"]'),
    "a rule was added and saved and did not come back, so the save reported success and wrote nothing",
  ).toHaveCount(before + 1);
  await expect(page.locator('select[id$="-kind"]').nth(before)).toHaveValue("below-rhythm");

  // Remove it again so the rest of the suite sees the fixture it was written against.
  await page.getByRole("button", { name: `Delete rule ${before + 1}` }).click();
  await page.getByRole("button", { name: "Save rules" }).click();
  await expect(page.getByRole("status")).toContainText(/Saved 3 rules/, { timeout: 20_000 });

  expect(faults, `using the rules editor produced errors:\n${faults.join("\n")}`).toEqual([]);
});

test("deleting every rule is refused with a way forward", async ({ page }) => {
  await page.goto("/");
  await openRulesEditor(page);

  const count = await page.locator('select[id$="-kind"]').count();
  for (let i = count; i >= 1; i -= 1) {
    await page.getByRole("button", { name: `Delete rule ${i}` }).click();
  }
  await expect(page.locator('select[id$="-kind"]')).toHaveCount(0);

  await page.getByRole("button", { name: "Save rules" }).click();

  // Refusing is right - a file with no rules is almost never what somebody meant. Refusing without saying what to do instead is not,
  // because both ways out are non-obvious: one gets the built-in rules back, the other keeps a tuned threshold.
  await expect(page.locator("main"), "deleting every rule was refused with no way forward offered").toContainText(
    /at least one rule/i,
    { timeout: 20_000 },
  );
  await expect(page.locator("main")).toContainText(/-alerts/);

  // The file must be untouched, so a reload brings the rules back. A refusal that had already written would be the outage it prevents.
  await page.reload();
  await openRulesEditor(page);
  await expect(
    page.locator('select[id$="-kind"]'),
    "a refused save emptied the rules file anyway",
  ).toHaveCount(count);
});

test("a rule can be switched off without losing its threshold", async ({ page }) => {
  await page.goto("/");
  await openRulesEditor(page);

  const threshold = await page.locator("#alert-rule-1-threshold").inputValue();
  const box = page.getByRole("checkbox", { name: "Checking this rule" }).nth(1);
  await expect(box).toBeChecked();

  await box.uncheck();
  await page.getByRole("button", { name: "Save rules" }).click();
  await expect(page.getByRole("status")).toBeVisible({ timeout: 20_000 });

  await page.reload();
  await openRulesEditor(page);

  // Both halves matter. Off is the point of the switch, and keeping the number is the reason it exists rather than a delete: a
  // threshold somebody tuned against their own traffic is the expensive part of a rule.
  await expect(page.getByRole("checkbox", { name: "Checking this rule" }).nth(1)).not.toBeChecked();
  await expect(
    page.locator("#alert-rule-1-threshold"),
    "switching a rule off lost the threshold, which is the thing worth keeping",
  ).toHaveValue(threshold);

  // Back on, for the rest of the suite.
  await page.getByRole("checkbox", { name: "Checking this rule" }).nth(1).check();
  await page.getByRole("button", { name: "Save rules" }).click();
  await expect(page.getByRole("status")).toBeVisible({ timeout: 20_000 });
});

test("a rule that cannot be evaluated cannot be described", async ({ page }) => {
  await page.goto("/");
  await openRulesEditor(page);

  // Every kind offered must carry its own explanation, because the threshold is meaningless without one - and a rule an operator
  // cannot reason about is one they will set wrongly and trust.
  const kinds = await page
    .locator('select[id$="-kind"]')
    .first()
    .locator("option")
    .evaluateAll((os) => os.map((o) => (o as HTMLOptionElement).value));

  const select = page.locator("#alert-rule-0-kind");
  for (const kind of kinds) {
    await select.selectOption(kind);
    await page.waitForTimeout(120);

    const text = await page.locator("main").innerText();
    expect(text, `${kind} is offered with no explanation of what it watches`).toMatch(/Fires when/);

    // channel-down has nothing to threshold, so it must not show a threshold box. Offering one would invite a number that does
    // nothing, which is indistinguishable from a number that does something.
    const hasThreshold = await page.locator("#alert-rule-0-threshold").count();
    if (kind === "channel-down") {
      expect(hasThreshold, "channel-down offers a threshold box and has nothing to threshold").toBe(0);
    } else {
      expect(hasThreshold, `${kind} offers no threshold box`).toBe(1);
    }
  }
});

test("a channel that no longer exists can still be chosen back", async ({ page }) => {
  await page.goto("/");
  await openRulesEditor(page);

  // The fixture's second rule names a channel this server does not have, which is a legitimate state: a rule can be written before a
  // channel is created, or the channel can be renamed afterwards. The rule is valid and matches nothing.
  const channel = page.locator("#alert-rule-1-channel");
  const original = await channel.inputValue();
  expect(original, "the fixture rule no longer names a channel that is missing, so this test is not testing anything").not.toBe("");

  await expect(channel.locator(`option[value="${original}"]`)).toContainText("no such channel now");

  // Away and back. The first version of this screen built that option from the current value, so choosing anything else made it
  // disappear and there was no way to return - a misspelled channel could be looked at once and then only fixed by editing the file
  // this screen exists to avoid. It surfaced as the widget sweep timing out after four minutes rather than as anything legible.
  await channel.selectOption("");
  await expect(channel).toHaveValue("");

  await channel.selectOption(original);
  await expect(
    channel,
    "a channel that does not exist could not be selected again after choosing another, so the rule cannot be put back",
  ).toHaveValue(original);
});
