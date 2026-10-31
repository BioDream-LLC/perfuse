import { test, expect } from "@playwright/test";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
import { openTab } from "./nav";

// A provider's prior authorization requests, sent by a channel to a payer. Built in the form, then run for real against the
// HL7 Da Vinci PAS payer reference implementation when PERFUSE_TEST_PAS_RI names it (docker image hlseven/davinci-prior-auth,
// for example http://localhost:9015/fhir) and PERFUSE_TEST_PAS_RI_TOKEN is the token it accepts.

test("a channel can be given a PAS destination, with the payer and where its answers go", async ({ page }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`uncaught exception: ${e.message}`));
  await page.goto("/");
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();
  await page.getByRole("button", { name: /Record an HL7 feed/ }).click();
  const preview = page.locator("pre").first();
  await expect(preview).toBeVisible({ timeout: 20_000 });

  const type = page.locator('select[id^="dest-"][id$="-type"]').first();
  await type.selectOption("pas");
  await page.getByLabel("Payer's FHIR address").fill("https://pas.payer.example/fhir");
  await page.getByLabel(/Hand the payer's answers to channel/).fill("pas-answers");
  await page.getByLabel("Payer's token").fill("${PAYER_TOKEN}");
  for (const want of ["type: pas", "payer: https://pas.payer.example/fhir", "results_to: pas-answers", "bearer_token: ${PAYER_TOKEN}"]) {
    await expect.poll(async () => (await preview.textContent()) ?? "", { timeout: 20_000 }).toContain(want);
  }
  expect(problems, problems.join("\n")).toEqual([]);
});

test("a request sent through a channel reaches the reference payer and its answer is listed", async ({ page }) => {
  const payer = process.env.PERFUSE_TEST_PAS_RI;
  test.skip(!payer, "PERFUSE_TEST_PAS_RI is not set (the Da Vinci PAS payer reference implementation)");
  const name = `e2e-pas-${Date.now().toString(36)}`;
  const port = 20000 + Math.floor(Math.random() * 20000);
  const yaml = `name: ${name}
dataType: raw
source:
  type: http
  http:
    listen: 127.0.0.1:${port}
    path: /pas
destinations:
  - name: reference-payer
    type: pas
    pas:
      payer: ${payer}
      bearer_token: ${process.env.PERFUSE_TEST_PAS_RI_TOKEN ?? ""}
`;
  await page.goto("/");
  const created = await page.request.post("/api/channels", { headers: { "X-Perfuse-Request": "1" }, data: { yaml } });
  expect(created.ok(), `creating the channel failed: ${created.status()} ${await created.text()}`).toBe(true);
  const started = await page.request.post(`/api/channels/${name}/start`, { headers: { "X-Perfuse-Request": "1" } });
  expect(started.ok(), `starting the channel failed: ${await started.text()}`).toBe(true);

  const bundle = readFileSync(join(here, "..", "..", "internal", "priorauth", "testdata", "pas-ri-request.json"), "utf8");
  let status = 0;
  for (let i = 0; i < 20 && status === 0; i++) {
    try {
      status = (await fetch(`http://127.0.0.1:${port}/pas`, { method: "POST", body: bundle, headers: { "Content-Type": "application/fhir+json" } })).status;
    } catch {
      await new Promise((r) => setTimeout(r, 500));
    }
  }
  expect(status, "the channel did not accept the request").toBeLessThan(300);

  await openTab(page, "CMS-0057");
  await page.getByRole("button", { name: "Requests sent to payers" }).click();
  const table = page.getByTestId("prior-auth-sent");
  await expect(table.getByRole("row").filter({ hasText: "reference-payer" }).first()).toContainText("Pending", { timeout: 30_000 });

  await page.request.post(`/api/channels/${name}/stop`, { headers: { "X-Perfuse-Request": "1" } });
  await page.request.delete(`/api/channels/${name}`, { headers: { "X-Perfuse-Request": "1" } });
});
