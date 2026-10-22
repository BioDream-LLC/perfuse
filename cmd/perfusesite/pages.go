package main

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// sitePage is one section page: a fragment in site/pages, wrapped in the layout.
type sitePage struct {
	src, path, section, title, description, priority string
}

var sitePages = []sitePage{
	{"home.html", "/", "", "Perfuse: the free, open-source healthcare integration engine",
		"Perfuse connects hospital, lab, imaging and payer systems: HL7 v2, FHIR, DICOM, X12 and CDA in one program. Free under Apache 2.0, runs your Mirth channels, and is tested with the official Inferno suites in public.", "1.0"},
	{"platform.html", "/platform/", "platform", "The Perfuse platform: channels, builder, monitoring and security",
		"How Perfuse receives, checks, changes and delivers healthcare messages, and the tools around it: a browser builder, dashboards and alerts, shadow mode, and single sign-on.", "0.9"},
	{"connectors.html", "/connectors/", "connectors", "Connectors: every format and transport Perfuse speaks",
		"HL7 v2, FHIR, DICOM, X12, CDA and NCPDP over MLLP, HTTP, files, databases, AWS, Azure, Google Cloud, Kafka, MQTT and IBM MQ: 24 source types and 26 destination types.", "0.9"},
	{"payers.html", "/payers/", "payers", "For payers: CMS-0057-F, prior authorization and the provider directory",
		"The four CMS-0057-F FHIR APIs, Da Vinci CRD, DTR, PAS, PDex and Plan-Net, and CARIN Blue Button in one free engine tested with Inferno's suites, and an X12 278 link to your UM system.", "0.9"},
	{"sandbox.html", "/sandbox/", "payers", "Free payer sandbox: test Da Vinci CRD, DTR and PAS against a real payer server",
		"A free, public payer for EHR and app developers: Da Vinci CRD, DTR and PAS 2.2 on test data, SMART tokens with published test keys, and a script that runs the whole round.", "0.9"},
	{"exchange.html", "/exchange/", "exchange", "Exchange: public health reporting, TEFCA, SMART on FHIR and imaging",
		"eCR and ELR reporting, TEFCA Facilitated FHIR with UDAP, a built-in SMART on FHIR authorization server, and DICOM routing with de-identification.", "0.8"},
	{"mirth.html", "/mirth-connect-alternative/", "mirth", "A free alternative to Mirth Connect that runs your channels",
		"Mirth Connect needs a paid licence from 4.6. Perfuse is free under Apache 2.0, reads your Mirth channel exports, runs your JavaScript unchanged and proves the result on your own traffic.", "0.9"},
	{"proof.html", "/proof/", "proof", "Proof: how Perfuse is tested, and the results",
		"Inferno test results against the public test server, the real software Perfuse is tested against, and what has not been tested.", "0.8"},
	{"learn.html", "/learn/", "learn", "Guides: healthcare integration explained in plain language",
		"Plain-language guides to HL7, FHIR, CMS-0057-F, prior authorization, public health reporting, TEFCA and leaving Mirth Connect.", "0.8"},
	{"download.html", "/download/", "", "Download Perfuse",
		"Download Perfuse for Linux, macOS and Windows, on Intel and Arm. One file, no installer needed, free under Apache 2.0.", "0.9"},
}

var firstImage = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)

// guideCards lists the guides on the learn page, each with its picture.
func guideCards(root string) string {
	var b strings.Builder
	b.WriteString(`<div class="grid">`)
	for _, p := range pages {
		if !strings.HasPrefix(p.src, "site/articles/") {
			continue
		}
		img := ""
		if raw, err := os.ReadFile(filepath.Join(root, p.src)); err == nil {
			if m := firstImage.FindStringSubmatch(string(raw)); m != nil {
				img = `<img src="` + strings.Replace(m[2], "docs/assets/", "/assets/", 1) + `" alt="" loading="lazy" style="border-radius:10px;border:1px solid var(--line)">`
			}
		}
		fmt.Fprintf(&b, `<a class="card" href="/%s/">%s<h3>%s</h3><p>%s</p></a>`, p.dir, img, html.EscapeString(p.title),
			html.EscapeString(p.description))
	}
	b.WriteString(`</div>`)
	return b.String()
}
