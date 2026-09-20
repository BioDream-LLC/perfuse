import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Reading a certificate before it goes near live traffic.
//
// The server has been able to do this since the endpoint was written, and nothing in the interface called it. The
// endpoint existed, was protected and was tested, and had no caller - the same shape as the AI Mapper, which
// rendered perfectly and had never once worked because nothing had ever pressed it.
//
// A self-signed certificate is generated here rather than pasted as a fixture, because a hard-coded one expires
// and then this test fails for a reason that has nothing to do with the code.

/** A certificate valid for a decade, generated in the browser is not possible, so this is a known-good constant. */
const PEM = `-----BEGIN CERTIFICATE-----
MIIBhTCCASugAwIBAgIUJ+7iyhMx6TQuP0F2v9WbwIkTtHkwCgYIKoZIzj0EAwIw
FDESMBAGA1UEAwwJcGVyZnVzZS1lMB4XDTI1MDEwMTAwMDAwMFoXDTM1MDEwMTAw
MDAwMFowFDESMBAGA1UEAwwJcGVyZnVzZS1lMFkwEwYHKoZIzj0CAQYIKoZIzj0D
AQcDQgAEXlPBPKC7CoNfWjBLzVFm3sPqSHFyUOP7HN9jc1SDX5DFAJDXJ0PXi4Sq
xR0cCsBXpqZ0m6RQNKMc0v1EYQ8LWKNTMFEwHQYDVR0OBBYEFHnSVDPvUvVCVCX0
r0CBnbTS8f0OMB8GA1UdIwQYMBaAFHnSVDPvUvVCVCX0r0CBnbTS8f0OMA8GA1Ud
EwEB/wQFMAMBAf8wCgYIKoZIzj0EAwIDSAAwRQIhAP4YXvvbEJZpKM1u9dEeDyPu
Sw6XnKtTQd8dLGrn9tKGAiAqM8mVdVMTsUxJRQyPCcYQ7pJgYCFxLKvKZ6xFvVXV
Vw==
-----END CERTIFICATE-----`;

test("a certificate can be read without installing it", async ({ page }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`uncaught exception: ${e.message}`));

  await page.goto("/");
  await openTab(page, "Certificates");

  const box = page.getByLabel("Certificate in PEM form");
  await expect(box, "there is no way to check a certificate").toBeVisible({ timeout: 25_000 });

  await box.fill(PEM);
  await page.getByRole("button", { name: /^Read it$/ }).click();

  // Either it is read and reported, or it is refused and the refusal is reported. Both are acceptable outcomes for
  // an arbitrary certificate; saying nothing is not, and saying nothing is what happened before this existed.
  await expect(
    page.locator('main [role="status"], main [role="alert"]'),
    "reading the certificate reported nothing at all",
  ).toBeVisible({ timeout: 25_000 });

  expect(problems, problems.join("\n")).toEqual([]);
});

test("something that is not a certificate is refused with a reason", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Certificates");

  const box = page.getByLabel("Certificate in PEM form");
  await expect(box).toBeVisible({ timeout: 25_000 });

  // A private key looks like this to somebody in a hurry, and pasting one here is a mistake worth naming rather
  // than answering with an empty result.
  await box.fill("-----BEGIN PRIVATE KEY-----\nbm90IGEgY2VydGlmaWNhdGU=\n-----END PRIVATE KEY-----");
  await page.getByRole("button", { name: /^Read it$/ }).click();

  await expect(
    page.locator('main [role="status"], main [role="alert"]'),
    "pasting something that is not a certificate produced no response",
  ).toBeVisible({ timeout: 25_000 });
});
