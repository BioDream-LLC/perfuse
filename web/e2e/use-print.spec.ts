import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Rendering a clinical document to PDF, which is what gets printed, faxed and filed.
//
// The test that matters is not "a file downloaded". It is that the file is a PDF a reader will open, and that a
// section nobody attested is not quietly filled in from the codes on a page that will be read years later.

const WITH_NARRATIVE = `<?xml version="1.0"?>
<ClinicalDocument xmlns="urn:hl7-org:v3">
  <templateId root="2.16.840.1.113883.10.20.22.1.1"/>
  <templateId root="2.16.840.1.113883.10.20.22.1.2"/>
  <code code="34133-9" codeSystem="2.16.840.1.113883.6.1" displayName="Summarization of Episode Note"/>
  <title>Continuity of Care Document</title>
  <effectiveTime value="20260818120000-0500"/>
  <recordTarget><patientRole>
    <id root="2.16.840.1.113883.19.5" extension="P1"/>
    <patient><name><given>Ada</given><family>Lovelace</family></name>
    <administrativeGenderCode code="F"/><birthTime value="19151210"/></patient>
  </patientRole></recordTarget>
  <custodian><assignedCustodian><representedCustodianOrganization>
    <name>Example Hospital</name>
  </representedCustodianOrganization></assignedCustodian></custodian>
  <component><structuredBody>
    <component><section>
      <templateId root="2.16.840.1.113883.10.20.22.2.1.1"/>
      <code code="10160-0" codeSystem="2.16.840.1.113883.6.1"/>
      <title>Medications</title>
      <text>Patient takes metformin 500 MG twice daily with food.</text>
    </section></component>
  </structuredBody></component>
</ClinicalDocument>`;

test("a document renders to a PDF a reader will open", async ({ page }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`uncaught exception: ${e.message}`));

  await page.goto("/");
  await openTab(page, "Documents");

  const box = page.locator("main textarea").first();
  await expect(box).toBeVisible({ timeout: 25_000 });
  await box.fill(WITH_NARRATIVE);
  await page.getByRole("button", { name: "Read the document" }).click();

  const tab = page.locator("main").getByRole("button", { name: /^Print$/ });
  await expect(tab, "there is no way to print a document").toBeVisible({ timeout: 25_000 });
  await tab.click();

  const download = page.waitForEvent("download", { timeout: 25_000 });
  await page.getByRole("button", { name: /Render as PDF/ }).click();
  const file = await download;

  // The filename has to identify the patient. A folder of document-1.pdf through document-40.pdf is a folder
  // nobody can use six months later.
  expect(file.suggestedFilename(), "the filename does not identify the document").toMatch(/lovelace/i);
  expect(file.suggestedFilename()).toMatch(/\.pdf$/);

  // And the bytes have to be a PDF. A download that produces a file a reader rejects is worse than no download,
  // because the failure surfaces on somebody else's desk.
  const path = await file.path();
  expect(path, "the download produced no file").toBeTruthy();

  const fs = await import("node:fs/promises");
  const head = (await fs.readFile(path as string)).subarray(0, 8).toString("latin1");
  expect(head, `the downloaded file begins ${JSON.stringify(head)} rather than being a PDF`).toContain("%PDF");

  // Said on screen too, because a download the browser handles silently leaves somebody unsure the button worked.
  await expect(page.locator("main")).toContainText(/Saved /i);

  expect(problems, problems.join("\n")).toEqual([]);
});

test("printing explains that unattested sections are not filled in", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Documents");

  const box = page.locator("main textarea").first();
  await expect(box).toBeVisible({ timeout: 25_000 });
  await box.fill(WITH_NARRATIVE);
  await page.getByRole("button", { name: "Read the document" }).click();

  await page.locator("main").getByRole("button", { name: /^Print$/ }).click();

  // The reasoning belongs on screen, not only in a commit message. Somebody deciding whether to hand this printout
  // to a clinician needs to know what it does and does not contain.
  await expect(page.locator("main")).toContainText(/narrative/i, { timeout: 15_000 });
  await expect(page.locator("main")).toContainText(/attested|approved/i);
});
