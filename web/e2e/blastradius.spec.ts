import { test, expect } from "@playwright/test";
import { openTab } from "./nav";
import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));

// The blast radius view, driven with shared mapping tables that several channels use.
//
// This tab had only ever been seen empty. The API and the interface behind it were both complete and well built - what each table
// affects, how many channels, which fields, who decided the mapping and when - and the first time real tables went through it the whole
// console rendered nothing at all.
//
// A table loaded by a channel and referenced by none left usedBy and paths as nil, which marshals as JSON null, and the interface reads
// .length on them. An exception thrown during a render does not blank the panel, it blanks the application: no message, no clue.
//
// That case is not obscure. The endpoint exists partly to surface it, because an unreferenced mapping table is either a misspelling or
// dead weight and both are worth knowing.
test("the blast radius view survives a table that nothing uses", async ({ page }) => {
  test.setTimeout(120_000);

  const state = JSON.parse(readFileSync(join(here, ".state.json"), "utf8")) as { dir: string };
  const channels = join(state.dir, "channels");

  mkdirSync(channels, { recursive: true });

  writeFileSync(
    join(channels, "shared.codeset.yaml"),
    [
      "tables:",
      "  - name: sex",
      "    describes: sending system sex codes into ours",
      "    decided_by: the 2019 migration",
      '    decided_on: "2019-04-02"',
      "    source: the interface specification, section 4",
      "    default: U",
      "    entries:",
      "      - from: M",
      "        to: MALE",
      "      - from: F",
      "        to: FEMALE",
      "      - from: A",
      "        to: OTHER",
      "        why: the lab sends A for ambiguous",
      '        since: "2021-06-01"',
      "  - name: department",
      "    describes: sending department codes into ours",
      "    strict: true",
      "    entries:",
      "      - from: ICU",
      "        to: CRIT",
      // The table that broke it. Loaded, referenced by nobody.
      "  - name: orphan",
      "    describes: a table nobody references any more",
      "    entries:",
      "      - from: X",
      "        to: Y",
      "",
    ].join("\n"),
  );

  // Three channels on the sex table and one on department, so the counts are distinguishable from each other and from one.
  for (const [name, paths] of [
    ["admissions", ["PID-8", "PV1-3"]],
    ["results", ["PID-8"]],
    ["billing", ["PID-8"]],
  ] as [string, string[]][]) {
    const steps = paths.map((p) =>
      ["  - map:", `      path: ${p}`, `      use: ${p === "PID-8" ? "sex" : "department"}`].join("\n"),
    );

    writeFileSync(
      join(channels, `${name}.yaml`),
      [
        `name: ${name}`,
        "tables:",
        "  - shared.codeset.yaml",
        "source:",
        "  type: mllp",
        "  listen: 127.0.0.1:0",
        "transformations:",
        ...steps,
        "destinations:",
        "  - name: archive",
        "    type: file",
        `    dir: ${join(state.dir, "out")}`,
        "",
      ].join("\n"),
    );
  }

  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(String(e)));
  page.on("console", (m) => {
    if (m.type() === "error") problems.push(m.text());
  });

  await page.goto("/");
  await openTab(page, "Tables");

  // main must exist. Asserted explicitly because its absence is what the failure looked like: an exception during a render leaves
  // an empty body, so every assertion about content times out and none of them says why.
  await expect(page.locator("main")).toBeVisible({ timeout: 15_000 });

  const main = page.locator("main");

  // Assertions that retry, because the panel fetches its data after the tab renders. Reading textContent once passed while the
  // panel was still empty in my first version of this test, which would have been a test that could only fail by timeout.
  //
  // The reach of each table, which is the whole point of the view.
  await expect(main, "the widest-reaching table does not report how many channels it affects").toContainText(
    "changes 3 channels",
    { timeout: 15_000 },
  );
  await expect(
    main,
    "the field a table writes is not shown, and 'three channels' without 'PID-8' is not actionable",
  ).toContainText("PID-8");

  // Provenance, because a mapping nobody can explain becomes one nobody dares change.
  await expect(main, "who decided the mapping was not carried through").toContainText("2019 migration");

  // And the unreferenced table is named as such rather than omitted.
  await expect(
    main,
    "a table used by nothing is not reported, though that is either a misspelling or dead weight",
  ).toContainText("referenced by no channel");

  expect(problems, "the console reported errors while rendering the blast radius view").toEqual([]);
});
