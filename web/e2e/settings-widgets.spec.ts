import { test, expect, type Page } from "@playwright/test";
import { openTab } from "./nav";

/**
 * Operates every control in every settings group, and checks a saved setting comes back.
 *
 * # Why this is separate from the general widget sweep
 *
 * The general sweep meets each screen as it arrives. Settings does not put its controls on one screen: there are ten groups shown one
 * at a time, and thirty-eight registered settings behind them. Sweeping the arrival state reaches seven controls out of sixty-seven,
 * which would have looked like coverage and been a tenth of it.
 *
 * # What it asserts
 *
 * Every control reports what it was given - typed, ticked, chosen or dragged. Then one setting is saved and read back from a fresh
 * page load, because a settings screen that accepts input and forgets it is the failure mode that matters here and every
 * value-reflection check in the world passes while it happens.
 *
 * Sliders get particular attention. Where a slider and a number box drive the same setting they must agree, and a pair that drifts is
 * a real defect that no single-control check can see: each control is individually correct and the screen still lies about its state.
 */

const GROUPS = ["Alerts", "Branding", "Data", "Engine", "FHIR", "Fleet", "Monitoring", "Security", "Sign-in", "TEFCA"];

async function openGroup(page: Page, group: string) {
  await page.getByRole("button", { name: group, exact: true }).first().click();
  await page.waitForTimeout(350);
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

for (const group of GROUPS) {
  test(`every control in settings group ${group} responds`, async ({ page }) => {
    test.setTimeout(120_000);

    const faults = watchForFaults(page);
    const problems: string[] = [];

    await page.goto("/");
    await openTab(page, "Settings");
    await openGroup(page, group);

    expect(faults, `${group} reported errors on arrival:\n${faults.join("\n")}`).toEqual([]);

    // Text and number boxes.
    const texts = page.locator("main input[type=text], main input[type=number], main input:not([type])");
    for (let i = 0; i < (await texts.count()); i += 1) {
      const box = texts.nth(i);
      if (!(await box.isVisible()) || !(await box.isEnabled())) continue;

      const label = (await box.getAttribute("aria-label")) ?? (await box.getAttribute("id")) ?? `text ${i}`;
      const numeric = (await box.getAttribute("type")) === "number";
      const sample = numeric ? "12" : "perfuse-qa";
      const before = await box.inputValue();

      faults.length = 0;
      try {
        await box.fill(sample, { timeout: 5_000 });
      } catch {
        problems.push(`${label}: would not accept typing`);
        continue;
      }

      const got = await box.inputValue();
      if (got.trim().toLowerCase() !== sample.toLowerCase()) {
        problems.push(`${label}: typed ${JSON.stringify(sample)} and it holds ${JSON.stringify(got)}`);
      }
      if (faults.length) problems.push(`${label}: typing produced ${faults.join("; ")}`);

      await box.fill(before).catch(() => {});
    }

    // Checkboxes.
    const boxes = page.locator("main input[type=checkbox]");
    for (let i = 0; i < (await boxes.count()); i += 1) {
      const cb = boxes.nth(i);
      if (!(await cb.isVisible()) || !(await cb.isEnabled())) continue;

      const label = (await cb.getAttribute("aria-label")) ?? (await cb.getAttribute("id")) ?? `checkbox ${i}`;
      const before = await cb.isChecked();

      faults.length = 0;
      await cb.click({ timeout: 5_000 }).catch(() => problems.push(`${label}: would not click`));
      await page.waitForTimeout(150);

      if ((await cb.isChecked()) === before) problems.push(`${label}: clicked and stayed ${before}`);
      if (faults.length) problems.push(`${label}: clicking produced ${faults.join("; ")}`);

      await cb.setChecked(before).catch(() => {});
    }

    // Radio groups: each option can be selected, and selecting one clears the rest.
    const radios = page.locator("main input[type=radio]");
    const radioCount = await radios.count();
    for (let i = 0; i < radioCount; i += 1) {
      const r = radios.nth(i);
      if (!(await r.isVisible()) || !(await r.isEnabled())) continue;

      const label = (await r.getAttribute("value")) ?? `radio ${i}`;

      faults.length = 0;
      await r.click({ timeout: 5_000 }).catch(() => problems.push(`${label}: would not click`));
      await page.waitForTimeout(150);

      if (!(await r.isChecked())) problems.push(`${label}: clicked and did not select`);

      // Exactly one of a group may be selected. Two selected at once means the name attribute is missing or wrong, which looks fine
      // until somebody saves and gets whichever value the form serialised last.
      const name = await r.getAttribute("name");
      if (name) {
        const checked = await page.locator(`main input[type=radio][name="${name}"]:checked`).count();
        if (checked !== 1) problems.push(`${label}: ${checked} options selected in group ${name}, want exactly 1`);
      }
      if (faults.length) problems.push(`${label}: clicking produced ${faults.join("; ")}`);
    }

    // Selects, every option.
    const selects = page.locator("main select");
    for (let i = 0; i < (await selects.count()); i += 1) {
      const sel = selects.nth(i);
      if (!(await sel.isVisible()) || !(await sel.isEnabled())) continue;

      const label = (await sel.getAttribute("aria-label")) ?? (await sel.getAttribute("id")) ?? `select ${i}`;
      const values = await sel.locator("option").evaluateAll((os) => os.map((o) => (o as HTMLOptionElement).value));
      const before = await sel.inputValue();

      for (const value of values) {
        faults.length = 0;
        await sel.selectOption(value, { timeout: 5_000 }).catch(() => {
          problems.push(`${label}: would not accept ${JSON.stringify(value)}`);
        });
        await page.waitForTimeout(120);

        if ((await sel.inputValue()) !== value) {
          problems.push(`${label}: chose ${JSON.stringify(value)} and it reports ${JSON.stringify(await sel.inputValue())}`);
        }
        if (faults.length) problems.push(`${label}: choosing ${JSON.stringify(value)} produced ${faults.join("; ")}`);
      }

      await sel.selectOption(before).catch(() => {});
    }

    // Sliders, and the number box beside them if there is one.
    const sliders = page.locator("main input[type=range]");
    for (let i = 0; i < (await sliders.count()); i += 1) {
      const sl = sliders.nth(i);
      if (!(await sl.isVisible()) || !(await sl.isEnabled())) continue;

      const label = (await sl.getAttribute("aria-label")) ?? (await sl.getAttribute("id")) ?? `slider ${i}`;
      const before = await sl.inputValue();

      faults.length = 0;
      await sl.focus();
      for (let k = 0; k < 3; k += 1) await page.keyboard.press("ArrowRight");
      await page.waitForTimeout(250);

      let after = await sl.inputValue();
      if (after === before) {
        for (let k = 0; k < 3; k += 1) await page.keyboard.press("ArrowLeft");
        await page.waitForTimeout(250);
        after = await sl.inputValue();
        if (after === before) problems.push(`${label}: would not move in either direction from ${before}`);
      }
      if (faults.length) problems.push(`${label}: moving produced ${faults.join("; ")}`);

      // If a number box shows the same setting, it has to agree. Two controls for one value that disagree is worse than one
      // control, because the screen is now stating two different things about what the server will be told.
      // Found by walking up to the nearest shared container, rather than by guessing at an id convention. CSS.escape is a browser
      // API and is not defined in the test process, which is how the first version of this failed - a reminder that code in a spec
      // file and code inside evaluate() run in different worlds.
      const shown = await sl.evaluate((el) => {
        let node: HTMLElement | null = el as HTMLElement;
        for (let up = 0; up < 4 && node; up += 1) {
          const n = node.querySelector('input[type="number"]') as HTMLInputElement | null;
          if (n) return n.value;

          node = node.parentElement;
        }
        return null;
      });
      if (shown !== null && shown !== after) {
        problems.push(`${label}: slider says ${after} and the number box beside it says ${shown}`);
      }
    }

    expect(problems, `settings group ${group}: ${problems.length} problem(s):\n${problems.join("\n")}`).toEqual([]);
  });
}

test("a saved setting survives a reload", async ({ page }) => {
  test.setTimeout(120_000);

  // The one assertion the per-control checks cannot make.
  //
  // Everything above proves a control reports what it was given. This proves the server was told, which is a different claim: a
  // settings screen that accepts input, says "Saved", and writes nothing satisfies every reflection check there is.
  //
  // monitoring.traceEndpoint is used rather than something prominent because one server serves the whole suite. Renaming the product
  // or changing an accent colour would rebrand every spec that ran afterwards, and the failures would land somewhere else entirely.
  const control = "#monitoring\\.traceEndpoint-control";

  await page.goto("/");
  await openTab(page, "Settings");
  await openGroup(page, "Monitoring");

  const box = page.locator(control);
  await expect(box, "the trace endpoint setting has no control on the Monitoring group").toBeVisible();

  const before = await box.inputValue();
  const wanted = `https://qa-${Date.now()}.invalid/traces`;

  await box.fill(wanted);

  // The button appears only when something has changed, which is itself worth asserting: a save button that is always live tells
  // an operator nothing about whether they have edited anything.
  const save = page.getByRole("button", { name: "Save changes" });
  await expect(save, "nothing offered to save after a setting was edited").toBeVisible({ timeout: 10_000 });
  await save.click();
  await page.waitForTimeout(1_500);

  await page.reload();
  await openTab(page, "Settings");
  await openGroup(page, "Monitoring");

  await expect(
    page.locator(control),
    "the setting was saved and the screen came back with the old value, so the save reported success and changed nothing",
  ).toHaveValue(wanted, { timeout: 10_000 });

  // Put it back, so later specs see the server they were written against.
  await page.locator(control).fill(before);
  await page.getByRole("button", { name: "Save changes" }).click().catch(() => {});
  await page.waitForTimeout(1_200);
});
