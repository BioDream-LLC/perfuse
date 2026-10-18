import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// The connectors and screens added in the G round (mailbox source, AS2, OpenPGP, UN/EDIFACT, the patient index, the cluster
// panel, SNMP, terminology), each driven from the console and checked by what it produced rather than by whether it rendered.

function watch(page: import("@playwright/test").Page): string[] {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`page error: ${e}`));
  return problems;
}

async function newChannel(page: import("@playwright/test").Page, name: string) {
  await page.goto("/");
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();
  await page.getByRole("button", { name: "Start from nothing" }).click();
  await page.getByLabel("Channel name", { exact: true }).fill(name);
}

async function choose(page: import("@playwright/test").Page, label: string | RegExp, option: RegExp) {
  const sel = page.getByLabel(label).first();
  await expect(sel).toBeVisible({ timeout: 20_000 });
  const options = await sel.locator("option").allTextContents();
  const match = options.find((o) => option.test(o));
  expect(match, `no option matching ${option} in ${label}; offered: ${options.join(" | ")}`).toBeTruthy();
  await sel.selectOption({ label: match as string });
}

async function yamlContaining(page: import("@playwright/test").Page, wants: string[]) {
  const pre = page.locator("pre").first();
  await expect
    .poll(
      async () => {
        const text = (await pre.count()) > 0 ? ((await pre.textContent()) ?? "") : "";
        return wants.filter((w) => !text.includes(w));
      },
      { timeout: 25_000, message: `the channel file never contained: ${wants.join(", ")}` },
    )
    .toEqual([]);
}

/** The server's own objections, as the builder shows them. */
async function serverSays(page: import("@playwright/test").Page): Promise<string> {
  return (await page.locator("main [role=alert]").allInnerTexts()).join("\n");
}

test("a mailbox read with Microsoft 365 sign-in reaches the file, and a missing secret is named", async ({ page }) => {
  const problems = watch(page);
  await newChannel(page, "e2e-mailbox");
  await choose(page, /How do messages get here/, /We read a mailbox/);
  await page.getByLabel("Mail server").fill("outlook.office365.com");
  await page.getByLabel("Mailbox address (user name)").fill("lab@example.org");
  await choose(page, "Sign in with", /OAuth/);
  await page.getByLabel("Tenant").fill("contoso.onmicrosoft.com");
  await page.getByLabel("Client id").fill("11111111-2222-3333-4444-555555555555");
  await yamlContaining(page, ["type: email", "host: outlook.office365.com", "provider: microsoft", "tenant: contoso.onmicrosoft.com"]);
  await expect.poll(() => serverSays(page), { timeout: 15_000 }).toMatch(/refresh_token .* or client_secret/);
  expect(problems, problems.join("\n")).toEqual([]);
});

test("an AS2 source taking EDIFACT from a partner reaches the file", async ({ page }) => {
  const problems = watch(page);
  await newChannel(page, "e2e-as2-in");
  await choose(page, "Message format", /UN\/EDIFACT/);
  await choose(page, /How do messages get here/, /AS2/);
  await page.getByLabel("Our AS2 identifier").fill("PERFUSE-AS2");
  await page.getByLabel("Our certificate (PEM)").first().fill("/etc/perfuse/as2/us.crt");
  await page.getByLabel("Our private key (PEM)").first().fill("/etc/perfuse/as2/us.key");
  await page.getByRole("button", { name: "Add a partner" }).click();
  await page.getByLabel("Partner 1 AS2 identifier").fill("SUPPLIER-AS2");
  await page.getByLabel("Partner 1 certificate").fill("/etc/perfuse/as2/supplier.crt");
  await yamlContaining(page, [
    "dataType: edifact",
    "type: as2",
    "id: PERFUSE-AS2",
    "SUPPLIER-AS2",
    "/etc/perfuse/as2/supplier.crt",
  ]);
  // What the server still wants is the destination, and nothing about AS2.
  await expect.poll(() => serverSays(page), { timeout: 15_000 }).not.toMatch(/source\.as2/);
  expect(problems, problems.join("\n")).toEqual([]);
});

test("OpenPGP on a source reaches the file", async ({ page }) => {
  const problems = watch(page);
  await newChannel(page, "e2e-pgp-in");
  await choose(page, /How do messages get here/, /folder on this server/);
  await page.getByLabel(/Directory this channel may read/).fill("/var/spool/pgp");
  // Behind its disclosure, as rarely-needed settings are.
  await page.getByRole("button", { name: /Open PGP-encrypted messages/ }).click();
  await page.getByLabel("Messages arrive encrypted with OpenPGP").check();
  await page.getByLabel("Our secret key").fill("/etc/perfuse/pgp/ours.asc");
  await page.getByLabel("Senders' public keys").fill("/etc/perfuse/pgp/senders.asc");
  await yamlContaining(page, ["pgp:", "/etc/perfuse/pgp/ours.asc", "/etc/perfuse/pgp/senders.asc"]);
  expect(problems, problems.join("\n")).toEqual([]);
});

test("an AS2 destination with compression reaches the file", async ({ page }) => {
  const problems = watch(page);
  await newChannel(page, "e2e-as2-out");
  await page.getByLabel(/^Name$/i).first().fill("partner").catch(() => {});
  await choose(page, /^Type$/i, /AS2/);
  await page.getByLabel("Partner's AS2 URL").fill("https://as2.partner.example/as2");
  await page.getByLabel("From (our AS2 identifier)").fill("PERFUSE-AS2");
  await page.getByLabel("To (the partner's AS2 identifier)").fill("PARTNER-AS2");
  await page.getByLabel("compress").check();
  await yamlContaining(page, ["type: as2", "https://as2.partner.example/as2", "PARTNER-AS2", "compress: true"]);
  expect(problems, problems.join("\n")).toEqual([]);
});

test("the patient index opens, matches nothing on its own by default, and searches", async ({ page }) => {
  const problems = watch(page);
  await page.goto("/");
  await openTab(page, "Patient index");
  const main = page.locator("main");
  await expect(main.getByRole("heading", { name: "How the index matches" })).toBeVisible({ timeout: 20_000 });
  await expect(main.getByRole("heading", { name: "Possible matches to review" })).toBeVisible();
  await main.getByLabel("Family name").first().fill("NOBODY-E2E");
  await main.getByLabel("Family name").first().press("Enter");
  await expect(main).not.toContainText(/something went wrong|failed to load/i);
  expect(problems, problems.join("\n")).toEqual([]);
});

test("the fleet says plainly that this server is not in a cluster", async ({ page }) => {
  const problems = watch(page);
  await page.goto("/");
  await openTab(page, "Fleet");
  const main = page.locator("main");
  await expect(main.getByRole("heading", { name: "Cluster" })).toBeVisible({ timeout: 20_000 });
  await expect(main).toContainText(/runs on its own/);
  await expect(main).toContainText("-cluster active-passive");
  expect(problems, problems.join("\n")).toEqual([]);
});

test("the SNMP pane reports the agent and offers the MIB", async ({ page }) => {
  const problems = watch(page);
  await page.goto("/");
  await openTab(page, "Settings");
  const status = page.getByTestId("snmp-status");
  await expect(status).toBeVisible({ timeout: 20_000 });
  await expect(status).toContainText(/Agent/);
  const mib = await page.request.get("/api/snmp/mib");
  expect(mib.status()).toBe(200);
  expect(await mib.text()).toContain("DEFINITIONS ::= BEGIN");
  expect(problems, problems.join("\n")).toEqual([]);
});

test("terminology: code systems and unmapped values are on the tables screen", async ({ page }) => {
  const problems = watch(page);
  await page.goto("/");
  await openTab(page, "Tables");
  const main = page.locator("main");
  await expect(main.getByRole("heading", { name: "Code systems" })).toBeVisible({ timeout: 20_000 });
  await expect(main.getByRole("heading", { name: "Values no table maps" })).toBeVisible();
  expect(problems, problems.join("\n")).toEqual([]);
});
