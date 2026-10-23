package translate

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/mirth"
)

// Open Integration Engine 4.7.0 added a local address to the HTTP Sender and a request-header limit to the HTTP Listener.
// Neither has an equivalent here; a setting that changes behaviour is said, the defaults (as OIE writes them) are not.
func TestOIE470HTTPSettingsAreSaidWhenTheyMatter(t *testing.T) {
	sender := func(props map[string]string) mirth.Connector {
		p := map[string]string{"host": "https://partner.example/in", "method": "post"}
		for k, v := range props {
			p[k] = v
		}
		return mirth.Connector{Name: "Partner", Mode: mirth.ModeDestination, Enabled: true, Transport: "HTTP Sender",
			PropertiesClass: "com.mirth.connect.connectors.http.HttpDispatcherProperties", Properties: p}
	}
	notes := func(res *Result) string {
		var all []string
		for _, n := range res.Notes {
			all = append(all, n.Message)
		}
		return strings.Join(all, "\n")
	}
	res, _ := translated(t, &mirth.Channel{Name: "Defaults", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{sender(map[string]string{"overrideLocalBinding": "false", "localAddress": "0.0.0.0"})}})
	if strings.Contains(notes(res), "local address") {
		t.Errorf("OIE's defaults are not worth a note:\n%s", notes(res))
	}
	res, _ = translated(t, &mirth.Channel{Name: "Bound", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{sender(map[string]string{"overrideLocalBinding": "true", "localAddress": "10.0.0.7"})}})
	if !strings.Contains(notes(res), "sent from the local address 10.0.0.7") {
		t.Errorf("a chosen local address must be said:\n%s", notes(res))
	}

	listener := func(size string) mirth.Connector {
		return mirth.Connector{Name: "sourceConnector", Mode: mirth.ModeSource, Enabled: true, Transport: "HTTP Listener",
			PropertiesClass: "com.mirth.connect.connectors.http.HttpReceiverProperties",
			Properties:      map[string]string{"listenerConnectorProperties.port": "8081", "contextPath": "/in", "requestHeaderSize": size}}
	}
	for size, said := range map[string]bool{"8192": false, "4194304": true} {
		res, _ := translated(t, &mirth.Channel{Name: "Listener " + size, Enabled: true, Source: listener(size),
			Destinations: []mirth.Connector{mllpDestination("Registry", "10.0.0.9", "6662")}})
		if got := strings.Contains(notes(res), "request headers up to"); got != said {
			t.Errorf("requestHeaderSize %s: said %v, want %v:\n%s", size, got, said, notes(res))
		}
	}
}
