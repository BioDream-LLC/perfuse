import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Checking a signature on a clinical document.
//
// The property being tested is honesty. A verifier that overstates what it established is worse than none, because a
// signature nobody questions carries more weight than no signature at all - so the report has to separate "the bytes
// are intact" from "we know who signed it" from "we did not check".

const UNSIGNED = `<?xml version="1.0" encoding="UTF-8"?>
<ClinicalDocument xmlns="urn:hl7-org:v3" ID="d1">
  <title>Discharge Summary</title>
  <recordTarget><patientRole><patient><name>Ada Lovelace</name></patient></patientRole></recordTarget>
  <section><title>Medications</title><text>metformin 500 MG twice daily</text></section>
</ClinicalDocument>`;

async function openSignatureTab(page: import("@playwright/test").Page, doc: string) {
  await page.goto("/");
  await openTab(page, "Documents");

  const box = page.locator("main textarea").first();
  await expect(box).toBeVisible({ timeout: 25_000 });
  await box.fill(doc);
  await page.getByRole("button", { name: "Read the document" }).click();

  const tab = page.locator("main").getByRole("button", { name: /^Signature$/ });
  await expect(tab, "there is no way to check a signature").toBeVisible({ timeout: 25_000 });
  await tab.click();
}

test("an unsigned document is reported as unsigned rather than as a failure", async ({ page }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`uncaught exception: ${e.message}`));

  await openSignatureTab(page, UNSIGNED);
  await page.getByRole("button", { name: /^Check it$/ }).click();

  // Most documents are unsigned. Reporting them as failures makes the whole check useless, because everybody learns
  // to ignore it.
  await expect(page.locator("main")).toContainText(/carries no signature/i, { timeout: 25_000 });
  await expect(page.locator("main")).not.toContainText(/does not stand up/i);

  expect(problems, problems.join("\n")).toEqual([]);
});

test("signing then checking reports the capacity and admits what it did not establish", async ({ page }) => {
  await openSignatureTab(page, UNSIGNED);

  // Signing is behind a disclosure because checking is the common action and signing is rare and consequential.
  //
  // Clicked as a summary element rather than by button role: a details/summary pair reports as a group, and asking
  // for a button here times out looking for something that was never there.
  await page.locator("main summary").filter({ hasText: "Sign this document" }).click();

  const identity = page.locator("main");

  // Either this server can sign, or it says why not. Both are acceptable; saying nothing is not.
  await expect(identity).toContainText(/would be signed as|no certificate to sign with/i, { timeout: 25_000 });

  // No conditional skip. The fixture is given a signing certificate precisely so this path runs: an earlier version
  // of this test returned early when the button was absent, which meant it passed against a server that could not
  // sign at all and proved nothing.
  const signButton = page.getByRole("button", { name: /^Sign it$/ });
  await expect(signButton, "this server cannot sign, so the test below would prove nothing").toBeVisible({
    timeout: 25_000,
  });

  await page.getByLabel(/Capacity/).fill("Attending physician responsible for this discharge");
  await signButton.click();

  // The caveat arrives with the signature and is shown beside it, so nobody can use this without meeting it.
  await expect(identity, "no caveat was shown with the signature").toContainText(/does not assert/i, {
    timeout: 25_000,
  });
  await expect(page.getByRole("button", { name: /Download the signed document/ })).toBeVisible();
});

test("the report separates the questions instead of giving one verdict", async ({ page }) => {
  await page.goto("/");

  // Signed through the API with the document text passed in directly.
  //
  // My first version read it back out of the textarea with textContent, which is empty for a textarea whose content
  // was set as a value - so it signed an empty string and the check below had nothing to report.
  const signedXml = await page.evaluate(async (xml) => {
    const res = await fetch("/api/document/sign", {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Perfuse-Request": "1" },
      credentials: "same-origin",
      body: JSON.stringify({ document: xml, referenceId: "d1" }),
    });
    if (!res.ok) return null;
    return ((await res.json()) as { document: string }).document;
  }, UNSIGNED);

  expect(signedXml, "the server would not sign, so the report below would have nothing to describe").toBeTruthy();

  await openSignatureTab(page, signedXml as string);
  await page.getByRole("button", { name: /^Check it$/ }).click();

  const main = page.locator("main");

  // The questions, separately. A single badge cannot distinguish an expired certificate from an altered document,
  // and those need completely different responses.
  await expect(main).toContainText(/The content has not changed/i, { timeout: 25_000 });
  await expect(main).toContainText(/Signed by the key in that certificate/i);
  await expect(main).toContainText(/signing time and capacity are covered/i);

  // Trust reported as not checked rather than as failed, since no trust store was given. Conflating those is how a
  // verifier reports every document as untrusted and teaches everybody to ignore the field.
  await expect(main).toContainText(/Trust was not checked/i);
});
