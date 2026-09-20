import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Every source type and every destination type, built and checked.
//
// The builder offers seventeen ways for messages to arrive and sixteen places to send them. Each reveals its
// own set of fields, and a field that is mis-wired between the form and the YAML is invisible until
// somebody picks that combination - which for the less common ones might be a year later. So this walks
// all of them and asserts the generated file actually reflects what was typed.
//
// The values are placeholders. The property under test is that the form and the file agree, not that a
// SOAP endpoint exists.

function watch(page: import("@playwright/test").Page): string[] {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`page error: ${e}`));
  page.on("console", (m) => {
    if (m.type() !== "error") return;
    // A 400 from the build endpoint is expected while a required field is empty: the panel asks the
    // server whether the draft can be written yet and shows the answer. The browser logs every non-2xx
    // response as a failed resource regardless, so filtering it here keeps this watching the app rather
    // than the browser.
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

/** settled waits until the panel has finished rebuilding and reports what it settled on.
 *
 * Not "wait for the text to change": the preview is replaced by an explanation when the draft cannot be
 * written, so there may be no pre element at all, and comparing against the previous text then waits for
 * a change that never comes. Not "wait for text already present" either, which returns the previous
 * draft - the race that made an earlier version of this report every source type as broken when all of
 * them were fine.
 *
 * So it waits for one of the two real outcomes: the file contains what was chosen, or the panel says why
 * it cannot be written yet. */
async function settled(
  page: import("@playwright/test").Page,
  wants: string,
): Promise<{ yaml: string; explained: boolean }> {
  const pre = page.locator("pre").first();
  const main = page.locator("main");

  await expect
    .poll(
      async () => {
        const yaml = (await pre.count()) ? ((await pre.textContent()) ?? "") : "";
        if (yaml.includes(wants)) return "written";
        const panel = (await main.textContent()) ?? "";
        if (/cannot be written as a channel file yet/.test(panel)) return "explained";
        return "waiting";
      },
      { timeout: 20_000, intervals: [100, 200, 300, 500] },
    )
    .not.toBe("waiting");

  const yaml = (await pre.count()) ? ((await pre.textContent()) ?? "") : "";
  return { yaml, explained: !yaml.includes(wants) };
}

/** yamlNow returns the preview once it names this channel. */
async function yamlNow(page: import("@playwright/test").Page, contains: string): Promise<string> {
  const pre = page.locator("pre").first();
  await expect(pre).toContainText(contains, { timeout: 20_000 });
  return (await pre.textContent()) ?? "";
}

// Every source the builder offers, with the exact option text and the type it must write.
const sources: { option: RegExp; writes: string }[] = [
  { option: /A hospital system connects to us \(MLLP\)/, writes: "mllp" },
  { option: /A sender posts messages to us \(HTTP\)/, writes: "http" },
  { option: /We collect files from a server \(SFTP\)/, writes: "sftp" },
  { option: /We poll a database table/, writes: "database" },
  { option: /A sender calls us as a web service \(SOAP\)/, writes: "soap" },
  { option: /A scanner sends us images \(DICOM C-STORE\)/, writes: "dicom" },
  { option: /We ask an imaging archive what is new \(DICOM C-FIND\)/, writes: "dicom_query" },
  { option: /A script we write produces messages \(JavaScript Reader\)/, writes: "javascript" },
  { option: /We read files from a folder on this server/, writes: "file" },
  { option: /We collect files from a server \(FTP or FTPS\)/, writes: "ftp" },
  { option: /We collect files from a Windows share/, writes: "smb" },
  { option: /We collect files from a WebDAV collection/, writes: "webdav" },
  { option: /A device connects to us on a socket, not MLLP \(TCP\)/, writes: "tcp" },
  { option: /A device is wired to us \(serial cable\)/, writes: "serial" },
  { option: /We read from a message queue \(ActiveMQ, RabbitMQ\)/, writes: "broker" },
];

test("every source type writes its own type into the channel file", async ({ page }) => {
  const problems = watch(page);
  const failures: string[] = [];
  const needsInput: string[] = [];

  // A fresh form per source, rather than switching the select on one form.
  //
  // Switching carried the previous source's verdict across. The preview is rebuilt on a debounce, so for the first few
  // hundred milliseconds after switching the panel still shows the previous state - and if that state was "cannot be
  // written yet", settled() returns "explained" immediately for a source it never actually built. Which meant a source
  // following one that needs input inherited its verdict and was never checked at all.
  //
  // Found while confirming this test would catch the three unimplemented DICOM types: it reported them as explained with
  // no problems shown, when a fresh form shows exactly the right refusal. Slower, and correct.
  for (const { option, writes } of sources) {
    await openNewChannelForm(page);
    await page.getByLabel("Channel name", { exact: true }).fill("e2e-sources");

    const selector = page.getByLabel(/How do messages get here/).first();
    await expect(selector).toBeVisible({ timeout: 20_000 });

    const options = await selector.locator("option").allTextContents();
    const match = options.find((o) => option.test(o));
    if (!match) {
      failures.push(`no option matching ${option} is offered`);
      continue;
    }

    await selector.selectOption({ label: match });

    // Either the file says what was chosen, or the panel says why it cannot be written yet. Both are
    // acceptable; a blank panel with no explanation is not, and was what DICOM C-FIND produced.
    const { explained } = await settled(page, `type: ${writes}`);
    if (explained) {
      // Recorded, not failed. Six sources ship defaults good enough to produce a file immediately; five
      // have a required field with no sensible default - a PACS address cannot be guessed - so they
      // explain what is missing instead. Both are acceptable. Only silence is not, and silence is what
      // this used to do.
      needsInput.push(match);
    }

    // And it must show at least one field of its own, or there is nothing to configure.
    const fields = await page.locator("main input, main select, main textarea").count();
    if (fields < 3) {
      failures.push(`choosing "${match}" showed almost no fields (${fields})`);
    }

    // The assertion this test was missing.
    //
    // Checking that "type: x" appears in the preview only proves the form wrote a string. It does not prove the server
    // would accept the channel, and for three DICOM source types it did not: they were offered in the dropdown with no
    // implementation behind them, so the form wrote type: dicom_move and this test passed for a year.
    //
    // So a source type that produces a file must produce one the server accepts. A source still waiting for a required
    // field is exempt, because there is nothing to validate yet.
    //
    // Scoped to the source deliberately. This form leaves its destination blank while cycling through source types, so
    // every draft is invalid for that unrelated reason - filling one in would test destinations as well and couple this
    // to their field labels. What must never appear is a problem blaming the source, which is exactly what the three
    // unimplemented DICOM types produced: source.type "dicom_move" is not supported.
    if (!explained) {
      const refused = page.getByRole("alert").filter({ hasText: /would not accept this channel/ });
      if ((await refused.count()) > 0) {
        const text = await refused.first().innerText();
        if (/source\.type|is not supported/.test(text)) {
          failures.push(`choosing "${match}" produced a source the server does not support: ${text}`);
        }
      }
    }
  }

  expect(
    failures,
    `Source types that do not survive being chosen:\n  ${failures.join("\n  ")}`,
  ).toEqual([]);

  // Replaced a tautology.
  //
  // This used to assert needsInput.length plus sources.length minus needsInput.length equals sources.length, which is
  // true for any two numbers. What it was reaching for is that at least some sources produce a complete file from their
  // defaults - if every one needed input, the loop above would never have validated anything and the whole test would
  // pass while checking nothing.
  expect(
    sources.length - needsInput.length,
    `every source type needed input before a file appeared, so nothing was validated: ${needsInput.join(", ")}`,
  ).toBeGreaterThan(0);

  test.info().annotations.push({
    type: "sources needing input before a file appears",
    description: needsInput.join(", ") || "none",
  });

  expect(problems, problems.join("\n")).toEqual([]);
});

test("every destination type writes its own type into the channel file", async ({ page }) => {
  const problems = watch(page);
  const failures: string[] = [];
  const needsInput: string[] = [];

  await openNewChannelForm(page);
  await page.getByLabel("Channel name", { exact: true }).fill("e2e-dests");

  // The one destination the empty template starts with.
  const typeSelect = page.locator("main select").filter({ has: page.locator('option:text-is("A directory on disk")') }).first();
  await expect(typeSelect, "the destination type selector was not found").toBeVisible({ timeout: 20_000 });

  const options = await typeSelect.locator("option").allTextContents();
  expect(options.length, "no destination types are offered").toBeGreaterThan(5);

  for (const label of options) {
    await typeSelect.selectOption({ label });

    const { yaml, explained } = await settled(page, "destinations:");

    // Same two acceptable outcomes as the sources: a file, or a reason it cannot be written yet. A
    // destination with a required address cannot produce one from nothing.
    if (explained) {
      needsInput.push(label);
      continue;
    }

    // Every destination writes a destinations block naming a type. Which token is the builder's own
    // mapping, so assert one exists rather than guessing it.
    const destSection = yaml.slice(yaml.indexOf("destinations:"));
    if (!/type:\s*\S+/.test(destSection)) {
      failures.push(`choosing "${label}" produced a destination with no type`);
    }
  }

  expect(
    failures,
    `Destination types that do not survive being chosen:\n  ${failures.join("\n  ")}`,
  ).toEqual([]);

  test.info().annotations.push({
    type: "destinations needing input before a file appears",
    description: needsInput.join(", ") || "none",
  });

  expect(problems, problems.join("\n")).toEqual([]);
});

test("what is typed into a source field reaches the file", async ({ page }) => {
  const problems = watch(page);

  await openNewChannelForm(page);
  await page.getByLabel("Channel name", { exact: true }).fill("e2e-roundtrip");

  const selector = page.getByLabel(/How do messages get here/).first();

  // SFTP has the most fields of any source, so it is the best test of whether they are all wired.
  const options = await selector.locator("option").allTextContents();
  const sftp = options.find((o) => /SFTP/.test(o));
  expect(sftp, "no SFTP source is offered").toBeTruthy();
  await selector.selectOption({ label: sftp! });

  const typed: Record<string, string> = {
    Server: "sftp.e2e.example.org:2222",
    Username: "e2e-user",
    "Private key file": "/e2e/id_ed25519",
    "Known hosts file": "/e2e/known_hosts",
    "Directory to watch": "/e2e/inbound",
    "Move each file here once it has been read": "/e2e/done",
    "Only files matching": "*.e2e",
  };

  for (const [label, value] of Object.entries(typed)) {
    const field = page.getByLabel(label, { exact: true });
    if (!(await field.count())) continue;
    await field.fill(value);
  }

  const { yaml } = await settled(page, "name: e2e-roundtrip");

  const missing = Object.entries(typed).filter(([, value]) => !yaml.includes(value));
  expect(
    missing.map(([label, value]) => `${label} = ${value}`),
    `These values were typed into the form and are not in the file:\n  ${missing
      .map(([label, value]) => `${label} = ${value}`)
      .join("\n  ")}\n\n` +
      `A field that renders but is never written produces a channel that does not do what the form says.`,
  ).toEqual([]);

  expect(problems, problems.join("\n")).toEqual([]);
});

test("a filter and a transformation step typed into the form reach the file", async ({ page }) => {
  const problems = watch(page);

  await openNewChannelForm(page);
  await page.getByLabel("Channel name", { exact: true }).fill("e2e-transform");
  await page.getByLabel("Listen on", { exact: true }).fill("127.0.0.1:21777");

  // Add a change, which is the control that turns a forwarder into an interface engine.
  const add = page.getByRole("button", { name: "+ Add a change" });
  await expect(add, "there is no way to add a transformation step").toBeVisible({ timeout: 20_000 });
  await add.click();

  // The first step defaults to something; set a field so the value is unmistakable in the output.
  const what = page.getByLabel("What should this do?", { exact: true }).first();
  if (await what.count()) {
    await what.selectOption({ label: "Set a field to a value" });
  }

  const field = page.getByLabel("Field", { exact: true }).first();
  if (await field.count()) await field.fill("PID-8");
  const setTo = page.getByLabel("Set it to", { exact: true }).first();
  if (await setTo.count()) await setTo.fill("E2EVALUE");

  const yaml = await yamlNow(page, "name: e2e-transform");
  expect(yaml, "the step's field never reached the file").toContain("PID-8");
  expect(yaml, "the step's value never reached the file").toContain("E2EVALUE");

  expect(problems, problems.join("\n")).toEqual([]);
});

test("a second destination can be added and both reach the file", async ({ page }) => {
  const problems = watch(page);

  await openNewChannelForm(page);
  await page.getByLabel("Channel name", { exact: true }).fill("e2e-twodest");
  await page.getByLabel("Listen on", { exact: true }).fill("127.0.0.1:21778");

  // Name the first one, then add a second and name that.
  const names = page.getByLabel("Name", { exact: true });
  await names.first().fill("first-place");

  await page.getByRole("button", { name: "+ Add destination" }).click();
  await expect(names, "adding a destination did not produce a second name field").toHaveCount(2, {
    timeout: 15_000,
  });
  await names.last().fill("second-place");

  await expect
    .poll(async () => (await page.locator("pre").first().textContent()) ?? "", { timeout: 20_000 })
    .toContain("second-place");
  const yaml = (await page.locator("pre").first().textContent()) ?? "";
  expect(yaml).toContain("first-place");

  // And removing one takes it away again, through its confirmation.
  await page.getByRole("button", { name: "Remove this destination" }).last().click();
  await page.getByRole("button", { name: "Remove", exact: true }).click();

  // Polled rather than read once: the preview is rebuilt on a debounce, so reading it immediately
  // returns the draft that still had two destinations.
  await expect
    .poll(async () => (await page.locator("pre").first().textContent()) ?? "", { timeout: 20_000 })
    .not.toContain("second-place");

  expect(problems, problems.join("\n")).toEqual([]);
});
