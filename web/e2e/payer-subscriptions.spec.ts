import { test, expect, type Page } from "@playwright/test";
import { createServer, type IncomingMessage } from "node:http";
import type { AddressInfo } from "node:net";
import { openTab } from "./nav";

// The standards work added in one pass: v2 scheduling, documents and immunizations in the FHIR lab; topic subscriptions with
// real delivery to a real receiver; the 275 builder and reader; and 278 to Da Vinci PAS. Each test drives the interface and then
// checks the outcome somewhere other than the interface - the receiver's log, the bytes of a download - because a page that
// reports success is not evidence of success.

const mutating = { "X-Perfuse-Request": "1", "Content-Type": "application/json" };

async function convertSample(page: Page, sample: string, resource: string) {
  await page.getByRole("button", { name: sample }).click();
  await page.getByRole("button", { name: "Convert to FHIR" }).click();
  await expect(page.getByText(`"resourceType": "${resource}"`).first()).toBeVisible();
}

test("the FHIR lab converts appointments, notes and immunizations", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "FHIR lab");
  await convertSample(page, "Sample appointment", "Appointment");
  await convertSample(page, "Sample note", "DocumentReference");
  await convertSample(page, "Sample immunization", "Immunization");
});

test("a subscription handshakes, receives an encounter notification, and the page says so", async ({ page }) => {
  // A real receiver, recording what it is sent.
  const received: { type: string; body: any; auth: string | undefined }[] = [];
  const receiver = createServer((req: IncomingMessage, res) => {
    let data = "";
    req.on("data", (c) => (data += c));
    req.on("end", () => {
      const body = JSON.parse(data || "{}");
      const params = body.entry?.[0]?.resource?.parameter ?? [];
      const type = params.find((p: any) => p.name === "type")?.valueCode ?? "?";
      received.push({ type, body, auth: req.headers["authorization"] as string | undefined });
      res.writeHead(200).end();
    });
  });
  await new Promise<void>((r) => receiver.listen(0, "127.0.0.1", r));
  const port = (receiver.address() as AddressInfo).port;
  let token = "";

  try {
    await page.goto("/");
    const tok = await page.request.post("/api/tokens", { headers: mutating, data: { label: "e2e-fhir", role: "editor" } });
    expect(tok.ok(), await tok.text()).toBeTruthy();
    token = (await tok.json()).token as string;
    const fhir = { Authorization: `Bearer ${token}`, "Content-Type": "application/fhir+json" };

    const sub = await page.request.put("/fhir/Subscription/e2e-sub", {
      headers: fhir,
      data: {
        resourceType: "Subscription",
        status: "requested",
        reason: "e2e",
        criteria: "https://perfuse.health/fhir/SubscriptionTopic/encounter",
        _criteria: {
          extension: [
            {
              url: "http://hl7.org/fhir/uv/subscriptions-backport/StructureDefinition/backport-filter-criteria",
              valueString: "Encounter?patient=Patient/e2e-patient",
            },
          ],
        },
        channel: {
          type: "rest-hook",
          endpoint: `http://127.0.0.1:${port}/hook?token=do-not-show-me`,
          payload: "application/fhir+json",
          header: ["Authorization: Bearer receiver-secret"],
        },
      },
    });
    expect(sub.status(), await sub.text()).toBeLessThan(300);
    expect((await sub.json()).status).toBe("requested");

    await expect.poll(() => received.filter((r) => r.type === "handshake").length, { timeout: 15_000 }).toBe(1);

    const enc = await page.request.put("/fhir/Encounter/e2e-enc", {
      headers: fhir,
      data: {
        resourceType: "Encounter",
        status: "in-progress",
        class: { system: "http://terminology.hl7.org/CodeSystem/v3-ActCode", code: "EMER" },
        subject: { reference: "Patient/e2e-patient" },
      },
    });
    expect(enc.status(), await enc.text()).toBeLessThan(300);

    await expect.poll(() => received.filter((r) => r.type === "event-notification").length, { timeout: 15_000 }).toBe(1);
    const note = received.find((r) => r.type === "event-notification")!;
    expect(note.auth).toBe("Bearer receiver-secret");
    expect(JSON.stringify(note.body)).toContain("/Encounter/e2e-enc");

    await openTab(page, "Subscriptions");
    const card = page.getByTestId("subscription").filter({ hasText: "e2e-sub" });
    await expect(card.getByText("active", { exact: true })).toBeVisible();
    await expect(card.getByText("Encounter?patient=Patient/e2e-patient")).toBeVisible();
    await expect(card).toContainText("Sends headers: Authorization (values withheld)");

    // The credentials the subscriber gave the server appear nowhere on the page.
    const text = await page.locator("main").innerText();
    expect(text).not.toContain("receiver-secret");
    expect(text).not.toContain("do-not-show-me");
  } finally {
    // Deleted first, while the token still works, so the server is not left retrying a receiver that has gone away.
    if (token) {
      await page.request.delete("/fhir/Subscription/e2e-sub", { headers: { Authorization: `Bearer ${token}` } });
    }
    receiver.close();
    // Revoked, because the server is shared by the whole suite: a token left behind appears as a second table on the Users
    // view, and use-admin.spec.ts's locator("tbody") then matched two elements and failed. State a test creates is state it
    // removes.
    await page.request.delete("/api/tokens/e2e-fhir", { headers: { "X-Perfuse-Request": "1" } });
  }
});

test("a 275 is built, read back, and carries the document byte for byte", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Claims & auth");
  await page.getByRole("button", { name: "Build 275" }).click();
  await expect(page.getByTestId("attachment-result")).toContainText("byte-for-byte");
  await expect(page.getByTestId("attachment-result")).toContainText("not validated against the X12 TR3");

  const x12 = (await page.getByTestId("x12-output").innerText()).replace(/\n/g, "");
  expect(x12).toContain("ST*275*");
  expect(x12).toContain("006020X314");

  await page.getByRole("button", { name: "Read a 275" }).click();
  await page.getByLabel("X12 275 to read").fill(x12);
  await page.getByRole("button", { name: "Read 275" }).click();
  const view = page.getByTestId("attachment-view");
  await expect(view).toContainText("operative-note.xml");
  await expect(view).toContainText("BGN01 02 (unsolicited)");

  const [download] = await Promise.all([
    page.waitForEvent("download"),
    view.getByRole("button", { name: "Download the document" }).click(),
  ]);
  const path = await download.path();
  const { readFileSync } = await import("node:fs");
  expect(readFileSync(path!, "utf8")).toContain('code="11504-8"');
});

test("a 278 decision becomes a PAS ClaimResponse", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Claims & auth");
  await page.getByRole("button", { name: "278 to PAS" }).click();
  await page.getByRole("button", { name: "Convert to PAS ClaimResponse" }).click();
  await expect(page.getByTestId("priorauth-summary")).toContainText("authorisation AUTH-99120");
  const cr = page.getByTestId("claimresponse");
  await expect(cr).toContainText("profile-claimresponse");
  await expect(cr).toContainText('"preAuthRef": "AUTH-99120"');
  await expect(cr).toContainText('"code": "A1"');
});
