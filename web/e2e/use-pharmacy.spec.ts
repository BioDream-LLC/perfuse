import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// The pharmacy formats, built from the form.
//
// NCPDP was the one gap named as out of scope, so this is the test that it is genuinely in: not that the option exists,
// but that choosing it produces a channel the server will load.

async function newChannel(page: import("@playwright/test").Page, name: string) {
  await page.goto("/");
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();
  await page.getByRole("button", { name: "Start from nothing" }).click();
  await page.getByLabel("Channel name", { exact: true }).fill(name);
}

async function chooseFormat(page: import("@playwright/test").Page, label: RegExp) {
  const sel = page.getByLabel(/Message format/);
  await expect(sel).toBeVisible({ timeout: 20_000 });
  const options = await sel.locator("option").allTextContents();
  const match = options.find((o) => label.test(o));
  expect(match, `no message format matching ${label} is offered`).toBeTruthy();
  await sel.selectOption({ label: match as string });
}

for (const { format, writes, name } of [
  { format: /NCPDP — pharmacy claims/, writes: "ncpdp", name: "e2e-claims" },
  { format: /NCPDP SCRIPT — prescriptions/, writes: "script", name: "e2e-rx" },
]) {
  test(`a ${writes} channel can be built and the server accepts it`, async ({ page }) => {
    await newChannel(page, name);
    await chooseFormat(page, format);

    const pre = page.locator("pre").first();
    await expect(pre).toContainText(`dataType: ${writes}`, { timeout: 20_000 });

    // The assertion that matters. A format in the dropdown with nothing behind it writes its name into the file and
    // produces a channel that will not load - which is exactly the defect found in the DICOM options.
    const refused = page.getByRole("alert").filter({ hasText: /would not accept this channel/ });
    if ((await refused.count()) > 0) {
      const text = await refused.first().innerText();
      expect(text, `the server refuses a ${writes} channel`).not.toMatch(/dataType|not supported/);
    }
  });
}

// Choosing a pharmacy format must move the source off MLLP, because the loader refuses that combination.
test("choosing a pharmacy format moves the source off MLLP rather than leaving a channel that cannot load", async ({
  page,
}) => {
  await newChannel(page, "e2e-rx-mllp");

  // MLLP is the default, so this starts from the combination that would fail.
  await chooseFormat(page, /NCPDP SCRIPT/);

  const pre = page.locator("pre").first();
  await expect(pre).toContainText("dataType: script", { timeout: 20_000 });

  // Only the source block. The default destination is also MLLP and that is perfectly valid - a prescription can be
  // forwarded over MLLP to something that speaks it; what the loader refuses is receiving one that way.
  const yaml = (await pre.textContent()) ?? "";
  const sourceBlock = yaml.split(/^destinations:/m)[0] ?? "";
  expect(sourceBlock, "the source was left as MLLP, which the loader refuses for a prescription").not.toMatch(
    /type:\s*mllp/,
  );
});

// The format must explain itself where the choice is made, not in a tooltip.
test("the pharmacy formats say what is and is not done to a message", async ({ page }) => {
  await newChannel(page, "e2e-rx-note");
  await chooseFormat(page, /NCPDP SCRIPT/);

  const note = page.getByText(/never rewritten/i);
  await expect(note, "nothing tells the operator whether prescriptions are edited in transit").toBeVisible({
    timeout: 20_000,
  });
});
