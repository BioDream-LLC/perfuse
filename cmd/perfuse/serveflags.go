package main

import (
	"flag"
	"strconv"
)

// settingFlags maps a command-line flag to the setting it seeds.
//
// Only flags that have a settings equivalent appear. The listen address, database path and channel directory deliberately do
// not: all three decide where the process finds the thing it would be editing, so they stay flags.
//
// A map rather than logic, so adding a setting with a flag is one line and the pairing is readable in one place. The settings
// registry records the same pairing from its own side, and TestEveryFlaggedSettingHasItsFlag checks the two agree - two
// descriptions of one relationship is exactly the drift this whole area exists to avoid.
var settingFlags = map[string]string{
	"retention-days":        "data.retentionDays",
	"store-messages":        "data.storeMessages",
	"store-payloads":        "data.storePayloads",
	"allow-metadata-egress": "security.allowMetadataEgress",
	"alert-webhook":         "alerts.webhook",
	"alert-severity":        "alerts.minSeverity",
	"json-logs":             "monitoring.jsonLogs",
	"trace-endpoint":        "monitoring.traceEndpoint",
	"drain-for":             "engine.drainFor",
	"fhir-read-only":        "fhir.readOnly",
	"fleet-label":           "fleet.label",
	"passkey-domain":        "signin.passkeyDomain",
	"passkey-name":          "signin.passkeyName",
	"scim":                  "signin.scimEnabled",
	"scim-default-role":     "signin.scimDefaultRole",
}

// flagSettings collects the settings implied by flags the operator actually passed.
//
// Only flags that were given, which is the important part. Including every flag at its default would mean the first start writes
// a file asserting every default as a deliberate choice - and from then on nothing could be changed by upgrading, because every
// value would look like something somebody decided.
//
// It also makes the conflict report meaningful: a flag nobody passed cannot disagree with the file.
func flagSettings(fset *flag.FlagSet) map[string]any {
	out := make(map[string]any)

	fset.Visit(func(f *flag.Flag) {
		key, ok := settingFlags[f.Name]
		if !ok {
			return
		}

		// The value arrives as text, because that is what a flag is. Converted by shape rather than by consulting the
		// registry, so this function has no opinion about what any particular setting means.
		raw := f.Value.String()

		switch raw {
		case "true":
			out[key] = true
		case "false":
			out[key] = false
		default:
			if n, err := strconv.Atoi(raw); err == nil {
				out[key] = n

				break
			}
			out[key] = raw
		}
	})

	return out
}
