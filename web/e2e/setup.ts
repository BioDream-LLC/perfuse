import { spawn, execFileSync, ChildProcess } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, createWriteStream } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "node:net";
import { acquireRunLock } from "./runlock";
import { chromium, request } from "@playwright/test";

// Starts a real perfuse server for the test run and signs in once.
//
// The credentials come from the server's own first-run output rather than from a test-only backdoor, so this exercises the
// path a real operator takes. A flag that seeded a known password would be a security control an author could grant
// themselves, and would test a code path nobody in production uses.

// This package is ESM, so here does not exist and has to be derived.
const here = dirname(fileURLToPath(import.meta.url));

const REPO = resolve(here, "..", "..");
const STATE_FILE = join(here, ".state.json");

/** freePort asks the OS for an unused port rather than guessing one. */
function freePort(): Promise<number> {
  return new Promise((res, rej) => {
    const srv = createServer();
    srv.on("error", rej);
    srv.listen(0, "127.0.0.1", () => {
      const addr = srv.address();
      if (addr && typeof addr === "object") {
        const port = addr.port;
        srv.close(() => res(port));
      } else {
        srv.close(() => rej(new Error("could not determine a free port")));
      }
    });
  });
}

export default async function globalSetup() {
  // Before anything else, including choosing a port. A second run has to stop before it can overwrite the state file or the signed-in
  // session, not part way through - the damage is done the moment it starts writing.
  acquireRunLock();

  const dir = mkdtempSync(join(tmpdir(), "perfuse-e2e-"));
  const channels = join(dir, "channels");
  mkdirSync(channels, { recursive: true });

  // One channel, so tabs that list channels have something to show. A GUI tested against an empty install passes every
  // "renders without crashing" assertion and tells you nothing about the case that matters.
  //
  // This was wrong for a whole day and nobody noticed. It nested the listen address under an "mllp:" key, which is not the schema -
  // source is flat - so the server refused the file at load and every test above ran against the empty install this fixture exists
  // to avoid. The channel count on the dashboard read 0/0 throughout and I read past it.
  //
  // Refusing it was correct behaviour. The failure was that the harness asserted nothing about whether its own fixture took effect,
  // which is checked below now.
  // A known port rather than 0. With an OS-assigned port nothing could send this channel a message, so
  // Messages, message detail and the trace panel had no data to be tested against and every assertion
  // about them was really an assertion about the empty state.
  const mllpPort = await freePort();

  // A contract on the labs channel, so the Contracts view has something to watch.
  //
  // A contract says what a feed must look like, so the day it changes somebody is told rather than
  // finding out weeks later from a receiver that fell over. With no contract anywhere, every test of that
  // view tested the sentence "No feed has a contract yet".
  //
  // Written by hand rather than promoted from a corpus, so the fixture does not depend on running another
  // command, and deliberately small: two expectations about segments that every message the tests send
  // does contain.
  const contractFile = join(dir, "labs.contract.yaml");
  writeFileSync(
    contractFile,
    [
      "expectations:",
      "  - path: MSH-9.1",
      "    rule: one of",
      "    values: [ADT]",
      "    why: this feed carries admissions only",
      "  - path: PID-3.1",
      "    rule: populated",
      "    why: every patient must be identifiable",
      "",
    ].join("\n"),
  );

  writeFileSync(
    join(channels, "labs.yaml"),
    [
      "name: labs",
      "contract:",
      `  file: ${contractFile}`,
      "source:",
      "  type: mllp",
      `  listen: 127.0.0.1:${mllpPort}`,
      "destinations:",
      "  - name: archive",
      "    type: file",
      `    dir: ${join(dir, "out")}`,
      "",
    ].join("\n"),
  );

  // A second channel with a shadow, so the Shadow view has something to report.
  //
  // Shadowing runs a candidate configuration beside the live one and compares the output, which is the
  // feature that lets somebody change an interface without guessing. With no shadowed channel anywhere,
  // every test of that view was a test of the words "Nothing is being shadowed".
  //
  // The candidate differs from the live channel by one transformation, so there is a difference to find.
  const candidate = join(channels, "shadowed-candidate.yaml");
  writeFileSync(
    candidate,
    [
      "name: shadowed-candidate",
      "enabled: false",
      "source:",
      "  type: mllp",
      "  listen: 127.0.0.1:0",
      "transformations:",
      "  - set:",
      "      path: PID-8",
      "      value: U",
      "destinations:",
      "  - name: archive",
      "    type: file",
      `    dir: ${join(dir, "shadow-out")}`,
      "",
    ].join("\n"),
  );

  const shadowPort = await freePort();
  writeFileSync(
    join(channels, "shadowed.yaml"),
    [
      "name: shadowed",
      "source:",
      "  type: mllp",
      `  listen: 127.0.0.1:${shadowPort}`,
      "shadow:",
      `  channel: ${candidate}`,
      "destinations:",
      "  - name: archive",
      "    type: file",
      `    dir: ${join(dir, "out")}`,
      "",
    ].join("\n"),
  );

  // A second shadowed channel, so the channel selector in that view exists at all.
  //
  // It only renders when more than one channel is shadowed, so with a single one it was unreachable: no
  // test had ever clicked it, and none could. This is the same shape as the channels filter that only
  // appears past eight channels - a control the fixture cannot produce is a control nobody has tried.
  const candidateB = join(channels, "shadowed-candidate-b.yaml");
  writeFileSync(
    candidateB,
    [
      "name: shadowed-candidate-b",
      "enabled: false",
      "source:",
      "  type: mllp",
      "  listen: 127.0.0.1:0",
      "transformations:",
      "  - set:",
      "      path: PID-7",
      "      value: 19700101",
      "destinations:",
      "  - name: archive",
      "    type: file",
      `    dir: ${join(dir, "shadow-out-b")}`,
      "",
    ].join("\n"),
  );

  const shadowPortB = await freePort();
  writeFileSync(
    join(channels, "shadowed-b.yaml"),
    [
      "name: shadowed-b",
      "source:",
      "  type: mllp",
      `  listen: 127.0.0.1:${shadowPortB}`,
      "shadow:",
      `  channel: ${candidateB}`,
      "destinations:",
      "  - name: archive",
      "    type: file",
      `    dir: ${join(dir, "out-b")}`,
      "",
    ].join("\n"),
  );

  const port = await freePort();
  const url = `http://127.0.0.1:${port}`;

  const { cert: signingCert, key: signingKey } = writeSigningPair(dir);

  const oidcConfig = join(dir, "oidc.yaml");
  writeFileSync(
    oidcConfig,
    [
      "issuer: https://login.example.invalid",
      "client_id: perfuse-e2e",
      "client_secret: e2e-client-secret",
      "redirect_url: https://perfuse.example.invalid/api/oidc/callback",
      "label: Sign in with Example",
      "roles:",
      "  admin:",
      "    - perfuse-admins",
      "  viewer:",
      "    - clinical-staff",
      "",
    ].join("\n"),
  );

  // SAML, configured so the third sign-on tab has something in it.
  //
  // The signing certificate is reused from the pair written above. It is not the certificate any real identity provider would use,
  // and it does not need to be: nothing here signs an assertion, and what these tests check is that the screen reads and writes the
  // file. A certificate that parses is enough, and one that does not would be refused at startup - which is the point of that check.
  const samlConfig = join(dir, "saml.yaml");
  writeFileSync(
    samlConfig,
    [
      "entity_id: https://perfuse.example.invalid",
      "acs_url: https://perfuse.example.invalid/auth/saml/acs",
      "idp_sso_url: https://login.example.invalid/saml/sso",
      `idp_cert_file: ${signingCert}`,
      "groups_attribute: groups",
      "label: Sign in with Example SAML",
      "roles:",
      "  admin:",
      "    - perfuse-admins",
      "  viewer:",
      "    - clinical-staff",
      "",
    ].join("\n"),
  );

  const ldapConfig = join(dir, "ldap.yaml");
  writeFileSync(
    ldapConfig,
    [
      "addr: dc01.example.invalid:636",
      "tls: true",
      "bind_dn: cn=perfuse,ou=services,dc=example,dc=invalid",
      "bind_password: e2e-bind-secret",
      "user_base_dn: ou=people,dc=example,dc=invalid",
      "username_attribute: uid",
      "member_of_attribute: memberOf",
      "roles:",
      "  perfuse-admins: admin",
      "",
    ].join("\n"),
  );

  const alertRules = join(dir, "alerts.yaml");
  writeFileSync(
    alertRules,
    [
      "rules:",
      "  - kind: error-rate",
      "    threshold: 0.1",
      "    for: 5m",
      "    severity: critical",
      "  - kind: queue-depth",
      "    channel: e2e-hl7",
      "    destination: archive",
      "    threshold: 250",
      "  - kind: below-rhythm",
      "    threshold: 0.8",
      "    disabled: true",
      "",
    ].join("\n"),
  );

  const proc: ChildProcess = spawn(
    join(REPO, "bin", "perfuse"),
    [
      "serve",
      "-channels", channels,
      "-db", join(dir, "perfuse.db"),
      "-addr", `127.0.0.1:${port}`,
      "-insecure",
      "-store-messages",

      // Sign-on files, so the sign-on editor has somewhere to save and its tests are not reaching a disabled button.
      //
      // Neither is a working provider or directory - there is nothing to point them at from a test harness - and that is the state
      // this screen is most often used in anyway: somebody is editing these files because sign-on is not working yet.
      "-oidc", oidcConfig,
      "-saml", samlConfig,
      "-ldap", ldapConfig,

      // An alert rules file, so the rules editor can actually save.
      //
      // Without it the server uses its built-in defaults and has nowhere to write, so the editor correctly says as much and every
      // test of saving reaches a disabled button - passing while testing nothing, which is the same trap the signing certificate
      // below was added to escape. The starting rules are deliberately ordinary, and one is disabled so that the read path has to
      // carry a field the form can quietly drop.
      "-alerts", alertRules,

      // A signing certificate, so the document signing path is actually exercised.
      //
      // Without it the signature tests reached a server with no key, took the "nothing to sign with" branch, and
      // passed while testing nothing. Kept separate from TLS because the fixture serves plain HTTP: signing and
      // terminating TLS are different jobs and tying them together would mean the only way to test one was to
      // reconfigure the other.
      "-signing-cert", signingCert,
      "-signing-key", signingKey,
    ],
    { cwd: REPO, stdio: ["ignore", "pipe", "pipe"] },
  );

  // The generated administrator password is printed once to stdout, deliberately not to the log so it is never swept into
  // an aggregator. So it has to be read here, as it streams.
  let out = "";
  let password = "";

  const ready = new Promise<void>((res, rej) => {
    const deadline = setTimeout(
      () => rej(new Error(`the server did not print credentials within 30s. Output so far:\n${out}`)),
      30_000,
    );

    proc.stdout?.on("data", (chunk) => {
      out += String(chunk);
      const m = out.match(/password:\s+(\S+)/);
      if (m && !password) {
        password = m[1];
        clearTimeout(deadline);
        res();
      }
    });
    proc.stderr?.on("data", (chunk) => {
      out += String(chunk);
    });

    // Everything the server says, kept for the whole run rather than only until it prints its password.
    //
    // Added while investigating one scattered failure per suite run. The server's output was accumulated during startup and then
    // discarded, so eighteen minutes of warnings - a slow query, a refused request, a retry - were invisible, and every failure had
    // to be diagnosed from the browser's side alone. A test that fails because the server was busy looks exactly like a test that
    // fails because the interface is wrong.
    const serverLog = createWriteStream(join(dir, "server.log"), { flags: "a" });
    proc.stdout?.pipe(serverLog);
    proc.stderr?.pipe(serverLog);
    proc.on("exit", (code) => {
      clearTimeout(deadline);
      rej(new Error(`the server exited with code ${code} before printing credentials:\n${out}`));
    });
  });

  await ready;

  // Wait for the HTTP surface, separately from the credential line. The password is printed during setup, before the
  // listener is necessarily accepting.
  const api = await request.newContext({ baseURL: url });
  let up = false;
  for (let i = 0; i < 60; i++) {
    try {
      const r = await api.get("/");
      if (r.status() < 500) {
        up = true;
        break;
      }
    } catch {
      // not listening yet
    }
    await new Promise((r) => setTimeout(r, 250));
  }
  await api.dispose();
  if (!up) {
    proc.kill("SIGKILL");
    rmSync(dir, { recursive: true, force: true });
    throw new Error(`the server never answered on ${url}. Output:\n${out}`);
  }

  // Sign in through the real login endpoint and keep the cookie, so tests do not each repeat it. The custom header is
  // required by the API to prove the request is not a cross-site form post.
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ baseURL: url });
  const res = await ctx.request.post("/api/login", {
    headers: { "X-Perfuse-Request": "1" },
    data: { username: "admin", password },
  });
  if (!res.ok()) {
    await browser.close();
    proc.kill("SIGKILL");
    throw new Error(`signing in failed with ${res.status()}: ${await res.text()}`);
  }

  // The fixture channel must actually have loaded.
  //
  // Checked because it silently had not: the file was invalid, the server refused it, and the whole suite ran against an empty
  // install for a day. A fixture that fails quietly is worse than no fixture, because every test that depended on it still passes and
  // reports that it covered something it did not.
  const status = await ctx.request.get("/api/status", { headers: { "X-Perfuse-Request": "1" } });
  if (!status.ok()) {
    await browser.close();
    proc.kill("SIGKILL");
    throw new Error(`could not read status to verify the fixture: ${status.status()}`);
  }

  // Every fixture channel, by name and by count.
  //
  // Counting was not enough. A channel is refused wholesale when anything it references is wrong, so an
  // invalid contract file made the labs channel disappear entirely - and with two other channels present
  // the count still looked healthy while the one every test depends on was gone. Naming them means a
  // fixture that breaks says which part broke.
  const loaded = (await status.json()) as {
    channelsTotal?: number;
    channels?: { name: string }[];
  };
  const names = (loaded.channels ?? []).map((c) => c.name);
  const wanted = ["labs", "shadowed", "shadowed-candidate", "shadowed-b", "shadowed-candidate-b"];
  const absent = wanted.filter((w) => !names.includes(w));

  if (absent.length > 0 || !loaded.channelsTotal) {
    await browser.close();
    proc.kill("SIGKILL");
    throw new Error(
      `these fixture channels did not load: ${absent.join(", ") || "(none named, but the count is " + loaded.channelsTotal + ")"}. ` +
        `Loaded: ${names.join(", ") || "nothing"}. ` +
        `A channel is refused wholesale when anything it references is invalid, so check the contract and ` +
        `shadow files as well as the channel itself. The tests would otherwise run against a partly empty ` +
        `install, which is what this fixture exists to prevent.\nServer output:\n${out}`,
    );
  }

  mkdirSync(join(here, ".auth"), { recursive: true });
  await ctx.storageState({ path: join(here, ".auth", "admin.json") });
  await browser.close();

  process.env.PERFUSE_E2E_URL = url;
  // So a test can send the fixture channel a real message and exercise the views that need one.
  process.env.PERFUSE_E2E_MLLP = String(mllpPort);
  // The shadowed channel, so a test can put traffic through it and see the comparison.
  process.env.PERFUSE_E2E_SHADOW_MLLP = String(shadowPort);
  // The second shadowed channel, so a test can switch between them and see the report follow.
  process.env.PERFUSE_E2E_SHADOW_MLLP_B = String(shadowPortB);

  // Recorded for the teardown, which runs in a separate process and cannot see these values.
  writeFileSync(STATE_FILE, JSON.stringify({ pid: proc.pid, dir, url }, null, 2));

  // Detach so this process exiting does not take the server with it before the tests run.
  proc.unref();

  console.log(`  perfuse serving on ${url} (pid ${proc.pid}), data in ${dir}`);
}


/**
 * writeSigningPair generates a certificate for the fixture to sign documents with.
 *
 * Generated per run rather than checked in. A committed certificate expires, and then a test suite starts failing for
 * a reason that has nothing to do with the code - which is how a fixture becomes something nobody trusts.
 */
function writeSigningPair(dir: string): { cert: string; key: string } {
  const certPath = join(dir, "signing-cert.pem");
  const keyPath = join(dir, "signing-key.pem");

  execFileSync("openssl", [
    "req", "-x509", "-newkey", "rsa:2048", "-nodes",
    "-keyout", keyPath,
    "-out", certPath,
    "-days", "2",
    "-subj", "/CN=perfuse-e2e-signer/O=Example Hospital",
  ], { stdio: "ignore" });

  return { cert: certPath, key: keyPath };
}
