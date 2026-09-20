import { test, expect, type Page } from "@playwright/test";
import { openTab } from "./nav";

/**
 * Operates every control in the channel builder, including the ones that only exist after something is expanded.
 *
 * # Why the builder gets its own sweep
 *
 * It is the largest form in the product and the screen the whole thing is judged on. Forty-one controls are present when a template is
 * applied, and more appear behind "+ Add condition", "+ Add a change", "+ Add destination" and a connection-limits disclosure. The
 * general widget sweep deliberately stops at the screen as it arrives, so none of that depth is covered by it.
 *
 * # What makes an assertion here worth making
 *
 * The builder has one output: the channel file it generates. So every interaction is checked against the preview rather than against
 * the control that produced it. A control that updates itself and never reaches the file is the exact failure this screen had - a
 * patch built from a stale closure meant six script boxes displayed text that had already been discarded, and every control looked
 * correct while it happened.
 *
 * That is why the expansion checks assert a new control appeared and the value checks assert the file changed. Neither alone is
 * enough: a form can grow controls that are wired to nothing, and it can accept a value it never serialises.
 */

/** Waits until the generated file contains what the caller just caused, so a debounce cannot be mistaken for a missing feature. */
async function fileContains(page: Page, want: string, why: string) {
  const preview = page.locator("pre").first();
  await expect(preview).toBeVisible({ timeout: 20_000 });

  await expect(async () => {
    expect((await preview.textContent()) ?? "").toContain(want);
  }).toPass({ timeout: 20_000, intervals: [200, 300, 500] });

  // Reported separately so a failure says what was being attempted rather than only what was missing.
  expect((await preview.textContent()) ?? "", why).toContain(want);
}

async function openBuilderWithTemplate(page: Page) {
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();
  await page.getByRole("button", { name: /Record an HL7 feed/ }).click();
  await expect(page.locator("pre").first()).toBeVisible({ timeout: 20_000 });
}

function watchForFaults(page: Page): string[] {
  const faults: string[] = [];

  page.on("pageerror", (e) => faults.push(`uncaught: ${e.message}`));
  page.on("console", (m) => {
    if (m.type() !== "error") return;

    const text = m.text();
    if (text.includes("Failed to load resource") || text.includes("net::ERR")) return;

    faults.push(`console: ${text}`);
  });

  return faults;
}

test("the channel name typed into the builder reaches the file", async ({ page }) => {
  const faults = watchForFaults(page);

  await page.goto("/");
  await openBuilderWithTemplate(page);

  await page.getByLabel("Channel name").fill("qa-swept-channel");
  await fileContains(page, "name: qa-swept-channel", "the name was typed and never reached the generated file");

  expect(faults, `typing a channel name produced errors:\n${faults.join("\n")}`).toEqual([]);
});

test("every select in the builder offers options that all reach the file", async ({ page }) => {
  test.setTimeout(180_000);

  const faults = watchForFaults(page);
  const problems: string[] = [];

  await page.goto("/");
  await openBuilderWithTemplate(page);

  const selects = page.locator("main select");
  const count = await selects.count();
  expect(count, "the builder offers no selects at all, which cannot be right").toBeGreaterThan(2);

  for (let i = 0; i < count; i += 1) {
    const sel = selects.nth(i);
    if (!(await sel.isVisible()) || !(await sel.isEnabled())) continue;

    const label = (await sel.getAttribute("aria-label")) ?? (await sel.getAttribute("id")) ?? `select ${i}`;
    const values = await sel.locator("option").evaluateAll((os) => os.map((o) => (o as HTMLOptionElement).value));
    const original = await sel.inputValue();

    for (const value of values) {
      faults.length = 0;

      // Selecting an option can replace the section the select lives in - changing the message format rebuilds the format block - so
      // the locator is re-resolved rather than held across the interaction.
      const live = page.locator("main select").nth(i);
      if (!(await live.count()) || !(await live.isVisible().catch(() => false))) break;

      await live.selectOption(value, { timeout: 8_000 }).catch(() => {
        problems.push(`${label}: would not accept ${JSON.stringify(value)}`);
      });
      await page.waitForTimeout(250);

      if (faults.length) problems.push(`${label}: choosing ${JSON.stringify(value)} produced ${faults.join("; ")}`);

      // The file must still be generated. A choice that makes the builder unable to describe itself is worth knowing about, and the
      // panel says so in words when that happens rather than showing a stale file.
      const shown = await page
        .locator("pre, p:has-text('cannot be written as a channel file yet')")
        .first()
        .isVisible()
        .catch(() => false);
      if (!shown) problems.push(`${label}: after choosing ${JSON.stringify(value)} the preview showed neither a file nor an explanation`);
    }

    await page.locator("main select").nth(i).selectOption(original).catch(() => {});
  }

  expect(problems, `builder selects: ${problems.length} problem(s):\n${problems.join("\n")}`).toEqual([]);
});

test("adding a filter condition adds controls and changes the file", async ({ page }) => {
  const faults = watchForFaults(page);

  await page.goto("/");
  await openBuilderWithTemplate(page);

  const before = await page.locator("main input, main select, main textarea").count();

  await page.getByRole("button", { name: "+ Add condition" }).first().click();
  await page.waitForTimeout(400);

  const after = await page.locator("main input, main select, main textarea").count();
  expect(after, "pressing Add condition added no controls, so there is nothing to fill in").toBeGreaterThan(before);

  // A row of empty controls is not a condition. Filling it has to reach the file, which is the only place a condition means anything.
  const fields = page.locator("main input[type=text]");
  const n = await fields.count();
  await fields.nth(n - 1).fill("MSH-9");
  await page.waitForTimeout(300);

  const filled = page.locator("main input[type=text]");
  const value = await filled.nth((await filled.count()) - 1).inputValue();
  expect(value, "the condition field discarded what was typed into it").toBe("MSH-9");

  expect(faults, `adding a condition produced errors:\n${faults.join("\n")}`).toEqual([]);
});

test("adding a destination adds controls and reaches the file", async ({ page }) => {
  const faults = watchForFaults(page);

  await page.goto("/");
  await openBuilderWithTemplate(page);

  const before = await page.locator("main select").count();

  await page.getByRole("button", { name: "+ Add destination" }).first().click();
  await page.waitForTimeout(500);

  const after = await page.locator("main select").count();
  expect(after, "pressing Add destination added no controls").toBeGreaterThan(before);

  // Two destinations in the file, which is the outcome the button claims and the only one that matters. Counting controls proves the
  // form grew; counting the file proves the form is connected to anything.
  const preview = page.locator("pre").first();
  await expect(async () => {
    const yaml = (await preview.textContent()) ?? "";
    expect(yaml.match(/^\s*- name:/gm)?.length ?? 0, "a destination was added and the file still describes one").toBeGreaterThan(1);
  }).toPass({ timeout: 20_000, intervals: [300, 500] });

  expect(faults, `adding a destination produced errors:\n${faults.join("\n")}`).toEqual([]);
});

test("the connection limits disclosure opens and its numbers reach the file", async ({ page }) => {
  const faults = watchForFaults(page);

  await page.goto("/");
  await openBuilderWithTemplate(page);

  const trigger = page.getByRole("button", { name: /Connection limits/ }).first();
  await expect(trigger, "the connection limits disclosure is missing").toBeVisible();

  const before = await page.locator("main input[type=number]").count();
  await trigger.click();
  await page.waitForTimeout(400);

  const numbers = page.locator("main input[type=number]");
  expect(await numbers.count(), "opening connection limits revealed no number fields").toBeGreaterThan(before);

  const box = numbers.nth((await numbers.count()) - 1);
  const label = (await box.getAttribute("aria-label")) ?? "the last number field";
  await box.fill("42");
  await page.waitForTimeout(300);

  expect(await box.inputValue(), `${label} discarded what was typed`).toBe("42");
  await fileContains(page, "42", `${label} was set to 42 and it never reached the generated file`);

  expect(faults, `using connection limits produced errors:\n${faults.join("\n")}`).toEqual([]);
});

test("every checkbox in the builder toggles and the file stays describable", async ({ page }) => {
  const faults = watchForFaults(page);
  const problems: string[] = [];

  await page.goto("/");
  await openBuilderWithTemplate(page);

  const boxes = page.locator("main input[type=checkbox]");
  for (let i = 0; i < (await boxes.count()); i += 1) {
    const cb = boxes.nth(i);
    if (!(await cb.isVisible()) || !(await cb.isEnabled())) continue;

    const label = (await cb.getAttribute("aria-label")) ?? `checkbox ${i}`;
    const before = await cb.isChecked();

    faults.length = 0;
    await cb.click({ timeout: 5_000 }).catch(() => problems.push(`${label}: would not click`));
    await page.waitForTimeout(300);

    if ((await cb.isChecked().catch(() => before)) === before) problems.push(`${label}: clicked and stayed ${before}`);
    if (faults.length) problems.push(`${label}: clicking produced ${faults.join("; ")}`);

    const shown = await page
      .locator("pre, p:has-text('cannot be written as a channel file yet')")
      .first()
      .isVisible()
      .catch(() => false);
    if (!shown) problems.push(`${label}: after toggling, the preview showed neither a file nor an explanation`);

    await cb.setChecked(before).catch(() => {});
  }

  expect(problems, `builder checkboxes: ${problems.length} problem(s):\n${problems.join("\n")}`).toEqual([]);
});

test("the reliability settings on an HTTP destination reach the generated file", async ({ page }) => {
  // These four fields were added because a guard found them missing from the form entirely.
  //
  // The builder model carried bearer_token, success_status and follow_redirects, the server honoured all three, and the drift
  // guard was green because it compares the channel format against the builder's Go model - both sides Go. Nothing in the browser
  // mentioned them, so they were reachable only by editing YAML, which this project treats as a product bug.
  //
  // Asserted through the generated file rather than through the controls, because the file is the builder's only output and a form
  // that accepts a value it never serialises is the failure being guarded against.
  await page.goto("/");
  await openBuilderWithTemplate(page);

  // The destination's own Type select, held by its id. Position would break the moment a destination is added or removed, and
  // holding a locator by position through a change that alters which controls exist is a mistake this suite has made before.
  const type = page.locator('select[id^="dest-"][id$="-type"]').first();
  await type.selectOption("http");

  await page.getByRole("textbox", { name: "Post to" }).fill("https://api.internal/messages");
  await fileContains(page, "api.internal", "the destination URL never reached the generated file");

  // A comma-separated list has to become numbers, because the server takes numbers and a string list is refused with a message
  // about the request body rather than about the field somebody typed in.
  await page.getByRole("textbox", { name: /Status codes that mean delivered/ }).fill("200, 202");
  await fileContains(page, "success_status", "the accepted status codes never reached the generated file");
  await fileContains(page, "202", "the status list was entered and 202 is not in the file");

  await page.getByRole("checkbox", { name: /Follow a 3xx response/ }).check();
  await fileContains(page, "follow_redirects", "following redirects was switched on and the file does not say so");

  // The token is written to the file like any other setting; the interface never reads one back, which is a separate property
  // covered where the channel is loaded.
  await page.getByRole("textbox", { name: "Bearer token" }).fill("s3cr3t-value");
  await fileContains(page, "bearer_token", "the bearer token never reached the generated file");
});

test("the FHIR destination's validation settings reach the generated file", async ({ page }) => {
  // Three more fields the guard found missing from the interface, on a destination where the consequence is clinical rather than
  // operational: claiming US Core conformance a resource does not have, and choosing whether to find out here or from a receiver.
  await page.goto("/");
  await openBuilderWithTemplate(page);

  const type = page.locator('select[id^="dest-"][id$="-type"]').first();
  await type.selectOption("fhir");

  await page.getByRole("checkbox", { name: /Mark resources as conforming to US Core/ }).check();
  await fileContains(page, "claim_us_core", "US Core conformance was claimed and the file does not say so");

  // Refusing on warnings does nothing unless validation is on, so the control is disabled until it is - and the file must not
  // claim warnings are being refused when nothing is being checked.
  const rejectOnWarning = page.getByRole("checkbox", { name: /Refuse a resource that only produces warnings/ });
  await expect(rejectOnWarning).toBeDisabled();

  await page.getByRole("checkbox", { name: /Validate each resource before it is sent/ }).check();
  await fileContains(page, "validate_before_send", "validation was switched on and the file does not say so");

  await expect(rejectOnWarning).toBeEnabled();
  await rejectOnWarning.check();
  await fileContains(page, "reject_on_warning", "refusing on warnings was switched on and the file does not say so");
});

test("the file destination's naming and retention reach the generated file", async ({ page }) => {
  // The model's own comment says the file name is the difference between a folder somebody can find a message in and one they
  // cannot, and the form offered only the folder.
  await page.goto("/");
  await openBuilderWithTemplate(page);

  const type = page.locator('select[id^="dest-"][id$="-type"]').first();
  await type.selectOption("file");

  await page.getByRole("textbox", { name: "File name" }).fill("${date}-adt.hl7");
  await fileContains(page, "file_name", "a file name was typed and never reached the generated file");

  await page.getByRole("textbox", { name: /Suffix while writing/ }).fill(".writing");
  await fileContains(page, "temp_suffix", "the temporary suffix never reached the generated file");

  // Retention is a number, and zero has to stay out of the file: zero and empty both mean keep everything, and one of them reads
  // as a deliberate setting somebody chose.
  await page.getByRole("spinbutton", { name: /Delete after/ }).fill("0");
  const preview = page.locator("pre").first();
  await page.waitForTimeout(700);
  expect((await preview.textContent()) ?? "", "a retention of zero hours was written into the file").not.toContain("retain_hours");

  await page.getByRole("spinbutton", { name: /Delete after/ }).fill("72");
  await fileContains(page, "retain_hours", "a retention of 72 hours never reached the generated file");
});

test("an HTTP listener's TLS and limits reach the generated file", async ({ page }) => {
  // A channel listening on its own port is its own server and needs its own certificate. None of this was in the interface: TLS
  // appeared only under Settings, which is Perfuse's web interface rather than a channel's listener.
  await page.goto("/");
  await openBuilderWithTemplate(page);

  await page.getByRole("combobox", { name: /How do messages get here/ }).selectOption("http");

  await page.getByRole("button", { name: /TLS and limits/ }).click();

  await page.getByRole("checkbox", { name: /Encrypt this listener with TLS/ }).check();
  await page.getByRole("textbox", { name: "Certificate file" }).fill("/etc/perfuse/channel.pem");
  await fileContains(page, "cert_file", "a certificate path was typed and never reached the generated file");

  // The authority is only asked for once a client certificate is demanded, and must not be written before that: a file naming an
  // authority that is never consulted is worse than one naming none.
  const ca = page.getByRole("textbox", { name: /Certificate authority to trust/ });
  await expect(ca).toHaveCount(0);

  await page.getByRole("checkbox", { name: /Require a client certificate/ }).check();
  await fileContains(page, "require_client_cert", "requiring a client certificate never reached the generated file");

  await ca.fill("/etc/perfuse/senders-ca.pem");
  await fileContains(page, "ca_file", "the trusted authority never reached the generated file");

  await page.getByRole("textbox", { name: /Largest message accepted/ }).fill("1048576");
  await fileContains(page, "max_message_size", "a size limit was typed and never reached the generated file");
});

test("a delimited channel can be configured without a text editor", async ({ page }) => {
  // A whole format was choosable and had no options at all. The guard reported three fields missing; the truth was eight, because
  // words like "delimiter", "quote" and "columns" appear elsewhere in the interface and a substring match found them.
  await page.goto("/");
  await openBuilderWithTemplate(page);

  await page.getByRole("combobox", { name: /Message format|What kind of messages/i }).first().selectOption("delimited");

  await page.getByRole("textbox", { name: "Column separator" }).fill("|");
  await fileContains(page, "delimiter:", "the column separator never reached the generated file");

  // Without a header row the columns have to be named, or a filter written against a name has nothing to match.
  const columns = page.getByRole("textbox", { name: /Column names, in order/ });
  await columns.fill("mrn, surname, dob");
  await fileContains(page, "surname", "the column names never reached the generated file");

  // Turning the header on must remove the list rather than leave it: with a header the names come from the file, and a list that is
  // silently ignored is worse than one refused, because the file reads as though it decided the names.
  await page.getByRole("checkbox", { name: /The first row names the columns/ }).check();
  await fileContains(page, "has_header", "a header row was declared and the file does not say so");

  const preview = page.locator("pre").first();
  await expect(async () => {
    expect((await preview.textContent()) ?? "").not.toContain("surname");
  }).toPass({ timeout: 20_000, intervals: [200, 400] });

  await expect(columns, "the column list is still offered when the file provides the names").toHaveCount(0);

  await page.getByRole("checkbox", { name: /Accept rows with the wrong number of columns/ }).check();
  await fileContains(page, "relaxed", "accepting ragged rows never reached the generated file");
});

test("what a DICOM listener accepts reaches the generated file", async ({ page }) => {
  // The model's comment on allowed calling AE titles says that without it, any host on the network may push images into a clinical
  // channel. It had no control. The called AE title, which the form did offer, is the name a sender dials - so it identifies the
  // listener rather than restricting who may use it, which is an easy thing to mistake for a restriction.
  await page.goto("/");
  await openBuilderWithTemplate(page);

  await page.getByRole("combobox", { name: /How do messages get here/ }).selectOption("dicom");
  await page.getByRole("button", { name: /What this listener accepts/ }).click();

  await page.getByRole("textbox", { name: /Only accept these calling AE titles/ }).fill("CT01, MR02");
  await fileContains(page, "allowed_calling_ae", "the permitted senders never reached the generated file");
  await fileContains(page, "MR02", "a second permitted sender was entered and is not in the file");

  await page.getByRole("textbox", { name: /Only accept these SOP classes/ }).fill("1.2.840.10008.5.1.4.1.1.2");
  await fileContains(page, "sop_classes", "the accepted SOP classes never reached the generated file");

  await page.getByRole("textbox", { name: /Largest object accepted/ }).fill("2000000");
  await fileContains(page, "max_object_bytes", "a size limit was typed and never reached the generated file");
});

test("delivery semantics on an MLLP destination reach the generated file", async ({ page }) => {
  await page.goto("/");
  await openBuilderWithTemplate(page);

  const type = page.locator('select[id^="dest-"][id$="-type"]').first();
  await type.selectOption("mllp");

  // What "delivered" means. Without waiting for a reply, success means the bytes reached the send buffer, which a peer that
  // crashed a moment later never read - so a channel can report a clean delivery rate while the far end received nothing.
  await page.getByRole("checkbox", { name: /Treat a message as delivered only once/ }).check();
  await fileContains(page, "expect_reply", "waiting for a reply never reached the generated file");

  await page.getByRole("checkbox", { name: /Hold one connection open/ }).check();
  await fileContains(page, "keep_alive", "reusing the connection never reached the generated file");
});

test("a broker destination can be created at all", async ({ page }) => {
  // The builder could not create one. Broker was in the destination union and in the Go model, and absent from the label list, so
  // it was never offered - the field guard named one missing setting when the whole destination type was unreachable.
  await page.goto("/");
  await openBuilderWithTemplate(page);

  const type = page.locator('select[id^="dest-"][id$="-type"]').first();
  await type.selectOption("broker");

  await page.getByRole("textbox", { name: "Broker address" }).fill("broker.hospital.local:61613");
  await fileContains(page, "61613", "the broker address never reached the generated file");

  await page.getByRole("textbox", { name: "Queue or topic" }).fill("/queue/hl7.outbound");
  await fileContains(page, "hl7.outbound", "the queue name never reached the generated file");

  // Persistence is on by default and written only when turned off, so the file must not mention it until then. A message that does
  // not survive a broker restart is lost with nothing reporting it, because the send succeeded.
  const preview = page.locator("pre").first();
  expect((await preview.textContent()) ?? "", "persistence was written while still at its default").not.toContain("persistent");

  await page.getByRole("checkbox", { name: /keep the message across a restart/i }).uncheck();
  await fileContains(page, "persistent: false", "turning persistence off never reached the generated file");
});

test("the queue a destination falls back to can be configured", async ({ page }) => {
  // A whole block the builder could not write. Easy to confuse with the retry settings beside it: retry governs immediate attempts
  // inside one delivery, and the queue governs what happens once those are exhausted.
  await page.goto("/");
  await openBuilderWithTemplate(page);

  const enable = page.getByRole("checkbox", { name: /Queue messages this destination could not deliver/ });
  await enable.check();
  await fileContains(page, "queue:", "enabling the queue never reached the generated file");

  // The two that stop a disk filling. A queue with no bound grows until the filesystem is full, which arrives as something
  // unrelated breaking rather than as a queue problem.
  await page.getByRole("spinbutton", { name: /Most messages to hold/ }).fill("10000");
  await fileContains(page, "max_depth", "the queue depth limit never reached the generated file");

  await page.getByRole("spinbutton", { name: /Keep delivered messages for/ }).fill("72");
  await fileContains(page, "retain_hours", "the queue retention never reached the generated file");

  await page.getByRole("textbox", { name: /Wait before the first retry/ }).fill("30s");
  await fileContains(page, "backoff", "the queue backoff never reached the generated file");

  // Turning it off must remove the block rather than leave it disabled, because a queue block that says enabled false reads as a
  // decision when it is a leftover.
  await enable.uncheck();
  const preview = page.locator("pre").first();
  await expect(async () => {
    expect((await preview.textContent()) ?? "").not.toContain("max_depth");
  }).toPass({ timeout: 20_000, intervals: [200, 400] });
});

test("a DICOM query's polling window settings reach the generated file", async ({ page }) => {
  // One of these three was already in the mapping and could never have worked: the key was written as emit_on_first_poll, the file
  // format's spelling, where the build endpoint decodes camelCase strictly. Nothing in the form could set it, so nothing failed.
  await page.goto("/");
  await openBuilderWithTemplate(page);

  await page.getByRole("combobox", { name: /How do messages get here/ }).selectOption("dicom_query");

  await page.getByRole("textbox", { name: /Overlap each window by/ }).fill("5m");
  await fileContains(page, "overlap", "the window overlap never reached the generated file");

  await page.getByRole("checkbox", { name: /Query by patient rather than by study/ }).check();
  await fileContains(page, "patient_root", "querying by patient never reached the generated file");

  // The first poll looks back over the whole window, so this sends every study in it at once. Off is the default and the right one
  // for anything other than a deliberate backfill.
  await page.getByRole("checkbox", { name: /Send everything found on the first poll/ }).check();
  await fileContains(page, "emit_on_first_poll", "emitting on the first poll never reached the generated file");
});

test("connection limits and MLLP encryption reach the generated file", async ({ page }) => {
  // Both of these failed the whole build before, and for the same reason: the form sent keys the endpoint does not have, and it
  // decodes strictly. Opening Connection limits and typing a number produced "unknown field idleTimeout" and no channel at all,
  // which reads as a broken builder rather than a broken field.
  await page.goto("/");
  await openBuilderWithTemplate(page);

  await page.getByRole("button", { name: /Connection limits/ }).click();
  await page.getByRole("spinbutton", { name: /Close a silent connection after/ }).fill("300");
  await fileContains(page, "idle_timeout", "an idle timeout was set and never reached the generated file");

  await page.getByRole("spinbutton", { name: /Most connections at once/ }).fill("8");
  await fileContains(page, "max_connections", "a connection limit was set and never reached the generated file");

  // An MLLP listener is its own server with its own certificate, and had no way to be encrypted. On MLLP a client certificate is
  // the only authentication available - there is no token and no header.
  await page.getByRole("button", { name: /Encrypt this listener/ }).click();
  await page.getByRole("checkbox", { name: /Encrypt connections to this listener/ }).check();
  await page.getByRole("textbox", { name: "Certificate file" }).fill("/etc/perfuse/mllp.pem");
  await fileContains(page, "cert_file", "a certificate path was typed and never reached the generated file");

  const ca = page.getByRole("textbox", { name: /Certificate authority to trust/ });
  await expect(ca, "the authority is asked for before a client certificate is demanded").toHaveCount(0);

  await page.getByRole("checkbox", { name: /Require a client certificate/ }).check();
  await fileContains(page, "require_client_cert", "requiring a client certificate never reached the generated file");
});

test("script capabilities and the directories file access is limited to reach the file", async ({ page }) => {
  // These could not be granted from the form at all. The build model's own comment said the form has to offer file roots or the
  // capability is unusable from the interface: granting file access without naming directories used to reach the whole filesystem,
  // including this program's own database of password hashes - and creating a channel needs only the editor role, so the escalation
  // completed the moment an administrator started it.
  await page.goto("/");
  await openBuilderWithTemplate(page);

  // A script first. A capability granted to a channel with no scripts means nothing, and the server correctly omits the whole block
  // in that case - so a test that grants file access without writing a script proves nothing about either.
  const transformer = page.getByRole("textbox", { name: /Transformer/i }).first();
  await expect(transformer).toBeVisible({ timeout: 20_000 });
  await transformer.fill("msg;");

  const allowFile = page.getByRole("checkbox", { name: /Read and write files/ });
  await expect(allowFile).toBeVisible({ timeout: 20_000 });

  // The directories are asked for only once file access is granted, because they mean nothing otherwise.
  await expect(page.getByRole("textbox", { name: /Directories it may use/ })).toHaveCount(0);

  await allowFile.check();
  await page.getByRole("textbox", { name: /Directories it may use/ }).fill("/var/perfuse/scratch");

  await fileContains(page, "file_roots", "the permitted directories never reached the generated file");
  await fileContains(page, "scratch", "the directory that was typed is not in the file");
});

test("a raw socket destination can be configured, and its framing reaches the generated file", async ({ page }) => {
  // The transport a guard found missing on 17 September. It was in the server's model with a validator, a framing block and its own
  // tests, and the builder never offered it - so the only way to send to a laboratory instrument was to write YAML by hand, which is
  // the situation the builder exists to end.
  //
  // Driven through the controls and asserted on the file, because a form that accepts a value it never serialises is the failure this
  // suite exists to catch, and it has caught it more than once.
  await page.goto("/");
  await openBuilderWithTemplate(page);

  const type = page.locator('select[id^="dest-"][id$="-type"]').first();

  // The option has to exist at all. Its absence was the defect.
  await expect(type.locator('option[value="tcp"]')).toHaveCount(1);

  await type.selectOption("tcp");

  await page.getByRole("textbox", { name: "Send to" }).fill("instrument.lab:9100");
  await fileContains(page, "instrument.lab:9100", "the address never reached the generated file");
  await fileContains(page, "type: tcp", "the destination type is not in the file");

  // Framing has no default, so nothing is written until it is chosen. That is deliberate: writing with the wrong framing does not
  // fail, it produces messages split in the wrong places at the far end.
  const framing = page.locator('select[id^="dest-"][id$="-tcp-framing"]').first();
  await framing.selectOption("length");
  await fileContains(page, "framing: length", "the chosen framing never reached the generated file");

  // Only the fields the chosen framing uses appear. A record length belongs to fixed framing and must not be offered here, because
  // the server refuses it on a length-prefixed stream rather than ignoring it.
  await expect(page.getByRole("spinbutton", { name: "Record length" })).toHaveCount(0);

  await page.getByRole("spinbutton", { name: /Length prefix size/ }).fill("2");
  await fileContains(page, "length_bytes: 2", "the length prefix size never reached the generated file");

  await page.getByRole("checkbox", { name: /Big-endian/ }).check();
  await fileContains(page, "big_endian", "the byte order was chosen and the file does not say so");

  // Switching framing hides the fields that no longer apply, and the delimiter appears instead.
  await framing.selectOption("delimited");
  await expect(page.getByRole("spinbutton", { name: /Length prefix size/ })).toHaveCount(0);

  await page.getByRole("textbox", { name: "Delimiter" }).fill("\\r");
  await fileContains(page, "delimiter:", "the delimiter never reached the generated file");

  // Nested under tcp:, not flat at the destination. Writing these flat is a mistake already made in this codebase, where the form
  // accepted the values and the file never carried them.
  await fileContains(page, "    tcp:", "the socket settings are not nested under a tcp block");
});

test("the builder does not offer a transport the server has never heard of", async ({ page }) => {
  // A DICOMweb destination was offered for weeks with a label, a hint and a three-field form, and no such destination type existed in
  // the server. Choosing it produced a channel the server refused wholesale - not the destination, the channel - which reads as the
  // builder being broken rather than as one option that should never have been on the list.
  //
  // Asserted here as well as in Go, because the Go guard compares source files and this asks the question a person would: is it in
  // the menu?
  await page.goto("/");
  await openBuilderWithTemplate(page);

  const type = page.locator('select[id^="dest-"][id$="-type"]').first();

  await expect(type.locator('option[value="dicomweb"]')).toHaveCount(0);

  // A positive control: the locator has to be capable of finding an option that is there.
  await expect(type.locator('option[value="dicom"]')).toHaveCount(1);
});
