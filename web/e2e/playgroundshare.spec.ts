import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// A shared playground link must reproduce what the sender was looking at, and must not put the message anywhere it does not belong.
//
// Driven rather than unit tested, because the unit tests cover the encoding and this covers the claim: paste the link, see the message.
// Those are different claims and only the second one is the feature.
test("a playground link reproduces the session it was copied from", async ({ page }) => {
  test.setTimeout(120_000);

  const requests: string[] = [];
  page.on("request", (r) => requests.push(r.url()));

  await page.goto("/");
  await openTab(page, "Playground");

  // Wait for the engine, since the controls only exist once the module has instantiated.
  await expect(page.getByRole("button", { name: "Try a filter" })).toBeVisible({ timeout: 60_000 });

  // A message with an accented name, because btoa throws above U+00FF and a patient called Évrard is not an edge case.
  const marker = "Évrard^Camille~ZZTOP" + Date.now();

  await page.getByRole("button", { name: "Try a filter" }).click();

  // By label, not by position. My first attempt used the first textarea on the page and filled the v3 field picker above the
  // playground instead - so the test failed while the feature worked. The labels were not associated with their fields at all,
  // which is why that was possible.
  const editor = page.getByLabel("The message");
  await editor.fill(
    ["MSH|^~\\&|SENDING|SITEA|PERFUSE|SITEB|20260823090000||ADT^A01|SHARE1|P|2.5.1", `PID|1||MRN1^^^SITEA^MR||${marker}`].join(
      "\r",
    ),
  );

  await page.getByRole("button", { name: "Copy a link to this" }).click();

  // The warning is the point, not decoration. A tool that invites a real patient to be pasted and then offers a share button owes
  // the person pressing it a sentence about what the link contains.
  await expect(page.locator("main")).toContainText("contains everything in these boxes");

  // The URL now carries the session, and carries it in the fragment.
  const shared = page.url();
  expect(shared, "the session was not put in the URL").toContain("#");

  const [path, fragment] = shared.split("#");
  expect(fragment ?? "", "the fragment is empty").not.toEqual("");

  // Nothing before the # may carry the payload. This is the property that keeps the message out of access logs, proxy logs and
  // request traces on the way to the server.
  expect(path, "the session leaked into the part of the URL that is sent to the server").not.toContain("?");

  // And no request may have carried the message. Checked directly rather than reasoned about, because "fragments are not sent" is
  // true of the browser and says nothing about what the application might have done with it.
  const leaked = requests.filter((u) => u.includes("ZZTOP") || u.includes("Camille"));
  expect(leaked, `the message appeared in ${leaked.length} request URL(s)`).toEqual([]);

  // Follow the link in a fresh page, the way a recipient would.
  const recipient = await page.context().newPage();
  await recipient.goto(shared);

  await expect(recipient.getByRole("button", { name: "Try a filter" })).toBeVisible({ timeout: 60_000 });

  // The message came back.
  await expect(recipient.getByLabel("The message"), "the shared message was not restored").toHaveValue(
    new RegExp(marker.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")),
  );

  // And on the tab it was shared from, because a link is usually sent to show one particular thing.
  await expect(
    recipient.getByRole("button", { name: "Try a filter" }),
    "the recipient landed on a different tab from the one shared",
  ).toHaveClass(/bg-sky-600/);

  await recipient.close();
});

// A damaged link must fall back to the samples rather than restoring half a session.
test("a damaged playground link falls back rather than restoring part of a session", async ({ page }) => {
  test.setTimeout(120_000);

  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(String(e)));

  // The shape a link arrives in after a mail client has wrapped it.
  await page.goto("/#playground=eyJ0YWIiOiJmaWx0ZXIiLCJtZXNzYWdl");

  await expect(page.getByRole("button", { name: "Try a filter" })).toBeVisible({ timeout: 60_000 });

  // Still usable, and no crash. Restoring the half that decoded would show somebody a message that is not the one they were sent,
  // with nothing on screen to say so, and they would debug the wrong thing.
  await expect(page.getByLabel("The message")).not.toHaveValue("");
  expect(problems, "a damaged link threw").toEqual([]);
});
