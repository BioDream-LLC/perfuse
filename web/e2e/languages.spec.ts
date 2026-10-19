import { test, expect } from "@playwright/test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { TAB_IDS } from "../src/tabHash";

// Every screen, in every language the console is translated into.
//
// The catalogue test proves every string has a translation. This proves the translations reach the screen: that no
// text the catalogue translates is still shown in English (a string built some way the extractor did not see, a screen
// drawn before the catalogue loaded), and that no screen breaks in another language. Widths are recorded too, since
// German and Finnish run longer than English, and written to qa/ for whoever looks at the layout.

const catDir = path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "src", "i18n", "catalog");
const en = JSON.parse(readFileSync(path.join(catDir, "en.json"), "utf8")) as { strings: string[] };
const LOCALES: Record<string, string> = {
  de: "de-DE", fr: "fr-FR", nl: "nl-NL", es: "es-ES", it: "it-IT", pt: "pt-PT", pl: "pl-PL", sv: "sv-SE", da: "da-DK",
  nb: "nb-NO", fi: "fi-FI",
};

for (const [lang, locale] of Object.entries(LOCALES)) {
  test(`every screen in ${lang}`, async ({ page }) => {
    test.setTimeout(240_000);
    const cat = JSON.parse(readFileSync(path.join(catDir, `${lang}.json`), "utf8")) as Record<string, string>;
    // The English texts this language translates differently: if one of them is on the screen, it was missed.
    // Unless it is also a translation: Italian for "Notes" is "Note", which is English too.
    const outputs = new Set(Object.values(cat).map((t) => t.trim()));
    const translated = en.strings.filter((s) => cat[s] && cat[s].trim() !== s && s.length > 3 && !outputs.has(s));
    const pageErrors: string[] = [];
    page.on("pageerror", (err) => pageErrors.push(String(err).slice(0, 200)));
    await page.addInitScript((l) => localStorage.setItem("perfuse.locale", l), locale);

    const report: Record<string, { untranslated: string[]; overflowPx: number }> = {};
    for (const id of TAB_IDS) {
      await page.goto(`/#/${id}`);
      await page.waitForLoadState("networkidle").catch(() => {});
      await page.waitForTimeout(400);
      expect(await page.evaluate(() => document.documentElement.lang)).toBe(lang);
      const shown = await page.evaluate(() => {
        const out: string[] = [];
        const skip = 'code, pre, kbd, samp, textarea, script, style, .cm-editor, [translate="no"], [data-no-translate]';
        const tw = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
        for (let n = tw.nextNode(); n; n = tw.nextNode()) {
          const el = n.parentElement;
          if (!el || el.closest(skip)) continue;
          const style = window.getComputedStyle(el);
          if (style.display === "none" || style.visibility === "hidden") continue;
          const t = (n.textContent ?? "").replace(/\s+/g, " ").trim();
          if (t) out.push(t);
        }
        return out;
      });
      const want = new Set(translated);
      const untranslated = [...new Set(shown.filter((t) => want.has(t)))];
      const overflowPx = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      report[id] = { untranslated, overflowPx };
    }
    mkdirSync("qa", { recursive: true });
    writeFileSync(`qa/language-${lang}.json`, JSON.stringify(report, null, 2));
    const missed = Object.entries(report).filter(([, r]) => r.untranslated.length > 0);
    expect(missed.map(([id, r]) => `${id}: ${r.untranslated.slice(0, 5).join(" | ")}`), "English left on the screen").toEqual([]);
    expect(pageErrors).toEqual([]);
  });
}
