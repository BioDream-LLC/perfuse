import { test, expect } from "@playwright/test";
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// Every route that needs more than a viewer, called by a viewer, and by nobody at all.
//
// The role a route requires is written next to it in the Go source - s.require(store.RoleAdmin, ...) - so that is the
// specification, and this checks the running server against it: a viewer is refused (403) everything above viewer, and
// a request with no session is refused (401) everything that requires one. A route that answers 200 to a viewer when
// the source says admin is a privilege escalation, and nothing else in the suite would see it, because the suite signs
// in as admin.

const here = dirname(fileURLToPath(import.meta.url));
const apiDir = join(here, "..", "..", "internal", "api");
const source = readdirSync(apiDir)
  .filter((f) => f.endsWith(".go") && !f.endsWith("_test.go"))
  .map((f) => readFileSync(join(apiDir, f), "utf8"))
  .join("\n");

const guarded = [...source.matchAll(/mux\.Handle\("(GET|POST|PUT|DELETE|PATCH) (\/api\/[^"]*)", s\.require\(store\.Role(\w+)/g)].map(
  (m) => ({ method: m[1], path: m[2].replace(/\{[^}]+\}/g, "no-such-thing-9f3a"), role: m[3] }),
).filter((r) => !/\/logout$/.test(r.path) && r.path !== "/api/events"); // events is the live stream: 200, then it never ends

test("a viewer is refused every route above viewer, and nobody is refused every guarded route", async ({ browser, baseURL }) => {
  test.setTimeout(300_000);
  expect(guarded.length, "the route pattern matched nothing").toBeGreaterThan(80);

  // Its own users, signed in fresh. Calling every route as the suite's shared admin session once included /api/logout,
  // which ended that session for every test after this one: all 24 hostile-input tests then failed on a missing
  // navigation bar. So this probe never touches the shared session, and never calls logout at all.
  const shared = await browser.newContext({ baseURL, storageState: "./e2e/.auth/admin.json" });
  const stamp = Date.now();
  const password = "Authz-password-9f3a-long";
  for (const role of ["admin", "viewer"]) {
    const made = await shared.request.post("/api/users", {
      headers: { "X-Perfuse-Request": "1" },
      data: { username: `authz-${role}-${stamp}`, password, role },
    });
    expect(made.ok(), await made.text()).toBeTruthy();
  }
  await shared.close();
  const signIn = async (role: string) => {
    const ctx = await browser.newContext({ baseURL, storageState: { cookies: [], origins: [] } });
    const res = await ctx.request.post("/api/login", {
      headers: { "X-Perfuse-Request": "1" },
      data: { username: `authz-${role}-${stamp}`, password },
    });
    expect(res.ok(), await res.text()).toBeTruthy();
    return ctx;
  };
  const admin = await signIn("admin");

  const viewer = await signIn("viewer");
  const anon = await browser.newContext({ baseURL, storageState: { cookies: [], origins: [] } });

  const problems: string[] = [];
  for (const r of guarded) {
    const opts = { method: r.method, headers: { "X-Perfuse-Request": "1", "Content-Type": "application/json" }, data: r.method === "GET" ? undefined : "{}", failOnStatusCode: false };
    // Some routes are registered only when their feature is configured - passkeys, for one. Unregistered, the path is
    // unclaimed and answers the same JSON 404 to everybody, admin included: nothing is reachable, so there is nothing to
    // authorise. Told apart by asking as admin first.
    const asAdmin = await admin.request.fetch(r.path, opts);
    const nobody = await anon.request.fetch(r.path, opts);
    if (asAdmin.status() === 404 && nobody.status() === 404) continue;
    if (nobody.status() !== 401) problems.push(`no session: ${r.method} ${r.path} (${r.role}) -> ${nobody.status()}, want 401`);
    if (r.role !== "Viewer") {
      const v = await viewer.request.fetch(r.path, opts);
      if (v.status() !== 403) problems.push(`viewer: ${r.method} ${r.path} (${r.role}) -> ${v.status()}, want 403`);
    }
  }
  await admin.close();
  console.log(`checked ${guarded.length} guarded routes`);
  expect(problems, problems.join("\n")).toEqual([]);
});
