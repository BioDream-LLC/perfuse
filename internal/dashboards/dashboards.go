// Package dashboards defines the dashboards Perfuse ships for each kind of person who watches it, and how a person is given
// one: by their role, by a directory group the dashboards file maps, or by the view they saved.
//
// Dashboards are made of tiles, and tiles show aggregates only - counts, rates, times, states. None shows a message, a patient
// or an identifier, so a dashboard can be given to a department manager or put on a wall screen without widening who sees
// patient data. A tile that needs data Perfuse does not yet record is not faked: the dashboard lists it under Planned.
//
// Full flexibility is Grafana's job, not this package's. Every tile that has a metric behind it exports as a Grafana panel over
// Perfuse's /metrics, so a site that wants more than light configuration takes the dashboard there.
package dashboards

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Tile is one panel.
type Tile struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Source is the console data the tile is drawn from; the console knows how to draw each.
	Source string `json:"source"`
	// Query is the PromQL for the same figure over /metrics, for the Grafana export. Empty: the tile has no metric yet, and
	// is drawn in the console only.
	Query string `json:"query,omitempty"`
	// Unit is how Grafana labels the value.
	Unit string `json:"unit,omitempty"`
}

// Dashboard is a persona's set of tiles.
type Dashboard struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Audience    string   `json:"audience"`
	Description string   `json:"description"`
	Tiles       []string `json:"tiles"`
	// Planned are figures this person needs that Perfuse does not record yet, said rather than drawn as empty tiles.
	Planned []string `json:"planned,omitempty"`
}

// Tiles are every tile there is.
var Tiles = []Tile{
	{ID: "channels", Title: "Channels", Description: "Running, stopped, and files that would not load.", Source: "status",
		Query: `sum(perfuse_channels_running)`, Unit: "short"},
	{ID: "throughput", Title: "Throughput", Description: "Messages received, delivered and failed over time.", Source: "throughput",
		Query: `sum by (channel) (rate(perfuse_messages_received_total[5m]))`, Unit: "reqps"},
	{ID: "outcomes", Title: "Outcomes, 24h", Description: "Delivered, failed, filtered and unparseable, as a share of everything received.", Source: "stats",
		Query: `sum(increase(perfuse_messages_failed_total[24h])) / sum(increase(perfuse_messages_received_total[24h]))`, Unit: "percentunit"},
	{ID: "errors-by-channel", Title: "Failures by channel, 24h", Description: "Where the failed and unparseable messages are, worst first.", Source: "stats",
		Query: `topk(10, sum by (channel) (increase(perfuse_messages_failed_total[24h]) + increase(perfuse_messages_unparseable_total[24h])))`, Unit: "short"},
	{ID: "naks-by-sender", Title: "Rejected by sender, 24h", Description: "Messages this server answered with a negative acknowledgement, per sending application.", Source: "stats"},
	{ID: "destinations", Title: "Destinations, 24h", Description: "Delivered, failed, attempts per message and delivery time, per destination.", Source: "destinations",
		Query: `sum by (destination) (increase(perfuse_delivery_failures_total[24h]))`, Unit: "short"},
	{ID: "queue", Title: "Queues", Description: "Messages waiting per destination, and how long the oldest has waited.", Source: "queue",
		Query: `max by (channel, destination) (perfuse_queue_oldest_seconds)`, Unit: "s"},
	{ID: "connections", Title: "Connections", Description: "Each destination checked in layers - name, network, TLS, delivery - with what to do when one fails.", Source: "connections"},
	{ID: "inbound", Title: "Inbound connections", Description: "Connections open on each listening channel.", Source: "status",
		Query: `sum by (channel) (perfuse_connections_open)`, Unit: "short"},
	{ID: "alerts", Title: "Alerts", Description: "What is firing now, and since when.", Source: "alerts"},
	{ID: "fhir-validation", Title: "FHIR validation", Description: "Resources converted and those that failed validation.", Source: "metrics",
		Query: `sum(increase(perfuse_fhir_validation_errors_total[24h]))`, Unit: "short"},
	{ID: "certificates", Title: "Certificates", Description: "TLS certificates in use and when they expire.", Source: "certificates"},
	{ID: "audit", Title: "Sign-ins and changes", Description: "Recent sign-ins, refusals and configuration changes, from the audit log.", Source: "audit"},
	{ID: "plain-summary", Title: "In plain words", Description: "One line per problem, written for someone who does not run interfaces.", Source: "summary"},
}

// Dashboards are the shipped dashboards.
var Dashboards = []Dashboard{
	{ID: "operations", Title: "Operations", Audience: "Integration team", Description: "Everything at once: the default.",
		Tiles: []string{"channels", "throughput", "outcomes", "destinations", "queue", "alerts"}},
	{ID: "connections", Title: "Connections", Audience: "Network and interface engineers",
		Description: "Can each partner be reached, layer by layer, and why not.",
		Tiles:       []string{"connections", "inbound", "queue", "certificates"},
		Planned: []string{"VPN tunnel state from AWS Site-to-Site VPN, Azure VPN Gateway and strongSwan",
			"a partner connection sheet with both sides' tunnel settings, and a check that they match"}},
	{ID: "interface-analyst", Title: "Interface analyst", Audience: "Interface analysts",
		Description: "Where messages are failing and backing up, so the right one gets replayed.",
		Tiles:       []string{"errors-by-channel", "naks-by-sender", "queue", "destinations", "throughput", "alerts"}},
	{ID: "prior-auth", Title: "Prior authorization", Audience: "Payer operations",
		Description: "The CMS-0057 prior authorization APIs and their health.",
		Tiles:       []string{"fhir-validation", "destinations", "alerts"},
		Planned:     []string{"PAS turnaround against the 72-hour and 7-day limits", "approval and denial rates, live (the yearly metrics page reads a decisions file)"}},
	{ID: "imaging", Title: "Imaging", Audience: "PACS and imaging administrators",
		Description: "DICOM and imaging feeds.",
		Tiles:       []string{"connections", "destinations", "queue", "errors-by-channel"},
		Planned:     []string{"C-STORE failures by modality", "routing delay", "worklist query failures", "reports that did not reach the EHR"}},
	{ID: "revenue-cycle", Title: "Revenue cycle", Audience: "Billing and EDI",
		Description: "X12 feeds and their failures.",
		Tiles:       []string{"errors-by-channel", "destinations", "throughput"},
		Planned:     []string{"999 and 277CA rejection rates", "835s matched to claims", "the commonest rejection reasons"}},
	{ID: "lab", Title: "Laboratory", Audience: "Lab IT",
		Description: "Result feeds: are results going out, and being acknowledged.",
		Tiles:       []string{"throughput", "destinations", "queue", "errors-by-channel"},
		Planned:     []string{"result delivery time from receipt to acknowledgement", "critical values not delivered"}},
	{ID: "privacy", Title: "Privacy and security", Audience: "Privacy and security officers",
		Description: "Who signed in, what changed, and the certificates.",
		Tiles:       []string{"audit", "certificates", "alerts"},
		Planned:     []string{"patient-record access by user, from the audit log", "API token use"}},
	{ID: "manager", Title: "Department summary", Audience: "Department managers",
		Description: "Read-only, in plain words: what is wrong and whether anyone is on it.",
		Tiles:       []string{"plain-summary", "channels", "alerts"}},
}

// TileByID finds a tile.
func TileByID(id string) (Tile, bool) {
	for _, t := range Tiles {
		if t.ID == id {
			return t, true
		}
	}
	return Tile{}, false
}

// ByID finds a dashboard.
func ByID(id string) (Dashboard, bool) {
	for _, d := range Dashboards {
		if d.ID == id {
			return d, true
		}
	}
	return Dashboard{}, false
}

// Assignments say which dashboard a person opens on: by directory group first, then by role. Read from the dashboards file.
type Assignments struct {
	Groups map[string]string `yaml:"groups" json:"groups"`
	Roles  map[string]string `yaml:"roles" json:"roles"`
}

// LoadAssignments reads a dashboards file and checks each name it uses.
func LoadAssignments(path string) (*Assignments, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var a Assignments
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&a); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, m := range []map[string]string{a.Groups, a.Roles} {
		for k, v := range m {
			if _, ok := ByID(v); !ok {
				return nil, fmt.Errorf("%s: %s is given dashboard %q, which does not exist", path, k, v)
			}
		}
	}
	return &a, nil
}

// For picks the dashboard for a person: the first of their groups the file maps, else their role, else operations.
func (a *Assignments) For(role string, groups []string) string {
	if a != nil {
		for _, g := range groups {
			if d, ok := a.Groups[g]; ok {
				return d
			}
		}
		if d, ok := a.Roles[role]; ok {
			return d
		}
	}
	return "operations"
}
