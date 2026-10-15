import { test, expect } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { connect } from "node:net";
import { openTab } from "./nav";
import { freePort } from "./mllpchannel";
import { waitForListener } from "./mllp";

// Direct secure messaging, end to end with OpenSSL standing in for another organisation's edge system: a channel receives
// an OpenSSL-signed and -encrypted message for its Direct address over SMTP, opens it and stores the document; the Direct
// screen shows the address, and a correspondent's certificate is added to (and refused from) the address book.

const headers = { "X-Perfuse-Request": "1" };

function openssl(args: string[], input?: string): string {
  return execFileSync("openssl", args, { input, encoding: "utf8", stdio: ["pipe", "pipe", "pipe"] });
}

/** pki makes an anchor and two address certificates (ours and a correspondent's) with OpenSSL. */
function pki(dir: string) {
  const f = (n: string) => join(dir, n);
  openssl(["req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", f("ca.key"), "-out", f("anchors.pem"), "-days", "2",
    "-subj", "/CN=E2E Direct Anchor", "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign"]);
  const issue = (name: string, email: string) => {
    openssl(["req", "-newkey", "rsa:2048", "-nodes", "-keyout", f(`${name}.key`), "-out", f(`${name}.csr`), "-subj", `/CN=${email}`]);
    writeFileSync(f(`${name}.ext`), `subjectAltName=email:${email}\nkeyUsage=digitalSignature,keyEncipherment\nextendedKeyUsage=emailProtection\n`);
    openssl(["x509", "-req", "-in", f(`${name}.csr`), "-CA", f("anchors.pem"), "-CAkey", f("ca.key"), "-CAcreateserial", "-days", "2",
      "-extfile", f(`${name}.ext`), "-out", f(`${name}.crt`)]);
  };
  issue("us", "intake@direct.e2e-clinic.example");
  issue("them", "referrals@direct.e2e-hospital.example");
  return f;
}

/** smtp delivers one message to a listener as a HISP would, returning the server's reply to the end of DATA. */
function smtp(port: number, from: string, to: string, message: string): Promise<string> {
  return new Promise((resolve, reject) => {
    const sock = connect(port, "127.0.0.1");
    const steps = [`EHLO hisp.e2e.example`, `MAIL FROM:<${from}>`, `RCPT TO:<${to}>`, `DATA`, message.replace(/\r?\n/g, "\r\n").replace(/^\./gm, "..") + "\r\n.", "QUIT"];
    let buf = "";
    let i = 0;
    let dataReply = "";
    sock.setEncoding("utf8");
    sock.on("data", (d: string) => {
      buf += d;
      // Wait for a complete final reply line (code followed by a space).
      const lines = buf.split("\r\n").filter(Boolean);
      const last = lines[lines.length - 1] ?? "";
      if (!buf.endsWith("\r\n") || !/^\d{3} /.test(last)) return;
      if (i === 5) dataReply = last;
      buf = "";
      if (i < steps.length) sock.write(steps[i++] + "\r\n");
      else sock.end();
    });
    sock.on("close", () => resolve(dataReply));
    sock.on("error", reject);
  });
}

test("a Direct message made by OpenSSL is received, opened and stored; the address book takes a certificate", async ({ page }) => {
  const faults: string[] = [];
  page.on("pageerror", (e) => faults.push(e.message));
  const dir = mkdtempSync(join(tmpdir(), "perfuse-direct-"));
  try {
    const f = pki(dir);
    const port = await freePort();
    const yaml = [
      "name: e2e-direct-in",
      "dataType: raw",
      "source:",
      "  type: direct",
      "  direct:",
      "    address: intake@direct.e2e-clinic.example",
      `    cert_file: ${f("us.crt")}`,
      `    key_file: ${f("us.key")}`,
      `    anchors_file: ${f("anchors.pem")}`,
      `    listen: 127.0.0.1:${port}`,
      "destinations:",
      "  - name: keep",
      "    type: file",
      `    dir: ${join(dir, "out")}`,
      "",
    ].join("\n");
    await page.goto("/");
    const created = await page.request.post("/api/channels", { headers, data: { yaml } });
    expect(created.ok(), await created.text()).toBe(true);
    expect((await page.request.post("/api/channels/e2e-direct-in/start", { headers })).ok()).toBe(true);
    await waitForListener(port);

    // The other organisation's edge: sign as referrals@, encrypt to our certificate, as S/MIME (OpenSSL's smime).
    const content = "Content-Type: text/plain; charset=UTF-8\r\n\r\nReferral for DOE, JANE: please see her on Tuesday.\r\n";
    writeFileSync(f("content.txt"), content);
    const signed = openssl(["smime", "-sign", "-md", "sha256", "-in", f("content.txt"), "-signer", f("them.crt"), "-inkey", f("them.key")]);
    writeFileSync(f("signed.eml"), signed);
    const encrypted = openssl(["smime", "-encrypt", "-aes256", "-in", f("signed.eml"), "-from", "referrals@direct.e2e-hospital.example",
      "-to", "intake@direct.e2e-clinic.example", "-subject", "Referral", f("us.crt")]);
    const reply = await smtp(port, "referrals@direct.e2e-hospital.example", "intake@direct.e2e-clinic.example", encrypted);
    expect(reply, "the HISP's delivery was taken").toMatch(/^250 /);

    // A plain e-mail is refused at DATA: Direct is never accepted unencrypted.
    const plain = await smtp(port, "someone@example.org", "intake@direct.e2e-clinic.example", "Subject: hi\r\n\r\nNot Direct.");
    expect(plain).toMatch(/^554 /);

    const msgs = await (await page.request.get("/api/messages?channel=e2e-direct-in&limit=5")).json();
    const list = (msgs.messages ?? msgs) as { id: string }[];
    expect(list.length, JSON.stringify(msgs)).toBeGreaterThan(0);

    // The Direct screen shows the address and its certificate.
    await openTab(page, "Direct");
    const addresses = page.getByTestId("direct-addresses");
    await expect(addresses).toContainText("intake@direct.e2e-clinic.example");
    await expect(addresses).toContainText("Receives on 127.0.0.1:" + port);
    await expect(addresses).toContainText("E2E Direct Anchor");

    // The address book: a certificate for someone else is refused; the right one is kept and offered as PEM.
    const form = page.getByTestId("direct-contact-form");
    await form.getByLabel("Direct address or domain").fill("referrals@direct.e2e-hospital.example");
    await form.getByLabel("Name").fill("Dr Jane Smith");
    await form.getByLabel("Organisation").fill("E2E Hospital");
    await page.getByLabel("Certificate (PEM)").fill(readFileSync(f("us.crt"), "utf8"));
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByText(/is not for referrals@direct\.e2e-hospital\.example/)).toBeVisible();
    await page.getByLabel("Certificate (PEM)").fill(readFileSync(f("them.crt"), "utf8"));
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByTestId("direct-saved")).toContainText("saved with 1 certificate");
    const contacts = page.getByTestId("direct-contacts");
    await expect(contacts).toContainText("Dr Jane Smith");
    await expect(contacts).toContainText("Pasted");
    const pem = await page.request.get("/api/direct/contacts/referrals@direct.e2e-hospital.example/certificate");
    expect(await pem.text()).toContain("BEGIN CERTIFICATE");

    // A DNS lookup of a malformed server is a plain refusal, not a fault.
    await page.getByLabel("Direct address", { exact: true }).fill("referrals@direct.e2e-hospital.example");
    await page.getByLabel("DNS server").fill("not a server");
    await page.getByRole("button", { name: "Look up" }).click();
    await expect(page.getByText("the DNS server is host:port")).toBeVisible();

    page.once("dialog", (d) => d.accept());
    await contacts.getByRole("button", { name: "Remove" }).click();
    await expect(page.getByTestId("direct-no-contacts")).toBeVisible();

    await page.request.post("/api/channels/e2e-direct-in/stop", { headers });
    await page.request.delete("/api/channels/e2e-direct-in", { headers });
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
  expect(faults, faults.join("\n")).toEqual([]);
});

test("the builder writes a Direct destination the server accepts", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Channels");
  await page.getByRole("button", { name: "+ New channel" }).click();
  await page.getByRole("button", { name: "Start from nothing" }).click();
  await page.getByLabel("Channel name", { exact: true }).fill("e2e-direct-dest");
  const typeSelect = page.locator("main select").filter({ has: page.locator('option:text-is("A directory on disk")') }).first();
  await typeSelect.selectOption({ label: "A Direct address, through a HISP (Direct secure messaging)" });
  await page.getByLabel("From (this Direct address)").fill("referrals@direct.hospital.example");
  await page.getByLabel("To (Direct addresses)").fill("intake@direct.clinic.example");
  await page.getByLabel("HISP submission server").fill("smtp.hisp.example:587");
  await page.getByLabel("Package a CDA document as IHE XDM (with its metadata)").check();
  await expect(page.getByLabel("XDM source ID (your organisation's OID)")).toBeVisible();
  const pre = page.locator("pre").first();
  await expect(pre).toContainText("type: direct", { timeout: 20_000 });
  await expect(pre).toContainText("smtp_host: smtp.hisp.example:587");
  await expect(pre).toContainText("xdm: true");
});
