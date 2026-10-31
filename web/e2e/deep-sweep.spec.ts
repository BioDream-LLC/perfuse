import { test, expect, type Page } from "@playwright/test";
import { writeFileSync } from "node:fs";

import { openTab, tabLabels } from "./nav";

// A deeper pass than "every screen arrives clean": on every screen, every inner tab is opened and every button that opens
// something is pressed, with console errors, uncaught errors, server failures and error boundaries recorded on the way.
//
// Buttons that end the session, stop the server or delete what later screens need are never pressed, and nor are the ones
// that commit a change (save, deploy, send): this pass is about what a screen shows and opens, which the feature specs do
// not reach, not about what its actions do, which they do.
const AVOID =
  /sign out|log out|restart|shut ?down|delete|remove|revoke|reset|wipe|purge|discard|stop|step down|disable|deprovision|rotate|forget|drop|clear|unlink|leave/i;
const COMMIT = /^(save|apply|submit|send|deploy|import|publish|approve|deny|decide|promote|replay|resend|run|start|create|add|upload|sign|test|evaluate|generate|convert|check|validate|try|ask|go)\b/i;

type Finding = { screen: string; kind: string; detail: string };

async function settle(page: Page) {
  await page.waitForLoadState("networkidle", { timeout: 8000 }).catch(() => {});
  await page.waitForTimeout(250);
}

test("every screen, tab and opening button works", async ({ page }) => {
  test.setTimeout(30 * 60_000);
  const findings: Finding[] = [];
  let where = "start";
  const note = (kind: string, detail: string) => findings.push({ screen: where, kind, detail: detail.slice(0, 400) });

  page.on("console", (m) => {
    if (m.type() !== "error") return;
    const t = m.text();
    // 401/403/404 are probes answered "off" or "not yours"; 409 is a refusal the screen shows (no traffic yet to learn a
    // contract from, too few records to estimate weights). The screen saying so is checked by the feature specs.
    if (/status of 40[1349]/.test(t)) return;
    note("console", t);
  });
  page.on("pageerror", (e) => note("pageerror", e.message));
  page.on("response", (r) => {
    if (r.status() >= 500) note(`http ${r.status()}`, `${r.request().method()} ${r.url()}`);
  });

  async function inspect() {
    if (await page.getByText(/something went wrong|this (page|view) failed|unexpected error/i).count())
      note("error-boundary", (await page.locator("main, body").first().innerText()).slice(0, 300));
    const text = (await page.locator("[role=tabpanel]").first().innerText().catch(() => "")).trim();
    if (text.length < 15) note("blank", JSON.stringify(text));
    const body = await page.locator("body").innerText();
    for (const bad of ["undefined", "NaN", "[object Object]", "Invalid Date"]) {
      const esc = bad.replace(/[[\]]/g, "\\$&");
      if (new RegExp(`(^|[^A-Za-z])${esc}([^A-Za-z]|$)`).test(body)) note("raw-value", bad);
    }
  }

  await page.goto("/");
  const labels = await tabLabels(page);
  for (const label of labels) {
    writeFileSync("e2e-report/deep-sweep.json", JSON.stringify({ screens: labels, findings }, null, 1));
    where = label;
    await openTab(page, label);
    await settle(page);
    const home = page.url();
    await inspect();

    const inner = page.locator("[role=tabpanel] [role=tab]");
    const innerLabels: string[] = [];
    for (let i = 0; i < (await inner.count()); i++) innerLabels.push((await inner.nth(i).innerText().catch(() => "")).trim());
    for (const tab of innerLabels) {
      if (!tab) continue;
      where = `${label} › ${tab}`;
      const t = page.locator("[role=tabpanel] [role=tab]").filter({ hasText: tab }).first();
      if (!(await t.isVisible().catch(() => false))) continue;
      await t.click({ timeout: 3000 }).catch((e) => note("click-failed", String(e)));
      await settle(page);
      await inspect();
    }

    where = label;
    await page.goto(home);
    await settle(page);
    const names: string[] = [];
    const buttons = page.locator("[role=tabpanel] button:visible");
    for (let i = 0; i < Math.min(await buttons.count(), 60); i++) {
      const b = buttons.nth(i);
      const name = ((await b.getAttribute("aria-label")) ?? (await b.innerText().catch(() => ""))).trim();
      if (name && !AVOID.test(name) && !COMMIT.test(name) && !(await b.isDisabled())) names.push(name);
    }
    for (const name of [...new Set(names)]) {
      where = `${label} › [${name.slice(0, 50)}]`;
      const b = page.locator("[role=tabpanel]").getByRole("button", { name, exact: true }).first();
      if (!(await b.count())) continue;
      await b.click({ timeout: 3000 }).catch(() => {});
      await settle(page);
      const dialog = page.locator("[role=dialog]:visible");
      if (await dialog.count()) {
        if ((await dialog.first().innerText().catch(() => "")).trim().length < 5) note("blank-dialog", name);
        await inspect();
        await page.keyboard.press("Escape");
        await page.waitForTimeout(200);
      }
      if (page.url() !== home) {
        await page.goto(home);
        await settle(page);
      }
    }
  }

  writeFileSync("e2e-report/deep-sweep.json", JSON.stringify({ screens: labels, findings }, null, 1));
  expect(findings, JSON.stringify(findings, null, 1)).toEqual([]);
});
