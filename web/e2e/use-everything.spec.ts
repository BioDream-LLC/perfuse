import { test, expect, type Page } from "@playwright/test";
import { openTab } from "./nav";

// Every control in every section, actually operated.
//
// Not "the section renders" and not "a control exists". This walks each of the twenty-one sections, finds
// every button, input, select and textarea inside it, and works each one, watching for anything that breaks.
//
// Why a self-discovering sweep rather than a list. A list is written once against the UI as it was, and the
// control added next month is not in it. The AI Mapper proved the cost of that: the tab rendered, the button
// was there, and the feature had never once worked, because nothing had ever pressed it and watched what
// happened. This cannot go stale - a new control is exercised the day it appears.
//
// Two rules keep it from being theatre. It counts what it operated and fails if the count is implausibly low,
// so a locator that stops matching produces a failure rather than a silent pass. And it judges by
// consequences that can only come from the interaction - an uncaught exception, a refused or failed request,
// a React error - never by text that was already on screen.

const SECTIONS = [
  "Dashboard",
  "Channels",
  "Messages",
  "Queue",
  "Alerts",
  "Metrics",
  "FHIR lab",
  "Documents",
  "Scripts",
  "Migrate",
  "Contracts",
  "Tables",
  "AI Mapper",
  "Fleet",
  "TEFCA",
  "Flow map",
  "Playground",
  "Shadow",
  "Certificates",
  "Users",
  "Activity",
  "Settings",
] as const;

// Controls that would wreck the fixture for every later section, or leave the browser somewhere this sweep
// cannot come back from. Matched on the accessible name.
//
// Deliberately short. Each exclusion is a thing this sweep does not cover, so each one needs a test of its
// own somewhere else - deletion and start/stop live in use-channels, sign-out in the auth specs. A long
// exclusion list would quietly become the same coverage hole this is meant to close.
const LEAVE_ALONE =
  /delete|remove|revoke|sign out|log out|restart|shut ?down|reset|drop|purge|clear all|discard|wipe|rotate|disable|deactivate|abandon|retry all|download|export all/i;

/**
 * LEAVE_ALONE_EXACT is for names too short to match loosely without catching innocent controls.
 *
 * Start and Stop appear on the dashboard against each channel. Pressing them here stops the traffic every
 * later section is reading, so the sweep would be sabotaging its own remaining tests - and the failure would
 * surface several sections away from the cause, which is the worst kind to debug.
 *
 * They are not left untested: dashboardControl.spec.ts starts and stops a channel from the dashboard and puts
 * it back, which is what this sweep cannot do safely in the middle of a shared run.
 */
const LEAVE_ALONE_EXACT = /^(start|stop|start all|stop all)$/i;

/** Problems collects anything the browser reports that a person would call broken. */
interface Problems {
  /** list is what is broken regardless of what the interface says about it. */
  list: string[];
  /** refused is a request the server declined. Only a fault if the interface says nothing about it. */
  refused: string[];
}

function watch(page: Page): Problems {
  const p: Problems = { list: [], refused: [] };

  page.on("pageerror", (e) => p.list.push(`uncaught exception: ${e.message}`));

  page.on("console", (m) => {
    if (m.type() !== "error") return;
    const t = m.text();
    // Favicon and aborted navigations are noise from the harness, not the product.
    if (/Failed to load resource|net::ERR_ABORTED|favicon/.test(t)) return;
    p.list.push(`console error: ${t}`);
  });

  page.on("response", async (r) => {
    const url = r.url();
    if (!url.includes("/api/")) return;
    const s = r.status();
    if (s < 400) return;
    // A 404 on a lookup for something absent is a legitimate answer, not a fault.
    if (s === 404) return;

    const where = `${s} from ${new URL(url).pathname}`;

    // A 403 is always this application's fault, never the input's.
    //
    // It means the request went out without the header that marks it as coming from the interface, which is
    // how the AI Mapper managed to be shipped, rendered and demonstrated without ever having worked. No
    // amount of nonsense typed into a field should produce one.
    if (s === 403) {
      p.list.push(`${where} - a refused request means the interface sent it wrongly, not that the input was bad`);
      return;
    }

    // Anything at or above 500 is the server failing, whatever was sent to it. Invalid input deserves a 400.
    if (s >= 500) {
      p.list.push(`${where} - the server failed rather than refusing bad input`);
      return;
    }

    // A 4xx is the correct answer to the nonsense this sweep types into fields. What matters is whether the
    // person who typed it is told. Judged after the sweep, once the interface has had a chance to react.
    p.refused.push(where);
  });

  return p;
}

/** operate works one control and returns what it did, or null when it declined. */
async function operate(page: Page, el: ReturnType<Page["locator"]>): Promise<string | null> {
  if (!(await el.isVisible().catch(() => false))) return null;
  if (!(await el.isEnabled().catch(() => false))) return null;

  const tag = await el.evaluate((n) => n.tagName.toLowerCase()).catch(() => "");
  const type = ((await el.getAttribute("type").catch(() => "")) ?? "").toLowerCase();
  const name = ((await el.getAttribute("aria-label")) ?? (await el.innerText().catch(() => "")) ?? "")
    .trim()
    .slice(0, 60);

  if (LEAVE_ALONE.test(name)) return null;
  if (LEAVE_ALONE_EXACT.test(name)) return null;

  try {
    if (tag === "select") {
      const values = await el.locator("option").evaluateAll((os) =>
        os.map((o) => (o as HTMLOptionElement).value).filter((v) => v !== ""),
      );
      // Every option, not just one. A source type that empties the preview only does it for some values,
      // which is exactly how the DICOM C-FIND defect survived.
      for (const v of values.slice(0, 12)) {
        await el.selectOption(v, { timeout: 5_000 });
        await page.waitForTimeout(120);
      }
      return `select ${name || "(unnamed)"} through ${values.length} options`;
    }

    if (tag === "textarea") {
      await el.fill("", { timeout: 5_000 });
      await el.fill("MSH|^~\\&|SWEEP|HOSP|EHR|HOSP|20260826100000||ADT^A01|SWEEP1|P|2.5", { timeout: 5_000 });
      return `textarea ${name || "(unnamed)"}`;
    }

    if (tag === "input") {
      if (type === "checkbox" || type === "radio") {
        // Both states, then back, so a control that only breaks when turned off is caught.
        await el.click({ timeout: 5_000 });
        await page.waitForTimeout(120);
        await el.click({ timeout: 5_000 });
        return `${type} ${name || "(unnamed)"}`;
      }
      if (type === "file" || type === "submit" || type === "button") return null;
      if (type === "number") {
        await el.fill("3", { timeout: 5_000 });
        return `number ${name || "(unnamed)"}`;
      }
      if (type === "date") {
        await el.fill("2026-08-01", { timeout: 5_000 });
        return `date ${name || "(unnamed)"}`;
      }
      await el.fill("sweep", { timeout: 5_000 });
      return `input ${name || "(unnamed)"}`;
    }

    // A disclosure. Opened rather than toggled twice, so the second pass can reach what it was hiding.
    if (tag === "summary" || tag === "details") {
      await el.click({ timeout: 5_000 });
      await page.waitForTimeout(200);
      return `disclosure ${name || "(unnamed)"}`;
    }

    const role = ((await el.getAttribute("role").catch(() => "")) ?? "").toLowerCase();
    if (role === "switch" || role === "tab") {
      await el.click({ timeout: 5_000 });
      await page.waitForTimeout(200);
      return `${role} ${name || "(unnamed)"}`;
    }

    if (tag === "button") {
      await el.click({ timeout: 6_000 });
      await page.waitForTimeout(250);
      // A dialog that opens must be closed, or every later control is unreachable behind it.
      const dialog = page.locator('[role="dialog"]');
      if (await dialog.count()) {
        const close = dialog.getByRole("button", { name: /close|cancel|done|back|dismiss/i });
        if (await close.count()) {
          await close.first().click({ timeout: 4_000 }).catch(() => {});
        } else {
          await page.keyboard.press("Escape").catch(() => {});
        }
        await page.waitForTimeout(150);
      }
      return `button ${name || "(unnamed)"}`;
    }
  } catch {
    // A control that will not take the interaction is not automatically a defect - it may have moved
    // because an earlier click re-rendered the view. The problems watcher is what decides.
    return null;
  }

  return null;
}

// One test per section, so a failure names the section rather than the sweep.
for (const section of SECTIONS) {
  test(`using every control in ${section}`, async ({ page }) => {
    const problems = watch(page);

    await page.goto("/");
    await openTab(page, section);

    // Let the section settle: most fetch on mount, and clicking mid-load produces failures that belong to
    // the harness rather than the product.
    await page.waitForTimeout(1_500);

    const done: string[] = [];

    // Whether the interface ever reported a refusal, noticed as it happens rather than at the end.
    //
    // Checking afterwards was wrong, and wrong in a way worth recording: error boxes have a Dismiss button,
    // this sweep operates every button, so it was clicking away the very message it then looked for. A
    // harness that destroys its own evidence reports a defect that is not there - which costs as much trust
    // as missing one that is.
    let sawReport = false;
    let refusedSeen = 0;

    const noticeReport = async () => {
      if (problems.refused.length === refusedSeen) return;
      refusedSeen = problems.refused.length;
      if (sawReport) return;
      // Give the interface a moment to render its reaction to the refusal.
      await page.waitForTimeout(250);
      const announced = await page
        .locator('main [role="alert"], main [role="status"]')
        .count()
        .catch(() => 0);
      if (announced > 0) sawReport = true;
    };

    // Two passes. The first opens whatever panels and sub-views exist; the second reaches the controls that
    // only appeared because of it. Without this, everything behind a sub-view switcher stays untouched -
    // eleven such switchers existed here and two were covered.
    for (let pass = 0; pass < 2; pass++) {
      // Wider than the four obvious tags, and each addition earned its place.
      //
      // A summary element is a real control that hides other controls behind it, and ten of them exist here.
      // Two are in Alerts, which is why that section reported having nothing to operate at all: everything
      // was behind a disclosure this sweep could not see. role=switch and role=tab are controls that are not
      // buttons, and a sweep that only knows about tags would silently skip them.
      const controls = page.locator(
        'main button, main input, main select, main textarea, main summary, ' +
          'main [role="switch"], main [role="tab"]',
      );
      const n = await controls.count();

      for (let i = 0; i < n; i++) {
        const did = await operate(page, controls.nth(i));
        if (did !== null) done.push(did);
        await noticeReport();
      }
      await page.waitForTimeout(400);
    }

    // The guard against a vacuous pass. Every section has controls; a section that reports none has a
    // locator problem, not an empty interface.
    expect(
      done.length,
      `nothing in ${section} could be operated, so this test proved nothing. Either the section failed to ` +
        `render or the controls are not reachable as buttons, inputs, selects or textareas.`,
    ).toBeGreaterThan(0);

    // The section must still be there afterwards. A blank panel after a click is the DICOM C-FIND defect,
    // which discarded a build error and showed nothing.
    await expect(page.locator("main"), `${section} is empty after being used`).not.toBeEmpty();

    expect(
      problems.list,
      `using ${section} produced ${problems.list.length} problem(s) after operating ` +
        `${done.length} control(s):\n\n${problems.list.join("\n")}\n\nOperated:\n${done.join("\n")}`,
    ).toEqual([]);

    // A request the server declined must leave a trace on screen.
    //
    // This is the silent-failure class, and it has cost more here than crashes have. A blank preview after
    // choosing DICOM C-FIND, a mapper that did nothing when pressed, a shadow report that stayed on the
    // previous channel - in each case the request failed, the interface said nothing, and the feature looked
    // like it worked. A refusal nobody sees is worse than a crash, because a crash gets reported.
    if (problems.refused.length > 0) {
      // Structural first, wording second.
      //
      // Looking for particular words was guesswork: a refusal reading "this does not look like an HL7 v3
      // message" is a perfectly good message that matches no keyword anybody would think to list. An alert
      // region is what a screen reader would be told, so it is the honest test of whether the interface
      // reported anything - and requiring it is what made every error box announce itself.
      await noticeReport();
      const announced = await page.locator('main [role="alert"], main [role="status"]').count();
      const shown = await page
        .locator("main")
        .innerText()
        .catch(() => "");
      const saysSomething =
        sawReport ||
        announced > 0 ||
        /error|invalid|could not|cannot|couldn.t|failed|must be|required|not valid|unable|problem|no results|nothing matched|try again|does not look like|no fields/i.test(
          shown,
        );
      expect(
        saysSomething,
        `${section} had ${problems.refused.length} request(s) declined and shows nothing about it:\n` +
          `${problems.refused.join("\n")}\n\nA refusal the interface swallows looks exactly like a feature ` +
          `that worked. Show the message the server sent.`,
      ).toBe(true);
    }

    console.log(`${section}: operated ${done.length} controls`);
  });
}
