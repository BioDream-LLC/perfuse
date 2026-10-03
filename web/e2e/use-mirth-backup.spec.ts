import { test, expect } from "@playwright/test";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { openTab } from "./nav";

// A whole-server backup, dropped on the migration screen the way a site leaving Mirth, the Open Integration Engine or BridgeLink would.
// The file is BridgeLink 26.9.0's own backup (scripts/mirth-engine-corpus.sh): three channels, a code template library enabled for one
// of them, and a channel group. Checked against the server afterwards, not only on the page: a channel that says "Imported" and does not
// load is the failure this guards against.

const here = dirname(fileURLToPath(import.meta.url));
const backup = readFileSync(
  join(here, "..", "..", "internal", "mirth", "testdata", "engines", "bridgelink-26.9.0", "server-configuration.xml"),
  "utf8",
);
const mutating = { "X-Perfuse-Request": "1" };

test("a BridgeLink server backup imports with its code template library and group", async ({ page }) => {
  const faults: string[] = [];
  page.on("pageerror", (e) => faults.push(e.message));

  await page.goto("/");
  // A channel left by an earlier run would make the import fail on the name rather than test anything.
  await page.request.delete("/api/channels/adt-inbound-from-ward", { headers: mutating });

  await openTab(page, "Migrate");
  const zone = page.getByText("Drop your channel export here");
  const target = zone.locator("xpath=ancestor::div[contains(@class,'border-dashed')][1]");
  await target.evaluate((el, xml) => {
    const dt = new DataTransfer();
    dt.items.add(new File([xml], "server-configuration.xml", { type: "text/xml" }));
    el.dispatchEvent(new DragEvent("dragover", { bubbles: true, cancelable: true, dataTransfer: dt }));
    el.dispatchEvent(new DragEvent("drop", { bubbles: true, cancelable: true, dataTransfer: dt }));
  }, backup);

  const read = page.getByTestId("mirth-what-was-read");
  await expect(read).toContainText("Read a server backup written by version 26.9.0: 3 channels, 1 code template library, 1 group", {
    timeout: 20_000,
  });
  await expect(read).toContainText("Admissions and labs");

  const libs = page.getByTestId("mirth-libraries");
  await expect(libs).toContainText("lib/site-helpers.js");
  await expect(libs).toContainText("1 function, 1 snippet left out");
  await expect(libs).toContainText("included by adt-inbound-from-ward");
  await libs.getByText("Show the script").click();
  await expect(libs).toContainText("function formatMRN");

  // Importing the channel saves the library it includes first; nobody has to know the order.
  const card = page.locator("div.rounded-xl", { hasText: "ADT Inbound From Ward" }).first();
  await card.getByRole("button", { name: "Import", exact: true }).click();
  await expect(card).toContainText("Imported", { timeout: 20_000 });
  await expect(libs).toContainText("Saved");

  // And the server agrees: the channel exists, in its group, including the library.
  const res = await page.request.get("/api/channels/adt-inbound-from-ward");
  expect(res.ok(), await res.text()).toBeTruthy();
  const body = await res.json();
  expect(body.yaml).toContain("group: Admissions and labs");
  expect(body.yaml).toContain("lib/site-helpers.js");

  // The other two came across without a blocker: an HTTP listener and a file reader, which the translator once refused.
  for (const name of ["Lab Results To Warehouse", "Orders File Drop"]) {
    const other = page.locator("div.rounded-xl", { hasText: name }).first();
    await expect(other.getByRole("button", { name: "Import", exact: true })).toBeEnabled();
  }

  await page.request.delete("/api/channels/adt-inbound-from-ward", { headers: mutating });
  expect(faults, faults.join("\n")).toEqual([]);
});
