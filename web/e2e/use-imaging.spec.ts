import { test, expect } from "@playwright/test";
import { readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { openTab } from "./nav";
import { freePort } from "./mllpchannel";
import { waitForListener } from "./mllp";

// The imaging archive, end to end: a study posted by STOW-RS to a channel's DICOMweb source, delivered by its DICOMweb
// destination to this server's own archive with an API token, then found and downloaded from the Imaging view.

const here = dirname(fileURLToPath(import.meta.url));
const fixture = readFileSync(join(here, "..", "..", "internal", "dicomweb", "testdata", "jpeg-lossless.dcm"));
const STUDY = "1.2.826.0.1.3680043.10.1337.9.1";

function multipart(body: Buffer): { type: string; data: Buffer } {
  const boundary = "e2e-boundary-7f3a";
  const head = Buffer.from(`--${boundary}\r\nContent-Type: application/dicom\r\n\r\n`);
  const tail = Buffer.from(`\r\n--${boundary}--\r\n`);
  return { type: `multipart/related; type="application/dicom"; boundary=${boundary}`, data: Buffer.concat([head, body, tail]) };
}

test("a study goes STOW-RS to a channel, into the archive, and shows in Imaging", async ({ page, baseURL }) => {
  const faults: string[] = [];
  page.on("pageerror", (e) => faults.push(e.message));
  const headers = { "X-Perfuse-Request": "1" };
  await page.goto("/");

  const label = `e2e-imaging-${Date.now()}`;
  const tok = await page.request.post("/api/tokens", { headers, data: { label, role: "operator" } });
  expect(tok.ok(), await tok.text()).toBe(true);
  const token = (await tok.json()).token as string;

  const port = await freePort();
  const yaml = [
    "name: e2e-imaging-relay",
    "dataType: dicom",
    "source:",
    "  type: dicomweb",
    "  dicomweb:",
    `    listen: 127.0.0.1:${port}`,
    "    token: e2e-stow",
    "destinations:",
    "  - name: archive",
    "    type: dicomweb",
    "    dicomweb:",
    `      url: ${baseURL}/dicomweb`,
    `      token: ${token}`,
    "",
  ].join("\n");
  const created = await page.request.post("/api/channels", { headers, data: { yaml } });
  expect(created.ok(), await created.text()).toBe(true);
  expect((await page.request.post("/api/channels/e2e-imaging-relay/start", { headers })).ok()).toBe(true);
  await waitForListener(port);

  const body = multipart(fixture);
  const stow = await page.request.post(`http://127.0.0.1:${port}/dicom-web/studies`, {
    headers: { "Content-Type": body.type, Authorization: "Bearer e2e-stow" },
    data: body.data,
  });
  expect(stow.status(), await stow.text()).toBe(200);

  // QIDO-RS on the archive with the session, as the view makes it.
  const qido = await page.request.get(`/dicomweb/studies?PatientID=DWMRN1`, { headers: { Accept: "application/dicom+json" } });
  expect(qido.status()).toBe(200);
  expect(await qido.text()).toContain(STUDY);

  await openTab(page, "Imaging");
  await expect(page.getByTestId("dicomweb-base")).toContainText("/dicomweb");
  const studies = page.getByTestId("imaging-studies");
  await expect(studies).toContainText("DICOMWEB TEST STUDY");
  await expect(studies).toContainText("Doe Jane");
  await studies.getByText("DICOMWEB TEST STUDY").click();
  const series = page.getByTestId("imaging-series");
  await expect(series).toContainText("OT");
  await series.getByRole("button", { name: "Instances" }).click();
  const download = series.getByRole("link", { name: "Download" });
  await expect(download).toBeVisible();
  const file = await page.request.get((await download.getAttribute("href"))!);
  expect(file.headers()["content-type"]).toBe("application/dicom");
  expect(Buffer.compare(await file.body(), fixture)).toBe(0);

  // A search that matches nothing says so.
  await page.getByLabel("Patient ID").fill("NOBODY-HERE");
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await expect(page.getByTestId("imaging-result")).toHaveText("No study matches: PatientID NOBODY-HERE.");

  await page.request.post("/api/channels/e2e-imaging-relay/stop", { headers });
  await page.request.delete("/api/channels/e2e-imaging-relay", { headers });
  await page.request.delete(`/api/tokens/${encodeURIComponent(label)}`, { headers });
  expect(faults, faults.join("\n")).toEqual([]);
});

test("the builder writes a DICOMweb destination the server accepts", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();
  await page.getByRole("button", { name: "Start from nothing" }).click();
  await page.getByLabel("Channel name", { exact: true }).fill("e2e-dicomweb-dest");
  const typeSelect = page.locator("main select").filter({ has: page.locator('option:text-is("A directory on disk")') }).first();
  await typeSelect.selectOption({ label: "An imaging archive or cloud service (DICOMweb STOW-RS)" });
  await page.getByLabel("DICOMweb service root").fill("https://pacs.e2e.example/dicom-web/studies");
  await expect(page.getByText("Give the service root without /studies")).toBeVisible();
  await page.getByLabel("DICOMweb service root").fill("https://pacs.e2e.example/dicom-web");
  await expect(page.getByText("Give the service root without /studies")).toHaveCount(0);
  const pre = page.locator("pre").first();
  await expect(pre).toContainText("type: dicomweb", { timeout: 20_000 });
  await expect(pre).toContainText("url: https://pacs.e2e.example/dicom-web");
});
