import { test, expect } from "@playwright/test";
import { openTab } from "./nav";
import { smartToken } from "./smart";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));

// Eligibility, claim status and enrolment on the claims page, driven as a billing office would. Each test checks what the server
// computed - the built interchange, the assembled answer, the CORE findings - not merely that a panel rendered.

test("a 270 is built, and a 271 is read into an answer with its CORE findings", async ({ page }) => {
  const faults: string[] = [];
  page.on("pageerror", (e) => faults.push(e.message));
  await page.goto("/");
  await openTab(page, "Claims & auth");
  await page.getByRole("button", { name: "Eligibility (270/271)" }).click();

  await page.getByRole("button", { name: "Build the 270" }).click();
  const built = page.getByTestId("built-x12");
  await expect(built).toContainText("ST*270*");
  await expect(built).toContainText("NM1*IL*1*DOE*JANE****MI*MBR123456");
  await expect(page.getByText(/envelope counts and control numbers agree/)).toBeVisible();

  await page.getByRole("button", { name: "Read the 271" }).click();
  const summary = page.getByTestId("eligibility-summary");
  await expect(summary).toContainText("Coverage active");
  await expect(summary).toContainText("Co-payment in network: $25.");
  await expect(summary).toContainText("Individual deductible remaining in network: $800.");
  const core = page.getByTestId("eligibility-core");
  await expect(core).toContainText("Met: Deductible remaining, in network");
  await expect(core).not.toContainText("Missing");

  // A bad NPI is refused with the reason, not built.
  await page.getByLabel("Provider NPI").fill("1234567890");
  await page.getByRole("button", { name: "Build the 270" }).click();
  await expect(page.getByRole("alert").first()).toContainText("check digit");
  expect(faults, faults.join("\n")).toEqual([]);
});

test("a 276 is built and an 834 is read into members", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Claims & auth");
  await page.getByRole("button", { name: "Claim status (276)" }).click();
  await page.getByRole("button", { name: "Build the 276" }).click();
  await expect(page.getByTestId("built-x12")).toContainText("REF*EJ*PCN0042");

  await page.getByRole("button", { name: "Enrolment (834)" }).click();
  await page.getByRole("button", { name: "Read the 834" }).click();
  const members = page.getByTestId("enrollment-members");
  await expect(members).toContainText("JANE DOE");
  await expect(members).toContainText("cancellation or termination");
  await expect(members).toContainText("dental DENTAL1 FAM from 20261101");
});

test("an order is asked about at order-sign, and the payer's rules answer with coverage information", async ({ page }) => {
  await page.goto("/");
  // The payer loads its DTR questionnaire into the FHIR endpoint, as a payer would.
  // A SMART Backend Services token: with SMART configured, the FHIR endpoint takes SMART tokens only by default.
  const token = await smartToken(page.request);
  const questionnaire = JSON.parse(readFileSync(join(here, "..", "..", "examples", "crd", "questionnaire-home-oxygen.json"), "utf8"));
  const put = await page.request.put("/fhir/Questionnaire/home-oxygen", {
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/fhir+json" },
    data: questionnaire,
  });
  expect(put.ok(), await put.text()).toBeTruthy();

  await openTab(page, "Claims & auth");
  await page.getByRole("button", { name: "Coverage requirements (CRD)" }).click();
  await page.getByRole("button", { name: "Ask for coverage requirements" }).click();
  const cards = page.getByTestId("crd-cards");
  await expect(cards).toContainText("Home oxygen equipment: covered, prior authorization required");
  await expect(cards).toContainText("From Springfield Health Plan");
  const info = page.getByTestId("crd-coverage-info");
  await expect(info).toContainText("pa-needed: auth-needed");
  await expect(info).toContainText("questionnaire: https://www.springfield-health-plan.example/fhir/Questionnaire/home-oxygen");
  // The rest of the payer's answer, each part on screen: why, the billing code, the limit, who to call and when it lapses.
  await expect(info).toContainText("reason: Prior authorization is required for home oxygen");
  await expect(info).toContainText("billingCode: E0424 Stationary compressed gas 02");
  await expect(info).toContainText("detail: allowed-period: Rental for up to 36 months");
  await expect(info).toContainText("contact: Springfield Health Plan utilization management, +1-217-555-0199");
  await expect(info).toContainText(/expiry-date: \d{4}-\d{2}-\d{2}/);

  // DTR: the questionnaire that coverage-information names, packaged with the response the documentation app fills in. The setup loads
  // the example questionnaire into the FHIR endpoint.
  await page.getByRole("button", { name: "Get the questionnaire package" }).click();
  const pkg = page.getByTestId("dtr-package");
  await expect(pkg).toContainText("Home oxygen therapy: documentation for prior authorization");
  await expect(pkg).toContainText("Oxygen saturation at rest on room air (%)");
  await expect(pkg).toContainText("Response to fill in: in-progress");

  // And the discovery document is published for EHRs.
  const discovery = await page.request.get("/cds-services");
  expect(discovery.ok()).toBeTruthy();
  expect(JSON.stringify(await discovery.json())).toContain("crd-order-sign");
});
