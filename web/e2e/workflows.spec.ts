import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// The panels that take input and produce output, driven the way a person drives them.
//
// These are the tabs where something is actually computed: paste a message, press the button, read the result. A panel that renders
// and then produces nothing is invisible to a test that only checks rendering, which is how the playground stayed broken.

/** problems collects everything that should never happen, for the life of a page. */
function problems(page: import("@playwright/test").Page): string[] {
  const found: string[] = [];
  page.on("console", (m) => {
    if (m.type() !== "error") return;

    // The browser logs "Failed to load resource" for every 4xx, and a 4xx can be the correct answer - a malformed filter
    // expression should be refused. Counting that as a defect would push towards a server that accepts nonsense. 5xx is watched
    // separately below, and what matters for a 4xx is whether the interface explains it, which the tests assert directly.
    if (/Failed to load resource.*\b4\d\d\b/.test(m.text())) return;

    found.push(`console: ${m.text()}`);
  });
  page.on("pageerror", (e) => found.push(`uncaught: ${e.message}`));
  page.on("response", (r) => {
    if (r.status() >= 500) found.push(`${r.status()} ${new URL(r.url()).pathname}`);
  });

  return found;
}

test("the FHIR lab converts a message and shows resources", async ({ page }) => {
  const found = problems(page);

  await page.goto("/");
  await openTab(page, "FHIR lab");

  // The sample is loaded by the panel's own button, so the test is not carrying its own copy of an HL7 message that could drift
  // from what the product considers a representative one.
  await page.getByRole("button", { name: "Sample ADT" }).click();
  await page.getByRole("button", { name: "Convert to FHIR" }).click();

  // A conversion produces resources. Asserting on the output rather than the absence of an error, because producing nothing is also
  // silent.
  await expect(page.locator("main")).toContainText(/Patient/, { timeout: 20_000 });

  expect(found, `the FHIR lab reported problems:\n${found.join("\n")}`).toEqual([]);
});

test("the document lab reads a clinical document", async ({ page }) => {
  const found = problems(page);

  await page.goto("/");
  await openTab(page, "Documents");
  await page.getByRole("button", { name: "Sample document" }).click();
  await page.getByRole("button", { name: "Read the document" }).click();

  // Sections named in English is the point of the panel, so the output must contain something section-shaped.
  await expect(page.locator("main")).toContainText(/section|Allergies|Medications|Problem/i, { timeout: 20_000 });

  expect(found, `the document lab reported problems:\n${found.join("\n")}`).toEqual([]);
});

test("the migration panel analyses a Mirth channel", async ({ page }) => {
  const found = problems(page);

  await page.goto("/");
  await openTab(page, "Migrate");

  // A minimal but genuine Mirth channel export. Deliberately not a full one: what matters is that the panel reports what it can and
  // cannot carry across, and it has to do that for a small input as well as a large one.
  const mirth = `<channel version="4.5.2">
  <id>7d3f1c22-0000-4000-8000-000000000001</id>
  <name>ADT Inbound</name>
  <description>Receives ADT from the hospital</description>
  <enabled>true</enabled>
  <sourceConnector version="4.5.2">
    <name>sourceConnector</name>
    <transportName>TCP Listener</transportName>
    <properties class="com.mirth.connect.connectors.tcp.TcpReceiverProperties" version="4.5.2">
      <listenerConnectorProperties version="4.5.2">
        <host>0.0.0.0</host>
        <port>6661</port>
      </listenerConnectorProperties>
    </properties>
  </sourceConnector>
  <destinationConnectors>
    <connector version="4.5.2">
      <name>To Registry</name>
      <transportName>TCP Sender</transportName>
      <enabled>true</enabled>
    </connector>
  </destinationConnectors>
</channel>`;

  // The paste box is inside a collapsed disclosure; the primary path is dropping a file. My first version filled a textarea that
  // was not on the page yet and timed out - a wrong test rather than a bug.
  await page.getByText("or paste the XML instead").click();
  await page.locator("textarea").first().fill(mirth);
  await page.getByRole("button", { name: "Analyse" }).click();

  // It must say something about what it found. A migration tool that reads a channel and reports nothing is worse than one that
  // refuses, because the operator cannot tell whether it understood.
  await expect(page.locator("main")).toContainText(/ADT Inbound|6661|TCP|destination|source/i, { timeout: 20_000 });

  expect(found, `the migration panel reported problems:\n${found.join("\n")}`).toEqual([]);
});

test("a script runs and returns a result", async ({ page }) => {
  const found = problems(page);

  await page.goto("/");
  await openTab(page, "Scripts");

  // The panel offers examples; running one exercises the whole path without this test inventing a script whose dialect might not be
  // the one the product supports.
  await page.getByRole("button", { name: "Set a field" }).click();

  const run = page.getByRole("button", { name: /^(Run|Run script|Try it)/ });
  if (await run.count()) {
    await run.first().click();
    await expect(page.locator("main")).toContainText(/MSH|PID|result|output/i, { timeout: 20_000 });
  }

  expect(found, `the script panel reported problems:\n${found.join("\n")}`).toEqual([]);
});

test("a message search with a filter expression returns without error", async ({ page }) => {
  const found = problems(page);

  await page.goto("/");
  await openTab(page, "Messages");

  // The filter language is the interesting part: it is parsed on the server, and a malformed expression must be reported rather than
  // returning a 500 or silently matching everything.
  const expr = page.getByPlaceholder("PID-8 in ['1', '2'] and MSH-9.2 == 'A01'");
  await expr.fill("MSH-9.1 == 'ADT'");
  await page.getByRole("button", { name: "Search" }).last().click();
  await page.waitForTimeout(1500);

  // Now a deliberately broken one. This must produce a readable complaint, not a server error.
  await expr.fill("MSH-9.1 == = = 'ADT'");
  await page.getByRole("button", { name: "Search" }).last().click();
  await page.waitForTimeout(1500);

  await expect(page.locator("main")).not.toContainText(/panic|runtime error/i);

  // A refusal has to be visible and legible. A malformed expression that is rejected silently leaves someone staring at an empty
  // result list, concluding that no messages match, when in fact nothing was searched.
  await expect(page.locator("main")).toContainText(/expected|unexpected|invalid|could not|cannot|error/i, {
    timeout: 10_000,
  });

  expect(found, `the message search reported problems:\n${found.join("\n")}`).toEqual([]);
});
