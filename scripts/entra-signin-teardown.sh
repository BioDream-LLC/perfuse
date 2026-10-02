#!/usr/bin/env bash
#
# Remove everything ./scripts/entra-signin-setup.sh left in the tenant.
#
# Why this exists as a script rather than a note. The setup script deliberately leaves an application and a user behind, because Entra will not
# issue an assertion without a human signing in and so the application has to outlive the run. Anything deliberately left behind needs something
# that removes it, or it accumulates - which it did: four applications had collected in the tenant before anybody looked.
#
# It reports what it deleted and what it did not find. That distinction is the whole point of the script.
#
# The version of this logic inside the test suite treated a 404 as success, which meant a cleanup that found nothing reported that everything had
# been removed. Every test passed while the tenant filled up. So here, "not found" is printed as "not found" and a deletion is only claimed when
# the server actually returned 204, and the exit status reflects whether anything failed.
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

APP_NAME="${APP_NAME:-perfuse-local-signin}"
USER_PREFIXES=("perfuse-test" "perfuse-entra")

# Dry run by default is deliberate. This deletes objects from a real directory, and the one thing worse than a cleanup that silently does nothing
# is a cleanup that silently removes something somebody needed.
APPLY="${APPLY:-0}"

echo "== getting a Graph token"
TOKEN="$(curl -sf -X POST "https://login.microsoftonline.com/$PERFUSE_ENTRA_TENANT_ID/oauth2/v2.0/token" \
  -d "client_id=$PERFUSE_ENTRA_CLIENT_ID" \
  -d "client_secret=$PERFUSE_ENTRA_CLIENT_SECRET" \
  -d "scope=https://graph.microsoft.com/.default" \
  -d "grant_type=client_credentials" | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')"

if [ -z "$TOKEN" ]; then
  echo "could not get a token" >&2
  exit 1
fi

FAILED=0
DELETED=0
FOUND=0

# graph_delete removes one object and says what actually happened.
#
# The status code is reported rather than interpreted generously. 204 is a deletion; 404 means it was already gone, which is worth knowing and is
# not the same thing; anything else is a failure that should not be swallowed.
graph_delete() {
  local kind="$1" id="$2" label="$3"
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' -X DELETE \
    -H "Authorization: Bearer $TOKEN" \
    "https://graph.microsoft.com/v1.0/$kind/$id")"

  case "$code" in
    204)
      echo "   deleted  $label"
      DELETED=$((DELETED + 1))
      ;;
    404)
      echo "   absent   $label (already gone)"
      ;;
    *)
      echo "   FAILED   $label (HTTP $code)" >&2
      FAILED=$((FAILED + 1))
      ;;
  esac
}

echo "== applications named $APP_NAME"
APPS="$(curl -sf -G -H "Authorization: Bearer $TOKEN" \
  "https://graph.microsoft.com/v1.0/applications" \
  --data-urlencode "\$filter=startswith(displayName,'$APP_NAME')" \
  --data-urlencode "\$select=id,displayName,appId" |
  python3 -c '
import sys, json
for a in json.load(sys.stdin).get("value", []):
    print(a["id"], a["displayName"])')"

if [ -z "$APPS" ]; then
  echo "   none"
else
  while read -r id name; do
    [ -z "$id" ] && continue
    FOUND=$((FOUND + 1))
    if [ "$APPLY" = "1" ]; then
      # Deleting the application removes its service principal too, so the enterprise application entry does not need removing separately.
      graph_delete applications "$id" "$name ($id)"
    else
      echo "   would delete  $name ($id)"
    fi
  done <<<"$APPS"
fi

for prefix in "${USER_PREFIXES[@]}"; do
  echo "== users named $prefix*"
  USERS="$(curl -sf -G -H "Authorization: Bearer $TOKEN" \
    "https://graph.microsoft.com/v1.0/users" \
    --data-urlencode "\$filter=startswith(userPrincipalName,'$prefix')" \
    --data-urlencode "\$select=id,userPrincipalName" |
    python3 -c '
import sys, json
for u in json.load(sys.stdin).get("value", []):
    print(u["id"], u["userPrincipalName"])')"

  if [ -z "$USERS" ]; then
    echo "   none"
    continue
  fi

  while read -r id upn; do
    [ -z "$id" ] && continue
    FOUND=$((FOUND + 1))
    if [ "$APPLY" = "1" ]; then
      graph_delete users "$id" "$upn"
    else
      echo "   would delete  $upn ($id)"
    fi
  done <<<"$USERS"
done

echo
if [ "$APPLY" != "1" ]; then
  echo "Nothing was deleted. $FOUND object(s) matched; re-run with APPLY=1 to remove them."
  exit 0
fi

echo "Deleted $DELETED of $FOUND matching object(s)."

if [ "$FAILED" -gt 0 ]; then
  echo "$FAILED deletion(s) failed; the tenant is not clean." >&2
  exit 1
fi

if [ "$FOUND" -eq 0 ]; then
  echo "The tenant was already clean."
fi

# Deleted directory objects sit in a recycle bin for 30 days and can be restored from the portal, or purged with
# DELETE /directory/deletedItems/{id}. Mentioned because "deleted" in Entra does not mean gone.
echo "Note: Entra keeps deleted objects for 30 days under Deleted items; they are restorable until then."
