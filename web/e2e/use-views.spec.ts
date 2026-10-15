import { test, expect } from "@playwright/test";
import { openTab } from "./nav";
import { smartToken } from "./smart";

// SQL on FHIR: a view run over this server's FHIR store from the screen, saved, and then run by the FHIR endpoint's
// $sql-run with a SMART token, as an analytics tool would.

test("a view runs over the FHIR store, is saved, and $sql-run runs it", async ({ page }) => {
  const faults: string[] = [];
  page.on("pageerror", (e) => faults.push(e.message));
  await page.goto("/");
  const token = await smartToken(page.request);
  const fhir = { Authorization: `Bearer ${token}`, "Content-Type": "application/fhir+json" };
  for (const [id, family, gender] of [["e2e-sof-1", "DOE", "female"], ["e2e-sof-2", "SMITH", "male"]]) {
    const r = await page.request.put(`/fhir/Patient/${id}`, {
      headers: fhir,
      data: { resourceType: "Patient", id, gender, birthDate: "1970-01-01", name: [{ use: "official", family, given: ["Jo"] }],
        address: [{ city: "Springfield", state: "IL" }] },
    });
    expect(r.ok(), await r.text()).toBe(true);
  }

  await openTab(page, "SQL on FHIR");
  await page.getByTestId("view-picks").getByRole("button", { name: "Patient demographics" }).click();
  await page.getByRole("button", { name: "Run", exact: true }).click();
  const rows = page.getByTestId("view-rows");
  await expect(rows).toContainText("e2e-sof-1");
  await expect(rows).toContainText("SMITH");
  await expect(rows).toContainText("Springfield");

  // One patient only.
  await page.getByLabel("Run for one patient").fill("e2e-sof-2");
  await page.getByRole("button", { name: "Run", exact: true }).click();
  await expect(rows).toContainText("SMITH");
  await expect(rows).not.toContainText("DOE");

  // Over pasted resources instead of the store.
  await page.getByLabel("Resources to run over (optional)").fill('{"resourceType":"Patient","id":"pasted-1","gender":"other"}');
  await page.getByLabel("Run for one patient").fill("");
  await page.getByRole("button", { name: "Run", exact: true }).click();
  await expect(rows).toContainText("pasted-1");
  await expect(page.getByText(/from Patient resources pasted above/)).toBeVisible();
  await page.getByLabel("Resources to run over (optional)").fill("");

  // A download is a CSV file.
  const dl = page.waitForEvent("download");
  await page.getByRole("button", { name: "Download CSV" }).click();
  expect((await dl).suggestedFilename()).toBe("patient_demographics.csv");

  // A broken view says why.
  const editor = page.getByLabel("ViewDefinition (JSON)");
  const good = await editor.inputValue();
  await editor.fill(good.replace('"path": "gender"', '"path": "gender.("'));
  await page.getByRole("button", { name: "Run", exact: true }).click();
  await expect(page.getByText(/is not valid/)).toBeVisible();
  await editor.fill(good);

  // Saved, then run by $sql-run.
  await page.getByLabel("Save as").fill("e2e_demographics");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("view-saved")).toContainText("ViewDefinition/e2e_demographics");
  await expect(page.getByTestId("views-saved")).toContainText("e2e_demographics");
  const run = await page.request.get("/fhir/$sql-run?subjectReference=ViewDefinition/e2e_demographics&_format=csv&patient=Patient/e2e-sof-1", {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(run.status(), await run.text()).toBe(200);
  expect(run.headers()["content-type"]).toBe("text/csv");
  const csv = await run.text();
  expect(csv.split("\n")[0]).toBe("id,gender,birth_date,deceased,family,given,city,state,postal_code");
  expect(csv).toContain("e2e-sof-1,female,1970-01-01,,DOE,Jo,Springfield,IL,");
  expect(csv).not.toContain("SMITH");
  const cap = await (await page.request.get("/fhir/metadata")).json();
  expect(JSON.stringify(cap)).toContain("OperationDefinition/SQLRun");

  page.once("dialog", (d) => d.accept());
  await page.getByTestId("views-saved").getByRole("button", { name: "Delete" }).click();
  await expect(page.getByTestId("views-saved")).toHaveCount(0);
  expect(faults, faults.join("\n")).toEqual([]);
});
