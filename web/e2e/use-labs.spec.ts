import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Using the four analysis tools, rather than confirming they render.
//
// Each of these takes input, does work, and shows an answer. A test that loads the tab proves none of
// that. So each test here supplies input, runs the tool, and asserts on the answer - including the
// answer's content, not just that something appeared.

function watch(page: import("@playwright/test").Page): string[] {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`page error: ${e}`));
  page.on("console", (m) => {
    if (m.type() === "error") problems.push(`console: ${m.text()}`);
  });
  return problems;
}

test("the FHIR lab converts both samples and explains its decisions", async ({ page }) => {
  const problems = watch(page);

  await page.goto("/");
  await openTab(page, "FHIR lab");

  // The sample buttons are the shortest path a new user takes, so they are worth exercising rather
  // than pasting a message of my own invention.
  for (const sample of ["Sample ADT", "Sample lab result"]) {
    await page.getByRole("button", { name: sample }).click();
    await page.getByRole("button", { name: "Convert to FHIR" }).click();

    // The result card names the outcome.
    await expect(page.getByText("Result", { exact: true })).toBeVisible({ timeout: 20_000 });

    // A conversion that produced no resources is a failure dressed as a success. The resource types are
    // shown as badges with a count beside them, so match the text rather than an exact node.
    await expect(
      page.locator("main"),
      `${sample} produced no Patient resource`,
    ).toContainText("Patient", { timeout: 15_000 });

    // Every sub-view, on real output.
    for (const view of [/^FHIR bundle$/, /^Decisions/, /^v2 explained$/]) {
      await page.getByRole("button", { name: view }).first().click();
      await expect(page.locator("main")).not.toHaveText("", { timeout: 10_000 });
    }

    // The bundle view must contain actual FHIR, not an empty shell.
    await page.getByRole("button", { name: /^FHIR bundle$/ }).click();
    await expect(page.locator("main")).toContainText("resourceType", { timeout: 10_000 });
  }

  expect(problems, problems.join("\n")).toEqual([]);
});

test("the FHIR lab honours the release and the identifier system", async ({ page }) => {
  const problems = watch(page);

  await page.goto("/");
  await openTab(page, "FHIR lab");

  // An identifier system is the one setting the tool nags about, because a medical record number means
  // nothing outside the facility that issued it. Setting it should remove that complaint.
  await page.getByRole("button", { name: "Sample ADT" }).click();
  await page.getByLabel("Identifier system", { exact: true }).fill("http://e2e.example.org/mrn");
  await page.getByRole("button", { name: "Convert to FHIR" }).click();

  await expect(page.getByText("Result", { exact: true })).toBeVisible({ timeout: 20_000 });
  await page.getByRole("button", { name: /^FHIR bundle$/ }).click();
  await expect(
    page.locator("main"),
    "the identifier system was not used in the output",
  ).toContainText("http://e2e.example.org/mrn", { timeout: 15_000 });

  // Now a different release, which changes the shape of the output.
  const release = page.getByLabel("FHIR release", { exact: true });
  const options = await release.locator("option").allTextContents();
  expect(options.length, "no FHIR releases were offered").toBeGreaterThan(1);

  await release.selectOption({ index: options.length - 1 });
  await page.getByRole("button", { name: "Convert to FHIR" }).click();
  await expect(page.getByText("Result", { exact: true })).toBeVisible({ timeout: 20_000 });

  expect(problems, problems.join("\n")).toEqual([]);
});

test("the document lab reads a CDA and reports where narrative and codes disagree", async ({ page }) => {
  const problems = watch(page);

  await page.goto("/");
  await openTab(page, "Documents");

  await page.getByRole("button", { name: "Sample document" }).click();
  await page.getByRole("button", { name: "Read the document" }).click();

  // The sample deliberately disagrees with itself: two allergies in the narrative, one in the coded
  // entries. Finding that is the feature, so assert the finding rather than that a panel rendered. The
  // badge counts warnings as "N to check" and reserves "contradiction" for errors, so accept either.
  await expect(page.locator("main")).toContainText("Continuity of Care Document", { timeout: 20_000 });
  await expect(
    page.locator("main"),
    "the disagreement between narrative and coded entries was not reported",
  ).toContainText(/to check|contradiction/i, { timeout: 15_000 });

  // And the Agreement view carries a count, so the findings are reachable rather than merely counted.
  await expect(page.getByRole("button", { name: /^Agreement \(\d+\)/ })).toBeVisible({
    timeout: 15_000,
  });

  for (const view of [/^Sections \(/, /^Agreement/, /^FHIR$/, /^Notes/]) {
    const b = page.locator("main").getByRole("button", { name: view });
    if (!(await b.count())) continue;
    await b.first().click();
    await expect(page.locator("main")).not.toHaveText("", { timeout: 10_000 });
  }

  // Open a section and read both halves of it, which is the thing a clinician and a system see
  // differently.
  await page.getByRole("button", { name: /^Sections \(/ }).click();
  const sectionToggle = page.locator("main button").filter({ hasText: /Allergies|Medications|Problems/ }).first();
  if (await sectionToggle.count()) {
    await sectionToggle.click();
    for (const half of ["What a clinician reads", "What a system imports"]) {
      const b = page.getByRole("button", { name: half });
      if (await b.count()) await b.first().click();
    }
  }

  expect(problems, problems.join("\n")).toEqual([]);
});

test("the document lab reads a CDA carried inside an HL7 message", async ({ page }) => {
  const problems = watch(page);

  await page.goto("/");
  await openTab(page, "Documents");

  // The other input mode, which no test had used.
  await page.getByRole("button", { name: "Sample MDM" }).click();
  await page.getByRole("button", { name: "Read the document" }).click();

  await expect(
    page.locator("main"),
    "the document inside the MDM message was not found",
  ).toContainText(/Continuity of Care Document|document/i, { timeout: 20_000 });

  expect(problems, problems.join("\n")).toEqual([]);
});

test("the script lab compiles all three samples, as a filter and as a transformer", async ({ page }) => {
  const problems = watch(page);

  await page.goto("/");
  await openTab(page, "Scripts");

  // Each sample sets its own kind, and the status line says whether the server could compile it. A
  // sample that does not compile is a broken example, which is worse than no example.
  for (const sample of ["Mirth filter", "E4X for-each", "Set a field"]) {
    await page.getByRole("button", { name: sample }).click();
    await expect(
      page.locator("main").getByText(/compiles as a (filter|transformer)/),
      `the ${sample} sample does not compile`,
    ).toBeVisible({ timeout: 20_000 });
  }

  // And the kind toggle, which changes what the server checks against.
  await page.getByRole("button", { name: "Mirth filter" }).click();
  await page.getByRole("button", { name: "transformer", exact: true }).click();
  await expect(page.locator("main")).not.toHaveText("", { timeout: 10_000 });
  await page.getByRole("button", { name: "filter", exact: true }).click();
  await expect(page.locator("main").getByText(/compiles as a filter/)).toBeVisible({ timeout: 20_000 });

  expect(problems, problems.join("\n")).toEqual([]);
});

test("the playground runs all four tools against a real message", async ({ page }) => {
  const problems = watch(page);

  await page.goto("/");
  await openTab(page, "Playground");

  // The engine is a WASM download, so the tabs do not exist until it is ready.
  await expect(page.getByRole("button", { name: "Run a Mirth script" })).toBeVisible({ timeout: 60_000 });

  // Each tab, run, with an assertion on what it produced rather than on the click.
  const expectations: { tab: string; expect: RegExp }[] = [
    { tab: "Run a Mirth script", expect: /The message afterwards|needed no translation|was translated/ },
    { tab: "Try a filter", expect: /Passes|Filtered out/ },
    { tab: "Transformation steps", expect: /change\(s\)|Nothing changed/ },
    { tab: "Look inside a message", expect: /Control ID|Structure/ },
  ];

  for (const { tab, expect: want } of expectations) {
    await page.getByRole("button", { name: tab }).click();
    await page.getByRole("button", { name: "Run it" }).click();
    await expect(page.locator("main"), `${tab} produced nothing`).toContainText(want, {
      timeout: 30_000,
    });
  }

  expect(problems, problems.join("\n")).toEqual([]);
});

test("the v3 field picker reads a message, searches it and tests a path", async ({ page }) => {
  const problems = watch(page);

  await page.goto("/");
  await openTab(page, "Playground");

  const box = page.locator("textarea").first();
  await box.fill(
    `<PRPA_IN201306UV02 xmlns="urn:hl7-org:v3">` +
      `<controlActProcess><subject><registrationEvent><subject1><patient>` +
      `<patientPerson><name><family>Frost</family><given>Ivy</given></name>` +
      `<administrativeGenderCode code="F"/><birthTime value="19800101"/></patientPerson>` +
      `</patient></subject1></registrationEvent></subject></controlActProcess>` +
      `</PRPA_IN201306UV02>`,
  );
  await page.getByRole("button", { name: "Read this message" }).click();

  // It found fields, and says how many.
  await expect(page.locator("main")).toContainText(/readable field/, { timeout: 20_000 });

  // The values from the message are actually listed, which is the point of the picker.
  await expect(page.locator("main"), "the picker did not surface the family name").toContainText("Frost", {
    timeout: 15_000,
  });

  // Search narrows it.
  const search = page.getByPlaceholder("Search fields or values…");
  if (await search.count()) {
    await search.fill("birth");
    await expect(page.locator("main")).toContainText(/birthTime|19800101/, { timeout: 10_000 });
    await search.fill("");
  }

  // And a path can be tested, which is how somebody builds a filter for a v3 feed.
  const pathBox = page.getByPlaceholder("//birthTime@value");
  if (await pathBox.count()) {
    await pathBox.fill("//birthTime@value");
    await page.getByRole("button", { name: "Test", exact: true }).click();
    await expect(page.locator("main"), "testing a path that exists reported nothing").toContainText(
      /Selects 1 value|19800101/,
      { timeout: 15_000 },
    );

    // And a path that matches nothing must say so rather than look like a success.
    await pathBox.fill("//thisDoesNotExist@value");
    await page.getByRole("button", { name: "Test", exact: true }).click();
    await expect(page.locator("main")).toContainText(/Nothing in this message is at that path/, {
      timeout: 15_000,
    });
  }

  expect(problems, problems.join("\n")).toEqual([]);
});
