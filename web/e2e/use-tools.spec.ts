import { test, expect } from "@playwright/test";
import { openTab } from "./nav";
import { sendMLLP, anADT } from "./mllp";

// Using the migration, mapping and observation sections.
//
// Several of these are read-only, which makes them easy to leave untested: there is no button to press,
// so a test that loads the tab looks like coverage. It is not. What each one claims is that it read
// something and reached a conclusion, and the conclusion is the part worth asserting.

function watch(page: import("@playwright/test").Page): string[] {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`page error: ${e}`));
  page.on("console", (m) => {
    if (m.type() === "error") problems.push(`console: ${m.text()}`);
  });
  return problems;
}

/** A Mirth channel export, small but real, so the migration has something to read. */
const mirthChannel = `<channel version="4.5.2">
  <id>e2e-1111-2222-3333</id>
  <name>ADT Inbound E2E</name>
  <description>Receives ADT from the HIS</description>
  <enabled>true</enabled>
  <sourceConnector version="4.5.2">
    <name>sourceConnector</name>
    <transportName>TCP Listener</transportName>
    <mode>SOURCE</mode>
    <enabled>true</enabled>
    <properties class="com.mirth.connect.connectors.tcp.TcpReceiverProperties" version="4.5.2">
      <listenerConnectorProperties version="4.5.2">
        <host>0.0.0.0</host>
        <port>6661</port>
      </listenerConnectorProperties>
      <transmissionModeProperties class="com.mirth.connect.model.transmission.framemode.FrameModeProperties">
        <pluginPointName>MLLP</pluginPointName>
      </transmissionModeProperties>
    </properties>
    <transformer version="4.5.2">
      <elements/>
      <inboundDataType>HL7V2</inboundDataType>
      <outboundDataType>HL7V2</outboundDataType>
    </transformer>
    <filter version="4.5.2">
      <elements/>
    </filter>
  </sourceConnector>
  <destinationConnectors>
    <connector version="4.5.2">
      <name>Archive</name>
      <transportName>File Writer</transportName>
      <mode>DESTINATION</mode>
      <enabled>true</enabled>
      <properties class="com.mirth.connect.connectors.file.FileDispatcherProperties" version="4.5.2">
        <host>/var/log/archive</host>
        <outputPattern>adt.txt</outputPattern>
      </properties>
      <transformer version="4.5.2">
        <elements/>
        <inboundDataType>HL7V2</inboundDataType>
        <outboundDataType>HL7V2</outboundDataType>
      </transformer>
      <filter version="4.5.2">
        <elements/>
      </filter>
    </connector>
  </destinationConnectors>
</channel>`;

test("the migration reads a Mirth channel and says what would come across", async ({ page }) => {
  const problems = watch(page);

  await page.goto("/");
  await openTab(page, "Migrate");

  // The paste route, because a file chooser needs a file on disk and the point here is the analysis.
  await page.locator("main summary").filter({ hasText: /paste the XML/ }).click();
  await page.getByLabel("Mirth channel XML", { exact: true }).fill(mirthChannel);
  await page.getByRole("button", { name: "Analyse", exact: true }).click();

  // The verdict, which is the whole feature: how much of this export will run.
  await expect(page.locator("main"), "the export was read but no verdict appeared").toContainText(
    /will run on Perfuse/,
    { timeout: 30_000 },
  );

  // And the channel itself, by the name it has in Mirth.
  await expect(page.locator("main")).toContainText("ADT Inbound E2E", { timeout: 15_000 });

  // Expand it and read the channel it would create. A migration tool that will not show its output is
  // asking to be trusted rather than checked.
  await page.locator("main button").filter({ hasText: "ADT Inbound E2E" }).first().click();
  const show = page.locator("main summary").filter({ hasText: /Show the channel this would create/ });
  await expect(show, "the generated channel cannot be inspected").toBeVisible({ timeout: 15_000 });
  await show.click();
  await expect(page.locator("main"), "the generated channel is empty").toContainText("name:", {
    timeout: 15_000,
  });

  expect(problems, problems.join("\n")).toEqual([]);
});

test("the migration refuses something that is not a Mirth export, and says so", async ({ page }) => {
  const problems = watch(page);

  await page.goto("/");
  await openTab(page, "Migrate");

  await page.locator("main summary").filter({ hasText: /paste the XML/ }).click();
  await page.getByLabel("Mirth channel XML", { exact: true }).fill("<not-a-channel>hello</not-a-channel>");
  await page.getByRole("button", { name: "Analyse", exact: true }).click();

  // Refusing is correct. Saying nothing is not, because the operator is left wondering whether it
  // worked.
  await expect(page.locator("main"), "unreadable XML was accepted silently").toContainText(
    /no Mirth channel was found/i,
    { timeout: 30_000 },
  );

  expect(problems.filter((p) => !/40[03]|Failed to load resource/.test(p))).toEqual([]);
});

test("the mapper suggests mappings and abstains rather than guessing", async ({ page }) => {
  const problems = watch(page);

  await page.goto("/");
  await openTab(page, "AI Mapper");

  // The defaults include a field with no plausible target, which is the interesting case: the tool is
  // supposed to abstain rather than offer something wrong.
  await page.getByRole("button", { name: "Suggest mappings" }).click();

  await expect(page.locator("main"), "the mapper returned nothing at all").toContainText(
    /PatientMRN|abstained|No suggestions/,
    { timeout: 30_000 },
  );

  // A confident mapping for the obvious pair. Asserted on the reasoning rather than on the field name,
  // because the field names are the default contents of the two textareas - my first version of this
  // matched those and passed while the request behind it was being refused with a 403.
  await expect(
    page.locator("main"),
    "the mapper produced no reasoning, so nothing was actually suggested",
  ).toContainText(/confidence|match|name|abstained/i, { timeout: 20_000 });

  // Raising the threshold must reduce what it will commit to, not leave it unchanged.
  const before = await page.locator("main").textContent();
  await page.getByLabel(/Confidence threshold/i).fill("95");
  await page.getByRole("button", { name: "Suggest mappings" }).click();
  await expect(page.locator("main")).not.toHaveText("", { timeout: 20_000 });
  const after = await page.locator("main").textContent();
  expect(
    after,
    "raising the confidence threshold to 95 changed nothing, so the slider does nothing",
  ).not.toBe(before);

  // And a pair with no sane answer must not be given one.
  await page.getByLabel("Source fields (one per line)", { exact: true }).fill("DateOfBirth");
  await page.getByLabel("Target fields (one per line)", { exact: true }).fill("PID-29");
  await page.getByLabel(/Confidence threshold/i).fill("70");
  await page.getByRole("button", { name: "Suggest mappings" }).click();

  // PID-29 is the date of death. Offering date of birth for it is the defect this abstention exists to
  // prevent, and it was a real one.
  await expect(page.locator("main"), "date of birth was mapped to date of death").toContainText(
    /abstained|No suggestions/,
    { timeout: 20_000 },
  );

  expect(problems, problems.join("\n")).toEqual([]);
});

test("contracts and tables report what they are watching", async ({ page }) => {
  const problems = watch(page);

  await page.goto("/");

  await openTab(page, "Contracts");

  // The fixture puts a contract on the labs channel, so this is a test of the feature rather than of the
  // sentence "No feed has a contract yet" - which is all it could assert before.
  await expect(page.locator("main"), "the contract on labs is not being watched").toContainText(
    /channel watched|channels watched/,
    { timeout: 25_000 },
  );
  await expect(page.locator("main")).toContainText("labs", { timeout: 15_000 });

  // And it must say where the feed stands: holding, changed, or not enough traffic to judge. A count with
  // no verdict leaves somebody none the wiser.
  await expect(
    page.locator("main"),
    "the contract is listed without saying whether it holds",
  ).toContainText(/As expected|Not enough messages|Changed since/, { timeout: 15_000 });

  await openTab(page, "Tables");
  await expect(page.locator("main")).toContainText(
    /Shared mapping tables|No shared tables yet/,
    { timeout: 20_000 },
  );

  // If a table exists, the usage list must open, since knowing what an edit affects is the point.
  const uses = page.locator("main button").filter({ hasText: /Show the \d+ place/ });
  if (await uses.count()) {
    await uses.first().click();
    await expect(page.locator("main button").filter({ hasText: /Hide the \d+ place/ })).toBeVisible({
      timeout: 10_000,
    });
  }

  expect(problems, problems.join("\n")).toEqual([]);
});

test("the flow map draws traffic and explains a channel", async ({ page }) => {
  const problems = watch(page);

  // Traffic first, or the map is an empty grid and every assertion is about the empty state.
  for (let i = 0; i < 3; i++) await sendMLLP(anADT(`FLOW${i}`));

  await page.goto("/");
  await openTab(page, "Flow map");

  await expect(page.locator("main")).toContainText("labs", { timeout: 25_000 });

  // Each window, since each is a different query with a different bucket size.
  for (const w of ["Last hour", "Last 6 hours", "Last 24 hours", "Last 7 days"]) {
    await page.getByRole("button", { name: w }).click();
    await expect(page.locator("main")).toContainText("labs", { timeout: 20_000 });
  }

  // The scrubber, which is the control that makes this a map over time rather than a snapshot.
  const scrubber = page.getByLabel("Moment shown", { exact: true });
  if (await scrubber.count()) {
    const max = await scrubber.getAttribute("max");
    await scrubber.fill(String(Math.floor(Number(max ?? 1) / 2)));
    await expect(page.getByRole("button", { name: "Back to live" })).toBeVisible({ timeout: 15_000 });
    await page.getByRole("button", { name: "Back to live" }).click();
    await expect(page.getByRole("button", { name: "Live" })).toBeVisible({ timeout: 15_000 });
  }

  // Selecting a strand must explain what it does, in sentences. That narration is the feature.
  const strand = page.locator("main button").filter({ hasText: "labs" }).first();
  await strand.click();
  await expect(page.locator("main"), "selecting a channel explained nothing").toContainText(
    /What labs does/,
    { timeout: 15_000 },
  );

  expect(problems, problems.join("\n")).toEqual([]);
});

test("fleet, shadow and certificates each state a conclusion", async ({ page }) => {
  const problems = watch(page);

  await page.goto("/");

  await openTab(page, "Fleet");
  // One instance is the normal case here, and it must say so rather than look broken.
  await expect(page.locator("main")).toContainText(/instance|reachable|Add another/i, {
    timeout: 20_000,
  });

  await openTab(page, "Shadow");
  await expect(page.locator("main")).toContainText(
    /Nothing is being shadowed|Compared|verdict/i,
    { timeout: 20_000 },
  );

  await openTab(page, "Certificates");
  await expect(page.locator("main")).toContainText(
    /No endpoint is using TLS|days left|expired/i,
    { timeout: 20_000 },
  );

  expect(problems, problems.join("\n")).toEqual([]);
});
