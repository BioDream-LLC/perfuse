import { test, expect, type Page } from "@playwright/test";
import { openTab } from "./nav";

/**
 * Operates every control on every screen and asserts the interaction had an effect.
 *
 * # Why a sweep rather than a spec per screen
 *
 * There are twenty-two views and several hundred controls. Written by hand that is a spec per screen that nobody would keep current,
 * and the gap between "a control exists" and "a control was tested" would be invisible. This finds the controls itself, so a widget
 * added tomorrow is swept tomorrow without anybody remembering to add it.
 *
 * # What it asserts, and why that is more than "nothing crashed"
 *
 * For every control it checks the application does not report an uncaught error and does not blank the screen. That alone is worth
 * having: a null in one API response blanked the whole settings screen and twenty-six specs missed it.
 *
 * But "nothing broke" is a weak claim on its own, so each kind of control is also checked for the effect only that interaction can
 * produce:
 *
 *   - a text box holds what was typed
 *   - a checkbox reports the opposite state
 *   - a select reports the option that was chosen
 *   - a slider reports a different value
 *
 * A control that silently discards input passes a click test and fails this one. That is the exact defect found in the channel
 * builder, where six boxes displayed text that had already been thrown away.
 *
 * # What it deliberately does not do
 *
 * It does not press buttons that destroy data, sign out, or navigate away - those are named below and covered by specs that can make
 * assertions about the consequences. A sweep that deleted a channel would be testing that deletion works by making every later
 * assertion meaningless.
 */

const VIEWS = [
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
  "Flow map",
  "Playground",
  "Shadow",
  "Certificates",
  "Users",
  "Activity",
  "Settings",
  "TEFCA",
];

/**
 * Buttons this sweep must not press, matched against the accessible name.
 *
 * Each is here because pressing it would invalidate what follows rather than because it is untested. Deletion, sign-out and anything
 * that leaves the page belong in specs that assert on the consequence.
 */
/**
 * Screens with nothing to operate when the server is healthy, which is the state a test harness is in.
 *
 * Empty now, and worth keeping for the next screen that needs it. Alerts was the only entry: with nothing firing there was nothing to
 * acknowledge or dismiss, so the sweep found no controls at all - and an empty sweep is indistinguishable from a locator that stopped
 * matching, which is why it was named here rather than passing quietly.
 *
 * It came out because the screen now has a rules editor on it, which is the better answer to the gap the exemption was documenting: an
 * operator could see how many rules were being checked and could not change any of them without leaving for the settings screen and
 * editing YAML. The test that removes an entry from here is the one that told me to - it fails when a listed screen grows controls,
 * rather than quietly continuing to skip them.
 */
const NO_CONTROLS_WHEN_QUIET = new Set<string>([]);

const DO_NOT_PRESS =
  /delete|remove|sign out|log ?out|revoke|reset|clear|discard|undeploy|stop|restart|shut ?down|drop|purge|wipe|deploy|save|apply|upload|download|print|export/i;

/**
 * Escapes an id for use in a CSS selector.
 *
 * Written out because CSS.escape is a browser API and this file runs in the test process, which is a distinction that has already cost
 * time once in this suite. Ids here contain dots - settings keys look like monitoring.traceEndpoint - and an unescaped dot in a
 * selector means a class.
 */
function cssEscape(id: string): string {
  return id.replace(/([^a-zA-Z0-9_-])/g, "\\$1");
}

/** Reports uncaught errors and error-level console output, ignoring network noise a harness legitimately produces. */
function watchForFaults(page: Page): string[] {
  const faults: string[] = [];

  page.on("pageerror", (e) => faults.push(`uncaught: ${e.message}`));
  page.on("console", (m) => {
    if (m.type() !== "error") return;

    const text = m.text();
    if (text.includes("Failed to load resource")) return;
    if (text.includes("net::ERR")) return;

    faults.push(`console: ${text}`);
  });

  return faults;
}

/** The screen still has content. A crashed React tree leaves the shell and an empty main. */
async function stillRendered(page: Page, what: string) {
  const text = (await page.locator("main").innerText()).trim();
  expect(text.length, `${what} left the screen blank - ${text.length} characters in main`).toBeGreaterThan(20);
}

for (const view of VIEWS) {
  test(`every control on ${view} responds`, async ({ page }) => {
    test.setTimeout(240_000);

    const faults = watchForFaults(page);

    await page.goto("/");
    await openTab(page, view);
    await page.waitForTimeout(600);

    await stillRendered(page, `${view} on arrival`);
    expect(faults, `${view} reported errors before anything was touched:\n${faults.join("\n")}`).toEqual([]);

    const problems: string[] = [];

    // Counted so this cannot pass by touching nothing.
    //
    // Every control here is found by a locator, and a locator that stops matching - a class renamed, main becoming a different
    // element - makes the loops run zero times and the assertion at the end succeed against an empty list. That is the failure this
    // whole file exists to catch, so it would be a poor joke to leave it in the catcher. use-everything.spec.ts guards the same way
    // and for the same reason.
    let operated = 0;

    // Text boxes hold what is typed into them.
    const texts = page.locator(
      "main input:not([type=checkbox]):not([type=radio]):not([type=file]):not([type=range]):not([readonly]), main textarea:not([readonly])",
    );
    for (let i = 0; i < (await texts.count()); i += 1) {
      const box = texts.nth(i);
      if (!(await box.isVisible()) || !(await box.isEnabled())) continue;

      const label = (await box.getAttribute("aria-label")) ?? (await box.getAttribute("placeholder")) ?? `text ${i}`;
      const type = (await box.getAttribute("type")) ?? "text";

      // A typed sample the field will accept. A number box cannot hold letters and a date box wants a date, so guessing "abc"
      // everywhere would report the browser rejecting nonsense as an application fault.
      const sample = type === "number" ? "7" : type === "date" ? "2026-01-02" : type === "email" ? "a@b.co" : "perfuse-qa";

      faults.length = 0;
      try {
        await box.fill(sample, { timeout: 5_000 });
      } catch {
        problems.push(`${label}: would not accept typing`);
        continue;
      }

      // Compared case-insensitively and trimmed, because several fields normalise on purpose - an HL7 message type box uppercases
      // what it is given, and reporting that as data loss would be reporting a feature. What this still catches is the case that
      // matters: input silently discarded or replaced, which is what the builder was doing to six boxes at once.
      operated += 1;
      const got = await box.inputValue();
      if (got.trim().toLowerCase() !== sample.toLowerCase()) {
        problems.push(`${label}: typed ${JSON.stringify(sample)} and it holds ${JSON.stringify(got)}`);
      }
      if (faults.length) problems.push(`${label}: typing produced ${faults.join("; ")}`);
    }

    await stillRendered(page, `${view} after typing`);

    // Checkboxes report the opposite state after being clicked.
    const boxes = page.locator("main input[type=checkbox]");
    for (let i = 0; i < (await boxes.count()); i += 1) {
      const cb = boxes.nth(i);
      if (!(await cb.isVisible()) || !(await cb.isEnabled())) continue;

      const label = (await cb.getAttribute("aria-label")) ?? `checkbox ${i}`;
      const before = await cb.isChecked();

      faults.length = 0;
      await cb.click({ timeout: 5_000 }).catch(() => problems.push(`${label}: would not click`));
      await page.waitForTimeout(150);

      operated += 1;
      if ((await cb.isChecked()) === before) problems.push(`${label}: clicked and stayed ${before}`);
      if (faults.length) problems.push(`${label}: clicking produced ${faults.join("; ")}`);

      // Put it back, so later controls see the state they were designed against.
      await cb.setChecked(before).catch(() => {});
    }

    // Selects report the option that was chosen, for every option offered.
    //
    // Held by id rather than by position, because choosing an option can change which controls exist. On the alert rules editor,
    // picking a kind that has no destination removes that select from the page, and every select after it shifts up one - so a loop
    // holding the fifth select by index is suddenly pointing at a different control. Playwright then retries the interaction against
    // an element that will never accept it until the five-second timeout expires, nine selects and eleven options at a time, and the
    // test dies at four minutes having reported nothing.
    //
    // Selects with no id are still swept by position, which is fine: a form static enough not to give its controls ids is a form
    // whose controls do not move.
    const selectIDs = await page.locator("main select").evaluateAll((els) => els.map((e) => (e as HTMLSelectElement).id));
    for (let i = 0; i < selectIDs.length; i += 1) {
      const sel = selectIDs[i] ? page.locator(`main select#${cssEscape(selectIDs[i])}`) : page.locator("main select").nth(i);
      if (!(await sel.count()) || !(await sel.isVisible().catch(() => false)) || !(await sel.isEnabled().catch(() => false))) continue;

      const label = (await sel.getAttribute("aria-label")) ?? selectIDs[i] ?? `select ${i}`;
      const values = await sel.locator("option").evaluateAll((os) => os.map((o) => (o as HTMLOptionElement).value));
      const original = await sel.inputValue();

      for (const value of values) {
        faults.length = 0;
        try {
          await sel.selectOption(value, { timeout: 5_000 });
        } catch {
          problems.push(`${label}: would not accept option ${JSON.stringify(value)}`);
          continue;
        }
        await page.waitForTimeout(120);

        operated += 1;
        const got = await sel.inputValue();
        if (got !== value) problems.push(`${label}: chose ${JSON.stringify(value)} and it reports ${JSON.stringify(got)}`);
        if (faults.length) problems.push(`${label}: choosing ${JSON.stringify(value)} produced ${faults.join("; ")}`);
      }

      await sel.selectOption(original).catch(() => {});
      await stillRendered(page, `${view} after using ${label}`);
      void 0;
    }

    // Sliders report a different value after being moved.
    const sliders = page.locator("main input[type=range]");
    for (let i = 0; i < (await sliders.count()); i += 1) {
      const sl = sliders.nth(i);
      if (!(await sl.isVisible()) || !(await sl.isEnabled())) continue;

      const label = (await sl.getAttribute("aria-label")) ?? `slider ${i}`;
      const before = await sl.inputValue();

      faults.length = 0;
      await sl.focus();
      // Keyboard rather than a drag, because a drag depends on where the thumb happens to be and this is asserting the control
      // responds, not that the browser can be dragged.
      for (let k = 0; k < 4; k += 1) await page.keyboard.press("ArrowRight");
      await page.waitForTimeout(200);

      operated += 1;
      const after = await sl.inputValue();
      if (after === before) {
        // The far end of its range is a legitimate reason not to move, so try the other direction before reporting it.
        for (let k = 0; k < 4; k += 1) await page.keyboard.press("ArrowLeft");
        await page.waitForTimeout(200);
        if ((await sl.inputValue()) === before) problems.push(`${label}: would not move in either direction from ${before}`);
      }
      if (faults.length) problems.push(`${label}: moving produced ${faults.join("; ")}`);
    }

    await stillRendered(page, `${view} after sliders`);

    // Radios select when clicked.
    const radios = page.locator("main input[type=radio]");
    for (let i = 0; i < (await radios.count()); i += 1) {
      const r = radios.nth(i);
      if (!(await r.isVisible()) || !(await r.isEnabled())) continue;

      const label = (await r.getAttribute("aria-label")) ?? (await r.getAttribute("value")) ?? `radio ${i}`;

      faults.length = 0;
      await r.click({ timeout: 5_000 }).catch(() => problems.push(`${label}: would not click`));
      await page.waitForTimeout(150);

      operated += 1;
      if (!(await r.isChecked())) problems.push(`${label}: clicked and did not select`);
      if (faults.length) problems.push(`${label}: clicking produced ${faults.join("; ")}`);
    }

    // Buttons, excluding the ones that would invalidate everything after them.
    // The list of buttons is taken once, before any of them is pressed.
    //
    // A live count does not terminate. Pressing a button opens a panel, the panel has buttons, and those buttons open more panels -
    // the Tables sweep ran for four minutes and was still finding new ones. Naming them up front means this sweeps the screen as it
    // arrives, which is the screen a person meets, and anything a button reveals is a separate view to sweep on its own terms.
    const buttons = page.locator("main button");
    const buttonCount = await buttons.count();
    const pressed: string[] = [];
    for (let i = 0; i < buttonCount; i += 1) {
      const b = buttons.nth(i);
      if (!(await b.isVisible().catch(() => false)) || !(await b.isEnabled().catch(() => false))) continue;

      const name = ((await b.getAttribute("aria-label")) ?? (await b.innerText().catch(() => "")) ?? "").trim();
      if (!name || DO_NOT_PRESS.test(name)) continue;

      faults.length = 0;
      await b.click({ timeout: 5_000 }).catch(() => {});
      await page.waitForTimeout(250);

      pressed.push(name);
      operated += 1;
      if (faults.length) problems.push(`button ${JSON.stringify(name)}: pressing produced ${faults.join("; ")}`);

      // A button may legitimately open a panel or a dialog, but it must not blank the screen.
      const text = (await page.locator("main").innerText().catch(() => "")).trim();
      if (text.length <= 20) {
        problems.push(`button ${JSON.stringify(name)}: left the screen blank`);
        break;
      }
    }

    // Every control has a name an assistive technology can read.
    //
    // Included here rather than in a separate accessibility spec because it belongs to the same question: a control nobody can
    // identify is not usable, whatever it does when operated. It also earns its place by having already caught me out - reading the
    // markup by hand, I concluded two settings controls were unlabelled and was wrong, because both are wrapped in a label element
    // that supplies the name implicitly. Computing it the way the browser does is the only way to be sure either way.
    const unnamed = await page.locator("main input, main select, main textarea").evaluateAll((els) =>
      els
        .filter((e) => {
          const el = e as HTMLElement;
          if (el.getAttribute("type") === "hidden") return false;

          const aria = el.getAttribute("aria-label")?.trim();
          const by = el.getAttribute("aria-labelledby");
          const wrapping = el.closest("label")?.textContent?.trim();
          const forLabel = el.id ? document.querySelector(`label[for="${el.id}"]`)?.textContent?.trim() : null;
          const title = el.getAttribute("title")?.trim();

          return !(aria || by || wrapping || forLabel || title);
        })
        .map((e) => {
          const el = e as HTMLElement;
          const where = el.closest("section,div[class*=rounded]")?.textContent?.trim().slice(0, 60) ?? "?";
          return `${el.tagName.toLowerCase()}[${el.getAttribute("type") ?? ""}] id=${el.id || "(none)"} near ${JSON.stringify(where)} html=${el.outerHTML.slice(0, 120)}`;
        }),
    );
    for (const u of unnamed) problems.push(`${u}: has no accessible name, so it cannot be identified by a screen reader`);

    if (NO_CONTROLS_WHEN_QUIET.has(view)) {
      // A screen with genuinely nothing to operate still has to say something, or an empty sweep and a broken locator look the same.
      expect(
        operated,
        `${view} was listed as having no controls when quiet and now has ${operated}. Remove it from NO_CONTROLS_WHEN_QUIET so its controls are swept`,
      ).toBe(0);
      await expect(
        page.locator("main"),
        `${view} has no controls and says nothing about its own state, so there is no way to tell it is working from a broken one`,
      ).toContainText(/rule|checked|nothing|no alerts|quiet|healthy/i);
    } else {
      expect(
        operated,
        `${view}: nothing was operated at all, so the locators stopped matching rather than the screen having no widgets`,
      ).toBeGreaterThan(0);
    }

    expect(
      problems,
      `${view}: ${problems.length} control(s) misbehaved (operated ${operated}, pressed ${pressed.length} button(s)):\n${problems.join("\n")}`,
    ).toEqual([]);
  });
}
