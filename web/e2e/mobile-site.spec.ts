import { test, expect, devices } from '@playwright/test';

// The download buttons were invisible on phones for two separate reasons, and both were the kind
// of fault that is invisible to whoever built the page. The images 404'd, which on a desktop is
// masked by alt text that reads almost like a button; and they are SVGs with a fixed width
// attribute - 290px for macOS - on a page that had no width-based media query, so on a narrow
// phone the button was wider than the space it had and an image with an intrinsic width does not
// shrink on its own.
//
// These tests drive a real phone viewport and assert what a visitor can actually see: that the
// images loaded, and that nothing sticks out past the edge of the screen.

const BASE = process.env.SITE_BASE || 'http://127.0.0.1:8971';

// The narrowest phone still in wide use. If it works here it works on everything bigger.
const narrow = { width: 320, height: 568 };

const pages = ['/', '/overview/', '/reference/', '/mirth-connect-alternative/'];

test.describe('the site on a phone', () => {
  test('every download button image actually loads', async ({ page }) => {
    await page.setViewportSize(narrow);
    await page.goto(`${BASE}/`);

    const imgs = page.locator('.buttons img');
    const count = await imgs.count();
    expect(count, 'there should be three download buttons').toBe(3);

    for (let i = 0; i < count; i++) {
      const img = imgs.nth(i);
      const alt = await img.getAttribute('alt');

      // naturalWidth is 0 for an image the browser could not fetch or parse. This is the check
      // that a 404 cannot pass, unlike asserting the element exists.
      const loaded = await img.evaluate(
        (el) => (el as HTMLImageElement).complete && (el as HTMLImageElement).naturalWidth > 0,
      );
      expect(loaded, `the image for "${alt}" did not load`).toBe(true);

      await expect(img, `the button for "${alt}" is not visible`).toBeVisible();
    }
  });

  test('the download buttons fit inside the screen', async ({ page }) => {
    await page.setViewportSize(narrow);
    await page.goto(`${BASE}/`);

    const imgs = page.locator('.buttons img');
    for (let i = 0; i < (await imgs.count()); i++) {
      const box = await imgs.nth(i).boundingBox();
      const alt = await imgs.nth(i).getAttribute('alt');
      expect(box, `no box for "${alt}"`).not.toBeNull();
      expect(
        box!.x + box!.width,
        `the button for "${alt}" extends past the right edge of a ${narrow.width}px screen`,
      ).toBeLessThanOrEqual(narrow.width + 1);
      expect(box!.x, `the button for "${alt}" starts off the left edge`).toBeGreaterThanOrEqual(-1);
    }
  });

  for (const path of pages) {
    test(`${path} does not scroll sideways on a phone`, async ({ page }) => {
      await page.setViewportSize(narrow);
      await page.goto(`${BASE}${path}`);

      // A page wider than the viewport is the signature of a fixed-width element that was never
      // tested narrow. A table allowed to scroll inside its own box does not widen the document.
      const overflow = await page.evaluate(() => ({
        doc: document.documentElement.scrollWidth,
        win: window.innerWidth,
      }));

      expect(
        overflow.doc,
        `the page is ${overflow.doc}px wide in a ${overflow.win}px window, so something overflows`,
      ).toBeLessThanOrEqual(overflow.win + 1);
    });
  }

  test('the front page is usable on a real phone profile', async ({ browser }) => {
    // Not just a narrow window: a device profile brings the right pixel ratio and user agent,
    // which is what decides whether a mobile browser applies the media query at all.
    const ctx = await browser.newContext({ ...devices['iPhone SE'] });
    const page = await ctx.newPage();
    await page.goto(`${BASE}/`);

    await expect(page.locator('.buttons img').first()).toBeVisible();

    const wide = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth + 1,
    );
    expect(wide, 'the front page scrolls sideways on an iPhone SE').toBe(false);

    await ctx.close();
  });
});
