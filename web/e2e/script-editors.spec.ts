import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// The builder's script boxes are syntax-highlighting editors.
//
// Six plain textareas became six CodeMirror editors. Three things about that can only be shown in a real browser: that the editors
// mount at all, that they tokenise (which is the one thing a textarea cannot do), and that what somebody types survives the trip into
// the generated file.
//
// The last of those is not paranoia. Swapping the control exposed a lost-update bug in the builder that textareas had been hiding:
// the editors report changes from outside React's event handling, where updates are not batched, so patches built from a stale draft
// overwrote each other. Every box showed the right text and the file kept one of them. A test that only checked the boxes looked
// right would have passed.

async function openScriptsSection(page: import("@playwright/test").Page) {
  await page.goto("/");
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();
  await page.getByRole("button", { name: "Start from nothing" }).click();
}

test("every script slot is a named editor rather than a textarea", async ({ page }) => {
  await openScriptsSection(page);

  // A CodeMirror editor is a div carrying role=textbox. It cannot be associated with a surrounding label the way a textarea can,
  // so each one is named directly - otherwise this page presents six anonymous edit boxes.
  const editors = page.locator('[role="textbox"][aria-label]');
  await expect(editors).toHaveCount(6);

  expect(await editors.evaluateAll((els) => els.map((e) => e.getAttribute("aria-label")))).toEqual([
    "Preprocessor",
    "Filter",
    "Transformer",
    "Postprocessor",
    "On start",
    "On stop",
  ]);

  // No textarea should be left in the scripts section.
  await expect(page.locator('textarea[aria-label="Preprocessor"]')).toHaveCount(0);
});

test("a script is highlighted as it is typed", async ({ page }) => {
  await openScriptsSection(page);

  const pre = page.getByLabel("Preprocessor", { exact: true });
  await pre.click();
  await page.keyboard.type('var greeting = "hello"');

  // Tokenising splits the line into spans. A textarea holds one flat string, so a count above one is the proof that highlighting
  // is actually running rather than that a stylesheet was loaded.
  const spans = pre.locator("span");
  expect(await spans.count()).toBeGreaterThan(1);

  // And the text is really there, not merely painted.
  await expect(pre).toContainText("greeting");
});

test("a script typed into the builder reaches the generated file", async ({ page }) => {
  await openScriptsSection(page);
  await page.getByLabel("Channel name", { exact: true }).fill("typed-script");

  const pre = page.getByLabel("Preprocessor", { exact: true });
  await pre.click();
  await page.keyboard.type("/* typed-by-hand */");

  await expect
    .poll(async () => await page.locator("pre").first().innerText(), { timeout: 15_000 })
    .toContain("typed-by-hand");
});
