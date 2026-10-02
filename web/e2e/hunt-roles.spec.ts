import { test, expect, type Browser, type Page } from "@playwright/test";
import { VIEWS } from "./views";
import { openTab } from "./nav";

// Every view, as the people who are not the administrator.
//
// The rest of the suite signs in as admin, which is the one role that can never see a permission
// problem. A view offered to a viewer that then asks the server for something only an editor may have
// shows the viewer a broken screen - and nothing run as admin can notice. So each view is opened as a
// viewer and as an editor, and every refusal the server gives is recorded against the view.
//
// A refusal is not automatically a defect: a view may legitimately try an editor-only call and explain
// why it cannot. It is a defect when the refusal reaches nobody - no alert, no status, nothing on screen
// saying why part of the page is missing. That is what is asserted.
//
// The same pass checks each view on a phone-width screen for content running off the side, and after a
// reload, which is how people arrive at a view from a bookmark.

async function signIn(browser: Browser, baseURL: string, role: string): Promise<Page> {
  const admin = await browser.newContext({ baseURL, storageState: "./e2e/.auth/admin.json" });
  const username = `hunt-${role}-${Date.now()}`;
  const password = "Hunt-password-9f3a-long";
  const made = await admin.request.post("/api/users", {
    headers: { "X-Perfuse-Request": "1" },
    data: { username, password, role },
  });
  expect(made.ok(), await made.text()).toBeTruthy();
  await admin.close();

  const ctx = await browser.newContext({ baseURL, storageState: { cookies: [], origins: [] } });
  const login = await ctx.request.post("/api/login", {
    headers: { "X-Perfuse-Request": "1" },
    data: { username, password },
  });
  expect(login.ok(), await login.text()).toBeTruthy();
  const page = await ctx.newPage();
  await page.goto("/");
  // A first sign-in may be asked to change its password; the views are what is under test, so skip past.
  return page;
}

for (const role of ["viewer", "editor"]) {
  test(`every view works for a ${role}`, async ({ browser, baseURL }) => {
    test.setTimeout(300_000);
    const page = await signIn(browser, baseURL!, role);
    const report: string[] = [];

    const nav = page.locator("nav[role=tablist]");
    await expect(nav).toBeVisible();
    const offered = VIEWS;

    for (const view of offered) {
      const refused: string[] = [];
      const broken: string[] = [];
      const onResponse = (r: import("@playwright/test").Response) => {
        if (!r.url().includes("/api/")) return;
        if (r.status() === 401 || r.status() === 403) refused.push(`${r.request().method()} ${new URL(r.url()).pathname} ${r.status()}`);
        if (r.status() >= 500) broken.push(`${r.request().method()} ${new URL(r.url()).pathname} ${r.status()}`);
      };
      const onError = (e: Error) => broken.push(`page error: ${e.message.slice(0, 160)}`);
      page.on("response", onResponse);
      page.on("pageerror", onError);

      // Is the view offered to this role at all? Views above the role are hidden, which is correct.
      const present = await page
        .getByRole("tab", { name: view, exact: true })
        .or(page.getByRole("menuitem", { name: view, exact: true }))
        .count();
      try {
        await openTab(page, view);
      } catch {
        page.off("response", onResponse);
        page.off("pageerror", onError);
        if (present) report.push(`${view}: offered but could not be opened`);
        continue;
      }
      await page.waitForTimeout(1_200);

      if (refused.length > 0) {
        const explained = await page.locator('main [role="alert"], main [role="status"]').count();
        const text = await page.locator("main").innerText();
        if (explained === 0 && !/permission|not allowed|requires|editor|admin|role/i.test(text)) {
          report.push(`${view}: refused silently: ${[...new Set(refused)].join(", ")}`);
        }
      }
      if (broken.length > 0) report.push(`${view}: ${[...new Set(broken)].join("; ")}`);

      page.off("response", onResponse);
      page.off("pageerror", onError);
    }
    expect(report, report.join("\n")).toEqual([]);
  });
}

test("no view runs off the side of a phone screen", async ({ page }) => {
  test.setTimeout(300_000);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");
  const report: string[] = [];
  for (const view of VIEWS) {
    await openTab(page, view);
    await page.waitForTimeout(800);
    const over = await page.evaluate(() => {
      const doc = document.documentElement;
      if (doc.scrollWidth <= doc.clientWidth + 1) return null;
      // Name the widest thing responsible, so the report can be acted on.
      let worst = "";
      let widest = 0;
      for (const el of Array.from(document.querySelectorAll("main *"))) {
        const r = el.getBoundingClientRect();
        if (r.right > doc.clientWidth + 1 && r.width > widest && getComputedStyle(el).position !== "fixed") {
          widest = r.width;
          worst = `${el.tagName.toLowerCase()}.${(el.getAttribute("class") || "").split(" ").slice(0, 3).join(".")}`;
        }
      }
      return `${doc.scrollWidth}px wide in ${doc.clientWidth}px, widest: ${worst} (${Math.round(widest)}px)`;
    });
    if (over) report.push(`${view}: ${over}`);
  }
  expect(report, report.join("\n")).toEqual([]);
});

test("every view survives a reload", async ({ page }) => {
  test.setTimeout(300_000);
  await page.goto("/");
  const report: string[] = [];
  for (const view of VIEWS) {
    await openTab(page, view);
    const errors: string[] = [];
    const onError = (e: Error) => errors.push(e.message.slice(0, 120));
    page.on("pageerror", onError);
    await page.reload();
    await page.waitForTimeout(800);
    const selected = await page.locator("header nav[role=tablist] [role=tab][aria-selected=true]").first().innerText().catch(() => "");
    if (selected.trim() !== view) report.push(`${view}: a reload lands on "${selected.trim()}"`);
    if (errors.length) report.push(`${view}: ${errors.join("; ")}`);
    page.off("pageerror", onError);
  }
  expect(report, report.join("\n")).toEqual([]);
});
