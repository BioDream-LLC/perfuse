import { defineConfig, devices } from "@playwright/test";

// End-to-end tests that drive the real GUI against a real perfuse server.
//
// These exist because nothing else in this repository verifies that the web interface works. The vitest suite covers helper
// functions - wireToDraft, settingsControls, the command palette - and there is no component-testing library, so a button
// wired to nothing, a form posting the wrong shape, or a tab rendering blank because an API contract drifted would all pass
// every existing check. That is the class of defect these catch.
//
// They test the *embedded* build rather than the vite dev server, because the embedded build is what ships. A dev-server test
// can pass while the binary serves a stale internal/web/dist, which is precisely the drift worth catching.
export default defineConfig({
  testDir: "./e2e",

  // Deliberately not in `make check`. These start a real server, download nothing but do drive a real browser, and take
  // seconds rather than milliseconds. `make check` stays fast enough to run on every commit; this is a deliberate QA pass.
  // See `make e2e`.
  fullyParallel: false,
  workers: 1,

  // Zero retries. A test that passes on the second attempt is a test that found a real race and hid it.
  retries: 0,

  // Fail the run if a test is left marked .only, which otherwise silently reduces the suite to one test.
  forbidOnly: true,

  // Ninety seconds, raised from thirty.
  //
  // The suite now drives the product rather than checking that it renders: building a channel through the
  // form, walking nine templates, visiting all twenty-one sections twice for accessibility and network
  // sweeps. Several of those genuinely take half a minute, and at thirty they failed intermittently as
  // timeouts - which read like defects they had found and cost more to investigate than they were worth.
  //
  // The per-assertion budget stays at ten seconds, which is what actually catches a stuck interface. A
  // generous overall limit does not hide a hung test; it just reports it later.
  timeout: 90_000,
  expect: { timeout: 10_000 },

  reporter: [["list"], ["html", { outputFolder: "e2e-report", open: "never" }]],

  globalSetup: "./e2e/setup.ts",
  globalTeardown: "./e2e/teardown.ts",

  use: {
    // Set by the global setup, which picks a free port.
    baseURL: process.env.PERFUSE_E2E_URL,

    // Signed-in state, established once by the setup rather than by logging in in every test.
    storageState: "./e2e/.auth/admin.json",

    // Kept only for failures. A trace for every passing test is hundreds of megabytes nobody reads.
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "off",

    // A viewport wide enough for the real layout. Nineteen tabs at 800px wide is a different interface, and testing a
    // layout nobody uses proves nothing about the one they do.
    viewport: { width: 1440, height: 900 },
  },

  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
});
