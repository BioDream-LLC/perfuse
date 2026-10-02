import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Combining several documents into one view.
//
// A patient seen at three facilities means three summaries, each claiming to describe the same person's medications.
// Working out the union by hand under time pressure is the task that goes wrong.
//
// The dangerous failure is merging two people's lists into one that belongs to neither, which looks entirely plausible.
// So that is what these tests are mostly about.

function medsDoc(mrn: string, meds: Array<[string, string]>): string {
  const entries = meds
    .map(
      ([code, name]) => `
      <entry><substanceAdministration classCode="SBADM" moodCode="EVN">
        <statusCode code="active"/>
        <consumable><manufacturedProduct><manufacturedMaterial>
          <code code="${code}" codeSystem="2.16.840.1.113883.6.88" displayName="${name}"/>
        </manufacturedMaterial></manufacturedProduct></consumable>
      </substanceAdministration></entry>`,
    )
    .join("");

  return `<?xml version="1.0"?>
<ClinicalDocument xmlns="urn:hl7-org:v3">
  <templateId root="2.16.840.1.113883.10.20.22.1.1"/>
  <templateId root="2.16.840.1.113883.10.20.22.1.2"/>
  <code code="34133-9" codeSystem="2.16.840.1.113883.6.1"/>
  <title>Continuity of Care Document</title>
  <effectiveTime value="20260818120000-0500"/>
  <recordTarget><patientRole>
    <id root="2.16.840.1.113883.19.5" extension="${mrn}"/>
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
      <text>See coded entries.</text>${entries}
    </section></component>
  </structuredBody></component>
</ClinicalDocument>`;
}

async function combine(page: import("@playwright/test").Page, first: string, second: string) {
  await page.goto("/");
  await openTab(page, "Documents");

  const box = page.locator("main textarea").first();
  await expect(box).toBeVisible({ timeout: 25_000 });
  await box.fill(first);
  await page.getByRole("button", { name: "Read the document" }).click();

  const tab = page.locator("main").getByRole("button", { name: /Combine sources/ });
  await expect(tab, "there is no way to combine several documents").toBeVisible({ timeout: 25_000 });
  await tab.click();

  await page.getByLabel(/Source 2/).fill(second);
  await page.getByRole("button", { name: /Show what they all say/ }).click();
}

test("two summaries for one patient combine into the union of their medications", async ({ page }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`uncaught exception: ${e.message}`));

  await combine(
    page,
    medsDoc("MRN-1", [["860975", "metformin 500 MG"]]),
    medsDoc("MRN-1", [["855332", "warfarin sodium 5 MG"]]),
  );

  const main = page.locator("main");
  await expect(main).toContainText(/same patient/i, { timeout: 25_000 });

  // Both drugs must be present. Either alone is the failure this exists to prevent.
  await expect(main).toContainText(/metformin/i);
  await expect(main).toContainText(/warfarin/i);

  // And it must say what it is not, because a merged view that looks like a record and was authored by nobody is the
  // worst possible artefact.
  await expect(main, "the merged view does not say what it is not").toContainText(/not a clinical record/i);

  expect(problems, problems.join("\n")).toEqual([]);
});

test("combining two different patients is reported before anything else", async ({ page }) => {
  await combine(
    page,
    medsDoc("MRN-1", [["860975", "metformin 500 MG"]]),
    medsDoc("MRN-2", [["855332", "warfarin sodium 5 MG"]]),
  );

  const main = page.locator("main");
  await expect(main, "nothing warned that the sources describe different people").toContainText(
    /may describe a different person/i,
    { timeout: 25_000 },
  );

  // Which source, because with several sources "one of these" is useless.
  await expect(main).toContainText(/Document two/i);
});
