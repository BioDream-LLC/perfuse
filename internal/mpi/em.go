package mpi

import (
	"context"
	"errors"
	"math"
	"sort"
)

// Estimate is what EstimateWeights found in a site's own records.
type Estimate struct {
	// Weights are the m and u probabilities per field, ready for Settings.Weights.
	Weights map[string]Weights `json:"weights"`
	// Compared is how many pairs had each field on both sides: a field compared on few pairs is estimated loosely.
	Compared map[string]int `json:"compared"`
	// Pairs is the number of candidate pairs the estimate was made from.
	Pairs int `json:"pairs"`
	// MatchShare is the estimated share of those pairs that are the same person.
	MatchShare float64 `json:"matchShare"`
	// Iterations and Converged describe the fit.
	Iterations int  `json:"iterations"`
	Converged  bool `json:"converged"`
}

// MinEstimatePairs is the fewest candidate pairs an estimate is made from. Fewer, and the fit says more about the handful
// of records than about the site.
const MinEstimatePairs = 200

// ErrTooFewPairs means the index holds too little to estimate from yet.
var ErrTooFewPairs = errors.New("mpi: too few candidate pairs to estimate weights from; register more records first")

// estimateFields are the fields estimated: those whose agreement Compare weighs.
var estimateFields = []string{"family", "given", "birth_date", "sex", "postal", "phone", "line"}

// EstimateWeights fits the Fellegi-Sunter m and u probabilities to unlabelled candidate pairs with the
// expectation-maximisation algorithm (Winkler 1988; Jaro 1989): each pair is a mixture of "same person" and "not", and
// the fit finds the probabilities under which the agreement seen is likeliest. No pair needs to have been reviewed.
//
// Each field is agree (partial agreement counts as agreement) or disagree, and a field missing on either side is left
// out of that pair, as Compare leaves it out of the score. The usual conditional-independence assumption applies: names,
// date and address are treated as independent given the class, which they are not quite, so the m probabilities come out
// somewhat high. That is why an estimate is shown to an administrator and applied only when they choose.
func EstimateWeights(pairs []Comparison) (Estimate, error) {
	return estimate(pairs, nil)
}

// EstimateWeightsWithChance is EstimateWeights with u fixed from pairs drawn at random, nearly all of them different
// people: how often each field agrees by chance in the whole index. Candidate pairs alone cannot tell it, because blocking
// chose them for agreeing on a name or a date, so their chance agreement on those fields is far higher than the index's.
// This is the usual remedy (as in Splink and fastLink): u from random pairs, m and the match share from EM.
func EstimateWeightsWithChance(pairs, random []Comparison) (Estimate, error) {
	if len(random) < MinEstimatePairs {
		return Estimate{}, ErrTooFewPairs
	}
	u := map[string]float64{}
	for _, f := range estimateFields {
		var agree, n float64
		for _, c := range random {
			for _, r := range c.Fields {
				if r.Field != f {
					continue
				}
				switch r.Result {
				case "agree", "partial":
					agree++
					n++
				case "disagree":
					n++
				}
			}
		}
		if n > 0 {
			// One agreement added, so a field that never agreed by chance in the sample is not given u = 0.
			u[f] = (agree + 1) / (n + 2)
		}
	}
	return estimate(pairs, u)
}

func estimate(pairs []Comparison, fixedU map[string]float64) (Estimate, error) {
	if len(pairs) < MinEstimatePairs {
		return Estimate{}, ErrTooFewPairs
	}
	nf := len(estimateFields)
	// gamma[j][k]: 1 agree, 0 disagree, -1 missing.
	gamma := make([][]int8, len(pairs))
	compared := map[string]int{}
	for j, c := range pairs {
		g := make([]int8, nf)
		for k := range g {
			g[k] = -1
		}
		for _, f := range c.Fields {
			k := indexOf(estimateFields, f.Field)
			if k < 0 {
				continue
			}
			switch f.Result {
			case "agree", "partial":
				g[k] = 1
			case "disagree":
				g[k] = 0
			}
		}
		for k, v := range g {
			if v >= 0 {
				compared[estimateFields[k]]++
			}
		}
		gamma[j] = g
	}

	m := make([]float64, nf)
	u := make([]float64, nf)
	for k := range m {
		m[k], u[k] = 0.9, 0.1
		if v, ok := fixedU[estimateFields[k]]; ok {
			u[k] = v
		}
	}
	p := 0.1
	const eps = 1e-4
	clamp := func(v float64) float64 { return math.Min(1-eps, math.Max(eps, v)) }
	g := make([]float64, len(pairs))
	out := Estimate{Pairs: len(pairs), Compared: compared}
	for it := 1; it <= 1000; it++ {
		// E: the probability each pair is a match, given the current parameters.
		for j, row := range gamma {
			lm, lu := math.Log(p), math.Log(1-p)
			for k, v := range row {
				switch v {
				case 1:
					lm += math.Log(m[k])
					lu += math.Log(u[k])
				case 0:
					lm += math.Log(1 - m[k])
					lu += math.Log(1 - u[k])
				}
			}
			g[j] = 1 / (1 + math.Exp(lu-lm))
		}
		// M: the parameters that make those probabilities likeliest.
		var sg float64
		for _, v := range g {
			sg += v
		}
		np := clamp(sg / float64(len(g)))
		change := math.Abs(np - p)
		p = np
		for k := 0; k < nf; k++ {
			var ma, mt, ua, ut float64
			for j, row := range gamma {
				if row[k] < 0 {
					continue
				}
				mt += g[j]
				ut += 1 - g[j]
				if row[k] == 1 {
					ma += g[j]
					ua += 1 - g[j]
				}
			}
			if mt == 0 || ut == 0 {
				continue
			}
			nm, nu := clamp(ma/mt), clamp(ua/ut)
			if _, fixed := fixedU[estimateFields[k]]; fixed {
				nu = u[k]
			}
			change = math.Max(change, math.Max(math.Abs(nm-m[k]), math.Abs(nu-u[k])))
			m[k], u[k] = nm, nu
		}
		out.Iterations = it
		if change < 1e-7 {
			out.Converged = true
			break
		}
	}
	out.MatchShare = round4(p)
	out.Weights = map[string]Weights{}
	for k, f := range estimateFields {
		// A field compared on too few pairs, or that came out no more likely to agree among matches than by chance, keeps
		// the default: an estimate it cannot support would be worse than the literature's.
		if compared[f] < MinEstimatePairs/4 || m[k] <= u[k] {
			continue
		}
		out.Weights[f] = Weights{M: round4(m[k]), U: math.Max(1e-6, round6(u[k]))}
	}
	return out, nil
}

func indexOf(list []string, v string) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return -1
}

func round4(v float64) float64 { return math.Round(v*1e4) / 1e4 }
func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// CandidatePairs compares records the blocking would bring together (the same-sounding family name and birth year, the
// same birth date, the same phone), at most max pairs. These are the pairs the index actually decides on, linked or not,
// which is what the weights have to tell apart.
func (s *Store) CandidatePairs(ctx context.Context, tenant string, max int) ([]Comparison, error) {
	if max <= 0 {
		max = 50000
	}
	recs, _, err := s.queryRecords(ctx, s.db, `r.tenant = ? ORDER BY r.id`, tenant)
	if err != nil {
		return nil, err
	}
	blocks := map[string][]int{}
	for i, r := range recs {
		b := blockKeys(r)
		add := func(k string) { blocks[k] = append(blocks[k], i) }
		if b.sx != "" && len(b.dob) >= 4 {
			add("s|" + b.sx + "|" + b.dob[:4])
		}
		if len(b.dob) >= 8 {
			add("d|" + b.dob)
		}
		if b.phone != "" {
			add("p|" + b.phone)
		}
		if b.names != "" {
			add("n|" + b.names)
		}
		if b.place != "" {
			add("l|" + b.place)
		}
	}
	keys := make([]string, 0, len(blocks))
	for k := range blocks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	seen := map[[2]int]bool{}
	var out []Comparison
	for _, k := range keys {
		b := blocks[k]
		// A block as large as a common birth date in a big index is mostly unrelated people; its first records are
		// enough to show what chance agreement looks like.
		if len(b) > 50 {
			b = b[:50]
		}
		for x := 0; x < len(b); x++ {
			for y := x + 1; y < len(b); y++ {
				key := [2]int{b[x], b[y]}
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, Compare(recs[b[x]], recs[b[y]], nil))
				if len(out) >= max {
					return out, nil
				}
			}
		}
	}
	return out, nil
}

// RandomPairs compares n pairs of records drawn at random (deterministically, so an estimate can be repeated).
func (s *Store) RandomPairs(ctx context.Context, tenant string, n int) ([]Comparison, error) {
	recs, _, err := s.queryRecords(ctx, s.db, `r.tenant = ? ORDER BY r.id`, tenant)
	if err != nil {
		return nil, err
	}
	if len(recs) < 2 {
		return nil, nil
	}
	// A linear congruential walk: no math/rand state to seed, the same sample every time for the same records.
	var out []Comparison
	x := uint64(len(recs))*2654435761 + 1
	next := func() int {
		x = x*6364136223846793005 + 1442695040888963407
		return int((x >> 33) % uint64(len(recs)))
	}
	for len(out) < n {
		a, b := next(), next()
		if a == b {
			continue
		}
		out = append(out, Compare(recs[a], recs[b], nil))
	}
	return out, nil
}

// EstimateFromRecords estimates weights from the tenant's own records: u from random pairs, m from the candidate pairs.
func (s *Store) EstimateFromRecords(ctx context.Context, tenant string) (Estimate, error) {
	pairs, err := s.CandidatePairs(ctx, tenant, 0)
	if err != nil {
		return Estimate{}, err
	}
	random, err := s.RandomPairs(ctx, tenant, 20000)
	if err != nil {
		return Estimate{}, err
	}
	return EstimateWeightsWithChance(pairs, random)
}
