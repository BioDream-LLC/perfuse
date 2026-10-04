import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// US Core claims in the FHIR lab: an admission with a diagnosis and an allergy converts to resources that each claim the US Core 9.0.0
// profile the server checked them against - the same conversions the HL7 validator passed with no errors - and one that cannot
// conform is shown without a claim rather than with a false one.
test("the FHIR lab shows which resources claim US Core, after checking each", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "FHIR lab");
  const adt = [
    "MSH|^~\\&|EHR|SPRINGFIELD|PERFUSE|HIE|20260410093000-0500||ADT^A01^ADT_A01|MSG1|P|2.5.1",
    "PID|1||MRN48213^^^SPRINGFIELD^MR||EXAMPLE^ALEX||19700101|F||2106-3^White^CDCREC|123 MAIN ST^^SPRINGFIELD^IL^62701^USA^H||^PRN^PH^^1^217^5550100||eng^English^ISO639",
    "PV1|1|I|4W^401^A^SPRINGFIELD||||1234567893^CLINICIAN^PAT^^^^^^NPI&2.16.840.1.113883.4.6&ISO|||MED|||||||||VN1^^^SPRINGFIELD^VN|||||||||||||||||||||20260410093000-0500",
    "DG1|1||J18.9^Pneumonia, unspecified organism^I10||20260410|A",
    "AL1|1|DA|70618^Penicillin^RXNORM|SV|Hives",
  ].join("\n");
  await page.getByLabel("HL7 v2 message to convert").fill(adt);
  await page.getByText("Claim US Core profiles on the output").click();
  await page.getByRole("button", { name: "Convert to FHIR" }).click();

  const claims = page.getByTestId("us-core-claims");
  await expect(claims).toContainText(/Patient\/\S+: claims us-core-patient/);
  await expect(claims).toContainText(/Condition\/\S+: claims us-core-condition-encounter-diagnosis/);
  await expect(claims).toContainText(/AllergyIntolerance\/\S+: claims us-core-allergyintolerance/);
  await expect(claims).toContainText(/Location\/\S+: claims us-core-location/);
  // PV1 here has no admission or patient type, so the Encounter has no type, and US Core requires one.
  await expect(claims).toContainText(/Encounter\/\S+: no claim; see the notes/);
});

// An awkward update posted on chat.fhir.org: an A08 carrying MRG (which the ADT_A01 structure has no place for), a time of birth
// and a Z segment. Each used to vanish without a note; the Decisions tab now says what was and was not done.
test("the FHIR lab reports what it could not map in an awkward message", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "FHIR lab");
  const a08 = [
    "MSH|^~\\&|PAS|RIVERLAND|GW|RCVFAC|20260918060000+1000||ADT^A08^ADT_A01|TEST002|P|2.5",
    "EVN|A08|20260918055900+1000|||OP12345",
    "PID|1||441122^^^RIVERLAND^MR~3950000000^^^AUSHIC&2.16.840.1.113883.3.879&ISO^MC||TESTPATIENT^ALEX^^^MS^^L~NGUYEN^ALEX^^^^^B||19800101120000|F",
    "MRG|998877^^^RIVERLAND^MR",
    "PV1|1|I|WARD3^BED2^RAH||||||||||||||||VN0099|||||||||||||||||||||||||20260910080000+1000|20260917",
    "ZPD|1|INTERPRETER|AUSLAN|REQUIRED",
  ].join("\n");
  await page.getByLabel("HL7 v2 message to convert").fill(a08);
  await page.getByRole("button", { name: "Convert to FHIR" }).click();
  await expect(page.getByText('"resourceType": "Patient"').first()).toBeVisible();
  await page.getByRole("button", { name: /^Decisions/ }).click();
  await expect(page.getByText(/prior identifier "998877" is still live/)).toBeVisible();
  await expect(page.getByText(/ZPD is a site-defined Z segment/)).toBeVisible();
  await expect(page.getByText(/patient-birthTime extension/)).toBeVisible();
  await expect(page.getByText(/urn:oid:2\.16\.840\.1\.113883\.3\.879 was used/)).toBeVisible();
});
