import { test, expect } from "@playwright/test";
import { openTab } from "./nav";

// The MCP server, as an AI agent uses it: a token, the handshake, the tools its role allows, and every call audited.

const headers = { "X-Perfuse-Request": "1" };

async function setMCP(page: import("@playwright/test").Page, on: boolean) {
  const r = await page.request.put("/api/settings/values", { headers, data: { changes: { "security.mcp": on } } });
  expect(r.ok(), await r.text()).toBe(true);
}

test("an agent connects with a token, reads within its role, and is refused what it may not do", async ({ page, playwright, baseURL }) => {
  await page.goto("/");
  const label = `e2e-agent-${Date.now()}`;
  const issued = await page.request.post("/api/tokens", { headers, data: { label, role: "viewer" } });
  expect(issued.ok(), await issued.text()).toBe(true);
  const token = (await issued.json()).token as string;

  const agent = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${token}` } });
  const call = async (method: string, params?: unknown, id = 1) => {
    const r = await agent.post("/mcp", {
      headers: { "Content-Type": "application/json", Accept: "application/json, text/event-stream", "MCP-Protocol-Version": "2025-06-18" },
      data: { jsonrpc: "2.0", id, method, ...(params ? { params } : {}) },
    });
    return r;
  };

  await setMCP(page, false);
  expect((await call("ping")).status(), "switched off").toBe(404);
  await setMCP(page, true);
  try {
    const init = await call("initialize", { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "e2e-agent", version: "1" } });
    expect(init.status()).toBe(200);
    expect(init.headers()["mcp-session-id"]).toBeTruthy();
    expect((await init.json()).result.serverInfo.name).toBe("perfuse");

    const list = await (await call("tools/list", undefined, 2)).json();
    const names = list.result.tools.map((t: { name: string }) => t.name);
    expect(names).toContain("search_messages");
    expect(names).toContain("fhir_search");
    expect(names, "a viewer token without mcp:write is offered no tool that acts").not.toContain("stop_channel");

    const channels = await (await call("tools/call", { name: "list_channels", arguments: {} }, 3)).json();
    expect(channels.result.isError).toBe(false);

    const refused = await (await call("tools/call", { name: "stop_channel", arguments: { name: "anything" } }, 4)).json();
    expect(refused.result.isError).toBe(true);
    expect(refused.result.content[0].text).toContain("admin role");

    // A browser session is not an agent's credential.
    const asPerson = await page.request.post("/mcp", { headers: { ...headers, "Content-Type": "application/json" }, data: { jsonrpc: "2.0", id: 1, method: "ping" } });
    expect(asPerson.status()).toBe(401);

    // Every tool call is in Activity under the token's name.
    const audit = await (await page.request.get("/api/audit?limit=50")).json();
    const entries = JSON.stringify(audit);
    expect(entries).toContain(`token:${label}`);
    expect(entries).toContain("mcp.tool");

    // The Users screen shows MCP on, its address and its tools, and offers the scope when issuing a token.
    await openTab(page, "Users");
    const card = page.getByTestId("mcp-card");
    await expect(card).toContainText("on");
    await expect(card).toContainText("/mcp");
    await expect(card).toContainText("stop_channel");
    await page.getByLabel("What is it for").fill(`${label}-writer`);
    await page.getByText("Let an AI agent act with it").click();
    await page.getByRole("button", { name: "Issue token" }).click();
    await expect(page.getByTestId("issued-token")).toBeVisible();
    await page.getByRole("button", { name: "I have saved it" }).click();
    await expect(page.getByText("AI agents may act (mcp:write)")).toBeVisible();
  } finally {
    await setMCP(page, false);
    await agent.dispose();
    await page.request.delete(`/api/tokens/${encodeURIComponent(label)}`, { headers });
    await page.request.delete(`/api/tokens/${encodeURIComponent(label + "-writer")}`, { headers });
  }
});
