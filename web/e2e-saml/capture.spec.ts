import { test } from "@playwright/test";
import { writeFileSync } from "node:fs";

/**
 * Captures the SAMLResponse a real Keycloak produces, so it can be examined offline.
 *
 * Kept because the document is the evidence. A digest mismatch says the canonical form differs and says nothing about where, and
 * guessing at that from a specification is how the first implementation came to be wrong.
 */

const PERFUSE = process.env.SAML_VERIFY_URL ?? "http://127.0.0.1:8099";

test("capture a real assertion", async ({ page }) => {
  let captured = "";

  await page.route("**/auth/saml/acs", async (route) => {
    const body = route.request().postData() ?? "";
    const params = new URLSearchParams(body);

    captured = params.get("SAMLResponse") ?? "";

    // Let it through, so the server log records its verdict on the same document that was saved.
    await route.continue();
  });

  await page.goto(PERFUSE + "/");
  await page.getByRole("link", { name: /Sign in with Keycloak/ }).click();

  const username = page.locator("#username");
  if (await username.isVisible({ timeout: 20_000 }).catch(() => false)) {
    await username.fill("grace");
    await page.locator("#password").fill("perfuse-e2e");
    await page.locator("#kc-login").click();
  }

  await page.waitForTimeout(3000);

  if (captured === "") {
    throw new Error("no SAMLResponse was posted");
  }

  writeFileSync("/tmp/keycloak-response.b64", captured);
  writeFileSync("/tmp/keycloak-response.xml", Buffer.from(captured, "base64").toString("utf8"));

  console.log("CAPTURED", captured.length, "base64 characters");
});
