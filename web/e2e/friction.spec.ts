import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// The friction report, driven through the browser.
//
// This screen replaced a queue item asking for one real operator to be handed the binary and watched building a channel. That item stood
// at the top of the queue for three days and could never be done, because the only parties who have ever run this software are the
// person who commissioned it and the program that wrote it.
//
// Its purpose survives: every claim here about ease of use rests on the author's judgement of his own work, and a refusal is the one
// signal that does not. So this spec makes the product refuse something and then checks the report noticed - which is the only way to
// test a feature whose whole job is to contradict its author.

test("a refusal the operator causes appears in the friction report", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Activity");

  await expect(page.getByRole("heading", { name: "Where this installation got stuck" })).toBeVisible();

  // The total before, asked of the server. Read rather than assumed: the suite shares one server and earlier specs have certainly been
  // refused things, so an absolute count would be a test of the run order.
  const total = async () =>
    page.evaluate(async () => {
      const res = await fetch("/api/friction", { headers: { "X-Perfuse-Request": "1" } });
      return ((await res.json()) as { total: number }).total;
    });

  const before = await total();

  // Cause a real refusal through the same API the interface uses. Asking for a channel that does not exist is genuine friction:
  // somebody followed a stale link or mistyped a name.
  const status = await page.evaluate(async () => {
    const res = await fetch("/api/channels/there-is-no-channel-called-this-one", {
      headers: { "X-Perfuse-Request": "1" },
    });
    return res.status;
  });

  expect(status, "the request was meant to be refused").toBeGreaterThanOrEqual(400);

  // The count has to move. Note what is deliberately not asserted: that the channel name appears anywhere. It must not - the record
  // holds the route with identifiers replaced and the server's own sentence, because the value that breaks a rule is often patient
  // data. Looking for the name here was the first version of this test, and it failed for exactly that reason.
  await expect
    .poll(total, { message: "the refusal was not recorded", timeout: 15_000 })
    .toBeGreaterThan(before);

  // And the panel has to show it without a page reload. Its own button is the interaction under test: a report needing a reload to
  // change is one nobody looks at twice.
  //
  // Scoped to the panel, because the Activity view has a Refresh button of its own that sits earlier in the document. The first
  // version of this used .first() and refreshed the audit trail instead, then failed saying the refusal never appeared - a locator
  // matching the wrong control reports the absence of whatever it was looking for.
  const panel = page.getByRole("region", { name: "Where this installation got stuck" });
  await panel.getByRole("button", { name: /Reload report|Reading/ }).click();

  const refusalRows = panel.getByRole("row").filter({ hasText: "404" });
  await expect(refusalRows.first()).toBeVisible({ timeout: 15_000 });

  // The name must not be on the screen either, since the screen only ever shows what was recorded.
  await expect(page.getByText("there-is-no-channel-called-this-one")).toHaveCount(0);
});

test("the report names where the installation is stuck", async ({ page }) => {
  // The funnel's one job. Four timestamps a reader has to compare would get interpreted by whoever wrote the software, which is the
  // circularity the whole screen exists to escape.
  await page.goto("/");
  await openTab(page, "Activity");

  const stuck = page.getByText(/Stuck at:/);
  await expect(stuck).toBeVisible();

  // Any of the funnel's states is a pass. Asserting a particular one would make this a test of what the fixture channel happens to have
  // done, and the states are exercised directly in internal/api/friction_test.go.
  await expect(stuck).toHaveText(
    /nobody has signed in|no channel has ever been created|deleted or would not load|no message has ever arrived|none has been delivered|received and delivered/,
  );
});

test("the report shows delivery separately from arrival", async ({ page }) => {
  // The distinction worth having a screen for. A channel can take traffic for weeks and deliver none of it, and from every other view
  // in the product that looks like it is working.
  await page.goto("/");
  await openTab(page, "Activity");

  await expect(page.getByText("Messages in", { exact: true })).toBeVisible();
  await expect(page.getByText("Delivered", { exact: true })).toBeVisible();
});
