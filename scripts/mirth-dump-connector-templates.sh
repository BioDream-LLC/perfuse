#!/usr/bin/env bash
#
# Have Mirth serialise its own default connector properties, so Perfuse can write channels Mirth accepts.
#
# Why this exists. Perfuse needs to produce Mirth XML for channels authored in Perfuse, and a property block written from a reading of
# Mirth's format does not work. XStream treats the class and version attributes as instructions and discards an element it does not
# recognise without a word, so a wrong guess is stored as "This channel is invalid. Verify all required extensions are loaded correctly"
# with nothing naming the element at fault. Four hand-written attempts died that way before the first fixture was made by asking Mirth.
#
# This asks Mirth. Each connector properties class is instantiated with its own defaults and serialised by ObjectXMLSerializer, the class
# Mirth uses for its own exports, and the output is committed as a template that Perfuse substitutes values into.
#
# Two traps, both already paid for by scripts/mirth-author-channel.sh:
#
#   1. Mirth's connectors live in extensions/, not server-lib. Without extensions/tcp/tcp-shared.jar on the classpath the TCP classes
#      are not found at all.
#   2. Mirth ships a patched Rhino shaded into its own jars with a stock rhino-1.7.13.jar beside it. NativeDate exists in five jars and
#      Mirth's must win, or the run dies with IllegalAccessError. Hence mirth-server.jar first.
#
set -euo pipefail

MIRTH_VERSION="${MIRTH_VERSION:-4.5.2}"
WORK="$HOME/.cache/perfuse-mirth-author"
OUT="$(cd "$(dirname "$0")/.." && pwd)/internal/tomirth/templates"

if ! docker ps --format '{{.Names}}' | grep -qx mirth; then
  echo "no mirth container running. Start it with ./scripts/interop-up.sh" >&2
  exit 1
fi

mkdir -p "$WORK" "$OUT"

for dir in server-lib client-lib extensions; do
  if [ ! -d "$WORK/$dir" ]; then
    echo "== copying $dir out of the container"
    docker cp "mirth:/opt/connect/$dir" "$WORK/$dir"
  fi
done

cp "$HOME/mirth-jars/DumpProps.java" "$WORK/DumpProps.java"

echo "== asking Mirth $MIRTH_VERSION to serialise its own connector defaults"

# Every extension that ships a -shared.jar, because that is where the properties classes live. Built by listing rather than naming, so a
# connector added by a later Mirth is picked up instead of silently missing.
docker run --rm -v "$WORK:/work" -w /work eclipse-temurin:17-jdk sh -c "
  set -e
  SHARED=\"\$(find extensions -name '*-shared.jar' | tr '\n' ':')\"
  CP=\"server-lib/mirth-server.jar:server-lib/mirth-client-core.jar:\$(find server-lib -name '*.jar' ! -name 'mirth-server.jar' ! -name 'mirth-client-core.jar' | tr '\n' ':')\$SHARED\"
  javac -cp \"\$CP\" -d . DumpProps.java
  mkdir -p out
  java -cp \"\$CP:.\" DumpProps out $MIRTH_VERSION
"

echo "== copying the templates into the tree"
cp "$WORK"/out/*.xml "$WORK"/out/*.transport "$OUT"/
ls -la "$OUT" | tail -n +2 | awk '{print $5, $9}'

echo
echo "== done. internal/tomirth verifies these against a running Mirth:"
echo "   go test ./internal/tomirth/ -count=1"
