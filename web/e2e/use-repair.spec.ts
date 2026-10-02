import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// The document that is valid, transferred successfully, and blank on screen.
//
// A section with coded entries and no narrative passes the schema, passes the receiver's import, is counted as a
// successful exchange by both ends, and displays as nothing - because almost every viewer renders the narrative and
// ignores the entries. The medication list is in the file and invisible, and no error is raised anywhere.
//
// Two active prescriptions here, one of them warfarin, which is exactly the drug you do not want missing from a
// list somebody is about to prescribe against.

const INVISIBLE = `<?xml version="1.0"?>
<ClinicalDocument xmlns="urn:hl7-org:v3">
  <templateId root="2.16.840.1.113883.10.20.22.1.1"/>
  <templateId root="2.16.840.1.113883.10.20.22.1.2"/>
  <code code="34133-9" codeSystem="2.16.840.1.113883.6.1"/>
  <title>Continuity of Care Document</title>
  <effectiveTime value="20260818120000-0500"/>
  <recordTarget><patientRole><id root="2.16.840.1.113883.19.5" extension="P1"/>
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
      <text/>
      <entry><substanceAdministration classCode="SBADM" moodCode="EVN">
        <statusCode code="active"/>
        <consumable><manufacturedProduct><manufacturedMaterial>
          <code code="860975" codeSystem="2.16.840.1.113883.6.88" displayName="metformin 500 MG"/>
        </manufacturedMaterial></manufacturedProduct></consumable>
      </substanceAdministration></entry>
      <entry><substanceAdministration classCode="SBADM" moodCode="EVN">
        <statusCode code="active"/>
        <consumable><manufacturedProduct><manufacturedMaterial>
          <code code="855332" codeSystem="2.16.840.1.113883.6.88" displayName="warfarin sodium 5 MG"/>
        </manufacturedMaterial></manufacturedProduct></consumable>
      </substanceAdministration></entry>
    </section></component>
  </structuredBody></component>
</ClinicalDocument>`;

test("a document whose medications would display blank says so, and repairs itself", async ({ page }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`uncaught exception: ${e.message}`));

  await page.goto("/");
  await openTab(page, "Documents");

  const box = page.locator("main textarea").first();
  await expect(box).toBeVisible({ timeout: 25_000 });
  await box.fill(INVISIBLE);
  await page.getByRole("button", { name: "Read the document" }).click();

  const tab = page.locator("main").getByRole("button", { name: /What a reader sees/ });
  await expect(tab, "there is no way to find out what a reader would see").toBeVisible({ timeout: 25_000 });
  await tab.click();

  await page.getByRole("button", { name: /Check what a reader would see/ }).click();

  // The count is the finding: two coded facts nobody would have seen.
  await expect(page.locator("main"), "the invisible entries were not counted").toContainText(
    /2 coded facts no reader would have seen/i,
    { timeout: 25_000 },
  );

  // And the repair has to actually produce the drug names as narrative. Finding "warfarin" anywhere would prove
  // nothing - it was already in the coded entry, which is the part viewers ignore. So this checks the repaired
  // document is offered at all, then that it carries the name outside an entry element.
  const repaired = page.locator("main pre").last();
  await expect(repaired, "no repaired document was offered").toBeVisible({ timeout: 15_000 });
  await expect(repaired).toContainText(/warfarin/i);

  expect(problems, problems.join("\n")).toEqual([]);
});

test("a document that already displays properly is not reported as broken", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Documents");

  const sample = page.getByRole("button", { name: /Sample document/i });
  await expect(sample).toBeVisible({ timeout: 25_000 });
  await sample.first().click();
  await page.getByRole("button", { name: "Read the document" }).click();

  await page.locator("main").getByRole("button", { name: /What a reader sees/ }).click();
  await page.getByRole("button", { name: /Check what a reader would see/ }).click();

  // A tool that reports every document as broken gets ignored, which costs more than not having it.
  await expect(page.locator("main")).toContainText(/a reader sees all of it|no reader would have seen/i, {
    timeout: 25_000,
  });
  await expect(page.locator("main")).not.toContainText(/coded facts no reader would have seen/i);
});
