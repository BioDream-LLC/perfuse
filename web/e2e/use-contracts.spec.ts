import { test, expect } from "@playwright/test";
import { openTab } from "./nav";
import { sendMLLP, anADT } from "./mllp";

// Making a contract from the interface, which was previously a command-line step.
//
// The section could report that a feed had drifted from its contract and gave no way to create one, so using
// the feature at all needed a terminal on the server. This walks the whole path: send traffic, propose a
// contract from it, read what was proposed, save it, and confirm the channel is watched afterwards.

// The shadowed channel, chosen because it has traffic and no contract.
//
// Not the main fixture channel: that one already has a contract, so it is not offered here - which is correct
// behaviour and would make this test time out looking for it.
function trafficPort(): number {
  const p = Number(process.env.PERFUSE_E2E_SHADOW_MLLP);
  if (!p || Number.isNaN(p)) {
    throw new Error("PERFUSE_E2E_SHADOW_MLLP is not set; the global setup exports it");
  }
  return p;
}

const CHANNEL = "shadowed";

test("a contract can be built from recorded traffic and saved", async ({ page }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`uncaught exception: ${e.message}`));

  // Enough messages that the profile has something to say. A contract built from one message describes that
  // message, which the interface warns about rather than hiding.
  for (let i = 0; i < 6; i++) {
    await sendMLLP(anADT(`CONTRACT${i}`), trafficPort());
  }

  await page.goto("/");
  await openTab(page, "Contracts");

  const picker = page.getByRole("combobox", { name: /Channel with no contract/i });
  await expect(picker, "there is no way to choose a channel to build a contract for").toBeVisible({
    timeout: 25_000,
  });

  // The fixture channel that has traffic and no contract.
  await picker.selectOption(CHANNEL);

  await page.getByRole("button", { name: /^Propose a contract$/ }).click();

  // The outcome that can only follow the request: a contract to read. Never assert on the surrounding
  // explanation, which was on screen before the click.
  const draft = page.getByRole("textbox", { name: "Proposed contract" });
  await expect(draft, "no contract was proposed from the recorded traffic").toBeVisible({ timeout: 30_000 });

  const yaml = await draft.inputValue();
  expect(yaml.length, "the proposed contract is empty").toBeGreaterThan(20);
  expect(yaml, "the proposal does not look like a contract").toContain("expectations");

  // It must say how much evidence it is built on. A contract from six messages and one from six hundred are
  // different things, and the person deciding needs to know which they have.
  await expect(page.locator("main"), "the proposal does not say how many messages it read").toContainText(
    /message/i,
  );

  await page.getByRole("button", { name: /^Save and start checking/ }).click();

  await expect(
    page.locator("main"),
    "saving the contract reported nothing, so there is no way to know whether it worked",
  ).toContainText(/Saved as/i, { timeout: 30_000 });

  expect(problems, problems.join("\n")).toEqual([]);
});

test("a contract that asserts nothing is refused with a reason", async ({ page }) => {
  // A different channel from the test above, which saved a contract for its own and so removed it from this
  // list. Tests in a file share one server, so each needs its own subject or the second depends on the first
  // having failed.
  const portB = Number(process.env.PERFUSE_E2E_SHADOW_MLLP_B);
  expect(portB, "PERFUSE_E2E_SHADOW_MLLP_B is not set").toBeTruthy();
  for (let i = 0; i < 4; i++) {
    await sendMLLP(anADT(`EMPTY${i}`), portB);
  }

  await page.goto("/");
  await openTab(page, "Contracts");

  const picker = page.getByRole("combobox", { name: /Channel with no contract/i });
  await expect(picker).toBeVisible({ timeout: 25_000 });
  await picker.selectOption("shadowed-b");

  await page.getByRole("button", { name: /^Propose a contract$/ }).click();
  const draft = page.getByRole("textbox", { name: "Proposed contract" });
  await expect(draft).toBeVisible({ timeout: 30_000 });

  // Emptied deliberately. A contract with no expectations reports every message as a departure from it, and a
  // contract file that will not load takes the whole channel off the air - Perfuse refuses a channel wholesale
  // when anything it references is broken. So this must be refused before it is written, and it must say why.
  await draft.fill("expectations: []\n");
  await page.getByRole("button", { name: /^Save and start checking/ }).click();

  await expect(
    page.locator('main [role="alert"]'),
    "an empty contract was accepted, or refused without saying anything",
  ).toBeVisible({ timeout: 20_000 });
  await expect(page.locator('main [role="alert"]')).toContainText(/asserts nothing|no expectations/i);
});
