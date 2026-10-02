import { test, expect } from "@playwright/test";
import { openTab, inlineTabs, tabLabels } from "./nav";

// Whether every form label in the product names its control.
//
// A swept property rather than a per-screen assertion, because the two defects behind it were invisible
// on screen and therefore could appear anywhere. Field took an optional htmlFor that almost nobody
// passed, so labels named nothing; and the builder's wrapper enclosed the hint inside the label, so a
// field's accessible name was a paragraph.
//
// Two rules, both cheap to state and both violated in the shipped build:
//
//   A label's `for` must reference an input, select or textarea. Pointing it at a div looks associated
//   and is not, which is worse than leaving it off.
//
//   A control that is visible and interactive should have an accessible name. An unnamed box is
//   announced as "edit text, blank" and there is nothing to tell a screen reader user what to type.

/** offences describes one broken label, with enough detail to find it. */
interface Offence {
  tab: string;
  detail: string;
}

async function auditLabels(page: import("@playwright/test").Page, tab: string): Promise<Offence[]> {
  return page.evaluate((where) => {
    const out: { tab: string; detail: string }[] = [];
    const main = document.querySelector("main");
    if (!main) return out;

    // Rule one: every `for` resolves to a labelable element.
    for (const label of Array.from(main.querySelectorAll("label[for]"))) {
      const target = label.getAttribute("for")!;
      const el = document.getElementById(target);
      const text = (label.textContent || "").trim().slice(0, 40);
      if (!el) {
        out.push({ tab: where, detail: `label "${text}" points at #${target}, which does not exist` });
      } else if (!["INPUT", "SELECT", "TEXTAREA"].includes(el.tagName)) {
        out.push({ tab: where, detail: `label "${text}" points at a <${el.tagName.toLowerCase()}>` });
      }
    }

    // Rule two: every visible control has a name from somewhere.
    const controls = Array.from(main.querySelectorAll("input, select, textarea")) as HTMLElement[];
    for (const c of controls) {
      const input = c as HTMLInputElement;
      if (input.type === "hidden") continue;

      const style = getComputedStyle(c);
      if (style.display === "none" || style.visibility === "hidden") continue;
      // A file input styled sr-only is driven by a wrapping label, which is a legitimate pattern.
      const wrapped = c.closest("label") !== null;

      const named =
        wrapped ||
        !!c.getAttribute("aria-label") ||
        !!c.getAttribute("aria-labelledby") ||
        !!c.getAttribute("title") ||
        (!!c.id && !!document.querySelector(`label[for="${CSS.escape(c.id)}"]`));

      if (!named) {
        const hint =
          input.placeholder || c.getAttribute("name") || input.type || c.tagName.toLowerCase();
        out.push({ tab: where, detail: `an unnamed <${c.tagName.toLowerCase()}> (${hint})` });
      }
    }

    return out;
  }, tab);
}

test("every form label in every section names a real control", async ({ page }) => {
  // Twenty-one sections, each given a moment to render its forms. That does not fit the suite's default
  // per-test budget when the machine is busy, and it failed as a timeout rather than as a finding.
  test.setTimeout(120_000);

  await page.goto("/");

  const labels = await tabLabels(page);
  const offences: Offence[] = [];

  for (const label of labels) {
    await openTab(page, label);
    // Give the panel a moment to fetch and render its forms.
    await page.waitForTimeout(400);
    offences.push(...(await auditLabels(page, label)));
  }

  expect(
    offences.map((o) => `${o.tab}: ${o.detail}`),
    `Labels that do not name a control:\n  ${offences.map((o) => `${o.tab}: ${o.detail}`).join("\n  ")}`,
  ).toEqual([]);
});

test("the channel builder names every field it shows", async ({ page }) => {
  // Nine templates, each reopened from the list.
  test.setTimeout(120_000);

  await page.goto("/");
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();

  const offences: Offence[] = [];

  // Every template, because each reveals a different set of source and destination fields, and a field
  // only shown for SFTP or a database is exactly the sort that goes unlabelled.
  const templates = [
    "Record an HL7 feed",
    "Pass a feed to another system",
    "Send admissions one way and results another",
    "Turn HL7 into FHIR",
    "Accept messages posted over HTTP",
    "Collect files from an SFTP server",
    "Send messages queued in a database",
    "Take in X12 claims",
    "Clean up a feed as it passes through",
  ];

  for (const t of templates) {
    const button = page.getByRole("button", { name: t }).first();
    await expect(button, `the ${t} template is not offered`).toBeVisible({ timeout: 15_000 });
    await button.scrollIntoViewIfNeeded();
    await button.click();
    await page.waitForTimeout(300);
    offences.push(...(await auditLabels(page, `builder / ${t}`)));

    // Back to the picker for the next one.
    await page.goto("/");
    await openTab(page, "Channels");
    await page.getByRole("button", { name: "+ New channel" }).click();
  }

  expect(
    offences.map((o) => `${o.tab}: ${o.detail}`),
    `Builder fields that do not name a control:\n  ${offences.map((o) => `${o.tab}: ${o.detail}`).join("\n  ")}`,
  ).toEqual([]);
});

// The accessible name must be the label, not the label plus its explanation.
//
// The builder's wrapper used to enclose the required marker and the whole hint inside the label element,
// so a field was announced as "Listen on required A port on its own accepts connections on every
// interface, which on a hospital network is usually what you want but never what you want on a machine
// facing the internet". That is a description, and there is an attribute for descriptions.
test("a field's accessible name is its label, not its label plus its hint", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();
  await page.getByRole("button", { name: "Record an HL7 feed" }).click();

  const tooLong = await page.evaluate(() => {
    const out: string[] = [];
    for (const label of Array.from(document.querySelectorAll("main label"))) {
      const text = (label.textContent || "").trim();
      // A label is a name. Eighty characters is generous for one - some are genuine questions, like
      // "When should the sender be told we have the message?" - and still far short of the paragraph of
      // advice that appears when a hint has been fused into the label.
      if (text.length > 80) out.push(text.slice(0, 120));
    }
    return out;
  });

  expect(
    tooLong,
    `These labels contain more than a name, so they become part of the field's accessible name:\n  ${tooLong.join("\n  ")}\n\n` +
      `Put the explanation in aria-describedby instead.`,
  ).toEqual([]);
});
