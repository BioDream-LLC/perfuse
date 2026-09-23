#!/bin/sh
# Publish the built site to S3 and invalidate the CloudFront cache.
#
# This exists because the upload was being typed by hand each time, slightly differently, and every
# file type needs a Content-Type set explicitly: S3 does not infer one, and an object served as
# application/octet-stream is offered as a download instead of being rendered. A WebP screenshot, a
# PDF manual and an XML sitemap all fail in their own quiet way if that is forgotten, and the site
# looks fine to whoever uploaded it because their browser already has the old copy.
#
# Usage: scripts/deploy-site.sh [--no-invalidate]
#
# Requires the AWS CLI with credentials for the account that owns the bucket.

set -eu

BUCKET="s3://perfuse-website-893675906148"
DISTRIBUTION="E1EQTICSGCWXC9"
DIR="dist-site"

if [ ! -d "$DIR" ]; then
  echo "no $DIR: run make site first" >&2
  exit 1
fi

# HTML is revalidated often because it is the only thing that changes with a release; assets carry a
# content hash in nothing, so they get a day rather than a year.
HTML_CACHE="public, max-age=300, must-revalidate"
ASSET_CACHE="public, max-age=86400"
DOC_CACHE="public, max-age=3600"

cd "$DIR"

echo "html"
aws s3 sync . "$BUCKET" --delete \
  --exclude "*" --include "*.html" \
  --content-type "text/html; charset=utf-8" --cache-control "$HTML_CACHE" --only-show-errors

# Each type named explicitly. The --delete above is deliberately only on the HTML pass: a second
# pass with --delete and a different --include would remove everything the first pass uploaded.
for spec in \
  "*.svg:image/svg+xml:$ASSET_CACHE" \
  "*.png:image/png:$ASSET_CACHE" \
  "*.webp:image/webp:$ASSET_CACHE" \
  "*.ico:image/x-icon:$ASSET_CACHE" \
  "*.pdf:application/pdf:$DOC_CACHE" \
  "*.xml:application/xml; charset=utf-8:$DOC_CACHE" \
  "*.txt:text/plain; charset=utf-8:$DOC_CACHE" \
  "*.json:application/json; charset=utf-8:$DOC_CACHE"
do
  pattern=$(echo "$spec" | cut -d: -f1)
  type=$(echo "$spec" | cut -d: -f2)
  cache=$(echo "$spec" | cut -d: -f3-)

  count=$(find . -name "$pattern" -type f | wc -l | tr -d ' ')
  [ "$count" = "0" ] && continue

  echo "$pattern ($count)"
  aws s3 sync . "$BUCKET" \
    --exclude "*" --include "$pattern" \
    --content-type "$type" --cache-control "$cache" --only-show-errors
done

cd - >/dev/null

# Anything in the build that this script has no Content-Type for would be uploaded by no pass at all
# and simply be missing from the site. Better to say so than to leave a hole.
missing=$(find "$DIR" -type f \
  ! -name "*.html" ! -name "*.svg" ! -name "*.png" ! -name "*.webp" ! -name "*.ico" \
  ! -name "*.pdf" ! -name "*.xml" ! -name "*.txt" ! -name "*.json")
if [ -n "$missing" ]; then
  echo "these files have no Content-Type rule and were NOT uploaded:" >&2
  echo "$missing" >&2
  echo "add them to this script" >&2
  exit 1
fi

if [ "${1:-}" != "--no-invalidate" ]; then
  echo "invalidating"
  aws cloudfront create-invalidation --distribution-id "$DISTRIBUTION" --paths "/*" \
    --region us-east-1 --query 'Invalidation.Id' --output text
fi

# Tell the search engines that accept being told.
#
# IndexNow is a single POST that reaches Bing, Yandex, Seznam and Naver. Google declined to adopt it,
# so Google still learns about changes by crawling or by being asked in Search Console.
#
# The URL list is read from the sitemap the build just wrote, so a page added to the site is
# submitted without anybody remembering to add it here. A list maintained separately from the
# sitemap would drift, and the drift would be invisible: the submission would succeed, just without
# the new page in it.
KEY=$(basename "$(find "$DIR" -maxdepth 1 -name '*.txt' ! -name 'robots.txt' | head -1)" .txt 2>/dev/null || true)
if [ -n "$KEY" ]; then
  echo "indexnow"
  python3 - "$KEY" "$DIR/sitemap.xml" <<'PY'
import json, re, sys, urllib.error, urllib.request

key, sitemap = sys.argv[1], sys.argv[2]
urls = re.findall(r"<loc>([^<]+)</loc>", open(sitemap).read())
if not urls:
    print("  no URLs in the sitemap; nothing submitted")
    raise SystemExit(0)

payload = {
    "host": "perfuse.health",
    "key": key,
    "keyLocation": "https://perfuse.health/%s.txt" % key,
    "urlList": urls,
}
req = urllib.request.Request(
    "https://api.indexnow.org/indexnow",
    data=json.dumps(payload).encode(),
    headers={"Content-Type": "application/json; charset=utf-8"},
)
try:
    r = urllib.request.urlopen(req, timeout=30)
    print("  %s %s for %d URLs" % (r.status, r.reason, len(urls)))
except urllib.error.HTTPError as e:
    # Not fatal. A rejected submission means the search engines were not told about this deploy,
    # which is worth seeing, but the deploy itself succeeded and the site is live.
    print("  IndexNow refused: HTTP %s %s" % (e.code, e.read().decode()[:200]))
except Exception as e:
    print("  IndexNow unreachable: %s" % e)
PY
else
  echo "  no IndexNow key in the build; skipping submission" >&2
fi

echo "done"
