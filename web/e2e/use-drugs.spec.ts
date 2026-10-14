import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// The drug formulary and the two NCPDP standards answered from it, through the Drugs screen and the public FHIR API.

test("the formulary is public over the Drug Formulary API and listed on the screen", async ({ page, playwright, baseURL }) => {
  // No cookie and no token: the Drug Formulary API is public, as CMS requires, and holds nothing else.
  const anon = await playwright.request.newContext({ baseURL });
  const items = await anon.get("/formulary/fhir/Basic?drug-tier=specialty&_include=Basic:subject");
  expect(items.status()).toBe(200);
  const bundle = await items.json();
  expect(JSON.stringify(bundle)).toContain("FormularyDrug-1872980");
  expect((await anon.post("/formulary/fhir/Patient", { data: { resourceType: "Patient" } })).status()).toBe(405);
  expect((await anon.get("/fhir/Patient")).status()).toBe(401);
  await anon.dispose();

  await page.goto("/");
  await openTab(page, "Drugs");
  await expect(page.getByTestId("formulary-base")).toContainText("/formulary/fhir");
  const table = page.getByTestId("formulary-drugs");
  await expect(table).toContainText("Lovenox");
  await page.getByLabel("Find a drug").fill("humira");
  await expect(table).toContainText("prior authorization");
  await expect(table).not.toContainText("metformin");
});

test("a price check shows the restriction, the member's share and the cheaper alternative", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Drugs");
  await page.getByRole("tab", { name: "Price check (RTPB)" }).click();
  await page.getByLabel("Member ID", { exact: true }).fill("MBR1001");
  await page.getByLabel("Drug", { exact: true }).selectOption({ label: "canagliflozin 100 MG Oral Tablet [Invokana]" });
  await page.getByRole("button", { name: "Check", exact: true }).click();
  const result = page.getByTestId("rtpb-result");
  await expect(result).toContainText("Step therapy required");
  await expect(result).toContainText("$45.00");
  await expect(result).toContainText("Alternative");
  await expect(result).toContainText("metformin");
  await expect(result).toContainText("$10.00");

  await page.getByLabel("Member ID", { exact: true }).fill("NOBODY-AT-ALL");
  await page.getByRole("button", { name: "Check", exact: true }).click();
  await expect(result).toContainText("Not priced (reject 52)");
});

test("an ePA is started, answered and decided, and lands in the review queue as a drug", async ({ page }) => {
  const faults: string[] = [];
  page.on("pageerror", (e) => faults.push(e.message));
  await page.goto("/");
  await openTab(page, "Drugs");
  await page.getByRole("tab", { name: "Prior authorization (ePA)" }).click();
  await page.getByLabel("Drug", { exact: true }).selectOption({ label: "0.4 ML adalimumab 100 MG/ML Auto-Injector [Humira] (prior authorization, quantity limit of 2 per 28 days)" });
  await page.getByLabel("Member ID", { exact: true }).fill("MBR1001");
  await page.getByLabel("Quantity", { exact: true }).fill("2");
  await page.getByLabel("Days supply", { exact: true }).fill("28");
  await page.getByRole("button", { name: "Start a prior authorization" }).click();
  const outcome = page.getByTestId("epa-outcome");
  await expect(outcome).toContainText("PAInitiationResponse: open");
  const questions = page.getByTestId("epa-questions");
  await expect(questions).toContainText("diagnosis");
  const boxes = questions.locator("textarea");
  for (let i = 0; i < (await boxes.count()); i++) await boxes.nth(i).fill("Rheumatoid arthritis; methotrexate for 6 months did not control it.");
  await questions.getByRole("button", { name: "Send the answers" }).click();
  // No rule decides it, so it waits for a reviewer, and the prescriber is told it is under review.
  await expect(outcome).toContainText("PAResponse: open");
  await expect(outcome).toContainText("under review");

  const cases = page.getByTestId("epa-cases");
  await expect(cases).toContainText("Humira");
  await expect(cases).toContainText("submitted");
  await cases.getByText("Humira").first().click();
  await expect(page.getByTestId("epa-case")).toContainText("methotrexate");

  // The same case in the PAS reviewer queue, marked Drug, held to the drug timeframe.
  await openTab(page, "CMS-0057");
  await page.getByRole("button", { name: "Reviewer queue" }).click();
  await expect(page.getByText("Drug", { exact: true }).first()).toBeVisible();
  await page.getByRole("button", { name: "The four APIs" }).click();
  const drugCard = page.getByTestId("cms0057-apis").locator("section", { has: page.getByRole("heading", { name: /Drug prior authorization/ }) });
  await expect(drugCard).toContainText("Ready");
  await expect(drugCard).toContainText("/ncpdp/script");
  expect(faults, faults.join("\n")).toEqual([]);
});
