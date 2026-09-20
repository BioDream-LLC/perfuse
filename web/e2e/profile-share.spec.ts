import { expect, test } from "@playwright/test";
import { openTab } from "./nav";
import { admitMessage, freePort, sendMLLPTo } from "./mllpchannel";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

/** feedWithMessages creates a channel and sends it real traffic, so there is something to profile.
 *
 * A profile of an empty channel is correctly refused - "no messages have been recorded yet" - so a test that shares a profile has to
 * produce one first. Reusing the MLLP helpers rather than writing new ones, because two ways of sending a message into this server
 * would drift and one of them would be wrong. */
async function feedWithMessages(page: import("@playwright/test").Page): Promise<string> {
  const port = await freePort();
  const name = `e2e-profile-${Date.now().toString(36)}`;
  const outDir = mkdtempSync(join(tmpdir(), "perfuse-profile-"));

  const yaml = [
    `name: ${name}`,
    "source:",
    "  type: mllp",
    `  listen: 127.0.0.1:${port}`,
    "destinations:",
    "  - name: archive",
    "    type: file",
    `    dir: ${outDir}`,
    "",
  ].join("\n");

  const created = await page.request.post("/api/channels", {
    headers: { "X-Perfuse-Request": "1" },
    data: { yaml },
  });
  expect(created.ok(), `creating the channel failed: ${await created.text()}`).toBe(true);

  const started = await page.request.post(`/api/channels/${name}/start`, {
    headers: { "X-Perfuse-Request": "1" },
  });
  expect(started.ok(), `starting the channel failed: ${await started.text()}`).toBe(true);

  // The listener binds asynchronously, so connecting immediately races it.
  let sent = false;
  for (let attempt = 0; attempt < 20 && !sent; attempt++) {
    try {
      await sendMLLPTo(port, admitMessage(`PROF${attempt}`));
      sent = true;
    } catch {
      await new Promise((r) => setTimeout(r, 500));
    }
  }
  expect(sent, "no message could be delivered, so there is nothing to profile").toBe(true);

  return name;
}

/**
 * Sharing a dialect profile.
 *
 * internal/profile/share.go held ExportProfile, MarshalProfile and UnmarshalProfile with a versioned format for some time, and nothing
 * called any of them. The queue described this as unbuilt when what was missing was two handlers and a form - which is the shape this
 * project's own defect list names: an implementation with no caller is indistinguishable from a feature that does not exist.
 */

test("a profile can be named and downloaded, and refuses to go out unnamed", async ({ page }) => {
  const channel = await feedWithMessages(page);

  await page.goto("/");
  await openTab(page, "Channels");

  // The profiler is opened from a channel, because a profile is of a feed rather than of the server.
  // The card is found through its heading rather than by position, because the channel list is sorted and this one's place in it
  // depends on every other channel the suite has created.
  const card = page.locator(".card").filter({ has: page.getByRole("heading", { name: channel, exact: true }) });

  await expect(card).toHaveCount(1, { timeout: 20_000 });
  await card.getByRole("button", { name: "Feed", exact: true }).click();

  // The profile has to be read before it can be shared. Sharing is offered with the report rather than beside the button, because
  // the moment somebody wants to share one is the moment they are looking at it.
  await page.getByRole("button", { name: /Profile this feed/ }).click();

  const name = page.getByRole("textbox", { name: "Name" }).first();
  const source = page.getByRole("textbox", { name: /Which system produced it/ });
  const download = page.getByRole("button", { name: "Download profile" });

  await expect(name).toBeVisible({ timeout: 20_000 });

  // Refused by the control until both are given. The sending system is required because the question somebody else asks of a shared
  // profile is whether it describes their system, and an optional field makes "Unknown" the commonest answer in a shared library.
  await expect(download).toBeDisabled();

  await name.fill("Epic ADT feed");
  await expect(download, "a profile with no sending system could be shared").toBeDisabled();

  await source.fill("Epic 2023");
  await expect(download).toBeEnabled();

  // The download itself, which is the outcome. A form that accepts a name and produces no file is what this asserts against.
  const [saved] = await Promise.all([page.waitForEvent("download", { timeout: 30_000 }), download.click()]).catch(
    async (e) => {
      // The refusal, if there was one, is on screen. Reported here because "no download appeared" is the same symptom whether the
      // button did nothing or the server declined, and those need different fixes.
      const shown = (await page.locator("body").textContent()) ?? "";
      const note = shown.includes("profile") && shown.match(/[A-Z][^.]*(refus|not be|invalid|error)[^.]*\./i);

      throw new Error(`${String(e)}${note ? `\n\nthe screen said: ${note[0]}` : "\n\nthe screen showed no refusal"}`);
    },
  );

  expect(saved.suggestedFilename(), "the downloaded file is not named as a profile").toMatch(/\.profile\.json$/);
});

test("a shared profile says it carries no patient data, and why that is checked", async ({ page }) => {
  const channel = await feedWithMessages(page);

  await page.goto("/");
  await openTab(page, "Channels");

  // The card is found through its heading rather than by position, because the channel list is sorted and this one's place in it
  // depends on every other channel the suite has created.
  const card = page.locator(".card").filter({ has: page.getByRole("heading", { name: channel, exact: true }) });

  await expect(card).toHaveCount(1, { timeout: 20_000 });
  await card.getByRole("button", { name: "Feed", exact: true }).click();

  await page.getByRole("button", { name: /Profile this feed/ }).click();

  // Stated on the screen rather than only in the manual. Somebody about to send a file describing a hospital's live feed to a
  // stranger should be told what is in it at that moment, not invited to go and read about it.
  const main = page.locator("body");

  await expect(main).toContainText(/carries no patient data/i, { timeout: 20_000 });
  await expect(main, "the screen does not say the export verifies it rather than assuming it").toContainText(
    /refuses if that is ever not true/i,
  );
});
