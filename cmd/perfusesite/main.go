// Command perfusesite builds the perfuse.health website as a directory of static files.
//
// The site is generated from the documents the repository already publishes rather than being a
// separate hand-written copy of them. That is a deliberate response to a defect this project hit
// twice in one day: the published manual sat three chapters behind the website because they were
// two documents saying the same thing, and the site deploy read a file its trigger did not watch.
// Two copies of a fact drift, and nothing tells you when they have. One source does not.
//
// Usage:
//
//	perfusesite -out dist-site -base https://perfuse.health
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/manual"
)

// indexNowKey proves to IndexNow that whoever submits URLs for this site controls it. The key is
// served as a file at the site root; anyone can read it, which is fine - it authorises notifying a
// search engine that a page changed and nothing else.
const indexNowKey = "8f08bc81acb53071f925d0fe1603b225"

// page is one markdown document published as its own URL.
type page struct {
	src         string // path to the markdown, relative to the module root
	dir         string // directory under the site root, "" for the root itself
	title       string
	description string
	priority    string // sitemap priority
	// section, when set, publishes only that "## " section of src (to the next one), its subsections as the page's
	// sections: the manual's standards tables are one text, in the manual, the README and here.
	section string
}

// The documents published as pages. Anything referenced from the README has to be here, because a
// link that resolves on GitHub and 404s on the website is worse than no link.
var pages = []page{
	{
		src:         "site/articles/cms-0057-readiness-checklist.md",
		dir:         "cms-0057-readiness-checklist",
		title:       "CMS-0057-F before January 2027: a payer's readiness checklist",
		description: "The four CMS-0057-F APIs, the decision timeframes and the metrics, in the order to work on them and what to test first, and how Perfuse covers each.",
		priority:    "0.9",
	},
	{
		src:         "site/articles/davinci-2-1-and-2-2.md",
		dir:         "davinci-2-1-and-2-2",
		title:       "Da Vinci 2.1.0 or 2.2: which release a payer must answer in 2027",
		description: "Why EHRs will send Da Vinci CRD, DTR and PAS in both 2.1.0 and 2.2.x, what differs, and how one Perfuse server answers each caller in its own release.",
		priority:    "0.9",
	},
	{
		src:         "site/articles/hl7-v2-and-european-reports.md",
		dir:         "hl7-v2-and-european-reports",
		title:       "HL7 v2 and the European lab and discharge reports: what a feed can give, and what it cannot",
		description: "Converting HL7 v2 to the HL7 Europe Laboratory and Hospital Discharge Reports: what conversion can do, what needs a clinician's sign-off, and Perfuse's results.",
		priority:    "0.8",
	},
	{
		src:         "site/articles/lab-result-people.md",
		dir:         "lab-result-people",
		title:       "Who ordered, who performed, who signed it off: the people on a lab result, from HL7 v2 to FHIR",
		description: "Where OBR-16, OBR-32, OBX-15 and OBX-23 to OBX-25 belong in FHIR, the mistakes that are easy to make, and how Perfuse maps them.",
		priority:    "0.8",
	},
	{
		src:         "site/articles/silent-interface-failures.md",
		dir:         "silent-interface-failures",
		title:       "The quiet feed: why hospital interfaces fail silently, and how to notice",
		description: "A stopped feed produces no errors. Why fixed timers fail on clinical feeds, how rhythm-aware alerts work, and Perfuse's below-rhythm alert.",
		priority:    "0.8",
	},
	{
		src:         "site/articles/patient-matching.md",
		dir:         "patient-matching",
		title:       "Patient matching: one person, many records, and why automatic linking should start switched off",
		description: "Deterministic and probabilistic patient matching, blocking keys, why overlays are worse than duplicates, and Perfuse's optional patient index.",
		priority:    "0.8",
	},
	{
		src:         "site/articles/dicom-routing.md",
		dir:         "dicom-routing",
		title:       "DICOM routing and de-identification: moving medical images safely",
		description: "The DICOM services, imaging routing and de-identification explained, and how Perfuse routes and de-identifies studies in one pipeline.",
		priority:    "0.8",
	},
	{
		src:         "site/articles/claims-attachments.md",
		dir:         "claims-attachments",
		title:       "Claims attachments and CMS-0053: signed C-CDAs in the X12 275",
		description: "The X12 275, C-CDA attachments and HL7 DSDR signatures explained, and how Perfuse builds and verifies signed attachments.",
		priority:    "0.8",
	},
	{
		src:         "site/articles/prior-auth-metrics.md",
		dir:         "prior-auth-metrics",
		title:       "Prior authorization metrics under CMS-0057-F: what payers must publish, and how",
		description: "What the CMS-0057-F prior authorization metrics page must show, and how Perfuse produces it from a utilization management export.",
		priority:    "0.8",
	},
	{
		src:         "site/articles/integration-engines-compared.md",
		dir:         "integration-engines-compared",
		title:       "Open-source healthcare integration engines compared: Mirth 4.5.2, OIE, BridgeLink and Perfuse",
		description: "Mirth Connect 4.5.2, Open Integration Engine, BridgeLink and Perfuse side by side: origin, runtime, licence, channels and features.",
		priority:    "0.8",
	},
	{
		src:         "site/articles/shadow-mode.md",
		dir:         "shadow-mode",
		title:       "Shadow mode: how to change a live interface without guessing",
		description: "Run a new version of a channel on real traffic with nothing delivered, and see field by field what changes.",
		priority:    "0.8",
	},
	{
		src:         "site/articles/feed-contracts.md",
		dir:         "feed-contracts",
		title:       "Feed contracts: catch upstream changes before they break an interface",
		description: "Profile what a feed really contains, turn it into a contract, and be told the day a sender changes something.",
		priority:    "0.8",
	},
	{
		src:         "site/articles/ecr-elr.md",
		dir:         "ecr-elr",
		title:       "eCR and ELR explained: automatic public health reporting from clinical data",
		description: "How electronic case reporting and electronic lab reporting work, from trigger codes to Reportability Responses, and how Perfuse automates both.",
		priority:    "0.8",
	},
	{
		src:         "site/articles/tefca-udap.md",
		dir:         "tefca-udap",
		title:       "TEFCA and UDAP explained: national health data exchange, in plain terms",
		description: "How TEFCA, QHINs, Facilitated FHIR and UDAP work together for national exchange, and how Perfuse takes part.",
		priority:    "0.8",
	},
	{
		src:         "site/articles/smart-on-fhir.md",
		dir:         "smart-on-fhir",
		title:       "SMART on FHIR explained: how apps get secure access to health data",
		description: "SMART App Launch, Backend Services, scopes and PKCE explained, and Perfuse's built-in SMART authorization server.",
		priority:    "0.8",
	},
	{
		src:         "site/articles/x12-278-vs-pas.md",
		dir:         "x12-278-vs-pas",
		title:       "X12 278 and FHIR PAS: two ways to request prior authorization, and how to bridge them",
		description: "The X12 278 and Da Vinci PAS compared, why payers need both, and how Perfuse converts between them.",
		priority:    "0.8",
	},
	{
		src:         "site/articles/cms-0057-explained.md",
		dir:         "cms-0057-explained",
		title:       "CMS-0057-F explained: the Interoperability and Prior Authorization rule, in plain terms",
		description: "Who CMS-0057-F applies to, what it requires from 2026 and 2027, the FHIR standards behind it, and how Perfuse delivers the four payer APIs.",
		priority:    "0.9",
	},
	{
		src:         "site/articles/da-vinci-prior-authorization.md",
		dir:         "da-vinci-prior-authorization",
		title:       "Da Vinci CRD, DTR and PAS explained: electronic prior authorization from order to decision",
		description: "How the Da Vinci CRD, DTR and PAS guides work together for electronic prior authorization, and how Perfuse implements all three.",
		priority:    "0.9",
	},
	{
		src:         "site/articles/migrate-mirth-channels.md",
		dir:         "migrate-mirth-channels",
		title:       "How to migrate Mirth Connect channels, step by step",
		description: "Export, explain, translate, test and prove: a step-by-step migration from Mirth Connect, OIE or BridgeLink to Perfuse.",
		priority:    "0.9",
	},
	{
		src:         "site/articles/hl7-v2-to-fhir.md",
		dir:         "hl7-v2-to-fhir",
		title:       "HL7 v2 to FHIR: a practical guide to converting real feeds",
		description: "What HL7 v2 messages map to in FHIR, the details that make or break a conversion, and how Perfuse converts v2 to FHIR and US Core.",
		priority:    "0.8",
	},
	{
		src:         "site/articles/mirth-licence-change.md",
		dir:         "mirth-licence-change",
		title:       "Mirth Connect 4.6 licence change: your options, and the fastest way forward",
		description: "Mirth Connect now needs a paid licence from version 4.6. Your options, and how Perfuse, a free Apache-2.0 engine, runs your existing Mirth channels from a single download.",
		priority:    "0.9",
	},
	{
		src:         "README.md",
		dir:         "overview",
		title:       "Perfuse — a healthcare integration engine",
		description: "What Perfuse is, what it connects to, how it compares to Mirth Connect, and what has been verified against real software.",
		priority:    "0.9",
	},
	{
		src:         "docs/reference.md",
		dir:         "reference",
		title:       "Reference — every command, connector and configuration key",
		description: "Complete reference for Perfuse: commands, connectors, transformation steps and every configuration key.",
		priority:    "0.8",
	},
	{
		src:         "internal/manual/chapters/introduction.md",
		section:     "Standards and versions",
		dir:         "standards",
		title:       "Standards and versions Perfuse supports: HL7, FHIR, Da Vinci, X12, NCPDP, IHE",
		description: "Every standard Perfuse implements, the release of each (Da Vinci PAS 2.2.1 and 2.1.0, CRD, DTR, US Core, SMART, X12 005010, NCPDP SCRIPT), and the outside tool that checked it.",
		priority:    "0.9",
	},
	{
		src:         "docs/verification.md",
		dir:         "verification",
		title:       "What was tested, what it found, and what was never run",
		description: "Perfuse tested against real Keycloak, real Entra, real Mirth Connect, HAPI and Orthanc — including what has not been tested.",
		priority:    "0.8",
	},
	{
		src:         "docs/hl7-api.md",
		dir:         "hl7-api",
		title:       "The HL7 API",
		description: "Reading and writing HL7 v2 messages with the Perfuse Go API.",
		priority:    "0.6",
	},
	{
		src:         "docs/releasing.md",
		dir:         "releasing",
		title:       "Releasing Perfuse",
		description: "The release checklist: what is built, what is verified, and what is published.",
		priority:    "0.3",
	},
}

func main() {
	out := flag.String("out", "dist-site", "directory to write the site into")
	base := flag.String("base", "https://perfuse.health", "canonical base URL, no trailing slash")
	root := flag.String("root", ".", "module root")
	flag.StringVar(&manualsDir, "manuals", "dist-manuals", "directory of the manual's translations (perfuse-manual.<lang>.html), from make manuals")
	flag.Parse()

	if err := run(*root, *out, strings.TrimSuffix(*base, "/")); err != nil {
		fmt.Fprintln(os.Stderr, "perfusesite:", err)
		os.Exit(1)
	}
}

// manualsDir holds the manual's translations; each is published at /manual/<lang>/. Absent: English only.
var manualsDir = "dist-manuals"

// cssVersion is a short hash of site/static/site.css, put in the stylesheet's URL by layout.
var cssVersion = "0"

func run(root, out, base string) error {
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	if css, err := os.ReadFile(filepath.Join(root, "site/static/site.css")); err == nil {
		sum := sha256.Sum256(css)
		cssVersion = hex.EncodeToString(sum[:4])
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	var urls []sitemapEntry

	// The hand-written landing pages, copied with their URLs pointed at the new home.
	// The section pages: HTML fragments in site/pages, each wrapped in the shared layout.
	for _, sp := range sitePages {
		frag, err := os.ReadFile(filepath.Join(root, "site/pages", sp.src))
		if err != nil {
			return fmt.Errorf("reading %s: %w", sp.src, err)
		}
		body := iconRE.ReplaceAllStringFunc(rewriteHost(string(frag), base), func(m string) string { return icon(m[5:]) })
		if sp.path == "/learn/" {
			body = strings.Replace(body, "<!--GUIDES-->", guideCards(root), 1)
		}
		page := layout(base, sp.path, sp.section, sp.title, sp.description, body, "website")
		for _, v := range europeVersions {
			if v.path == sp.path {
				page = layoutLang(base, sp.path, sp.section, sp.title, sp.description, body, "website", v.lang, europeVersions)
			}
		}
		if err := writeFile(filepath.Join(out, strings.TrimPrefix(sp.path, "/"), "index.html"), []byte(page)); err != nil {
			return err
		}
		urls = append(urls, sitemapEntry{Loc: base + sp.path, Priority: sp.priority})
	}

	// Guides and documents: markdown, rendered into the article layout with a contents list beside it.
	for _, p := range pages {
		raw, err := os.ReadFile(filepath.Join(root, p.src))
		if err != nil {
			return fmt.Errorf("reading %s: %w", p.src, err)
		}
		if p.section != "" {
			raw = []byte(sectionOf(string(raw), p.section))
		}
		md := stripShieldsBadges(rewriteLinks(rewriteHost(string(raw), base)))
		// The byline is in the page header; the guide's own line would say it twice.
		md = strings.Replace(md, "By **BioDream Developer**, BioDream LLC\n", "", 1)
		prose := manual.RenderProse(md)
		words := len(strings.Fields(md))
		section := "learn"
		if !strings.HasPrefix(p.src, "site/articles/") {
			section = ""
		}
		body := fmt.Sprintf(`<div class="article-head"><div class="wrap"><p class="eyebrow">%s</p><h1>%s</h1>`+
			`<p class="meta">By BioDream Developer, BioDream LLC · about %d minutes to read</p></div></div>`+
			`<div class="wrap article"><aside><b>On this page</b>%s</aside><div class="prose">%s</div></div>`,
			map[bool]string{true: "Guide", false: "Documentation"}[section == "learn"], html.EscapeString(p.title),
			max(1, words/220), toc(prose), prose)
		page := layout(base, "/"+p.dir+"/", section, p.title, p.description, body, "article")
		if err := writeFile(filepath.Join(out, p.dir, "index.html"), []byte(page)); err != nil {
			return err
		}
		urls = append(urls, sitemapEntry{Loc: base + "/" + p.dir + "/", Priority: p.priority})
	}

	// The stylesheet and anything else the pages share.
	if err := filepath.WalkDir(filepath.Join(root, "site/static"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(filepath.Join(root, "site/static"), p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return writeFile(filepath.Join(out, rel), b)
	}); err != nil {
		return fmt.Errorf("copying site/static: %w", err)
	}

	man, err := os.ReadFile(filepath.Join(root, "docs/manual/perfuse-manual.html"))
	if err != nil {
		return fmt.Errorf("reading the manual: %w", err)
	}
	if err := writeFile(filepath.Join(out, "manual/index.html"), []byte(rewriteHost(string(man), base))); err != nil {
		return err
	}
	urls = append(urls, sitemapEntry{Loc: base + "/manual/", Priority: "0.9"})

	// The manual's translations, made by machine (each says so under its title), one page per language.
	if files, err := filepath.Glob(filepath.Join(manualsDir, "perfuse-manual.*.html")); err == nil {
		for _, f := range files {
			lang := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "perfuse-manual."), ".html")
			b, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			if err := writeFile(filepath.Join(out, "manual", lang, "index.html"), []byte(rewriteHost(string(b), base))); err != nil {
				return err
			}
			urls = append(urls, sitemapEntry{Loc: base + "/manual/" + lang + "/", Priority: "0.5"})
		}
	}

	pdf, err := os.ReadFile(filepath.Join(root, "docs/manual/perfuse-manual.pdf"))
	if err != nil {
		return fmt.Errorf("reading the manual PDF: %w", err)
	}
	if err := writeFile(filepath.Join(out, "manual/perfuse-manual.pdf"), pdf); err != nil {
		return err
	}

	// Images, shared with the README so the page and the repository cannot show different pictures.
	//
	// Walked recursively. The first version of this read the directory and skipped anything that
	// was a directory, which silently dropped 26 of the 30 asset files: every download button,
	// every capability chip and every screenshot lives in a subdirectory. The build reported
	// success, the pages referenced the files by the right paths, and the files were not there.
	if err := filepath.WalkDir(filepath.Join(root, "docs/assets"), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// The screenshot manifest is a record for the repository, not a file the website serves.
		// It was being published because this walk copies whatever it finds, and it was caught by
		// the deploy script refusing to upload a file it had no Content-Type for.
		if d.Name() == "sources.sha256" {
			return nil
		}
		rel, err := filepath.Rel(filepath.Join(root, "docs/assets"), p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		return writeFile(filepath.Join(out, "assets", rel), b)
	}); err != nil {
		return fmt.Errorf("copying assets: %w", err)
	}

	// Search-engine verification files, which have to stay at the root to keep working.
	for _, v := range []string{"google32f8480bb1e4af80.html", "BingSiteAuth.xml"} {
		b, err := os.ReadFile(filepath.Join(root, "site", v))
		if err != nil {
			continue
		}
		if err := writeFile(filepath.Join(out, v), b); err != nil {
			return err
		}
	}

	if err := writeFile(filepath.Join(out, "robots.txt"), []byte(robots(base))); err != nil {
		return err
	}

	// The IndexNow key.
	//
	// IndexNow lets a site tell a search engine that a URL has changed, rather than waiting to be
	// crawled. Bing, Yandex, Seznam and Naver accept it; Google declined to adopt it, so Google
	// still has to be told through Search Console.
	//
	// Ownership is proved by serving the key as a file at the root. It is written by the build on
	// purpose: the deploy uses --delete, so a key uploaded by hand would survive exactly until the
	// next deploy and then vanish, and the submissions would start failing with no obvious cause.
	if err := writeFile(filepath.Join(out, indexNowKey+".txt"), []byte(indexNowKey+"\n")); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(out, "sitemap.xml"), []byte(sitemap(urls))); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(out, "404.html"), []byte(notFound(base))); err != nil {
		return err
	}

	// Every local reference the built pages make must resolve to a file that is actually here.
	//
	// This is not belt and braces, it is the guard that was missing. The GitHub Pages workflow
	// used to run this check, it was dropped when that workflow became redirect stubs, and the
	// very next build shipped a front page whose three download buttons were missing images. A
	// broken image is the worst kind of defect to leave to chance, because the page still returns
	// 200 and still looks like a page: on a desktop the alt text stands in and reads almost like a
	// button, so the fault is invisible to whoever built it and obvious to a visitor on a phone.
	missing, err := missingLocalRefs(out)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		for _, m := range missing {
			fmt.Fprintf(os.Stderr, "  %s references %s, which was not built\n", m.page, m.ref)
		}

		return fmt.Errorf("%d local reference(s) point at files that do not exist", len(missing))
	}

	fmt.Printf("built %d pages into %s\n", len(urls), out)

	return nil
}

// brokenRef is a reference from a built page to a file that was not built.
type brokenRef struct {
	page, ref string
}

// missingLocalRefs reports every src or href in the built site that names a local file which is
// not present. Absolute URLs and fragments are left alone; only paths this build is responsible
// for are checked, because those are the ones it can get wrong.
func missingLocalRefs(out string) ([]brokenRef, error) {
	ref := regexp.MustCompile(`(?:src|href)="([^"]+)"`)

	var broken []brokenRef
	err := filepath.WalkDir(out, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".html") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		pageDir := filepath.Dir(p)
		for _, m := range ref.FindAllStringSubmatch(string(b), -1) {
			target := m[1]
			switch {
			case target == "",
				strings.HasPrefix(target, "http://"),
				strings.HasPrefix(target, "https://"),
				strings.HasPrefix(target, "//"),
				strings.HasPrefix(target, "#"),
				strings.HasPrefix(target, "mailto:"),
				strings.HasPrefix(target, "data:"):
				continue
			}

			// Strip any fragment or query before looking for the file.
			clean := target
			if i := strings.IndexAny(clean, "#?"); i >= 0 {
				clean = clean[:i]
			}
			if clean == "" {
				continue
			}

			var candidate string
			if strings.HasPrefix(clean, "/") {
				candidate = filepath.Join(out, clean)
			} else {
				candidate = filepath.Join(pageDir, clean)
			}
			// A directory URL means the index document inside it.
			if strings.HasSuffix(clean, "/") {
				candidate = filepath.Join(candidate, "index.html")
			}

			if _, statErr := os.Stat(candidate); statErr != nil {
				if _, dirErr := os.Stat(filepath.Join(candidate, "index.html")); dirErr == nil {
					continue
				}
				rel, _ := filepath.Rel(out, p)
				broken = append(broken, brokenRef{page: rel, ref: target})
			}
		}

		return nil
	})

	return broken, err
}

// rewriteHost points the old GitHub Pages address at the site's own domain.
//
// The old URLs keep working and redirect, but they must not appear in a canonical tag, an og:url or
// a sitemap, because two addresses claiming to be the same page is how ranking signals get split.
func rewriteHost(s, base string) string {
	s = strings.ReplaceAll(s, "https://biodream-llc.github.io/perfuse", base)

	return strings.ReplaceAll(s, "https://BioDream-LLC.github.io/perfuse", base)
}

// stripShieldsBadges removes the shields.io badge row from a document being published on the site.
//
// Badges are a GitHub convention and they stay on GitHub. On the website they are a poor trade.
// Each one is an image fetched from a third party on every page load, which means img.shields.io
// learns the address and referring page of every visitor - including which documents a hospital's
// staff read before making contact. For a project whose pitch includes that a peer report carries
// no identifying data, importing an image beacon onto every page of the site is the wrong shape.
//
// They would also simply not appear. The content security policy on this site allows images from
// its own origin only, so the badges would render as broken images rather than badges, which is
// worse than absent.
//
// Nothing is lost that the page does not already say in its own words: the licence, the version,
// the supported platforms and the test status are all stated in the prose and the tables.
//
// Only a line that is entirely badges is removed. A badge in the middle of a sentence would be
// content, and this must not quietly delete content.
func stripShieldsBadges(s string) string {
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && strings.Contains(trimmed, "shields.io") && isOnlyBadges(trimmed) {
			continue
		}
		kept = append(kept, line)
	}

	return strings.Join(kept, "\n")
}

// isOnlyBadges reports whether a line consists of nothing but badge links and whitespace.
func isOnlyBadges(line string) bool {
	rest := line
	for {
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return true
		}
		if !strings.HasPrefix(rest, "[![") && !strings.HasPrefix(rest, "![") {
			return false
		}
		// Consume up to the end of this construct: the last closing paren of the outer link.
		end := strings.Index(rest, ")")
		if end < 0 {
			return false
		}
		// A [![alt](src)](href) has a second closing paren to consume.
		if strings.HasPrefix(rest, "[![") {
			next := strings.Index(rest[end+1:], ")")
			if next < 0 {
				return false
			}
			end = end + 1 + next
		}
		rest = rest[end+1:]
	}
}

// rewriteLinks turns repository-relative markdown links into site URLs.
//
// Without this, every link the README makes to another document would point at a path that exists
// in a git checkout and nowhere on the web.
func rewriteLinks(s string) string {
	// Most specific first: the manual's two forms, then assets, then any other doc.
	s = strings.ReplaceAll(s, "](docs/manual/perfuse-manual.pdf)", "](/manual/perfuse-manual.pdf)")
	s = strings.ReplaceAll(s, "](docs/manual/perfuse-manual.html)", "](/manual/)")
	s = strings.ReplaceAll(s, "](docs/assets/", "](/assets/")
	s = strings.ReplaceAll(s, "\"docs/assets/", "\"/assets/")
	s = strings.ReplaceAll(s, "src=\"docs/assets/", "src=\"/assets/")

	// Files that exist in the repository and have no web equivalent. The licence and the notice
	// are the two the README and the reference link to, and both are things a reader may genuinely
	// want, so they point at the canonical copy on GitHub rather than being stripped.
	//
	// Both the bare and the parent-relative form, because the same file is linked from the root
	// (README.md, so "LICENSE") and from a subdirectory (docs/reference.md, so "../LICENSE"). That
	// second form arrived when the link was corrected for GitHub, where a bare "LICENSE" inside
	// docs/ resolves to docs/LICENSE and 404s. One link, two readers, and a fix for one of them
	// broke the other - which is why the build refuses to finish with a reference it did not write.
	const blob = "https://github.com/BioDream-LLC/perfuse/blob/main/"
	for _, f := range []string{"LICENSE", "NOTICE"} {
		for _, form := range []string{f, "../" + f, "./" + f} {
			s = strings.ReplaceAll(s, "]("+form+")", "]("+blob+f+")")
		}
	}

	for _, p := range pages {
		if p.src == "README.md" {
			continue
		}
		s = strings.ReplaceAll(s, "]("+p.src+")", "](/"+p.dir+"/)")
		// A link with a fragment, e.g. docs/reference.md#migrating-from-mirth
		s = strings.ReplaceAll(s, "]("+p.src+"#", "](/"+p.dir+"/#")
	}

	return s
}

type sitemapEntry struct {
	Loc      string
	Priority string
}

func sitemap(urls []sitemapEntry) string {
	today := time.Now().UTC().Format("2006-01-02")

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, u := range urls {
		fmt.Fprintf(&b, "  <url>\n    <loc>%s</loc>\n    <lastmod>%s</lastmod>\n    <priority>%s</priority>\n  </url>\n",
			html.EscapeString(u.Loc), today, u.Priority)
	}
	b.WriteString("</urlset>\n")

	return b.String()
}

func robots(base string) string {
	return "User-agent: *\nAllow: /\n\nSitemap: " + base + "/sitemap.xml\n"
}

func notFound(base string) string {
	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Not found — Perfuse</title>
<meta name="robots" content="noindex">
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
         max-width: 40rem; margin: 6rem auto; padding: 0 1.5rem; line-height: 1.6; color: #1a1a1a; }
  a { color: #0b6bcb; }
  ul { padding-left: 1.2rem; }
</style>
</head>
<body>
<h1>That page is not here</h1>
<p>The address may have changed, or it may never have existed. These are the ones that do:</p>
<ul>
  <li><a href="/">The front page</a></li>
  <li><a href="/overview/">Overview</a> — what Perfuse is and what it connects to</li>
  <li><a href="/manual/">The manual</a> — the whole thing, one page</li>
  <li><a href="/reference/">Reference</a> — every command and configuration key</li>
  <li><a href="/verification/">Verification</a> — what was tested and what was not</li>
  <li><a href="https://github.com/BioDream-LLC/perfuse">The source on GitHub</a></li>
</ul>
</body>
</html>
`
}

func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	return os.WriteFile(path, b, 0o644)
}

// sectionOf returns the "## name" section of a markdown text, without its heading, with its "### " subsections raised to
// "## " so they are the page's sections. A section that is not there is an empty page, which the site test refuses.
func sectionOf(md, name string) string {
	start := strings.Index(md, "\n## "+name+"\n")
	if start < 0 {
		return ""
	}
	rest := md[start+len("\n## "+name+"\n"):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	rest = strings.ReplaceAll(rest, "\n### ", "\n## ")
	return strings.ReplaceAll(rest, "the repository's `conformance/national` folder",
		"[conformance/national](https://github.com/biodream-llc/perfuse/tree/main/conformance/national)")
}
