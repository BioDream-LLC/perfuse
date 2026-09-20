import { test, expect } from "@playwright/test";
import { readFileSync } from "node:fs";

import { openTab } from "./nav";

// Configuring SAML from a provider's metadata document, in the browser.
//
// Why this is worth a browser test rather than only an API one. The standing rule here is that everything can be done from the web interface,
// and the thing this replaces was not a missing endpoint - it was an administrator opening an XML document, finding a certificate inside it,
// stripping the line breaks out of the base64 and pasting the result into a form. If that work has really been removed, it has been removed
// from a screen somebody uses.
//
// The documents are the ones two real providers published, committed under internal/saml/testdata. A hand-written metadata document would only
// prove the form can read a hand-written metadata document, and the providers differ in exactly the way that breaks a parser written against
// one of them: Keycloak prefixes its elements with md:, Entra does not.

const keycloakMetadata = readFileSync("../internal/saml/testdata/keycloak-metadata.xml", "utf8");
const entraMetadata = readFileSync("../internal/saml/testdata/entra-metadata.xml", "utf8");

/** openSAMLSettings gets to the SAML tab of the sign-on editor.
 *
 * The same route signon.spec.ts takes, including waiting on the field ids rather than on headings. The first version of this clicked a button
 * matched by name and found the disabled "Save sign-on settings" instead, then waited ninety seconds for it to become clickable.
 */
async function openSAMLSettings(page: import("@playwright/test").Page) {
  await page.goto("/");
  await openTab(page, "Settings");

  await expect(page.locator("#signon-oidc-issuer")).toBeVisible({ timeout: 20_000 });

  await page.getByRole("tab", { name: /SAML/ }).click();
  await expect(page.locator("#signon-saml-entity_id")).toBeVisible({ timeout: 10_000 });
}

test("a provider's metadata fills in the SAML settings", async ({ page }) => {
  await openSAMLSettings(page);

  // Offered before the fields, because reading the document is the first thing to do rather than a recovery from filling them in wrongly.
  await page.getByRole("button", { name: "Read metadata" }).click();

  await page.getByLabel(/paste the document/i).fill(keycloakMetadata);
  await page.getByRole("button", { name: "Read it" }).click();

  // What is about to be trusted, described rather than shown as base64. The fingerprint is the only way to check the document against what the
  // provider's own console says.
  await expect(page.getByText(/Expires|Expired/)).toBeVisible({ timeout: 15_000 });

  await page.getByRole("button", { name: "Use these settings" }).click();

  // The certificate lands in the form as PEM, which is the transcription this feature exists to remove.
  const cert = page.getByLabel(/certificate/i).first();
  await expect(cert).toHaveValue(/BEGIN CERTIFICATE/, { timeout: 10_000 });

  // And the sign-on URL, which came out of the same document rather than being typed.
  await expect(page.getByLabel(/sign-?on URL/i).first()).toHaveValue(/protocol\/saml/);
});

test("an Entra document works as well as a Keycloak one", async ({ page }) => {
  await openSAMLSettings(page);

  // The reason for testing both. Keycloak writes md:EntityDescriptor and Entra writes EntityDescriptor with no prefix, and a form that worked
  // for only one of them would look finished.
  await page.getByRole("button", { name: "Read metadata" }).click();
  await page.getByLabel(/paste the document/i).fill(entraMetadata);
  await page.getByRole("button", { name: "Read it" }).click();

  // Scoped to the summary rather than the page. The pasted document is now syntax highlighted, so the issuer appears in several spans inside the
  // editor as well as in the report - and an unscoped match is a strict-mode violation rather than a useful assertion.
  await expect(page.getByRole("definition").filter({ hasText: "sts.windows.net" }).first()).toBeVisible({ timeout: 15_000 });

  await page.getByRole("button", { name: "Use these settings" }).click();

  await expect(page.getByLabel(/sign-?on URL/i).first()).toHaveValue(/login\.microsoftonline\.com/);
});

test("a service provider's document is refused with an explanation", async ({ page }) => {
  await openSAMLSettings(page);

  await page.getByRole("button", { name: "Read metadata" }).click();

  // The usual mistake: pasting the descriptor of the thing being configured rather than the thing being trusted. The two look very similar.
  await page.getByLabel(/paste the document/i).fill(
    '<EntityDescriptor entityID="https://example.test/sp"><SPSSODescriptor/></EntityDescriptor>',
  );
  await page.getByRole("button", { name: "Read it" }).click();

  await expect(page.getByText(/not an identity provider/i)).toBeVisible({ timeout: 15_000 });

  // And nothing is applied, so a refusal cannot half-configure anything.
  await expect(page.getByRole("button", { name: "Use these settings" })).toHaveCount(0);
});

test("a metadata URL this server may not fetch is refused by name", async ({ page }) => {
  await openSAMLSettings(page);

  await page.getByRole("button", { name: "Read metadata" }).click();

  // The cloud instance metadata address, which on a hosted machine serves that machine's own credentials to anything that can make a request
  // from it. An administrator can already reach a great deal, but making the server fetch this on their behalf is not something to offer.
  await page.getByLabel(/Metadata URL/i).fill("https://169.254.169.254/latest/meta-data/");
  await page.getByRole("button", { name: "Read it" }).click();

  await expect(page.getByText(/will not fetch|169\.254/)).toBeVisible({ timeout: 15_000 });
});
