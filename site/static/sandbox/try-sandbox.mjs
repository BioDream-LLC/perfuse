// try-sandbox.mjs: one CRD -> DTR -> PAS round against the free Perfuse payer sandbox, test.perfuse.health.
//
//   node try-sandbox.mjs            (Node 18 or newer; no packages to install)
//
// It signs with the sandbox's published test keys (https://perfuse.health/sandbox/private-jwks.json). They are public on
// purpose: everything they reach is invented test data. Never put a real patient's data into the sandbox.
import crypto from 'node:crypto';

const SANDBOX = 'https://test.perfuse.health';
const ISSUER = 'https://perfuse.health/sandbox';
const HCPCS = 'http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets';

const { keys } = await (await fetch(ISSUER + '/private-jwks.json')).json();
const jwk = keys.find((k) => k.alg === 'ES384');
const key = crypto.createPrivateKey({ key: jwk, format: 'jwk' });
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString('base64url');

// sign makes a short-lived ES384 JWT with the sandbox key.
function sign(claims) {
  const now = Math.floor(Date.now() / 1000);
  const body = b64({ alg: 'ES384', typ: 'JWT', kid: jwk.kid }) + '.' +
    b64({ iat: now, exp: now + 240, jti: crypto.randomUUID(), ...claims });
  const sig = crypto.sign('sha384', Buffer.from(body), { key, dsaEncoding: 'ieee-p1363' });
  return body + '.' + sig.toString('base64url');
}

// 1. A SMART Backend Services token (client_credentials with a signed assertion).
const tokenUrl = SANDBOX + '/auth/token';
const tr = await fetch(tokenUrl, {
  method: 'POST',
  headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
  body: new URLSearchParams({
    grant_type: 'client_credentials',
    scope: 'system/*.rs system/Claim.c',
    client_assertion_type: 'urn:ietf:params:oauth:client-assertion-type:jwt-bearer',
    client_assertion: sign({ iss: 'sandbox', sub: 'sandbox', aud: tokenUrl }),
  }),
});
const token = await tr.json();
if (!tr.ok) throw new Error('token: ' + JSON.stringify(token));
console.log('1. token: ok, scopes', token.scope);
const fhir = (path, init = {}) => fetch(SANDBOX + '/fhir/' + path, {
  ...init,
  headers: { Authorization: 'Bearer ' + token.access_token, Accept: 'application/fhir+json',
    'Content-Type': 'application/fhir+json', ...init.headers },
});

// The test plan's member: Amy Shaw, subscriber id 123. Her order: a portable oxygen system (HCPCS E0431), which needs approval.
// (Her stationary oxygen, E0424, is already approved: send E0424 instead to see the plan say so.)
const patient = { resourceType: 'Patient', id: 'pmember', name: [{ family: 'Shaw', given: ['Amy'] }],
  gender: 'female', birthDate: '1987-02-20' };
const coverage = { resourceType: 'Coverage', id: 'cmember', status: 'active', subscriberId: '123',
  beneficiary: { reference: 'Patient/pmember' }, payor: [{ display: 'Springfield Health Plan' }] };
const order = { resourceType: 'ServiceRequest', id: 'oxygen-1', status: 'draft', intent: 'order',
  code: { coding: [{ system: HCPCS, code: 'E0431', display: 'Portable gaseous oxygen system, rental' }] },
  subject: { reference: 'Patient/pmember' }, authoredOn: new Date().toISOString().slice(0, 10),
  insurance: [{ reference: 'Coverage/cmember' }] };

// 2. CRD: the order-sign hook, as an EHR sends it when the order is signed.
const hookUrl = SANDBOX + '/cds-services/crd-order-sign';
const hr = await fetch(hookUrl, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + sign({ iss: ISSUER, aud: hookUrl }) },
  body: JSON.stringify({
    hook: 'order-sign', hookInstance: crypto.randomUUID(), fhirServer: 'https://ehr.example/fhir',
    context: { userId: 'Practitioner/example', patientId: 'pmember',
      draftOrders: { resourceType: 'Bundle', type: 'collection', entry: [{ resource: order }] } },
    prefetch: { patient, coverage: { resourceType: 'Bundle', type: 'searchset', entry: [{ resource: coverage }] } },
  }),
});
const hook = await hr.json();
if (!hr.ok) throw new Error('CRD: ' + JSON.stringify(hook));
console.log('2. CRD cards:');
for (const c of hook.cards || []) console.log('   -', c.summary);
for (const a of hook.systemActions || []) console.log('   - system action:', a.type, a.description || '');

// 3. DTR: the questionnaire package. CRD's system action returned the order with its coverage-information (which names the
// questionnaire), so that updated order is what goes to DTR, as an EHR would send it.
const updated = (hook.systemActions || []).map((a) => a.resource).find((r) => r && r.resourceType === 'ServiceRequest') || order;
const qp = await fhir('Questionnaire/$questionnaire-package', { method: 'POST', body: JSON.stringify({
  resourceType: 'Parameters', parameter: [
    { name: 'coverage', resource: coverage },
    { name: 'order', resource: updated },
  ] }) });
const pkg = await qp.json();
console.log('3. DTR package:', qp.status, (pkg.parameter || []).filter((p) => p.name === 'PackageBundle' || p.name === 'packagebundle')
  .map((p) => (p.resource.entry || []).map((e) => e.resource.resourceType + (e.resource.title ? ' "' + e.resource.title + '"' : '')).join(', ')).join(' | ')
  || JSON.stringify(pkg).slice(0, 300));

// 4. PAS: the PAS guide's own referral example (a consultation, X12 service type 3), which the test plan approves.
const example = await (await fetch('https://hl7.org/fhir/us/davinci-pas/Bundle-ReferralAuthorizationBundleExample.json')).text();
const sr = await fhir('Claim/$submit', { method: 'POST', body: example });
const answer = await sr.json();
const cr = (answer.entry || []).map((e) => e.resource).find((r) => r.resourceType === 'ClaimResponse');
const actions = JSON.stringify(cr?.item || []).match(/"code":"(A[1-6])"/g) || [];
console.log('4. PAS $submit:', sr.status, cr ? 'outcome ' + cr.outcome + ', review action ' +
  (actions.map((a) => a.slice(8, -1)).join(', ') || 'none') + (cr.preAuthRef ? ', authorization ' + cr.preAuthRef : '')
  : JSON.stringify(answer).slice(0, 300));
