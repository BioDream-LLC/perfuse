import { test, expect } from "@playwright/test";
import { VIEWS } from "./views";
import { openTab } from "./nav";

// Every text field in every view, given what people actually paste, then submitted.
//
// use-everything operates every control with whatever is already in it, which is mostly the sample data
// the view ships with - the input a view was written to handle. People paste other things: half a
// message, a Word document, a file in the wrong format, text in another script, something enormous. The
// questions are the ones that matter to whoever pasted it: did the page crash, did the server fall over,
// and was the person told what was wrong rather than left looking at a button that did nothing.
//
// Judged by consequence: an uncaught exception, a 5xx, or a primary action that neither produced a
// result nor said why not.

const INPUTS: [string, string][] = [
  ["empty", ""],
  ["whitespace", "   \n\t  "],
  ["garbage", "}{<>\"'\\;DROP TABLE users;--\u0000"],
  ["wrong format", "%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj"],
  ["unicode", "患者 名前 — Ünïcödé 🩺 \u202Eevil\u202C"],
  ["half a message", "MSH|^~\\&|A|B|C|D|2026|"],
  ["huge", "X".repeat(400_000)],
];

// Actions that would change the shared server for later tests, or sign out. Everything else is pressed.
// "Create", "Add", "Send" and the replay family were missing at first; in Channels that meant pressing Create with a
// garbage name into the fixture every later test relies on.
const NEVER = /sign out|log out|delete|remove|revoke|reset|stop|disable|restore|import|save|apply|deploy|start|create|add|new|upload|send|resend|reprocess|replay|approve|issue/i;

for (const view of VIEWS) {
  test(`hostile input in ${view}`, async ({ page }) => {
    // Settings has dozens of fields, each filled seven times including a 400 KB paste; that alone is minutes.
    test.setTimeout(900_000);
    const crashes: string[] = [];
    page.on("pageerror", (e) => crashes.push(`page error: ${e.message.slice(0, 160)}`));
    page.on("response", (r) => {
      if (r.url().includes("/api/") && r.status() >= 500) crashes.push(`${r.status()} ${new URL(r.url()).pathname}`);
    });

    await page.goto("/");
    await openTab(page, view);
    await page.waitForTimeout(1_000);

    const fields = page.locator("main textarea:visible, main input[type=text]:visible, main input:not([type]):visible");
    const count = await fields.count();
    if (count === 0) test.skip(true, `${view} has no text fields`);

    const silent: string[] = [];
    for (const [label, value] of INPUTS) {
      // The 400 KB paste goes into the first three fields only. Into every field of the channel builder it took longer
      // than the fifteen-minute budget - the probe's cost, not the page's: in Settings five such fills took 711 ms.
      const limit = label === "huge" ? Math.min(count, 3) : count;
      for (let i = 0; i < limit; i++) {
        const f = fields.nth(i);
        if (!(await f.isEditable({ timeout: 2_000 }).catch(() => false))) continue;
        // Bounded: fill() otherwise waits the whole test budget for a field that re-rendered away between the check
        // above and this line, which is how Settings - where typing in the search box replaces the fields - timed out.
        await f.fill(value, { timeout: 5_000 }).catch(() => {});
      }
      // The already-selected tab or toggle is styled as primary and correctly does nothing when pressed again.
      const actions = page.locator('main button.btn-primary:visible:not([aria-selected="true"]):not([aria-pressed="true"])');
      const n = await actions.count();
      for (let i = 0; i < n; i++) {
        const b = actions.nth(i);
        const name = (await b.innerText().catch(() => "")).trim();
        if (!name || NEVER.test(name) || !(await b.isEnabled().catch(() => false))) continue;
        const before = await page.locator("main").innerText().catch(() => "");
        await b.click({ timeout: 5_000 }).catch(() => {});
        await page.waitForTimeout(900);
        const after = await page.locator("main").innerText().catch(() => "");
        const told = await page.locator('main [role="alert"], main [role="status"]').count();
        // Nothing changed and nothing was announced: the person pressed a button and learned nothing.
        if (after === before && told === 0 && label !== "empty" && label !== "whitespace") {
          silent.push(`"${name}" with ${label} input`);
        }
      }
    }
    expect(crashes, crashes.join("\n")).toEqual([]);
    expect([...new Set(silent)], `pressing these did nothing visible:\n${[...new Set(silent)].join("\n")}`).toEqual([]);
  });
}
