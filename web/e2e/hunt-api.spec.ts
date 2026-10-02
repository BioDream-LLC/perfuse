import { test, expect } from "@playwright/test";
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// Every API route, given input nobody means to send.
//
// The interface only ever sends well-formed requests, so everything else the sweeps do exercises the
// happy path of the API. A proxy, a script or a hostile user sends the rest. The properties checked are
// the ones the API promises everywhere: a refusal is a 4xx rather than a 500, and every /api answer is
// JSON - an HTML page or an empty body is what an unmatched route used to return, and a client cannot
// tell that from success.
//
// Routes are read from the Go source, so a route added tomorrow is probed tomorrow. Path parameters are
// filled with a name that exists nowhere, which is what makes the destructive routes safe to call: there
// is nothing for them to destroy. Logout is excluded because it ends the session the probe runs in.

const here = dirname(fileURLToPath(import.meta.url));
const apiDir = join(here, "..", "..", "internal", "api");
const source = readdirSync(apiDir)
  .filter((f) => f.endsWith(".go") && !f.endsWith("_test.go"))
  .map((f) => readFileSync(join(apiDir, f), "utf8"))
  .join("\n");

const routes = [...source.matchAll(/mux\.Handle(?:Func)?\("(GET|POST|PUT|DELETE|PATCH) (\/api\/[^"]*)"/g)]
  .map((m) => ({ method: m[1], path: m[2].replace(/\{[^}]+\}/g, "no-such-thing-9f3a") }))
  // /api/events is the live event stream: it answers 200 and then never ends, which is its job. It is the only
  // handler that sets text/event-stream (checked with grep when this was written), and it is excluded rather
  // than given a short timeout, because a timeout would also swallow a real hang anywhere else.
  .filter((r) => r.path !== "/api/logout" && r.path !== "/api/events");

const bodies: [string, string | undefined][] = [
  ["no body", undefined],
  ["truncated JSON", "{"],
  ["wrong type", "[1,2,3]"],
  ["empty object", "{}"],
  ["nulls", '{"name":null,"message":null,"x12":null,"id":null}'],
  ["huge string", JSON.stringify({ message: "A".repeat(3_000_000), x12: "B".repeat(3_000_000) })],
];

test("no API route answers 500 or non-JSON to hostile input", async ({ page }) => {
  test.setTimeout(600_000);
  expect(routes.length, "the route pattern matched nothing").toBeGreaterThan(100);
  await page.goto("/");

  const failures: string[] = [];
  let calls = 0;
  for (const r of routes) {
    const tries = r.method === "GET" ? [["", undefined] as [string, string | undefined]] : bodies;
    for (const [label, body] of tries) {
      calls++;
      const res = await page.request.fetch(r.path, {
        method: r.method,
        headers: { "X-Perfuse-Request": "1", ...(body !== undefined ? { "Content-Type": "application/json" } : {}) },
        data: body,
        failOnStatusCode: false,
        timeout: 30_000,
      });
      const type = res.headers()["content-type"] ?? "";
      const status = res.status();
      // Streams and file downloads are legitimately not JSON when they succeed.
      const binaryOK = status < 300 && /event-stream|pdf|octet-stream|zip|text\/plain|text\/csv|text\/markdown|x-yaml|image\//.test(type);
      if (status >= 500) failures.push(`${r.method} ${r.path} [${label}] -> ${status} ${(await res.text()).slice(0, 120)}`);
      else if (!type.includes("json") && !binaryOK) failures.push(`${r.method} ${r.path} [${label}] -> ${status} ${type || "no content type"}`);
    }
  }
  console.log(`probed ${routes.length} routes with ${calls} requests`);
  expect(failures, failures.join("\n")).toEqual([]);
});
