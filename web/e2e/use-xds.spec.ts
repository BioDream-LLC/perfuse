import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// IHE document sharing through the console: a CDA document published to this server's repository, then found by patient
// over this server's registry and over its own XCA gateway, and downloaded; and the same document found over MHD (FHIR).

const headers = { "X-Perfuse-Request": "1" };
const patient = "DWMRN1^^^&1.2.826.0.1.3680043.10.1337.99&ISO";

test("a CDA document is published, found over XDS and XCA and MHD, and downloaded", async ({ page, baseURL }) => {
  const faults: string[] = [];
  page.on("pageerror", (e) => faults.push(e.message));
  await page.goto("/");
  await openTab(page, "Document sharing");
  await expect(page.getByTestId("xds-repository")).toContainText("/xds/repository");

  await page.getByRole("button", { name: "Publish" }).click();
  const published = page.getByTestId("xds-published");
  await expect(published).toContainText("Registered Consult note for " + patient);
  await expect(published).toContainText("C-CDA R2.1 structured body");
  await expect(page.getByTestId("xds-documents")).toContainText("Consult note");
  await expect(page.getByTestId("xds-documents")).toContainText("Alex Smith");

  // This registry.
  await page.getByLabel("Patient ID", { exact: true }).fill(patient);
  await page.getByRole("button", { name: "Find documents" }).click();
  await expect(page.getByTestId("xds-found")).toContainText("this server: Success");
  const download = page.getByTestId("xds-found-docs").getByRole("link", { name: /Download/ }).first();
  const file = await page.request.get((await download.getAttribute("href"))!);
  expect(file.status()).toBe(200);
  expect(await file.text()).toContain("<title>Consult note</title>");

  // Over XCA, through this server's own gateway, as another community would. The gateway takes a token.
  const label = `e2e-xca-${Date.now()}`;
  const tok = await page.request.post("/api/tokens", { headers, data: { label, role: "viewer" } });
  const token = (await tok.json()).token as string;
  try {
    await page.getByLabel("Registry or gateway (optional)").fill(`${baseURL}/xca/gateway`);
    await page.getByLabel("It is an XCA gateway").check();
    await page.getByLabel("Bearer token for it (optional)").fill(token);
    await page.getByRole("button", { name: "Find documents" }).click();
    await expect(page.getByTestId("xds-found")).toContainText("/xca/gateway: Success");
    await expect(page.getByTestId("xds-found-docs")).toContainText("Consult note");

    // And over MHD, as a mobile client.
    const mhd = await page.request.get(`/mhd/DocumentReference?patient.identifier=${encodeURIComponent("urn:oid:1.2.826.0.1.3680043.10.1337.99|DWMRN1")}`,
      { headers: { Authorization: `Bearer ${token}` } });
    expect(mhd.status()).toBe(200);
    const bundle = await mhd.json();
    expect(bundle.total).toBeGreaterThanOrEqual(1);
    const url = bundle.entry[0].resource.content[0].attachment.url as string;
    const doc = await page.request.get(url.replace(/^https?:\/\/[^/]+/, ""), { headers: { Authorization: `Bearer ${token}` } });
    expect(await doc.text()).toContain("ClinicalDocument");
  } finally {
    await page.request.delete(`/api/tokens/${encodeURIComponent(label)}`, { headers });
  }

  // Publishing the same document again is the registry's duplicate error, shown.
  await page.getByRole("button", { name: "Publish" }).click();
  await expect(page.getByText(/XDSDuplicateUniqueIdInRegistry/)).toBeVisible();
  expect(faults, faults.join("\n")).toEqual([]);
});

test("the builder writes an xds destination the server accepts", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();
  await page.getByRole("button", { name: "Start from nothing" }).click();
  await page.getByLabel("Channel name", { exact: true }).fill("e2e-xds-dest");
  const typeSelect = page.locator("main select").filter({ has: page.locator('option:text-is("A directory on disk")') }).first();
  await typeSelect.selectOption({ label: "A document repository (IHE XDS.b ITI-41)" });
  await page.getByLabel("Repository's ITI-41 address").fill("https://hie.e2e.example/xds/repository");
  await page.getByLabel("Source ID (this sender's OID)").fill("1.2.3.4.5");
  await page.getByLabel("Facility type code").fill("225732001");
  await page.getByLabel("Practice setting code").fill("394802001");
  const pre = page.locator("pre").first();
  await expect(pre).toContainText("type: xds", { timeout: 20_000 });
  await expect(pre).toContainText("source_id: 1.2.3.4.5");
  await expect(pre).toContainText("facility_type:");
});
