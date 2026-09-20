import { expect, test } from "@playwright/test";
import { openTab } from "./nav";

/**
 * Acting on a mapping suggestion.
 *
 * The panel used to have one button. Somebody read "MSH-4 to Organization.name, 87%" and typed it into the builder themselves, which
 * is where a transcription error enters a mapping that was suggested correctly.
 *
 * The property most of this guards is the abstention design. The engine declining is the feature - a confident wrong mapping in a
 * clinical system is worse than no mapping - and the way to lose it is to make approval convenient.
 */

async function suggest(page: import("@playwright/test").Page, sources: string, targets: string) {
  await page.goto("/");
  await openTab(page, "AI Mapper");

  const boxes = page.getByRole("textbox");
  await boxes.nth(0).fill(sources);
  await boxes.nth(1).fill(targets);

  await page.getByRole("button", { name: /Suggest/i }).click();
}

test("a confident suggestion can be approved and added to a channel", async ({ page }) => {
  // Deliberately a pair the engine should be confident about: the same name in two notations.
  // Two things have to hold for a suggestion to be applicable as a step, and both were learned the hard way.
  //
  // It needs example values: on the name alone, an exact match reaches only sixty-eight and abstains at the default threshold, so the
  // panel could not reach the confident path at all before. And the source has to be a path in the message, because a step copies
  // between two places inside one message - a vendor's field name is not one, and belongs in a recipe instead.
  await suggest(page, "ZPI-1 = MRN00412, MRN00998", "PID-3.1 = mrn");

  const approve = page.getByRole("checkbox", { name: /Approve this mapping/ }).first();
  await expect(approve).toBeVisible({ timeout: 20_000 });

  // Nothing is approved until a box is ticked, and the button says so rather than being silently inert.
  await expect(page.getByText(/Nothing approved yet/)).toBeVisible();
  await expect(page.getByRole("button", { name: "Add these steps" })).toBeDisabled();

  await approve.check();

  // The count is stated. Somebody who ticks one box and reads "1 mapping approved" knows what is about to happen; a screen that
  // implied it would leave them to guess.
  await expect(page.getByText(/1 mapping approved/)).toBeVisible();

  // A channel is still needed, because approving a mapping and applying it are different decisions.
  await expect(page.getByRole("button", { name: "Add these steps" })).toBeDisabled();
  await expect(page.getByText(/Choose a channel first/)).toBeVisible();

  const channel = page.getByRole("combobox", { name: /Add them to/ });
  const options = await channel.locator("option").allTextContents();
  const pick = options.find((o) => o !== "choose a channel…");

  expect(pick, "there are no channels to add steps to, so this test proves nothing").toBeTruthy();
  await channel.selectOption({ label: pick as string });

  await page.getByRole("button", { name: "Add these steps" }).click();

  // The outcome, and the part that stops somebody watching traffic for a change that cannot appear yet.
  // Asserted on the page rather than on a role, and reported with what the screen actually said - a failure here is either the apply
  // being refused or the confirmation not rendering, and those need different fixes.
  const body = page.locator("body");

  await expect(async () => {
    const text = (await body.textContent()) ?? "";
    expect(text.replace(/\s+/g, ' '), `screen: ${text.replace(/\s+/g, ' ').slice(400, 1100)}`).toMatch(/step\(s\) added to/);
  }).toPass({ timeout: 20_000, intervals: [300, 500, 1000] });

  await expect(body, "the confirmation does not say the steps are not running yet").toContainText(/next loads/);
});

test("an abstention offers no way to approve it", async ({ page }) => {
  // Date of birth against date of death: near-miss names, both dates, so string similarity rates them highly and the engine has to
  // decline. This is the pair the danger tests in internal/mapper are built around.
  await suggest(page, "DateOfBirth", "DateOfDeath");

  const main = page.locator("body");

  await expect(main).toContainText(/abstained|abstained entirely/i, { timeout: 20_000 });

  // The assertion that matters: no approval control anywhere on the screen. Not a disabled one - none, because a disabled checkbox
  // beside a suggestion still reads as something that could be enabled.
  await expect(page.getByRole("checkbox", { name: /Approve this mapping/ })).toHaveCount(0);

  // And it says why, in terms of what the engine knows rather than what the interface will not let you do.
  await expect(main).toContainText(/does not know/i);
});

test("the panel as it opens can produce an approvable suggestion", async ({ page }) => {
  // The guard for a claim I made without checking it.
  //
  // The panel shipped abstaining on all thirty-two pairs of its own defaults: it sent no example values and no target kinds, so
  // confidence rested on comparing a descriptive name against a literal path like PID-7, and nothing ever cleared the threshold. The
  // engine's confident path was unreachable from the interface, and the screen's whole purpose was invisible on first use.
  //
  // Nothing is typed here deliberately. A test that fills the boxes tests the input it chose, which is exactly how the first claim
  // came to be wrong.
  await page.goto("/");
  await openTab(page, "AI Mapper");

  await page.getByRole("button", { name: /Suggest/i }).click();

  const approve = page.getByRole("checkbox", { name: /Approve this mapping/ });
  await expect(approve.first(), "the panel abstains on its own defaults, so it looks broken on first use").toBeVisible({
    timeout: 20_000,
  });

  // Why it worked, shown on screen. The engine explains that PID-7 is the date of birth rather than only scoring it, which is what
  // makes a suggestion reviewable instead of merely presented.
  await expect(page.locator("body")).toContainText(/PID-7 is Date\/Time of Birth/);
});

test("a suggestion below the threshold is shown but cannot be approved", async ({ page }) => {
  // A third state between confident and abstained, and one the interface got wrong at first.
  //
  // Abstained is a property of the source field, not of one suggestion: it is set on all of them only when the best is below the
  // threshold. Once the best clears it, every weaker suggestion for that field carries abstained false - so a 46 arrived looking as
  // endorsed as a 77, with an approval box beside it.
  await page.goto("/");
  await openTab(page, "AI Mapper");

  await page.getByRole("button", { name: /Suggest/i }).click();

  await expect(page.getByText(/Below your threshold of 70%/).first()).toBeVisible({ timeout: 20_000 });

  // Shown for context rather than hidden, because a near miss is worth reading - it is often the mapping somebody wanted, and
  // hiding it would leave them wondering whether the engine considered it.
  const approvable = await page.getByRole("checkbox", { name: /Approve this mapping/ }).count();
  const shownForContext = await page.getByText(/Below your threshold/).count();

  expect(shownForContext, "no below-threshold suggestion was rendered, so this test proves nothing").toBeGreaterThan(0);
  expect(approvable, "every suggestion is approvable, so the threshold is not gating anything").toBeLessThan(
    approvable + shownForContext,
  );
});
