import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Kill the Clipboard, both directions through the interface: a visit record shared as a SMART Health Link with a passcode, then that
// same link read back the way a front desk would - which exercises the public manifest endpoint, the decryption and the passcode.

test("a visit record is shared as a link, and the link reads back to the patient", async ({ page }) => {
  const faults: string[] = [];
  page.on("pageerror", (e) => faults.push(e.message));
  await page.goto("/");
  await openTab(page, "Health links");

  await page.getByRole("button", { name: "Share", exact: true }).click();
  await page.getByLabel("Passcode").fill("2468");
  await page.getByRole("button", { name: "Create the link" }).click();
  const link = (await page.getByTestId("shl-link").innerText()).trim();
  expect(link).toMatch(/^shlink:\//);
  await expect(page.getByTestId("shl-qr").locator("svg")).toBeVisible();
  await expect(page.getByTestId("shl-hosted")).toContainText("Visit summary · passcode, 10 tries left");

  await page.getByRole("button", { name: "Receive", exact: true }).click();
  await page.getByLabel("SMART Health Link").fill(link);
  await page.getByLabel("Passcode").fill("0000");
  await page.getByRole("button", { name: "Read the records" }).click();
  await expect(page.getByRole("alert").first()).toContainText("9 attempt(s) remain");

  await page.getByLabel("Passcode").fill("2468");
  await page.getByRole("button", { name: "Read the records" }).click();
  const received = page.getByTestId("shl-received");
  await expect(received).toContainText("Visit summary: 1 file");
  await expect(received).toContainText("Jane Doe, born 1980-01-01");
  await expect(received).toContainText("1 Encounter · 1 Patient");

  // And the sharing side saw it fetched.
  await page.getByRole("button", { name: "Share", exact: true }).click();
  await expect(page.getByTestId("shl-hosted")).toContainText("1 time, last by Perfuse (admin)");
  await page.getByTestId("shl-hosted").getByRole("button", { name: "Revoke" }).first().click();
  await expect(page.getByTestId("shl-hosted").locator("tr").nth(1)).toContainText("revoked");
  expect(faults, faults.join("\n")).toEqual([]);
});

test("something that is not a health card is refused with the reason", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Health links");
  await page.getByRole("button", { name: "Verify a card" }).click();
  await page.getByLabel("SMART Health Card").fill("hello");
  await page.getByRole("button", { name: "Verify", exact: true }).click();
  await expect(page.getByRole("alert").first()).toContainText("not a SMART Health Card");
});
