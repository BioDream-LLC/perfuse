import { test, expect } from "@playwright/test";
import { openTab } from "./nav";
import { smartToken } from "./smart";

// Kill the Clipboard, both directions through the interface: a visit record shared as a SMART Health Link with a passcode, then that
// same link read back the way a front desk would - which exercises the public manifest endpoint, the decryption and the passcode.

test("a visit record is shared as a link, and the link reads back to the patient", async ({ page }) => {
  const faults: string[] = [];
  page.on("pageerror", (e) => faults.push(e.message));
  await page.goto("/");
  await openTab(page, "Health links");

  await page.getByRole("button", { name: "Share", exact: true }).click();
  await page.getByLabel("Passcode").fill("2468");
  await page.getByRole("button", { name: "Create the link" }).click();
  const link = (await page.getByTestId("shl-link").innerText()).trim();
  expect(link).toMatch(/^shlink:\//);
  await expect(page.getByTestId("shl-qr").locator("svg")).toBeVisible();
  await expect(page.getByTestId("shl-hosted")).toContainText("Visit summary · passcode, 10 tries left");

  await page.getByRole("button", { name: "Receive", exact: true }).click();
  await page.getByLabel("SMART Health Link").fill(link);
  await page.getByLabel("Passcode").fill("0000");
  await page.getByRole("button", { name: "Read the records" }).click();
  await expect(page.getByRole("alert").first()).toContainText("9 attempt(s) remain");

  await page.getByLabel("Passcode").fill("2468");
  await page.getByRole("button", { name: "Read the records" }).click();
  const received = page.getByTestId("shl-received");
  await expect(received).toContainText("Visit summary: 1 file");
  await expect(received).toContainText("Jane Doe, born 1980-01-01");
  await expect(received).toContainText("1 Encounter · 1 Patient");

  // And the sharing side saw it fetched.
  await page.getByRole("button", { name: "Share", exact: true }).click();
  await expect(page.getByTestId("shl-hosted")).toContainText("1 time, last by Perfuse (admin)");
  await page.getByTestId("shl-hosted").getByRole("button", { name: "Revoke" }).first().click();
  await expect(page.getByTestId("shl-hosted").locator("tr").nth(1)).toContainText("revoked");
  expect(faults, faults.join("\n")).toEqual([]);
});

test("something that is not a health card is refused with the reason", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Health links");
  await page.getByRole("button", { name: "Verify a card" }).click();
  await page.getByLabel("SMART Health Card").fill("hello");
  await page.getByRole("button", { name: "Verify", exact: true }).click();
  await expect(page.getByRole("alert").first()).toContainText("not a SMART Health Card");
});

// The patient summary: built from the server's own FHIR store, shown before it is shared, and shared as a link.
test("a patient's International Patient Summary is built from the store and shared", async ({ page }) => {
  const faults: string[] = [];
  page.on("pageerror", (e) => faults.push(e.message));
  await page.goto("/");
  const token = await smartToken(page.request);
  const fhir = { Authorization: `Bearer ${token}`, "Content-Type": "application/fhir+json" };
  const id = `e2e-ips-${Date.now()}`;
  for (const [path, body] of [
    [`/fhir/Patient/${id}`, { resourceType: "Patient", id, name: [{ family: "Lindqvist", given: ["Maja"] }], birthDate: "1971-05-02" }],
    [`/fhir/Condition/${id}-c`, { resourceType: "Condition", id: `${id}-c`, subject: { reference: `Patient/${id}` },
      clinicalStatus: { coding: [{ system: "http://terminology.hl7.org/CodeSystem/condition-clinical", code: "active" }] },
      code: { text: "Asthma" } }],
  ] as const) {
    const put = await page.request.put(path, { headers: fhir, data: body });
    expect(put.status(), await put.text()).toBeLessThan(300);
  }

  await openTab(page, "Health links");
  await page.getByRole("button", { name: "Share", exact: true }).click();
  await page.getByLabel("Patient", { exact: true }).fill(id);
  await page.getByRole("button", { name: "Use their patient summary" }).click();
  await expect(page.getByTestId("ips-note")).toContainText("International Patient Summary");
  const content = page.getByLabel("FHIR content to share");
  await expect(content).toContainText('"type": "document"');
  await expect(content).toContainText("Asthma");
  // The empty allergy section says the server holds nothing, not that there is nothing.
  await expect(content).toContainText("does not mean there is none");

  await page.getByRole("button", { name: "Create the link" }).click();
  await expect(page.getByTestId("shl-link")).toContainText("shlink:/");
  await expect(page.getByTestId("shl-hosted")).toContainText("Patient summary");

  // An unknown patient is refused with the reason.
  await page.getByLabel("Patient", { exact: true }).fill("nobody-here");
  await page.getByRole("button", { name: "Use their patient summary" }).click();
  await expect(page.getByRole("alert").first()).toContainText("no Patient with id nobody-here");
  expect(faults, faults.join("\n")).toEqual([]);
});
