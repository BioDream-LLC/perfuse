import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Creating a shared mapping table from the interface.
//
// These were readable and not writable: the section reported which channels each table affected and, on a fresh
// installation, printed a YAML example for you to write on the server by hand. Found by the control sweep, which
// reported the section had nothing operable in it at all.

test("a shared mapping table can be created and then appears in the listing", async ({ page }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`uncaught exception: ${e.message}`));

  await page.goto("/");
  await openTab(page, "Tables");

  await page.getByRole("button", { name: /^New shared table$/ }).click();

  const name = `sex-codes-${Date.now()}`;
  await page.getByLabel("Name", { exact: true }).fill(name);
  await page
    .getByLabel("What this table is for")
    .fill("How this hospital's sex codes map to what the receiver expects");
  await page.getByLabel("Where these came from").fill("the lab's specification, version 4");

  await page.getByLabel("Value 1 to map from").fill("1");
  await page.getByLabel("Value 1 to map to").fill("M");
  await page.getByLabel("Why value 1 maps that way").fill("agreed with the lab in 2019");

  await page.getByLabel("Value 2 to map from").fill("2");
  await page.getByLabel("Value 2 to map to").fill("F");

  await page.getByRole("button", { name: /^Save table$/ }).click();

  // The outcome that can only follow saving. Not the surrounding explanation, which was already on screen.
  await expect(
    page.locator('main [role="status"]'),
    "saving the table reported nothing, so there is no way to know whether it worked",
  ).toBeVisible({ timeout: 25_000 });

  // It must say the table is not in use yet. Somebody who has just entered mappings carefully deserves to know
  // that before they go looking for the effect on a channel.
  await expect(page.locator("main")).toContainText(/Nothing uses this yet/i);

  // And the listing must show it. A table nobody references was previously invisible here, because tables were
  // only discovered through the channels that loaded them - so a newly created one did not appear in the very
  // section that created it.
  await expect(page.locator("main"), "the new table is not listed after being created").toContainText(name, {
    timeout: 25_000,
  });

  expect(problems, problems.join("\n")).toEqual([]);
});

test("a table that would misbehave quietly is refused with a reason", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Tables");

  await page.getByRole("button", { name: /^New shared table$/ }).click();

  // The same value mapped twice. Whichever wins is an implementation detail, and the value that loses looks
  // exactly like one that was never mapped - which gets debugged as a missing mapping, in production.
  await page.getByLabel("Name", { exact: true }).fill("ambiguous");
  await page.getByLabel("What this table is for").fill("deliberately contradictory, to check the refusal");
  await page.getByLabel("Value 1 to map from").fill("1");
  await page.getByLabel("Value 1 to map to").fill("M");
  await page.getByLabel("Value 2 to map from").fill("1");
  await page.getByLabel("Value 2 to map to").fill("F");

  await page.getByRole("button", { name: /^Save table$/ }).click();

  const alert = page.locator('main [role="alert"]');
  await expect(alert, "a table mapping the same value twice was accepted").toBeVisible({ timeout: 20_000 });
  await expect(alert, "the refusal does not explain what is wrong").toContainText(/twice/i);
});

test("a table with no description is refused", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Tables");

  await page.getByRole("button", { name: /^New shared table$/ }).click();

  // Required rather than optional, and deliberately so. A table called "codes" that nobody can explain is
  // exactly the artefact this section warns about: nobody dares change it and nobody dares delete it.
  await page.getByLabel("Name", { exact: true }).fill("nameless-purpose");
  await page.getByLabel("Value 1 to map from").fill("A");
  await page.getByLabel("Value 1 to map to").fill("B");

  await page.getByRole("button", { name: /^Save table$/ }).click();

  const alert = page.locator('main [role="alert"]');
  await expect(alert, "a table with no description was accepted").toBeVisible({ timeout: 20_000 });
  await expect(alert).toContainText(/what this table is for/i);
});
