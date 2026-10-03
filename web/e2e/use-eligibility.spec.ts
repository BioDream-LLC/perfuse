import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

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
