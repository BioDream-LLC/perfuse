#!/usr/bin/env bash
#
# Stand up an Entra application and a test user, so somebody can sign in to a local Perfuse with a real Microsoft account.
#
# Why this is separate from the tests in internal/saml. Those provision an application, read its published metadata and delete it again,
# which verifies everything up to the point where an assertion would be issued. Entra will not issue one without a human signing in, so the
# last step cannot be automated and the application has to outlive the script. It is named so it can be recognised and removed.
#
# What it leaves behind, and how to remove it:
#
#   perfuse-local-signin          an enterprise application configured for SAML
#   perfuse-test@<tenant>         a cloud user assigned to it
#
# Both are deleted by ./scripts/entra-signin-teardown.sh.
#
# Credentials come from ~/.config/perfuse/entra.env and are never printed.
set -euo pipefail

ENV_FILE="${ENTRA_ENV:-$HOME/.config/perfuse/entra.env}"
if [ ! -f "$ENV_FILE" ]; then
  echo "no credentials at $ENV_FILE" >&2
  exit 1
fi
# shellcheck disable=SC1090
source "$ENV_FILE"

OUT="${1:-$HOME/perfuse-signin}"
ADDR="${PERFUSE_ADDR:-localhost:8543}"
ACS="https://$ADDR/auth/saml/acs"
ENTITY="urn:perfuse:local-signin"
APP_NAME="perfuse-local-signin"
USER_PREFIX="perfuse-test"

mkdir -p "$OUT"

echo "== getting a Graph token"
TOKEN="$(curl -sf -X POST "https://login.microsoftonline.com/$PERFUSE_ENTRA_TENANT_ID/oauth2/v2.0/token" \
  -d "client_id=$PERFUSE_ENTRA_CLIENT_ID" \
  -d "client_secret=$PERFUSE_ENTRA_CLIENT_SECRET" \
  -d "scope=https://graph.microsoft.com/.default" \
  -d "grant_type=client_credentials" | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')"

graph() {
  local method="$1" path="$2" data="${3:-}"
  if [ -n "$data" ]; then
    curl -s -X "$method" -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
      -d "$data" "https://graph.microsoft.com/v1.0$path"
  else
    curl -s -X "$method" -H "Authorization: Bearer $TOKEN" "https://graph.microsoft.com/v1.0$path"
  fi
}

# A write retried while Graph says the object is not there.
#
# An object in Entra is readable before it is writable: a GET of a new service principal returns 200 while a PATCH of the same id returns
# 404. Waiting for the read to succeed proves nothing, so the write is what gets retried.
graph_write() {
  local method="$1" path="$2" data="$3" i
  for i in $(seq 1 25); do
    local code
    code="$(curl -s -o /tmp/gw.out -w '%{http_code}' -X "$method" -H "Authorization: Bearer $TOKEN" \
      -H "Content-Type: application/json" -d "$data" "https://graph.microsoft.com/v1.0$path")"
    if [ "$code" -lt 400 ]; then
      cat /tmp/gw.out
      return 0
    fi
    # 404 is the object itself not having propagated. A 400 saying "Not a valid reference update" is an object the request *refers to* not
    # having propagated - assigning a user created seconds earlier reports exactly that, and the wording names neither the field nor the
    # reason. Both are waited out; anything else will not improve with time.
    if [ "$code" = "404" ] || grep -q "Not a valid reference update" /tmp/gw.out; then
      sleep 3
      continue
    fi

    echo "$method $path returned $code:" >&2
    cat /tmp/gw.out >&2
    return 1
  done
  echo "$method $path still 404 after 75 seconds" >&2
  return 1
}

echo "== removing any previous copy, so this can be run twice"
EXISTING="$(graph GET "/applications?\$filter=displayName%20eq%20'$APP_NAME'&\$select=id" |
  python3 -c 'import sys,json; v=json.load(sys.stdin).get("value",[]); print(v[0]["id"] if v else "")')"
if [ -n "$EXISTING" ]; then
  graph DELETE "/applications/$EXISTING" >/dev/null
  sleep 5
fi

echo "== creating the application"
TEMPLATE="$(graph GET "/applicationTemplates?\$filter=displayName%20eq%20'Custom'" |
  python3 -c 'import sys,json; print(json.load(sys.stdin)["value"][0]["id"])')"

INST="$(graph POST "/applicationTemplates/$TEMPLATE/instantiate" "{\"displayName\":\"$APP_NAME\"}")"

APP_OBJ="$(echo "$INST" | python3 -c 'import sys,json; print(json.load(sys.stdin)["application"]["id"])')"
APP_ID="$(echo "$INST" | python3 -c 'import sys,json; print(json.load(sys.stdin)["application"]["appId"])')"
SP_ID="$(echo "$INST" | python3 -c 'import sys,json; print(json.load(sys.stdin)["servicePrincipal"]["id"])')"

echo "   application $APP_ID"

echo "== configuring SAML"
graph_write PATCH "/servicePrincipals/$SP_ID" '{"preferredSingleSignOnMode":"saml"}' >/dev/null
graph_write PATCH "/applications/$APP_OBJ" \
  "{\"identifierUris\":[\"$ENTITY\"],\"web\":{\"redirectUris\":[\"$ACS\"]}}" >/dev/null

# The signing certificate. Entra creates none by itself and publishes metadata without a key until one exists.
graph_write POST "/servicePrincipals/$SP_ID/addTokenSigningCertificate" \
  "{\"displayName\":\"CN=perfuse-local-signin\",\"endDateTime\":\"$(date -u -v+180d '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -d '+180 days' '+%Y-%m-%dT%H:%M:%SZ')\"}" >/dev/null

echo "== creating the test user"
# The tenant's domain, taken from an existing user rather than from /domains.
#
# /domains needs Domain.Read.All, and asking for another consented permission to learn one string is a worse trade than reading it off a user
# principal name - which User.ReadWrite.All already allows. The part after the final @ is the tenant domain even for a guest account, whose
# name looks like someone_example.com#EXT#@tenant.onmicrosoft.com.
DOMAIN="$(graph GET "/users?\$select=userPrincipalName&\$top=1" | python3 -c 'import sys,json
v = json.load(sys.stdin).get("value", [])
if not v:
    raise SystemExit("the tenant lists no users, so its domain cannot be derived")
print(v[0]["userPrincipalName"].rsplit("@", 1)[1])')"
# A new name each run, rather than deleting and recreating one.
#
# Deleting a user and immediately creating one with the same principal name fails: the deletion has not propagated and Entra refuses the
# duplicate, with the creation error swallowed by the script that did it. A fresh name has no race in it. The teardown script removes every
# perfuse-test-* user, so this does not accumulate.
UPN="$USER_PREFIX-$(date +%s)@$DOMAIN"
PASSWORD="$(openssl rand -base64 18)Aa1!"

USER_JSON="$(python3 - "$UPN" "$PASSWORD" <<'PY'
import json, sys
upn, password = sys.argv[1], sys.argv[2]
print(json.dumps({
    "accountEnabled": True,
    "displayName": "Perfuse Test User",
    "mailNickname": upn.split("@")[0],
    "userPrincipalName": upn,
    # No forced change, so signing in is one step rather than a password reset flow in the middle of a verification.
    "passwordProfile": {"forceChangePasswordNextSignIn": False, "password": password},
}))
PY
)"

USER_RESPONSE="$(graph POST "/users" "$USER_JSON")"
USER_ID="$(echo "$USER_RESPONSE" | python3 -c 'import sys,json
try:
    print(json.load(sys.stdin).get("id",""))
except Exception:
    print("")')"
if [ -z "$USER_ID" ]; then
  # Graph says exactly what was wrong - a password policy, a duplicate name, a missing licence - and swallowing it cost a run.
  echo "could not create the test user. Graph said:" >&2
  echo "$USER_RESPONSE" >&2
  exit 1
fi

echo "== assigning the user to the application"
# The application's own User role.
#
# Not the all-zero GUID, which is documented as "default access" and is what the first version of this fell back to when its filter found
# nothing. Graph answered "Not a valid reference update", which names neither the field nor the reason. An application created from the
# Custom template has a real User role and that is what has to be used.
#
# The filter does not require isEnabled either: that was the bug in the first version, since the field is absent from the projection and a
# missing value read as false, so every role was skipped and the fallback was used.
APP_ROLE="$(graph GET "/servicePrincipals/$SP_ID?\$select=appRoles" |
  python3 -c 'import sys,json
roles = json.load(sys.stdin).get("appRoles", [])
users = [r for r in roles if "User" in r.get("allowedMemberTypes", []) and r.get("displayName") == "User"]
if not users:
    users = [r for r in roles if "User" in r.get("allowedMemberTypes", [])]
if not users:
    raise SystemExit("the application has no role a user can be assigned to")
print(users[0]["id"])')"

graph_write POST "/servicePrincipals/$SP_ID/appRoleAssignedTo" \
  "{\"principalId\":\"$USER_ID\",\"resourceId\":\"$SP_ID\",\"appRoleId\":\"$APP_ROLE\"}" >/dev/null

echo "== waiting for the federation metadata to carry a certificate"
META_URL="https://login.microsoftonline.com/$PERFUSE_ENTRA_TENANT_ID/federationmetadata/2007-06/federationmetadata.xml?appid=$APP_ID"
for i in $(seq 1 40); do
  curl -sf "$META_URL" -o "$OUT/entra-metadata.xml" && grep -q X509Certificate "$OUT/entra-metadata.xml" && break
  sleep 3
done
grep -q X509Certificate "$OUT/entra-metadata.xml" || { echo "metadata never carried a certificate" >&2; exit 1; }

echo "== writing the Perfuse SAML configuration"
python3 - "$OUT" "$ENTITY" "$ACS" <<'PYEOF'
import re, sys

# Regex rather than an XML parser, deliberately.
#
# This machine's Python has no expat, so ElementTree cannot parse anything at all. The document is machine-generated by Entra and the two
# values wanted are unambiguous within it, so matching text is adequate for a setup script. It would not be adequate inside Perfuse, which is
# why ParseIDPMetadata exists and does the job properly.
out, entity, acs = sys.argv[1], sys.argv[2], sys.argv[3]

doc = open(out + "/entra-metadata.xml", encoding="utf-8").read()

# The redirect binding's Location. Attribute order is not guaranteed by anything, so both orders are tried.
sso = ""
for pattern in (
    r'<[^>]*SingleSignOnService[^>]*Binding="urn:oasis:names:tc:SAML:2\.0:bindings:HTTP-Redirect"[^>]*Location="([^"]+)"',
    r'<[^>]*SingleSignOnService[^>]*Location="([^"]+)"[^>]*Binding="urn:oasis:names:tc:SAML:2\.0:bindings:HTTP-Redirect"',
):
    m = re.search(pattern, doc)
    if m:
        sso = m.group(1)
        break

certs = re.findall(r"<[^>]*X509Certificate[^>]*>([^<]+)<", doc)

if not sso:
    raise SystemExit("the metadata document has no HTTP-Redirect SingleSignOnService")
if not certs:
    raise SystemExit("the metadata document has no X509Certificate")

body = re.sub(r"\s+", "", certs[0])
lines = [body[i:i + 64] for i in range(0, len(body), 64)]
pem = "-----BEGIN CERTIFICATE-----\n" + "\n".join(lines) + "\n-----END CERTIFICATE-----\n"

with open(out + "/entra.crt", "w") as f:
    f.write(pem)

config = [
    "# Perfuse SAML sign-in against a real Microsoft Entra tenant.",
    "#",
    "# Written by scripts/entra-signin-setup.sh from the tenant's own published metadata, so the certificate below came out of Entra's",
    "# document rather than being pasted by hand.",
    "entity_id: " + entity,
    "acs_url: " + acs,
    "idp_sso_url: " + sso,
    "idp_cert_file: " + out + "/entra.crt",
    "label: Sign in with Microsoft",
    "# Every assertion is tied to a request this server issued; unsolicited responses are refused.",
    "allow_unsolicited: false",
    "# A first sign-in has no local account yet, and without this there is nobody to become.",
    "create_users: true",
    "groups_attribute: http://schemas.microsoft.com/ws/2008/06/identity/claims/groups",
    "roles:",
    "  admin:",
    "    - perfuse-admins",
    "",
]

with open(out + "/saml.yaml", "w") as f:
    f.write("\n".join(config))

print("   sso:", sso)
print("   certificate written to", out + "/entra.crt")
PYEOF

cat > "$OUT/signin.txt" <<EOF
Sign in to Perfuse with Microsoft Entra
=======================================

Start the server:

  cd ~/perfuse
  ./perfuse serve -addr $ADDR \\
    -tls-cert $OUT/localhost.crt \\
    -tls-key $OUT/localhost.key \\
    -saml $OUT/saml.yaml \\
    -db $OUT/perfuse.db \\
    -channels $OUT/channels

Then open https://$ADDR and choose "Sign in with Microsoft".

The browser will warn about the certificate, because it is self-signed. Continue past it.

Sign in as:

  $UPN
  $PASSWORD

Afterwards, remove the application and the user:

  ./scripts/entra-signin-teardown.sh
EOF

chmod 600 "$OUT/signin.txt"

echo
echo "== done"
echo "   application: $APP_NAME ($APP_ID)"
echo "   user:        $UPN"
echo "   credentials and instructions: $OUT/signin.txt"
