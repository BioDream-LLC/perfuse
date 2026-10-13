package main

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

// The site's shared layout: the menu across the top, the footer, and the icons. Pages are HTML fragments in site/pages and
// articles are markdown in site/articles; both are wrapped here, so every page has the same menu and nothing drifts.

// icons are drawn for this site: 24-unit stroke icons, coloured by currentColor.
var icons = map[string]string{
	"engine":     `<path d="M4 7h10M4 12h16M4 17h7"/><circle cx="17" cy="7" r="2.2"/><circle cx="14" cy="17" r="2.2"/>`,
	"plug":       `<path d="M9 3v5M15 3v5M7 8h10v3a5 5 0 0 1-10 0V8zM12 16v5"/>`,
	"shield":     `<path d="M12 3l7 3v5c0 5-3.2 8.4-7 10-3.8-1.6-7-5-7-10V6l7-3z"/><path d="M9 12l2 2 4-4"/>`,
	"heart":      `<path d="M3 12h4l2-5 3 10 2-5h7"/>`,
	"payer":      `<rect x="3" y="5" width="18" height="14" rx="2"/><path d="M3 10h18M7 15h4"/>`,
	"swap":       `<path d="M4 8h13l-3-3M20 16H7l3 3"/>`,
	"check":      `<circle cx="12" cy="12" r="9"/><path d="M8 12.5l2.8 2.8L16.5 9"/>`,
	"book":       `<path d="M4 5a2 2 0 0 1 2-2h13v16H6a2 2 0 0 0-2 2V5z"/><path d="M8 7h7"/>`,
	"download":   `<path d="M12 3v12M7 10l5 5 5-5M4 20h16"/>`,
	"github":     `<path d="M9 19c-4 1.3-4-2-6-2.5M15 21v-3.5c0-1 .1-1.4-.5-2 2.8-.3 5.5-1.4 5.5-6a4.6 4.6 0 0 0-1.3-3.2 4.3 4.3 0 0 0-.1-3.2s-1-.3-3.4 1.3a11.7 11.7 0 0 0-6.2 0C6.6 2.8 5.6 3.1 5.6 3.1a4.3 4.3 0 0 0-.1 3.2A4.6 4.6 0 0 0 4.2 9.5c0 4.6 2.7 5.7 5.5 6-.6.6-.6 1.2-.5 2V21"/>`,
	"chart":      `<path d="M4 20V4M4 20h16"/><path d="M8 16l3-4 3 2 5-7"/>`,
	"queue":      `<rect x="3" y="4" width="18" height="4" rx="1"/><rect x="3" y="10" width="18" height="4" rx="1"/><rect x="3" y="16" width="18" height="4" rx="1"/>`,
	"code":       `<path d="M9 8l-5 4 5 4M15 8l5 4-5 4"/>`,
	"flask":      `<path d="M9 3h6M10 3v6L5 19a1.5 1.5 0 0 0 1.3 2h11.4a1.5 1.5 0 0 0 1.3-2L14 9V3"/><path d="M7.5 15h9"/>`,
	"image":      `<rect x="3" y="4" width="18" height="16" rx="2"/><circle cx="9" cy="10" r="2"/><path d="M21 16l-5-5-9 9"/>`,
	"doc":        `<path d="M6 3h8l4 4v14H6z"/><path d="M14 3v4h4M9 12h6M9 16h6"/>`,
	"globe":      `<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c3 3.5 3 14.5 0 18M12 3c-3 3.5-3 14.5 0 18"/>`,
	"users":      `<circle cx="9" cy="8" r="3.2"/><path d="M3 20c.8-3.4 3.2-5 6-5s5.2 1.6 6 5"/><circle cx="17" cy="9" r="2.5"/><path d="M16 14.5c2.3.2 4.2 1.7 5 4.5"/>`,
	"layers":     `<path d="M12 3l9 5-9 5-9-5 9-5z"/><path d="M3 13l9 5 9-5"/>`,
	"eye":        `<path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>`,
	"bell":       `<path d="M6 16V11a6 6 0 0 1 12 0v5l2 2H4l2-2z"/><path d="M10 20a2 2 0 0 0 4 0"/>`,
	"cloud":      `<path d="M7 18a4.5 4.5 0 0 1-.6-9A6 6 0 0 1 18 9.5a4.3 4.3 0 0 1-.5 8.5H7z"/>`,
	"key":        `<circle cx="8" cy="15" r="4"/><path d="M11 12l8-8M16 7l2 2M14 9l2 2"/>`,
	"server":     `<rect x="4" y="3" width="16" height="7" rx="1.5"/><rect x="4" y="14" width="16" height="7" rx="1.5"/><path d="M8 6.5h.01M8 17.5h.01"/>`,
	"arrowright": `<path d="M5 12h14M13 6l6 6-6 6"/>`,
	"lab":        `<path d="M7 3h10M8 3v4l-3 9a4 4 0 0 0 3.8 5h6.4a4 4 0 0 0 3.8-5l-3-9V3"/>`,
	"menu":       `<path d="M4 7h16M4 12h16M4 17h16"/>`,
}

// icon renders one icon.
func icon(name string) string {
	return `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">` +
		icons[name] + `</svg>`
}

type navItem struct {
	href, icon, title, blurb string
}

type navSection struct {
	key, title, href string
	items            []navItem
}

// menu is the site's top menu. key matches a page's section, so the menu marks where the reader is.
var menu = []navSection{
	{key: "platform", title: "Platform", href: "/platform/", items: []navItem{
		{"/platform/#engine", "engine", "The integration engine", "Channels that receive, check, change and deliver messages"},
		{"/platform/#builder", "layers", "Channel builder", "Build interfaces in the browser, no code needed"},
		{"/platform/#monitoring", "chart", "Monitoring and alerts", "Dashboards, flow map, alerts, message search"},
		{"/platform/#safe-change", "eye", "Changing live interfaces", "Shadow mode, feed contracts, drift detection"},
		{"/platform/#security", "shield", "Security and sign-on", "SAML, OIDC, LDAP, passkeys, SMART, audit"},
		{"/platform/#operations", "server", "Running it", "One file, every platform, multi-tenant"},
	}},
	{key: "connectors", title: "Connectors", href: "/connectors/", items: []navItem{
		{"/connectors/#formats", "doc", "Healthcare formats", "HL7 v2, FHIR, DICOM, X12, CDA, NCPDP"},
		{"/connectors/#transports", "plug", "Transports", "MLLP, TCP, HTTP, SOAP, files, FTP, SFTP, databases"},
		{"/connectors/#cloud", "cloud", "Cloud and queues", "AWS, Azure, Google Cloud, Kafka, MQTT, IBM MQ"},
	}},
	{key: "payers", title: "Payers", href: "/payers/", items: []navItem{
		{"/payers/#cms-0057", "payer", "CMS-0057-F, all four APIs", "Patient Access, Provider Access, Payer-to-Payer, Prior Auth"},
		{"/payers/#prior-auth", "check", "Prior authorization", "Da Vinci CRD, DTR and PAS, and the X12 278"},
		{"/payers/#directory", "users", "Provider directory", "Da Vinci Plan-Net"},
		{"/payers/#claims", "doc", "Claims and attachments", "837, 835, CARIN Blue Button, signed 275s"},
	}},
	{key: "exchange", title: "Exchange", href: "/exchange/", items: []navItem{
		{"/exchange/#public-health", "heart", "Public health reporting", "eCR and ELR, sent and answered"},
		{"/exchange/#tefca", "globe", "TEFCA and UDAP", "National exchange, trust and audit"},
		{"/exchange/#smart", "key", "SMART on FHIR", "A built-in authorization server for apps"},
		{"/exchange/#imaging", "image", "Imaging", "DICOM routing and de-identification"},
	}},
	{key: "mirth", title: "Leaving Mirth", href: "/mirth-connect-alternative/"},
	{key: "proof", title: "Proof", href: "/proof/"},
	{key: "learn", title: "Learn", href: "/learn/", items: []navItem{
		{"/learn/", "book", "Guides", "Plain-language guides to the standards and the work"},
		{"/manual/", "doc", "Manual", "Every feature, every setting, in one document"},
		{"/manual/perfuse-manual.pdf", "download", "Manual as PDF", "The same manual, for printing"},
	}},
}

// layout wraps a page body in the site's head, menu and footer.
func layout(base, path, section, title, description, body, ogType string) string {
	var b strings.Builder
	canonical := base + path
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	fmt.Fprintf(&b, "<title>%s</title>\n", html.EscapeString(title))
	fmt.Fprintf(&b, "<meta name=\"description\" content=\"%s\">\n", html.EscapeString(description))
	fmt.Fprintf(&b, "<link rel=\"canonical\" href=\"%s\">\n", html.EscapeString(canonical))
	b.WriteString("<link rel=\"icon\" type=\"image/svg+xml\" href=\"/assets/perfuse-logo.svg\">\n")
	// The stylesheet is cached for a day, so its URL carries a hash of its contents: a changed stylesheet is a new URL, and a
	// visitor never sees new pages drawn with yesterday's styles.
	fmt.Fprintf(&b, "<link rel=\"stylesheet\" href=\"/site.css?v=%s\">\n", cssVersion)
	fmt.Fprintf(&b, "<meta property=\"og:type\" content=\"%s\">\n", ogType)
	fmt.Fprintf(&b, "<meta property=\"og:title\" content=\"%s\">\n", html.EscapeString(title))
	fmt.Fprintf(&b, "<meta property=\"og:description\" content=\"%s\">\n", html.EscapeString(description))
	fmt.Fprintf(&b, "<meta property=\"og:url\" content=\"%s\">\n", html.EscapeString(canonical))
	fmt.Fprintf(&b, "<meta property=\"og:image\" content=\"%s/assets/social-card.png\">\n", base)
	b.WriteString("<meta name=\"twitter:card\" content=\"summary_large_image\">\n")
	b.WriteString("<meta name=\"author\" content=\"BioDream Developer\">\n")
	b.WriteString("</head>\n<body>\n<a class=\"skip\" href=\"#main\">Skip to content</a>\n")

	b.WriteString("<header class=\"top\"><div class=\"wrap\">\n")
	b.WriteString("<a class=\"brand\" href=\"/\"><img src=\"/assets/perfuse-logo.svg\" alt=\"\">Perfuse</a>\n")
	b.WriteString("<ul class=\"menu\">\n")
	for _, s := range menu {
		cur := ""
		if s.key == section {
			cur = ` aria-current="page"`
		}
		fmt.Fprintf(&b, "<li><a href=\"%s\"%s>%s</a>", s.href, cur, s.title)
		if len(s.items) > 0 {
			b.WriteString("<ul class=\"drop\">")
			for _, it := range s.items {
				fmt.Fprintf(&b, "<li><a href=\"%s\">%s<span><b>%s</b><small>%s</small></span></a></li>",
					it.href, icon(it.icon), html.EscapeString(it.title), html.EscapeString(it.blurb))
			}
			b.WriteString("</ul>")
		}
		b.WriteString("</li>\n")
	}
	b.WriteString("</ul>\n<div class=\"cta\">")
	b.WriteString("<a class=\"btn btn-ghost\" href=\"https://github.com/biodream-llc/perfuse\">" + icon("github") + "GitHub</a>")
	b.WriteString("<a class=\"btn btn-primary\" href=\"/download/\">" + icon("download") + "Download</a></div>\n")
	b.WriteString("<details class=\"mobile\"><summary aria-label=\"Menu\">" + `<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">` + icons["menu"] + "</svg></summary><nav>")
	for _, s := range menu {
		fmt.Fprintf(&b, "<b>%s</b><a href=\"%s\">%s</a>", s.title, s.href, s.title+" overview")
		for _, it := range s.items {
			fmt.Fprintf(&b, "<a href=\"%s\">%s</a>", it.href, html.EscapeString(it.title))
		}
	}
	b.WriteString("<a href=\"/download/\">Download</a><a href=\"https://github.com/biodream-llc/perfuse\">GitHub</a></nav></details>\n")
	b.WriteString("</div></header>\n")

	b.WriteString("<main id=\"main\">\n")
	b.WriteString(body)
	b.WriteString("\n</main>\n")
	b.WriteString(footer)
	b.WriteString("</body>\n</html>\n")
	return b.String()
}

const footer = `<footer class="site"><div class="wrap">
<div class="cols">
<div><a class="brand" href="/" style="display:flex"><img src="/assets/perfuse-logo.svg" alt="">Perfuse</a>
<p style="margin-top:12px">A free, open-source healthcare integration engine from BioDream LLC. Apache License 2.0.</p></div>
<div><b>Product</b><a href="/platform/">Platform</a><a href="/connectors/">Connectors</a><a href="/payers/">Payers</a><a href="/exchange/">Exchange</a></div>
<div><b>Moving</b><a href="/mirth-connect-alternative/">Leaving Mirth</a><a href="/migrate-mirth-channels/">Migration guide</a><a href="/integration-engines-compared/">Engines compared</a></div>
<div><b>Evidence</b><a href="/proof/">Test results</a><a href="https://test.perfuse.health/">Public test server</a><a href="https://github.com/biodream-llc/perfuse/blob/main/docs/verification.md">Verification record</a></div>
<div><b>Help</b><a href="/learn/">Guides</a><a href="/manual/">Manual</a><a href="/download/">Download</a><a href="https://github.com/biodream-llc/perfuse">GitHub</a></div>
</div>
<div class="legal">© BioDream LLC. Perfuse is free software. HL7, FHIR and the FHIR flame are trademarks of Health Level Seven International. Mirth is a trademark of NextGen Healthcare. Other names belong to their owners; Perfuse is not affiliated with them.</div>
</div></footer>
`

var iconRE = regexp.MustCompile(`ICON:[a-z]+`)

var headingRE = regexp.MustCompile(`<h2 id="([^"]+)">(.*?)</h2>`)

// toc lists an article's sections, from the ids the renderer gives its h2 headings.
func toc(body string) string {
	var b strings.Builder
	for _, m := range headingRE.FindAllStringSubmatch(body, -1) {
		text := regexp.MustCompile(`<[^>]+>`).ReplaceAllString(m[2], "")
		fmt.Fprintf(&b, "<a href=\"#%s\">%s</a>", m[1], text)
	}
	return b.String()
}
