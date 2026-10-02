#!/bin/sh
# Publish a release everywhere it appears: the version in the source, the GitHub release, the website.
#
#   GITHUB_TOKEN=... make publish VERSION=v0.1.4 NOTES=path/to/notes.md
#
# Why one script. A release is published in five places - the Makefile's version, the README's download
# links, the website's download links and comparison table, the GitHub release and its assets, and the
# website itself - and doing them by hand is how v0.1.2 shipped three manual chapters in the wrong file
# and why the site went on describing a version after it had been replaced. Each step here checks the one
# before it, and the script stops at the first thing that is not right rather than publishing half a
# release.
#
# What it will not do. It never replaces the assets of an existing release: a version number must mean one
# set of bytes, so a tag that already exists is a refusal, not an update. And it reads the token from the
# environment only - never from a file path baked in here.
set -eu

NEW="${1:-}"
NOTES="${2:-}"
REPO="BioDream-LLC/perfuse"
API="https://api.github.com/repos/$REPO"
UPLOADS="https://uploads.github.com/repos/$REPO"

die() { echo "publish: $*" >&2; exit 1; }

# ---- 1. Refuse anything that is not a clean, new, documented release -------------------------------------
case "$NEW" in v[0-9]*.[0-9]*.[0-9]*) ;; *) die "usage: $0 vX.Y.Z NOTES.md (got \"$NEW\")";; esac
[ -n "$NOTES" ] && [ -s "$NOTES" ] || die "release notes file \"$NOTES\" is missing or empty"
[ -n "${GITHUB_TOKEN:-}" ] || die "GITHUB_TOKEN is not set"
[ "$(git rev-parse --abbrev-ref HEAD)" = "main" ] || die "not on main"
[ -z "$(git status --porcelain --untracked-files=no)" ] || die "the working tree has uncommitted changes"
git fetch -q origin main
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || die "main is not the same as origin/main; pull or push first"
if git rev-parse -q --verify "refs/tags/$NEW" >/dev/null || git ls-remote --exit-code --tags origin "$NEW" >/dev/null 2>&1; then
  die "$NEW is already tagged. A version names one set of bytes; publish a new version instead"
fi

OLD=$(sed -n 's/^VERSION ?= //p' Makefile)
[ -n "$OLD" ] || die "could not read the current version from the Makefile"
[ "$OLD" != "$NEW" ] || die "the Makefile already says $NEW"
echo "publish: $OLD -> $NEW"

# ---- 2. Move every mention of the version, and check none was missed -------------------------------------
FILES="Makefile README.md site/index.html site/mirth-connect-alternative/index.html"
for f in $FILES; do
  OLD="$OLD" NEW="$NEW" python3 - "$f" <<'PY'
import os, sys
p = sys.argv[1]
s = open(p).read()
s = s.replace(os.environ["OLD"], os.environ["NEW"])
open(p, "w").write(s)
PY
done
left=$(grep -l -F "$OLD" $FILES || true)
[ -z "$left" ] || die "$OLD is still mentioned in: $left"

# ---- 3. Build and check what ships ------------------------------------------------------------------------
# The web bundle first: the WASM module inside it is built with the version stamped in, so moving the version changes
# committed files, and make check's freshness test rightly refuses a bundle that no longer matches its source. The first
# run of this script stopped exactly there.
make web VERSION="$NEW" >/dev/null
make check VERSION="$NEW" | tail -1 | grep -q "CHECK PASSED" || die "make check did not pass"
make docs VERSION="$NEW" >/dev/null
rm -rf dist
make release VERSION="$NEW" >/dev/null

native="dist/perfuse-$(go env GOOS)-$(go env GOARCH)"
"$native" version | grep -q "$NEW" || die "$native does not report $NEW"
( cd dist && shasum -a 256 -c SHA256SUMS >/dev/null ) || die "dist/SHA256SUMS does not match the files"
count=$(ls dist/perfuse-"$NEW"-* | wc -l | tr -d ' ')
[ "$count" -eq 6 ] || die "expected 6 archives in dist, found $count"

# ---- 4. Commit, tag, push ---------------------------------------------------------------------------------
git add $FILES docs/manual/ internal/web/dist
printf 'Release %s\n\nThe version moved in the Makefile, the README and the website, by scripts/publish-release.sh.\n' "$NEW" > .git/PUBLISH_MSG
git commit -q -F .git/PUBLISH_MSG && rm -f .git/PUBLISH_MSG
git tag -a "$NEW" -m "Perfuse $NEW"

askpass=$(mktemp)
trap 'rm -f "$askpass"' EXIT
printf '#!/bin/sh\ncase "$1" in Username*) echo x-access-token;; *) printf %%s "$GITHUB_TOKEN";; esac\n' > "$askpass"
chmod 700 "$askpass"
GIT_ASKPASS="$askpass" GIT_TERMINAL_PROMPT=0 git push -q origin main "$NEW"

# ---- 5. The GitHub release and its assets -----------------------------------------------------------------
auth="Authorization: Bearer $GITHUB_TOKEN"
body=$(NEW="$NEW" python3 -c 'import json,os,sys; print(json.dumps({"tag_name":os.environ["NEW"],"name":"Perfuse "+os.environ["NEW"],"body":sys.stdin.read(),"make_latest":"true"}))' < "$NOTES")
id=$(curl -sf -X POST -H "$auth" -H "Accept: application/vnd.github+json" "$API/releases" -d "$body" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])') || die "the GitHub release could not be created"
echo "publish: release $id created"

for f in dist/perfuse-"$NEW"-* dist/SHA256SUMS dist/perfuse.cdx.json; do
  name=$(basename "$f")
  curl -sf -X POST -H "$auth" -H "Content-Type: application/octet-stream" --data-binary @"$f" \
    "$UPLOADS/releases/$id/assets?name=$name" >/dev/null || die "uploading $name failed"
  echo "publish:   $name"
done

# ---- 6. Prove the release as a stranger sees it -----------------------------------------------------------
sums=$(mktemp)
curl -sfL -o "$sums" "https://github.com/$REPO/releases/download/$NEW/SHA256SUMS" || die "SHA256SUMS is not downloadable"
cmp -s "$sums" dist/SHA256SUMS || die "the published SHA256SUMS differs from the built one"
rm -f "$sums"
latest=$(curl -sf "$API/releases/latest" | python3 -c 'import json,sys; print(json.load(sys.stdin)["tag_name"])')
[ "$latest" = "$NEW" ] || die "GitHub says the latest release is $latest"

# ---- 7. The website ---------------------------------------------------------------------------------------
make site VERSION="$NEW" >/dev/null
./scripts/deploy-site.sh
echo "publish: $NEW is on GitHub and perfuse.health"
