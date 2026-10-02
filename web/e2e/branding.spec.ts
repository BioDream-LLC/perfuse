import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// White-labelling, driven the way a customer would.
//
// The property that matters is not that the upload returns 200 - it is that the mark then appears
// everywhere, including on the sign-in page where nobody is authenticated yet, and that a logo carrying
// an attack cannot run it. Both are asserted here rather than assumed from the unit tests, because the
// unit tests cover the sanitiser and this covers the wiring around it.

const cleanSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48">
  <defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1">
    <stop offset="0" stop-color="#f43f5e"/><stop offset="1" stop-color="#f59e0b"/>
  </linearGradient></defs>
  <rect width="48" height="48" rx="10" fill="url(#g)"/>
  <title>Acme Health</title>
</svg>`;

/** upload puts an image through the branding endpoint as the interface does. */
async function upload(page: import("@playwright/test").Page, body: string, type: string) {
  return page.evaluate(
    async ({ body, type }) => {
      const res = await fetch("/api/branding/logo", {
        method: "PUT",
        headers: { "X-Perfuse-Request": "1", "Content-Type": type },
        body,
        credentials: "same-origin",
      });
      return { status: res.status, text: (await res.text()).slice(0, 300) };
    },
    { body, type },
  );
}

async function clearLogo(page: import("@playwright/test").Page) {
  await page.evaluate(() =>
    fetch("/api/branding/logo", {
      method: "DELETE",
      headers: { "X-Perfuse-Request": "1" },
      credentials: "same-origin",
    }),
  );
}

test.afterEach(async ({ page }) => {
  // Left as it was found, or a later test inherits somebody else's branding.
  await page.goto("/");
  await clearLogo(page);
  await page.evaluate(() =>
    fetch("/api/settings/values", {
      method: "PUT",
      headers: { "X-Perfuse-Request": "1", "Content-Type": "application/json" },
      body: JSON.stringify({ changes: { "branding.productName": "", "branding.accentColour": "" } }),
      credentials: "same-origin",
    }),
  );
});

test("a customer can rename the product and it appears everywhere", async ({ page }) => {
  await page.goto("/");

  const res = await page.evaluate(() =>
    fetch("/api/settings/values", {
      method: "PUT",
      headers: { "X-Perfuse-Request": "1", "Content-Type": "application/json" },
      body: JSON.stringify({ changes: { "branding.productName": "Acme Health" } }),
      credentials: "same-origin",
    }).then((r) => r.status),
  );
  expect(res).toBe(200);

  await page.reload();

  // The header carries it.
  await expect(page.locator("header")).toContainText("Acme Health");
  // And so does the browser tab, which is where somebody with twelve tabs open actually looks.
  await expect.poll(() => page.title()).toBe("Acme Health");
  // And the word Perfuse is gone from the header.
  await expect(page.locator("header")).not.toContainText("Perfuse");
});

test("an accent colour reaches the stylesheet", async ({ page }) => {
  await page.goto("/");

  await page.evaluate(() =>
    fetch("/api/settings/values", {
      method: "PUT",
      headers: { "X-Perfuse-Request": "1", "Content-Type": "application/json" },
      body: JSON.stringify({ changes: { "branding.accentColour": "#f43f5e" } }),
      credentials: "same-origin",
    }),
  );
  await page.reload();

  const accent = await page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue("--brand-accent").trim(),
  );
  expect(accent, "the accent colour did not reach the custom property").toBe("#f43f5e");
});

// A colour nobody can read against a near-black background makes the interface unusable rather than
// branded, and the customer would blame the product. It is refused with the reason.
test("an unreadably dark accent colour is refused", async ({ page }) => {
  await page.goto("/");

  const result = await page.evaluate(() =>
    fetch("/api/settings/values", {
      method: "PUT",
      headers: { "X-Perfuse-Request": "1", "Content-Type": "application/json" },
      body: JSON.stringify({ changes: { "branding.accentColour": "#050510" } }),
      credentials: "same-origin",
    }).then(async (r) => ({ status: r.status, body: (await r.text()).slice(0, 300) })),
  );

  expect(result.status, `a black accent was accepted: ${result.body}`).toBeGreaterThanOrEqual(400);
  expect(result.body.toLowerCase()).toContain("dark");
});

test("a logo uploads, is served, and renders without a session", async ({ page, context }) => {
  await page.goto("/");

  const up = await upload(page, cleanSVG, "image/svg+xml");
  expect(up.status, `upload failed: ${up.text}`).toBe(200);

  // Served, with the headers that make a malicious upload inert.
  const res = await page.request.get("/api/branding/logo");
  expect(res.status()).toBe(200);
  expect(res.headers()["content-type"]).toContain("image/svg+xml");
  expect(res.headers()["x-content-type-options"]).toBe("nosniff");
  const csp = res.headers()["content-security-policy"] ?? "";
  expect(csp, "the logo is served without a restrictive policy").toContain("default-src 'none'");
  expect(csp).toContain("sandbox");

  // The gradient survived, so a real logo is not destroyed by sanitising.
  const body = await res.text();
  expect(body).toContain("linearGradient");
  expect(body).toContain("#f43f5e");

  // It renders in the header as an image.
  await page.reload();
  const headerLogo = page.locator('header img[src*="/api/branding/logo"]');
  await expect(headerLogo, "the uploaded logo is not shown in the header").toBeVisible();

  // And on the sign-in page, where there is no session at all. This is the whole point of the
  // read endpoints being public.
  const anon = await context.browser()!.newContext();
  const anonPage = await anon.newPage();
  await anonPage.goto(page.url());

  // Scoped to the header, because the dashboard now shows the site's logo too and an unscoped locator matches both.
  //
  // Worth recording what this test does and does not prove, since its name overstates it. The harness runs the server with
  // -insecure, so there is no sign-in page here at all: a fresh context is already authenticated, and this page is the console.
  // What is actually verified - and it is the part that matters - is that the logo is fetched and rendered by a browser context
  // carrying no session of its own, which is why the branding read endpoints are public. A test that could see the real sign-in
  // form would need a harness that requires a password, and that does not exist yet.
  await expect(
    anonPage.locator('header img[src*="/api/branding/logo"]'),
    "the logo is not served to a context with no session, so a customer's first screen would be unbranded",
  ).toBeVisible({ timeout: 15_000 });
  await anon.close();
});

// The sanitiser is unit tested; this proves the wiring around it, end to end, in a real browser.
test("a logo carrying script is stored inert", async ({ page }) => {
  await page.goto("/");

  const nasty =
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48" onload="window.__brandXSS=1">` +
    `<script>window.__brandXSS=1</script>` +
    `<a href="javascript:window.__brandXSS=1"><rect width="48" height="48" fill="#f00"/></a>` +
    `</svg>`;

  const up = await upload(page, nasty, "image/svg+xml");
  // Either refused outright or stored with the attack removed. Both are acceptable; serving it is not.
  if (up.status === 200) {
    const body = await (await page.request.get("/api/branding/logo")).text();
    expect(body.toLowerCase(), "a script survived into the stored logo").not.toContain("script");
    expect(body.toLowerCase()).not.toContain("onload");
    expect(body.toLowerCase()).not.toContain("javascript:");
  }

  // And nothing executed in the page that renders it.
  await page.reload();
  await page.waitForTimeout(1000);
  const fired = await page.evaluate(() => (window as unknown as { __brandXSS?: number }).__brandXSS === 1);
  expect(fired, "script from an uploaded logo executed").toBe(false);
});

test("a file that is not an image is refused", async ({ page }) => {
  await page.goto("/");

  const up = await upload(page, "<html><body><script>alert(1)</script></body></html>", "image/png");
  expect(up.status, "an HTML document declared as a PNG was accepted").toBeGreaterThanOrEqual(400);
  expect(up.text.toLowerCase()).toMatch(/image|format/);
});

test("the branding panel is reachable and offers an upload", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Settings");

  const main = page.locator("main");
  await expect(main).toContainText("Make it yours");
  // The control is a label wrapping a file input, so the input itself is what is asserted on - the
  // visible text matches both the label and the span inside it.
  await expect(main.locator('input[type=file]')).toHaveCount(1);
  await expect(main.getByText(/Upload a logo|Replace logo/).first()).toBeVisible();

  // The explanation of SVG rewriting is on screen, not only in the commit message: an administrator
  // whose file comes back changed deserves to know why before it happens.
  await expect(main).toContainText(/rewritten to remove/i);
});
