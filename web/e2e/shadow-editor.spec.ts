import { expect, test } from "@playwright/test";
import { openTab } from "./nav";

/**
 * Starting and stopping a comparison from the interface.
 *
 * The builder's drift guard excused the whole shadow block because it is "configured from the shadow tab". The shadow tab was
 * read-only and there was no endpoint behind it, so the one feature whose purpose is checking a rewrite before it goes live could
 * only be switched on by hand-editing YAML.
 *
 * These specs assert the outcome rather than the form: after using the editor, the screen reports the comparison, and after stopping
 * it, it does not. A form that accepts a choice and writes nothing is what is being guarded against.
 */

test("a comparison can be started and stopped without editing a file", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Shadow");

  const live = page.getByRole("combobox", { name: /Channel that is running/ });
  const candidate = page.getByRole("combobox", { name: /Candidate to compare against/ });

  await expect(live).toBeVisible({ timeout: 20_000 });

  // Two channels are needed, and the harness has several. The candidate list must exclude whichever channel is being configured,
  // because a channel comparing against itself has nothing to compare.
  const liveName = await live.inputValue();
  const options = await candidate.locator("option").allTextContents();

  expect(options, "the channel being configured was offered as its own candidate").not.toContain(liveName);
  expect(options.length, "there is nothing to compare against, so this test proves nothing").toBeGreaterThan(1);

  const pick = options.find((o) => o !== "choose a channel…" && o !== liveName);
  expect(pick, "no candidate available").toBeTruthy();

  await candidate.selectOption({ label: pick as string });

  // A share is a percentage here and a fraction in the file. Fifty is entered to prove the conversion happens on the way through
  // rather than being written as fifty times everything.
  await page.getByRole("spinbutton", { name: /Share of messages to compare/ }).fill("50");
  await page.getByRole("textbox", { name: /Fields to ignore/ }).fill("MSH-7, MSH-10");

  await page.getByRole("button", { name: "Start comparing" }).click();

  // The outcome, not the click. The confirmation names both channels because a comparison between the wrong pair looks identical to
  // the right one until somebody reads the report.
  const status = page.getByRole("status");
  await expect(status).toContainText(new RegExp(`${liveName}.*compared against`), { timeout: 20_000 });

  // The wording has to say it is configured rather than already observing. A comparison begins when the channel next loads, and a
  // screen claiming it is running would be wrong for as long as that takes - which is exactly the sort of small lie that makes
  // somebody distrust the whole report.
  await expect(status).toContainText(/starts observing when the channel next loads/i);
  await expect(status).toContainText(pick as string);

  // And it must now offer stopping rather than starting, because the screen showing a Start button for a running comparison is how
  // somebody sets one up twice.
  await expect(page.getByRole("button", { name: "Stop comparing" })).toBeVisible({ timeout: 20_000 });
  await expect(page.getByRole("button", { name: "Start comparing" })).toHaveCount(0);

  await page.getByRole("button", { name: "Stop comparing" }).click();

  await expect(status).toContainText(/no longer being compared/i, { timeout: 20_000 });
  await expect(status, "stopping the comparison did not say the candidate survives").toContainText(/candidate channel is untouched/i);
  await expect(page.getByRole("button", { name: "Start comparing" })).toBeVisible({ timeout: 20_000 });
});

test("a comparison cannot be started without choosing a candidate", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Shadow");

  // Refused by the control rather than by the server, because the server's message would be correct and would arrive after a click
  // that looked like it should work.
  const start = page.getByRole("button", { name: "Start comparing" });

  await expect(start).toBeDisabled({ timeout: 20_000 });
  await expect(page.getByText(/Choose a candidate first/)).toBeVisible();
});
