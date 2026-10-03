import com.mirth.connect.connectors.file.FileDispatcherProperties;
import com.mirth.connect.connectors.file.FileReceiverProperties;
import com.mirth.connect.connectors.http.HttpDispatcherProperties;
import com.mirth.connect.connectors.http.HttpReceiverProperties;
import com.mirth.connect.connectors.jdbc.DatabaseDispatcherProperties;
import com.mirth.connect.connectors.tcp.TcpDispatcherProperties;
import com.mirth.connect.connectors.tcp.TcpReceiverProperties;
import com.mirth.connect.model.Channel;
import com.mirth.connect.model.ChannelGroup;
import com.mirth.connect.model.Connector;
import com.mirth.connect.model.Filter;
import com.mirth.connect.model.Rule;
import com.mirth.connect.model.Step;
import com.mirth.connect.model.Transformer;
import com.mirth.connect.model.codetemplates.CodeTemplate;
import com.mirth.connect.model.codetemplates.CodeTemplateContextSet;
import com.mirth.connect.model.codetemplates.CodeTemplateLibrary;
import com.mirth.connect.model.codetemplates.CodeTemplateProperties.CodeTemplateType;
import com.mirth.connect.model.converters.ObjectXMLSerializer;
import com.mirth.connect.plugins.javascriptrule.JavaScriptRule;
import com.mirth.connect.plugins.javascriptstep.JavaScriptStep;
import com.mirth.connect.plugins.mapper.MapperStep;
import com.mirth.connect.plugins.rulebuilder.RuleBuilderRule;

import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashSet;
import java.util.List;

/**
 * Builds Perfuse's migration corpus with an engine's own model classes and serialiser.
 *
 * Run once per engine - Mirth 4.5.2, the Open Integration Engine, BridgeLink - against that engine's jars, so each document is that
 * engine's serialisation and not a guess at it. The channels are the shapes a real estate has: an MLLP feed with a mapper, a script, a
 * rule-builder filter and a call into a shared code template library; an HTTP feed into a database; a file poll out over MLLP.
 *
 * Writes: one file per channel (a per-channel export), a code template library export, and a channel group export with the channels
 * inside it, which is what the Administrator's "Export Group" produces.
 */
public class BuildCorpus {
    static final String ADT_ID = "c0a1e5d0-0000-4000-8000-000000000001";
    static final String LAB_ID = "c0a1e5d0-0000-4000-8000-000000000002";
    static final String ORD_ID = "c0a1e5d0-0000-4000-8000-000000000003";

    public static void main(String[] args) throws Exception {
        Path out = Path.of(args[0]);
        String version = args[1];
        Files.createDirectories(out);

        ObjectXMLSerializer s = ObjectXMLSerializer.getInstance();
        s.init(version);

        Channel adt = adtChannel();
        Channel lab = labChannel();
        Channel ord = ordersChannel();

        for (Channel c : List.of(adt, lab, ord)) {
            write(out.resolve("channel-" + slug(c.getName()) + ".xml"), s.serialize(c));
        }

        List<CodeTemplateLibrary> libraries = List.of(library());
        write(out.resolve("code-template-libraries.xml"), s.serialize(new ArrayList<>(libraries)));

        ChannelGroup group = new ChannelGroup("c0a1e5d0-0000-4000-8000-0000000000a0", "Admissions and labs",
                "Exported as a group, the way the Administrator does it");
        group.setChannels(new ArrayList<>(List.of(adt, lab)));
        write(out.resolve("channel-group.xml"), s.serialize(group));

        // Read every document back with the same serialiser. One its own writer cannot read would be the interesting result.
        for (Channel c : List.of(adt, lab, ord)) {
            Channel again = s.deserialize(s.serialize(c), Channel.class);
            System.out.println(c.getName() + ": " + again.getDestinationConnectors().size() + " destinations, "
                    + again.getSourceConnector().getTransformer().getElements().size() + " source steps");
        }
    }

    static void write(Path p, String xml) throws Exception {
        Files.writeString(p, xml);
        System.out.println("wrote " + p.getFileName() + " (" + xml.length() + " bytes)");
    }

    static String slug(String name) {
        return name.toLowerCase().replaceAll("[^a-z0-9]+", "-").replaceAll("(^-|-$)", "");
    }

    static Connector source(String transport, Object props) {
        Connector c = new Connector();
        c.setName("sourceConnector");
        c.setMode(Connector.Mode.SOURCE);
        c.setMetaDataId(0);
        c.setTransportName(transport);
        c.setEnabled(true);
        c.setProperties((com.mirth.connect.donkey.model.channel.ConnectorProperties) props);
        c.setTransformer(new Transformer());
        c.setFilter(new Filter());
        return c;
    }

    static Connector destination(int id, String name, String transport, Object props) {
        Connector c = source(transport, props);
        c.setName(name);
        c.setMode(Connector.Mode.DESTINATION);
        c.setMetaDataId(id);
        return c;
    }

    static void hl7(Connector c) {
        c.getTransformer().setInboundDataType("HL7V2");
        c.getTransformer().setOutboundDataType("HL7V2");
    }

    static Channel channel(String id, String name, String description) {
        Channel ch = new Channel();
        ch.setId(id);
        ch.setName(name);
        ch.setDescription(description);
        ch.setRevision(1);
        return ch;
    }

    static Channel adtChannel() {
        Channel ch = channel(ADT_ID, "ADT Inbound From Ward",
                "Admissions over MLLP: a mapper, a script calling the site library, a filter, two destinations");

        TcpReceiverProperties tcp = new TcpReceiverProperties();
        tcp.getListenerConnectorProperties().setHost("0.0.0.0");
        tcp.getListenerConnectorProperties().setPort("6661");
        Connector src = source("TCP Listener", tcp);
        hl7(src);

        MapperStep mrn = new MapperStep();
        mrn.setName("Medical record number");
        mrn.setSequenceNumber("0");
        mrn.setEnabled(true);
        mrn.setVariable("mrn");
        mrn.setMapping("msg['PID']['PID.3']['PID.3.1'].toString()");
        mrn.setDefaultValue("");

        JavaScriptStep pad = new JavaScriptStep();
        pad.setName("Pad the MRN with the site library");
        pad.setSequenceNumber("1");
        pad.setEnabled(true);
        pad.setScript("msg['PID']['PID.3']['PID.3.1'] = formatMRN($('mrn'));\nchannelMap.put('padded', 'yes');");
        src.getTransformer().setElements(new ArrayList<Step>(List.of(mrn, pad)));

        RuleBuilderRule adtOnly = new RuleBuilderRule();
        adtOnly.setName("Only ADT");
        adtOnly.setSequenceNumber("0");
        adtOnly.setEnabled(true);
        adtOnly.setField("msg['MSH']['MSH.9']['MSH.9.1'].toString()");
        adtOnly.setCondition(RuleBuilderRule.Condition.EQUALS);
        adtOnly.setValues(new ArrayList<>(List.of("'ADT'")));
        src.getFilter().setElements(new ArrayList<Rule>(List.of(adtOnly)));
        ch.setSourceConnector(src);

        FileDispatcherProperties file = new FileDispatcherProperties();
        file.setHost("/var/spool/adt-archive");
        file.setOutputPattern("${message.messageId}.hl7");
        Connector archive = destination(1, "Archive To Disk", "File Writer", file);
        hl7(archive);

        HttpDispatcherProperties http = new HttpDispatcherProperties();
        http.setHost("https://registry.example.invalid/adt");
        http.setMethod("post");
        Connector registry = destination(2, "Post To Registry", "HTTP Sender", http);
        hl7(registry);
        JavaScriptRule a08 = new JavaScriptRule();
        a08.setName("Not updates");
        a08.setSequenceNumber("0");
        a08.setEnabled(true);
        a08.setScript("return msg['MSH']['MSH.9']['MSH.9.2'].toString() != 'A08';");
        registry.getFilter().setElements(new ArrayList<Rule>(List.of(a08)));

        ch.getDestinationConnectors().add(archive);
        ch.getDestinationConnectors().add(registry);
        ch.setNextMetaDataId(3);
        ch.setPreprocessingScript("// Repair the line endings some ward systems send\nreturn message.replace(/\\n/g, '\\r');");
        ch.setDeployScript("globalChannelMap.put('deployedAt', String(new Date()));\nreturn;");
        return ch;
    }

    static Channel labChannel() {
        Channel ch = channel(LAB_ID, "Lab Results To Warehouse", "HTTP in, one row per result into the warehouse");

        HttpReceiverProperties http = new HttpReceiverProperties();
        http.getListenerConnectorProperties().setPort("8081");
        http.setContextPath("/results");
        Connector src = source("HTTP Listener", http);
        hl7(src);
        ch.setSourceConnector(src);

        DatabaseDispatcherProperties db = new DatabaseDispatcherProperties();
        db.setDriver("org.postgresql.Driver");
        db.setUrl("jdbc:postgresql://warehouse.example.invalid:5432/results");
        db.setUsername("loader");
        db.setPassword("");
        db.setQuery("INSERT INTO results (mrn, code, value) VALUES (${mrn}, ${code}, ${value})");
        Connector dst = destination(1, "Insert Result Row", "Database Writer", db);
        hl7(dst);
        ch.getDestinationConnectors().add(dst);
        ch.setNextMetaDataId(2);
        return ch;
    }

    static Channel ordersChannel() {
        Channel ch = channel(ORD_ID, "Orders File Drop", "Poll a directory and send each order on over MLLP");

        FileReceiverProperties file = new FileReceiverProperties();
        file.setHost("/var/spool/orders/in");
        file.setFileFilter("*.hl7");
        Connector src = source("File Reader", file);
        hl7(src);
        ch.setSourceConnector(src);

        TcpDispatcherProperties tcp = new TcpDispatcherProperties();
        tcp.setRemoteAddress("lis.example.invalid");
        tcp.setRemotePort("6670");
        Connector dst = destination(1, "Send To LIS", "TCP Sender", tcp);
        hl7(dst);
        ch.getDestinationConnectors().add(dst);
        ch.setNextMetaDataId(2);
        return ch;
    }

    static CodeTemplateLibrary library() {
        CodeTemplateLibrary lib = new CodeTemplateLibrary();
        lib.setId("c0a1e5d0-0000-4000-8000-0000000000b0");
        lib.setName("Site Helpers");
        lib.setDescription("Functions every channel calls");
        lib.setIncludeNewChannels(false);
        lib.setEnabledChannelIds(new HashSet<>(Arrays.asList(ADT_ID)));

        CodeTemplate pad = new CodeTemplate("formatMRN", CodeTemplateType.FUNCTION, CodeTemplateContextSet.getConnectorContextSet(),
                "/** Pads a medical record number to ten digits. */\nfunction formatMRN(mrn) {\n"
                        + "  return ('0000000000' + String(mrn).replace(/\\D/g, '')).slice(-10);\n}",
                "Pads a medical record number");
        pad.setId("c0a1e5d0-0000-4000-8000-0000000000b1");

        CodeTemplate drop = new CodeTemplate("Drop test patients", CodeTemplateType.DRAG_AND_DROP_CODE,
                CodeTemplateContextSet.getConnectorContextSet(),
                "if (msg['PID']['PID.5']['PID.5.1'].toString() == 'TEST') { return false; }", "A snippet, not a function");
        drop.setId("c0a1e5d0-0000-4000-8000-0000000000b2");

        lib.setCodeTemplates(new ArrayList<>(List.of(pad, drop)));
        return lib;
    }
}
