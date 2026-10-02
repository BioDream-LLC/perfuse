import { test, expect, type Page } from "@playwright/test";
import { openTab } from "./nav";

// What the builder does when the file cannot be built, and what a test is told about it.
//
// The reason this exists. dicom-steps.spec.ts:166 failed once in a full run with "element(s) not found" while looking for the generated
// file preview, and passed four out of four alone. The investigation that followed found the mechanism but could not distinguish the two
// causes, because the trace had been cleaned by the passing re-run.
//
// The mechanism: there is no preview element at all until the first build returns. The panel renders a paragraph instead - either
// "Nothing to show yet" while the debounced build is in flight, or "This cannot be written as a channel file yet" with the server's own
// words when the build was refused. A test asserting on the preview's contents before then is racing a server round-trip while appearing
// to race a render, and the message it produces names neither.
//
// So the two states are made distinguishable here, and asserted, so that the next occurrence explains itself instead of starting another
// investigation. The failing assertion has also been routed through the helper that waits for the preview, which every other assertion in
// that file already used.

/** anImagingDraft fills the builder far enough to have something to build. */
async function anImagingDraft(page: Page, name: string): Promise<void> {
  await page.goto("/");
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();
  await page.getByRole("button", { name: "Start from nothing" }).click();
  await page.getByLabel("Channel name", { exact: true }).fill(name);
  await page.getByLabel("Message format", { exact: true }).selectOption("dicom");
}

test("a refused build says so in the panel rather than leaving it empty", async ({ page }) => {
  // The state that produces no preview element. Forced by refusing the build request, which is the one thing that cannot be arranged by
  // filling the form differently - the server accepts almost any draft and reports problems inside a successful response.
  await page.route("**/api/channels/build", (route) =>
    route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({ error: "the build service is unavailable" }),
    }),
  );

  await anImagingDraft(page, "build-refused");

  // No preview, and the panel says why. Both halves matter: a panel that went blank would leave an operator with a Create channel button
  // under an empty box and nothing to act on, which is the defect the build error was added to fix.
  await expect(page.getByText(/cannot be written as a channel file yet/)).toBeVisible({ timeout: 20_000 });
  await expect(page.locator("pre")).toHaveCount(0);
});

test("a slow build shows nothing to show yet rather than an error", async ({ page }) => {
  // The other state, and the one the flake most likely hit. It has to be distinguishable from a refusal, because the two call for
  // completely different responses: waiting longer, or looking at the server.
  let release: (() => void) | null = null;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });

  await page.route("**/api/channels/build", async (route) => {
    await held;
    await route.continue();
  });

  await anImagingDraft(page, "build-slow");

  // While the build is held there is no preview, and the panel says it is waiting rather than that anything is wrong.
  await expect(page.getByText(/Nothing to show yet/)).toBeVisible({ timeout: 20_000 });
  await expect(page.locator("pre")).toHaveCount(0);

  // Released, the preview arrives. Without this the test would pass against a builder that never built anything.
  release?.();

  await expect(page.locator("pre").first()).toBeVisible({ timeout: 30_000 });
  // dataType rather than a bare dicom:, which is what the generated file actually says. The first version of this looked for the
  // wrong string and failed with the whole file in the message, which is the good failure mode.
  await expect(page.locator("pre").first()).toContainText("dataType: dicom", { timeout: 30_000 });
});
