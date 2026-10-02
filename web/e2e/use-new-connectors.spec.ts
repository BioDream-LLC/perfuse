import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// The six connectors added to close the gap against Mirth, built from the form.
//
// use-builder.spec.ts walks every source type and accepts "the panel explained what is missing" as a pass. That is
// reasonable there - it is checking that choosing a type does not break the form - but it means a source with required
// fields never has its generated file inspected. All six of these have required fields, so all six would pass that test
// while writing nothing.
//
// So this fills them in and asserts on the values in the file. It also asserts the things the form is supposed to teach:
// that a settle time exists, that an archive is offered, and that framing has no default.

function watch(page: import("@playwright/test").Page): string[] {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`page error: ${e}`));
  return problems;
}

async function openNewChannelForm(page: import("@playwright/test").Page, name: string) {
  await page.goto("/");
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();
  await page.getByRole("button", { name: "Start from nothing" }).click();
  await page.getByLabel("Channel name", { exact: true }).fill(name);
}

async function chooseSource(page: import("@playwright/test").Page, option: RegExp) {
  const selector = page.getByLabel(/How do messages get here/).first();
  await expect(selector).toBeVisible({ timeout: 20_000 });

  const options = await selector.locator("option").allTextContents();
  const match = options.find((o) => option.test(o));
  expect(match, `no option matching ${option} is offered`).toBeTruthy();
  await selector.selectOption({ label: match as string });
}

/** yamlContaining waits for the preview to include every fragment, and reports what it actually said if it does not. */
async function yamlContaining(page: import("@playwright/test").Page, wants: string[]) {
  const pre = page.locator("pre").first();

  await expect
    .poll(
      async () => {
        const text = (await pre.count()) > 0 ? ((await pre.first().textContent()) ?? "") : "";
        return wants.filter((w) => !text.includes(w));
      },
      {
        timeout: 25_000,
        message: `the channel file never contained: ${wants.join(", ")}`,
      },
    )
    .toEqual([]);
}

test("a folder on this server can be turned into a channel from the form", async ({ page }) => {
  const problems = watch(page);

  await openNewChannelForm(page, "e2e-folder");
  await chooseSource(page, /We read files from a folder on this server/);

  await page.getByLabel(/Directory this channel may read/).fill("/var/spool/lab");
  await page.getByLabel(/Only files matching/).fill("*.hl7");

  // The values typed must reach the file, and so must the settle time - which is the setting that decides whether a
  // half-written message gets delivered, and the one a form could most easily fail to carry.
  await yamlContaining(page, [
    "type: file",
    "/var/spool/lab",
    "*.hl7",
    // Snake case, because the form sends JSON and the server writes the YAML.
    "stable_for",
  ]);

  expect(problems, problems.join("\n")).toEqual([]);
});

test("the folder form offers an archive rather than defaulting to deletion", async ({ page }) => {
  await openNewChannelForm(page, "e2e-folder-archive");
  await chooseSource(page, /We read files from a folder on this server/);
  await page.getByLabel(/Directory this channel may read/).fill("/var/spool/lab");

  // A first configuration that deletes destroys somebody's files while they are still working out whether the channel is
  // right, and the file is the only copy. So the default must be to move, and the archive directory must be asked for.
  const archive = page.getByLabel(/Archive directory/);
  await expect(archive, "no archive directory is offered, so the default is destructive").toBeVisible({
    timeout: 20_000,
  });
  await expect(archive).toHaveValue(/\S/);

  await yamlContaining(page, ["move_to"]);
});

test("a Windows share can be turned into a channel from the form", async ({ page }) => {
  await openNewChannelForm(page, "e2e-share");
  await chooseSource(page, /We collect files from a Windows share/);

  await page.getByLabel(/^Server/).fill("fileserver.hospital.local");
  await page.getByLabel(/Share name/).fill("data");
  await page.getByLabel(/^Username/).fill("svc-perfuse");

  await yamlContaining(page, ["type: smb", "fileserver.hospital.local", "data", "svc-perfuse"]);
});

test("an FTP server can be turned into a channel, and defaults to FTPS", async ({ page }) => {
  await openNewChannelForm(page, "e2e-ftp");
  await chooseSource(page, /We collect files from a server \(FTP or FTPS\)/);

  await page.getByLabel(/^Server/).fill("ftp.example.org:21");
  await page.getByLabel(/Directory this channel may read/).fill("/incoming");

  // FTPS is the default, so the file should not carry a security setting at all - and it certainly should not say none.
  // A form that quietly chose plain FTP would send the password in clear text.
  await yamlContaining(page, ["type: ftp", "ftp.example.org:21", "/incoming"]);

  const yaml = (await page.locator("pre").first().textContent()) ?? "";
  expect(yaml, "the form chose plain FTP without being asked").not.toMatch(/security:\s*none/);
});

test("a WebDAV collection can be turned into a channel from the form", async ({ page }) => {
  await openNewChannelForm(page, "e2e-webdav");
  await chooseSource(page, /We collect files from a WebDAV collection/);

  await page
    .getByLabel(/Collection address/)
    .fill("https://docs.example.org/remote.php/dav/files/perfuse/inbox");

  await yamlContaining(page, ["type: webdav", "https://docs.example.org"]);
});

test("a socket device can be configured, and framing has no default", async ({ page }) => {
  await openNewChannelForm(page, "e2e-socket");
  await chooseSource(page, /A device connects to us on a socket, not MLLP \(TCP\)/);

  const framing = page.getByLabel(/How messages are separated/);
  await expect(framing).toBeVisible({ timeout: 20_000 });

  // Nothing chosen to begin with, deliberately. The wrong framing does not fail - it delivers messages cut in half or
  // joined together, both of which usually still parse - so a default would appear to work.
  await expect(framing, "a framing was pre-selected, and there is no safe default").toHaveValue("");

  await framing.selectOption({ label: "A character marks the end (most devices)" });

  // Choosing it must reveal the delimiter field, or there is no way to say what the character is.
  const delimiter = page.getByLabel(/End of message/);
  await expect(delimiter, "choosing a delimited stream did not ask which character ends a message").toBeVisible();
  await delimiter.fill("\\x03");

  await page.getByLabel(/Start of message/).fill("\\x02");

  await yamlContaining(page, ["type: tcp", "framing: delimited", "x03", "x02"]);
});

test("a serial device can be configured, and the baud rate is not guessed", async ({ page }) => {
  await openNewChannelForm(page, "e2e-serial");
  await chooseSource(page, /A device is wired to us \(serial cable\)/);

  const baud = page.getByLabel(/Speed \(baud\)/);
  await expect(baud).toBeVisible({ timeout: 20_000 });

  // Empty or zero, never 9600. A wrong speed does not fail: it delivers readable-looking nonsense, so a guessed default
  // would be worse than no default at all.
  await expect(baud, "a baud rate was guessed, which would appear to work while delivering nonsense").toHaveValue(
    /^(0|)$/,
  );

  await page.getByLabel(/^Port/).fill("/dev/ttyUSB0");
  await baud.fill("9600");

  const framing = page.getByLabel(/How messages are separated/);
  await framing.selectOption({ label: "A character marks the end (most devices)" });
  await page.getByLabel(/End of message/).fill("\\r");

  await yamlContaining(page, ["type: serial", "/dev/ttyUSB0", "9600"]);

  // And the silence warning must be carried, because it is the only thing that will ever report this feed dying.
  await yamlContaining(page, ["quiet_after"]);
});
