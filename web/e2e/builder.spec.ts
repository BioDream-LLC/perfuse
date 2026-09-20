import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Builds channels through the form the way an operator does, then checks the file that results and the form that comes back.
//
// The tab sweep only proved each panel renders. This exercises the round trip nobody had: form to YAML, YAML to disk, disk back to
// form. Three representations of the same channel, any pair of which can disagree.

/** watch collects console errors and uncaught exceptions for the life of a page. */
function watch(page: import("@playwright/test").Page): string[] {
  const problems: string[] = [];
  page.on("console", (m) => {
    if (m.type() === "error") problems.push(`console: ${m.text()}`);
  });
  page.on("pageerror", (e) => problems.push(`uncaught: ${e.message}`));
  page.on("response", (r) => {
    if (r.status() >= 500) problems.push(`${r.status()} from ${new URL(r.url()).pathname}`);
  });

  return problems;
}

/**
 * yamlPreview reads the generated channel file the builder shows live.
 *
 * It is a pre, not a textarea. Reaching for the last textarea instead - which is the unrelated "fields to move out" box - reported
 * all ten templates as generating nothing, which was a wrong test rather than ten bugs.
 */
async function yamlPreview(
  page: import("@playwright/test").Page,
  expect_?: string,
): Promise<string> {
  const pre = page.locator("pre").first();
  // Not waited for unconditionally any more. The preview panel now shows an explanation instead of a pre
  // when the draft cannot yet be written as a file, so the element is legitimately absent for a moment
  // after a change - and waiting for it to be visible then fails as a timeout rather than as a finding.
  //
  // Forty-five seconds, not fifteen. This sub-wait sits inside a test with a two-minute budget and was the
  // first thing to give way when the machine was busy - it failed the whole test in a quarter of the time
  // the test was allowed, which reported a broken template and meant a loaded laptop. The builder itself
  // reaches a visible preview in about 1.4 seconds when measured, so anything near this ceiling is a real
  // fault and will still be reported as one.
  await page
    .locator("pre, p:has-text('cannot be written as a channel file yet')")
    .first()
    .waitFor({ state: "visible", timeout: 45_000 });

  // The preview is rebuilt by a debounced request as fields change, so it can briefly be empty - and,
  // more awkwardly, it is briefly the *previous* draft. The form builds its default draft on mount and
  // again when a template is applied, so waiting only for non-empty text can return the default
  // channel, which has one nameless destination and is correctly refused by the loader. That produced a
  // failure that read like a broken template and was really this poll finishing too early.
  //
  // So when the caller knows what it is waiting for, wait for that.
  for (let i = 0; i < 40; i++) {
    const text = (await pre.count()) ? ((await pre.textContent()) ?? "") : "";
    if (text.trim() && (!expect_ || text.includes(expect_))) return text;
    await page.waitForTimeout(250);
  }

  return (await pre.count()) ? ((await pre.textContent()) ?? "") : "";
}

/** openBuilder gets to a new channel form from wherever the app starts. */
async function openBuilder(page: import("@playwright/test").Page, template: RegExp) {
  await page.goto("/");
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();
  await page.getByRole("button", { name: template }).click();
  // The name field is the first thing on the form proper, so its presence means the template has been applied.
  await expect(page.getByPlaceholder("adt-inbound")).toBeVisible();
}

test("a channel built through the form saves, and comes back the same", async ({ page }) => {
  const problems = watch(page);

  await openBuilder(page, /Turn HL7 into FHIR/);

  const name = "gui-built-fhir";
  await page.getByPlaceholder("adt-inbound").fill(name);
  await page.getByPlaceholder("ADT from the hospital, forwarded to the registry").fill("Built through the GUI");
  await page.getByPlaceholder(":6661").fill("127.0.0.1:0");

  // Read the YAML the form generated, before saving. This is what the operator is shown and told will be written.
  const shownBefore = await yamlPreview(page);

  await page.getByRole("button", { name: "Create channel" }).click();

  // Saving returns to the list, so the channel appearing there is the signal it worked.
  await expect(page.getByText(name, { exact: false }).first()).toBeVisible({ timeout: 15_000 });

  // Now reopen it. This is the leg that was never exercised: the saved file parsed back into form state.
  await page.getByText(name, { exact: false }).first().click();
  await expect(page.getByPlaceholder("adt-inbound")).toBeVisible({ timeout: 10_000 });

  // The name and description must survive.
  await expect(page.getByPlaceholder("adt-inbound")).toHaveValue(name);
  await expect(page.getByPlaceholder("ADT from the hospital, forwarded to the registry")).toHaveValue(
    "Built through the GUI",
  );

  expect(problems, `problems while building and reopening a channel:\n${problems.join("\n")}`).toEqual([]);
  expect(shownBefore, "the form generated no YAML").toContain(`name: ${name}`);
});

test("every template produces a channel the engine accepts", async ({ page }) => {
  // Nine templates, each reopened from the list and each waiting on a debounced preview. That does not
  // fit the default per-test budget once the suite has other work in flight, and it failed as a timeout
  // rather than as a finding - which reads like a broken template.
  test.setTimeout(120_000);

  const problems = watch(page);

  // Each template is a claim that this configuration works. A template that generates a channel the loader refuses is worse than
  // no template, because the operator reasonably assumes the offered starting points are valid ones.
  // Paired with the channel name each one produces, so the preview can be waited on specifically
  // rather than waited on for merely being non-empty.
  const templates: { label: RegExp; produces: string }[] = [
    { label: /Record an HL7 feed/, produces: "hl7-archive" },
    { label: /Pass a feed to another system/, produces: "hl7-forward" },
    { label: /Send admissions one way and re/, produces: "hl7-router" },
    { label: /Turn HL7 into FHIR/, produces: "hl7-to-fhir" },
    { label: /Accept messages posted over HT/, produces: "http-in" },
    { label: /Collect files from an SFTP ser/, produces: "sftp-in" },
    { label: /Send messages queued in a data/, produces: "db-in" },
    { label: /Take in X12 claims/, produces: "claims-in" },
    { label: /Clean up a feed as it passes t/, produces: "hl7-normalise" },
  ];

  // "Start from nothing" is deliberately excluded. It is an empty channel, so being refused is the correct answer, not a defect -
  // it is checked separately below for whether it says *what* is missing.

  const failures: string[] = [];

  for (const [i, { label: template, produces }] of templates.entries()) {
    await openBuilder(page, template);

    const yaml = await yamlPreview(page, `name: ${produces}`);
    if (!yaml.trim()) {
      failures.push(`template ${i} (${template}) generated empty YAML`);

      continue;
    }
    // If the preview never became this template's YAML, say so rather than reporting the default
    // channel's problems as though they were the template's.
    if (!yaml.includes(`name: ${produces}`)) {
      failures.push(
        `template ${i} (${template}) preview never showed name: ${produces}; ` +
          `got a channel named ${/^name:.*$/m.exec(yaml)?.[0] ?? "(none)"}`,
      );

      continue;
    }

    // Check it through the same endpoint the form uses, which is the engine's own loader rather than a copy of its rules.
    const res = await page.request.post("/api/channels/validate", {
      headers: { "X-Perfuse-Request": "1" },
      data: { yaml: yaml.replace(/^name:.*$/m, `name: tmpl-check-${i}`) },
    });

    if (res.status() >= 500) {
      failures.push(`template ${i} (${template}): validate returned ${res.status()}`);

      continue;
    }

    const body = await res.json().catch(() => null);
    if (!body) {
      failures.push(`template ${i} (${template}): validate returned no JSON`);

      continue;
    }
    if (!body.ok) {
      const msgs = (body.problems ?? []).map((p: { message?: string }) => p.message).join("; ");
      failures.push(`template ${i} (${template}) is refused by the loader: ${msgs}`);
    }
  }

  expect(failures, `templates the engine will not accept:\n${failures.join("\n")}`).toEqual([]);
  expect(problems, `console problems while walking the templates:\n${problems.join("\n")}`).toEqual([]);
});

test("the empty template says what is missing rather than just failing", async ({ page }) => {
  const problems = watch(page);

  await openBuilder(page, /Start from nothing/);

  const yaml = await yamlPreview(page);
  expect(yaml.trim(), "the empty template generated nothing at all").not.toBe("");

  const res = await page.request.post("/api/channels/validate", {
    headers: { "X-Perfuse-Request": "1" },
    data: { yaml: yaml.replace(/^name:.*$/m, "name: empty-check") },
  });
  const body = await res.json();

  // Being refused is right. Being refused without saying which fields are blank is what would leave someone stuck on a form with
  // a red banner and no next action.
  expect(body.ok, "an empty channel was accepted, so the loader is not checking required fields").toBe(false);

  const said = ((body.problems ?? []) as { message?: string }[]).map((p) => p.message ?? "").join(" ");
  expect(said, "the refusal does not mention the destination name").toMatch(/name/i);
  expect(said, "the refusal does not mention the missing address").toMatch(/address/i);

  expect(problems, `console problems on the empty template:\n${problems.join("\n")}`).toEqual([]);
});
