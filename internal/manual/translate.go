package manual

import (
	"strings"
)

// Translations of the manual.
//
// The manual is written in English and stays so: the HTML Perfuse serves at /manual is English. A translation is made unit by
// unit (each title, and each paragraph, list or table of the prose), and kept as a map from the English unit to its translation
// (docs/manual/i18n/<lang>.json, filled by web/scripts/i18n-manual.mjs). Code blocks are never units: they are the same in every
// language. A unit with no translation stays English, so a translated manual is never missing text, only less translated.

// TranslationNotice is said, in its language, under the title of every translation.
const TranslationNotice = "This manual was translated from English by machine and has not yet been reviewed by a native speaker. " +
	"The English original is the reference: https://perfuse.health/manual/"

// Units lists the manual's translatable units, in order, each once. A paragraph that is almost all code (a table of
// configuration keys, say) is not a unit: it reads the same in every language.
func (d *Doc) Units() []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	addBody := func(body string) {
		for _, b := range blocks(body) {
			if !b.code && wordy(b.text) {
				add(b.text)
			}
		}
	}
	add(d.Title)
	add(d.Subtitle)
	add(d.Description)
	add(TranslationNotice)
	add("Table of Contents")
	add("Other languages")
	for _, c := range d.Chapters {
		add(c.Title)
		addBody(c.Intro)
		for _, s := range c.Sections {
			add(s.Title)
			addBody(s.Body)
			for _, ss := range s.Subsections {
				add(ss.Title)
				addBody(ss.Body)
			}
		}
	}
	return out
}

// Translated is a copy of the manual with every unit the map translates replaced. It answers how many units had no
// translation.
func (d *Doc) Translated(lang string, tr map[string]string) (*Doc, int) {
	missing := 0
	one := func(s string) string {
		key := strings.TrimSpace(s)
		if key == "" {
			return s
		}
		if t, ok := tr[key]; ok && strings.TrimSpace(t) != "" {
			return t
		}
		missing++
		return s
	}
	body := func(b string) string {
		parts := blocks(b)
		var out []string
		for _, p := range parts {
			if p.code || !wordy(p.text) {
				out = append(out, p.text)
			} else {
				out = append(out, one(p.text))
			}
		}
		return strings.Join(out, "\n\n")
	}
	c := *d
	c.Lang = lang
	c.Title, c.Subtitle, c.Description = one(d.Title), one(d.Subtitle), one(d.Description)
	c.Notice, c.Contents, c.OtherLanguages = one(TranslationNotice), one("Table of Contents"), one("Other languages")
	c.Chapters = make([]Chapter, len(d.Chapters))
	for i, ch := range d.Chapters {
		nc := Chapter{Title: one(ch.Title), Key: orKey(ch.Key, ch.Title), Intro: body(ch.Intro)}
		for _, s := range ch.Sections {
			ns := Section{Title: one(s.Title), Key: orKey(s.Key, s.Title), Body: body(s.Body)}
			for _, ss := range s.Subsections {
				ns.Subsections = append(ns.Subsections, Subsection{Title: one(ss.Title), Key: orKey(ss.Key, ss.Title), Body: body(ss.Body)})
			}
			nc.Sections = append(nc.Sections, ns)
		}
		c.Chapters[i] = nc
	}
	return &c, missing
}

type block struct {
	text string
	code bool
}

// blocks splits a body at blank lines into paragraphs, lists and tables, keeping each fenced code block whole and marked as
// code. A list or a table is one unit, so a translation keeps its shape.
func blocks(body string) []block {
	var out []block
	var cur []string
	inFence := false
	flush := func(code bool) {
		if len(cur) == 0 {
			return
		}
		out = append(out, block{strings.Join(cur, "\n"), code})
		cur = nil
	}
	for _, line := range strings.Split(body, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") {
			if !inFence {
				flush(false)
				inFence = true
				cur = append(cur, line)
				continue
			}
			cur = append(cur, line)
			flush(true)
			inFence = false
			continue
		}
		if inFence {
			cur = append(cur, line)
			continue
		}
		if trim == "" {
			flush(false)
			continue
		}
		cur = append(cur, line)
	}
	flush(inFence)
	return out
}

func orKey(k, t string) string {
	if k != "" {
		return k
	}
	return t
}

// wordy says a block has prose in it: at least three words outside backquotes and markup.
func wordy(s string) bool {
	var b strings.Builder
	inCode := false
	for _, r := range s {
		if r == '`' {
			inCode = !inCode
			continue
		}
		if !inCode {
			b.WriteRune(r)
		}
	}
	n := 0
	for _, w := range strings.Fields(b.String()) {
		letters := 0
		for _, r := range w {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				letters++
			}
		}
		if letters >= 2 {
			n++
		}
	}
	return n >= 3
}
