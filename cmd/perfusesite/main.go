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
	"flag"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/manual"
)

// page is one markdown document published as its own URL.
type page struct {
	src         string // path to the markdown, relative to the module root
	dir         string // directory under the site root, "" for the root itself
	title       string
	description string
	priority    string // sitemap priority
}

// The documents published as pages. Anything referenced from the README has to be here, because a
// link that resolves on GitHub and 404s on the website is worse than no link.
var pages = []page{
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
		src:         "docs/queue.md",
		dir:         "queue",
		title:       "The durable queue",
		description: "How the on-disk queue works, what it guarantees, and what it costs.",
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
	flag.Parse()

	if err := run(*root, *out, strings.TrimSuffix(*base, "/")); err != nil {
		fmt.Fprintln(os.Stderr, "perfusesite:", err)
		os.Exit(1)
	}
}

func run(root, out, base string) error {
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	var urls []sitemapEntry

	// The hand-written landing pages, copied with their URLs pointed at the new home.
	landing := []struct {
		src, dst, priority string
	}{
		{"site/index.html", "index.html", "1.0"},
		{"site/mirth-connect-alternative/index.html", "mirth-connect-alternative/index.html", "0.9"},
	}
	for _, l := range landing {
		b, err := os.ReadFile(filepath.Join(root, l.src))
		if err != nil {
			return fmt.Errorf("reading %s: %w", l.src, err)
		}
		if err := writeFile(filepath.Join(out, l.dst), []byte(rewriteHost(string(b), base))); err != nil {
			return err
		}
		u := base + "/" + strings.TrimSuffix(l.dst, "index.html")
		urls = append(urls, sitemapEntry{Loc: u, Priority: l.priority})
	}

	// The markdown documents, rendered.
	for _, p := range pages {
		raw, err := os.ReadFile(filepath.Join(root, p.src))
		if err != nil {
			return fmt.Errorf("reading %s: %w", p.src, err)
		}

		canonical := base + "/" + p.dir + "/"
		body := rewriteLinks(rewriteHost(string(raw), base))
		out2 := filepath.Join(out, p.dir, "index.html")
		if err := writeFile(out2, []byte(manual.RenderStandalonePage(p.title, p.description, canonical, body))); err != nil {
			return err
		}
		urls = append(urls, sitemapEntry{Loc: canonical, Priority: p.priority})
	}

	// The manual, which is already self-contained HTML, plus the PDF beside it.
	//
	// The host rewrite matters here as much as anywhere: perfusedoc writes a canonical tag and an
	// og:url into the manual, and copying it verbatim published a page that named a different
	// address as its own canonical URL. That is an instruction to search engines to credit the old
	// location for the page they are looking at.
	man, err := os.ReadFile(filepath.Join(root, "docs/manual/perfuse-manual.html"))
	if err != nil {
		return fmt.Errorf("reading the manual: %w", err)
	}
	if err := writeFile(filepath.Join(out, "manual/index.html"), []byte(rewriteHost(string(man), base))); err != nil {
		return err
	}
	urls = append(urls, sitemapEntry{Loc: base + "/manual/", Priority: "0.9"})

	pdf, err := os.ReadFile(filepath.Join(root, "docs/manual/perfuse-manual.pdf"))
	if err != nil {
		return fmt.Errorf("reading the manual PDF: %w", err)
	}
	if err := writeFile(filepath.Join(out, "manual/perfuse-manual.pdf"), pdf); err != nil {
		return err
	}

	// Images, shared with the README so the page and the repository cannot show different pictures.
	assets, err := os.ReadDir(filepath.Join(root, "docs/assets"))
	if err == nil {
		for _, a := range assets {
			if a.IsDir() {
				continue
			}
			b, err := os.ReadFile(filepath.Join(root, "docs/assets", a.Name()))
			if err != nil {
				return err
			}
			if err := writeFile(filepath.Join(out, "assets", a.Name()), b); err != nil {
				return err
			}
		}
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
	if err := writeFile(filepath.Join(out, "sitemap.xml"), []byte(sitemap(urls))); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(out, "404.html"), []byte(notFound(base))); err != nil {
		return err
	}

	fmt.Printf("built %d pages into %s\n", len(urls), out)

	return nil
}

// rewriteHost points the old GitHub Pages address at the site's own domain.
//
// The old URLs keep working and redirect, but they must not appear in a canonical tag, an og:url or
// a sitemap, because two addresses claiming to be the same page is how ranking signals get split.
func rewriteHost(s, base string) string {
	s = strings.ReplaceAll(s, "https://biodream-llc.github.io/perfuse", base)

	return strings.ReplaceAll(s, "https://BioDream-LLC.github.io/perfuse", base)
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
