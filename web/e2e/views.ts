import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// Every view the interface has, read from the interface itself.
//
// Each sweep used to keep its own hand-written list, and the lists drifted: three of them had never
// included TEFCA or the AI Mapper, and none included the two views added after they were written. A sweep
// over "every view" that silently skips four is worse than one that admits it covers seventeen, because
// the claim is what people rely on. So the list is derived from App.tsx, where the navigation is defined,
// and a view added there is swept the day it appears.
//
// Read from source rather than from the rendered navigation because several specs need the list before a
// page exists (to name one test per view). The count check below is what stops a change to App.tsx's
// shape from turning this into an empty list and every sweep into a silent pass.
const here = dirname(fileURLToPath(import.meta.url));
const app = readFileSync(join(here, "..", "src", "App.tsx"), "utf8");

// The e2e server signs in as an administrator, so views for the platform role (a shared server's operator) are left out:
// that account is not shown them, and saying it was would be the drifting claim this file exists to prevent.
export const VIEWS: string[] = [
  ...app.matchAll(/id: '[a-z0-9]+',\s*\n\s*label: '([^']+)',\s*\n\s*minRole: '([a-z]+)'/g),
]
  .filter((m) => m[2] !== "platform")
  .map((m) => m[1]);

if (VIEWS.length < 20) {
  throw new Error(`views.ts found only ${VIEWS.length} views in App.tsx; the pattern no longer matches how tabs are declared`);
}
