package main

import (
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/settings"
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
	"retention-days":                    "data.retentionDays",
	"store-messages":                    "data.storeMessages",
	"store-payloads":                    "data.storePayloads",
	"allow-metadata-egress":             "security.allowMetadataEgress",
	"alert-webhook":                     "alerts.webhook",
	"alert-severity":                    "alerts.minSeverity",
	"json-logs":                         "monitoring.jsonLogs",
	"trace-endpoint":                    "monitoring.traceEndpoint",
	"drain-for":                         "engine.drainFor",
	"fhir-read-only":                    "fhir.readOnly",
	"fhir-patient-directory-search":     "fhir.patientDirectorySearch",
	"fhir-api-tokens":                   "fhir.apiTokensWithSMART",
	"mcp":                               "security.mcp",
	"fhir-writes-per-minute":            "fhir.writesPerMinute",
	"fhir-max-resources":                "fhir.maxResources",
	"fleet-label":                       "fleet.label",
	"passkey-domain":                    "signin.passkeyDomain",
	"passkey-name":                      "signin.passkeyName",
	"scim":                              "signin.scimEnabled",
	"scim-default-role":                 "signin.scimDefaultRole",
	"fhir":                              "fhir.serve",
	"mpi":                               "data.patientIndex",
	"icd11-client-id":                   "data.icd11ClientId",
	"residency":                         "privacy.residency",
	"icd11-client-secret":               "data.icd11ClientSecret",
	"icd11-release":                     "data.icd11Release",
	"icd11-url":                         "data.icd11Url",
	"fhir-version":                      "fhir.version",
	"fhir-open":                         "fhir.open",
	"fhir-subscriptions":                "fhir.subscriptions",
	"fhir-subscriptions-allow-http":     "fhir.subscriptionsAllowHTTP",
	"fhir-bulk-export":                  "fhir.bulkExport",
	"public-url":                        "fhir.publicURL",
	"fhir-payer-apis":                   "payers.apis",
	"fhir-member-match-without-consent": "payers.memberMatchWithoutConsent",
	"pas":                               "payers.pas",
	"crd-rules":                         "payers.crdRules",
	"cds-clients":                       "payers.cdsClients",
	"pas-278-url":                       "payers.pas278URL",
	"pas-278-payer-id":                  "payers.pas278PayerID",
	"pas-278-sender":                    "payers.pas278Sender",
	"pas-278-receiver":                  "payers.pas278Receiver",
	"pas-278-production":                "payers.pas278Production",
	"pas-decider-url":                   "payers.pasDeciderURL",
	"pas-decider-token-env":             "payers.pasDeciderTokenEnv",
	"pas-decider-ca":                    "payers.pasDeciderCA",
	"formulary":                         "payers.formulary",
	"epa-payer-id":                      "payers.epaPayerID",
	"epa-reply-channel":                 "payers.epaReplyChannel",
	"crd-advisor-url":                   "payers.crdAdvisorURL",
	"crd-advisor-token-env":             "payers.crdAdvisorTokenEnv",
	"crd-advisor-ca":                    "payers.crdAdvisorCA",
	"pas-278-token-env":                 "payers.pas278TokenEnv",
	"ecr-agency":                        "publicHealth.ecrAgency",
	"ecr-responses":                     "publicHealth.ecrResponses",
	"rctc":                              "publicHealth.rctc",
	"shl-allow-http":                    "publicHealth.shlAllowHTTP",
	"dicomweb-archive":                  "data.dicomwebArchive",
	"xds":                               "data.xdsRepository",
	"xds-home-community":                "data.xdsHomeCommunity",
	"dicomweb-cors":                     "data.dicomwebCORS",
	"smart-issuer":                      "smart.issuer",
	"smart-audience":                    "smart.audience",
	"smart-authorize":                   "smart.authorize",
	"smart-token":                       "smart.token",
	"smart-jwks":                        "smart.jwks",
	"smart-jwks-uri":                    "smart.jwksURI",
	"smart-clients":                     "smart.clients",
	"smart-users":                       "smart.users",
	"smart-key":                         "smart.key",
	"smart-backend-services":            "smart.backendServices",
	"smart-oidc":                        "smart.oidc",
	"tls-cert":                          "security.tlsCert",
	"tls-key":                           "security.tlsKey",
	"signing-cert":                      "security.signingCert",
	"signing-key":                       "security.signingKey",
	"tsa-url":                           "security.tsaURL",
	"metrics-open":                      "security.metricsOpen",
	"metrics-token":                     "security.metricsToken",
	"passkey-origins":                   "signin.passkeyOrigins",
	"peers":                             "fleet.peersFile",
	"vpn":                               "monitoring.vpnFile",
	"dashboards":                        "monitoring.dashboardsFile",
	"inventory":                         "monitoring.inventory",
	"inventory-every":                   "monitoring.inventoryEvery",
	"trace-instance":                    "monitoring.traceInstance",
	"snmp-listen":                       "monitoring.snmpListen",
	"snmp-community":                    "monitoring.snmpCommunity",
	"snmp-allow":                        "monitoring.snmpAllow",
	"snmp-traps":                        "monitoring.snmpTraps",
	"snmp-base-oid":                     "monitoring.snmpBaseOID",
	"snmp-contact":                      "monitoring.snmpContact",
	"snmp-location":                     "monitoring.snmpLocation",
}

// The rest come from the registry: every setting that names a flag is seeded by it and applied to it. The table above was once
// the only list, kept by hand, and fell behind it: the utilization management link's transport and credentials, and
// -index-identity, were saved in the console and then ignored at start.
// listSettings are the settings whose flag is a comma-separated list.
var listSettings = func() map[string]bool {
	out := map[string]bool{}
	for _, st := range settings.Default() {
		if st.Kind == settings.KindList {
			out[st.Key] = true
		}
	}
	return out
}()

func init() {
	for _, st := range settings.Default() {
		if st.Flag != "" {
			if _, ok := settingFlags[st.Flag]; !ok {
				settingFlags[st.Flag] = st.Key
			}
		}
	}
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

		// Converted by the flag's own type, which flag.Getter gives. It was once converted by the value's shape, so a text
		// flag holding digits - a payer ID like 60054, an interchange receiver ID - became a number, and the settings file
		// refused it at start: "should be text, got 60054".
		raw := f.Value.String()
		// A list setting's flag is comma-separated, one entry per item.
		if listSettings[key] {
			var list []string
			for _, item := range strings.Split(raw, ",") {
				if item = strings.TrimSpace(item); item != "" {
					list = append(list, item)
				}
			}
			out[key] = list
			return
		}
		if g, ok := f.Value.(flag.Getter); ok {
			switch v := g.Get().(type) {
			case bool:
				out[key] = v
				return
			case int:
				out[key] = v
				return
			case int64:
				out[key] = int(v)
				return
			case string:
				out[key] = v
				return
			}
		}
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

// applyFileSettings sets each flag the settings file has a value for to that value, so code reading the flag sees what the
// console saved. The file wins over the command line (Conflicts reports the clash); this is what makes that true for
// settings that take effect at a restart, which are read from their flags.
func applyFileSettings(fset *flag.FlagSet, store interface {
	IsSet(string) bool
	Get(string) any
}) error {
	for name, key := range settingFlags {
		if !store.IsSet(key) {
			continue
		}
		f := fset.Lookup(name)
		if f == nil {
			continue
		}
		value := fmt.Sprint(store.Get(key))
		if list, ok := store.Get(key).([]string); ok {
			value = strings.Join(list, ",")
		}
		if err := f.Value.Set(value); err != nil {
			return fmt.Errorf("the settings file's %s cannot be used as -%s: %w", key, name, err)
		}
	}
	return nil
}
