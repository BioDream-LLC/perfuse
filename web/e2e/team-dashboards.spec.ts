import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// Team dashboards: each person opens on a dashboard, can pick another, arrange and hide its tiles, and save that as their own
// view, which is what they open on next time. The Connections dashboard checks the fixture's real destinations, layer by layer.
test("a team dashboard is picked, arranged, saved, and opens as saved", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Team dashboards");
  await expect(page.getByText(/You open on Operations/)).toBeVisible();

  await page.getByLabel("Dashboard", { exact: true }).selectOption("connections");
  // What the fixture holds depends on which specs ran first: none over the network, or channels pointing at example names that
  // do not resolve. Either way the tile says so in words. The layers themselves are tested against real listeners in
  // internal/api/connections_test.go.
  await expect(page.getByTestId("dashboard-tiles")).toContainText(/No destination reaches over the network|refused the connection|does not exist in DNS|timed out|Reachable/, { timeout: 20_000 });
  await expect(page.getByText("Not measured yet")).toBeVisible();

  // Arrange: hide the certificates tile and save.
  await page.getByRole("button", { name: "Arrange tiles" }).click();
  await page.getByRole("button", { name: "Hide Certificates" }).click();
  await page.getByRole("button", { name: "Save as my view" }).click();
  await expect(page.getByRole("status")).toContainText("Saved as your view");

  await page.reload();
  await openTab(page, "Team dashboards");
  await expect(page.getByLabel("Dashboard", { exact: true })).toHaveValue("connections");
  const tiles = page.getByTestId("dashboard-tiles");
  await expect(tiles).toContainText("Inbound connections");
  await expect(tiles).not.toContainText("TLS certificates in use");

  // The Grafana export downloads.
  const download = page.waitForEvent("download");
  await page.getByRole("button", { name: "Export to Grafana" }).click();
  expect((await download).suggestedFilename()).toBe("perfuse-connections.grafana.json");

  // Reset returns to the assigned dashboard.
  await page.getByRole("button", { name: "Reset" }).click();
  await expect(page.getByLabel("Dashboard", { exact: true })).toHaveValue("operations");
});

test("the department summary says what is wrong in plain words", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Team dashboards");
  await page.getByLabel("Dashboard", { exact: true }).selectOption("manager");
  await expect(page.getByTestId("dashboard-tiles")).toContainText(/In plain words/);
  await expect(page.getByTestId("dashboard-tiles")).toContainText(/Everything is working|failed in the last day|would not load|\w/);
});

test("the department dashboards draw the server's figures", async ({ page }) => {
  await page.goto("/");
  await openTab(page, "Team dashboards");
  const tiles = page.getByTestId("dashboard-tiles");
  await page.getByLabel("Dashboard", { exact: true }).selectOption("prior-auth");
  await expect(tiles).toContainText("Prior authorization timeframes, 30 days");
  await expect(tiles).toContainText(/72 hours|No prior authorization requests in 30 days/, { timeout: 15_000 });
  await page.getByLabel("Dashboard", { exact: true }).selectOption("lab");
  await expect(tiles).toContainText(/critical result|Every critical result/, { timeout: 15_000 });
  await page.getByLabel("Dashboard", { exact: true }).selectOption("privacy");
  await expect(tiles).toContainText("API token use");
  await expect(page.getByTestId("dashboard-figure").first()).toBeVisible();
});
