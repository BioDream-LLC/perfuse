import { test, expect } from "@playwright/test";

// The manual's own styling, checked the way the application's is.
//
// The manual was restyled to match the product: a dark palette, a sticky contents rail, cards and coloured tables. That
// turned a plain black-on-white document into something with the same failure modes as the interface, so it gets the same
// two checks the interface has - readable contrast, and a print rendering that is still ink on paper.
//
// It reads the committed file rather than building one, because the committed file is what ships and what anybody who is
// mailed the manual will open.

const MANUAL = `file://${process.env.HOME}/perfuse/docs/manual/perfuse-manual.html`;

// Relative luminance and contrast ratio, per WCAG.
function contrast(fg: number[], bg: number[]): number {
  const lum = (c: number[]) => {
    const f = (v: number) => {
      const s = v / 255;
      return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
    };
    return 0.2126 * f(c[0]) + 0.7152 * f(c[1]) + 0.0722 * f(c[2]);
  };
  const [hi, lo] = [lum(fg), lum(bg)].sort((a, b) => b - a);
  return (hi + 0.05) / (lo + 0.05);
}

test("every text style in the manual is readable", async ({ page }) => {
  await page.goto(MANUAL);

  const samples = await page.evaluate(() => {
    const parse = (s: string) => (s.match(/\d+(\.\d+)?/g) ?? []).slice(0, 3).map(Number);

    // Walks up for a painted background, the same way the application's sweep does. A gradient is invisible to this and
    // there are none in the manual, which is worth knowing if one is ever added.
    const backdrop = (el: Element) => {
      for (let n: Element | null = el; n; n = n.parentElement) {
        const c = getComputedStyle(n).backgroundColor;
        if (c && c !== "rgba(0, 0, 0, 0)" && c !== "transparent") return c;
      }
      return "rgb(255, 255, 255)";
    };

    const out: { what: string; text: string; fg: number[]; bg: number[]; size: number; bold: boolean }[] = [];
    const seen = new Set<string>();

    const selector =
      ".toc a, .toc .num, .toc h2, .titlepage h1, .subtitle, .version, h2, h3, h4, p, li, th, td, code, .note, a";

    for (const el of Array.from(document.querySelectorAll(selector))) {
      if (!el.textContent?.trim()) continue;

      const style = getComputedStyle(el);
      const bg = backdrop(el);

      // One sample per distinct appearance. A 500-section manual has thousands of elements and about two dozen looks.
      const key = `${el.tagName}.${el.className}|${style.color}|${bg}|${style.fontSize}|${style.fontWeight}`;
      if (seen.has(key)) continue;
      seen.add(key);

      out.push({
        what: `${el.tagName.toLowerCase()}${el.className ? "." + el.className.split(" ")[0] : ""}`,
        text: el.textContent.trim().slice(0, 30),
        fg: parse(style.color),
        bg: parse(bg),
        size: parseFloat(style.fontSize),
        bold: Number(style.fontWeight) >= 700,
      });
    }

    return out;
  });

  // A positive control in the same run. Without it an empty failure list is indistinguishable from a sweep that selected
  // nothing at all - which is the failure this project keeps finding in its own guards.
  expect(samples.length, "the sweep found no text, so it is checking nothing").toBeGreaterThan(10);

  const failures = samples
    .map((s) => {
      const ratio = contrast(s.fg, s.bg);
      const large = s.size >= 24 || (s.size >= 18.66 && s.bold);
      const needed = large ? 3 : 4.5;

      return ratio < needed
        ? `${ratio.toFixed(2)}:1 needs ${needed}:1 - ${s.what} at ${s.size}px: "${s.text}"`
        : "";
    })
    .filter(Boolean);

  expect(failures, `unreadable text in the manual:\n${failures.join("\n")}`).toEqual([]);
});

test("the manual prints as ink on paper, not as the dark theme", async ({ page }) => {
  // The PDF is this HTML printed, so the print rules are the PDF's layout. Without them a palette change makes every
  // printed page a sheet of dark navy - which still looks deliberate on screen, and only shows up as a wasted ream.
  await page.goto(MANUAL);
  await page.emulateMedia({ media: "print" });

  const printed = await page.evaluate(() => {
    const body = getComputedStyle(document.body);
    const toc = getComputedStyle(document.querySelector(".toc")!);

    return {
      background: body.backgroundColor,
      colour: body.color,
      // Single flow, so the contents rail becomes an ordinary contents section ahead of the chapters.
      display: body.display,
      tocPosition: toc.position,
      chapterBreak: getComputedStyle(document.querySelector(".chapter")!).breakBefore,
    };
  });

  expect(printed.background, "the manual would print with a dark background").toBe("rgb(255, 255, 255)");
  expect(printed.colour, "the manual would print with pale text").toBe("rgb(0, 0, 0)");
  expect(printed.display, "the two-column layout would survive into print").toBe("block");
  expect(printed.tocPosition, "the contents rail would stay pinned in the PDF").toBe("static");
  expect(printed.chapterBreak, "chapters would not start on a fresh page").toBe("page");
});
