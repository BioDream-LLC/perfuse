import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Electronic case reporting in the FHIR lab: a reportable visit becomes an eCR 2.1 eICR - the same builder whose output the HL7
// validator passed against hl7.fhir.us.ecr 2.1.2 with no errors - and a visit with nothing reportable gets none, with the reason.
test("a reportable visit becomes a case report, and the trigger code says why", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "FHIR lab");
  await page.getByRole("button", { name: "Sample reportable visit" }).click();
  await page.getByLabel("Reporting facility facility name").fill("Springfield General Hospital");
  await page.getByLabel("Reporting facility phone").fill("+1-217-555-0100");
  await page.getByLabel("Reporting facility city").fill("Springfield");
  await page.getByLabel("Reporting facility state").fill("IL");
  await page.getByRole("button", { name: "Build the case report" }).click();

  const result = page.getByTestId("eicr-result");
  await expect(result).toContainText("Reportable. Triggered by:");
  await expect(result).toContainText("COVID-19: 840539006 (snomed.info/sct) on Condition");
  await expect(result).toContainText("built-in sample of reportable conditions (not the RCTC)");
  await expect(result).toContainText('"code": "55751-2"');
  await expect(result).toContainText("eicr-trigger-code-flag-extension");
  // The facility was complete, so nothing about it is missing.
  await expect(result).not.toContainText("reporting facility's");

  // The same visit with an asthma diagnosis instead is not reportable, and no report is built.
  const msg = page.getByLabel("HL7 v2 message to convert");
  const text = await msg.inputValue();
  await msg.fill(text.replace("840539006^COVID-19", "195967001^Asthma"));
  await page.getByRole("button", { name: "Build the case report" }).click();
  await expect(result).toContainText("Not reportable: nothing reportable");
  await expect(result).not.toContainText("eICR document bundle");
});
