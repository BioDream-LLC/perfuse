import { defineConfig } from "@playwright/test";

// A separate config: this suite talks to containers, so it must not be picked up by `make e2e`, which has to run without Docker.
export default defineConfig({
  testDir: "./e2e-saml",
  timeout: 90_000,
  reporter: "line",
  use: { ignoreHTTPSErrors: true },
});
