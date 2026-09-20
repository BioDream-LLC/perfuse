import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Text has to be legible against what is actually behind it.
//
// The Settings tab shipped with light-theme classes left in a dark-only interface: white cards, near-black headings on a near-black
// panel, and every value under "How this server was started" invisible. The tab sweep passed throughout, because the text was
// present in the DOM - it just could not be read. Rendering and legibility are different properties and only one of them was tested.
//
// Grepping for class names is not enough either. While fixing the above I darkened a card and left its label dark, so the label
// became invisible - a new instance of the same bug, introduced by the fix, and no class-name rule would have caught it because both
// halves looked reasonable in isolation. This measures the rendered result instead.

const TABS = [
  "Dashboard", "Channels", "Messages", "Queue", "Alerts", "Metrics", "FHIR lab", "Documents",
  "Scripts", "Migrate", "Contracts", "Tables", "Fleet", "Flow map", "Playground", "Shadow", "Certificates",
  "Users", "Activity", "Settings",
];

// WCAG AA for body text is 4.5, and 3.0 for large text. 3.0 is used here as the failure line: the intent is to catch text that
// cannot be read at all, not to enforce a full accessibility audit, and a stricter bar on a dark theme produces a long list of
// deliberate muted-grey labels that are perfectly legible in practice.
const MIN_CONTRAST = 3.0;

// Every theme, not just the one that shipped.
//
// A light theme is where this test earns its keep. The interface has about fourteen hundred hard-coded colour utilities and they are
// rethemed by redefining the palette variables, which works beautifully for the ninety percent that mean "muted text on a dark card"
// and silently fails for anything that assumed near-black. Reading every tab in every theme is the only way to find those, and it
// found real ones.
for (const theme of ["midnight", "dark", "light"] as const) {
  test(`no text is rendered unreadable against its own background in the ${theme} theme`, async ({ page }) => {
  // Nineteen tabs, each of which fetches before it renders. The default thirty seconds was almost exactly the runtime, so the first
  // version of this test passed with three seconds to spare and then failed on a timeout the moment anything got slower - reporting
  // a timeout rather than the contrast violation it had been given. A test whose pass depends on the machine being fast is not
  // evidence of anything.
  test.setTimeout(180_000);

  await page.goto("/");

  // Set the way a person sets it, through the control, so this also proves the picker works. Falling back to the attribute would
  // test the CSS while leaving the switch untested, and the switch is the part with wiring in it.
  await page.getByLabel("Theme").selectOption(theme);
  await expect(page.locator("html")).toHaveAttribute("data-theme", theme);

  const failures: string[] = [];

  for (const tab of TABS) {
    await openTab(page, tab);
    // Panels fetch before they render content, so this waits rather than measuring an empty panel. Kept short because the check is
    // repeated nineteen times and the cost adds up; the network here is loopback.
    await page.waitForTimeout(700);

    const bad = await page.evaluate((min) => {
      // Colours are resolved by painting them and reading the pixel back.
      //
      // Two earlier attempts were wrong in instructive ways. The first matched only rgb() with a regular expression, but Tailwind 4
      // emits oklch(), so it returned null for every colour on the page, the caller skipped what it could not parse, and the check
      // passed while looking at a panel with white text on a white card. The second assigned to canvas fillStyle expecting the
      // browser to normalise to hex - but Chromium keeps the oklch representation, so it still parsed nothing.
      //
      // Painting one pixel and reading it back cannot have that problem. Whatever syntax CSS gains next, the compositor still has to
      // turn it into bytes, and those bytes are what a person sees.
      const canvas = document.createElement("canvas");
      canvas.width = 1;
      canvas.height = 1;
      const probe = canvas.getContext("2d", { willReadFrequently: true });

      /** parse resolves any CSS colour to sRGB bytes plus alpha. */
      function parse(c: string): [number, number, number, number] | null {
        if (!c || c === "none" || c === "transparent") return [0, 0, 0, 0];
        if (!probe) return null;

        probe.clearRect(0, 0, 1, 1);

        // A colour the browser refuses leaves fillStyle at this sentinel, which is how an unsupported syntax is detected rather
        // than silently measured as black.
        probe.fillStyle = "#123456";
        probe.fillStyle = c;

        probe.fillRect(0, 0, 1, 1);
        const d = probe.getImageData(0, 0, 1, 1).data;

        return [d[0], d[1], d[2], d[3] / 255];
      }

      /** luminance is the WCAG relative luminance of an opaque colour. */
      function luminance(r: number, g: number, b: number): number {
        const f = (v: number) => {
          const s = v / 255;

          return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
        };

        return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
      }

      /** over composites a colour onto a background, since text is often drawn on a translucent card. */
      function over(
        fg: [number, number, number, number],
        bg: [number, number, number],
      ): [number, number, number] {
        const a = fg[3];

        return [
          Math.round(fg[0] * a + bg[0] * (1 - a)),
          Math.round(fg[1] * a + bg[1] * (1 - a)),
          Math.round(fg[2] * a + bg[2] * (1 - a)),
        ];
      }

      /**
       * effectiveBackground walks up the tree compositing every translucent layer.
       *
       * Reading only the element's own background is wrong: almost everything here is transparent or a slate-900/60 card over a
       * slate-950 page, and treating transparent as white produces nonsense on a dark theme.
       */
      function effectiveBackground(el: Element): [number, number, number] {
        const layers: [number, number, number, number][] = [];
        let node: Element | null = el;

        while (node) {
          const c = parse(getComputedStyle(node).backgroundColor);
          if (c && c[3] > 0) {
            layers.push(c);
            if (c[3] >= 1) break;
          }
          node = node.parentElement;
        }

        // Base is the page background. Assumed near-black because this interface is dark-only; if it ever gains a light mode this
        // needs to read the documentElement instead.
        let base: [number, number, number] = [2, 6, 23];
        for (let i = layers.length - 1; i >= 0; i--) base = over(layers[i], base);

        return base;
      }

      const out: string[] = [];
      const seen = new Set<string>();

      document.querySelectorAll("main *").forEach((el) => {
        const e = el as HTMLElement;

        // Only elements with their own visible text. Containers inherit their children's text and would report duplicates.
        const own = Array.from(e.childNodes)
          .filter((n) => n.nodeType === Node.TEXT_NODE)
          .map((n) => (n.textContent ?? "").trim())
          .join(" ")
          .trim();
        if (!own) return;
        if (e.offsetParent === null && getComputedStyle(e).position !== "fixed") return;

        const cs = getComputedStyle(e);
        if (cs.visibility === "hidden" || cs.opacity === "0") return;

        // A code box paints its text on a layer behind a transparent textarea, so the textarea's own glyphs are invisible on
        // purpose and measure 1.00:1. This is the one legitimate case, and the exemption is written to be narrower than it
        // sounds rather than as a blanket skip:
        //
        //   - the element must be a textarea, so no ordinary invisible text qualifies;
        //   - its text must be fully transparent rather than merely faint;
        //   - and a coloured layer must exist in the same container. Transparent text with nothing behind it is a real
        //     failure and still reported.
        //
        // Nothing is lost by skipping it, because that layer's own spans are separate elements in this same sweep and are
        // measured on their own merits - which is how this test caught the comment colour being too dark to read a moment ago.
        if (e.tagName === "TEXTAREA" && cs.color.replace(/\s/g, "").endsWith(",0)")) {
          if (e.parentElement?.querySelector("[data-code-shadow]")) return;
        }

        const fg = parse(cs.color);
        if (!fg) return;

        // An element painted with a gradient cannot be measured this way, and pretending otherwise is worse than skipping it.
        // effectiveBackground walks up looking for a background-color, so it never sees a gradient and reports whatever is behind
        // the element instead. The primary button is white text on a sky gradient: legible in every theme, computed here as white
        // on near-black in one and white on white in another - a false pass and a false fail from the same blind spot.
        //
        // Skipped rather than guessed at. Sampling a gradient's midpoint would be a different kind of made-up answer, and these
        // are a handful of hand-checked buttons rather than the long tail this test exists to police.
        const ownBg = getComputedStyle(e).backgroundImage;
        if (ownBg && ownBg !== "none" && ownBg.includes("gradient")) return;

        const bg = effectiveBackground(e);
        const composited = over(fg, bg);

        const l1 = luminance(composited[0], composited[1], composited[2]);
        const l2 = luminance(bg[0], bg[1], bg[2]);
        const ratio = (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05);

        if (ratio < min) {
          const key = own.slice(0, 40) + cs.color;
          if (seen.has(key)) return;
          seen.add(key);
          out.push(
            `${ratio.toFixed(2)}:1  ${JSON.stringify(own.slice(0, 50))}  ` +
              `color=${cs.color} on rgb(${bg.join(",")})  [${e.className?.toString?.().slice(0, 60) ?? ""}]`,
          );
        }
      });

      return out;
    }, MIN_CONTRAST);

    for (const b of bad) failures.push(`${theme}/${tab}: ${b}`);
  }

  expect(
    failures,
    `text that cannot be read against its own background (contrast below ${MIN_CONTRAST}:1):\n  ${failures.join("\n  ")}`,
  ).toEqual([]);
});
}
