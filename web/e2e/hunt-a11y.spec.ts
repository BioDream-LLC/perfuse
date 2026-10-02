import { test, expect } from "@playwright/test";
import { VIEWS } from "./views";
import { openTab } from "./nav";

// Every view, as a screen reader and a keyboard meet it.
//
// Four failures that make a control unusable without sight, each checked against the accessible name the browser
// itself computes rather than against attributes, so a label that exists but is not associated still fails:
//
//   - a button or link with no accessible name is announced as "button" and nothing else
//   - a form field with no label is announced by its type alone
//   - an image with no alt is read out as a file name, or skipped with its meaning
//   - two elements with one id break every label and aria reference that points at it
//
// And one for the keyboard: after pressing Tab, the focused element must be visible on screen - focus that lands
// on something hidden is focus a keyboard user cannot follow.

for (const view of VIEWS) {
  test(`${view} is usable without a mouse or sight`, async ({ page }) => {
    await page.goto("/");
    await openTab(page, view);
    await page.waitForTimeout(1_200);

    const found = await page.evaluate(() => {
      const out: string[] = [];
      const visible = (el: Element) => {
        const r = el.getBoundingClientRect();
        const cs = getComputedStyle(el);
        return r.width > 0 && r.height > 0 && cs.visibility !== "hidden" && cs.display !== "none";
      };
      const describe = (el: Element) =>
        `${el.tagName.toLowerCase()}${el.id ? "#" + el.id : ""}.${(el.getAttribute("class") || "").split(" ").slice(0, 3).join(".")} "${(el.textContent || "").trim().slice(0, 30)}"`;

      // Accessible name, approximately as the browser computes it.
      const nameOf = (el: Element): string => {
        const labelledby = el.getAttribute("aria-labelledby");
        if (labelledby) {
          const t = labelledby.split(/\s+/).map((id) => document.getElementById(id)?.textContent || "").join(" ").trim();
          if (t) return t;
        }
        const aria = el.getAttribute("aria-label");
        if (aria?.trim()) return aria.trim();
        if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement || el instanceof HTMLSelectElement) {
          if (el.labels && el.labels.length) return Array.from(el.labels).map((l) => l.textContent || "").join(" ").trim();
          const title = el.getAttribute("title") || (el as HTMLInputElement).placeholder;
          return title?.trim() || "";
        }
        const text = (el.textContent || "").trim();
        if (text) return text;
        const img = el.querySelector("img[alt], svg[aria-label]");
        if (img) return (img.getAttribute("alt") || img.getAttribute("aria-label") || "").trim();
        return (el.getAttribute("title") || "").trim();
      };

      for (const el of Array.from(document.querySelectorAll("main button, main a[href], main [role=button], main [role=tab], main [role=switch]"))) {
        if (visible(el) && !nameOf(el)) out.push(`control with no name: ${describe(el)}`);
      }
      for (const el of Array.from(document.querySelectorAll("main input:not([type=hidden]), main textarea, main select"))) {
        if (!visible(el)) continue;
        // CodeMirror and similar editors carry their name on the editable region.
        if (el.closest(".cm-editor")) continue;
        if (!nameOf(el)) out.push(`field with no label: ${describe(el)}`);
      }
      for (const el of Array.from(document.querySelectorAll("main img"))) {
        if (visible(el) && !el.hasAttribute("alt")) out.push(`image with no alt: ${describe(el)}`);
      }
      const ids = new Map<string, number>();
      for (const el of Array.from(document.querySelectorAll("[id]"))) ids.set(el.id, (ids.get(el.id) || 0) + 1);
      for (const [id, n] of ids) if (n > 1 && id) out.push(`id used ${n} times: ${id}`);
      return out;
    });

    // Keyboard: the first few Tab stops inside the view are on screen when focused.
    const hidden: string[] = [];
    await page.locator("main").click({ position: { x: 1, y: 1 } }).catch(() => {});
    for (let i = 0; i < 12; i++) {
      await page.keyboard.press("Tab");
      const where = await page.evaluate(() => {
        const el = document.activeElement;
        if (!el || el === document.body) return null;
        const r = el.getBoundingClientRect();
        const onScreen = r.width > 0 && r.height > 0 && r.bottom > 0 && r.right > 0 && r.top < innerHeight && r.left < innerWidth;
        return onScreen ? null : `${el.tagName.toLowerCase()}.${(el.getAttribute("class") || "").slice(0, 40)} "${(el.textContent || "").trim().slice(0, 30)}"`;
      });
      if (where) hidden.push(`Tab reached something not visible: ${where}`);
    }

    const all = [...new Set([...found, ...hidden])];
    expect(all, all.join("\n")).toEqual([]);
  });
}
