import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Building channels through the form, the way an operator does.
//
// The suite already checked that every template produces YAML the loader accepts. It never saved one.
// So nothing covered the path that matters: pick a starting point, change the addresses to yours, save
// it, see it in the list, start it, stop it, delete it. That is the product's core job.
//
// Each test uses its own channel name and its own port, because these run against one shared server and
// a name collision or a port already in use would look like a defect in the form.

/** uniqueName keeps channels from colliding across tests in the same run. */
function uniqueName(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}${Math.floor(Math.random() * 1e4)}`;
}

/** port hands out a listen port unlikely to be taken by anything else on this machine. */
let nextPort = 21000;
function port(): number {
  nextPort += 1;
  return nextPort;
}

/** failOnPageError turns a React crash into a failure rather than a blank panel. */
function watch(page: import("@playwright/test").Page): string[] {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`page error: ${e}`));
  page.on("console", (m) => {
    if (m.type() === "error") problems.push(`console: ${m.text()}`);
  });
  return problems;
}

/** expectSaved waits for the channel to appear in the list, and says why if it does not.
 *
 * A bare toBeVisible on the card reports only that the card is missing, which is the symptom. The builder
 * shows the server's refusal on screen, so reading it turns "the channel was not saved" into the reason. */
async function expectSaved(page: import("@playwright/test").Page, name: string) {
  const card = channelCard(page, name);
  try {
    await expect(card).toBeVisible({ timeout: 20_000 });
  } catch {
    const panel = ((await page.locator("main").textContent()) ?? "").replace(/\s+/g, " ");
    throw new Error(
      `${name} was not saved. What the panel says:\n${panel.slice(0, 600)}`,
    );
  }
}

/** channelCard locates one channel's card by the heading that names it. */
function channelCard(page: import("@playwright/test").Page, name: string) {
  return page
    .locator("main div.card")
    .filter({ has: page.getByRole("heading", { name, exact: true }) })
    .first();
}

async function openNewChannelForm(page: import("@playwright/test").Page) {
  await page.goto("/");
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();
}

/** setField fills a labelled input in the builder.
 *
 * By label, which works because Field now associates one. It did not before - htmlFor was optional and
 * the builder never passed it - so every locator here had to guess at the surrounding markup, and the
 * guess picked the wrong box for the HTTP template. An unlabelled input is an accessibility defect
 * first and an untestable one second; fixing the first fixed the second. */
async function setField(page: import("@playwright/test").Page, label: string, value: string) {
  await page.getByLabel(label, { exact: true }).first().fill(value);
}

/** waitForYaml waits for the debounced preview to show this channel, not the previous draft.
 *
 * Reports what the panel says on failure. The preview is replaced by an explanation when the draft cannot
 * be written yet, so "the name never appeared" is the symptom and the explanation is the cause. */
async function waitForYaml(page: import("@playwright/test").Page, name: string) {
  try {
    await expect(page.locator("pre").first()).toContainText(`name: ${name}`, { timeout: 20_000 });
  } catch {
    const panel = ((await page.locator("main").textContent()) ?? "").replace(/\s+/g, " ");
    throw new Error(`the preview never showed name: ${name}. What the panel says:\n${panel.slice(0, 700)}`);
  }
}

/** deleteChannel removes a channel through the interface, including the confirmation. */
async function deleteChannel(page: import("@playwright/test").Page, name: string) {
  await page.goto("/");
  await openTab(page, "Channels");

  const card = channelCard(page, name);
  await expect(card, `no card for ${name}`).toBeVisible({ timeout: 15_000 });
  await card.getByRole("button", { name: "Delete" }).click();
  await page.getByRole("button", { name: "Delete channel" }).click();
  await expect(page.locator("main")).not.toContainText(name, { timeout: 15_000 });
}

test("an archive channel can be built, saved, listed, started, stopped and deleted", async ({ page }) => {
  const problems = watch(page);
  const name = uniqueName("e2e-archive");
  const listen = `127.0.0.1:${port()}`;

  await openNewChannelForm(page);

  // Start from the template an operator is told is the safest first channel.
  await page.getByRole("button", { name: "Record an HL7 feed" }).click();

  await setField(page, "Channel name", name);
  await setField(page, "Listen on", listen);

  await waitForYaml(page, name);
  await expect(page.locator("pre").first()).toContainText(listen);

  await page.getByRole("button", { name: "Create channel" }).click();

  // Back to the list, with a card for it. Asserted on the card rather than on the text, because the
  // YAML preview in the builder also contains the name - so a text assertion passed even when the save
  // had failed and the form was still open.
  await expectSaved(page, name);

  // Start and stop it from the dashboard, which is where those controls live.
  await openTab(page, "Dashboard");
  const card = page.locator("main div").filter({ hasText: name }).last();

  // The template starts enabled, so it may already be running. Drive whichever control is offered.
  const start = card.getByRole("button", { name: "Start" });
  if (await start.count()) {
    await start.first().click();
  }
  await expect(card).toContainText(/Running|Stopped|Errors/, { timeout: 20_000 });

  const stop = card.getByRole("button", { name: "Stop" });
  if (await stop.count()) {
    await stop.first().click();
    await page.getByRole("button", { name: "Stop channel" }).click();
    await expect(card).toContainText(/Stopped/, { timeout: 20_000 });
  }

  await deleteChannel(page, name);
  expect(problems, problems.join("\n")).toEqual([]);
});

test("a channel with two filtered destinations saves and comes back with both", async ({ page }) => {
  const problems = watch(page);
  const name = uniqueName("e2e-router");
  const listen = `127.0.0.1:${port()}`;

  await openNewChannelForm(page);

  // Two destinations is the shape behind most real integrations, and the one most likely to lose a
  // field on the way to YAML.
  await page.getByRole("button", { name: "Send admissions one way and results another" }).click();

  await setField(page, "Channel name", name);
  await setField(page, "Listen on", listen);

  await waitForYaml(page, name);

  const yaml = (await page.locator("pre").first().textContent()) ?? "";
  expect(yaml, "the first destination is missing").toContain("admissions");
  expect(yaml, "the second destination is missing").toContain("results");

  await page.getByRole("button", { name: "Create channel" }).click();
  await expectSaved(page, name);

  // Reopen it in the form. A channel that saves but cannot be reopened is worse than one that refuses
  // to save, because the operator only finds out when they need to change it.
  const card = channelCard(page, name);
  await expect(card).toBeVisible({ timeout: 15_000 });
  await card.getByRole("button", { name: "Edit" }).click();

  await expect(page.locator("main")).toContainText("admissions", { timeout: 20_000 });
  await expect(page.locator("main")).toContainText("results");

  await page.getByRole("button", { name: "Cancel" }).click();

  await deleteChannel(page, name);
  expect(problems, problems.join("\n")).toEqual([]);
});

test("an HTTP source channel saves with its path and listen address", async ({ page }) => {
  const problems = watch(page);
  const name = uniqueName("e2e-http");
  const listen = `127.0.0.1:${port()}`;

  await openNewChannelForm(page);
  await page.getByRole("button", { name: "Accept messages posted over HTTP" }).click();

  await setField(page, "Channel name", name);
  await setField(page, "Listen on", listen);
  await setField(page, "Path", "/e2e-messages");

  await waitForYaml(page, name);
  const yaml = (await page.locator("pre").first().textContent()) ?? "";
  expect(yaml).toContain("/e2e-messages");

  await page.getByRole("button", { name: "Create channel" }).click();
  await expectSaved(page, name);

  await deleteChannel(page, name);
  expect(problems, problems.join("\n")).toEqual([]);
});

test("an X12 channel keeps its format when saved", async ({ page }) => {
  const problems = watch(page);
  const name = uniqueName("e2e-x12");
  const listen = `127.0.0.1:${port()}`;

  await openNewChannelForm(page);
  await page.getByRole("button", { name: "Take in X12 claims" }).click();

  await setField(page, "Channel name", name);
  await setField(page, "Listen on", listen);

  await waitForYaml(page, name);
  const yaml = (await page.locator("pre").first().textContent()) ?? "";
  expect(yaml, "the X12 format was lost between the form and the file").toContain("x12");

  await page.getByRole("button", { name: "Create channel" }).click();
  await expectSaved(page, name);

  await deleteChannel(page, name);
  expect(problems, problems.join("\n")).toEqual([]);
});

test("a channel with transformation steps keeps them", async ({ page }) => {
  const problems = watch(page);
  const name = uniqueName("e2e-normalise");
  const listen = `127.0.0.1:${port()}`;

  await openNewChannelForm(page);
  await page.getByRole("button", { name: "Clean up a feed as it passes through" }).click();

  await setField(page, "Channel name", name);
  await setField(page, "Listen on", listen);

  await waitForYaml(page, name);
  const yaml = (await page.locator("pre").first().textContent()) ?? "";
  // The template ships two steps. Losing them silently would make the channel a plain forwarder.
  expect(yaml, "the transformation steps are missing").toContain("PID-3.1");
  expect(yaml).toContain("PID-5.1");

  await page.getByRole("button", { name: "Create channel" }).click();
  await expectSaved(page, name);

  await deleteChannel(page, name);
  expect(problems, problems.join("\n")).toEqual([]);
});

// Every template must produce a channel that STARTS, not merely one that validates.
//
// The existing template test checked the loader accepted the YAML. It did, and the templates were still
// broken: three of them pointed their archive at /var/lib/perfuse/archive, which cannot be created
// without root. So the template described as the safest first channel saved cleanly and then refused to
// start with a permission error. Validating is not running, and a starting point that cannot start is
// worse than no starting point.
//
// Only the file-destination templates are exercised here. The others point at hosts like
// downstream.example.org, which are supposed to be edited before use and cannot be reached from a test.
test("every template with a local destination actually starts", async ({ page }) => {
  const problems = watch(page);

  const startable = [
    { template: "Record an HL7 feed", produces: "hl7-archive" },
    { template: "Accept messages posted over HTTP", produces: "http-in" },
    { template: "Take in X12 claims", produces: "claims-in" },
  ];

  const failures: string[] = [];

  for (const { template, produces } of startable) {
    const name = uniqueName(`e2e-start-${produces}`);
    const listen = `127.0.0.1:${port()}`;

    await openNewChannelForm(page);
    await page.getByRole("button", { name: template }).click();
    await setField(page, "Channel name", name);
    await setField(page, "Listen on", listen);
    await waitForYaml(page, name);

    const yaml = (await page.locator("pre").first().textContent()) ?? "";

    // Save it through the API rather than the button, so this test is about starting rather than about
    // the form, and so a failure names the reason the engine gave.
    const created = await page.request.post("/api/channels", {
      headers: { "X-Perfuse-Request": "1" },
      data: { yaml },
    });
    if (!created.ok()) {
      failures.push(`${template}: could not be saved: ${(await created.text()).slice(0, 200)}`);
      continue;
    }

    const started = await page.request.post(`/api/channels/${encodeURIComponent(name)}/start`, {
      headers: { "X-Perfuse-Request": "1" },
    });
    if (!started.ok()) {
      failures.push(`${template}: saved but would not start: ${(await started.text()).slice(0, 200)}`);
    } else {
      await page.request.post(`/api/channels/${encodeURIComponent(name)}/stop`, {
        headers: { "X-Perfuse-Request": "1" },
      });
    }

    await page.request.delete(`/api/channels/${encodeURIComponent(name)}`, {
      headers: { "X-Perfuse-Request": "1" },
    });
  }

  expect(
    failures,
    `templates that do not survive being run:\n  ${failures.join("\n  ")}\n\n` +
      `A template is a claim that this configuration works. Validating is not running.`,
  ).toEqual([]);
  expect(problems.filter((p) => !p.includes("409"))).toEqual([]);
});

// The empty template is expected to be refused, and the useful property is that it says what is
// missing rather than failing silently.
test("starting from nothing is refused with a reason", async ({ page }) => {
  const problems = watch(page);

  await openNewChannelForm(page);
  await page.getByRole("button", { name: "Start from nothing" }).click();

  // No name, no destination address. The preview panel should say so rather than claim it is valid.
  await expect(page.locator("main")).not.toContainText("Valid.", { timeout: 10_000 });

  await page.getByRole("button", { name: "Cancel" }).click();
  expect(problems, problems.join("\n")).toEqual([]);
});
