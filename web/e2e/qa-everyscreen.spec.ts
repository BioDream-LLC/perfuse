import { test, expect } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";

import { openTab, tabLabels } from "./nav";

// Every screen, checked for the faults that do not announce themselves.
//
// Why a sweep rather than more tests of individual screens. The suite already covers what each screen does. What it did not cover is the condition
// each screen is in on arrival - and that is where the faults nobody reports live. A console error does not show on the page. A request that
// answered 500 leaves a panel merely empty. A button with no accessible name works perfectly for everybody who can see it. None of these fail a
// test about whether the feature works, and all of them are real.
//
// One of these checks already found a defect on a screen everybody visits: the users screen was reporting an error because an unregistered API
// path answered with the web application instead of JSON. That had been true for every installation without passkeys configured, and no test
// noticed, because every test asked whether a feature worked rather than what the screen said.
//
// The output is written to qa/ as a report, and the assertions are on the things that must not regress. A report nobody reads is worth less than a
// test that fails, so both.

type ScreenReport = {
  consoleErrors: string[];
  pageErrors: string[];
  failedRequests: string[];
  unnamedControls: string[];
  headings: string[];
  overflowPx: number;
};

/** unnamedControls is every interactive element with no accessible name. */
async function unnamedControls(page: import("@playwright/test").Page) {
  return page.evaluate(() => {
    const out: string[] = [];

    for (const el of Array.from(document.querySelectorAll("button, a[href], input, select, textarea"))) {
      // Hidden means hidden from assistive technology, not zero-sized.
      //
      // The first version of this skipped anything measuring 0x0, and so missed a nameless button injected to check that it worked - an empty
      // button has no size. But a 0x0 button is still reachable by keyboard and still announced, so it is exactly the case worth catching. Only
      // things genuinely removed from the tree are skipped.
      const style = window.getComputedStyle(el);
      if (style.display === "none" || style.visibility === "hidden") continue;
      if (el.closest("[aria-hidden='true']") || el.closest("[hidden]")) continue;
      if (el.hasAttribute("disabled")) continue;

      // The same sources a screen reader would use, in roughly the order it would use them.
      const name =
        el.getAttribute("aria-label") ??
        (el.getAttribute("aria-labelledby")
          ? (document.getElementById(el.getAttribute("aria-labelledby")!)?.textContent ?? "")
          : "") ??
        "";

      const own = (el.textContent ?? "").trim();
      const title = el.getAttribute("title") ?? "";
      const placeholder = el.getAttribute("placeholder") ?? "";

      // A label pointing at it by id, or wrapping it.
      const id = el.getAttribute("id");
      const labelled = id ? document.querySelector(`label[for="${CSS.escape(id)}"]`) : null;
      const wrapping = el.closest("label");

      const named =
        name.trim() !== "" ||
        own !== "" ||
        title !== "" ||
        placeholder !== "" ||
        (labelled?.textContent ?? "").trim() !== "" ||
        (wrapping?.textContent ?? "").trim() !== "";

      if (!named) {
        out.push(`<${el.tagName.toLowerCase()} class="${el.className.toString().slice(0, 60)}">`);
      }
    }

    return out;
  });
}

test("every screen arrives clean", async ({ page }) => {
  const report: Record<string, ScreenReport> = {};

  const consoleErrors: string[] = [];
  const pageErrors: string[] = [];
  const failedRequests: string[] = [];

  page.on("console", (msg) => {
    if (msg.type() !== "error") return;
    const text = msg.text();

    // Chromium logs a console error for every failed request as well, which would double-count what the response handler already records.
    if (text.includes("Failed to load resource")) return;
    consoleErrors.push(text.slice(0, 200));
  });

  page.on("pageerror", (err) => pageErrors.push(String(err).slice(0, 200)));

  page.on("response", (res) => {
    if (res.status() < 400) return;
    if (!res.url().includes("/api/")) return;
    failedRequests.push(`${res.status()} ${res.request().method()} ${new URL(res.url()).pathname}`);
  });

  await page.goto("/");
  const labels = await tabLabels(page);

  for (const label of labels) {
    consoleErrors.length = 0;
    pageErrors.length = 0;
    failedRequests.length = 0;

    await openTab(page, label);
    await page.waitForLoadState("networkidle").catch(() => {});
    await page.waitForTimeout(500);

    // Horizontal overflow, which is how a broken layout shows up on a narrower screen than the one it was built on.
    const overflowPx = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );

    const headings = await page
      .locator("h1, h2, h3")
      .evaluateAll((els) => els.map((e) => `${e.tagName}: ${(e.textContent ?? "").trim().slice(0, 60)}`));

    report[label] = {
      consoleErrors: [...consoleErrors],
      pageErrors: [...pageErrors],
      failedRequests: [...failedRequests],
      unnamedControls: await unnamedControls(page),
      headings,
      overflowPx,
    };
  }

  mkdirSync("qa", { recursive: true });
  writeFileSync("qa/screens.json", JSON.stringify(report, null, 2));

  // Asserted separately so a failure says which kind of fault it is rather than that something somewhere is wrong.
  const withPageErrors = Object.entries(report)
    .filter(([, r]) => r.pageErrors.length > 0)
    .map(([k, r]) => `${k}: ${r.pageErrors.join("; ")}`);
  expect(withPageErrors, "screens that threw an uncaught error").toEqual([]);

  const withConsoleErrors = Object.entries(report)
    .filter(([, r]) => r.consoleErrors.length > 0)
    .map(([k, r]) => `${k}: ${r.consoleErrors.join("; ")}`);
  expect(withConsoleErrors, "screens logging console errors").toEqual([]);

  const withFailures = Object.entries(report)
    .filter(([, r]) => r.failedRequests.length > 0)
    .map(([k, r]) => `${k}: ${r.failedRequests.join("; ")}`);
  expect(withFailures, "screens whose requests failed").toEqual([]);

  const withUnnamed = Object.entries(report)
    .filter(([, r]) => r.unnamedControls.length > 0)
    .map(([k, r]) => `${k}: ${r.unnamedControls.join(" ")}`);
  expect(withUnnamed, "controls a screen reader would announce as nothing").toEqual([]);

  const withOverflow = Object.entries(report)
    .filter(([, r]) => r.overflowPx > 4)
    .map(([k, r]) => `${k}: ${r.overflowPx}px wider than the window`);
  expect(withOverflow, "screens that scroll sideways").toEqual([]);

  // And every screen names itself, because a screen with no heading gives a person arriving no way to confirm where they are.
  const withoutHeading = Object.entries(report)
    .filter(([, r]) => r.headings.length === 0)
    .map(([k]) => k);
  expect(withoutHeading, "screens with no heading at all").toEqual([]);
});
