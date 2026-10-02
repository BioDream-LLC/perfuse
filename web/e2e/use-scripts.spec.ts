import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// The scripts section of the channel builder, operated rather than inspected.
//
// This section did not exist until tonight. The draft model carried two script fields, the emitter wrote
// them, and no control ever set them - so every test passed while the feature was unreachable from the
// interface. That is why the assertions here are all on the generated file: what matters is that typing
// into a box changes what gets written to disk, which is the only thing a control is for.
//
// The three data types the form gained at the same time - delimited, dicom and raw - are exercised here too,
// because they were selectable nowhere and a channel carrying a CSV feed had to be hand-written.

function watch(page: import("@playwright/test").Page): string[] {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`page error: ${e}`));
  page.on("console", (m) => {
    if (m.type() !== "error") return;
    // A 400 from the build endpoint is expected while a required field is empty: the panel asks the server
    // whether the draft can be written yet and shows the answer.
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

/** generated waits for the preview to contain what was typed, or for the panel to explain why it cannot. */
async function generated(page: import("@playwright/test").Page): Promise<string> {
  const preview = page.locator("pre").first();
  await expect(preview).toBeVisible({ timeout: 15_000 });
  return (await preview.textContent()) ?? "";
}

test("a preprocessor typed into the form reaches the channel file", async ({ page }) => {
  const problems = watch(page);
  await openNewChannelForm(page);

  await page.getByLabel("Channel name", { exact: true }).fill("script-qa");

  const preprocessor = page.getByLabel("Preprocessor");
  await expect(preprocessor).toBeVisible();
  await preprocessor.fill("return message.replace('A', 'B');");

  await expect
    .poll(async () => await generated(page), { timeout: 15_000 })
    .toContain("preprocessor");

  const file = await generated(page);
  expect(file).toContain("scripts:");
  expect(file).toContain("replace('A', 'B')");

  expect(problems).toEqual([]);
});

test("every script slot writes its own key", async ({ page }) => {
  const problems = watch(page);
  await openNewChannelForm(page);

  await page.getByLabel("Channel name", { exact: true }).fill("all-slots");

  // Each slot gets a marker containing its own name, so a field wired to the wrong key is visible as a
  // marker under the wrong heading rather than as a missing one.
  const slots: [string, string][] = [
    ["Preprocessor", "preprocessor"],
    ["Filter", "filter"],
    ["Transformer", "transformer"],
    ["Postprocessor", "postprocessor"],
    ["On start", "deploy"],
    ["On stop", "undeploy"],
  ];

  for (const [label, key] of slots) {
    await page.getByLabel(label, { exact: true }).fill(`/* ${key}-marker */`);
  }

  await expect.poll(async () => await generated(page), { timeout: 15_000 }).toContain("undeploy");

  const file = await generated(page);
  for (const [, key] of slots) {
    // The key must be present and its marker must be the one belonging to it. Checking only that both
    // strings appear would pass if two fields wrote to the same key.
    expect(file, `${key} is missing from the generated file`).toContain(key);
    expect(file, `${key} does not carry its own marker`).toContain(`${key}-marker`);
  }

  expect(problems).toEqual([]);
});

test("choosing Lua writes the language and choosing JavaScript does not", async ({ page }) => {
  const problems = watch(page);
  await openNewChannelForm(page);

  await page.getByLabel("Channel name", { exact: true }).fill("lang-qa");
  await page.getByLabel("Filter", { exact: true }).fill("return true");

  // JavaScript is the loader's default, so it must not appear. A form that writes the default adds a line
  // nobody typed, and a diff full of those makes the next reviewer distrust all of it.
  await expect.poll(async () => await generated(page), { timeout: 15_000 }).toContain("filter");
  expect(await generated(page)).not.toContain("language:");

  await page.getByLabel("Language", { exact: true }).selectOption("lua");

  await expect.poll(async () => await generated(page), { timeout: 15_000 }).toContain("language: lua");

  expect(problems).toEqual([]);
});

test("a filter script is explained rather than hidden on a format that cannot run one", async ({
  page,
}) => {
  const problems = watch(page);
  await openNewChannelForm(page);

  // This test used to name X12 as the format that cannot run a filter script, because the form said so. X12 runs
  // both slots - through the generic path stage, in either language, verified against delivered bytes - so the test
  // was asserting a false claim and would have kept it alive. Second time this week an existing test defended a bug.
  //
  // Raw is the honest example: it has no addressable structure, so there is nothing for a filter to read.
  await page.getByLabel("Channel name", { exact: true }).fill("raw-scripts");
  await page.getByLabel("Message format", { exact: true }).selectOption("raw");

  // The filter box stays visible with the reason underneath. Hiding it would leave somebody wondering
  // whether they had misremembered where the setting was.
  //
  // Both the filter and the transformer carry the same explanation, so the assertion has to say which one. An
  // unqualified match resolves to two elements and fails on strict mode rather than on the product, which is a
  // test bug wearing a product bug's clothes.
  await expect(
    page.getByText("A raw channel has no addressable structure", { exact: false }).first(),
  ).toBeVisible();
  await expect(
    page.getByText("a transformer script would have nothing to read", { exact: false }).first(),
  ).toBeVisible();

  // The preprocessor is still offered, because it reads text before parsing and that is exactly what an
  // interchange needing repair requires.
  await expect(page.getByLabel("Preprocessor", { exact: true })).toBeVisible();

  expect(problems).toEqual([]);
});

test("the three data types the form was missing can be chosen and written", async ({ page }) => {
  const problems = watch(page);

  for (const dataType of ["delimited", "dicom", "raw"]) {
    await openNewChannelForm(page);
    await page.getByLabel("Channel name", { exact: true }).fill(`type-${dataType}`);
    await page.getByLabel("Message format", { exact: true }).selectOption(dataType);

    await expect
      .poll(async () => await generated(page), { timeout: 15_000 })
      .toContain(`dataType: ${dataType}`);
  }

  expect(problems).toEqual([]);
});

test("a preprocessor is refused on an imaging channel, with the reason", async ({ page }) => {
  const problems = watch(page);
  await openNewChannelForm(page);

  await page.getByLabel("Channel name", { exact: true }).fill("dicom-pre");
  await page.getByLabel("Message format", { exact: true }).selectOption("dicom");

  // Editing a binary object as text corrupts the image, which is the same reason DICOM gets named
  // transformation steps rather than a general path writer.
  //
  // first() because two places explain this now - the steps section's description and the script field's hint - and
  // both are wanted. The assertion is that a person choosing DICOM is told why, not that exactly one element says so.
  await expect(page.getByText("corrupt the image", { exact: false }).first()).toBeVisible();

  expect(problems).toEqual([]);
});
