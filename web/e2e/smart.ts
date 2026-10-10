import { createSign, randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";
import type { APIRequestContext } from "@playwright/test";

// smartToken gets a SMART Backend Services access token from Perfuse's own authorization server, as the e2e-backend client in
// setup.ts. The FHIR endpoint takes SMART tokens only while SMART is configured (the default), so a test that calls /fhir uses
// this rather than a console API token, the way a payer's own systems would.
export async function smartToken(request: APIRequestContext, scope = "system/*.*"): Promise<string> {
  const keyFile = process.env.PERFUSE_E2E_SMART_KEY;
  if (!keyFile) throw new Error("PERFUSE_E2E_SMART_KEY is not set: e2e/setup.ts writes it");
  const base = process.env.PERFUSE_E2E_URL!.replace(/\/$/, "");
  const tokenURL = `${base}/auth/token`;

  const b64 = (v: object | Buffer) => (Buffer.isBuffer(v) ? v : Buffer.from(JSON.stringify(v))).toString("base64url");
  const now = Math.floor(Date.now() / 1000);
  const head = b64({ alg: "RS384", typ: "JWT", kid: "e2e" });
  const body = b64({ iss: "e2e-backend", sub: "e2e-backend", aud: tokenURL, jti: randomUUID(), iat: now, exp: now + 240 });
  const sig = createSign("RSA-SHA384").update(`${head}.${body}`).sign(readFileSync(keyFile, "utf8"));
  const assertion = `${head}.${body}.${b64(sig)}`;

  const res = await request.post(tokenURL, {
    form: {
      grant_type: "client_credentials",
      scope,
      client_assertion_type: "urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
      client_assertion: assertion,
    },
  });
  if (!res.ok()) throw new Error(`the token request was refused: ${res.status()} ${await res.text()}`);
  return (await res.json()).access_token as string;
}
