import { expect, test } from "@playwright/test";

/**
 * Signs in to Perfuse through a real Keycloak, with a real assertion.
 *
 * This is the verification that everything before it was not. The SAML package had a thousand lines of tests, all of them checking
 * that Perfuse agrees with Perfuse: every response was built by this codebase, signed by this codebase, and read back by this
 * codebase. Agreement with itself is worth something and it is not evidence about a product.
 *
 * Here the assertion is produced by Keycloak — its canonicalisation, its namespace prefixes, its attribute name format, its idea of
 * where a signature belongs. If the implementation's reading of the specification is wrong anywhere that matters, this is where it
 * shows.
 *
 * Run separately from the main suite, against the servers scripts/keycloak-saml-setup.sh and scripts/saml-verify-serve.sh start.
 * It is not part of `make e2e` because it needs a container runtime, and a suite that cannot run without Docker is a suite people
 * stop running.
 */

const PERFUSE = process.env.SAML_VERIFY_URL ?? "http://127.0.0.1:8099";

test("a real Keycloak assertion signs somebody in with the role their group grants", async ({ page }) => {
  await page.goto(PERFUSE + "/");

  // The button exists because the server advertises SAML, which it only does when a certificate parsed at startup.
  const signIn = page.getByRole("link", { name: /Sign in with Keycloak/ });
  await expect(signIn).toBeVisible({ timeout: 20_000 });

  await signIn.click();

  // Keycloak's own login page. Asserted on before typing, so a failure here is distinguishable from a failure to authenticate:
  // arriving somewhere else means the redirect or the AuthnRequest was wrong, which is a different fault entirely.
  await expect(page.locator("#username")).toBeVisible({ timeout: 30_000 });

  await page.locator("#username").fill("grace");
  await page.locator("#password").fill("perfuse-e2e");
  await page.locator("#kc-login").click();

  // Back in Perfuse, signed in. The nav only renders for an authenticated session, so its presence is the assertion.
  await expect(page.getByRole("button", { name: /Sign out/ })).toBeVisible({ timeout: 30_000 });

  // Who Perfuse thinks this is, read from the API rather than the page, because the page could plausibly render a stale shell.
  const me = await page.evaluate(async (base) => {
    const res = await fetch(base + "/api/me", { credentials: "include" });

    return res.json();
  }, PERFUSE);

  // The username comes from the email local part, not the NameID.
  expect(me.username, `Perfuse identified the wrong person: ${JSON.stringify(me)}`).toBe("grace");

  // The role comes from the group Keycloak sent, which is the whole point: an attribute this codebase did not write, read through
  // the configured attribute name, matched against a mapping an administrator typed.
  expect(me.role, `the group mapping did not take effect: ${JSON.stringify(me)}`).toBe("editor");
});

test("a second sign-in works, so nothing about the first was single-use by accident", async ({ page }) => {
  // Worth checking separately. The request store consumes an outstanding id and the replay cache remembers assertion ids, and a
  // mistake in either would let exactly one person sign in per server lifetime - which passes a single-login test perfectly.
  await page.goto(PERFUSE + "/");
  await page.getByRole("link", { name: /Sign in with Keycloak/ }).click();

  // Keycloak remembers the earlier session in this browser context, so it may not ask again.
  const username = page.locator("#username");
  if (await username.isVisible({ timeout: 10_000 }).catch(() => false)) {
    await username.fill("grace");
    await page.locator("#password").fill("perfuse-e2e");
    await page.locator("#kc-login").click();
  }

  await expect(page.getByRole("button", { name: /Sign out/ })).toBeVisible({ timeout: 30_000 });
});
