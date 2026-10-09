import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Users → SMART apps: the built-in SMART authorization server's apps and people, edited in the console. A confidential app
// is given a secret shown once; a person is added with a password, which is never shown back.
test("SMART apps and the people who authorize them are added and removed", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Users");
  const clients = page.getByTestId("smart-clients");
  await expect(clients).toContainText("member-app");

  await page.getByLabel("App id").fill("portal");
  await page.getByLabel("App name").fill("Clinician portal");
  await page.getByLabel("Kind").selectOption("confidential-symmetric");
  await page.getByLabel("Scopes", { exact: true }).fill("openid fhirUser user/*.rs");
  await page.getByLabel("Redirect URIs").fill("https://portal.example.org/cb");
  await page.getByRole("button", { name: "Save app" }).click();
  await expect(page.getByTestId("smart-secret")).toHaveText(/^[A-Za-z0-9_-]{40,}$/);
  await expect(clients).toContainText("portal");
  await page.getByRole("button", { name: "I have saved it" }).last().click();

  // A mistake is refused with the reason.
  await page.getByLabel("App id").fill("broken");
  await page.getByLabel("Kind").selectOption("public");
  await page.getByLabel("Scopes", { exact: true }).fill("openid");
  await page.getByRole("button", { name: "Save app" }).click();
  await expect(page.getByText(/redirect_uris/)).toBeVisible();

  const users = page.getByTestId("smart-users");
  await page.getByLabel("Username").last().fill("drbrown");
  await page.getByLabel("Full name").fill("Dr Brown");
  await page.getByLabel("FHIR user").fill("Practitioner/example-clinician");
  await page.getByLabel("New password").fill("correct horse battery");
  await page.getByRole("button", { name: "Save person" }).click();
  await expect(users).toContainText("drbrown");
  await expect(users).toContainText("Practitioner/example-clinician");
  await expect(users).toContainText("password");

  page.once("dialog", (d) => void d.accept());
  await page.getByRole("button", { name: "Remove portal" }).click();
  await expect(clients).not.toContainText("portal");
});
