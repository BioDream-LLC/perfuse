import { test, expect } from "@playwright/test";
import { openTab } from "./nav";
import { sendMLLP, anADT } from "./mllp";

// Shadowing, with a candidate that actually differs.
//
// Running a proposed configuration beside the live one and comparing the output is what lets somebody
// change an interface without guessing. With no shadowed channel in the fixture, every test of this view
// was a test of the sentence "Nothing is being shadowed" - which proves the empty state renders and
// nothing else.
//
// The fixture's candidate sets PID-8, which the live channel leaves alone, so every message must be
// reported as differing. A comparison that found no differences here would mean the feature is not
// comparing.

function watch(page: import("@playwright/test").Page): string[] {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`page error: ${e}`));
  page.on("console", (m) => {
    if (m.type() === "error" && !/Failed to load resource/.test(m.text())) {
      problems.push(`console: ${m.text()}`);
    }
  });
  return problems;
}

/** shadowPort is the listener of the channel that has a shadow attached. */
function shadowPort(): number {
  const p = Number(process.env.PERFUSE_E2E_SHADOW_MLLP);
  if (!p || Number.isNaN(p)) {
    throw new Error("PERFUSE_E2E_SHADOW_MLLP is not set; the global setup exports it");
  }
  return p;
}

test("shadowing compares a candidate and reports the difference it finds", async ({ page }) => {
  const problems = watch(page);

  // Traffic through the shadowed channel, not the plain one.
  for (let i = 0; i < 3; i++) {
    await sendMLLP(anADT(`SHADOW${i}`), shadowPort());
  }

  await page.goto("/");
  await openTab(page, "Shadow");

  // The channel must be listed as shadowed at all, which is the part that was untestable before.
  await expect(page.locator("main"), "the shadowed channel is not listed").toContainText("shadowed", {
    timeout: 25_000,
  });

  // And the comparison must have run. Compared is the count of messages put through both.
  await expect(
    page.locator("main"),
    "no messages were compared, so shadowing did not run",
  ).toContainText(/Compared/i, { timeout: 25_000 });

  // The candidate sets a field the live channel does not, so it must differ. Reporting no differences
  // would mean the comparison is not comparing.
  await expect(
    page.locator("main"),
    "the candidate changes PID-8 and yet no difference was reported",
  ).toContainText(/Differed/i, { timeout: 25_000 });

  expect(problems, problems.join("\n")).toEqual([]);
});

test("a shadow report says which field differed", async ({ page }) => {
  const problems = watch(page);

  await sendMLLP(anADT("SHADOWFIELD"), shadowPort());

  await page.goto("/");
  await openTab(page, "Shadow");

  await expect(page.locator("main")).toContainText("shadowed", { timeout: 25_000 });

  // Choose the channel explicitly rather than relying on which one loads first.
  //
  // This test used to pass because exactly one channel was shadowed, so the default selection could only be
  // the right one. Adding a second made it depend on ordering, which is not what it is testing.
  // Required, not optional. Skipping the click when the locator misses would let this test pass while
  // proving nothing, which is the mistake that let a trace panel go untested for weeks.
  await page.getByRole("button", { name: /^shadowed(,|$)/ }).first().click();

  // Naming the field is the whole value of the report. A count of differences with no indication of
  // where they are leaves somebody diffing two configurations by eye, which is what this replaces.
  await expect(
    page.locator("main"),
    "the report counts differences without naming the field",
  ).toContainText(/PID-8/, { timeout: 25_000 });

  expect(problems, problems.join("\n")).toEqual([]);
});

// Switching between shadowed channels must switch the report.
//
// This control only renders when more than one channel is shadowed, so until the fixture had two it was
// unreachable: no test had clicked it, and none could. Behind it were two mistakes that only show up here.
//
// The report is not cleared when the selection changes and the fetch error is swallowed, so a failed or slow
// load leaves the previous channel's comparison on screen with the new channel highlighted. In a tool whose
// entire purpose is to say whether a configuration change is safe, showing the wrong channel's diff is the
// worst failure available: somebody promotes a candidate believing it was verified, and it was not.
test("switching shadowed channels switches the report", async ({ page }) => {
  const problems = watch(page);

  const portB = Number(process.env.PERFUSE_E2E_SHADOW_MLLP_B);
  expect(portB, "PERFUSE_E2E_SHADOW_MLLP_B is not set; the global setup exports it").toBeTruthy();

  // Traffic through both, so each has a comparison to show and neither is empty.
  await sendMLLP(anADT("SWITCHA"), shadowPort());
  await sendMLLP(anADT("SWITCHB"), portB);

  await page.goto("/");
  await openTab(page, "Shadow");

  // Both channels must be offered. The selector is the control under test.
  const pickA = page.getByRole("button", { name: /^shadowed(,|$)/ });
  const pickB = page.getByRole("button", { name: /^shadowed-b(,|$)/ });
  await expect(pickB, "the second shadowed channel is not offered, so the selector never appeared").toBeVisible(
    { timeout: 25_000 },
  );

  // The candidates change different fields, which is what makes the two reports distinguishable. A view
  // that showed the wrong one would otherwise look plausible.
  await pickB.click();
  await expect(
    page.locator("main"),
    "after selecting the second channel the report still names the first",
  ).toContainText("shadowed-b", { timeout: 25_000 });
  await expect(
    page.locator("main"),
    "the second channel's candidate changes PID-7, which is not reported",
  ).toContainText(/PID-7/, { timeout: 25_000 });

  // And back, so this is a switch rather than a one-way trip.
  await pickA.first().click();
  await expect(
    page.locator("main"),
    "switching back does not restore the first channel's difference",
  ).toContainText(/PID-8/, { timeout: 25_000 });

  expect(problems, problems.join("\n")).toEqual([]);
});

// When one channel's report cannot be loaded, the view must not show another channel's.
//
// The failure the happy-path test above cannot reach. The per-channel fetch swallows its error and the
// previous report is left in place, so a request that fails leaves the earlier channel's comparison on
// screen with the newly chosen channel highlighted in the selector.
//
// Why this one is worth a test of its own rather than a shrug: every other view showing stale data during a
// failed refresh is an annoyance. Here the view's only job is to answer "is this configuration change safe",
// and a confident answer about the wrong channel is how somebody promotes an unverified candidate. Silence
// would be correct; the wrong number is not.
test("a report that cannot be loaded does not leave another channel's on screen", async ({ page }) => {
  const portB = Number(process.env.PERFUSE_E2E_SHADOW_MLLP_B);
  await sendMLLP(anADT("FAILA"), shadowPort());
  await sendMLLP(anADT("FAILB"), portB);

  await page.goto("/");
  await openTab(page, "Shadow");

  // Land on the first channel deliberately, so there is something stale to leak. Relying on which channel
  // loads by default is what broke a sibling test when the fixture gained a second one.
  await expect(page.getByRole("button", { name: /^shadowed-b(,|$)/ })).toBeVisible({ timeout: 25_000 });
  await page.getByRole("button", { name: /^shadowed(,|$)/ }).first().click();
  await expect(page.locator("main")).toContainText(/PID-8/, { timeout: 25_000 });

  // Now make the second channel's report unavailable, the way a restart or a permissions problem would.
  await page.route("**/api/channels/shadowed-b/shadow", (route) =>
    route.fulfill({ status: 500, contentType: "application/json", body: '{"error":"unavailable"}' }),
  );

  await page.getByRole("button", { name: /^shadowed-b(,|$)/ }).click();

  // The selector must show the new choice - that part is local and always works.
  await expect(page.locator("main")).toContainText("shadowed-b", { timeout: 15_000 });

  // The claim: whatever is on screen must not be the other channel's comparison presented as this one's.
  // PID-8 belongs to the first candidate. The second changes PID-7. Seeing PID-8 here means the stale
  // report survived the switch.
  await expect(
    page.locator("main"),
    "the first channel's difference is still on screen after selecting the second, whose report failed " +
      "to load. Somebody reading this would believe shadowed-b had been compared and found to differ at " +
      "PID-8, which is another channel's result.",
  ).not.toContainText(/PID-8/, { timeout: 15_000 });
});
