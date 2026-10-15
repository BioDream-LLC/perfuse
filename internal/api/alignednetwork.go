package api

import (
	"net/http"

	"github.com/biodream-llc/perfuse/internal/store"
)

// CMS Aligned Network readiness: the 26 criteria of the CMS Interoperability Framework (CMS Health Technology Ecosystem,
// last modified 6 August 2026), each with what Perfuse provides for it and whether this server has it on. Networks
// self-attest; this is a worksheet for that attestation, not a certification. Criteria that belong to the organisation
// (HITRUST, agreements, publishing to the National Provider Directory) are marked as yours, and what Perfuse does not
// build is said plainly.

// alignedStatus is where one criterion stands on this server.
type alignedStatus string

const (
	// alignedReady: Perfuse provides it and it is on here.
	alignedReady alignedStatus = "ready"
	// alignedOff: Perfuse provides it and it is not switched on here.
	alignedOff alignedStatus = "off"
	// alignedPartial: Perfuse provides part of it; the rest is named.
	alignedPartial alignedStatus = "partial"
	// alignedYours: the organisation's to do (a policy, a certification, an agreement), not software's.
	alignedYours alignedStatus = "yours"
	// alignedNotBuilt: software would do it and Perfuse does not.
	alignedNotBuilt alignedStatus = "not-built"
)

type alignedCriterion struct {
	Number int           `json:"number"`
	Group  string        `json:"group"`
	Title  string        `json:"title"`
	Status alignedStatus `json:"status"`
	// InEffect marks the criteria CMS put in effect on 4 July 2026 (13 to 16).
	InEffect bool `json:"inEffect,omitempty"`
	// Perfuse is what Perfuse provides for it.
	Perfuse string `json:"perfuse"`
	// Missing is what this server lacks for it, each naming the setting or flag that turns it on.
	Missing []string `json:"missing"`
	// Gap is what nobody's software here does: the organisation's part, or what Perfuse does not build.
	Gap string `json:"gap,omitempty"`
	// Where is the screen that shows it.
	Where string `json:"where,omitempty"`
}

func (s *Server) handleAlignedNetwork(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	st := s.CMS0057
	fhir := st.FHIR
	smart := st.SMART || s.SMART != nil
	xds := s.XDS != nil
	xca := xds && s.XDS.HomeCommunityID != ""
	tefca := s.TEFCA != nil

	c := func(n int, group, title string, perfuse string, needs []need, gap, where string) alignedCriterion {
		out := alignedCriterion{Number: n, Group: group, Title: title, Perfuse: perfuse, Gap: gap, Where: where, Missing: []string{},
			InEffect: n >= 13 && n <= 16}
		for _, nd := range needs {
			if !nd.have {
				out.Missing = append(out.Missing, nd.what)
			}
		}
		switch {
		case perfuse == "":
			if gap == "" {
				out.Status = alignedNotBuilt
			} else {
				out.Status = alignedYours
			}
		case len(out.Missing) > 0:
			out.Status = alignedOff
		case gap != "":
			out.Status = alignedPartial
		default:
			out.Status = alignedReady
		}
		return out
	}
	fhirNeed := need{fhir, "the FHIR endpoint (Settings › FHIR › Serve a FHIR endpoint, -fhir)"}
	smartNeed := need{smart, "SMART on FHIR authorization (Settings › SMART: an issuer, or the built-in server's apps file)"}

	const (
		gPatient  = "Patient access and empowerment"
		gProvider = "Provider access and delegation"
		gData     = "Data availability and standards"
		gNetwork  = "Network connectivity and transparency"
		gTrust    = "Identity, security and trust"
	)
	list := []alignedCriterion{
		c(1, gPatient, "Patients reach their records, structured and unstructured, from apps of their choice",
			"A FHIR R4 endpoint on US Core with SMART App Launch: any registered app, commercial or not, reads the patient's resources, "+
				"and documents as DocumentReference with their Binary attachments.",
			[]need{fhirNeed, smartNeed}, "Reaching records held elsewhere on the network is the network's query fabric (criteria 16, 20, 21).", "Exchange › FHIR lab"),
		c(2, gPatient, "Patients reach claims, EOBs, prior authorizations and clinical data from current and past payers",
			"The CMS-0057 Patient Access API (CARIN Blue Button ExplanationOfBenefit, PDex, prior authorizations as ClaimResponse) and "+
				"Payer-to-Payer with $member-match.",
			[]need{fhirNeed, smartNeed, {st.PayerAPIs, "the payer APIs (Settings › Payers › CMS-0057-F, -fhir-payer-apis)"}}, "", "Payers › CMS-0057"),
		c(3, gPatient, "No extra login: an IAL2 and AAL2 credential returns the patient's records from every node",
			"SMART tokens are accepted from any OpenID Connect provider you trust, which can be a CMS-approved IAL2 identity service; "+
				"passkeys (AAL2) for people signing in to this server.",
			[]need{smartNeed}, "Identity proofing to IAL2 is the identity service's (such as Login.gov, CLEAR or ID.me), and a network-wide "+
				"answer needs the record locator (16). Digital insurance cards are not read.", "Settings › SMART"),
		c(4, gPatient, "An accounting of every network transaction: who saw the patient's data, when and why",
			"Every exchange this server makes is logged: the audit log of who did what, TEFCA exchanges with their purpose, and XDS, XCA "+
				"and MHD transactions with the patient and document.",
			nil, "An accounting a patient can read across the whole network is the network's to assemble.", "Administer › Activity"),
		c(5, gPatient, "Consent preferences travel with a transaction and are honoured",
			"Payer-to-Payer refuses $member-match without the member's Consent; Consent resources are stored and searchable; XDS "+
				"confidentiality codes travel with documents.",
			[]need{fhirNeed, {st.Consent, "member consent checking (leave $member-match-without-consent off)"}},
			"Perfuse does not enforce a general consent policy (such as a FHIR Consent restricting a disclosure) on every read.", "Payers › CMS-0057"),
		c(6, gProvider, "Full treatment access for a verified provider attesting treatment purpose",
			"Provider Access API (PDex $davinci-data-export by attribution) and TEFCA queries carrying the treatment purpose of use.",
			[]need{fhirNeed, {st.PayerAPIs || tefca, "the payer APIs (-fhir-payer-apis) or a TEFCA participant (-tefca-config)"}},
			"Checking a provider against the CMS National Provider Directory is not built.", "Exchange › TEFCA"),
		c(7, gProvider, "Providers may act through delegated vendors under a BAA",
			"API tokens and SMART Backend Services let a delegated system act with the role you give it, and every action is audited "+
				"under its name.", nil, "The Business Associate Agreement is yours.", "Administer › Users"),
		c(8, gProvider, "Payers query open quality gaps (HbA1c, mammograms, blood pressure...)",
			"", nil, "", ""),
		c(9, gProvider, "Payers query clinical data for an encounter on a claim from the last 60 days",
			"Claims attachments received with the claim: X12 275, HL7 DSDR signed documents and Da Vinci CDex $submit-attachment.",
			[]need{fhirNeed}, "A payer's query for a claim's encounter data, answered from the record, is not built.", "Payers › Claims & auth"),
		c(10, gData, "Chart notes and documents in machine- and human-readable form (USCDI v3)",
			"C-CDA read, validated and rendered to PDF; documents kept and returned with their format codes; CDA inside HL7 v2 "+
				"unwrapped.", nil, "", "Exchange › Documents"),
		c(11, gData, "Queries answered promptly and automatically, without portals",
			"Every query this server answers (FHIR, XDS, XCA, MHD, PIXm, PDQm) is answered automatically; a refusal says what is "+
				"missing.", []need{fhirNeed}, "", "Exchange › FHIR lab"),
		c(12, gData, "Appointment and encounter details shared",
			"Encounter and Appointment resources from HL7 v2 ADT and SIU feeds, served on US Core.", []need{fhirNeed}, "", "Exchange › FHIR lab"),
		c(13, gData, "FHIR APIs on US Core with a full CapabilityStatement, USCDI v3, standard terminology, and Bulk Data",
			"A FHIR R4 endpoint on US Core with its CapabilityStatement, v2 to FHIR mapped to LOINC, RxNorm and SNOMED where the "+
				"message says them, and Bulk Data $export.",
			[]need{fhirNeed, {st.BulkExport, "Bulk Data export (Settings › FHIR › Bulk data, -fhir-bulk-export)"}},
			"Terminology is what the sending systems code; a local code is kept and flagged, not translated.", "Exchange › FHIR lab"),
		c(14, gData, "Documents as human-readable FHIR attachments (PDF, TIFF, JPG)",
			"DocumentReference with Binary attachments, and IHE MHD (ITI-65 to 68) over the XDS registry: a PDF or scanned report is "+
				"returned as an attachment with its content type.",
			[]need{fhirNeed}, "", "Exchange › Document sharing"),
		c(15, gData, "Appointment and encounter notifications by FHIR Subscription",
			"Topic-based Subscriptions (R4 Backport) for encounter and appointment changes, notifying by rest-hook within seconds of "+
				"the ADT or SIU message.",
			[]need{fhirNeed, {st.Subscriptions, "FHIR subscriptions (Settings › FHIR › Notifications, -fhir-subscriptions)"}}, "", "Exchange › Subscriptions"),
		c(16, gData, "A record locator, so queries go only where records are",
			"An IHE XDS.b registry indexes which documents exist for a patient; XCA answers other communities from it; PIXm "+
				"cross-references identifiers.",
			[]need{{xds, "the document registry (Settings › Data › Document sharing, -xds)"}},
			"CMS's record locator model is still being defined with networks; Perfuse's registry covers this server's community.", "Exchange › Document sharing"),
		c(17, gNetwork, "Listed as an Aligned Network, with members and endpoints published in the National Provider Directory",
			"", nil, "Publishing membership and endpoints is the network's, in CMS's format.", ""),
		c(18, gNetwork, "National Provider Directory kept up to date",
			"", nil, "Updates in CMS's format and cadence are the network's.", ""),
		c(19, gNetwork, "Query metrics: counts of successful and unsuccessful replies",
			"Prometheus metrics for every channel and endpoint (messages, errors, latency), and the prior authorization metrics CMS-0057 "+
				"requires.", nil, "Reporting them to the directory in CMS's format is not built.", "Monitor › Metrics"),
		c(20, gNetwork, "Standards-based connectivity between networks",
			"XCA Responding Gateway (ITI-38, ITI-39) and XDS.b client; TEFCA facilitated FHIR and QHIN queries; Direct secure messaging.",
			[]need{{xca || tefca, "an XCA home community (-xds-home-community) or a TEFCA participant (-tefca-config)"}}, "", "Exchange › TEFCA"),
		c(21, gNetwork, "Network-wide or targeted search (by state, by NPI)",
			"XCA queries can be sent to one community (by its gateway) or several; FindDocuments narrows by author, class and practice "+
				"setting.", []need{{xds, "the document registry (-xds)"}}, "Fanning a query out across a whole network is the network's.", "Exchange › Document sharing"),
		c(22, gTrust, "Every query states its purpose (individual access, treatment, payment, operations)",
			"TEFCA exchanges carry and record the XP purpose of use; UDAP B2B tokens carry it.", []need{{tefca, "a TEFCA participant (-tefca-config)"}},
			"Plain FHIR and XDS queries to this server are not required to state a purpose.", "Exchange › TEFCA"),
		c(23, gTrust, "Accepts IAL2 and AAL2 digital credentials for patients and providers",
			"Passkeys (WebAuthn, AAL2) for signing in here, and OpenID Connect sign-in through the identity service you choose.",
			nil, "IAL2 proofing is the identity service's; mobile driver's licences (mDL) are not read.", "Administer › Users"),
		c(24, gTrust, "Access control and consent policy enforced for the context",
			"Roles, channel groups and tenants for people; SMART scopes for apps; API token roles and scopes for systems.",
			[]need{smartNeed}, "", "Administer › Users"),
		c(25, gTrust, "Verifiable logs of identity and authorization requests",
			"Sign-ins, token use and SMART authorizations are in the audit log; signed documents carry RFC 3161 time-stamps.",
			nil, "The audit log is not hash-chained, so independent review relies on the database's own controls.", "Administer › Activity"),
		c(26, gTrust, "HITRUST certification or equivalent",
			"", nil, "Certification is the organisation's. Perfuse's security notes and its HIPAA guide help with the assessment.", ""),
	}

	counts := map[alignedStatus]int{}
	for _, x := range list {
		counts[x.Status]++
	}
	s.ok(w, map[string]any{
		"criteria":  list,
		"counts":    counts,
		"source":    "CMS Interoperability Framework, CMS Health Technology Ecosystem, page last modified 6 August 2026",
		"sourceUrl": "https://www.cms.gov/initiatives/health-technology-ecosystem/overview/interoperability-framework",
	})
}

type need struct {
	have bool
	what string
}
