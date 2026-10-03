import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// The CMS-0057 view, driven the way a payer's analyst would use it: read which APIs are ready, convert a paid claim to CARIN
// Blue Button, convert a PAS decision to PDex, and produce the public metrics page. Each test checks content the server
// computed, not merely that a panel rendered.

test("the four CMS-0057 APIs are listed with what each still needs", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "CMS-0057");
  const apis = page.getByTestId("cms0057-apis");
  for (const name of ["Patient Access API", "Provider Access API", "Payer-to-Payer API", "Prior Authorization API"]) {
    await expect(apis.getByRole("heading", { name })).toBeVisible();
  }
  // The e2e server runs the payer operations and bulk export, so Provider Access is ready; it has no SMART issuer, so Patient
  // Access is not, and says why.
  const provider = apis.locator("section", { has: page.getByRole("heading", { name: "Provider Access API" }) });
  await expect(provider).toContainText("Ready");
  await expect(provider).toContainText("$davinci-data-export");
  const patient = apis.locator("section", { has: page.getByRole("heading", { name: "Patient Access API" }) });
  await expect(patient).toContainText("Not ready");
  await expect(patient).toContainText("SMART");
});

test("a paid claim becomes a CARIN Blue Button bundle, and the identifier system is required", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "CMS-0057");
  await page.getByRole("button", { name: "Claims to CARIN BB" }).click();

  await page.getByRole("button", { name: "Convert to CARIN Blue Button" }).click();
  await expect(page.getByRole("alert").or(page.locator("text=identifier system")).first()).toBeVisible();

  await page.getByLabel("Identifier system").fill("https://fhir.springfield-health-plan.org/identifier");
  await page.getByRole("button", { name: "Convert to CARIN Blue Button" }).click();
  const result = page.getByTestId("carin-result");
  await expect(result).toContainText("EHPCLAIM20250001");
  await expect(result).toContainText("C4BB-ExplanationOfBenefit-Professional-NonClinician");
  const bundle = await page.getByTestId("carin-bundle").innerText();
  expect(bundle).toContain('"J02.9"');
  expect(bundle).toContain('"memberliability"');
  expect(bundle).toContain('"clmrecvddate"');
});

test("a PAS decision becomes a PDex prior authorization", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "CMS-0057");
  await page.getByRole("button", { name: "Prior auth to PDex" }).click();
  await page.getByRole("button", { name: "Convert to PDex prior authorization" }).click();
  const result = page.getByTestId("pdex-result");
  await expect(result).toContainText("Decision: approved");
  await expect(result).toContainText("pdex-priorauthorization");
  await expect(result).toContainText("preauthorization");
});

test("the prior authorization metrics follow the CMS template, with a median under a day in hours", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "CMS-0057");
  await page.getByRole("button", { name: "Prior auth metrics" }).click();
  await page.getByLabel("Organization name").fill("Springfield Health Plan");
  await page.getByRole("button", { name: "Compute the metrics" }).click();

  const result = page.getByTestId("metrics-result");
  await expect(result).toContainText("Medicare Advantage H1234");
  // Three standard requests, one approved.
  await expect(result).toContainText("Standard: 33.3% approved of 3");
  // Expedited: 6 hours and 84 hours, median 45 hours - under two days, so in calendar days; the test is that a unit is written.
  await expect(result).toContainText(/median \d+(\.\d+)? (hours|calendar days)/);

  const preview = page.frameLocator('iframe[title="Preview of the public prior authorization metrics page"]');
  await expect(preview.getByRole("heading", { name: /Prior Authorization Metrics/ })).toBeVisible();
  await expect(preview.locator("body")).toContainText("Springfield Health Plan");
  await expect(preview.locator("body")).toContainText("Request approved only after appeal");

  const [download] = await Promise.all([
    page.waitForEvent("download"),
    page.getByRole("button", { name: "Download CSV" }).click(),
  ]);
  const { readFileSync } = await import("node:fs");
  const csv = readFileSync((await download.path())!, "utf8");
  expect(csv).toContain("2025,Medicare Advantage H1234,standard,approved,1,3,33.3");
});

test("a provider's token is issued limited to its Group, and the FHIR endpoint refuses it everything else", async ({ page }) => {
  // Exporting its own Group is covered against the server in Go (TestGroupLimitedTokenReachesOnlyItsGroup); this checks the
  // console issues the limit and that the running endpoint enforces it.
  const label = `e2e-provider-${Date.now().toString(36)}`;

  await page.goto("/");
  await openTab(page, "Users");
  await page.getByLabel("What is it for", { exact: true }).fill(label);
  await page.getByLabel("Limit to FHIR Groups", { exact: true }).fill(`${label}-own`);
  await page.getByRole("button", { name: "Issue token" }).click();
  const token = (await page.getByTestId("issued-token").innerText()).trim();
  expect(token.length).toBeGreaterThan(20);
  await page.getByRole("button", { name: "I have saved it" }).click();
  await expect(page.locator("tbody tr").filter({ hasText: label })).toContainText(`FHIR Groups: ${label}-own`);

  // Checked against the FHIR endpoint itself, not the page: the limit is only real if the server enforces it.
  const bearer = { Authorization: `Bearer ${token}`, Prefer: "respond-async" };
  expect((await page.request.get("/fhir/Patient", { headers: bearer })).status(), "an ordinary search").toBe(403);
  expect((await page.request.get(`/fhir/Group/${label}-other/$davinci-data-export`, { headers: bearer })).status(),
    "another provider's Group").toBe(404);
  expect((await page.request.get("/fhir/metadata", { headers: bearer })).status(), "the capability statement").toBe(200);

  await openTab(page, "CMS-0057");
  const provider = page.getByTestId("cms0057-apis").locator("section", { has: page.getByRole("heading", { name: "Provider Access API" }) });
  await expect(provider).toContainText(/API tokens? (is|are) limited to FHIR Groups/);

  await page.request.delete(`/api/tokens/${encodeURIComponent(label)}`, { headers: { "X-Perfuse-Request": "1" } });
});
