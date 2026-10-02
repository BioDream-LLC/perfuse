#!/usr/bin/env bash
# Configures a Keycloak realm as a SAML identity provider for Perfuse.
#
# Written as a script rather than done by hand so the verification can be repeated. A verification nobody can run again is a claim
# rather than a check, and this one has to survive being re-run after every change to the SAML code.
#
# Creates: a realm, a SAML client whose entity id and reply URL match Perfuse's, a group, a user in it, and a group membership
# mapper that sends the group names as a "groups" attribute. That mapper is the part sites forget, and its absence looks exactly
# like a broken role mapping.
set -euo pipefail

KC=http://127.0.0.1:8080
REALM=perfuse
ENTITY_ID=https://perfuse.test
# The reply URL has to be reachable by the browser, not merely agreed on as a string.
#
# The entity id is compared and never fetched, so any stable URI will do. The reply URL is different: the identity provider
# renders a form pointing at it and the browser submits it, so a value nobody can resolve means the response is never delivered
# and nothing appears in any log at either end. That failure is silent from both sides, which is what made it worth a comment.
ACS=${PERFUSE_ACS:-http://127.0.0.1:8099/auth/saml/acs}

token() {
  curl -s -X POST "$KC/realms/master/protocol/openid-connect/token" \
    -d client_id=admin-cli -d username=admin -d password=admin -d grant_type=password |
    python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])'
}

T=$(token)

api() {
  local method=$1 path=$2
  shift 2
  curl -s -X "$method" "$KC/admin/realms$path" \
    -H "Authorization: Bearer $T" -H "Content-Type: application/json" "$@"
}

echo "== realm"
api DELETE "/$REALM" >/dev/null 2>&1 || true
curl -s -X POST "$KC/admin/realms" -H "Authorization: Bearer $T" -H "Content-Type: application/json" \
  -d "{\"realm\":\"$REALM\",\"enabled\":true}" >/dev/null
echo "created $REALM"

echo "== saml client"
api POST "/$REALM/clients" -d "$(
  cat <<JSON
{
  "clientId": "$ENTITY_ID",
  "protocol": "saml",
  "enabled": true,
  "redirectUris": ["$ACS"],
  "attributes": {
    "saml.assertion.signature": "true",
    "saml.server.signature": "false",
    "saml.client.signature": "false",
    "saml_name_id_format": "email",
    "saml_assertion_consumer_url_post": "$ACS",
    "saml_force_name_id_format": "true"
  }
}
JSON
)" >/dev/null
echo "created client $ENTITY_ID"

CLIENT_UUID=$(api GET "/$REALM/clients?clientId=$(python3 -c "import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1],safe=''))" "$ENTITY_ID")" |
  python3 -c 'import sys,json; print(json.load(sys.stdin)[0]["id"])')

echo "== group membership mapper"
# Sends group names in an attribute called groups, unqualified.
#
# full.path false matters: with it on, Keycloak sends "/perfuse-editors" and a mapping written as "perfuse-editors" matches nothing.
# That is a one-character difference between working and refusing everybody, and it is invisible in the assertion unless you look.
api POST "/$REALM/clients/$CLIENT_UUID/protocol-mappers/models" -d '{
  "name": "groups",
  "protocol": "saml",
  "protocolMapper": "saml-group-membership-mapper",
  "config": {
    "attribute.name": "groups",
    "attribute.nameformat": "Basic",
    "single": "false",
    "full.path": "false"
  }
}' >/dev/null
echo "added groups mapper"

echo "== email mapper"
api POST "/$REALM/clients/$CLIENT_UUID/protocol-mappers/models" -d '{
  "name": "email",
  "protocol": "saml",
  "protocolMapper": "saml-user-property-mapper",
  "config": {
    "user.attribute": "email",
    "attribute.name": "email",
    "attribute.nameformat": "Basic"
  }
}' >/dev/null
echo "added email mapper"

echo "== group and user"
api POST "/$REALM/groups" -d '{"name":"perfuse-editors"}' >/dev/null
GROUP_ID=$(api GET "/$REALM/groups" | python3 -c 'import sys,json; print([g["id"] for g in json.load(sys.stdin) if g["name"]=="perfuse-editors"][0])')

api POST "/$REALM/users" -d '{
  "username": "grace",
  "email": "grace@hospital.test",
  "firstName": "Grace",
  "lastName": "Hopper",
  "enabled": true,
  "emailVerified": true,
  "credentials": [{"type":"password","value":"perfuse-e2e","temporary":false}]
}' >/dev/null

USER_ID=$(api GET "/$REALM/users?username=grace" | python3 -c 'import sys,json; print(json.load(sys.stdin)[0]["id"])')
api PUT "/$REALM/users/$USER_ID/groups/$GROUP_ID" >/dev/null
echo "created grace in perfuse-editors"

echo "== signing certificate"
# The realm's own key, which is what signs assertions. Fetched from the descriptor rather than the admin API so this reads the same
# document a service provider would.
# Extracted with a separate script file rather than a heredoc, because "curl | python3 - <<EOF" gives the heredoc to stdin and
# silently discards the piped document - the first version of this reported "no certificate in the descriptor" while the descriptor
# plainly contained one.
cat > /tmp/extract-cert.py <<'PYEOF'
import re
import sys

xml = sys.stdin.read()
m = re.search(r"<(?:ds:)?X509Certificate>([^<]+)<", xml)
if not m:
    sys.exit("no certificate in the descriptor")

body = re.sub(r"\s+", "", m.group(1))
print("-----BEGIN CERTIFICATE-----")
for i in range(0, len(body), 64):
    print(body[i:i + 64])
print("-----END CERTIFICATE-----")
PYEOF

curl -s "$KC/realms/$REALM/protocol/saml/descriptor" | python3 /tmp/extract-cert.py > /tmp/keycloak-idp.crt
echo "wrote /tmp/keycloak-idp.crt"

echo "== sso url"
echo "$KC/realms/$REALM/protocol/saml"
