import { test, expect } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";

import { openTab, tabLabels } from "./nav";

// What every screen looks like on arrival, before anybody has typed anything.
//
// The question this answers. Several screens show red text the moment they open, and red means one thing in an interface: something is wrong, and
// probably something you did. A screen that opens red has spent that meaning before the user has done anything, so when their input really is
// wrong there is no way to say so that stands out from the decoration.
//
// The distinction being drawn here is between three different things that had all been coloured the same way:
//
//   - a fact about how the product works, which is information and should never be red
//   - a state the operator may want to change, such as a feature not being configured, which is a note
//   - something that is actually wrong right now, which is the only case red is for
//
// This runs as a report rather than only as an assertion, because the useful output is the list of what each screen says in red and why. It writes
// that list to qa-report.json so it can be read after the run, and then asserts the part that must not regress.

/** redOnArrival is every visible element carrying error styling, with its text. */
async function redOnArrival(page: import("@playwright/test").Page) {
  return page.evaluate(() => {
    const out: { text: string; classes: string; role: string | null }[] = [];

    // Matched on the styling rather than on a component name, because the point is what a person sees.
    const candidates = document.querySelectorAll(
      '[class*="text-red-"],[class*="text-rose-"],[class*="border-red-"],[class*="bg-red-"]',
    );

    // Inside a code box, red is a token type rather than a judgement: it colours HL7 segment names and XML element names, and carries no claim
    // that anything is wrong. A different vocabulary, deliberately left alone.
    const inCode = (el: Element) => el.closest("pre, code, .cm-editor, [data-code-area]") !== null;

    // Severity words, where red is the meaning rather than the decoration. A critical alert listed as critical is not a screen opening red at
    // somebody; it is the word for the thing.
    const severityWords = new Set(["critical", "error", "failed", "high"]);

    for (const el of Array.from(candidates)) {
      const box = el.getBoundingClientRect();
      if (box.width === 0 || box.height === 0) continue;
      if (inCode(el)) continue;

      const style = window.getComputedStyle(el);
      if (style.visibility === "hidden" || style.display === "none" || style.opacity === "0") continue;

      const text = (el.textContent ?? "").trim().replace(/\s+/g, " ");
      if (!text) continue;

      // Only the innermost element carrying the text, or a wrapper and its child both report the same sentence.
      if (Array.from(el.children).some((child) => (child.textContent ?? "").trim() === text)) continue;

      if (severityWords.has(text.toLowerCase())) continue;

      out.push({
        text: text.slice(0, 200),
        classes: el.className.toString().slice(0, 160),
        role: el.getAttribute("role"),
      });
    }

    return out;
  });
}

test("no screen opens with red text before anybody has done anything", async ({ page }) => {
  await page.goto("/");

  const labels = await tabLabels(page);
  expect(labels.length).toBeGreaterThan(10);

  const report: Record<string, { text: string; classes: string; role: string | null }[]> = {};
  const offenders: string[] = [];

  for (const label of labels) {
    await openTab(page, label);

    // Settled rather than immediate. A screen that flashes red while loading and then resolves is a different problem from one that stays red, and
    // this test is about the resting state.
    await page.waitForLoadState("networkidle").catch(() => {});
    await page.waitForTimeout(400);

    const red = await redOnArrival(page);
    report[label] = red;

    if (red.length > 0) {
      offenders.push(`${label}: ${red.map((r) => r.text.slice(0, 90)).join(" | ")}`);
    }
  }

  mkdirSync("qa", { recursive: true });
  writeFileSync("qa/qa-report.json", JSON.stringify(report, null, 2));

  // The assertion, with the whole list in the failure message. A count would say something regressed without saying what.
  expect(offenders, `screens showing red text on arrival:\n\n${offenders.join("\n\n")}`).toEqual([]);
});
