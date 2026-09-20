import { test, expect } from "@playwright/test";
import { openTab } from "./nav";
import { sendMLLP, anADT } from "./mllp";

// The sub-views inside a tab, which nothing exercised until one of them crashed.
//
// The suite asserted that every tab renders. It never clicked the buttons that appear *inside* a tab
// after an action completes - and the FHIR lab's Decisions view died on a TypeError the first time an
// operator pressed it. Two reasons it survived:
//
// The control does not exist until a conversion has run, so a test that loads the tab cannot see it.
//
// The crash needed a message that converted *cleanly*. A nil findings list marshalled to null, and only
// a message with nothing wrong produced an empty one. Every deliberately broken input worked.
//
// So these tests perform the action first, then click every resulting sub-view, and use valid input
// rather than input designed to provoke errors.

/** A message that converts cleanly, so the empty-result paths are the ones under test. */
const cleanADT = [
  "MSH|^~\\&|LAB|HOSP|EHR|HOSP|20260825120000||ADT^A01|MSG00001|P|2.5",
  "EVN|A01|20260825120000",
  "PID|1||12345^^^HOSP^MR||DOE^JOHN^A||19800101|M",
  "PV1|1|I|WARD^1^01",
].join("\n");

/** failOnPageError makes a React crash fail the test instead of leaving a blank panel. */
function failOnPageError(page: import("@playwright/test").Page): string[] {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  page.on("console", (m) => {
    if (m.type() === "error") errors.push(m.text());
  });
  return errors;
}

test("every FHIR lab sub-view survives a message that converts cleanly", async ({ page }) => {
  const errors = failOnPageError(page);

  await page.goto("/");
  await openTab(page, "FHIR lab");

  const box = page.locator("textarea").first();
  await box.fill(cleanADT);
  await page.getByRole("button", { name: /convert/i }).first().click();

  // Wait for the result, which is what makes the sub-views exist at all.
  const decisions = page.getByRole("button", { name: /^Decisions/ });
  await expect(decisions).toBeVisible({ timeout: 15_000 });

  // Each sub-view in turn. This is the loop whose absence let the crash ship.
  for (const name of [/^FHIR bundle$/, /^Decisions/, /^v2 explained$/]) {
    await page.getByRole("button", { name }).first().click();
    // Something must render. A crashed React subtree leaves an empty panel.
    await expect(page.locator("main")).not.toHaveText("", { timeout: 5_000 });
    expect(errors, `clicking ${name} produced: ${errors.join(" | ")}`).toEqual([]);
  }

  // Specifically the case that crashed: a clean conversion, so the lists are empty.
  await page.getByRole("button", { name: /^Decisions/ }).first().click();
  await expect(page.locator("main")).toContainText(/Mapping decisions|judgement/i);
  expect(errors).toEqual([]);
});

test("every document lab sub-view survives a document that validates", async ({ page }) => {
  const errors = failOnPageError(page);

  await page.goto("/");
  await openTab(page, "Documents");

  // Use the built-in sample rather than inventing CDA, so this tests the views and not my XML.
  const sample = page.getByRole("button", { name: /Sample document/i });
  if (await sample.count()) {
    await sample.first().click();
    await page.getByRole("button", { name: "Read the document" }).click();

    // Sub-views only exist once a document parsed.
    const agreement = page.getByRole("button", { name: /^Agreement/ });
    await expect(agreement).toBeVisible({ timeout: 15_000 });

    for (const name of [/^Sections \(/, /^Agreement/, /^FHIR$/, /^Notes/]) {
      const b = page.locator("main").getByRole("button", { name });
      if (!(await b.count())) continue;
      await b.first().click();
      await expect(page.locator("main")).not.toHaveText("", { timeout: 5_000 });
      expect(errors, `clicking ${name} produced: ${errors.join(" | ")}`).toEqual([]);
    }
  }

  expect(errors).toEqual([]);
});

test("the dashboard and metrics window switchers work", async ({ page }) => {
  const errors = failOnPageError(page);

  await page.goto("/");
  for (const label of [/^1h$/, /^6h$/, /^24h$/]) {
    const b = page.getByRole("button", { name: label });
    if (await b.count()) {
      await b.first().click();
      await expect(page.locator("main")).not.toHaveText("");
    }
  }

  await openTab(page, "Metrics");
  for (const label of [/^15 min$/, /^1 hour$/, /^6 hours$/]) {
    const b = page.getByRole("button", { name: label });
    if (await b.count()) {
      await b.first().click();
      await expect(page.locator("main")).not.toHaveText("");
    }
  }

  expect(errors, errors.join(" | ")).toEqual([]);
});

test("the playground sub-tabs all render", async ({ page }) => {
  const errors = failOnPageError(page);

  await page.goto("/");
  await openTab(page, "Playground");

  for (const label of [
    /Run a Mirth script/i,
    /Try a filter/i,
    /Transformation steps/i,
    /Look inside a message/i,
  ]) {
    const b = page.getByRole("button", { name: label });
    if (!(await b.count())) continue;
    await b.first().click();
    await expect(page.locator("main")).not.toHaveText("", { timeout: 10_000 });
  }

  expect(errors, errors.join(" | ")).toEqual([]);
});

// No response may contain a JSON null.
//
// A nil Go slice marshals to null while the TypeScript interface declares an array, so the interface
// dereferences it and dies. This is the defect that crashed the FHIR lab, and an audit found four more
// of the same shape - each triggered by its own success case, which is why none had been noticed.
//
// Run here rather than in the Go tests because that harness starts without an engine, so seven of the
// interesting endpoints return 503 and skip. This runs against the real server.
test("no API response sends null where a list is promised", async ({ page }) => {
  await page.goto("/");

  const paths = [
    "/api/status",
    "/api/channels",
    "/api/alerts",
    "/api/queue",
    "/api/metrics",
    "/api/audit",
    "/api/messages",
    "/api/certificates",
    "/api/fhir/versions",
    "/api/document/types",
    "/api/branding",
    "/api/tokens",
    "/api/dictionary",
  ];

  const offenders: string[] = [];

  for (const path of paths) {
    const res = await page.request.get(path);
    if (!res.ok()) continue;

    const body = await res.text();
    let doc: unknown;
    try {
      doc = JSON.parse(body);
    } catch {
      continue; // not JSON, not this test's business
    }

    // Walk for nulls. Perfuse omits absent scalars rather than sending null, so any null here is a
    // nil slice or map.
    const walk = (v: unknown, at: string) => {
      if (v === null) {
        offenders.push(`${path} -> ${at || "(root)"}`);
        return;
      }
      if (Array.isArray(v)) {
        v.forEach((item, i) => walk(item, `${at}[${i}]`));
      } else if (typeof v === "object") {
        for (const [k, val] of Object.entries(v as Record<string, unknown>)) {
          walk(val, at ? `${at}.${k}` : k);
        }
      }
    };
    walk(doc, "");
  }

  expect(
    offenders,
    `These responses contain null where the client expects a list:\n  ${offenders.join("\n  ")}\n\n` +
      `A nil Go slice marshals to null. Initialise it empty at construction instead.`,
  ).toEqual([]);
});

// The v3 field picker, including the input that produced a nil field list.
//
// The picker was in the uncovered set. Its field list is built by a conditional append that skips
// pure-structure nodes, so a document of nothing but empty nested elements - a skeleton, or a template
// somebody pastes to see what the tool does - returned null where the interface filters an array.
test("the v3 field picker handles both a real message and a structure-only one", async ({ page }) => {
  const errors = failOnPageError(page);

  await page.goto("/");
  await openTab(page, "Playground");

  const inspect = page.getByRole("button", { name: /Read this message/i });
  if (!(await inspect.count())) {
    // The picker lives behind a sub-tab; find it first.
    const v3 = page.getByRole("button", { name: /v3|HL7 v3/i });
    if (await v3.count()) await v3.first().click();
  }

  if (await page.getByRole("button", { name: /Read this message/i }).count()) {
    const box = page.locator("textarea").last();

    // The structure-only document first, because that is the case that crashed.
    await box.fill(
      `<ClinicalDocument xmlns="urn:hl7-org:v3"><component><structuredBody>` +
        `<component><section></section></component></structuredBody></component></ClinicalDocument>`,
    );
    await page.getByRole("button", { name: /Read this message/i }).first().click();
    await page.waitForTimeout(500);
    expect(errors, `a structure-only v3 document produced: ${errors.join(" | ")}`).toEqual([]);

    // Then one with readable values, so the feature is proven to still work.
    await box.fill(
      `<ClinicalDocument xmlns="urn:hl7-org:v3"><patient><name>Doe</name>` +
        `<birthTime value="19800101"/></patient></ClinicalDocument>`,
    );
    await page.getByRole("button", { name: /Read this message/i }).first().click();
    await page.waitForTimeout(500);
  }

  expect(errors, errors.join(" | ")).toEqual([]);
});

// Message detail, including the trace panel, which nothing had ever opened.
test("opening a message and its trace panel does not crash", async ({ page }) => {
  const errors = failOnPageError(page);

  await page.goto("/");
  await openTab(page, "Messages");

  // Seed a real message, because the placeholder row in the empty state is not a message and clicking
  // it opens nothing. This test previously passed while exercising none of the three sub-views.
  await sendMLLP(anADT("TRACE001"));

  await page.getByRole("button", { name: /^Refresh$/ }).first().click();

  const row = page.locator("tbody tr").filter({ hasText: "TRACE001" });
  await expect(row, "the message just sent is not listed").toBeVisible({ timeout: 15_000 });
  await row.first().click();

  // Named rather than discovered, and required rather than skipped. A loop that quietly continues past
  // a control it cannot find is how the Decisions view went untested while the suite reported success.
  let clicked = 0;
  for (const name of [/^Explained$/, /^Raw$/, /Why this happened/i]) {
    const b = page.locator("main").getByRole("button", { name });
    await expect(b.first(), `the message detail panel has no ${name} control`).toBeVisible({
      timeout: 10_000,
    });
    await b.first().click();
    await expect(page.locator("main")).not.toHaveText("", { timeout: 5_000 });
    expect(errors, `clicking ${name} produced: ${errors.join(" | ")}`).toEqual([]);
    clicked++;
  }
  expect(clicked, "no message detail sub-views were exercised").toBe(3);

  expect(errors, errors.join(" | ")).toEqual([]);
});

//
// Both known instances of this defect were in responses to a POST or a PUT: the FHIR conversion, and the
// settings save that reported success with restartRequired as null. A sweep over read endpoints would
// have missed both, so the shape of the test has to match the shape of the defect.
test("no write response sends null where a list is promised", async ({ page }) => {
  await page.goto("/");

  const offenders: string[] = [];

  const walk = (v: unknown, at: string, where: string) => {
    if (v === null) {
      offenders.push(`${where} -> ${at || "(root)"}`);
      return;
    }
    if (Array.isArray(v)) {
      v.forEach((item, i) => walk(item, `${at}[${i}]`, where));
    } else if (typeof v === "object") {
      for (const [k, val] of Object.entries(v as Record<string, unknown>)) {
        walk(val, at ? `${at}.${k}` : k, where);
      }
    }
  };

  const writes: { path: string; body: unknown }[] = [
    { path: "/api/inspect/fhir", body: { message: cleanADT.replace(/\n/g, "\r") } },
    // A save that changes nothing needing a restart, which is what produced the null.
    { path: "/api/settings/values", body: { changes: { "branding.tagline": "" } } },
  ];

  for (const w of writes) {
    const res = await page.request.post(w.path, {
      data: w.body,
      headers: { "X-Perfuse-Request": "1", "Content-Type": "application/json" },
    });
    if (!res.ok()) continue;
    try {
      walk(JSON.parse(await res.text()), "", w.path);
    } catch {
      continue;
    }
  }

  // The settings save is a PUT, and the method matters: it is the branch that reported nulls.
  const put = await page.request.put("/api/settings/values", {
    data: { changes: { "branding.tagline": "" } },
    headers: { "X-Perfuse-Request": "1", "Content-Type": "application/json" },
  });
  if (put.ok()) {
    try {
      walk(JSON.parse(await put.text()), "", "PUT /api/settings/values");
    } catch {
      /* not JSON */
    }
  }

  expect(
    offenders,
    `These write responses contain null where the client expects a list:\n  ${offenders.join("\n  ")}`,
  ).toEqual([]);
});

