import { test, expect, type Page } from "@playwright/test";
import { openTab } from "./nav";

/**
 * Uses the sign-on editor, which configures the only thing on this server that can lock everybody out.
 *
 * # What these tests are guarding
 *
 * Both files were editable before as YAML text boxes, so "can they be edited" is not the question. The questions are whether somebody
 * who does not already know two file formats can edit them, whether a secret survives being edited around, and whether the screen can
 * tell them their configuration is wrong before a restart rather than after one.
 *
 * The secret test is the one to keep if any had to go. The settings screen has already been caught by a redacted placeholder being saved
 * back over a real credential, which takes sign-on down and leaves a file that reads perfectly plausibly.
 */

async function openSignon(page: Page) {
  await openTab(page, "Settings");
  await expect(page.getByRole("heading", { name: "Sign-on" }).first()).toBeVisible({ timeout: 20_000 });
  await expect(page.locator("#signon-oidc-issuer")).toBeVisible({ timeout: 20_000 });
}

async function showSAML(page: Page) {
  await page.getByRole("tab", { name: /SAML/ }).click();
  await expect(page.locator("#signon-saml-entity_id")).toBeVisible({ timeout: 10_000 });
}

async function showDirectory(page: Page) {
  await page.getByRole("tab", { name: /Directory/ }).click();
  await expect(page.locator("#signon-ldap-addr")).toBeVisible({ timeout: 10_000 });
}

function watchForFaults(page: Page): string[] {
  const faults: string[] = [];

  page.on("pageerror", (e) => faults.push(`uncaught: ${e.message}`));
  page.on("console", (m) => {
    if (m.type() !== "error") return;

    const text = m.text();
    if (text.includes("Failed to load resource") || text.includes("net::ERR")) return;

    faults.push(`console: ${text}`);
  });

  return faults;
}

test("the configuration on screen is the configuration in the file", async ({ page }) => {
  await page.goto("/");
  await openSignon(page);

  await expect(page.locator("#signon-oidc-issuer")).toHaveValue("https://login.example.invalid");
  await expect(page.locator("#signon-oidc-client_id")).toHaveValue("perfuse-e2e");

  // The group mapping arrives as one row per group, which is the shape somebody is trying to express. The file holds four lists, and a
  // group appearing in two of them is a contradiction the file allows and this shape cannot.
  await expect(page.getByRole("combobox", { name: "Role for perfuse-admins" })).toHaveValue("admin");
  await expect(page.getByRole("combobox", { name: "Role for clinical-staff" })).toHaveValue("viewer");

  await showDirectory(page);
  await expect(page.locator("#signon-ldap-addr")).toHaveValue("dc01.example.invalid:636");
  await expect(page.locator("#signon-ldap-username_attribute")).toHaveValue("uid");
});

test("no secret is ever sent to the browser", async ({ page }) => {
  const bodies: string[] = [];

  page.on("response", async (res) => {
    if (!res.url().includes("/api/signon")) return;

    bodies.push(await res.text().catch(() => ""));
  });

  await page.goto("/");
  await openSignon(page);

  expect(bodies.length, "no sign-on response was seen, so this test is not checking anything").toBeGreaterThan(0);

  // The fixture's real secrets. Checked against the whole response body rather than a field, because a leak through a field nobody
  // remembered would not show up in a field-by-field check and the browser receives the body.
  for (const secret of ["e2e-client-secret", "e2e-bind-secret"]) {
    for (const body of bodies) {
      expect(body.includes(secret), `the response body contains the secret ${secret}`).toBe(false);
    }
  }

  // And the boxes say a secret exists without showing it, which is the difference between "kept" and "lost".
  await expect(page.locator("#signon-oidc-client_secret")).toHaveAttribute("placeholder", "unchanged");
  await expect(page.locator("#signon-oidc-client_secret")).toHaveValue("");
  await expect(page.locator("main")).toContainText("A secret is stored");
});

test("a saved change keeps the stored secret", async ({ page }) => {
  await page.goto("/");
  await openSignon(page);

  const label = page.locator("#signon-oidc-label");
  const before = await label.inputValue();
  const wanted = `Sign in with QA ${Date.now()}`;

  await label.fill(wanted);
  await page.getByRole("button", { name: "Save sign-on settings" }).click();

  // Saved and not yet in effect, said in one message. Somebody who saves a fix and waits for it to work is waiting for nothing.
  await expect(page.getByRole("status")).toContainText(/after a restart/i, { timeout: 20_000 });

  await page.reload();
  await openSignon(page);
  await expect(page.locator("#signon-oidc-label")).toHaveValue(wanted);

  // The secret must still be there. A save that dropped it would report success, keep the label, and take single sign-on down at the
  // next restart - with the file reading perfectly plausibly.
  await expect(
    page.locator("main"),
    "the secret was lost by a save that never mentioned it, which would break sign-on at the next restart",
  ).toContainText("A secret is stored");

  // Put the label back, since one server serves the whole suite.
  await page.locator("#signon-oidc-label").fill(before);
  await page.getByRole("button", { name: "Save sign-on settings" }).click();
  await expect(page.getByRole("status")).toBeVisible({ timeout: 20_000 });
});

test("every field says what it is for, and the directory fields say which name each kind of directory uses", async ({ page }) => {
  await page.goto("/");
  await openSignon(page);
  await showDirectory(page);

  const text = await page.locator("main").innerText();

  // The attribute names are the single most useful thing on this screen. Each is a field where the wrong value finds nobody rather than
  // reporting an error, and knowing which name your directory uses is knowledge the product can supply rather than demand.
  for (const want of ["sAMAccountName", "objectGUID", "entryUUID", "memberOf"]) {
    expect(text, `the directory fields never mention ${want}, so somebody has to go and look it up`).toContain(want);
  }

  // And the consequence of the one that silently refuses everybody.
  expect(text, "nothing warns that a wrong username attribute looks like a wrong password").toMatch(/wrong password/i);
});

test("a directory preset fills the attribute names", async ({ page }) => {
  await page.goto("/");
  await openSignon(page);
  await showDirectory(page);

  await page.locator("#signon-ldap-username_attribute").fill("");
  await page.getByRole("button", { name: /Use Active Directory names/ }).click();

  // Filled visibly rather than applied invisibly at startup, so what is on screen is what would run.
  await expect(page.locator("#signon-ldap-username_attribute")).toHaveValue("sAMAccountName");
  await expect(page.locator("#signon-ldap-unique_id_attribute")).toHaveValue("objectGUID");
  await expect(page.locator("#signon-ldap-member_of_attribute")).toHaveValue("memberOf");

  // OpenLDAP is a different shape, not a different spelling: it finds groups by searching rather than by asking the person.
  await page.getByRole("button", { name: /Use OpenLDAP names/ }).click();
  await expect(page.locator("#signon-ldap-username_attribute")).toHaveValue("uid");
  await expect(page.locator("#signon-ldap-unique_id_attribute")).toHaveValue("entryUUID");
  await expect(page.locator("#signon-ldap-group_filter")).toHaveValue(/member=%d/);
});

test("every directory field can be typed into and reaches the form", async ({ page }) => {
  // The bug this exists for was nearly shipped: the property each control edits was derived in the browser by converting the YAML key,
  // which is wrong wherever an initialism meets a word boundary - start_tls, bind_dn, unique_id_attribute. Those controls would have
  // read and written nothing, silently, because the object is indexed by a computed string that no type checker can object to.
  await page.goto("/");
  await openSignon(page);
  await showDirectory(page);

  const suspects = [
    "signon-ldap-bind_dn",
    "signon-ldap-user_base_dn",
    "signon-ldap-unique_id_attribute",
    "signon-ldap-group_base_dn",
    "signon-ldap-group_name_attribute",
  ];

  for (const id of suspects) {
    const box = page.locator(`#${id}`);
    await expect(box, `${id} is not on the screen`).toBeVisible();

    await box.fill(`qa-${id}`);
    await expect(box, `${id} accepted text and did not keep it, so it is bound to nothing`).toHaveValue(`qa-${id}`);
  }

  // A checkbox too, since start_tls is the boolean with the same problem.
  const startTLS = page.locator("#signon-ldap-start_tls");
  const was = await startTLS.isChecked();
  await startTLS.click();
  await expect(startTLS, "start_tls was clicked and did not change, so it is bound to nothing").toBeChecked({ checked: !was });
});

test("testing the provider reports each stage rather than a verdict", async ({ page }) => {
  const faults = watchForFaults(page);

  await page.goto("/");
  await openSignon(page);

  // The fixture points at a host that does not resolve, which is the honest state of a harness and also the state somebody is in when
  // they open this screen: sign-on is not working yet.
  await page.getByRole("button", { name: "Run the test" }).click();

  const report = page.getByRole("status", { name: "Provider test result" });
  await expect(report).toBeVisible({ timeout: 30_000 });

  // A stage with an explanation, not "failed". The provider's own error carries the useful half - a name that does not resolve, a
  // discovery document that is not there, an issuer that disagrees with itself - and each sends somebody somewhere different.
  await expect(report).toContainText(/Reach the provider/);
  await expect(
    report,
    "the failure was reported with no explanation, so it says only that something is wrong",
  ).not.toHaveText(/^✗ Reach the provider$/);

  expect(faults, `testing the provider produced errors:\n${faults.join("\n")}`).toEqual([]);
});

test("testing the directory reports the stage that failed and asks for no password", async ({ page }) => {
  await page.goto("/");
  await openSignon(page);
  await showDirectory(page);

  // A username is what makes the test worth running, because the two things that silently refuse everybody - the username attribute
  // and the group mapping - are only reachable with one.
  await page.locator("#signon-test-username").fill("jsmith");
  await page.getByRole("button", { name: "Run the test" }).click();

  const report = page.getByRole("status", { name: "Directory test result" });
  await expect(report).toBeVisible({ timeout: 40_000 });
  await expect(report).toContainText(/Reach the directory/);

  // No password is asked for anywhere in this panel. Everything that goes silently wrong goes wrong before a password is checked, so a
  // probe needs none - and a password box on a screen that does not need one is how credentials reach a browser's saved-form data.
  const testPanel = page.locator("main").locator("xpath=//*[contains(text(),'Test this configuration')]/..");
  await expect(
    testPanel.locator('input[type="password"]'),
    "the directory test panel asks for a password, which it does not need",
  ).toHaveCount(0);
  await expect(page.locator("main")).toContainText(/No password is needed/i);
});

test("the settings are refused before saving when two options contradict each other", async ({ page }) => {
  await page.goto("/");
  await openSignon(page);
  await showDirectory(page);

  // LDAPS and StartTLS together. The loader refuses this, and the point is that it is refused here rather than at the next restart.
  const startTLS = page.locator("#signon-ldap-start_tls");
  if (!(await startTLS.isChecked())) await startTLS.click();

  const tls = page.locator("#signon-ldap-tls");
  if (!(await tls.isChecked())) await tls.click();

  await page.getByRole("button", { name: "Save sign-on settings" }).click();

  // Explained rather than only refused. One encrypts from the first byte on port 636 and the other upgrades a plain connection on 389,
  // and somebody who ticked both does not know that yet.
  await expect(page.locator("main"), "a contradictory configuration was accepted or refused without explanation").toContainText(
    /cannot both be set/i,
    { timeout: 20_000 },
  );

  // Reload rather than untick, so the next test starts from the file.
  await page.reload();
  await openSignon(page);
});

test("a mapping with no groups says nobody could sign in", async ({ page }) => {
  await page.goto("/");
  await openSignon(page);

  for (const group of ["perfuse-admins", "clinical-staff"]) {
    await page.getByRole("button", { name: `Stop mapping ${group}` }).click();
  }

  // The warning matters because an empty mapping is valid YAML that refuses everybody, with no error anywhere: the file loads, the
  // server starts, and the first person to try is refused with nothing to tell them why.
  await expect(page.locator("main"), "an empty group mapping is not flagged as refusing everybody").toContainText(
    /nobody could sign in/i,
  );

  await page.reload();
  await openSignon(page);
  await expect(page.getByRole("combobox", { name: "Role for perfuse-admins" })).toBeVisible();
});

test("a group can be mapped, saved, and comes back", async ({ page }) => {
  await page.goto("/");
  await openSignon(page);

  await page.getByLabel("Group name to map").fill("qa-reviewers");
  await page.getByRole("button", { name: "+ Map this group" }).click();

  const row = page.getByRole("combobox", { name: "Role for qa-reviewers" });
  await expect(row).toBeVisible();
  await row.selectOption("editor");

  await page.getByRole("button", { name: "Save sign-on settings" }).click();
  await expect(page.getByRole("status")).toContainText(/after a restart/i, { timeout: 20_000 });

  await page.reload();
  await openSignon(page);
  await expect(
    page.getByRole("combobox", { name: "Role for qa-reviewers" }),
    "a group mapping was saved and did not come back, so the save reported success and wrote nothing",
  ).toHaveValue("editor", { timeout: 20_000 });

  // Remove it again for the rest of the suite.
  await page.getByRole("button", { name: "Stop mapping qa-reviewers" }).click();
  await page.getByRole("button", { name: "Save sign-on settings" }).click();
  await expect(page.getByRole("status")).toBeVisible({ timeout: 20_000 });
});

test("the screen says whether sign-on is actually running", async ({ page }) => {
  await page.goto("/");
  await openSignon(page);

  // Configured and not loaded is the normal state after an edit, and the least obvious. Without saying so, somebody saves a correction
  // and concludes the product is broken when it is only waiting for a restart.
  await expect(page.locator("main")).toContainText(/has not loaded it|takes effect after a restart/i);
});

test("SAML can be configured, tested and saved from the browser", async ({ page }) => {
  // The reason this test exists. The SAML parser sat in the tree for weeks with a thousand lines of tests and no callers, and there
  // was no file format either, so the only way to configure it was to edit Go. A capability reachable only by editing source is not
  // reachable: everything has to be doable from here.
  await page.goto("/");
  await openSignon(page);
  await showSAML(page);

  // What is on screen is what is in the file, which is the same property the OIDC test asserts. A screen showing defaults over a
  // populated file is worse than an error, because it invites somebody to save and overwrite what was there.
  await expect(page.getByLabel("Entity ID")).toHaveValue("https://perfuse.example.invalid");
  await expect(page.getByLabel(/Attribute carrying group membership/)).toHaveValue("groups");

  // The certificate is shown rather than redacted, unlike a client secret, and that is deliberate: a signing certificate is public,
  // and not being able to see which one is installed removes the first thing anybody checks when signatures stop verifying.
  await expect(page.getByLabel(/read the certificate from a file/)).not.toHaveValue("");

  // Sign-ins started at the identity provider are off, because that setting is what lets a captured response be posted into
  // somebody else's browser.
  await expect(page.getByLabel(/Accept sign-ins started at the identity provider/)).not.toBeChecked();

  // Test reports stages, and says what it cannot check.
  await page.getByRole("button", { name: "Run the test" }).click();

  const body = page.locator("body");
  await expect(body).toContainText(/Signing certificate/, { timeout: 20_000 });
  await expect(body, "the test does not admit what it cannot prove").toContainText(/What is left to check/);

  // A change reaches the form and can be saved.
  await page.getByLabel("Sign-in button text").fill("Sign in with St Mary's");
  await expect(page.getByText("Unsaved changes")).toBeVisible();

  await page.getByRole("button", { name: "Save sign-on settings" }).click();

  // Saved, and told plainly that saving is not the same as running - all three mechanisms are read at startup, so a correct file is
  // not yet in use. Somebody who saved a fix and still cannot sign in needs that sentence, not congratulations.
  await expect(page.getByRole("status").first()).toContainText(/next starts/, { timeout: 20_000 });

  // And it survives a reload, which is the difference between writing a file and appearing to.
  await page.reload();
  await openSignon(page);
  await showSAML(page);

  await expect(page.getByLabel("Sign-in button text")).toHaveValue("Sign in with St Mary's");
});

test("an empty role mapping is refused rather than saved", async ({ page }) => {
  // A configuration that authenticates people and grants none of them anything looks exactly like a broken product: the round trip
  // completes and everybody is turned away with nothing explaining why. Refused at the save rather than discovered by a person who
  // cannot get in.
  await page.goto("/");
  await openSignon(page);
  await showSAML(page);

  // Remove every group mapping.
  //
  // Located by the accessible name rather than the visible text, which is the mistake this test made first: the button reads
  // "Remove" and is labelled "Stop mapping <group>", so a text locator matched nothing, nothing became dirty, and the save button
  // stayed disabled - which presents as a click that never resolves rather than as a locator that found nothing.
  const removes = page.getByRole("button", { name: /^Stop mapping / });

  for (let n = await removes.count(); n > 0; n = await removes.count()) {
    await removes.first().click();
  }

  await expect(removes, "no mapping was removed, so this test is not testing an empty mapping").toHaveCount(0);

  await page.getByRole("button", { name: "Save sign-on settings" }).click();

  await expect(page.locator("body")).toContainText(/nobody could sign in/, { timeout: 20_000 });
});
