import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// National exchange, and the record of it.
//
// This fixture does not participate in TEFCA, which is the normal state for an instance and is what most of these
// assertions are about: an unconfigured section must be usable and honest rather than a wall of red on a server that is
// working perfectly.

test("an instance that does not participate says so and stays usable", async ({ page }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`uncaught exception: ${e.message}`));

  await page.goto("/");
  await openTab(page, "TEFCA");

  const main = page.locator("main");

  // Not an error. Most instances do not take part, and reporting that as a failure would make every ordinary
  // installation look misconfigured — which teaches people to ignore the section.
  await expect(main).toContainText(/does not take part/i, { timeout: 25_000 });
  await expect(main).not.toContainText(/something went wrong/i);

  // And it must say where to go, because the standing rule is that everything is configurable from here.
  await expect(main, "nothing says how to take part").toContainText(/Settings/);

  expect(problems, problems.join("\n")).toEqual([]);
});

test("the purposes of use can be examined before anything is configured", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "TEFCA");

  const main = page.locator("main");

  // Deciding whether to take part means knowing what would be declared, and that question comes before any
  // configuration exists. A section that can only be read about until it is switched on cannot be evaluated.
  await expect(main).toContainText(/treatment/, { timeout: 25_000 });
  await expect(main).toContainText(/individual_access/);

  await page.getByRole("button", { name: /Check treatment/ }).click();

  // The answer has to say what this instance would actually do with it today, not just whether the word is valid.
  await expect(
    main.locator('[role="status"]'),
    "clicking a purpose reported nothing",
  ).toBeVisible({ timeout: 15_000 });
  await expect(main).toContainText(/not configured as a TEFCA participant/i);
});

test("an invented purpose of use is distinguished from an undeclared one", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "TEFCA");
  await expect(page.locator("main")).toContainText(/treatment/, { timeout: 25_000 });

  // Checked through the API, because the interface only offers the purposes that exist — which is correct, and means
  // the invented case cannot be reached by clicking.
  const answer = await page.evaluate(async () => {
    const res = await fetch("/api/tefca/purpose-check", {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Perfuse-Request": "1" },
      credentials: "same-origin",
      body: JSON.stringify({ purpose: "research" }),
    });
    return (await res.json()) as { recognised: boolean; explanation: string };
  });

  expect(answer.recognised, "an invented purpose was reported as recognised").toBe(false);

  // The two problems have different fixes: no configuration will make an unrecognised purpose work, while an
  // undeclared one is a gap a site can close. Saying which is which is the whole point.
  expect(answer.explanation).toMatch(/does not recognise/i);
});

test("a failure loading the section is shown rather than spun on forever", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "TEFCA");

  // The status request is made to fail after the section has been reached, then the section is asked to load again.
  //
  // Worth a test because the failing branch was unreachable. A failed status request set the error in state and left the status
  // null, and the render returned a spinner whenever the status was null - so the message saying what was wrong existed, was
  // correct, and could never appear. An operator watching a spinner concludes the server is slow and waits; one reading a
  // refusal knows to look at the certificate path. Found while writing the component test for the configured branch.
  await page.route("**/api/tefca/status", (route) =>
    route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: "the audit file could not be opened" }) }),
  );

  // Loaded fresh, so the section runs its own load path with the failure in place rather than keeping the status it already
  // fetched successfully.
  await page.goto("/");
  await openTab(page, "TEFCA");

  const main = page.locator("main");

  await expect(main).toContainText(/could not be opened/i, { timeout: 20_000 });

  // And the spinner must be gone, because a screen showing both says the request is still in flight.
  await expect(main.locator(".animate-spin")).toHaveCount(0);
});
