import { expect, test } from "@playwright/test";
import { openTab } from "./nav";

/**
 * The lock-in audit, which is the migration scan asked a different question.
 *
 * Everything else on the migration screen answers "what must I rewrite to move to Perfuse", which only interests somebody who has
 * already decided to look at Perfuse. This answers "how much of this only runs on one vendor's software", which a site wants answered
 * before it has any opinion about Perfuse - and it runs against a channel export, so it needs nothing installed.
 *
 * The property under test is honesty in both directions. A channel full of ordinary Java must be reported as portable, and a channel
 * with vendor calls must have them named rather than counted. A tool that inflated its findings would be checked and discarded.
 */

/** A Mirth export whose scripts use ordinary Java only. */
function portableExport() {
  return `<channel version="4.5.2">
  <id>7d3f1c22-0000-4000-8000-00000000aa01</id>
  <name>Portable Feed</name>
  <enabled>true</enabled>
  <sourceConnector version="4.5.2">
    <name>sourceConnector</name>
    <transportName>TCP Listener</transportName>
    <properties class="com.mirth.connect.connectors.tcp.TcpReceiverProperties" version="4.5.2">
      <listenerConnectorProperties version="4.5.2">
        <host>0.0.0.0</host>
        <port>7801</port>
      </listenerConnectorProperties>
    </properties>
    <transformer version="4.5.2">
      <elements>
        <com.mirth.connect.plugins.javascriptstep.JavaScriptStep>
          <sequenceNumber>0</sequenceNumber>
          <name>stamp</name>
          <script>var f = new java.text.SimpleDateFormat('yyyyMMdd'); var m = new java.util.HashMap();</script>
        </com.mirth.connect.plugins.javascriptstep.JavaScriptStep>
      </elements>
    </transformer>
  </sourceConnector>
</channel>`;
}

/** A Mirth export whose script calls the vendor's own server. */
function lockedInExport() {
  return `<channel version="4.5.2">
  <id>7d3f1c22-0000-4000-8000-00000000aa02</id>
  <name>Locked Feed</name>
  <enabled>true</enabled>
  <sourceConnector version="4.5.2">
    <name>sourceConnector</name>
    <transportName>TCP Listener</transportName>
    <properties class="com.mirth.connect.connectors.tcp.TcpReceiverProperties" version="4.5.2">
      <listenerConnectorProperties version="4.5.2">
        <host>0.0.0.0</host>
        <port>7802</port>
      </listenerConnectorProperties>
    </properties>
    <transformer version="4.5.2">
      <elements>
        <com.mirth.connect.plugins.javascriptstep.JavaScriptStep>
          <sequenceNumber>0</sequenceNumber>
          <name>vendor</name>
          <script>var c = com.mirth.connect.server.controllers.ChannelController.getInstance();</script>
        </com.mirth.connect.plugins.javascriptstep.JavaScriptStep>
      </elements>
    </transformer>
  </sourceConnector>
</channel>`;
}

async function analyse(page: import("@playwright/test").Page, xml: string) {
  page.on("pageerror", (e) => console.log("PAGEERROR:", e.message));

  await page.goto("/");
  await openTab(page, "Migrate");

  // The paste box is inside a collapsed disclosure, because the primary path is dropping a file.
  await page.getByText("or paste the XML instead").click();
  await page.locator("textarea").first().fill(xml);
  await page.getByRole("button", { name: "Analyse" }).click();

  // Waited for explicitly. Without this the first assertion raced the analysis and reported the paste box as the whole page, which
  // reads as "the feature is missing" rather than "the request has not come back".
  await expect(page.getByText("What came across")).toBeVisible({ timeout: 30_000 });
}

test("ordinary Java is reported as portable rather than as lock-in", async ({ page }) => {
  await analyse(page, portableExport());

  const main = page.locator("body");

  // Expanded only if it is not already. A card with findings opens itself, and clicking it then closes it - which hid the verdict
  // and reported it as missing.
  await expect(main).toContainText(/Portable Feed/, { timeout: 20_000 });

  if (!(await main.textContent())?.includes("Portability")) {
    await page.getByRole("button", { name: /Portable Feed/ }).click();
  }

  await expect(main).toContainText(/Nothing here is locked in/i, { timeout: 20_000 });

  // And no badge. A zero shown as a measurement invites reading the absence of a problem as a small amount of one.
  await expect(main.getByText("vendor-only")).toHaveCount(0);
});

test("a vendor call is named, not just counted", async ({ page }) => {
  await analyse(page, lockedInExport());

  const main = page.locator("body");

  // The badge is the headline; the name is the evidence. A count on its own is an assertion somebody cannot check, and the first
  // thing an engineer does with a claim like this is go and look.
  // Present rather than visible. The count badges on this screen are deliberately hidden below the small breakpoint, so asserting
  // visibility would make this test depend on the viewport rather than on the finding.
  await expect(main.getByText("vendor-only")).toHaveCount(1, { timeout: 20_000 });

  if (!(await main.textContent())?.includes("Portability")) {
    await page.getByRole("button", { name: /Locked Feed/ }).click();
  }

  await expect(main).toContainText(/run only on the vendor's own server/i, { timeout: 20_000 });
  await expect(main).toContainText(/com\.mirth\.connect/, { timeout: 20_000 });

  // The qualification has to be present in the same breath. Without it the number reads as the whole finding, which is the
  // dishonest version of this feature.
  await expect(main, "the verdict does not say what is not lock-in").toContainText(/not lock-in/i);
});
