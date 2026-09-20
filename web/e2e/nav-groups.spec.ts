import { test, expect } from "@playwright/test";
import { openTab, inlineTabs, groupButtons, groupNames, tabLabels } from "./nav";

// Whether the navigation is organised, and stays organised.
//
// The bar used to show the first seven views and put the other fifteen behind a menu called "More", in
// declaration order. Every view was reachable, so nothing was broken and no test had anything to
// report - but the order carried no meaning, so the only way to find a view was to open More and read
// all of it, every time.
//
// These are the properties that grouping is supposed to have. They are asserted rather than left to
// look right, because the failure mode of a grouped bar is not an error: it is a group quietly
// becoming a second list of everything.

/**
 * The one group name that must never appear.
 *
 * App.tsx renders anything left out of NAV_GROUPS into a group called Other, so a forgotten view stays
 * reachable rather than vanishing. That is a deliberate fallback and it must never be load-bearing:
 * invisible is the failure nobody reports, but a bucket called Other is the failure everybody ignores.
 */
const FALLBACK = "Other";

test("the bar is grouped rather than one long list", async ({ page }) => {
  await page.goto("/");

  const direct = await inlineTabs(page).count();
  const groups = await groupNames(page);

  // Few enough direct tabs that the groups are what the bar is made of. Seven inline views and one
  // catch-all menu is the arrangement this replaced, and it would satisfy a weaker assertion.
  expect(direct, `${direct} views sit directly in the bar, which is a list again rather than a group`).toBeLessThanOrEqual(3);

  expect(groups.length, "the bar has no group menus").toBeGreaterThanOrEqual(3);

  // Named for what they are for. A bar of Group 1, Group 2 would pass every other assertion here.
  for (const name of groups) {
    expect(name, "a group has no name").not.toBe("");
    expect(name, `"${name}" is not a category`).not.toMatch(/^(More|Misc|Other|Various|Tools)$/i);
  }
});

test("every view belongs to a named group", async ({ page }) => {
  await page.goto("/");

  const groups = await groupNames(page);
  expect(
    groups,
    `views have fallen into the ${FALLBACK} bucket, which means the tab list gained a view and NAV_GROUPS was not told about it`,
  ).not.toContain(FALLBACK);
});

test("no group is a dumping ground", async ({ page }) => {
  await page.goto("/");

  const groups = groupButtons(page);
  const count = await groups.count();
  const sizes: string[] = [];

  for (let i = 0; i < count; i++) {
    const name = ((await groups.nth(i).getAttribute("aria-label")) ?? "").replace(/ views.*/, "");
    await groups.nth(i).click();
    const items = await page.locator("[role=menu] [role=menuitem]").count();
    await page.keyboard.press("Escape");

    sizes.push(`${name}: ${items}`);

    // A group holding most of the product is the old More menu with a better name. Eight is generous;
    // the point is that it cannot hold fifteen.
    expect(items, `the ${name} group holds ${items} views, which is a list rather than a category`).toBeLessThanOrEqual(8);

    // And a group of one is a tab that has been hidden behind an extra click for no reason.
    expect(items, `the ${name} group holds only ${items} view, so the group earns nothing`).toBeGreaterThan(1);
  }

  expect(sizes.length, "no groups were measured").toBeGreaterThan(0);
});

test("a group menu says what it is for, and so does every view in it", async ({ page }) => {
  await page.goto("/");

  const first = groupButtons(page).first();
  const name = ((await first.getAttribute("aria-label")) ?? "").replace(/ views.*/, "");
  await first.click();

  const menu = page.locator("[role=menu]").first();

  // The group's own sentence, in the menu. It existed before only as a title attribute on the trigger,
  // which is unreachable by keyboard and never appears on a touch screen - so for many people the
  // explanation was not there at all.
  const menuText = await menu.innerText();
  expect(
    menuText.length,
    `the ${name} menu shows only names, so choosing between them means already knowing what they are`,
  ).toBeGreaterThan(name.length + 40);

  // Each item is a name above a description. Asserted per item, because one described view and five
  // bare ones would pass a check on the menu as a whole.
  const items = page.locator("[role=menu] [role=menuitem]");
  const n = await items.count();
  for (let i = 0; i < n; i++) {
    const lines = (await items.nth(i).innerText()).split("\n").filter((l) => l.trim());
    expect(
      lines.length,
      `"${lines[0] ?? "?"}" in ${name} is a bare name with nothing saying what it does`,
    ).toBeGreaterThanOrEqual(2);
  }
});

test("the group holding the current view says so, and the others do not", async ({ page }) => {
  await page.goto("/");

  // Queue is in a group, so opening it must mark exactly one group as holding the selection. The old
  // control replaced the word More with the view's name, which named the selection but lost the group -
  // so the bar stopped saying where you were.
  await openTab(page, "Queue");

  const groups = groupButtons(page);
  const marked: string[] = [];
  for (let i = 0; i < (await groups.count()); i++) {
    const label = (await groups.nth(i).getAttribute("aria-label")) ?? "";
    if (/selected/.test(label)) marked.push(label);
  }

  expect(marked, "no group reports holding the current view, so the bar does not say where you are").toHaveLength(1);

  // Both, not either. The group name is what makes the arrangement learnable by using it; the view name
  // is what stops the selection being anonymous.
  expect(marked[0]).toMatch(/Queue selected/);
  expect(marked[0]).toMatch(/^\w+ views,/);
});

test("every view is still reachable after grouping", async ({ page }) => {
  await page.goto("/");

  // The regression that matters most. Twenty-two views were reachable before; rearranging the bar must
  // not have stranded any of them, and a view reachable by no route is invisible rather than broken.
  const labels = await tabLabels(page);
  expect(labels.length, `only ${labels.length} views are reachable: ${labels.join(", ")}`).toBeGreaterThanOrEqual(20);

  // No duplicates. A view listed in two groups is a view somebody will look for in the third.
  expect(new Set(labels).size, `a view appears in more than one place: ${labels.join(", ")}`).toBe(labels.length);
});
