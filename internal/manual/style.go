package manual

// manualCSS styles the manual for reading on screen and for printing.
//
// # Why the styling is in here rather than a file
//
// The HTML is one self-contained file that can be mailed to somebody or opened from a memory stick with no server and
// no network. A linked stylesheet would break both, and it would break them quietly: the page still renders, just as
// unstyled text, which looks like a broken document rather than a missing file.
//
// # Why print rules matter as much as screen rules
//
// The PDF is produced by printing this HTML, so anything that looks wrong on paper is wrong in the PDF. The print block
// at the end is what stops a code example being split across a page break in the middle of a configuration file, and
// what makes headings avoid being left stranded at the foot of a page.
const manualCSS = `
/* Tokens, so the manual reads as part of the product rather than as a document about it.
   The values are the application's own dark palette. */
:root {
  --bg: #172433;
  --panel: #223348;
  --panel2: #293c54;
  --nav: #102033;
  --border: #3f5877;
  --text: #eaf6ff;
  --muted: #a9c5dc;
  --accent: #69b7ff;
  --accent2: #91d3ff;
  --note-edge: #f4c84a;
  --warn-edge: #ff7474;
}

/* Dark unconditionally, not adapted to the reader's system preference.
   An earlier version offered a light palette under prefers-color-scheme, and the effect was that most machines - which
   report a light preference by default - never saw the manual the product actually looks like. Paper is the one place a
   light treatment is genuinely required, and the print block at the end handles that. A reader who wants light on screen
   has the browser's own reader mode, which is a better light rendering than a hand-cut palette would be. */

* { box-sizing: border-box; }
html { scroll-behavior: smooth; }

body {
  margin: 0;
  background: var(--bg);
  color: var(--text);
  font: 15px/1.65 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  /* Hyphenation off. A reference manual is full of identifiers, and a hyphen inserted into a configuration key is
     indistinguishable from one that belongs there. */
  hyphens: none;
  display: grid;
  grid-template-columns: 320px 1fr;
}

/* Grid tracks size to their widest content by default, so one wide reference table would stretch the whole layout and
   push the prose off screen. Letting both tracks shrink hands the overflow to the element that can scroll it. */
.toc, .doc { min-width: 0; }

/* ── contents rail ──────────────────────────────────────────────────────── */
.toc {
  background: var(--nav);
  border-right: 1px solid var(--border);
  height: 100vh;
  position: sticky;
  top: 0;
  overflow-y: auto;
  padding: 16px 0 48px;
}
.toc h2 {
  font-size: 9px;
  letter-spacing: 0.12em;
  text-transform: uppercase;
  color: var(--accent);
  font-weight: 700;
  margin: 14px 0 6px;
  padding: 0 22px;
}
.toc a {
  display: block;
  padding: 5px 10px;
  margin: 0 12px;
  color: var(--text);
  text-decoration: none;
  border-radius: 8px;
  font-size: 13px;
  border: 1px solid transparent;
}
.toc a:hover { background: var(--panel2); border-color: var(--border); }
.toc .num {
  display: inline-block;
  min-width: 2.8rem;
  color: var(--muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.85em;
}
.toc-d1 > a { font-weight: 600; }
.toc-d2 > a { padding-left: 22px; font-size: 12.5px; }
.toc-d3 > a { padding-left: 38px; font-size: 12px; color: var(--muted); }

/* Title block, sitting at the head of the rail as the product's card. */
.titlepage {
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 14px;
  margin: 0 12px 14px;
  padding: 14px 16px;
}
.titlepage h1 {
  font-size: 20px;
  line-height: 1.2;
  margin: 0 0 3px;
  color: var(--accent2);
  letter-spacing: -0.01em;
}
.subtitle { font-size: 12.5px; color: var(--muted); margin: 0 0 6px; }
.version {
  font-size: 11px;
  color: var(--muted);
  margin: 0;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}

/* ── document body ──────────────────────────────────────────────────────── */
.doc {
  max-width: 1000px;
  padding: 0 34px 96px;
}

.chapter { padding-top: 34px; }

/* Headings. The number is monospaced and set apart so a reader scanning for 4.7 finds it without reading the titles. */
h2, h3, h4 { line-height: 1.25; scroll-margin-top: 24px; }
h2 .num, h3 .num, h4 .num {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  color: var(--accent);
  margin-right: 0.5rem;
  font-weight: 500;
}
.chapter > h2 {
  font-size: 25px;
  margin: 26px 0 12px;
  color: var(--accent2);
  border-bottom: 1px solid var(--border);
  padding-bottom: 8px;
}
h3 { font-size: 18px; margin: 30px 0 8px; color: var(--accent); }
h4 { font-size: 15px; margin: 22px 0 6px; color: var(--text); }

p { margin: 0 0 0.9rem; }
ul, ol { margin: 0 0 1rem; padding-left: 1.5rem; }
li { margin-bottom: 0.35rem; }
a { color: var(--accent2); }

a:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}

/* Code. Wrapped rather than scrolled, because a horizontal scrollbar does not exist on paper and the PDF would simply
   lose whatever ran past the right edge. */
code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.88em;
  background: var(--panel2);
  border: 1px solid var(--border);
  border-radius: 5px;
  padding: 1px 5px;
}
/* Diagrams.
   
   Centred, bounded to the text column, and scaling with it. An SVG with a viewBox and no fixed width fills
   whatever it is given, which is what lets one drawing work in a browser at any window size and on a
   printed page at 96mm without a second copy being kept. */
figure.diagram {
  margin: 1.75rem 0;
  text-align: center;
}
figure.diagram svg {
  max-width: 100%;
  height: auto;
}
figure.diagram figcaption {
  margin-top: 0.6rem;
  font-size: 0.82rem;
  color: var(--muted);
  text-align: left;
}

pre.code {
  background: var(--panel2);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 12px 14px;
  margin: 0 0 1.2rem;
  overflow-x: auto;
  white-space: pre-wrap;
  word-break: break-word;
}
pre.code code {
  background: none;
  border: 0;
  padding: 0;
  font-size: 0.82rem;
  line-height: 1.55;
}

/* Tables. The reference chapters are mostly tables, so these carry a lot of the document. */
table {
  width: 100%;
  border-collapse: collapse;
  margin: 0 0 1.4rem;
  font-size: 14px;
  border: 1px solid var(--border);
  border-radius: 10px;
  overflow: hidden;
}
th {
  background: var(--panel2);
  text-align: left;
  padding: 9px 11px;
  font-size: 11px;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--accent);
  border-bottom: 1px solid var(--border);
}
td {
  padding: 9px 11px;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
  text-align: left;
}
tr:last-child td { border-bottom: 0; }
td code { font-size: 0.85em; }

/* Notes carry the things that bite. Coloured, because in a document this long an inline warning in body text is not
   seen at all. */
.note {
  background: var(--panel2);
  border-left: 3px solid var(--note-edge);
  padding: 10px 14px;
  margin: 0 0 1.2rem;
  border-radius: 0 10px 10px 0;
  font-size: 0.95rem;
}

/* ── narrow screens ─────────────────────────────────────────────────────── */
@media (max-width: 980px) {
  body { grid-template-columns: 1fr; }
  .toc {
    position: relative;
    height: auto;
    overflow-y: visible;
    border-right: 0;
    border-bottom: 1px solid var(--border);
  }
  .doc { padding-left: 18px; padding-right: 18px; }
  .chapter > h2 { font-size: 21px; }
}

@media (max-width: 620px) {
  /* Wide reference tables scroll inside their own frame rather than dragging the page sideways. */
  table { display: block; overflow-x: auto; }
  .chapter > h2 { font-size: 19px; }
  pre.code code { font-size: 0.78rem; }
}

/* ── print ──────────────────────────────────────────────────────────────── */
/* The PDF is this HTML printed, so these rules are the PDF's layout. Everything above is undone here: the rail becomes
   a contents section, the dark palette becomes ink on paper, and the two columns become one flow. */
@page {
  margin: 18mm 16mm 20mm;
}

@media print {
  body {
    display: block;
    background: #fff;
    color: #000;
    font-size: 10.5pt;
    line-height: 1.5;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Georgia, serif;
  }

  /* The rail returns to the flow as an ordinary title page and contents. */
  .toc {
    position: static;
    height: auto;
    overflow: visible;
    background: none;
    border: 0;
    padding: 0;
    page-break-after: always;
  }
  .toc h2 {
    font-size: 1.3rem;
    text-transform: none;
    letter-spacing: normal;
    color: #000;
    padding: 0;
    margin: 0 0 1rem;
  }
  .toc a {
    display: block;
    margin: 0;
    padding: 1px 0;
    border: 0;
    border-radius: 0;
    font-size: 10.5pt;
    color: #000;
  }
  .toc-d2 > a { padding-left: 1.6rem; }
  .toc-d3 > a { padding-left: 3.4rem; color: #000; }
  .toc .num { color: #444; }

  .titlepage {
    background: none;
    border: 0;
    border-bottom: 3px solid #000;
    border-radius: 0;
    margin: 0 0 2.5rem;
    padding: 0 0 1.5rem;
  }
  .titlepage h1 { font-size: 2.6rem; color: #000; }
  .subtitle { font-size: 1.15rem; color: #333; }
  .version { font-size: 0.9rem; color: #333; }

  .doc { max-width: none; padding: 0; }

  /* A chapter starts on a fresh page, as it does in a printed manual. */
  .chapter { page-break-before: always; padding-top: 0; }

  /* A heading stranded at the foot of a page with its text overleaf is the commonest fault in a printed technical
     document. */
  h2, h3, h4 { page-break-after: avoid; break-after: avoid; color: #000; }
  .chapter > h2 { font-size: 1.9rem; border-bottom: 1px solid #999; }
  h3 { font-size: 1.35rem; }
  h2 .num, h3 .num, h4 .num { color: #444; }

  /* A configuration example split across a page break is worse than one that starts lower down: a reader who copies
     the visible half gets a file that does not load. */
  pre.code, table, .note, figure.diagram { page-break-inside: avoid; break-inside: avoid; }

  /* Links print as their text. A URL in brackets after every cross-reference in a document with a thousand internal
     links would be unreadable. */
  a { color: #000; text-decoration: none; }

  .note { background: #fff; border-left: 2pt solid #666; border-radius: 0; }
  pre.code { background: #fafafa; border: 1px solid #bbb; border-radius: 0; }
  code { background: none; border: 0; padding: 0; }
  table { border-color: #999; border-radius: 0; }
  th {
    background: #f0f0f0;
    color: #000;
    text-transform: none;
    letter-spacing: normal;
    font-size: 9.5pt;
    font-weight: 600;
    border-bottom: 2px solid #999;
  }
  td { border-bottom: 1px solid #ccc; }
}
`
