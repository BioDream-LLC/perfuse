import { test, expect } from "@playwright/test";
import { mkdtempSync, readdirSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { openTab } from "./nav";
import { freePort, sendMLLPTo } from "./mllpchannel";
import { waitForListener } from "./mllp";

// Immunization registries: the CDC IIS web service, end to end.
//
// Two halves. The builder must write a registry destination and a registry source the server accepts, without the
// plain-SOAP fields (a body, an action) the profile builds itself. And a real VXU must travel MLLP -> Perfuse ->
// CDC SOAP -> a Perfuse channel answering as a registry, with the registry's ACK coming back to the sender, and the
// message must show in Messages.

function watch(page: import("@playwright/test").Page): string[] {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`page error: ${e}`));
  page.on("console", (m) => {
    if (m.type() !== "error" || /Failed to load resource/.test(m.text())) return;
    problems.push(`console: ${m.text()}`);
  });
  return problems;
}

test("the builder writes a registry destination with the CDC profile and nothing it builds itself", async ({ page }) => {
  const problems = watch(page);
  await page.goto("/");
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();
  await page.getByRole("button", { name: "Start from nothing" }).click();
  await page.getByLabel("Channel name", { exact: true }).fill("e2e-iis-dest");

  const typeSelect = page.locator("main select").filter({ has: page.locator('option:text-is("A directory on disk")') }).first();
  await typeSelect.selectOption({ label: "A SOAP web service" });
  await page.getByLabel("SOAP service").selectOption("cdc-iis");

  // The plain-SOAP fields are gone; the registry's are there.
  await expect(page.getByText("Request body", { exact: true })).toHaveCount(0);
  await page.getByLabel("Endpoint").fill("https://iis.e2e.example/iis-ws/cdc");
  await page.getByLabel("Username", { exact: true }).fill("e2e-submitter");
  await page.getByLabel("Password", { exact: true }).fill("e2e-secret");
  await page.getByLabel("Facility ID").fill("FAC-E2E");
  await page.getByLabel("Treat warnings as failures").check();

  // Each awaited, because the preview is rebuilt on a debounce after the last keystroke.
  const pre = page.locator("pre").first();
  for (const want of ["profile: cdc-iis", "url: https://iis.e2e.example/iis-ws/cdc", "facility_id: FAC-E2E",
    "warnings_are_errors: true", "username: e2e-submitter", 'version: "1.2"']) {
    await expect(pre).toContainText(want, { timeout: 20_000 });
  }
  const yaml = (await pre.textContent()) ?? "";
  expect(yaml).not.toMatch(/\bbody:|\baction:/);
  // The form leaves the destination unnamed, which the server reports; nothing may be reported about the SOAP block.
  const refusal = page.getByRole("alert").filter({ hasText: /would not accept this channel/ });
  if ((await refusal.count()) > 0) {
    expect(await refusal.first().innerText()).not.toMatch(/soap|registry|profile/i);
  }

  // Back to a plain service: the registry fields leave the file.
  await page.getByLabel("SOAP service").selectOption("");
  await expect(pre).not.toContainText("profile: cdc-iis", { timeout: 20_000 });

  expect(problems, problems.join("\n")).toEqual([]);
});

test("a VXU reaches a registry over the CDC web service and the registry's ACK comes back", async ({ page }) => {
  const problems = watch(page);
  const mllpPort = await freePort();
  const iisPort = await freePort();
  const received = mkdtempSync(join(tmpdir(), "perfuse-iis-"));
  const controlId = `IIS${Date.now()}`;
  const headers = { "X-Perfuse-Request": "1" };

  const registry = [
    "name: e2e-registry",
    "source:",
    "  type: soap",
    "  soap:",
    "    profile: cdc-iis",
    `    listen: 127.0.0.1:${iisPort}`,
    "    path: /iis",
    "    username: e2e",
    "    password: e2e-pass",
    "destinations:",
    "  - name: received",
    "    type: file",
    `    dir: ${received}`,
    "",
  ].join("\n");
  const reporter = [
    "name: e2e-vxu-report",
    "source:",
    "  type: mllp",
    `  listen: 127.0.0.1:${mllpPort}`,
    "  ack:",
    "    when: on_delivery",
    "destinations:",
    "  - name: state-registry",
    "    type: soap",
    "    soap:",
    "      profile: cdc-iis",
    `      url: http://127.0.0.1:${iisPort}/iis`,
    "      username: e2e",
    "      password: e2e-pass",
    "      facility_id: FAC-E2E",
    "",
  ].join("\n");

  await page.goto("/");
  for (const yaml of [registry, reporter]) {
    const created = await page.request.post("/api/channels", { headers, data: { yaml } });
    expect(created.ok(), `creating a channel failed: ${created.status()} ${await created.text()}`).toBe(true);
  }
  for (const name of ["e2e-registry", "e2e-vxu-report"]) {
    const started = await page.request.post(`/api/channels/${name}/start`, { headers });
    expect(started.ok(), `starting ${name} failed: ${started.status()} ${await started.text()}`).toBe(true);
  }
  await waitForListener(iisPort);
  await waitForListener(mllpPort);

  // The registry serves the CDC WSDL.
  const wsdl = await page.request.get(`http://127.0.0.1:${iisPort}/iis?wsdl`);
  expect(await wsdl.text()).toContain("urn:cdc:iisb:2011:submitSingleMessage");

  const vxu = [
    `MSH|^~\\&|EHR|CLINIC|IIS|STATE|20261014120000||VXU^V04^VXU_V04|${controlId}|P|2.5.1|||ER|AL|||||Z22^CDCPHINVS`,
    "PID|1||E2E777^^^CLINIC^MR||Evrard^Camille||20200101|F",
    "ORC|RE||197023^CLINIC",
    "RXA|0|1|20261014|20261014|08^HepB^CVX|0.5|mL^mL^UCUM",
  ].join("\r");
  const ack = await sendMLLPTo(mllpPort, vxu);
  expect(ack, `the sender's acknowledgement was not an accept:\n${ack}`).toMatch(/\|AA\|/);

  // The registry channel received exactly the message, with HL7 segment separators intact.
  await expect.poll(() => readdirSync(received).length, { timeout: 20_000 }).toBeGreaterThan(0);
  const stored = readdirSync(received).map((f) => readFileSync(join(received, f), "latin1")).join("");
  expect(stored).toContain(controlId);
  expect(stored).toContain("RXA|0|1|20261014|20261014|08^HepB^CVX");
  expect(stored, "an LF reached the registry where HL7 has a CR").not.toMatch(/CDCPHINVS\n/);

  // And the console shows it.
  await page.reload();
  await openTab(page, "Messages");
  await expect(async () => {
    await page.getByRole("button", { name: "Refresh" }).first().click();
    await expect(page.locator("main")).toContainText(controlId, { timeout: 3000 });
  }).toPass({ timeout: 45_000 });

  for (const name of ["e2e-vxu-report", "e2e-registry"]) {
    await page.request.post(`/api/channels/${name}/stop`, { headers });
    await page.request.delete(`/api/channels/${name}`, { headers });
  }
  expect(problems, problems.join("\n")).toEqual([]);
});

test("the builder writes a source that answers as a registry", async ({ page }) => {
  const problems = watch(page);
  await page.goto("/");
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();
  await page.getByRole("button", { name: "Start from nothing" }).click();
  await page.getByLabel("Channel name", { exact: true }).fill("e2e-iis-source");

  const selector = page.getByLabel(/How do messages get here/).first();
  const options = await selector.locator("option").allTextContents();
  await selector.selectOption({ label: options.find((o) => /SOAP/.test(o))! });
  await page.getByLabel("Service", { exact: true }).selectOption("cdc-iis");
  await expect(page.getByLabel("Element holding the message")).toHaveCount(0);
  await page.getByLabel("Listen on").fill("127.0.0.1:18096");
  await page.getByLabel("Username callers send").fill("caller");
  await page.getByLabel("Password callers send").fill("caller-pass");

  const pre = page.locator("pre").first();
  for (const want of ["profile: cdc-iis", 'version: "1.2"', "username: caller"]) {
    await expect(pre).toContainText(want, { timeout: 20_000 });
  }
  const yaml = (await pre.textContent()) ?? "";
  expect(yaml).not.toMatch(/element:|response_element:/);
  expect(problems, problems.join("\n")).toEqual([]);
});
