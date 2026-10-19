// Package residency says where this server's data may go: the cloud regions a site allows (the EEA, the UK, Switzerland,
// or regions named one by one), and whether a connector's region is one of them.
//
// Perfuse checks what it can know. An AWS connector names its region (S3, SQS, SNS), and an AWS hostname carries one
// (sqs.eu-west-2.amazonaws.com); those are checked when a channel is saved and loaded, so a channel cannot send patient data
// to a bucket in Virginia from a server whose data must stay in Frankfurt. A plain hostname (a hospital's own system, a
// partner's MLLP listener) says nothing about where it is, and is not judged here: the record of processing lists every host
// a channel sends to, for the controller to check.
package residency

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Region is one cloud region Perfuse knows.
type Region struct {
	Provider string `json:"provider"` // aws, azure, gcp
	Code     string `json:"code"`
	City     string `json:"city"`
	Country  string `json:"country"` // ISO 3166 alpha-2
	Area     string `json:"area"`    // eea, uk, ch, other
}

// Regions are the European cloud regions, by area. Countries are those the providers publish for each region.
var Regions = []Region{
	{"aws", "eu-central-1", "Frankfurt", "DE", "eea"},
	{"aws", "eu-west-1", "Dublin", "IE", "eea"},
	{"aws", "eu-west-3", "Paris", "FR", "eea"},
	{"aws", "eu-north-1", "Stockholm", "SE", "eea"},
	{"aws", "eu-south-1", "Milan", "IT", "eea"},
	{"aws", "eu-south-2", "Aragón", "ES", "eea"},
	{"aws", "eu-west-2", "London", "GB", "uk"},
	{"aws", "eu-central-2", "Zurich", "CH", "ch"},
	{"azure", "westeurope", "Netherlands", "NL", "eea"},
	{"azure", "northeurope", "Ireland", "IE", "eea"},
	{"azure", "germanywestcentral", "Frankfurt", "DE", "eea"},
	{"azure", "germanynorth", "Berlin", "DE", "eea"},
	{"azure", "francecentral", "Paris", "FR", "eea"},
	{"azure", "francesouth", "Marseille", "FR", "eea"},
	{"azure", "swedencentral", "Gävle", "SE", "eea"},
	{"azure", "norwayeast", "Oslo", "NO", "eea"},
	{"azure", "norwaywest", "Stavanger", "NO", "eea"},
	{"azure", "italynorth", "Milan", "IT", "eea"},
	{"azure", "polandcentral", "Warsaw", "PL", "eea"},
	{"azure", "spaincentral", "Madrid", "ES", "eea"},
	{"azure", "uksouth", "London", "GB", "uk"},
	{"azure", "ukwest", "Cardiff", "GB", "uk"},
	{"azure", "switzerlandnorth", "Zurich", "CH", "ch"},
	{"azure", "switzerlandwest", "Geneva", "CH", "ch"},
	{"gcp", "europe-west1", "St. Ghislain", "BE", "eea"},
	{"gcp", "europe-west3", "Frankfurt", "DE", "eea"},
	{"gcp", "europe-west4", "Eemshaven", "NL", "eea"},
	{"gcp", "europe-west8", "Milan", "IT", "eea"},
	{"gcp", "europe-west9", "Paris", "FR", "eea"},
	{"gcp", "europe-west10", "Berlin", "DE", "eea"},
	{"gcp", "europe-west12", "Turin", "IT", "eea"},
	{"gcp", "europe-north1", "Hamina", "FI", "eea"},
	{"gcp", "europe-southwest1", "Madrid", "ES", "eea"},
	{"gcp", "europe-central2", "Warsaw", "PL", "eea"},
	{"gcp", "europe-west2", "London", "GB", "uk"},
	{"gcp", "europe-west6", "Zurich", "CH", "ch"},
}

// Areas are the names a policy may use for a group of regions.
var Areas = map[string]string{
	"eea": "the European Economic Area",
	"uk":  "the United Kingdom",
	"ch":  "Switzerland",
}

// Policy is the set of regions data may go to. The zero Policy allows everything (no residency was set).
type Policy struct {
	// Spec is what the setting said, kept to describe the policy back.
	Spec    string
	allowed map[string]bool
}

// Parse reads a policy: areas (eea, uk, ch) and region codes, separated by commas or spaces. Empty allows everything.
func Parse(spec string) (Policy, error) {
	p := Policy{Spec: strings.TrimSpace(spec)}
	if p.Spec == "" {
		return p, nil
	}
	p.allowed = map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(spec), func(r rune) bool { return r == ',' || r == ' ' || r == ';' || r == '+' }) {
		if _, ok := Areas[w]; ok {
			for _, r := range Regions {
				if r.Area == w {
					p.allowed[r.Code] = true
				}
			}
			continue
		}
		if !known(w) && !regionCode.MatchString(w) {
			return Policy{}, fmt.Errorf("%q is neither an area (eea, uk, ch) nor a region code such as eu-central-1", w)
		}
		p.allowed[w] = true
	}
	return p, nil
}

// regionCode is an AWS or Google Cloud region (eu-west-2, europe-west2, us-east-1); Azure's have no digits and are
// accepted by name from Regions.
var regionCode = regexp.MustCompile(`^[a-z]{2,}(-[a-z]+)+-?[0-9]+$`)

func known(code string) bool {
	for _, r := range Regions {
		if r.Code == code {
			return true
		}
	}
	return false
}

// Set says whether a residency was set at all.
func (p Policy) Set() bool { return p.allowed != nil }

// Allows says whether data may go to a region. Everything is allowed when no residency was set.
func (p Policy) Allows(region string) bool {
	if !p.Set() {
		return true
	}
	return p.allowed[strings.ToLower(strings.TrimSpace(region))]
}

// Describe says the policy in words, with its regions.
func (p Policy) Describe() string {
	if !p.Set() {
		return ""
	}
	var codes []string
	for c := range p.allowed {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	return p.Spec + " (" + strings.Join(codes, ", ") + ")"
}

// Codes lists the regions allowed, sorted.
func (p Policy) Codes() []string {
	var codes []string
	for c := range p.allowed {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	return codes
}

var awsHost = regexp.MustCompile(`(?i)(?:^|\.)(?:[a-z0-9-]+[.-])?((?:us|eu|ap|ca|sa|me|af|il|mx)(?:-gov)?-[a-z]+-[0-9])\.amazonaws\.com(?:\.cn)?(?::\d+)?$`)

// RegionOfHost reads the region out of a cloud hostname, when it carries one: sqs.eu-west-2.amazonaws.com,
// bucket.s3.eu-central-1.amazonaws.com, s3-eu-west-1.amazonaws.com.
func RegionOfHost(host string) (provider, region string, ok bool) {
	if m := awsHost.FindStringSubmatch(strings.TrimSpace(host)); m != nil {
		return "aws", strings.ToLower(m[1]), true
	}
	return "", "", false
}

var (
	mu      sync.RWMutex
	current Policy
)

// SetDefault makes p the policy channel validation checks against, returning the previous one.
func SetDefault(p Policy) Policy {
	mu.Lock()
	defer mu.Unlock()
	prev := current
	current = p
	return prev
}

// Default is the policy set at start-up.
func Default() Policy {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Check says what is wrong with sending to (or reading from) a region, or nil.
func (p Policy) Check(what, region string) error {
	if p.Allows(region) {
		return nil
	}
	return fmt.Errorf("%s is in %s, outside where this server's data may go (%s)", what, region, p.Describe())
}
