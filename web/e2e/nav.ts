import { expect, type Page } from "@playwright/test";

// Navigating the tab bar.
//
// The bar shows two views directly and puts the other twenty behind four named group menus - Monitor,
// Build, Exchange, Administer. So there are two ways to reach a view, and a test that knows only one
// silently stops covering whatever moved. Both paths live here, so regrouping the navigation is a
// change to this file rather than to fifteen specs.
//
// The groups are menus rather than more tabs, deliberately: ARIA permits only tabs inside a tablist,
// so a trigger cannot live there. That means a grouped view is a menuitem and getByRole("tab") will
// not find it - which is exactly the trap this helper exists to avoid.
//
// Nothing here names the groups. A helper that knew Queue lives under Monitor would need editing every
// time a view is regrouped, and would pass while the interface disagreed with it. It searches instead.

/** inlineTabs are the tab buttons rendered directly in the bar. */
export function inlineTabs(page: Page) {
  // The selected view is mirrored as a visually hidden tab when it lives in a group, so that the
  // tablist always reports a selection. It is excluded here because it is not a navigation control.
  return page.locator("nav[role=tablist] > button:not(.sr-only)");
}

/**
 * groupButtons are the group triggers.
 *
 * Matched on the accessible name ending in "views" rather than on a list of group names, so a renamed
 * or added group is found without editing this file. Each is labelled "<Group> views", or
 * "<Group> views, <View> selected" when the current view is inside it.
 */
export function groupButtons(page: Page) {
  return page.getByRole("button", { name: /\bviews(,|$)/ });
}

/** openTab selects a view by label, whether it is directly in the bar or inside a group menu. */
export async function openTab(page: Page, label: string): Promise<void> {
  // Wait for the navigation to exist before counting anything in it.
  //
  // count() does not wait. Unlike click() or an expect() assertion it answers immediately with whatever is in the DOM at that
  // instant, so on a page that has not finished its first render both branches below found nothing and the helper reported that the
  // view "is not in the bar and there are no group menus to look in" - a message describing a missing feature, produced by a page
  // that was still loading.
  //
  // This surfaced as different specs failing on different full-suite runs and every one of them passing alone, which reads like
  // flakiness in the specs and was one missing wait in the helper they all share.
  await expect(page.locator("nav[role=tablist]")).toBeVisible({ timeout: 30_000 });

  const inline = inlineTabs(page).filter({ hasText: new RegExp(`^${label}$`) });
  const groups = groupButtons(page);

  // The bar being visible does not mean anything is in it yet, which is the half of the race the wait above does not close.
  //
  // The comment above describes one missing wait found by this same investigation. Adding it narrowed the window and left this:
  // nav[role=tablist] can be painted while its children are still rendering, and both count() calls below then answer zero and
  // report a missing feature. So wait for something countable - either the tab itself or at least one group menu.
  //
  // How it fails matters more than that it fails. count() answers immediately, and the expect() below is given a number rather than
  // a locator so it does not retry either. The test fails in milliseconds instead of waiting, which is why these failures looked too
  // fast to be timeouts and were read as something not being ready. They were exactly that.
  // first() applies to the union, not to each side. Written as inline.first().or(groups.first()) it matches two elements whenever both
  // exist, which is a strict mode violation rather than a wait - and it fails instantly, so every spec in the suite broke at about
  // 220ms. That is the good failure mode for a mistake like this, and it was caught by running the whole suite.
  await expect(inline.or(groups).first()).toBeVisible({ timeout: 30_000 });

  if (await inline.count()) {
    await inline.first().click();
    await expect(page.locator("[role=tab][aria-selected=true]")).toHaveText(label);
    return;
  }

  const count = await groups.count();
  expect(count, `"${label}" is not in the bar and there are no group menus to look in`).toBeGreaterThan(0);

  const tried: string[] = [];
  for (let i = 0; i < count; i++) {
    const trigger = groups.nth(i);
    tried.push(((await trigger.getAttribute("aria-label")) ?? "?").replace(/ views.*/, ""));

    await trigger.click();

    // The menu has to be open before its items are counted, for the same reason as above: count() does not wait, so a menu that is
    // still rendering reads as a menu without the item in it. The loop would then press Escape, try the next group, run out, and
    // report that the view is in no group - naming a missing feature when the only problem was being early.
    const openMenu = page.locator("[role=menu]");
    await expect(openMenu).toBeVisible({ timeout: 10_000 });

    // And at least one item in it, because a visible menu is not a populated one. Stopping at the menu leaves the same race one level
    // down: the items are counted, none has rendered, the loop presses Escape and moves on, and the view is reported to be in no group.
    await expect(openMenu.getByRole("menuitem").first()).toBeVisible({ timeout: 10_000 });

    // Scoped to the open menu. Two groups could offer a similarly named view, and an unscoped lookup
    // would match one in a menu that is closed - which resolves to a click on nothing at all.
    const item = page
      .locator("[role=menu]")
      .getByRole("menuitem")
      .filter({ has: page.locator(`text="${label}"`) });

    // Exact, because every item now carries a description underneath and a substring match on the
    // whole button would find Queue inside "Messages waiting to be delivered".
    const exact = page.locator("[role=menu] [role=menuitem]").filter({
      hasText: new RegExp(`^${label}(\\n|$)`),
    });

    if ((await exact.count()) > 0) {
      await exact.first().click();
      await expect(page.locator("[role=tab][aria-selected=true]")).toHaveText(label);
      return;
    }
    if ((await item.count()) > 0) {
      await item.first().click();
      await expect(page.locator("[role=tab][aria-selected=true]")).toHaveText(label);
      return;
    }

    await page.keyboard.press("Escape");
  }

  throw new Error(`"${label}" is in no group. Looked in: ${tried.join(", ")}`);
}

/** tabLabels returns every reachable view label, from the bar and from every group. */
export async function tabLabels(page: Page): Promise<string[]> {
  // The same race as openTab, and a worse one, because this version does not throw.
  //
  // allInnerTexts() does not wait any more than count() does. A menu still rendering contributes no labels and no error, so this
  // returns a shorter list and every caller believes it. The caller that matters is the test asserting every permitted view is
  // reachable: fed a short list it checks fewer views and passes, which is the failure this repository keeps finding in other
  // people's code - a check that reports success having done less than it claims.
  const groups = groupButtons(page);

  await expect(inlineTabs(page).or(groups).first()).toBeVisible({ timeout: 30_000 });

  const labels = (await inlineTabs(page).allInnerTexts()).map((t) => t.trim()).filter(Boolean);

  const count = await groups.count();
  for (let i = 0; i < count; i++) {
    await groups.nth(i).click();

    const menu = page.locator("[role=menu]");
    await expect(menu).toBeVisible({ timeout: 10_000 });

    // At least one item, asserted rather than assumed. An empty group menu is a real defect - a menu that opens onto nothing - and
    // reading zero items without complaint is how it would go unnoticed.
    const items = menu.getByRole("menuitem");
    await expect(items.first()).toBeVisible({ timeout: 10_000 });

    // The first line only. Each item is now a name above a description, and taking the whole text would
    // return a paragraph where a label is expected.
    const texts = await items.allInnerTexts();
    labels.push(...texts.map((t) => t.split("\n")[0]?.trim() ?? "").filter(Boolean));
    await page.keyboard.press("Escape");
  }
  return [...new Set(labels)];
}

/**
 * groupNames returns the group menu names, in bar order.
 *
 * Used by the navigation tests to assert the bar is grouped at all, rather than that it happens to
 * contain a particular set of words.
 */
export async function groupNames(page: Page): Promise<string[]> {
  const groups = groupButtons(page);

  // Waited for, because a count taken too early is zero and this would return an empty list. A test asserting the bar is grouped at
  // all would then fail saying it is not grouped, which names a design regression that has not happened.
  await expect(inlineTabs(page).or(groups).first()).toBeVisible({ timeout: 30_000 });

  const out: string[] = [];
  for (let i = 0; i < (await groups.count()); i++) {
    const label = (await groups.nth(i).getAttribute("aria-label")) ?? "";
    out.push(label.replace(/ views.*/, ""));
  }
  return out;
}

/**
 * panelText waits for a view to render and returns its text.
 *
 * Polled rather than read once, because every view fetches its own data: reading immediately measures
 * how fast the network was rather than whether the view works, and reports every asynchronous tab as
 * blank. A genuinely empty panel still fails, it just takes a few seconds to say so.
 */
export async function panelText(page: Page, minLength = 20, timeoutMs = 8000): Promise<string> {
  const main = page.locator("main").first();
  const deadline = Date.now() + timeoutMs;
  let text = "";
  while (Date.now() < deadline) {
    text = ((await main.textContent()) ?? "").trim();
    if (text.length > minLength) return text;
    await page.waitForTimeout(100);
  }
  return text;
}
