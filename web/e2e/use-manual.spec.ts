import { test, expect } from '@playwright/test'

/** The manual has to be reachable from the interface, not only from the filesystem.
 *
 * The standing rule for this project is that everything can be done from the web interface. A manual
 * that exists only as a file next to the source fails that in the case where it matters most:
 * somebody logged in at two in the morning looking at a setting they do not recognise, on a machine
 * where the source is not checked out.
 */
test.describe.configure({ mode: 'serial' })

test('the manual link opens a manual with real content', async ({ page, context }) => {
  await page.goto('/')

  const link = page.getByRole('link', { name: 'Manual' })
  await expect(link).toBeVisible()

  // Opens in a new tab deliberately, so the screen that prompted the question stays open beside it.
  const [manual] = await Promise.all([context.waitForEvent('page'), link.click()])
  await manual.waitForLoadState('load')

  // Assert on content that can only be there if the document was rendered, not merely served.
  await expect(manual.getByRole('heading', { name: /Perfuse Reference Manual/ })).toBeVisible()
  await expect(manual.getByRole('heading', { name: /Table of Contents/ })).toBeVisible()

  // A chapter that is written rather than generated, so this proves the embedded prose survived the build.
  await expect(manual.getByRole('heading', { name: /Message Flow/ }).first()).toBeVisible()
})

test('a cross-reference in the manual navigates to the chapter it names', async ({ page, context }) => {
  await page.goto('/')

  const [manual] = await Promise.all([
    context.waitForEvent('page'),
    page.getByRole('link', { name: 'Manual' }).click(),
  ])
  await manual.waitForLoadState('load')

  // Links are written as title slugs and resolved to numbered anchors at render time, so that renumbering
  // a chapter cannot break them. This exercises that resolution end to end rather than trusting the map:
  // click a link in the prose and assert the target heading is what scrolled into view.
  const link = manual.getByRole('link', { name: 'message flow' }).first()
  const href = await link.getAttribute('href')

  await link.click()

  // Asserted through the anchor rather than through the scroll position.
  //
  // This used to end with toBeInViewport, and it failed roughly once in thirty runs with "viewport ratio 0" after ten
  // seconds of retrying - the heading was found every time and the page had simply not moved. The manual sets
  // scroll-behavior: smooth, and Chromium drops that animated scroll often enough to matter.
  //
  // The scroll is the browser's part. The product's part is resolving a title slug written in the prose to the numbered
  // anchor the heading actually carries, so that renumbering a chapter cannot break a link - and that is what these two
  // assertions check, end to end and without depending on an animation. A dropped scroll can no longer report a broken
  // cross-reference.
  const target = manual.getByRole('heading', { name: /Message Flow/ }).first()
  const id = await target.getAttribute('id')

  expect(id, 'the target heading carries no id, so no cross-reference could resolve to it').toBeTruthy()
  expect(href, 'the link in the prose does not point at the heading it names').toBe(`#${id}`)
  await expect(manual).toHaveURL(new RegExp(`#${id}$`))
})

test('the served manual says where the generated reference is rather than omitting it silently', async ({
  page,
  context,
}) => {
  await page.goto('/')

  const [manual] = await Promise.all([
    context.waitForEvent('page'),
    page.getByRole('link', { name: 'Manual' }).click(),
  ])
  await manual.waitForLoadState('load')

  // The generated chapters need the Go source, which a deployed binary does not have. The absence has
  // to be stated: a manual with five chapters silently missing reads as one whose author forgot them.
  await expect(manual.getByRole('heading', { name: /The Configuration Reference/ })).toBeVisible()
  await expect(manual.getByText(/make docs/).first()).toBeVisible()
})
