import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// HL7, FHIR, XML, YAML and scripts are shown coloured wherever they appear.
//
// The two things the Playground exists to demonstrate - a message and a Mirth script - were plain grey text, and so was the generated
// channel file, which is the thing the builder is for. Colour is drawn by one tokenizer, behind a real textarea where the content is
// editable and in a pre where it is not.

/** Counts the coloured spans in a box, whether it is editable or read-only. */
async function colouredSpans(page: import("@playwright/test").Page, label: string): Promise<number> {
  const box = page.getByLabel(label).locator("xpath=..");

  return await box.locator('[data-code-shadow] span').count();
}

test("the playground colours the message, the script and the filter", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Playground");

  await page.getByRole("button", { name: "Run a Mirth script" }).click();
  expect(await colouredSpans(page, "The message"), "the HL7 message is not coloured").toBeGreaterThan(3);
  expect(await colouredSpans(page, "The script"), "the Mirth script is not coloured").toBeGreaterThan(3);

  await page.getByRole("button", { name: "Try a filter" }).click();
  expect(await colouredSpans(page, "The filter"), "the filter expression is not coloured").toBeGreaterThan(1);
});

test("the generated channel file is coloured", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();
  await page.getByRole("button", { name: "Start from nothing" }).click();
  await page.getByLabel("Channel name", { exact: true }).fill("coloured-yaml");

  // The preview is what every other test reads as plain text, so it still has to be a pre containing the file.
  const preview = page.locator("pre").first();
  await expect.poll(async () => await preview.innerText(), { timeout: 15_000 }).toContain("coloured-yaml");

  // And now it has colour: keys, values and comments in separate spans.
  expect(await preview.locator("span").count(), "the generated YAML is not coloured").toBeGreaterThan(5);
});

test("an HL7 message pasted for conversion is coloured", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "FHIR lab");

  const box = page.getByLabel("HL7 v2 message to convert");
  await box.fill("MSH|^~\\&|A|B|C|D|20260101||ADT^A01|1|P|2.5.1\rPID|1||100294^^^WESTGEN^MR\r");

  const spans = box.locator("xpath=..").locator("[data-code-shadow] span");
  expect(await spans.count(), "the pasted HL7 is not coloured").toBeGreaterThan(5);

  // The text is still exactly what was typed - the coloured layer must not alter it.
  await expect(box).toHaveValue(/ADT\^A01/);
});
