import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Two medication lists that disagree about whether a drug was stopped.
//
// Both documents are conformant and internally consistent. They contradict each other about whether the patient is
// currently taking warfarin, and nothing in either file says so. Whichever one the receiving clinician happens to
// read decides what they believe.

function medsDoc(mrn: string, meds: Array<[string, string, string]>): string {
  const entries = meds
    .map(
      ([code, name, status]) => `
      <entry><substanceAdministration classCode="SBADM" moodCode="EVN">
        <statusCode code="${status}"/>
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

async function loadAndCompare(page: import("@playwright/test").Page, later: string, earlier: string) {
  await page.goto("/");
  await openTab(page, "Documents");

  const box = page.locator("main textarea").first();
  await expect(box).toBeVisible({ timeout: 25_000 });
  await box.fill(later);
  await page.getByRole("button", { name: "Read the document" }).click();

  const tab = page.locator("main").getByRole("button", { name: /Compare medications/ });
  await expect(tab, "there is no way to compare two medication lists").toBeVisible({ timeout: 25_000 });
  await tab.click();

  await page.getByLabel(/The earlier document/).fill(earlier);
  await page.getByRole("button", { name: /Compare the lists/ }).click();
}

test("a drug stopped in one document and active in the other is reported as a contradiction", async ({ page }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`uncaught exception: ${e.message}`));

  await loadAndCompare(
    page,
    medsDoc("MRN-1", [["855332", "warfarin sodium 5 MG", "completed"]]),
    medsDoc("MRN-1", [["855332", "warfarin sodium 5 MG", "active"]]),
  );

  await expect(page.locator("main"), "the contradiction was not reported").toContainText(/contradiction/i, {
    timeout: 25_000,
  });
  await expect(page.locator("main")).toContainText(/warfarin/i);

  expect(problems, problems.join("\n")).toEqual([]);
});

test("two different patients are not silently reconciled", async ({ page }) => {
  // A report full of additions and removals looks exactly like one patient whose therapy changed completely, so
  // this has to be said before anything else is believed.
  await loadAndCompare(
    page,
    medsDoc("MRN-2", [["855332", "warfarin sodium 5 MG", "active"]]),
    medsDoc("MRN-1", [["860975", "metformin 500 MG", "active"]]),
  );

  await expect(page.locator("main"), "nothing warned that these are different patients").toContainText(
    /may not be the same patient/i,
    { timeout: 25_000 },
  );
});

test("a started drug is reported as started and not as stopped", async ({ page }) => {
  await loadAndCompare(
    page,
    medsDoc("MRN-1", [
      ["860975", "metformin 500 MG", "active"],
      ["855332", "warfarin sodium 5 MG", "active"],
    ]),
    medsDoc("MRN-1", [["860975", "metformin 500 MG", "active"]]),
  );

  const main = page.locator("main");
  await expect(main).toContainText(/Started \(1\)/, { timeout: 25_000 });

  // A report that is confidently backwards is worse than no report at all.
  await expect(main, "a started drug was also reported as no longer listed").not.toContainText(
    /No longer listed \(1\)/,
  );
});
