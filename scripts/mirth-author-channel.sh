#!/usr/bin/env bash
# Have Mirth author a channel export, so the fixture is Mirth's work rather than ours.
#
# The problem this solves. internal/mirth/testdata/adt_channel.xml was written by hand, and a real Mirth 4.5.2 will not load it: the
# API accepts the POST and then stores the channel as "This channel is invalid. Verify all required extensions are loaded correctly"
# with the destination connectors discarded. That made every test in internal/mirth a test of agreement with a document composed here.
#
# Guessing at another program's serialisation does not work, and four attempts at patching the attributes by hand confirmed it. Mirth's
# serialiser is configured to ignore elements it does not recognise, so a wrong name produces no error at all - there is nothing to
# read and nothing to correct against.
#
# So this asks Mirth to write the document, using Mirth's own model classes and its own ObjectXMLSerializer, which is the class its
# exports go through. Whatever comes out is by definition what Mirth produces. It needs no Administrator GUI and no Xvfb, though that
# was the other way in and would also have worked.
#
# Two traps worth recording, because both cost time:
#
#   1. Mirth's connectors live in extensions/, not in server-lib. Without extensions/tcp/tcp-shared.jar on the classpath the
#      properties classes cannot be instantiated and the connectors are dropped in silence - the same failure as the hand-written
#      document, arrived at from the other direction.
#
#   2. Mirth ships a patched Rhino shaded into mirth-server.jar and mirth-client-core.jar, and a stock rhino-1.7.13.jar sits beside
#      them in server-lib. NativeDate exists in five jars. If the stock one wins the classpath the run dies with IllegalAccessError
#      from deep inside XStream, so Mirth's own jars have to come first.
#
# Usage: ./scripts/mirth-author-channel.sh [output.xml]
#   Needs the mirth container from scripts/interop-up.sh, and Docker for a JDK - the Mirth image ships a JRE with no compiler.

set -euo pipefail

OUT="${1:-internal/mirth/testdata/real_channel_from_mirth.xml}"
WORK="$HOME/.cache/perfuse-mirth-author"
MIRTH_VERSION="${MIRTH_VERSION:-4.5.2}"

if ! docker ps --format '{{.Names}}' | grep -qx mirth; then
  echo "the mirth container is not running: ./scripts/interop-up.sh" >&2
  exit 1
fi

mkdir -p "$WORK"

# Mirth's jars, taken from the running container so they match the server they will be posted to.
for dir in server-lib client-lib extensions; do
  if [ ! -d "$WORK/$dir" ]; then
    echo "== copying $dir out of the container"
    docker cp "mirth:/opt/connect/$dir" "$WORK/$dir"
  fi
done

cat > "$WORK/BuildChannel.java" <<'JAVA'
import com.mirth.connect.model.Channel;
import com.mirth.connect.model.Connector;
import com.mirth.connect.model.Filter;
import com.mirth.connect.model.Transformer;
import com.mirth.connect.model.converters.ObjectXMLSerializer;
import com.mirth.connect.connectors.tcp.TcpReceiverProperties;
import com.mirth.connect.connectors.http.HttpDispatcherProperties;

import java.nio.file.Files;
import java.nio.file.Path;
import java.util.UUID;

/** Builds a channel with Mirth's own classes and lets Mirth's own serialiser write it. */
public class BuildChannel {
    public static void main(String[] args) throws Exception {
        String out = args[0];
        String version = args.length > 1 ? args[1] : "4.5.2";

        ObjectXMLSerializer serializer = ObjectXMLSerializer.getInstance();
        serializer.init(version);

        Channel channel = new Channel();
        // A fixed id, so regenerating the fixture does not produce a diff that is only a UUID. Version 4 shape, clearly synthetic.
        channel.setId("8977d0ce-5045-4770-9aac-4cfc829ef36b");
        channel.setName("ADT Inbound - Built By Mirth");
        channel.setDescription("Constructed with Mirth's own model classes so the serialisation is Mirth's, not ours.");
        channel.setRevision(1);

        Connector source = new Connector();
        source.setName("sourceConnector");
        source.setMode(Connector.Mode.SOURCE);
        source.setMetaDataId(0);
        source.setTransportName("TCP Listener");
        source.setEnabled(true);

        TcpReceiverProperties tcp = new TcpReceiverProperties();
        tcp.getListenerConnectorProperties().setHost("0.0.0.0");
        tcp.getListenerConnectorProperties().setPort("6661");
        source.setProperties(tcp);
        source.setTransformer(new Transformer());
        source.setFilter(new Filter());
        channel.setSourceConnector(source);

        Connector destination = new Connector();
        destination.setName("To Registry");
        destination.setMode(Connector.Mode.DESTINATION);
        destination.setMetaDataId(1);
        destination.setTransportName("HTTP Sender");
        destination.setEnabled(true);

        HttpDispatcherProperties http = new HttpDispatcherProperties();
        http.setHost("http://registry.example.invalid/adt");
        destination.setProperties(http);
        destination.setTransformer(new Transformer());
        destination.setFilter(new Filter());

        channel.getDestinationConnectors().add(destination);
        channel.setNextMetaDataId(2);

        String xml = serializer.serialize(channel);
        Files.writeString(Path.of(out), xml);

        // Read it back. A document its own writer cannot read would be the more interesting result, so it is checked rather than
        // assumed, and the numbers are printed for whoever runs this.
        Channel again = serializer.deserialize(xml, Channel.class);
        System.out.println("source transport: "
                + (again.getSourceConnector() == null ? "MISSING" : again.getSourceConnector().getTransportName()));
        System.out.println("destinations: "
                + (again.getDestinationConnectors() == null ? "MISSING" : again.getDestinationConnectors().size()));
    }
}
JAVA

echo "== compiling and running against Mirth $MIRTH_VERSION's own jars"

# Mirth's own jars first. See trap 2 above.
docker run --rm -v "$WORK:/work" -w /work eclipse-temurin:17-jdk sh -c "
  set -e
  CP=\"server-lib/mirth-server.jar:server-lib/mirth-client-core.jar:\$(find server-lib -name '*.jar' ! -name 'mirth-server.jar' ! -name 'mirth-client-core.jar' | tr '\n' ':')extensions/tcp/tcp-shared.jar:extensions/http/http-shared.jar\"
  javac -cp \"\$CP\" -d . BuildChannel.java
  java -cp \"\$CP:.\" BuildChannel authored.xml $MIRTH_VERSION
"

cp "$WORK/authored.xml" "$OUT"
echo "== wrote $OUT ($(wc -c < "$OUT" | tr -d ' ') bytes)"

# And the part that matters: the real server has to accept it. A document that only satisfies the serialiser would prove nothing,
# since the hand-written fixture satisfied the serialiser too and was stored as invalid.
ID="$(grep -oE '<id>[^<]+</id>' "$OUT" | head -1 | sed 's/<[^>]*>//g')"

echo "== deleting any previous copy, then posting to the running server"
curl -sk -u admin:admin -H "X-Requested-With: perfuse" -X DELETE \
  "https://127.0.0.1:8443/api/channels/$ID" >/dev/null 2>&1 || true

curl -sk -u admin:admin -H "X-Requested-With: perfuse" -H "Content-Type: application/xml" \
  -X POST "https://127.0.0.1:8443/api/channels" --data-binary "@$OUT" >/dev/null

STORED="$(curl -sk -u admin:admin -H "X-Requested-With: perfuse" \
  "https://127.0.0.1:8443/api/channels/$ID" | grep -oE '<description>[^<]*</description>' | head -1)"

if echo "$STORED" | grep -q "channel is invalid"; then
  echo "== MIRTH REJECTED IT: $STORED" >&2
  exit 1
fi

echo "== Mirth stored it and kept the description, so the channel is valid to Mirth"
