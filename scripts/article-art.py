#!/usr/bin/env python3
"""Draws the article illustrations for perfuse.health as standalone SVG files.

One visual language for every drawing: a dark slate canvas, the cyan-to-indigo flow gradient Perfuse's
architecture diagram uses, rounded cards, and labels at 15px or larger so they stay readable when scaled.
"""
import html, os, sys

OUT = sys.argv[1] if len(sys.argv) > 1 else "docs/assets/articles"
FONT = "Inter, system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif"
MONO = "'SF Mono', Menlo, Consolas, monospace"
CYAN, BLUE, INDIGO, TEAL, AMBER, ROSE = "#22d3ee", "#3b82f6", "#6366f1", "#2dd4bf", "#fbbf24", "#fb7185"
INK, MUTED, CARD, CARD2, LINE = "#f1f5f9", "#94a3b8", "#1e293b", "#273449", "#334155"


def esc(s):
    return html.escape(s, quote=True)


class SVG:
    def __init__(self, w, h, title, desc):
        self.w, self.h, self.parts = w, h, []
        self.title, self.desc = title, desc

    def add(self, s):
        self.parts.append(s)

    def text(self, x, y, s, size=15, fill=INK, weight=400, anchor="start", font=FONT, opacity=1):
        self.add(f'<text x="{x}" y="{y}" font-family="{font}" font-size="{size}" font-weight="{weight}" '
                 f'fill="{fill}" text-anchor="{anchor}" opacity="{opacity}">{esc(s)}</text>')

    def lines(self, x, y, items, size=15, fill=INK, gap=None, **kw):
        gap = gap or size * 1.45
        for i, s in enumerate(items):
            self.text(x, y + i * gap, s, size=size, fill=fill, **kw)

    def card(self, x, y, w, h, fill=CARD, stroke=LINE, r=14, sw=1.5, extra=""):
        self.add(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{r}" fill="{fill}" stroke="{stroke}" '
                 f'stroke-width="{sw}" {extra}/>')

    def pill(self, x, y, s, color=CYAN, size=14):
        w = len(s) * size * 0.62 + 24
        self.add(f'<rect x="{x}" y="{y}" width="{w:.0f}" height="{size + 14}" rx="{(size + 14) / 2}" '
                 f'fill="{color}" fill-opacity="0.16" stroke="{color}" stroke-opacity="0.6"/>')
        self.text(x + w / 2, y + size + 3, s, size=size, fill=color, weight=600, anchor="middle")
        return w

    def arrow(self, x1, y1, x2, y2, color="url(#flowLine)", sw=3, dash=""):
        d = f' stroke-dasharray="{dash}"' if dash else ""
        self.add(f'<line x1="{x1}" y1="{y1}" x2="{x2}" y2="{y2}" stroke="{color}" stroke-width="{sw}" '
                 f'stroke-linecap="round" marker-end="url(#tip)"{d}/>')

    def path(self, d, color=BLUE, sw=3, dash="", arrow=True):
        m = ' marker-end="url(#tip)"' if arrow else ""
        da = f' stroke-dasharray="{dash}"' if dash else ""
        self.add(f'<path d="{d}" fill="none" stroke="{color}" stroke-width="{sw}" stroke-linecap="round"{da}{m}/>')

    def circle(self, cx, cy, r, fill, stroke="none", sw=0, extra=""):
        self.add(f'<circle cx="{cx}" cy="{cy}" r="{r}" fill="{fill}" stroke="{stroke}" stroke-width="{sw}" {extra}/>')

    def write(self, name):
        head = f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {self.w} {self.h}" width="{self.w}" height="{self.h}" role="img" aria-label="{esc(self.desc)}">
  <title>{esc(self.title)}</title>
  <defs>
    <linearGradient id="bg" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0%" stop-color="#0f172a"/><stop offset="100%" stop-color="#020617"/>
    </linearGradient>
    <linearGradient id="flow" x1="0" y1="0" x2="1" y2="0">
      <stop offset="0%" stop-color="{CYAN}"/><stop offset="50%" stop-color="{BLUE}"/><stop offset="100%" stop-color="{INDIGO}"/>
    </linearGradient>
    <linearGradient id="flowLine" gradientUnits="userSpaceOnUse" x1="0" y1="0" x2="{self.w}" y2="0">
      <stop offset="0%" stop-color="{CYAN}"/><stop offset="50%" stop-color="{BLUE}"/><stop offset="100%" stop-color="{INDIGO}"/>
    </linearGradient>
    <linearGradient id="hero" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0%" stop-color="{CYAN}" stop-opacity="0.9"/><stop offset="100%" stop-color="{INDIGO}" stop-opacity="0.9"/>
    </linearGradient>
    <radialGradient id="halo" cx="0.5" cy="0.5" r="0.5">
      <stop offset="0%" stop-color="{BLUE}" stop-opacity="0.45"/><stop offset="100%" stop-color="{BLUE}" stop-opacity="0"/>
    </radialGradient>
    <filter id="glow" x="-50%" y="-50%" width="200%" height="200%">
      <feGaussianBlur stdDeviation="6" result="b"/><feMerge><feMergeNode in="b"/><feMergeNode in="SourceGraphic"/></feMerge>
    </filter>
    <filter id="lift" x="-20%" y="-20%" width="140%" height="160%">
      <feDropShadow dx="0" dy="6" stdDeviation="8" flood-color="#000" flood-opacity="0.35"/>
    </filter>
    <marker id="tip" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="{BLUE}"/>
    </marker>
    <pattern id="grid" width="32" height="32" patternUnits="userSpaceOnUse">
      <path d="M32 0H0V32" fill="none" stroke="#1e293b" stroke-width="1"/>
    </pattern>
  </defs>
  <rect width="{self.w}" height="{self.h}" rx="18" fill="url(#bg)"/>
  <rect width="{self.w}" height="{self.h}" rx="18" fill="url(#grid)" opacity="0.55"/>
'''
        os.makedirs(OUT, exist_ok=True)
        with open(os.path.join(OUT, name), "w") as f:
            f.write(head + "\n".join("  " + p for p in self.parts) + "\n</svg>\n")


def hero(name, kicker, title_lines, motif, desc):
    """A banner: kicker, title and a drawn motif on the right."""
    s = SVG(1200, 360, " ".join(title_lines), desc)
    s.circle(930, 180, 260, "url(#halo)")
    s.pill(64, 64, kicker, CYAN, 15)
    s.lines(64, 150, title_lines, size=44, weight=700, gap=56)
    s.add(f'<rect x="64" y="{150 + 56 * len(title_lines) - 20}" width="120" height="6" rx="3" fill="url(#flow)"/>')
    s.text(64, 320, "perfuse.health", size=16, fill=MUTED, weight=500)
    motif(s)
    s.write(name)


# ---- motifs -------------------------------------------------------------------------------------------------------

def motif_fork(s):
    s.circle(760, 180, 16, CYAN, extra='filter="url(#glow)"')
    ys = [70, 140, 220, 290]
    labels = ["Buy a licence", "Stay on 4.5.2", "Move to a fork", "Move forward"]
    for i, y in enumerate(ys):
        hot = i == 3
        s.path(f"M776 180 C 840 180, 840 {y}, 900 {y}", color=CYAN if hot else LINE, sw=4 if hot else 2.5)
        s.card(910, y - 22, 230, 44, fill="url(#hero)" if hot else CARD, stroke="none" if hot else LINE, r=22)
        s.text(1025, y + 6, labels[i], size=16, weight=700 if hot else 500, anchor="middle", fill="#fff" if hot else MUTED)


def motif_steps(s):
    for i in range(6):
        x = 700 + i * 80
        y = 250 - i * 30
        s.card(x, y, 64, 64, fill="url(#hero)" if i == 5 else CARD, stroke=LINE, r=16)
        s.text(x + 32, y + 41, str(i + 1), size=24, weight=700, anchor="middle", fill="#fff" if i == 5 else CYAN)
        if i < 5:
            s.arrow(x + 66, y + 22, x + 78, y - 4, color=BLUE, sw=2.5)


def motif_shield(s):
    s.add('<path d="M930 60 L1050 104 V190 C1050 260 995 300 930 322 C865 300 810 260 810 190 V104 Z" '
          'fill="url(#hero)" opacity="0.95" filter="url(#lift)"/>')
    s.add('<path d="M880 192 L918 230 L990 150" fill="none" stroke="#fff" stroke-width="14" stroke-linecap="round" '
          'stroke-linejoin="round"/>')
    for i, t in enumerate(["2026", "2027"]):
        s.pill(700 + i * 380, 300 - i * 230, t, AMBER if i == 0 else TEAL, 16)


def motif_chain(s):
    names = [("CRD", CYAN), ("DTR", BLUE), ("PAS", INDIGO)]
    for i, (n, c) in enumerate(names):
        cx = 760 + i * 170
        s.circle(cx, 180, 62, CARD, stroke=c, sw=4, extra='filter="url(#lift)"')
        s.text(cx, 190, n, size=28, weight=800, anchor="middle", fill=c)
        if i < 2:
            s.arrow(cx + 66, 180, cx + 100, 180, color=c, sw=4)
    s.text(930, 290, "order  →  documentation  →  decision", size=17, fill=MUTED, anchor="middle")


def motif_v2fhir(s):
    s.card(700, 70, 210, 220, fill=CARD, stroke=LINE)
    for i, seg in enumerate(["MSH|^~\\&|LAB", "PID|1||123^^^H", "PV1|1|I|W3", "OBR|1||ACC9", "OBX|1|NM|2345"]):
        s.text(718, 108 + i * 38, seg, size=15, fill=AMBER, font=MONO)
    s.arrow(920, 180, 978, 180, color=BLUE, sw=4)
    s.add('<path d="M1050 70 l26 15 v30 l-26 15 l-26 -15 v-30 z" fill="url(#hero)"/>')
    for i, r in enumerate(["Patient", "Encounter", "DiagnosticReport", "Observation"]):
        s.card(990, 150 + i * 36, 180, 30, fill=CARD2, stroke=LINE, r=8)
        s.text(1080, 171 + i * 36, r, size=15, fill=INK, anchor="middle", weight=600)


# ---- diagrams -----------------------------------------------------------------------------------------------------

def memory_chart():
    s = SVG(1200, 420, "Memory at rest: Perfuse 31 MB, Mirth Connect 383 MB",
            "Bar chart: Perfuse uses 31 MB of memory at rest against Mirth Connect 4.5.2's 383 MB, measured on the "
            "same machine with both idle. Perfuse is one file; Mirth is a 254 MB application plus a Java runtime.")
    s.text(64, 70, "Memory at rest, same machine, both idle", size=24, weight=700)
    s.text(64, 102, "Lower is better", size=16, fill=MUTED)
    full = 900
    rows = [("Perfuse", 31, "url(#flow)", "one file, no Java"), ("Mirth Connect 4.5.2", 383, "#475569", "254 MB app + JVM")]
    for i, (n, v, c, note) in enumerate(rows):
        y = 150 + i * 120
        s.text(64, y + 8, n, size=18, weight=700)
        s.text(64, y + 34, note, size=15, fill=MUTED)
        w = max(16, full * v / 383 * 0.8)
        s.card(290, y - 14, w, 40, fill=c, stroke="none", r=8)
        s.text(290 + w + 16, y + 15, f"{v} MB", size=26, weight=800, fill=CYAN if i == 0 else INK)
    s.pill(64, 365, "about 12× less memory", TEAL, 16)
    s.write("memory-at-rest.svg")


def migration_pipeline():
    s = SVG(1200, 470, "Mirth migration in six steps",
            "Six steps from left to right: export from Mirth, perfuse explain, perfuse translate, perfuse check and "
            "test, perfuse compare on real traffic, then switch over one channel at a time. An arrow back shows "
            "channels can be exported to Mirth again.")
    s.text(64, 64, "From Mirth to Perfuse in six steps", size=24, weight=700)
    steps = [("Export", "channels, groups,", "or a backup"), ("Explain", "what converts,", "by name"),
             ("Translate", "into Perfuse", "channel files"), ("Check & test", "validate and", "run your tests"),
             ("Compare", "both engines,", "real traffic"), ("Switch", "one channel", "at a time")]
    cmds = ["Mirth / OIE", "perfuse explain", "perfuse translate", "perfuse test", "perfuse compare", "perfuse serve"]
    w, gap, x0, y = 160, 30, 64, 130
    for i, (t, a, b) in enumerate(steps):
        x = x0 + i * (w + gap)
        last = i == 5
        s.card(x, y, w, 200, fill="url(#hero)" if last else CARD, stroke="none" if last else LINE, extra='filter="url(#lift)"')
        s.circle(x + 32, y + 36, 18, "#0f172a" if last else CARD2, stroke=CYAN, sw=2)
        s.text(x + 32, y + 42, str(i + 1), size=16, weight=800, anchor="middle", fill=CYAN)
        s.text(x + 18, y + 92, t, size=19, weight=700, fill="#fff")
        s.lines(x + 18, y + 122, [a, b], size=15, fill="#e0f2fe" if last else MUTED)
        s.text(x + 18, y + 182, cmds[i], size=13.5, fill=AMBER if not last else "#fff", font=MONO)
        if i < 5:
            s.arrow(x + w + 4, y + 100, x + w + gap - 4, y + 100, color=BLUE, sw=3)
    s.path(f"M{x0 + 5 * (w + gap) + w / 2} {y + 206} C {x0 + 5 * (w + gap) + w / 2} 420, {x0 + 80} 420, {x0 + 80} {y + 210}",
           color=TEAL, sw=2.5, dash="7 7")
    s.text(600, 440, "and back: Perfuse writes Mirth channel files that Mirth, OIE and BridgeLink accept",
           size=15, fill=TEAL, anchor="middle")
    s.write("mirth-migration-steps.svg")


def cms_timeline():
    s = SVG(1200, 460, "CMS-0057-F timeline",
            "Timeline. From 1 January 2026: decisions within 72 hours for expedited and 7 days for standard requests, "
            "a specific reason for every denial, and public prior authorization metrics. From 1 January 2027: four "
            "FHIR APIs, Patient Access, Provider Access, Payer-to-Payer and Prior Authorization.")
    s.text(64, 64, "What CMS-0057-F requires, and when", size=24, weight=700)
    s.add('<line x1="100" y1="140" x2="1100" y2="140" stroke="url(#flowLine)" stroke-width="6" stroke-linecap="round"/>')
    for x, d, c in [(250, "1 January 2026", AMBER), (850, "1 January 2027", TEAL)]:
        s.circle(x, 140, 16, c, extra='filter="url(#glow)"')
        s.text(x, 110, d, size=18, weight=700, anchor="middle", fill=c)
    s.card(80, 180, 480, 250, extra='filter="url(#lift)"')
    s.text(108, 220, "Faster, clearer decisions", size=19, weight=700, fill=AMBER)
    for i, (big, small) in enumerate([("72 hours", "expedited requests"), ("7 days", "standard requests"),
                                      ("A reason", "for every denial"), ("Metrics", "published every year")]):
        s.text(108, 264 + i * 42, big, size=19, weight=800)
        s.text(240, 264 + i * 42, small, size=16, fill=MUTED)
    s.card(640, 180, 480, 250, extra='filter="url(#lift)"')
    s.text(668, 220, "Four FHIR APIs", size=19, weight=700, fill=TEAL)
    for i, a in enumerate(["Patient Access", "Provider Access", "Payer-to-Payer", "Prior Authorization"]):
        s.card(668, 240 + i * 44, 424, 36, fill=CARD2, stroke=LINE, r=10)
        s.circle(690, 258 + i * 44, 6, TEAL)
        s.text(708, 264 + i * 44, a, size=16, weight=600)
    s.write("cms-0057-timeline.svg")


def cms_apis():
    s = SVG(1200, 520, "The four CMS-0057 APIs around a payer",
            "A payer in the centre, served by Perfuse, connected to four parties: members through the Patient Access "
            "API, providers through the Provider Access API, other payers through the Payer-to-Payer API, and "
            "clinicians ordering care through the Prior Authorization API, with the standards each uses.")
    s.circle(600, 270, 190, "url(#halo)")
    s.card(470, 205, 260, 130, fill="url(#hero)", stroke="none", r=22, extra='filter="url(#lift)"')
    s.text(600, 258, "Payer", size=26, weight=800, anchor="middle", fill="#fff")
    s.text(600, 290, "served by Perfuse", size=16, anchor="middle", fill="#e0f2fe")
    nodes = [(64, 60, "Members", "Patient Access", "CARIN BB · PDex · SMART", CYAN),
             (836, 60, "Providers", "Provider Access", "Bulk Data · opt-outs", BLUE),
             (64, 380, "Other payers", "Payer-to-Payer", "HRex $member-match", INDIGO),
             (836, 380, "Clinicians ordering", "Prior Authorization", "CRD · DTR · PAS", TEAL)]
    for x, y, who, api, std, c in nodes:
        s.card(x, y, 300, 100, extra='filter="url(#lift)"', stroke=c)
        s.text(x + 24, y + 36, who, size=19, weight=700)
        s.text(x + 24, y + 62, api + " API", size=16, fill=c, weight=600)
        s.text(x + 24, y + 86, std, size=14.5, fill=MUTED)
        ex = x + 300 if x < 600 else x
        ey = y + 50
        tx = 470 if x < 600 else 730
        ty = 240 if y < 270 else 300
        s.add(f'<line x1="{ex}" y1="{ey}" x2="{tx}" y2="{ty}" stroke="{c}" stroke-width="3" stroke-dasharray="2 8" stroke-linecap="round"/>')
    s.write("cms-0057-apis.svg")


def davinci_flow():
    s = SVG(1200, 620, "Da Vinci prior authorization: CRD, DTR and PAS",
            "Sequence between the EHR and the payer. 1, CRD: the clinician signs an order and the payer answers "
            "whether prior authorization is needed and which questionnaire to use. 2, DTR: the questionnaire "
            "package returns and is pre-filled from the chart. 3, PAS: the request is submitted and a decision "
            "comes back, approved, denied or pended.")
    s.text(64, 64, "From order to decision, inside the clinician's workflow", size=24, weight=700)
    for x, n, sub in [(120, "EHR", "clinician"), (900, "Payer", "Perfuse")]:
        s.card(x, 96, 180, 70, fill="url(#hero)" if n == "Payer" else CARD, stroke=LINE, r=16)
        s.text(x + 90, 130, n, size=21, weight=800, anchor="middle", fill="#fff")
        s.text(x + 90, 154, sub, size=14.5, anchor="middle", fill="#e0f2fe" if n == "Payer" else MUTED)
        s.add(f'<line x1="{x + 90}" y1="170" x2="{x + 90}" y2="590" stroke="{LINE}" stroke-width="2" stroke-dasharray="4 6"/>')
    rows = [("CRD", CYAN, "order-sign over CDS Hooks", "covered · auth needed · questionnaire"),
            ("DTR", BLUE, "$questionnaire-package", "Questionnaire + CQL, pre-filled"),
            ("PAS", INDIGO, "Claim/$submit with documentation", "approved · denied · pended")]
    for i, (n, c, req, resp) in enumerate(rows):
        y = 262 + i * 130
        s.pill(574, y - 70, n, c, 16)
        s.arrow(214, y, 984, y, color=c, sw=3)
        s.text(600, y - 10, req, size=15.5, anchor="middle", fill=INK)
        s.arrow(986, y + 40, 216, y + 40, color=c, sw=2.5, dash="8 6")
        s.text(600, y + 32, resp, size=15.5, anchor="middle", fill=MUTED)
    s.write("da-vinci-flow.svg")


def v2_mapping():
    s = SVG(1200, 520, "HL7 v2 segments mapped to FHIR resources",
            "Mapping diagram. On the left, HL7 v2 segments: PID, PV1, OBR, OBX, AL1, DG1. On the right, the FHIR "
            "resources they become: Patient, Encounter, DiagnosticReport, Observation, AllergyIntolerance, Condition.")
    s.text(64, 64, "HL7 v2 segments become FHIR resources", size=24, weight=700)
    pairs = [("PID", "patient identity", "Patient"), ("PV1", "the visit", "Encounter"),
             ("OBR", "the order or report", "DiagnosticReport"), ("OBX", "each result", "Observation"),
             ("AL1", "an allergy", "AllergyIntolerance"), ("DG1", "a diagnosis", "Condition")]
    for i, (seg, what, res) in enumerate(pairs):
        y = 110 + i * 64
        s.card(80, y, 300, 48, fill=CARD, stroke=AMBER, r=12)
        s.text(104, y + 31, seg, size=18, weight=800, fill=AMBER, font=MONO)
        s.text(170, y + 31, what, size=15.5, fill=MUTED)
        s.path(f"M390 {y + 24} C 560 {y + 24}, 640 {y + 24}, 806 {y + 24}", color="url(#flowLine)", sw=3)
        s.card(820, y, 300, 48, fill=CARD2, stroke=TEAL, r=12)
        s.circle(846, y + 24, 7, TEAL)
        s.text(866, y + 30, res, size=17, weight=700)
    s.pill(470, 488, "dates · identifiers · codes · units · status · US Core", CYAN, 14)
    s.write("v2-fhir-mapping.svg")


# ---- second batch -------------------------------------------------------------------------------------------------

def motif_publichealth(s):
    s.circle(930, 180, 110, CARD, stroke=TEAL, sw=4, extra='filter="url(#lift)"')
    s.add('<rect x="910" y="120" width="40" height="120" rx="8" fill="url(#hero)"/>')
    s.add('<rect x="870" y="160" width="120" height="40" rx="8" fill="url(#hero)"/>')
    for i, (t, c) in enumerate([("eICR", CYAN), ("RR", TEAL), ("ELR", AMBER)]):
        s.pill(1060, 90 + i * 70, t, c, 16)


def motif_network(s):
    pts = [(760, 110), (880, 70), (1010, 120), (1100, 220), (980, 290), (840, 260), (930, 180)]
    for a in range(len(pts) - 1):
        x1, y1 = pts[a]; x2, y2 = pts[6]
        s.add(f'<line x1="{x1}" y1="{y1}" x2="{x2}" y2="{y2}" stroke="{BLUE}" stroke-width="2.5" stroke-opacity="0.7"/>')
        x3, y3 = pts[(a + 1) % 6]
        s.add(f'<line x1="{x1}" y1="{y1}" x2="{x3}" y2="{y3}" stroke="{LINE}" stroke-width="2"/>')
    for i, (x, y) in enumerate(pts):
        hub = i == 6
        s.circle(x, y, 30 if hub else 18, "url(#hero)" if hub else CARD, stroke=CYAN, sw=3, extra='filter="url(#glow)"' if hub else "")
    s.text(930, 186, "QHIN", size=14, weight=800, anchor="middle", fill="#fff")


def motif_key(s):
    s.circle(860, 180, 70, "none", stroke="url(#flowLine)", sw=18)
    s.add('<rect x="925" y="168" width="200" height="24" rx="8" fill="url(#hero)"/>')
    s.add('<rect x="1060" y="190" width="22" height="44" rx="5" fill="url(#hero)"/>')
    s.add('<rect x="1100" y="190" width="22" height="30" rx="5" fill="url(#hero)"/>')
    s.text(860, 188, "SMART", size=20, weight=800, anchor="middle", fill=CYAN)


def motif_bridge(s):
    s.card(700, 110, 170, 140, fill=CARD, stroke=AMBER)
    s.text(785, 170, "X12", size=34, weight=800, anchor="middle", fill=AMBER)
    s.text(785, 205, "278", size=22, weight=700, anchor="middle", fill=MUTED)
    s.card(1000, 110, 170, 140, fill=CARD, stroke=TEAL)
    s.text(1085, 170, "FHIR", size=34, weight=800, anchor="middle", fill=TEAL)
    s.text(1085, 205, "PAS", size=22, weight=700, anchor="middle", fill=MUTED)
    s.arrow(880, 160, 990, 160, color=CYAN, sw=4)
    s.arrow(990, 200, 880, 200, color=INDIGO, sw=4)


def ecr_flow():
    s = SVG(1200, 470, "Electronic case reporting and lab reporting",
            "Flow: an EHR or lab feed reaches Perfuse. Messages carrying a reportable-condition trigger code become "
            "an eICR sent to public health, and a Reportability Response comes back. Lab results become HL7 2.5.1 "
            "ELR messages carrying only the reportable orders.")
    s.text(64, 64, "From clinical data to public health, automatically", size=24, weight=700)
    s.card(64, 150, 220, 200, extra='filter="url(#lift)"')
    s.text(88, 194, "EHR and labs", size=19, weight=700)
    s.lines(88, 230, ["ADT, ORU, notes", "HL7 v2 or FHIR"], size=15.5, fill=MUTED)
    s.card(400, 130, 300, 240, fill="url(#hero)", stroke="none", extra='filter="url(#lift)"')
    s.text(550, 178, "Perfuse", size=24, weight=800, anchor="middle", fill="#fff")
    s.lines(424, 220, ["matches trigger codes (RCTC)", "builds the eICR", "reshapes lab results to ELR", "stores the response"], size=15.5, fill="#e0f2fe", gap=30)
    s.arrow(290, 250, 392, 250, color=CYAN, sw=4)
    s.card(820, 110, 316, 110, extra='filter="url(#lift)"', stroke=TEAL)
    s.text(844, 150, "Public health: case reports", size=18, weight=700)
    s.text(844, 180, "eCR 2.1.2 eICR out", size=15.5, fill=TEAL)
    s.text(844, 204, "Reportability Response back", size=15.5, fill=MUTED)
    s.card(820, 270, 316, 110, extra='filter="url(#lift)"', stroke=AMBER)
    s.text(844, 310, "Public health: lab reporting", size=18, weight=700)
    s.text(844, 340, "HL7 2.5.1 ELR", size=15.5, fill=AMBER)
    s.text(844, 364, "reportable orders only", size=15.5, fill=MUTED)
    s.arrow(706, 200, 812, 165, color=TEAL, sw=3.5)
    s.arrow(812, 190, 706, 225, color=TEAL, sw=2.5, dash="7 6")
    s.arrow(706, 300, 812, 325, color=AMBER, sw=3.5)
    s.pill(64, 410, "a report goes because a code matched, not because someone remembered", CYAN, 14)
    s.write("ecr-elr-flow.svg")


def tefca_diagram():
    s = SVG(1200, 480, "TEFCA exchange with UDAP",
            "Two organisations, each connected to a QHIN, exchange records across the national network. Perfuse "
            "registers with UDAP, verifies signed metadata and certificate chains, and audits every exchange to disk.")
    s.text(64, 64, "Exchanging records with organisations you have never connected to", size=24, weight=700)
    s.card(64, 170, 230, 170, extra='filter="url(#lift)"', stroke=CYAN)
    s.text(88, 214, "Your organisation", size=18, weight=700)
    s.text(88, 244, "with Perfuse", size=15.5, fill=CYAN)
    s.lines(88, 280, ["UDAP registration", "audit log on disk"], size=15, fill=MUTED)
    for x, n in [(390, "QHIN A"), (700, "QHIN B")]:
        s.circle(x + 55, 255, 62, "url(#hero)", extra='filter="url(#lift)"')
        s.text(x + 55, 262, n, size=17, weight=800, anchor="middle", fill="#fff")
    s.add('<path d="M507 255 H 693" stroke="url(#flowLine)" stroke-width="6" stroke-linecap="round"/>')
    s.text(600, 236, "TEFCA network", size=15, anchor="middle", fill=MUTED)
    s.card(906, 170, 230, 170, extra='filter="url(#lift)"', stroke=INDIGO)
    s.text(930, 214, "Another organisation", size=18, weight=700)
    s.text(930, 244, "anywhere in the US", size=15.5, fill=INDIGO)
    s.lines(930, 280, ["hospital, clinic,", "payer or agency"], size=15, fill=MUTED)
    s.arrow(298, 255, 380, 255, color=CYAN, sw=3.5)
    s.arrow(820, 255, 900, 255, color=INDIGO, sw=3.5)
    for i, t in enumerate(["signed metadata verified", "certificate chains checked", "every exchange audited"]):
        s.pill(140 + i * 330, 400, t, TEAL, 15)
    s.write("tefca-udap.svg")


def smart_flow():
    s = SVG(1200, 530, "SMART on FHIR authorization",
            "An app asks the authorization server for access with PKCE. The user signs in and consents to scopes. "
            "The server issues an access token and refresh token. The app calls the FHIR API with the token, and "
            "granular scopes decide what it may read. Backend services use signed client assertions instead.")
    s.text(64, 64, "How a SMART app gets access to FHIR data", size=24, weight=700)
    cols = [("App", "patient, clinician", "or backend", CYAN), ("Authorization server", "sign-in, consent,", "tokens", BLUE),
            ("FHIR API", "scopes enforced", "on every call", INDIGO)]
    for i, (n, a, b, c) in enumerate(cols):
        x = 64 + i * 380
        s.card(x, 110, 310, 120, fill="url(#hero)" if i == 1 else CARD, stroke=c, extra='filter="url(#lift)"')
        s.text(x + 24, 152, n, size=20, weight=800, fill="#fff")
        s.lines(x + 24, 182, [a, b], size=15.5, fill="#e0f2fe" if i == 1 else MUTED)
    for x in (219, 599, 979):
        s.add(f'<line x1="{x}" y1="234" x2="{x}" y2="452" stroke="{LINE}" stroke-width="2" stroke-dasharray="4 6"/>')
    rows = [(272, 219, 599, CYAN, "", "1  authorize, with PKCE"),
            (320, 599, 599, BLUE, "", "2  user signs in and consents to scopes"),
            (384, 599, 219, BLUE, "7 6", "3  access and refresh tokens"),
            (436, 219, 979, INDIGO, "", "4  call FHIR: only what the scopes allow")]
    for y, x1, x2, c, dash, label in rows:
        if x1 != x2:
            s.arrow(x1 + (6 if x2 > x1 else -6), y, x2 + (-8 if x2 > x1 else 8), y, color=c, sw=3, dash=dash)
            s.text((x1 + x2) / 2, y - 10, label, size=15.5, anchor="middle")
        else:
            s.card(x1 - 170, y - 22, 340, 34, fill=CARD2, stroke=c, r=17)
            s.text(x1, y + 1, label, size=15.5, anchor="middle")
    s.pill(64, 470, "Inferno SMART App Launch STU2.2: 80 of 80", TEAL, 15)
    s.write("smart-flow.svg")


def x12_vs_pas():
    s = SVG(1200, 470, "X12 278 and FHIR PAS",
            "Comparison. X12 278: the HIPAA transaction, batch-oriented, used by utilization management systems. "
            "FHIR PAS: a REST API inside the EHR workflow, with documentation attached and pended decisions pushed "
            "back. Perfuse sits between them and converts both ways.")
    s.text(64, 64, "Two ways to ask for prior authorization, one engine between them", size=24, weight=700)
    for x, n, c, items in [(64, "X12 278", AMBER, ["the HIPAA transaction", "segments and loops", "the payer's UM system speaks it", "clearinghouses and EDI"]),
                           (836, "FHIR PAS", TEAL, ["a REST API, Claim/$submit", "documentation attached", "pended decisions pushed back", "part of CMS-0057"])]:
        s.card(x, 120, 300, 300, extra='filter="url(#lift)"', stroke=c)
        s.text(x + 24, 164, n, size=24, weight=800, fill=c)
        for i, t in enumerate(items):
            s.circle(x + 30, 206 + i * 48, 5, c)
            s.text(x + 46, 212 + i * 48, t, size=16)
    s.card(450, 170, 300, 200, fill="url(#hero)", stroke="none", extra='filter="url(#lift)"')
    s.text(600, 228, "Perfuse", size=26, weight=800, anchor="middle", fill="#fff")
    s.lines(600, 266, ["PAS → 278 for the payer", "278 → PAS ClaimResponse", "validated by HL7's validator"], size=15.5, fill="#e0f2fe", anchor="middle", gap=28)
    s.arrow(370, 240, 444, 240, color=AMBER, sw=3.5); s.arrow(444, 300, 370, 300, color=AMBER, sw=3.5, dash="7 6")
    s.arrow(756, 240, 830, 240, color=TEAL, sw=3.5); s.arrow(830, 300, 756, 300, color=TEAL, sw=3.5, dash="7 6")
    s.write("x12-278-vs-pas.svg")


# ---- third batch --------------------------------------------------------------------------------------------------

def motif_dicom(s):
    for i, r in enumerate([120, 92, 64, 36]):
        s.circle(930, 180, r, "none", stroke=[LINE, BLUE, CYAN, TEAL][i], sw=3 if i else 2, extra=f'stroke-opacity="{0.5 + i * 0.15}"')
    s.circle(930, 180, 14, CYAN, extra='filter="url(#glow)"')
    for i, t in enumerate(["C-STORE", "C-FIND", "C-MOVE"]):
        s.pill(1068, 96 + i * 66, t, [CYAN, BLUE, INDIGO][i], 14)


def motif_signed_doc(s):
    s.card(820, 50, 200, 260, fill=CARD, stroke=LINE, r=14, extra='filter="url(#lift)"')
    for i in range(6):
        s.add(f'<rect x="846" y="{88 + i * 28}" width="{148 - (i % 3) * 30}" height="10" rx="5" fill="#334155"/>')
    s.circle(985, 262, 46, "url(#hero)", extra='filter="url(#glow)"')
    s.add('<path d="M965 262 L980 277 L1008 246" fill="none" stroke="#fff" stroke-width="7" stroke-linecap="round" stroke-linejoin="round"/>')
    s.pill(700, 290, "X12 275", AMBER, 15)
    s.pill(1060, 60, "C-CDA", TEAL, 15)


def motif_bars(s):
    vals = [0.55, 0.8, 0.4, 0.95, 0.7]
    for i, v in enumerate(vals):
        h = 220 * v
        s.card(740 + i * 80, 290 - h, 52, h, fill="url(#hero)" if i == 3 else CARD2, stroke=LINE, r=10)
    s.add('<line x1="720" y1="292" x2="1150" y2="292" stroke="#475569" stroke-width="2"/>')
    s.pill(1000, 40, "mean and median, in days", TEAL, 14)


def motif_engines(s):
    names = [("Mirth", "#475569"), ("OIE", "#475569"), ("BridgeLink", "#475569"), ("Perfuse", None)]
    for i, (n, c) in enumerate(names):
        x = 700 + i * 120
        hot = c is None
        h = 200 if hot else 140
        s.card(x, 290 - h, 100, h, fill="url(#hero)" if hot else CARD, stroke="none" if hot else LINE, r=14, extra='filter="url(#lift)"' if hot else "")
        s.text(x + 50, 314, n, size=15, weight=700 if hot else 500, anchor="middle", fill=CYAN if hot else MUTED)


def motif_shadow(s):
    s.path("M700 140 C 820 140, 880 140, 1150 140", color=CYAN, sw=6)
    s.path("M700 140 C 800 140, 820 230, 900 230 S 1050 230, 1150 230", color=INDIGO, sw=4, dash="10 10", arrow=False)
    s.circle(700, 140, 14, CYAN, extra='filter="url(#glow)"')
    s.pill(1000, 92, "live: delivers", CYAN, 14)
    s.pill(830, 252, "shadow: compares, never delivers", INDIGO, 14)


def motif_contract(s):
    s.card(820, 46, 220, 270, fill=CARD, stroke=LINE, r=14, extra='filter="url(#lift)"')
    rows = [("PID-3", TEAL), ("PID-8", TEAL), ("PV1-2", TEAL), ("OBX-6", ROSE), ("ZPI", TEAL)]
    for i, (f, c) in enumerate(rows):
        y = 90 + i * 44
        s.text(846, y + 6, f, size=16, font=MONO, fill=INK, weight=600)
        s.circle(1008, y, 11, c)
        mark = '<path d="M1003 %d l4 4 l8 -9" stroke="#0f172a" stroke-width="3" fill="none" stroke-linecap="round"/>' % (y)
        s.add(mark if c == TEAL else f'<path d="M1004 {y - 4} l8 8 M1012 {y - 4} l-8 8" stroke="#0f172a" stroke-width="3" stroke-linecap="round"/>')
    s.pill(600, 318, "contract violation: OBX-6", ROSE, 14)


def dicom_flow():
    s = SVG(1200, 470, "DICOM routing and de-identification",
            "Flow: modalities and PACS send studies to Perfuse over C-STORE. Perfuse routes, de-identifies, and "
            "extracts metadata. Studies go on to a PACS, de-identified to a research archive or cloud storage, and as "
            "observations in HL7 v2 or FHIR.")
    s.text(64, 64, "Routing imaging, with privacy built into the pipeline", size=24, weight=700)
    s.card(64, 150, 230, 200, extra='filter="url(#lift)"', stroke=CYAN)
    s.text(88, 194, "Modalities, PACS", size=19, weight=700)
    s.lines(88, 230, ["CT, MR, ultrasound", "C-STORE, C-FIND"], size=15.5, fill=MUTED)
    s.card(400, 120, 320, 260, fill="url(#hero)", stroke="none", extra='filter="url(#lift)"')
    s.text(560, 168, "Perfuse", size=24, weight=800, anchor="middle", fill="#fff")
    s.lines(426, 208, ["extracts metadata into tags", "routes on it with filters", "removes the patient", "strips private tags", "queries on a schedule"], size=15.5, fill="#e0f2fe", gap=30)
    s.arrow(300, 250, 392, 250, color=CYAN, sw=4)
    outs = [("PACS", "C-STORE, C-MOVE, C-GET", CYAN), ("Research archive", "de-identified, S3 or Azure", TEAL), ("EHR", "HL7 v2 or FHIR observations", INDIGO)]
    for i, (n, d, c) in enumerate(outs):
        y = 110 + i * 100
        s.card(830, y, 306, 80, extra='filter="url(#lift)"', stroke=c)
        s.text(854, y + 34, n, size=18, weight=700)
        s.text(854, y + 60, d, size=15, fill=c)
        s.arrow(726, 250, 822, y + 40, color=c, sw=3)
    s.pill(64, 418, "verified against a real Orthanc PACS", TEAL, 15)
    s.write("dicom-flow.svg")


def attachments_flow():
    s = SVG(1200, 440, "Signed claims attachments",
            "Flow: a C-CDA document is signed to the HL7 Digital Signatures guide with XAdES-X-L, OCSP and an RFC 3161 "
            "time-stamp, placed in an X12 275 attachment, and sent to the payer. On the way in, every signature is "
            "checked.")
    s.text(64, 64, "From clinical document to signed attachment", size=24, weight=700)
    steps = [("C-CDA", "the clinical document", TEAL), ("Sign", "XAdES-X-L, OCSP,", CYAN), ("X12 275", "006020 attachment", AMBER), ("Payer", "signatures checked", INDIGO)]
    for i, (n, d, c) in enumerate(steps):
        x = 64 + i * 280
        hot = i == 1
        s.card(x, 140, 230, 170, fill="url(#hero)" if hot else CARD, stroke="none" if hot else c, extra='filter="url(#lift)"')
        s.text(x + 24, 190, n, size=24, weight=800, fill="#fff" if hot else c)
        s.text(x + 24, 226, d, size=15.5, fill="#e0f2fe" if hot else MUTED)
        if hot:
            s.text(x + 24, 250, "RFC 3161 time-stamp", size=15.5, fill="#e0f2fe")
        if i < 3:
            s.arrow(x + 236, 225, x + 272, 225, color=BLUE, sw=3.5)
    s.pill(64, 370, "verified by xmlsec1 and OpenSSL", TEAL, 15)
    s.write("attachments-flow.svg")


def metrics_flow():
    s = SVG(1200, 470, "Prior authorization metrics",
            "Flow: a CSV of the year's prior authorization decisions goes into perfuse cms0057 metrics, which produces "
            "the public page in CMS's layout, a CSV and JSON, covering approvals, denials, approvals after appeal, "
            "extensions and mean and median decision times.")
    s.text(64, 64, "One year of decisions, one public page", size=24, weight=700)
    s.card(64, 140, 260, 220, extra='filter="url(#lift)"', stroke=AMBER)
    s.text(88, 184, "decisions.csv", size=19, weight=700, font=MONO, fill=AMBER)
    for i in range(5):
        s.add(f'<rect x="88" y="{206 + i * 26}" width="{200 - (i % 2) * 40}" height="10" rx="5" fill="#334155"/>')
    s.card(420, 170, 300, 160, fill="url(#hero)", stroke="none", extra='filter="url(#lift)"')
    s.text(570, 232, "perfuse cms0057", size=20, weight=800, anchor="middle", fill="#fff", font=MONO)
    s.text(570, 262, "metrics", size=20, weight=800, anchor="middle", fill="#fff", font=MONO)
    s.arrow(330, 250, 412, 250, color=AMBER, sw=4)
    s.card(816, 110, 320, 280, extra='filter="url(#lift)"', stroke=TEAL)
    s.text(840, 150, "Public page, CSV, JSON", size=19, weight=700, fill=TEAL)
    for i, m in enumerate(["approved", "denied", "approved after appeal", "extended timeframe", "mean and median days"]):
        s.circle(848, 188 + i * 40, 6, TEAL)
        s.text(866, 194 + i * 40, m, size=16)
    s.arrow(726, 250, 808, 250, color=TEAL, sw=4)
    s.pill(64, 420, "standard and expedited reported separately, units always written", CYAN, 14)
    s.write("metrics-flow.svg")


def engines_chart():
    s = SVG(1200, 470, "Open-source Mirth-family engines and Perfuse",
            "Comparison of runtime and channel format. Mirth Connect 4.5.2, Open Integration Engine and BridgeLink run "
            "on Java and share Mirth's channel format. Perfuse is a single native binary that imports and exports "
            "Mirth channels and adds FHIR, Da Vinci, CMS-0057, shadow mode and feed contracts.")
    s.text(64, 64, "Same channels, a new foundation", size=24, weight=700)
    s.card(64, 110, 520, 320, extra='filter="url(#lift)"')
    s.text(92, 152, "Mirth 4.5.2, OIE, BridgeLink", size=20, weight=700)
    for i, t in enumerate(["the Mirth 4.5.2 code base", "Java runtime and application server", "Mirth channel XML", "a mature, familiar model"]):
        s.circle(100, 196 + i * 50, 6, "#64748b")
        s.text(118, 202 + i * 50, t, size=16.5, fill=MUTED)
    s.card(616, 110, 520, 320, fill="url(#hero)", stroke="none", extra='filter="url(#lift)"')
    s.text(644, 152, "Perfuse", size=20, weight=800, fill="#fff")
    for i, t in enumerate(["a new engine, written in Go", "one native file, 31 MB at rest", "imports and exports Mirth channels", "FHIR, Da Vinci, CMS-0057 built in", "shadow mode and feed contracts"]):
        s.circle(652, 196 + i * 46, 6, "#fff")
        s.text(670, 202 + i * 46, t, size=16.5, fill="#fff")
    s.write("engines-compared.svg")


def shadow_flow():
    s = SVG(1200, 480, "Shadow mode",
            "Flow: each message reaches the live channel, which delivers it and acknowledges it. A copy runs through "
            "the candidate channel, which has no destinations and cannot deliver. The two outputs are compared field "
            "by field at component level, and the Shadow tab shows the difference rate and each disagreement.")
    s.text(64, 64, "Test a change on real traffic, with nothing delivered", size=24, weight=700)
    s.card(64, 200, 190, 100, extra='filter="url(#lift)"', stroke=CYAN)
    s.text(88, 244, "Message", size=19, weight=700)
    s.text(88, 272, "real traffic", size=15.5, fill=MUTED)
    s.card(360, 110, 300, 110, extra='filter="url(#lift)"', stroke=CYAN)
    s.text(384, 152, "Live channel", size=19, weight=700, fill=CYAN)
    s.text(384, 182, "delivers and acknowledges", size=15.5, fill=MUTED)
    s.card(360, 290, 300, 110, extra='filter="url(#lift)" stroke-dasharray="8 6"', stroke=INDIGO)
    s.text(384, 332, "Candidate channel", size=19, weight=700, fill=INDIGO)
    s.text(384, 362, "no destinations at all", size=15.5, fill=MUTED)
    s.arrow(258, 240, 352, 170, color=CYAN, sw=3.5)
    s.arrow(258, 262, 352, 340, color=INDIGO, sw=3, dash="8 6")
    s.card(780, 110, 180, 110, fill=CARD2, stroke=CYAN)
    s.text(870, 160, "Receiver", size=18, weight=700, anchor="middle")
    s.text(870, 188, "gets the live result", size=14.5, anchor="middle", fill=MUTED)
    s.arrow(666, 165, 772, 165, color=CYAN, sw=3.5)
    s.card(780, 270, 356, 150, fill="url(#hero)", stroke="none", extra='filter="url(#lift)"')
    s.text(804, 312, "Compared field by field", size=19, weight=800, fill="#fff")
    s.lines(804, 344, ["PID-5.1  SMITH  →  Smith", "filter: kept  vs  dropped", "difference rate on the Shadow tab"], size=15, fill="#e0f2fe", gap=26)
    s.add('<path d="M666 345 H 772" stroke="#6366f1" stroke-width="3" marker-end="url(#tip)"/>')
    s.add('<path d="M620 222 C 620 250, 700 290, 772 300" fill="none" stroke="#22d3ee" stroke-width="3" stroke-dasharray="4 6" marker-end="url(#tip)"/>')
    s.write("shadow-mode.svg")


def contract_flow():
    s = SVG(1200, 440, "Feed contracts",
            "Flow: perfuse profile reports what a feed really contains. perfuse contract promote turns that into a "
            "contract. Perfuse re-checks recent traffic against it on a schedule and raises a contract-violation alert the day a "
            "sender drops a field, adds a code or starts repeating a segment.")
    s.text(64, 64, "Know the day a sender changes something", size=24, weight=700)
    steps = [("Profile", "what the feed", "really contains", "perfuse profile", CYAN), ("Promote", "turn it into", "expectations", "contract promote", BLUE),
             ("Check", "recent traffic,", "on a schedule", "check_every: 15m", INDIGO), ("Alert", "contract violation,", "the same day", "alert rule", ROSE)]
    for i, (n, a, b, cmd, c) in enumerate(steps):
        x = 64 + i * 280
        hot = i == 3
        s.card(x, 130, 240, 200, fill=CARD, stroke=c, extra='filter="url(#lift)"')
        s.text(x + 24, 178, n, size=22, weight=800, fill=c)
        s.lines(x + 24, 214, [a, b], size=15.5, fill=MUTED)
        s.text(x + 24, 304, cmd, size=14, font=MONO, fill=AMBER)
        if i < 3:
            s.arrow(x + 246, 230, x + 274, 230, color=BLUE, sw=3.5)
    s.pill(64, 380, "every line says whether it was measured or decided", TEAL, 15)
    s.write("feed-contracts.svg")


if __name__ == "__main__":
    hero("hero-mirth-licence.svg", "MIRTH CONNECT 4.6", ["Your options after", "the licence change"], motif_fork,
         "Banner: four paths after the Mirth Connect 4.6 licence change, with moving forward highlighted.")
    hero("hero-migrate-mirth.svg", "STEP BY STEP", ["Migrating Mirth", "Connect channels"], motif_steps,
         "Banner: six numbered steps rising towards a finished migration.")
    hero("hero-cms-0057.svg", "CMS-0057-F", ["Interoperability and", "Prior Authorization"], motif_shield,
         "Banner: a shield with a check mark between the 2026 and 2027 deadlines.")
    hero("hero-da-vinci.svg", "DA VINCI", ["CRD, DTR and PAS,", "order to decision"], motif_chain,
         "Banner: CRD, DTR and PAS as three linked circles, from order to documentation to decision.")
    hero("hero-v2-fhir.svg", "HL7 V2 → FHIR", ["Converting real", "v2 feeds to FHIR"], motif_v2fhir,
         "Banner: HL7 v2 segments flowing into FHIR resources.")
    hero("hero-ecr.svg", "PUBLIC HEALTH", ["eCR and ELR,", "reporting on autopilot"], motif_publichealth,
         "Banner: a public health cross with eICR, RR and ELR labels.")
    hero("hero-tefca.svg", "TEFCA AND UDAP", ["National exchange,", "explained"], motif_network,
         "Banner: a network of organisations around a QHIN hub.")
    hero("hero-smart.svg", "SMART ON FHIR", ["Secure access", "to FHIR data"], motif_key,
         "Banner: a key labelled SMART.")
    hero("hero-x12-pas.svg", "PRIOR AUTHORIZATION", ["X12 278 and", "FHIR PAS"], motif_bridge,
         "Banner: X12 278 and FHIR PAS connected by arrows in both directions.")
    hero("hero-dicom.svg", "DICOM", ["Imaging routing and", "de-identification"], motif_dicom,
         "Banner: concentric rings like a scan, with DICOM service labels.")
    hero("hero-attachments.svg", "CMS-0053", ["Signed claims", "attachments"], motif_signed_doc,
         "Banner: a document with a signature seal, labelled C-CDA and X12 275.")
    hero("hero-metrics.svg", "CMS-0057 METRICS", ["Prior authorization", "metrics, published"], motif_bars,
         "Banner: a bar chart of prior authorization metrics.")
    hero("hero-engines.svg", "COMPARED", ["Open-source", "integration engines"], motif_engines,
         "Banner: four engines as columns, Perfuse highlighted.")
    hero("hero-shadow.svg", "SHADOW MODE", ["Change a live", "interface safely"], motif_shadow,
         "Banner: a live path that delivers and a dashed shadow path that only compares.")
    hero("hero-contracts.svg", "FEED CONTRACTS", ["Catch upstream", "changes the same day"], motif_contract,
         "Banner: a contract with field checks, one flagged as a violation.")
    dicom_flow(); attachments_flow(); metrics_flow(); engines_chart(); shadow_flow(); contract_flow()
    ecr_flow(); tefca_diagram(); smart_flow(); x12_vs_pas()
    memory_chart(); migration_pipeline(); cms_timeline(); cms_apis(); davinci_flow(); v2_mapping()
    print("wrote", len(os.listdir(OUT)), "files to", OUT)
