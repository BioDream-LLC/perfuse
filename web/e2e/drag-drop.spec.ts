import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

/**
 * Drops a file on the migration zone, which is the only drag-and-drop target in the application.
 *
 * # Why it was worth writing
 *
 * The zone was completely untested. Its sibling path - choosing the same file through the file picker - has specs, and those specs
 * pass whether the drop handler works, is wired to the wrong element, or was deleted. The two paths share nothing except the parsing
 * that happens after a file has been read, so coverage of one says nothing about the other.
 *
 * That matters more here than for most controls, because dropping a file is the advertised way in. The panel says "Drop your channel
 * export here" in the largest text on the screen, and a migration is somebody's first ten minutes with Perfuse.
 *
 * # How a drop is simulated
 *
 * Playwright has no drop-a-file-from-the-desktop primitive, so the DataTransfer is built in the page and dispatched. This is closer to
 * the real thing than it looks: the browser's own drop produces the same event with the same DataTransfer, and everything the
 * application does happens in the handler being invoked here.
 */

const MIRTH_CHANNEL = `<channel version="4.5.2">
  <id>c0ffee00-dead-beef-cafe-000000000001</id>
  <name>Dropped ADT Feed</name>
  <enabled>true</enabled>
  <sourceConnector version="4.5.2">
    <name>sourceConnector</name>
    <transportName>TCP Listener</transportName>
    <mode>SOURCE</mode>
    <enabled>true</enabled>
    <properties class="com.mirth.connect.connectors.tcp.TcpReceiverProperties" version="4.5.2">
      <listenerConnectorProperties version="4.5.2">
        <host>0.0.0.0</host>
        <port>6669</port>
      </listenerConnectorProperties>
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

test("a channel export dropped on the migration zone is read", async ({ page }) => {
  const faults: string[] = [];
  page.on("pageerror", (e) => faults.push(e.message));

  await page.goto("/");
  await openTab(page, "Migrate");

  const zone = page.getByText("Drop your channel export here");
  await expect(zone, "the drop zone is not on the migration screen").toBeVisible();

  // The element carrying the handlers is the bordered container, not the paragraph the text sits in.
  const target = zone.locator("xpath=ancestor::div[contains(@class,'border-dashed')][1]");
  await expect(target).toBeVisible();

  await target.evaluate((el, xml) => {
    const dt = new DataTransfer();
    dt.items.add(new File([xml], "adt_channel.xml", { type: "text/xml" }));

    el.dispatchEvent(new DragEvent("dragover", { bubbles: true, cancelable: true, dataTransfer: dt }));
    el.dispatchEvent(new DragEvent("drop", { bubbles: true, cancelable: true, dataTransfer: dt }));
  }, MIRTH_CHANNEL);

  // The name out of the dropped file, which can only be on screen if the drop was read and parsed. Asserting the zone still exists,
  // or that some panel appeared, would pass with a handler that read nothing.
  // Filtered to what is on screen, because the name also lands in an option of the "which channel to prove" select and an option is
  // never visible. Taking the first match found that one and reported the feature broken while it worked perfectly - the drop is
  // fine, the assertion was not.
  await expect(
    page.getByText("Dropped ADT Feed").locator("visible=true").first(),
    "the file was dropped and the channel name never appeared, so the drop handler read nothing",
  ).toBeVisible({ timeout: 20_000 });

  // The name Perfuse would give it, which is derived rather than copied - the summary lowercases and hyphenates what it read. This
  // is asserted instead of the listener port, which the summary does not show: an earlier version looked for the port, failed, and
  // said the export "was not really parsed" when the truth was that this screen reports readiness and not connector detail. An
  // assertion that names a thing the screen never claimed to show is a bug in the test, not a finding.
  await expect(
    page.getByText("renamed to dropped-adt-feed").locator("visible=true").first(),
    "the channel name was read but no Perfuse name was derived from it, so the export was recognised and not understood",
  ).toBeVisible({ timeout: 20_000 });

  // And that it judged the channel runnable, which it can only do by reading the connectors inside the file.
  await expect(
    page.getByText("channel will run on Perfuse").locator("visible=true").first(),
    "no verdict was reached on whether the dropped channel would run",
  ).toBeVisible({ timeout: 20_000 });

  expect(faults, `dropping a file produced errors:\n${faults.join("\n")}`).toEqual([]);
});

test("dropping something that is not a channel says so rather than failing silently", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Migrate");

  const zone = page.getByText("Drop your channel export here");
  const target = zone.locator("xpath=ancestor::div[contains(@class,'border-dashed')][1]");

  await target.evaluate((el) => {
    const dt = new DataTransfer();
    dt.items.add(new File(["this is not xml at all"], "notes.txt", { type: "text/plain" }));

    el.dispatchEvent(new DragEvent("dragover", { bubbles: true, cancelable: true, dataTransfer: dt }));
    el.dispatchEvent(new DragEvent("drop", { bubbles: true, cancelable: true, dataTransfer: dt }));
  });

  // Silence is the failure being guarded against. Somebody who drops the wrong file and sees nothing happen cannot tell that from a
  // broken drop zone, and will reasonably conclude the feature does not work.
  //
  // The same wording the paste path is held to, deliberately. Both routes end in the same analyse call, so the interesting question
  // is not whether an error exists but whether dropping a file reaches the same refusal as pasting one - a drop that swallowed the
  // error would still show a spinner settling and look like it had simply found nothing to say.
  await expect(page.locator("main"), "a file that is not a channel export was dropped and nothing was said about it").toContainText(
    /no Mirth channel was found/i,
    { timeout: 30_000 },
  );
});
