import { test, expect, type Page } from "@playwright/test";
import { openTab } from "./nav";

// The AWS connectors, built through the form the way an integration analyst would: an SQS source, an SQS destination grouped by
// patient, an S3 archive written to Glacier as JSON lines for Athena, an S3 source. Each checks the channel file the form produced and
// that the server accepts it - the server is the authority, and a form that writes a key the server does not know produces a channel
// that is refused whole.

const mutating = { "X-Perfuse-Request": "1" };

async function newChannel(page: Page, name: string) {
  await page.goto("/");
  await page.request.delete(`/api/channels/${name}`, { headers: mutating });
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();
  await page.getByRole("button", { name: "Start from nothing" }).click();
  await page.getByLabel("Channel name", { exact: true }).fill(name);
}

async function chooseSource(page: Page, option: RegExp) {
  const selector = page.getByLabel(/How do messages get here/).first();
  const options = await selector.locator("option").allTextContents();
  const match = options.find((o) => option.test(o));
  expect(match, `no source option matching ${option}`).toBeTruthy();
  await selector.selectOption({ label: match as string });
}

async function yamlContaining(page: Page, wants: string[]) {
  const pre = page.locator("pre").first();
  await expect
    .poll(async () => {
      const text = (await pre.textContent()) ?? "";
      return wants.filter((w) => !text.includes(w));
    }, {
      timeout: 25_000,
      message: `the channel file never contained: ${wants.join(", ")}`,
    })
    .toEqual([]);
}

async function fillAWSSource(page: Page) {
  await page.getByLabel(/^Region/).first().fill("us-east-1");
  // Literal test keys: the e2e server has no AWS_* variables, and an unresolved reference is refused as missing credentials.
  await page.getByLabel(/^Access key ID/).first().fill("AKIAEXAMPLETEST");
  await page.getByLabel(/^Secret access key/).first().fill("not-a-real-secret");
}

/** The destination's name and the shared AWS access fields every S3, SQS and SNS destination signs with. */
async function fillAWSDestination(page: Page, name: string) {
  await page.getByRole("textbox", { name: "Name", exact: true }).last().fill(name);
  await page.getByPlaceholder("eu-west-2").last().fill("us-east-1");
  await page.getByPlaceholder("${AWS_ACCESS_KEY_ID}").last().fill("AKIAEXAMPLETEST");
  await page.getByPlaceholder("${AWS_SECRET_ACCESS_KEY}").last().fill("not-a-real-secret");
}

async function saveAndCheck(page: Page, name: string, wants: string[]) {
  await page.getByRole("button", { name: "Create channel" }).click();
  // Polled through the API, not the page: the name is on screen in the form whether or not the save worked.
  await expect.poll(async () => (await page.request.get(`/api/channels/${name}`)).status(), {
    timeout: 15_000,
    message: `the channel was not created: ${await page.locator("main").innerText().catch(() => "")}`.slice(0, 600),
  }).toBe(200);
  const res = await page.request.get(`/api/channels/${name}`);
  expect(res.ok(), await res.text()).toBeTruthy();
  const yaml = (await res.json()).yaml as string;
  for (const w of wants) expect(yaml, `the saved channel lacks ${w}`).toContain(w);
  await page.request.delete(`/api/channels/${name}`, { headers: mutating });
}

test("an SQS queue to an SQS FIFO queue grouped by patient, from the form", async ({ page }) => {
  const faults: string[] = [];
  page.on("pageerror", (e) => faults.push(e.message));
  const name = "e2e-sqs";

  await newChannel(page, name);
  await chooseSource(page, /Amazon SQS queue/);
  await fillAWSSource(page);
  await page.getByLabel(/^Queue URL/).first().fill("https://sqs.us-east-1.amazonaws.com/123456789012/adt-in");
  await page.getByLabel(/^Visibility timeout/).fill("90s");

  const type = page.locator('select[id^="dest-"][id$="-type"]').first();
  await type.selectOption("sqs");
  await page.getByPlaceholder("https://sqs.eu-west-2.amazonaws.com/123456789012/adt.fifo").fill(
    "https://sqs.us-east-1.amazonaws.com/123456789012/adt-out.fifo",
  );
  await fillAWSDestination(page, "to-queue");

  await yamlContaining(page, [
    "type: sqs",
    "queue_url: https://sqs.us-east-1.amazonaws.com/123456789012/adt-in",
    "visibility_timeout: 90s",
    "queue_url: https://sqs.us-east-1.amazonaws.com/123456789012/adt-out.fifo",
  ]);
  await saveAndCheck(page, name, ["type: sqs", "adt-out.fifo", "region: us-east-1"]);
  expect(faults, faults.join("\n")).toEqual([]);
});

test("an S3 bucket prefix to a Glacier archive Athena can query, from the form", async ({ page }) => {
  const name = "e2e-s3";
  await newChannel(page, name);
  await chooseSource(page, /Amazon S3 bucket/);
  await fillAWSSource(page);
  await page.getByLabel(/^Bucket/).first().fill("hospital-inbound");
  await page.getByLabel(/^Only keys ending in/).fill(".hl7");

  const type = page.locator('select[id^="dest-"][id$="-type"]').first();
  await type.selectOption("s3");
  await page.getByPlaceholder("hospital-hl7-archive").fill("hospital-archive");
  await fillAWSDestination(page, "archive");
  await page.getByLabel("Storage class").selectOption("GLACIER_IR");
  await page.getByLabel("Write as").selectOption("ndjson");

  await yamlContaining(page, [
    "type: s3",
    "bucket: hospital-inbound",
    "prefix: inbound/",
    "suffix: .hl7",
    "bucket: hospital-archive",
    "storage_class: GLACIER_IR",
    "format: ndjson",
  ]);
  await saveAndCheck(page, name, ["storage_class: GLACIER_IR", "format: ndjson", "prefix: inbound/"]);
});

test("an SNS topic is offered as a destination", async ({ page }) => {
  await newChannel(page, "e2e-sns");
  const type = page.locator('select[id^="dest-"][id$="-type"]').first();
  await type.selectOption("sns");
  await page.getByPlaceholder("arn:aws:sns:eu-west-2:123456789012:adt").fill("arn:aws:sns:us-east-1:123456789012:adt");
  await yamlContaining(page, ["type: sns", "topic_arn: arn:aws:sns:us-east-1:123456789012:adt"]);
});
