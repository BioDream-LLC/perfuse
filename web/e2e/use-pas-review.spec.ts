import { test, expect } from "@playwright/test";
import { smartToken } from "./smart";
import { openTab } from "./nav";

// The PAS reviewer queue, driven the way a payer's nurse reviewer would use it: a provider's request arrives over FHIR and is
// pended, the provider sends the document it was asked for, and the reviewer reads both and certifies fewer units than asked.
// Each step checks what the server recorded, read back over FHIR.

function pasRequest(trace: string) {
  const base = "http://provider.example/fhir";
  return {
    resourceType: "Bundle",
    type: "collection",
    entry: [
      {
        fullUrl: `${base}/Claim/${trace}`,
        resource: {
          resourceType: "Claim", id: trace, status: "active", use: "preauthorization",
          identifier: [{ system: "http://provider.example/trace", value: trace }],
          type: { coding: [{ system: "http://terminology.hl7.org/CodeSystem/claim-type", code: "professional" }] },
          patient: { reference: "Patient/member" }, insurer: { reference: "Organization/plan" }, provider: { reference: "Organization/clinic" },
          created: "2026-10-05", priority: { coding: [{ code: "stat" }] },
          item: [{ sequence: 1, quantity: { value: 12 },
            productOrService: { coding: [{ system: "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets", code: "E0424", display: "Stationary compressed gas 02" }] } }],
        },
      },
      { fullUrl: `${base}/Patient/member`, resource: { resourceType: "Patient", id: "member",
        name: [{ family: "Reviewer", given: ["Riley"] }], identifier: [{ system: "http://plan.example/member", value: "E2E-MEMBER-1" }] } },
      { fullUrl: `${base}/Organization/plan`, resource: { resourceType: "Organization", id: "plan", name: "Plan" } },
      { fullUrl: `${base}/Organization/clinic`, resource: { resourceType: "Organization", id: "clinic", name: "Springfield Clinic",
        identifier: [{ system: "http://hl7.org/fhir/sid/us-npi", value: "1234567893" }] } },
    ],
  };
}

test("a pended prior authorization is read with its document and certified for fewer units", async ({ page }) => {
  await page.goto("/");
  // A SMART Backend Services token: with SMART configured, the FHIR endpoint takes SMART tokens only by default.
  const token = await smartToken(page.request);
  const fhir = { Authorization: `Bearer ${token}`, "Content-Type": "application/fhir+json" };
  try {
    const sub = await page.request.post("/fhir/Claim/$submit", { headers: fhir, data: pasRequest("E2E-PAS-1") });
    expect(sub.ok(), await sub.text()).toBeTruthy();
    const response = await sub.json();
    const cr = response.entry.find((e: { resource: { resourceType: string } }) => e.resource.resourceType === "ClaimResponse").resource;
    const task = response.entry.find((e: { resource: { resourceType: string } }) => e.resource.resourceType === "Task")?.resource;

    // Home oxygen needs the plan's questionnaire, so the response asks for it with a Task; the provider answers that Task.
    expect(task, "the pended response should ask for documentation").toBeTruthy();
    {
      const att = await page.request.post("/fhir/$submit-attachment", {
        headers: fhir,
        data: { resourceType: "Parameters", parameter: [
          { name: "TrackingId", valueIdentifier: task.identifier[0] },
          { name: "AttachTo", valueCode: "preauthorization" },
          { name: "MemberId", valueIdentifier: { value: "E2E-MEMBER-1" } },
          { name: "Attachment", part: [{ name: "Content", resource: { resourceType: "DocumentReference", status: "current",
            content: [{ attachment: { contentType: "text/plain", data: btoa("Oximetry 86% on room air; twelve months of oxygen requested. Plan: twelve sessions of therapy.") } }] } }] },
        ] },
      });
      expect(att.ok(), await att.text()).toBeTruthy();
    }

    await openTab(page, "CMS-0057");
    await page.getByRole("button", { name: "Reviewer queue" }).click();
    const queue = page.getByTestId("pas-queue");
    await expect(queue).toContainText("Riley Reviewer");
    await expect(queue).toContainText("Expedited");
    await queue.getByRole("button", { name: /Riley Reviewer/ }).click();

    const c = page.getByTestId("pas-case");
    await expect(c).toContainText("Stationary compressed gas 02 (E0424)");
    await expect(c).toContainText("A4 Pending");
    await expect(page.getByTestId("pas-documents")).toContainText("twelve sessions of therapy");
    await expect(c).toContainText("Asked for: questionnaire");

    await c.getByRole("button", { name: "Modify" }).click();
    await c.getByLabel("Units certified").fill("6");
    await c.getByLabel("Reason").fill("Six sessions, then a review of progress.");
    await c.getByRole("button", { name: "Record the decision" }).click();
    await expect(c).toContainText("A6 Modified");
    await expect(c).toContainText("Certified for 6");

    // Over FHIR, the provider reads the same decision.
    const read = await page.request.get(`/fhir/ClaimResponse/${cr.id}`, { headers: { Authorization: `Bearer ${token}` } });
    const now = await read.json();
    expect(JSON.stringify(now)).toContain("extension-itemAuthorizedDetail");
    expect(JSON.stringify(now)).toContain('"A6"');
  } finally {
  }
});
