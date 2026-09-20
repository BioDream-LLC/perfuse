import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Declarative steps on a SCRIPT channel, operated rather than inspected.
//
// The step editor already existed for v3. A prescription is XML addressed by the same path grammar, so it
// serves both - but the block it writes differs, and the server refuses an hl7v3 block on a SCRIPT channel.
// So every assertion here is on the generated file: a section that renders and writes the wrong key would
// pass any test that only checked the editor was visible.
//
// One action does not carry over. nullflavor states why a v3 value is absent and a prescription has no
// equivalent, so the server refuses it - and the form withholds it rather than offering a control that
// produces a file which cannot load.

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

test("a declarative step typed into the form reaches a prescription channel file", async ({
  page,
}) => {
  const problems = watch(page);
  await openNewChannelForm(page);

  await page.getByLabel("Channel name", { exact: true }).fill("rx-steps");
  await page.getByLabel("Message format").selectOption("script");

  await page.getByRole("button", { name: "+ Set a value" }).click();

  await page.getByLabel(/path/i).first().fill("//DrugDescription");
  await page.getByLabel(/value/i).first().fill("REDACTED");

  await expect
    .poll(async () => await generated(page), { timeout: 15_000 })
    .toContain("DrugDescription");

  const file = await generated(page);

  // The block the server expects for this data type.
  expect(file).toContain("script:");
  expect(file).toContain("transformations:");
  expect(file).toContain("REDACTED");

  // And not the one it refuses. A SCRIPT channel carrying an hl7v3 block will not load, so a form that
  // wrote one would be producing files that fail at deploy rather than in the editor.
  expect(file).not.toContain("hl7v3:");

  expect(problems).toEqual([]);
});

test("the nullflavor action is withheld on a prescription channel", async ({ page }) => {
  const problems = watch(page);
  await openNewChannelForm(page);

  await page.getByLabel("Channel name", { exact: true }).fill("rx-nullflavor");
  await page.getByLabel("Message format").selectOption("script");

  // A step is added by clicking the button for the kind wanted, so withholding an action means
  // withholding its button.
  await expect(page.getByRole("button", { name: "+ Say why there is no value" })).toHaveCount(0);

  // The actions that do carry over are still offered, or this would pass against an empty editor.
  await expect(page.getByRole("button", { name: "+ Set a value" })).toHaveCount(1);
  await expect(page.getByRole("button", { name: "+ Remove the element" })).toHaveCount(1);

  // And the per-step dropdown must agree with the buttons, or a step added as one kind could be
  // switched to the withheld one afterwards.
  await page.getByRole("button", { name: "+ Set a value" }).click();

  const kind = page.getByLabel("What this step does");
  await expect(kind.locator('option[value="nullflavor"]')).toHaveCount(0);
  await expect(kind.locator('option[value="set"]')).toHaveCount(1);

  expect(problems).toEqual([]);
});

test("a v3 channel still offers nullflavor", async ({ page }) => {
  const problems = watch(page);
  await openNewChannelForm(page);

  await page.getByLabel("Channel name", { exact: true }).fill("v3-nullflavor");
  await page.getByLabel("Message format").selectOption("hl7v3");

  // The regression half: filtering the list for SCRIPT must not remove it everywhere.
  await expect(page.getByRole("button", { name: "+ Say why there is no value" })).toHaveCount(1);

  await page.getByRole("button", { name: "+ Set a value" }).click();
  await expect(
    page.getByLabel("What this step does").locator('option[value="nullflavor"]'),
  ).toHaveCount(1);

  expect(problems).toEqual([]);
});
