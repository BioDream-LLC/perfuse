import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// The playground runs the engine core as WebAssembly in the browser, with no server involved.
//
// It was broken in every shipped build - the module was not embedded, so the loader script came back as HTML and the browser refused
// to execute it. The tab still rendered, which is why a sweep that only checked for rendering passed. The lesson is that "the panel
// appeared" and "the feature works" are different claims, and only the second one is worth making.

test("the playground loads the module and parses a message in the browser", async ({ page }) => {
  const problems: string[] = [];
  page.on("console", (m) => {
    if (m.type() === "error") problems.push(`console: ${m.text()}`);
  });
  page.on("pageerror", (e) => problems.push(`uncaught: ${e.message}`));

  // Record how the module and its loader are actually served. A 200 is not sufficient: the fallback returned 200 with HTML, which
  // is precisely what made this hard to see.
  const served: Record<string, string> = {};
  page.on("response", (r) => {
    const p = new URL(r.url()).pathname;
    if (p.startsWith("/wasm/")) served[p] = `${r.status()} ${r.headers()["content-type"] ?? "?"}`;
  });

  await page.goto("/");
  await openTab(page, "Playground");

  // These controls only exist once the module has instantiated, so their appearance is the signal that it loaded. The module is
  // 20 MB, so this waits generously rather than assuming.
  //
  // Not "Read this message": that button belongs to the separate v3 field picker above and is correctly disabled until its box has
  // something in it. My first version of this test clicked it and timed out, which was the test being wrong rather than the
  // playground.
  const lookInside = page.getByRole("button", { name: "Look inside a message" });
  await expect(lookInside).toBeVisible({ timeout: 40_000 });

  // The failure panel must be gone. Checked explicitly, because the tab renders whether or not the engine works and that is exactly
  // how this stayed broken.
  await expect(page.getByText("The engine could not be loaded.")).toHaveCount(0);

  await lookInside.click();

  // Something actually parsed, in the browser, with no server involved. Asserting on output rather than on the absence of an error,
  // because a playground that silently does nothing also produces no error.
  await expect(page.locator("main")).toContainText(/MSH|PID|segment/i, { timeout: 30_000 });

  expect(problems, `the playground reported problems:\n${problems.join("\n")}`).toEqual([]);

  // And the assets were served as themselves, not as the application.
  const loader = served["/wasm/wasm_exec.js"] ?? "never requested";
  expect(loader, `the loader script was served as ${loader}`).toContain("javascript");
});
