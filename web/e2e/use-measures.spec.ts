import { test, expect } from "@playwright/test";
import { openTab } from "./nav";
import { smartToken } from "./smart";

// Digital quality measures: an example Measure evaluated over this server's FHIR store from the screen, then over pasted
// resources with exact counts, saved to the store, and evaluated by the FHIR endpoint's Measure/$evaluate-measure.

const year = new Date().getFullYear() - 1;

const bp = (id: string, patient: string, sys: number, dia: number) => ({
  resourceType: "Observation", id, status: "final", subject: { reference: `Patient/${patient}` },
  effectiveDateTime: `${year}-05-01T09:00:00Z`,
  code: { coding: [{ system: "http://loinc.org", code: "85354-9" }] },
  component: [
    { code: { coding: [{ system: "http://loinc.org", code: "8480-6" }] }, valueQuantity: { value: sys, unit: "mmHg", system: "http://unitsofmeasure.org", code: "mm[Hg]" } },
    { code: { coding: [{ system: "http://loinc.org", code: "8462-4" }] }, valueQuantity: { value: dia, unit: "mmHg", system: "http://unitsofmeasure.org", code: "mm[Hg]" } },
  ],
});
const htn = (id: string, patient: string) => ({
  resourceType: "Condition", id, subject: { reference: `Patient/${patient}` },
  clinicalStatus: { coding: [{ system: "http://terminology.hl7.org/CodeSystem/condition-clinical", code: "active" }] },
  code: { coding: [{ system: "http://snomed.info/sct", code: "38341003" }] },
});

test("a measure is evaluated over the FHIR store and by $evaluate-measure", async ({ page }) => {
  const faults: string[] = [];
  page.on("pageerror", (e) => faults.push(e.message));
  await page.goto("/");
  const token = await smartToken(page.request);
  const fhir = { Authorization: `Bearer ${token}`, "Content-Type": "application/fhir+json" };
  const resources = [
    { resourceType: "Patient", id: "e2e-dqm-1", gender: "female", birthDate: "1960-01-01" },
    { resourceType: "Patient", id: "e2e-dqm-2", gender: "male", birthDate: "1958-01-01" },
    htn("e2e-dqm-c1", "e2e-dqm-1"), htn("e2e-dqm-c2", "e2e-dqm-2"),
    bp("e2e-dqm-o1", "e2e-dqm-1", 124, 78), bp("e2e-dqm-o2", "e2e-dqm-2", 150, 96),
  ];
  for (const r of resources) {
    const res = await page.request.put(`/fhir/${r.resourceType}/${r.id}`, { headers: fhir, data: r });
    expect(res.ok(), await res.text()).toBe(true);
  }

  await openTab(page, "Quality measures");
  await page.getByTestId("measure-picks").getByRole("button", { name: "Example: blood pressure under control" }).click();
  await page.getByLabel("Report").selectOption("subject-list");
  await page.getByRole("button", { name: "Evaluate", exact: true }).click();
  const pops = page.getByTestId("measure-populations");
  await expect(pops).toContainText("Patient/e2e-dqm-1");
  await expect(pops).toContainText("Patient/e2e-dqm-2");
  await expect(pops.locator("tr", { hasText: "Numerator" })).toContainText("Patient/e2e-dqm-1");
  await expect(pops.locator("tr", { hasText: "Numerator" })).not.toContainText("Patient/e2e-dqm-2");

  // Pasted resources: exact counts, a score and strata.
  await page.getByLabel("Resources to evaluate over (optional)").fill(resources.map((r) => JSON.stringify(r)).join("\n"));
  await page.getByLabel("Report").selectOption("summary");
  await page.getByRole("button", { name: "Evaluate", exact: true }).click();
  await expect(page.getByText(/from the resources pasted above/)).toBeVisible();
  await expect(pops.locator("tr", { hasText: "Denominator" }).first()).toContainText("2");
  await expect(page.getByTestId("measure-score")).toContainText("50.0%");
  await expect(page.getByTestId("measure-strata")).toContainText("female");
  const dl = page.waitForEvent("download");
  await page.getByRole("button", { name: "Download MeasureReport" }).click();
  expect((await dl).suggestedFilename()).toBe("MeasureReport.json");

  // One patient.
  await page.getByLabel("Report").selectOption("subject");
  await page.getByLabel("Patient", { exact: true }).fill("e2e-dqm-2");
  await page.getByRole("button", { name: "Evaluate", exact: true }).click();
  await expect(page.getByText(/individual report/)).toBeVisible();
  await expect(page.getByTestId("measure-score")).toContainText("0.0%");
  await page.getByLabel("Resources to evaluate over (optional)").fill("");
  await page.getByLabel("Patient", { exact: true }).fill("");
  await page.getByLabel("Report").selectOption("summary");

  // A CQL measure is refused with the reason.
  const editor = page.getByLabel("Measure (JSON)");
  const good = await editor.inputValue();
  await editor.fill(good.replaceAll('"text/fhirpath"', '"text/cql-identifier"'));
  await page.getByRole("button", { name: "Evaluate", exact: true }).click();
  await expect(page.getByText(/cannot be evaluated here.*Perfuse has no CQL engine/)).toBeVisible();
  await editor.fill(good);

  // Saved, then evaluated by the FHIR endpoint.
  await page.getByRole("button", { name: "Save to the FHIR store" }).click();
  await expect(page.getByTestId("measure-saved")).toContainText("Measure/example-bp-control/$evaluate-measure");
  await expect(page.getByTestId("measures-saved")).toContainText("Example: blood pressure under control");
  const ev = await page.request.get(`/fhir/Measure/example-bp-control/$evaluate-measure?periodStart=${year}-01-01&periodEnd=${year}-12-31&subject=Patient/e2e-dqm-1`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(ev.status(), await ev.text()).toBe(200);
  const rep = await ev.json();
  expect(rep.resourceType).toBe("MeasureReport");
  expect(rep.type).toBe("individual");
  expect(rep.measure).toBe("https://perfuse.health/fhir/Measure/example-bp-control|1.0.0");
  expect(rep.group[0].measureScore.value).toBe(1);
  const cap = await (await page.request.get("/fhir/metadata")).json();
  expect(JSON.stringify(cap)).toContain("Measure-evaluate-measure");

  page.once("dialog", (d) => d.accept());
  await page.getByTestId("measures-saved").getByRole("button", { name: "Delete" }).click();
  await expect(page.getByTestId("measures-saved")).toHaveCount(0);
  expect(faults, faults.join("\n")).toEqual([]);
});
