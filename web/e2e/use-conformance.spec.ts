import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Conformance checking, reachable from the interface.
//
// The checker was 343 lines of working, tested code with no caller anywhere outside its own package. That is the
// same defect as the AI Mapper and the certificate inspector: correct code, passing tests, and no way for anybody
// to use it. This asserts on the report arriving, not on the tab existing.

/** A document missing recordTarget and author - two failures that cause outright rejection. */
const REJECTABLE = `<?xml version="1.0" encoding="UTF-8"?>
<ClinicalDocument xmlns="urn:hl7-org:v3">
  <templateId root="2.16.840.1.113883.10.20.22.1.1"/>
  <templateId root="2.16.840.1.113883.10.20.22.1.2"/>
  <code code="34133-9" codeSystem="2.16.840.1.113883.6.1" displayName="Summarization of Episode Note"/>
  <title>Continuity of Care Document</title>
  <effectiveTime value="20260818120000-0500"/>
  <component><structuredBody>
    <component><section>
      <templateId root="2.16.840.1.113883.10.20.22.2.6.1"/>
      <code code="48765-2" codeSystem="2.16.840.1.113883.6.1"/>
      <title>Allergies</title>
      <text>No known allergies.</text>
    </section></component>
  </structuredBody></component>
</ClinicalDocument>`;

test("a document that a receiver would reject says so, and says which guide", async ({ page }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`uncaught exception: ${e.message}`));

  await page.goto("/");
  await openTab(page, "Documents");

  const box = page.locator("main textarea").first();
  await expect(box).toBeVisible({ timeout: 25_000 });
  await box.fill(REJECTABLE);
  await page.getByRole("button", { name: "Read the document" }).click();

  const tab = page.locator("main").getByRole("button", { name: /^Conformance/ });
  await expect(tab, "there is no way to see conformance findings").toBeVisible({ timeout: 25_000 });

  await tab.click();

  // The guide has to be named. "Your document is wrong" is not a conversation that goes anywhere with a supplier;
  // "it fails this statement in C-CDA R2.1" is.
  await expect(page.locator("main"), "the report does not say which guide was checked").toContainText(/C-CDA/, {
    timeout: 15_000,
  });

  // And the consequence has to be stated, since that is what decides whether anybody acts today.
  await expect(page.locator("main")).toContainText(/would cause rejection|would be rejected/i);

  expect(problems, problems.join("\n")).toEqual([]);
});
