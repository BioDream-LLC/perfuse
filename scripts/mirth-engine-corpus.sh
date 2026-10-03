#!/usr/bin/env bash
# Have each Mirth-family engine author Perfuse's migration corpus, then back itself up.
#
# Mirth Connect went closed-source at 4.6. A site leaving it starts from Mirth 4.5.2, the Open Integration Engine or BridgeLink, and an
# importer tested only against Mirth's documents is tested against one of the three. So the corpus is written by each engine:
#
#   1. BuildCorpus.java is compiled against the engine's own jars and run with its own ObjectXMLSerializer, producing per-channel
#      exports, a code template library export and a channel group export - the files the Administrator writes.
#   2. Those are loaded into the running engine through its REST API, and the engine's own server backup is downloaded. That backup
#      is what a site exporting "everything" actually hands over.
#
# The engines are the containers ./scripts/interop-up.sh starts. Output: internal/mirth/testdata/engines/<engine>-<version>/.
#
# Usage: ./scripts/mirth-engine-corpus.sh [engine...]   (default: every engine that is running)

set -euo pipefail
cd "$(dirname "$0")/.."

# container  port  product  install-dir-in-container
ENGINES=(
  "mirth      8443 mirth      /opt/connect"
  "oie452     8444 oie        /opt/engine"
  "bridgelink 8445 bridgelink /opt/bridgelink"
  "oie460     8446 oie        /opt/oie"
)

want=("$@")
CACHE="$HOME/.cache/perfuse-mirth-corpus"
mkdir -p "$CACHE"

api() { # port method path [curl args...]
  local port=$1 method=$2 path=$3
  shift 3
  curl -sk -u admin:admin -H "X-Requested-With: perfuse" -X "$method" "https://127.0.0.1:$port$path" "$@"
}

for row in "${ENGINES[@]}"; do
  read -r container port product dir <<<"$row"
  if [ ${#want[@]} -gt 0 ] && [[ ! " ${want[*]} " == *" $container "* ]]; then continue; fi
  if ! docker ps --format '{{.Names}}' | grep -qx "$container"; then
    echo "== $container is not running; skipped"
    continue
  fi

  version="$(api "$port" GET /api/server/version -H 'Accept: text/plain')"
  label="$product-$version"
  out="internal/mirth/testdata/engines/$label"
  work="$CACHE/$label"
  echo "== $label ($container on $port)"

  # The engine's own jars, from the container, so the serialiser matches the server.
  if [ ! -d "$work/server-lib" ]; then
    mkdir -p "$work"
    for d in server-lib extensions; do docker cp "$container:$dir/$d" "$work/$d"; done
  fi
  cp scripts/mirth-corpus/BuildCorpus.java "$work/"

  # The engine's own jars first: Mirth shades a patched Rhino into mirth-server.jar, and a stock copy winning the classpath fails deep
  # inside XStream with IllegalAccessError.
  docker run --rm -v "$work:/work" -w /work eclipse-temurin:17-jdk sh -c '
    set -e
    CP="server-lib/mirth-server.jar:server-lib/mirth-client-core.jar:$(find server-lib extensions -name "*.jar" ! -name mirth-server.jar ! -name mirth-client-core.jar | tr "\n" ":")"
    javac -nowarn -cp "$CP" -d . BuildCorpus.java
    rm -rf out && java -cp "$CP:." BuildCorpus out "'"$version"'"
  '

  rm -rf "$out"
  mkdir -p "$out"
  cp "$work"/out/*.xml "$out/"

  # Start from empty, so the backup holds the corpus and nothing a previous test left behind. These are throwaway test containers.
  for id in $(api "$port" GET /api/channels/idsAndNames | grep -oE '<string>[0-9a-f-]{36}</string>' | sed 's/<[^>]*>//g'); do
    api "$port" DELETE "/api/channels/$id" >/dev/null
  done
  echo '<list/>' >"$work/empty-list.xml"
  api "$port" PUT "/api/codeTemplateLibraries?override=true" -H 'Content-Type: application/xml' --data-binary "@$work/empty-list.xml" >/dev/null
  for id in $(api "$port" GET /api/codeTemplates | grep -oE '<id>[^<]+</id>' | sed 's/<[^>]*>//g'); do
    api "$port" DELETE "/api/codeTemplates/$id" >/dev/null
  done

  # Load the corpus into the engine. Channels first, since the library and the group name them by id.
  for f in "$out"/channel-*.xml; do
    [ "$(basename "$f")" = channel-group.xml ] && continue
    id="$(grep -m1 -oE '<id>[^<]+</id>' "$f" | sed 's/<[^>]*>//g')"
    api "$port" DELETE "/api/channels/$id" >/dev/null || true
    api "$port" POST /api/channels -H 'Content-Type: application/xml' --data-binary "@$f" >/dev/null
    stored="$(api "$port" GET "/api/channels/$id")"
    if grep -q "This channel is invalid" <<<"$stored"; then
      echo "   $label stored its own $(basename "$f") as invalid" >&2
      exit 1
    fi
  done

  # Code templates are saved individually and then the library that lists them, which is the order the Administrator uses.
  lib="$out/code-template-libraries.xml"
  awk '/<codeTemplate version=/{p=1} p{print} /<\/codeTemplate>/{if(p){print "@@END@@"}; p=0}' "$lib" |
    awk -v RS='@@END@@\n' -v dir="$work" 'NF{f=sprintf("%s/tmpl-%d.xml", dir, ++n); printf "%s", $0 > f; close(f)}'
  for t in "$work"/tmpl-*.xml; do
    id="$(grep -m1 -oE '<id>[^<]+</id>' "$t" | sed 's/<[^>]*>//g')"
    api "$port" PUT "/api/codeTemplates/$id?override=true" -H 'Content-Type: application/xml' --data-binary "@$t" >/dev/null
  done
  rm -f "$work"/tmpl-*.xml
  api "$port" PUT "/api/codeTemplateLibraries?override=true" -H 'Content-Type: application/xml' --data-binary "@$lib" >/dev/null

  # The group, through the bulk update the Administrator uses. It takes the group with channel ids rather than whole channels.
  sed -E '/<channel version=/,/<\/channel>/d' "$out/channel-group.xml" >"$work/group-ids.xml"
  python3 - "$out/channel-group.xml" "$work/group-ids.xml" <<'PY'
import re, sys
src = open(sys.argv[1]).read()
ids = re.findall(r'<channel version="[^"]*">\s*<id>([^<]+)</id>', src)
doc = re.sub(r'<channels>.*</channels>', '<channels>' + ''.join(
    '<channel version="x"><id>%s</id><revision>0</revision></channel>' % i for i in ids) + '</channels>', src, flags=re.S)
version = re.search(r'<channelGroup version="([^"]+)"', src).group(1)
doc = doc.replace('version="x"', 'version="%s"' % version)
open(sys.argv[2], 'w').write('<set>' + doc[doc.index('<channelGroup'):] + '</set>')
PY
  # curl reads a form value that starts with "<" as a file name, so the empty set goes in a file too.
  echo '<set/>' >"$work/no-removals.xml"
  api "$port" POST "/api/channelgroups/_bulkUpdate?override=true" \
    -F "channelGroups=@$work/group-ids.xml;type=application/xml" \
    -F "removedChannelGroupIds=@$work/no-removals.xml;type=application/xml" >/dev/null

  # And the engine's own backup of everything, which is the document a site migrating "all of it" hands over.
  api "$port" GET /api/server/configuration -H 'Accept: application/xml' >"$out/server-configuration.xml"
  echo "   $(ls "$out" | wc -l | tr -d ' ') files in $out"
done
