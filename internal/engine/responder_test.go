package engine

import (
	"sort"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// respondingSenders maps each destination type to a typed nil pointer of its sender.
//
// Typed nil pointers are enough, because satisfying an interface is decided at compile time: no sender is constructed, no
// configuration has to be valid, and nothing connects to anything. An earlier version of this test built a working sender
// for every type and spent more code getting each configuration past validation than it did testing anything.
//
// A new destination type has to be added here, and the test says so if it is not: an entry missing from this map is a
// destination nobody has decided about.
var respondingSenders = map[config.DestinationType]Sender{
	config.DestinationMLLP:       (*MLLPSender)(nil),
	config.DestinationHTTP:       (*HTTPSender)(nil),
	config.DestinationFHIR:       (*FHIRSender)(nil),
	config.DestinationSOAP:       (*SOAPSender)(nil),
	config.DestinationFile:       (*FileSender)(nil),
	config.DestinationDocument:   (*DocumentSender)(nil),
	config.DestinationSFTP:       (*SFTPSender)(nil),
	config.DestinationFTP:        (*FTPSender)(nil),
	config.DestinationS3:         (*S3Sender)(nil),
	config.DestinationSMTP:       (*SMTPSender)(nil),
	config.DestinationDatabase:   (*DatabaseSender)(nil),
	config.DestinationDICOM:      (*DICOMSender)(nil),
	config.DestinationJavaScript: (*JavaScriptSender)(nil),
	config.DestinationBroker:     (*BrokerSender)(nil),
}

// TestTheResponseTransformerListMatchesReality is a drift guard.
//
// Two places decide which destinations may have a response transformer, and they disagreed in both directions:
//
//   - internal/config/validate.go said mllp, http and fhir.
//   - The senders that actually implement Responder are MLLP and SOAP.
//
// So http and fhir passed validation and were refused when the channel started, and soap - which works - was refused at
// load and could never be used at all. A working feature unreachable, and two broken ones accepted until runtime.
//
// That is the shape of every list maintained by hand beside the thing it describes, which is why it is now derived rather
// than written down twice.
func TestTheResponseTransformerListMatchesReality(t *testing.T) {
	var respondsButRefused, acceptedButCannot []string

	for dt, sender := range respondingSenders {
		_, responds := sender.(Responder)
		accepted := configAcceptsResponseTransformer(t, dt)

		switch {
		case responds && !accepted:
			respondsButRefused = append(respondsButRefused, string(dt))
		case !responds && accepted:
			acceptedButCannot = append(acceptedButCannot, string(dt))
		}
	}

	// Sorted, because Go maps range randomly and a failure message that reorders between runs cannot be compared
	// with the last one.
	sort.Strings(respondsButRefused)
	sort.Strings(acceptedButCannot)

	if len(respondsButRefused) > 0 {
		t.Errorf("these destinations return a reply a script could read, and validation refuses a "+
			"response_transformer on them: %s\n\nThe capability exists and cannot be reached. Add them to "+
			"the switch in internal/config/validate.go.", strings.Join(respondsButRefused, ", "))
	}
	if len(acceptedButCannot) > 0 {
		t.Errorf("validation accepts a response_transformer on these destinations and they cannot report a "+
			"reply: %s\n\nThe channel passes validation and then fails to start. Either implement "+
			"SendForResponse or remove them from that switch.", strings.Join(acceptedButCannot, ", "))
	}
}

// TestEveryDestinationTypeIsAccountedFor stops the map above going stale.
//
// A destination added to config and not added here would simply not be checked, and the drift guard would keep passing
// while the thing it guards drifted. This is the guard on the guard, and it exists because that failure has happened
// twice on this project already - once where a builder check covered destinations but not sources, and once where a
// success grep could not see a vet failure.
func TestEveryDestinationTypeIsAccountedFor(t *testing.T) {
	var missing []string

	for _, dt := range config.AllDestinationTypes() {
		// The channel destination delivers into this engine rather than over a network, so there is no external
		// reply to read and it needs no sender here. Named explicitly rather than skipped by pattern.
		if dt == config.DestinationChannel {
			continue
		}
		if _, ok := respondingSenders[dt]; !ok {
			missing = append(missing, string(dt))
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("these destination types are not in respondingSenders, so nothing checks whether a "+
			"response_transformer on them is honest: %s\n\nAdd each one with its sender type.",
			strings.Join(missing, ", "))
	}
}

// configAcceptsResponseTransformer reports whether validation permits a response transformer on a type.
//
// Asked by running the real validator rather than by copying its list, which is the point: a copy would drift the same
// way the original did.
func configAcceptsResponseTransformer(t *testing.T, dt config.DestinationType) bool {
	t.Helper()

	d := config.Destination{
		Name:                "probe",
		Type:                dt,
		ResponseTransformer: "return true;",
	}

	for _, err := range config.ValidateDestinationForTest(&d) {
		if strings.Contains(err.Error(), "receives no reply to inspect") {
			return false
		}
	}
	return true
}
