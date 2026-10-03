import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// A claims attachment with an HL7 DSDR electronic signature, as CMS-0053 adopts it: signed on the way into a 275 with the
// server's key, then checked from the 275 alone on the payer's side. What is asserted is what the server found on reading the
// signature back - the participant, the purpose, that it holds - and that it states how far short of XAdES-X-L it stops.
test("a C-CDA is signed into a 275, and the signature is checked when the 275 is read", async ({ page }) => {
  const faults: string[] = [];
  page.on("pageerror", (e) => faults.push(e.message));
  await page.goto("/");
  await openTab(page, "Claims & auth");
  await page.getByRole("button", { name: "Build a 275" }).click();

  await page.getByLabel("Sign the C-CDA before it goes in").check();
  await page.getByLabel("Signature purpose").selectOption("8.2.1.1");
  await page.getByRole("button", { name: "Build 275" }).click();

  const signed = page.getByTestId("attachment-signed");
  await expect(signed).toContainText("Signed as legalAuthenticator at XAdES-");
  // The fixture's key is self-signed and there is no time-stamping authority, and the page says what that costs.
  await expect(signed).toContainText("short of the XAdES-X-L");
  await expect(signed).toContainText("no time-stamping authority");
  const report = page.getByTestId("attachment-signatures");
  await expect(report).toContainText("Signature holds");
  await expect(report).toContainText("8.2.1.1 - Author's signature");
  await expect(report).toContainText("207X00000X - Orthopaedic Surgery");

  const x12 = (await page.getByTestId("x12-output").innerText()).replace(/\n/g, "");
  await page.getByRole("button", { name: "Read a 275" }).click();
  await page.getByLabel("X12 275 to read").fill(x12);
  await page.getByRole("button", { name: "Read 275" }).click();
  const view = page.getByTestId("attachment-view");
  await expect(view).toContainText("Signature holds");
  await expect(view).toContainText("Digitally signed by Authorized Signer");
  expect(faults).toEqual([]);
});
