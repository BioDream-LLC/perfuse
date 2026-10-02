import { test, expect } from "@playwright/test";

import { openTab } from "./nav";

/** createChannel posts a channel file, which is quicker and less fragile than driving the builder for a test about exporting. */
async function createChannel(page: import("@playwright/test").Page, yaml: string): Promise<void> {
  await page.goto("/");

  const created = await page.request.post("/api/channels", {
    headers: { "X-Perfuse-Request": "1" },
    data: { yaml },
  });
  expect(created.ok(), `creating the channel failed: ${created.status()} ${await created.text()}`).toBe(true);
}

// Exporting a channel to Mirth from the browser.
//
// The standing rule here is that everything can be done from the web interface, so an exporter reachable only from Go would be a feature
// that does not exist as far as an operator is concerned.
//
// What this checks beyond the button working: that the losses are shown before the file is offered. Mirth's format has nowhere to put a
// Perfuse filter, transformation, contract or shadow comparison, so an exported channel does less than the one it came from. The file says
// so in its description, and a description in an XML file is the easiest thing in the world to scroll past - which is why the dialogue
// exists and why it is worth a test.

test("a channel can be exported to Mirth from the channel list", async ({ page }) => {
  const name = `mirth-export-${Date.now()}`;

  await createChannel(page, `name: ${name}
source:
  type: mllp
  listen: ":16801"
destinations:
  - name: To the registry
    type: mllp
    address: registry.example.invalid:16802
`);

  await page.goto("/");
  await openTab(page, "Channels");

  // Scoped to the card for this channel. The list is cards rather than a table, and "To Mirth" appears once per channel, so an
  // unscoped click would press whichever card happened to be first.
  const card = page.locator(".card").filter({ has: page.getByRole("heading", { name, exact: true }) });
  await expect(card).toBeVisible();

  await card.getByRole("button", { name: "To Mirth" }).click();

  const dialog = page.getByRole("dialog", { name: /Export .* to Mirth/ });
  await expect(dialog).toBeVisible();

  // This channel has no filter, transformations or contract, so there is nothing to lose and the dialogue says so rather than showing an
  // empty list. An empty list would read as a failure to check.
  await expect(dialog).toContainText("Nothing is lost");

  // A real link with a download attribute, so the browser's own handling applies and the file arrives as a file rather than as text in a
  // pane somebody would have to copy out.
  const link = dialog.getByRole("link", { name: "Download the Mirth file" });
  await expect(link).toHaveAttribute("download", "");
  await expect(link).toHaveAttribute("href", `/api/channels/${name}/mirth`);

  // What the link points at is fetched directly rather than through a download event.
  //
  // Waiting on Chromium's download event was tried first and timed out at ninety seconds while the endpoint itself answered 200 with an
  // attachment disposition and a valid document - so the test was measuring Playwright's download plumbing, not this feature. Fetching
  // the URL the button offers checks the part that belongs to Perfuse.
  const res = await page.request.get(`/api/channels/${name}/mirth`);
  expect(res.status(), await res.text()).toBe(200);

  expect(res.headers()["content-disposition"]).toContain(`${name}.mirth.xml`);

  const doc = await res.text();
  for (const want of ["<channel", "TCP Listener", "TCP Sender", "16801", "registry.example.invalid"]) {
    expect(doc, `the exported document is missing ${want}`).toContain(want);
  }

  // Mirth's Channel model has no enabled element, and writing one makes Mirth store the channel as invalid with its destinations
  // discarded. Asserted here too, on the document a person actually downloads.
  expect(doc).not.toContain("<enabled>true</enabled>\n  <sourceConnector");
});

test("exporting a channel with transformations says what will not survive", async ({ page }) => {
  const name = `mirth-lossy-${Date.now()}`;

  await createChannel(page, `name: ${name}
group: Admissions
filter: "PID-3 != ''"
source:
  type: mllp
  listen: ":16803"
transformations:
  - set:
      path: PID-8
      value: U
destinations:
  - name: To the registry
    type: mllp
    address: registry.example.invalid:16804
`);

  await page.goto("/");
  await openTab(page, "Channels");

  await page
    .locator(".card")
    .filter({ has: page.getByRole("heading", { name, exact: true }) })
    .getByRole("button", { name: "To Mirth" })
    .click();

  const dialog = page.getByRole("dialog", { name: /Export .* to Mirth/ });
  await expect(dialog).toBeVisible();

  // Each loss named separately. A single sentence saying "some things may not convert" would be true and useless.
  await expect(dialog).toContainText("filter");
  await expect(dialog).toContainText("transformation");
  await expect(dialog).toContainText("group");

  // And the file is still offered, because a lossy export somebody has seen the losses of is exactly what this feature is for.
  await expect(dialog.getByRole("link", { name: "Download the Mirth file" })).toBeVisible();
});

test("a channel Mirth cannot express is refused rather than approximated", async ({ page }) => {
  const name = `mirth-refused-${Date.now()}`;

  await createChannel(page, `name: ${name}
source:
  type: mllp
  listen: ":16805"
destinations:
  - name: To the bucket
    type: s3
    s3:
      bucket: results
      region: us-east-1
      access_key_id: placeholder-not-a-real-key
      secret_access_key: placeholder-not-a-real-secret
`);

  await page.goto("/");
  await openTab(page, "Channels");

  await page
    .locator(".card")
    .filter({ has: page.getByRole("heading", { name, exact: true }) })
    .getByRole("button", { name: "To Mirth" })
    .click();

  const dialog = page.getByRole("dialog", { name: /Export .* to Mirth/ });
  await expect(dialog).toBeVisible();

  await expect(dialog).toContainText("cannot be exported to Mirth");

  // The destination at fault is named. "This channel cannot be exported" on a channel with nine destinations would send somebody
  // looking through all nine.
  await expect(dialog).toContainText("To the bucket");

  // And no download is offered, which is the whole point: Mirth has no S3 connector, and a channel that imported cleanly while
  // delivering nowhere near the bucket would be worse than no file at all.
  await expect(dialog.getByRole("link", { name: "Download the Mirth file" })).toHaveCount(0);
});
