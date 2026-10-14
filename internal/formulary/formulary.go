// Package formulary is a payer's drug formulary: which drugs each plan covers, on what tier, and with which limits (prior
// authorization, step therapy, quantity limits), and what the member pays.
//
// One file is the single source for everything that answers a question about a drug: the Da Vinci US Drug Formulary API
// (published from it as FHIR), NCPDP Real-Time Prescription Benefit answers, and NCPDP SCRIPT electronic prior authorization
// (whether a drug needs one, and which questions to ask). A formulary that lived in three places would answer the same
// question three ways.
package formulary

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/biodream-llc/perfuse/internal/drug"
)

// Code systems drugs are named in.
const (
	RxNorm = "http://www.nlm.nih.gov/research/umls/rxnorm"
	NDC    = "http://hl7.org/fhir/sid/ndc"
)

// Tiers are the Da Vinci US Drug Formulary DrugTierCS codes.
var Tiers = []string{"generic", "preferred", "non-preferred", "preferred-generic", "non-preferred-generic", "specialty",
	"brand", "preferred-brand", "non-preferred-brand", "zero-cost-share-preventative", "medical-service"}

// Benefits are the Da Vinci US Drug Formulary PharmacyBenefitTypeCS codes: supply length, network and channel.
var Benefits = []string{"1-month-in-retail", "1-month-out-retail", "1-month-in-mail", "1-month-out-mail",
	"3-month-in-retail", "3-month-out-retail", "3-month-in-mail", "3-month-out-mail"}

// CostQualifiers are the Da Vinci US Drug Formulary CostShareOptionCS codes.
var CostQualifiers = []string{"after-deductible", "before-deductible", "no-charge", "no-charge-after-deductible", "charge",
	"coinsurance-not-applicable", "copay-not-applicable", "deductible-waived"}

// File is a formulary file.
type File struct {
	// Payer names the organization that publishes the formulary.
	Payer Payer `yaml:"payer"`
	// Plans are the payer's insurance plans, each pointing at one formulary.
	Plans []Plan `yaml:"plans"`
	// Formularies are the drug lists.
	Formularies []Formulary `yaml:"formularies"`
	// Members maps a member ID (the PBM member ID a prescriber's system sends) to a plan id. Members can also be found
	// through the FHIR store's Coverage resources (subscriberId, and a class of type plan whose value is the plan id) or
	// by group (a plan's groups), so this list is for a small payer or a test.
	Members map[string]string `yaml:"members,omitempty"`
	// DefaultPlan answers for a member whose plan cannot be found, when set. Leave it empty in production: a member this
	// server does not know is told so, rather than quoted another plan's prices.
	DefaultPlan string `yaml:"default_plan,omitempty"`
}

// Payer is who publishes the formulary.
type Payer struct {
	Name string `yaml:"name"`
	// ID is the payer's identifier on the e-prescribing network (the PayerID in SCRIPT and RTPB).
	ID string `yaml:"id,omitempty"`
}

// Plan is one insurance plan.
type Plan struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
	// Type is a Plan-Net InsuranceProductTypeCS code (commppo, commhmo, medicaid, mediadv, ...), default commppo.
	Type string `yaml:"type,omitempty"`
	// Formulary is the id of the formulary this plan uses.
	Formulary string `yaml:"formulary"`
	// Groups are the group IDs (on the member's card, and in RTPB and SCRIPT requests) of employers or programs on the plan.
	Groups []string `yaml:"groups,omitempty"`
	// Phone and Website are what a member or prescriber is pointed at.
	Phone   string `yaml:"phone,omitempty"`
	Website string `yaml:"website,omitempty"`
	// Costs are what the member pays, by pharmacy benefit and tier.
	Costs []Cost `yaml:"costs,omitempty"`
}

// Cost is a member's share for drugs of one tier bought one way.
type Cost struct {
	Benefit string `yaml:"benefit"`
	Tier    string `yaml:"tier"`
	// Copay is in US dollars; Coinsurance is a percentage. Either may be omitted, not both.
	Copay       *float64 `yaml:"copay,omitempty"`
	Coinsurance *float64 `yaml:"coinsurance,omitempty"`
	// Qualifier says when it applies (after-deductible, before-deductible, no-charge, ...), default after-deductible.
	Qualifier string `yaml:"qualifier,omitempty"`
}

// Formulary is one drug list.
type Formulary struct {
	ID    string `yaml:"id"`
	Name  string `yaml:"name"`
	Start string `yaml:"start,omitempty"`
	End   string `yaml:"end,omitempty"`
	Drugs []Drug `yaml:"drugs"`
}

// Drug is one formulary entry.
type Drug struct {
	// RxNorm is the drug's RxNorm concept (an SCD or SBD). Required: the Drug Formulary API names drugs by it.
	RxNorm  string `yaml:"rxnorm"`
	Display string `yaml:"display"`
	// Group is the drug's RxNorm group: the SCDG of a clinical drug (SCD), the SBDG of a branded one (SBD), "metformin
	// Pill" or "Humira Injectable Product". The Drug Formulary API requires it, and it is how a prescriber's search for a
	// drug by name and form finds every strength.
	Group        string `yaml:"group,omitempty"`
	GroupDisplay string `yaml:"group_display,omitempty"`
	// NDCs are the package codes that are this drug, since SCRIPT and RTPB requests name drugs by NDC. Hyphenated, or the
	// eleven-digit form: ten digits without hyphens can be read three ways, which are three different medicines.
	NDCs []string `yaml:"ndc,omitempty"`
	// Tier is a DrugTierCS code.
	Tier string `yaml:"tier"`
	// Benefits are the pharmacy benefit types it is available under; empty means 1-month-in-retail only.
	Benefits []string `yaml:"benefits,omitempty"`
	// NotCovered marks a drug the plan excludes. Excluded drugs are listed so a prescriber hears "not covered" rather
	// than "not found".
	NotCovered bool `yaml:"not_covered,omitempty"`
	// PriorAuthorization, StepTherapy and QuantityLimit are the utilization management limits.
	PriorAuthorization bool           `yaml:"prior_authorization,omitempty"`
	PANewStartsOnly    bool           `yaml:"pa_new_starts_only,omitempty"`
	StepTherapy        bool           `yaml:"step_therapy,omitempty"`
	STNewStartsOnly    bool           `yaml:"step_therapy_new_starts_only,omitempty"`
	QuantityLimit      *QuantityLimit `yaml:"quantity_limit,omitempty"`
	// Questionnaire is the canonical URL of the FHIR Questionnaire a prior authorization for it asks: the same one Da Vinci
	// DTR fills in, and the source of the NCPDP ePA question set.
	Questionnaire string `yaml:"questionnaire,omitempty"`
	// Alternatives are RxNorm codes of drugs to suggest instead, most preferred first.
	Alternatives []string `yaml:"alternatives,omitempty"`
	// Cost30 is the plan's estimated total cost of a 30-day supply in US dollars, when known: what a coinsurance is a
	// percentage of. Without it a coinsurance is quoted as a percentage, never as an invented amount.
	Cost30 float64 `yaml:"cost_30_day,omitempty"`
	// Notes are said to the prescriber as written (the Drug Formulary API's additional coverage information).
	Notes string `yaml:"notes,omitempty"`
}

// QuantityLimit is how much may be dispensed.
type QuantityLimit struct {
	// Quantity units over Days days, rolling.
	Quantity float64 `yaml:"quantity"`
	Days     int     `yaml:"days"`
	// MaxDaily is the most per day, when stated.
	MaxDaily float64 `yaml:"max_daily,omitempty"`
}

var (
	idPattern     = regexp.MustCompile(`^[A-Za-z0-9.-]{1,64}$`)
	rxnormPattern = regexp.MustCompile(`^[0-9]{1,10}$`)
)

// Load reads a formulary file. Every problem is reported at once, since a formulary is usually written by hand.
func Load(r io.Reader) (*File, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 64<<20))
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("the formulary file cannot be read: %w", err)
	}
	if problems := f.Validate(); len(problems) > 0 {
		return nil, &Invalid{Problems: problems}
	}
	f.normalize()
	return &f, nil
}

// Invalid lists what is wrong with a formulary file.
type Invalid struct{ Problems []string }

func (e *Invalid) Error() string {
	return "the formulary file has problems:\n  " + strings.Join(e.Problems, "\n  ")
}

func oneOf(v string, list []string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func date(v string) bool {
	_, err := time.Parse("2006-01-02", v)
	return err == nil
}

// Validate returns every problem with the file.
func (f *File) Validate() []string {
	var p []string
	add := func(format string, args ...any) { p = append(p, fmt.Sprintf(format, args...)) }
	if strings.TrimSpace(f.Payer.Name) == "" {
		add("payer.name is required: the Drug Formulary API names who publishes it")
	}
	formularies := map[string]bool{}
	for i, fm := range f.Formularies {
		where := fmt.Sprintf("formularies[%d]", i)
		if !idPattern.MatchString(fm.ID) {
			add("%s.id %q must be 1 to 64 letters, digits, dots or dashes (it becomes a FHIR id)", where, fm.ID)
		} else if formularies[fm.ID] {
			add("%s.id %q is used twice", where, fm.ID)
		}
		formularies[fm.ID] = true
		if strings.TrimSpace(fm.Name) == "" {
			add("%s.name is required", where)
		}
		for _, d := range []struct{ name, v string }{{"start", fm.Start}, {"end", fm.End}} {
			if d.v != "" && !date(d.v) {
				add("%s.%s %q is not a date (YYYY-MM-DD)", where, d.name, d.v)
			}
		}
		if fm.Start != "" && fm.End != "" && date(fm.Start) && date(fm.End) && fm.End < fm.Start {
			add("%s ends before it starts", where)
		}
		seen := map[string]bool{}
		ndcs := map[string]string{}
		for j, d := range fm.Drugs {
			dw := fmt.Sprintf("%s.drugs[%d]", where, j)
			if !rxnormPattern.MatchString(d.RxNorm) {
				add("%s.rxnorm %q is not an RxNorm code (digits)", dw, d.RxNorm)
			} else if seen[d.RxNorm] {
				add("%s.rxnorm %s is listed twice in this formulary", dw, d.RxNorm)
			}
			seen[d.RxNorm] = true
			if strings.TrimSpace(d.Display) == "" {
				add("%s.display is required: a prescriber reads it", dw)
			}
			if d.Group != "" && !rxnormPattern.MatchString(d.Group) {
				add("%s.group %q is not an RxNorm code", dw, d.Group)
			} else if d.Group == "" && !d.NotCovered {
				add("%s.group is required: the RxNorm SCDG (or SBDG, for a brand) the drug belongs to, which the Drug "+
					"Formulary API publishes beside it", dw)
			}
			if d.Group != "" && d.GroupDisplay == "" {
				add("%s.group_display is required with group", dw)
			}
			if !oneOf(d.Tier, Tiers) {
				add("%s.tier %q is not a Drug Formulary tier (%s)", dw, d.Tier, strings.Join(Tiers, ", "))
			}
			for _, b := range d.Benefits {
				if !oneOf(b, Benefits) {
					add("%s.benefits: %q is not a pharmacy benefit type (%s)", dw, b, strings.Join(Benefits, ", "))
				}
			}
			for _, n := range d.NDCs {
				code, err := drug.ParseNDC(n)
				if err != nil {
					add("%s.ndc %q: %v", dw, n, err)
					continue
				}
				if other, ok := ndcs[code.Eleven().Digits()]; ok && other != d.RxNorm {
					add("%s.ndc %s is also listed under RxNorm %s", dw, n, other)
				}
				ndcs[code.Eleven().Digits()] = d.RxNorm
			}
			if d.NotCovered && (d.PriorAuthorization || d.StepTherapy || d.QuantityLimit != nil) {
				add("%s is not covered and also has limits; a drug that is not covered has nothing to authorize", dw)
			}
			if q := d.QuantityLimit; q != nil && (q.Quantity <= 0 || q.Days <= 0) {
				add("%s.quantity_limit needs a quantity and days above 0", dw)
			}
			if d.Questionnaire != "" && !strings.HasPrefix(d.Questionnaire, "http") {
				add("%s.questionnaire %q is not a canonical URL", dw, d.Questionnaire)
			}
			for _, a := range d.Alternatives {
				if !rxnormPattern.MatchString(a) {
					add("%s.alternatives: %q is not an RxNorm code", dw, a)
				} else if a == d.RxNorm {
					add("%s names itself as its own alternative", dw)
				}
			}
		}
	}
	plans := map[string]bool{}
	for i, pl := range f.Plans {
		where := fmt.Sprintf("plans[%d]", i)
		if !idPattern.MatchString(pl.ID) {
			add("%s.id %q must be 1 to 64 letters, digits, dots or dashes", where, pl.ID)
		} else if plans[pl.ID] {
			add("%s.id %q is used twice", where, pl.ID)
		} else if formularies[pl.ID] {
			add("%s.id %q is also a formulary id; both become InsurancePlan resources and need different ids", where, pl.ID)
		}
		plans[pl.ID] = true
		if strings.TrimSpace(pl.Name) == "" {
			add("%s.name is required", where)
		}
		if !formularies[pl.Formulary] {
			add("%s.formulary %q names no formulary in this file", where, pl.Formulary)
		}
		for j, c := range pl.Costs {
			cw := fmt.Sprintf("%s.costs[%d]", where, j)
			if !oneOf(c.Benefit, Benefits) {
				add("%s.benefit %q is not a pharmacy benefit type", cw, c.Benefit)
			}
			if !oneOf(c.Tier, Tiers) {
				add("%s.tier %q is not a Drug Formulary tier", cw, c.Tier)
			}
			if c.Copay == nil && c.Coinsurance == nil {
				add("%s needs a copay or a coinsurance", cw)
			}
			if c.Copay != nil && *c.Copay < 0 || c.Coinsurance != nil && (*c.Coinsurance < 0 || *c.Coinsurance > 100) {
				add("%s: a copay is dollars from 0, a coinsurance a percentage from 0 to 100", cw)
			}
			if c.Qualifier != "" && !oneOf(c.Qualifier, CostQualifiers) {
				add("%s.qualifier %q is not one of %s", cw, c.Qualifier, strings.Join(CostQualifiers, ", "))
			}
		}
	}
	if len(f.Plans) == 0 {
		add("plans: at least one plan is required, so a member can be matched to a formulary")
	}
	groups := map[string]string{}
	for _, pl := range f.Plans {
		for _, g := range pl.Groups {
			if other, ok := groups[g]; ok && other != pl.ID {
				add("group %q is on both plan %s and plan %s; a member's group must name one plan", g, other, pl.ID)
			}
			groups[g] = pl.ID
		}
	}
	for m, pl := range f.Members {
		if !plans[pl] {
			add("members: %q is on plan %q, which is not in this file", m, pl)
		}
	}
	if f.DefaultPlan != "" && !plans[f.DefaultPlan] {
		add("default_plan %q names no plan in this file", f.DefaultPlan)
	}
	return p
}

func (f *File) normalize() {
	for i := range f.Formularies {
		for j := range f.Formularies[i].Drugs {
			d := &f.Formularies[i].Drugs[j]
			if len(d.Benefits) == 0 {
				d.Benefits = []string{"1-month-in-retail"}
			}
			for k, n := range d.NDCs {
				code, _ := drug.ParseNDC(n)
				d.NDCs[k] = code.Eleven().Digits()
			}
		}
	}
	for i := range f.Plans {
		if f.Plans[i].Type == "" {
			f.Plans[i].Type = "commppo"
		}
		for j := range f.Plans[i].Costs {
			if f.Plans[i].Costs[j].Qualifier == "" {
				f.Plans[i].Costs[j].Qualifier = "after-deductible"
			}
		}
	}
}

// Plan returns the plan with this id.
func (f *File) Plan(id string) (*Plan, bool) {
	for i := range f.Plans {
		if f.Plans[i].ID == id {
			return &f.Plans[i], true
		}
	}
	return nil, false
}

// PlanFor finds a member's plan: by the member list, then by group, then the default plan. ok is false when none applies.
// lookup, when not nil, is asked first (the FHIR store's Coverage resources) and answers a plan id or "".
func (f *File) PlanFor(member, group string, lookup func(member string) string) (*Plan, bool) {
	member, group = strings.TrimSpace(member), strings.TrimSpace(group)
	try := func(id string) (*Plan, bool) {
		if id == "" {
			return nil, false
		}
		return f.Plan(id)
	}
	if member != "" && lookup != nil {
		if p, ok := try(lookup(member)); ok {
			return p, true
		}
	}
	if p, ok := try(f.Members[member]); ok && member != "" {
		return p, true
	}
	if group != "" {
		for i := range f.Plans {
			for _, g := range f.Plans[i].Groups {
				if g == group {
					return &f.Plans[i], true
				}
			}
		}
	}
	return try(f.DefaultPlan)
}

// Formulary returns the formulary with this id.
func (f *File) Formulary(id string) (*Formulary, bool) {
	for i := range f.Formularies {
		if f.Formularies[i].ID == id {
			return &f.Formularies[i], true
		}
	}
	return nil, false
}

// ErrNotListed means the formulary names no such drug.
var ErrNotListed = errors.New("the drug is not on this formulary")

// Find returns a drug of the formulary named by RxNorm code or by NDC (any format).
func (fm *Formulary) Find(system, code string) (*Drug, error) {
	code = strings.TrimSpace(code)
	switch system {
	case RxNorm:
		for i := range fm.Drugs {
			if fm.Drugs[i].RxNorm == code {
				return &fm.Drugs[i], nil
			}
		}
	case NDC:
		n, err := drug.ParseNDC(code)
		if err != nil {
			return nil, fmt.Errorf("%q is not an NDC: %w", code, err)
		}
		for i := range fm.Drugs {
			for _, have := range fm.Drugs[i].NDCs {
				if have == n.Eleven().Digits() {
					return &fm.Drugs[i], nil
				}
			}
		}
	default:
		return nil, fmt.Errorf("drugs are named by RxNorm or NDC, not %q", system)
	}
	return nil, ErrNotListed
}

// Cost returns what a member of the plan pays for a drug of this tier under this benefit.
func (p *Plan) Cost(benefit, tier string) (Cost, bool) {
	for _, c := range p.Costs {
		if c.Benefit == benefit && c.Tier == tier {
			return c, true
		}
	}
	return Cost{}, false
}

// Requirements says, in words, what a drug's coverage asks of the prescriber.
func (d *Drug) Requirements() []string {
	var out []string
	if d.NotCovered {
		return []string{"not covered by this plan"}
	}
	if d.PriorAuthorization {
		s := "prior authorization"
		if d.PANewStartsOnly {
			s += " (new starts only)"
		}
		out = append(out, s)
	}
	if d.StepTherapy {
		s := "step therapy"
		if d.STNewStartsOnly {
			s += " (new starts only)"
		}
		out = append(out, s)
	}
	if q := d.QuantityLimit; q != nil {
		out = append(out, fmt.Sprintf("quantity limit of %g per %d days", q.Quantity, q.Days))
	}
	return out
}

// ExceedsLimit says whether a request for quantity over daysSupply days breaks the drug's quantity limit, and how.
//
// The limit is rolling: Quantity in any Days days. A fill for fewer days may use the whole of it (20 tablets for 10 days
// is within 30 per 30 days); a fill for more days is allowed in proportion (90 days of a 30-per-30 limit is 90). A daily
// maximum, when stated, holds on its own.
func (d *Drug) ExceedsLimit(quantity float64, daysSupply int) (bool, string) {
	q := d.QuantityLimit
	if q == nil || quantity <= 0 {
		return false, ""
	}
	if daysSupply > 0 && q.MaxDaily > 0 && quantity/float64(daysSupply) > q.MaxDaily+1e-9 {
		return true, fmt.Sprintf("%g a day is over the limit of %g a day", quantity/float64(daysSupply), q.MaxDaily)
	}
	allowed := q.Quantity
	if daysSupply > q.Days {
		allowed = q.Quantity * float64(daysSupply) / float64(q.Days)
	}
	if quantity > allowed+1e-9 {
		return true, fmt.Sprintf("%g is over the limit of %g per %d days", quantity, q.Quantity, q.Days)
	}
	return false, ""
}

// Summary counts what the file holds, for the screen that shows it.
type Summary struct {
	Payer       string             `json:"payer"`
	Plans       []PlanSummary      `json:"plans"`
	Formularies []FormularySummary `json:"formularies"`
}

// PlanSummary is one plan in the summary.
type PlanSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Formulary string `json:"formulary"`
	Costs     int    `json:"costs"`
}

// FormularySummary is one formulary in the summary.
type FormularySummary struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Start       string         `json:"start,omitempty"`
	End         string         `json:"end,omitempty"`
	Drugs       int            `json:"drugs"`
	Tiers       map[string]int `json:"tiers"`
	PA          int            `json:"priorAuthorization"`
	StepTherapy int            `json:"stepTherapy"`
	Quantity    int            `json:"quantityLimit"`
	NotCovered  int            `json:"notCovered"`
}

// Summarize counts the file.
func (f *File) Summarize() Summary {
	s := Summary{Payer: f.Payer.Name}
	for _, p := range f.Plans {
		s.Plans = append(s.Plans, PlanSummary{ID: p.ID, Name: p.Name, Formulary: p.Formulary, Costs: len(p.Costs)})
	}
	for _, fm := range f.Formularies {
		fs := FormularySummary{ID: fm.ID, Name: fm.Name, Start: fm.Start, End: fm.End, Drugs: len(fm.Drugs), Tiers: map[string]int{}}
		for _, d := range fm.Drugs {
			fs.Tiers[d.Tier]++
			if d.PriorAuthorization {
				fs.PA++
			}
			if d.StepTherapy {
				fs.StepTherapy++
			}
			if d.QuantityLimit != nil {
				fs.Quantity++
			}
			if d.NotCovered {
				fs.NotCovered++
			}
		}
		s.Formularies = append(s.Formularies, fs)
	}
	sort.Slice(s.Formularies, func(i, j int) bool { return s.Formularies[i].ID < s.Formularies[j].ID })
	return s
}
