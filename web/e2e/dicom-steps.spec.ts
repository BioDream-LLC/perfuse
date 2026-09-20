import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Imaging transformations on a DICOM channel, operated rather than inspected.
//
// Every assertion is on the generated file. A section that renders and writes the wrong key would pass any
// test that only checked the editor was visible, which is the failure this suite exists to catch - and it is
// the failure that actually happened here: the emitter wrote the steps and the reader ignored them, so a
// channel edited through the form came back without its transformations.
//
// The absence of a path field is asserted deliberately. The four actions are named because an object is
// binary with pixel data inside it: a general tag writer produces an image that opens and is wrong, which
// whoever reads the study cannot detect. If a path input ever appears here, that decision has been undone by
// accident and this test is where it should surface.
//
// # A caveat about running these
//
// playwright.config.ts has no webServer. It points at PERFUSE_E2E_URL, an externally managed server serving
// assets embedded in the Go binary, so a change to a .tsx file is invisible here until the binary is rebuilt
// and that server restarted. Against a stale server these will fail while the component tests in
// src/DICOMTransformations.test.tsx - which run from source and are inside make check - pass. That is not a
// contradiction, and the component tests are the ones to trust about the form's behaviour.

function watch(page: import("@playwright/test").Page): string[] {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`page error: ${e}`));
  page.on("console", (m) => {
    if (m.type() !== "error") return;
    if (/Failed to load resource/.test(m.text())) return;
    problems.push(`console: ${m.text()}`);
  });
  return problems;
}

async function openNewChannelForm(page: import("@playwright/test").Page) {
  await page.goto("/");
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();
  await page.getByRole("button", { name: "Start from nothing" }).click();
}

async function generated(page: import("@playwright/test").Page, contains?: string): Promise<string> {
  const preview = page.locator("pre").first();

  // If the preview never appears, say what the panel is showing instead.
  //
  // There is no preview element at all until the first build returns: the panel renders a paragraph saying either "Nothing to show yet"
  // or "This cannot be written as a channel file yet: ..." with the server's own words. Both are useful and neither reaches a test that
  // only reports "element(s) not found", which is what a bare locator gives you and what cost an investigation on 19 September - the
  // message named the absence of an element and said nothing about the build having failed or merely being slow.
  try {
    await expect(preview).toBeVisible({ timeout: 15_000 });
  } catch (error) {
    const panel = await page.getByText(/Nothing to show yet|cannot be written as a channel file/).first().textContent();

    throw new Error(
      `the generated file preview never appeared. The panel says: ${panel ?? "nothing recognisable"}\n` +
        `A build error there means the server refused the draft; "Nothing to show yet" means the build had not returned. ` +
        `Original: ${String(error).split("\n")[0]}`,
    );
  }

  // Waits for what the caller expects, rather than for the preview to stop changing.
  //
  // Two versions of this were wrong in opposite directions. The first read the preview the instant it appeared, which raced the
  // debounced rebuild and failed under load. The second waited for two identical reads and called that settled - but the preview is
  // equally still in the moment before the rebuild begins, so "finished" and "not started" are the same observation. It returned the
  // pre-click file and the test reported a missing key while the feature worked.
  //
  // Polling for the expected content has neither problem: there is exactly one string that ends the wait, and it cannot be produced
  // by the old state. Callers with nothing particular in mind still get the settled behaviour, which is fine for them because they
  // are not asserting on a change they just made.
  let last = "";
  for (let i = 0; i < 150; i += 1) {
    const now = (await preview.textContent()) ?? "";

    if (contains !== undefined) {
      if (now.includes(contains)) return now;
    } else if (now !== "" && now === last) {
      return now;
    }

    last = now;
    await page.waitForTimeout(100);
  }

  throw new Error(
    contains !== undefined
      ? `the generated file never contained ${JSON.stringify(contains)}. Last seen:\n${last.slice(0, 600)}`
      : `the generated file preview never settled. Last seen ${last.length} characters:\n${last.slice(0, 400)}`,
  );
}

async function startImagingChannel(
  page: import("@playwright/test").Page,
  name: string,
) {
  await openNewChannelForm(page);
  await page.getByLabel("Channel name", { exact: true }).fill(name);
  await page
    .getByLabel("Message format", { exact: true })
    .selectOption("dicom");
}

test("a de-identify step typed into the form reaches an imaging channel file", async ({
  page,
}) => {
  const problems = watch(page);
  await startImagingChannel(page, "imaging-deid");

  await page.getByRole("button", { name: /Remove the patient/i }).click();

  const yaml = await generated(page, "dicom:");

  expect(yaml).toContain("dicom:");
  expect(yaml).toContain("transformations:");
  expect(yaml).toContain("deidentify:");

  // The v3 block must not appear. Both editors render a list of named actions and the server refuses an
  // hl7v3 block on an imaging channel, so writing the wrong key produces a file that cannot load.
  expect(yaml).not.toContain("hl7v3:");

  expect(problems).toEqual([]);
});

test("the private tag keep list survives as a list rather than a string", async ({
  page,
}) => {
  const problems = watch(page);
  await startImagingChannel(page, "imaging-strip");

  await page.getByRole("button", { name: /Strip private tags/i }).click();

  // Two tags, one per line. The comma belongs inside a tag, so it cannot separate them - the form used to
  // split on commas and produced four entries that are not tags, and the server refused the file with an
  // error naming a value nobody typed.
  await page.getByLabel(/keep/i).fill("0009,0010\n0029,1010");

  const yaml = await generated(page, "strip_private:");

  expect(yaml).toContain("strip_private:");
  expect(yaml).toContain("keep:");

  // The pairs must survive whole. Halves here mean the file will be refused at load.
  expect(yaml).toMatch(/-\s*"?0009,0010"?/);
  expect(yaml).toMatch(/-\s*"?0029,1010"?/);
  expect(yaml).not.toMatch(/-\s*"?0009"?\s*$/m);

  expect(problems).toEqual([]);
});

test("the imaging editor offers named actions and no path field", async ({
  page,
}) => {
  const problems = watch(page);
  await startImagingChannel(page, "imaging-actions");

  for (const action of [
    /Remove the patient/i,
    /Strip private tags/i,
    /Set the institution/i,
    /Rewrite the AE titles/i,
  ]) {
    await expect(page.getByRole("button", { name: action })).toBeVisible();
  }

  await page.getByRole("button", { name: /Set the institution/i }).click();

  // The absence is the design. A path input here would mean somebody had added a general tag writer, which
  // can produce an image that opens and is wrong.
  await expect(page.getByLabel(/^path$/i)).toHaveCount(0);

  const yaml = await generated(page, "set_institution:");
  expect(yaml).toContain("set_institution:");

  // Not asserted on the whole file: the default http source writes its own path key, so a file-wide check
  // would fail for a reason that has nothing to do with the imaging editor. The absent input above is the
  // assertion that means something.

  expect(problems).toEqual([]);
});

test("an imaging channel does not write a dicom block when the format changes away", async ({
  page,
}) => {
  const problems = watch(page);
  await startImagingChannel(page, "imaging-then-hl7");

  await page.getByRole("button", { name: /Remove the patient/i }).click();

  // Through the helper, like every other preview assertion in this file.
  //
  // This was a bare locator with the default ten second budget, and it was the only place here that asserted on the preview's contents
  // without first waiting for the preview to exist. It failed once in a full run with "element(s) not found" - which is what a locator
  // matching nothing reports, and which says nothing about why. There is no preview element until the first build returns, so the
  // assertion was racing a server round-trip while looking as though it were racing a render.
  await generated(page, "deidentify:");

  // Switching format must drop the block rather than leave it behind. The server refuses a dicom block on an
  // HL7 channel, so a leftover produces a file that will not load - and the person who switched format has no
  // reason to look for it.
  await page.getByLabel("Message format", { exact: true }).selectOption("hl7");

  // A retrying assertion rather than reading the preview once. The first version of this test read it
  // immediately after the change and saw the previous render, which reported a leak that was not there - a
  // test failing for a reason unrelated to the thing it names is worse than no test, because the obvious
  // response is to go looking in the emitter.
  //
  // Twenty seconds rather than the default five. An absence assertion has to wait out the debounced rebuild, and on a loaded machine
  // that exceeded the default - reporting a leaked dicom block that was not there, which sends somebody into the emitter looking for
  // a bug in code that is correct. The assertion still means exactly what it says; only the budget changed.
  const preview = page.locator("pre").first();
  await expect(preview).not.toContainText("dicom:", { timeout: 20_000 });
  await expect(preview).not.toContainText("deidentify:", { timeout: 20_000 });

  expect(problems).toEqual([]);
});
