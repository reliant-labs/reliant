// Copyright (c) 2026 Reliant Labs

// Package library is the GTM idea bank: a fixed, generic set of go-to-market
// ideas embedded in the binary and served read-only to every org that has
// GTM. Its job is to seed an agent (or a person browsing the library) with
// jumping-off points across every kind of growth work, from pricing to
// in-person events, which are then tailored to one business. An org's own
// strategies and ideas live in the database, not here.
//
// The bank is plain YAML under ideas/ (one file per category), embedded in
// the binary, decoded strictly and validated once on first use. The tests
// fail on any invalid entry, so a bad edit never reaches a build that ships.
//
// The package has its own types and string vocabularies and no proto or
// store dependency: callers map an Entry to their own types. Every value in
// the vocabularies is a lower_snake_case slug; categories map 1:1 to
// GTM_STRATEGY_CATEGORY_<UPPER>.
//
// The content is generic by rule: no company, product or platform names
// (a test enforces it). Write "a professional social network" or "a launch
// directory", never a brand.
package library

import (
	"embed"
	"slices"
	"strings"
	"sync"
)

// Categories, in the stable order All, Filter and Diverse use.
const (
	CategoryPricingOffersIncentives   = "pricing_offers_incentives"
	CategoryReferralViralGrowth       = "referral_viral_growth"
	CategoryPaidAcquisition           = "paid_acquisition"
	CategoryOrganicContentSEO         = "organic_content_seo"
	CategoryOutboundSalesProspecting  = "outbound_sales_prospecting"
	CategoryPartnershipsDistribution  = "partnerships_distribution"
	CategoryLaunchesPR                = "launches_pr"
	CategoryCommunityEvents           = "community_events"
	CategoryProductLedGrowth          = "product_led_growth"
	CategoryConversionOptimization    = "conversion_optimization"
	CategoryRetentionExpansion        = "retention_expansion"
	CategoryTrustReputation           = "trust_reputation"
	CategoryMarketResearchPositioning = "market_research_positioning"
)

// Business types an entry can fit (Fits.BusinessTypes).
const (
	BusinessTypeB2BSaaS        = "b2b_saas"
	BusinessTypeB2CApp         = "b2c_app"
	BusinessTypeDeveloperTools = "developer_tools"
	BusinessTypeEcommerce      = "ecommerce"
	BusinessTypeMarketplace    = "marketplace"
	BusinessTypeAgencyServices = "agency_services"
	BusinessTypeCreatorMedia   = "creator_media"
	BusinessTypeLocalBusiness  = "local_business"
	BusinessTypeOpenSource     = "open_source"
	BusinessTypeHardware       = "hardware"
)

// Business models an entry can fit (Fits.BusinessModels).
const (
	BusinessModelSubscription = "subscription"
	BusinessModelUsageBased   = "usage_based"
	BusinessModelOneTime      = "one_time"
	BusinessModelFreemium     = "freemium"
	BusinessModelFreeOSS      = "free_oss"
	BusinessModelMarketplace  = "marketplace"
	BusinessModelServices     = "services"
	BusinessModelAdSupported  = "ad_supported"
)

var (
	categories = []string{
		CategoryPricingOffersIncentives,
		CategoryReferralViralGrowth,
		CategoryPaidAcquisition,
		CategoryOrganicContentSEO,
		CategoryOutboundSalesProspecting,
		CategoryPartnershipsDistribution,
		CategoryLaunchesPR,
		CategoryCommunityEvents,
		CategoryProductLedGrowth,
		CategoryConversionOptimization,
		CategoryRetentionExpansion,
		CategoryTrustReputation,
		CategoryMarketResearchPositioning,
	}
	businessTypes = []string{
		BusinessTypeB2BSaaS, BusinessTypeB2CApp, BusinessTypeDeveloperTools,
		BusinessTypeEcommerce, BusinessTypeMarketplace, BusinessTypeAgencyServices,
		BusinessTypeCreatorMedia, BusinessTypeLocalBusiness, BusinessTypeOpenSource,
		BusinessTypeHardware,
	}
	businessModels = []string{
		BusinessModelSubscription, BusinessModelUsageBased, BusinessModelOneTime,
		BusinessModelFreemium, BusinessModelFreeOSS, BusinessModelMarketplace,
		BusinessModelServices, BusinessModelAdSupported,
	}
	stages     = []string{"pre_launch", "launch", "early_traction", "growth", "scale"}
	objectives = []string{"awareness", "acquisition", "activation", "conversion", "retention", "expansion", "referral", "research"}
	efforts    = []string{"low", "medium", "high"}
	costs      = []string{"free", "low", "medium", "high"}
	// channelTypes is the generic channel-type vocabulary an entry's
	// ChannelTypes use. They are slugs (^[a-z0-9-]{1,32}$), the same shape as
	// an org channel's channel_types, so a caller can match an entry to the
	// org's channels by shared type.
	channelTypes = []string{
		"blog", "content", "email", "newsletter", "forum", "community", "social",
		"video", "podcast", "events", "paid-ads", "search", "directory",
		"partner", "outbound", "in-product", "website", "review-site", "press",
		"marketplace",
	}
)

// Categories returns the category vocabulary in its stable order.
func Categories() []string { return slices.Clone(categories) }

// BusinessTypes returns the business-type vocabulary.
func BusinessTypes() []string { return slices.Clone(businessTypes) }

// BusinessModels returns the business-model vocabulary.
func BusinessModels() []string { return slices.Clone(businessModels) }

// Stages returns the stage vocabulary, earliest first.
func Stages() []string { return slices.Clone(stages) }

// Objectives returns the objective vocabulary.
func Objectives() []string { return slices.Clone(objectives) }

// Efforts returns the effort vocabulary, least first.
func Efforts() []string { return slices.Clone(efforts) }

// Costs returns the cost vocabulary, cheapest first.
func Costs() []string { return slices.Clone(costs) }

// ChannelTypes returns the channel-type vocabulary.
func ChannelTypes() []string { return slices.Clone(channelTypes) }

// Entry is one idea in the bank. Text fields are markdown. An empty list in
// Fits or Stages means "any".
type Entry struct {
	Key        string `yaml:"key"`
	Category   string `yaml:"category"`
	Name       string `yaml:"name"`
	Summary    string `yaml:"summary"`
	WhyItWorks string `yaml:"why_it_works"`
	Fits       Fit    `yaml:"fits"`
	// Stages the idea suits; empty means any stage.
	Stages    []string `yaml:"stages"`
	Objective string   `yaml:"objective"`
	Effort    string   `yaml:"effort"`
	Cost      string   `yaml:"cost"`
	// SignalDays is the typical number of days to a first signal.
	SignalDays int `yaml:"signal_days"`
	// TailorQuestions are what an agent answers to fit the idea to one
	// business (2 to 5).
	TailorQuestions []string `yaml:"tailor_questions"`
	// SuggestedWorkflow describes an automation that supports the idea, in
	// tool-agnostic terms; empty when none makes sense.
	SuggestedWorkflow string   `yaml:"suggested_workflow"`
	ChannelTypes      []string `yaml:"channel_types"`
	KPIs              []string `yaml:"kpis"`
}

// Fit says which businesses an entry suits. Empty lists mean any.
type Fit struct {
	BusinessTypes  []string `yaml:"business_types"`
	BusinessModels []string `yaml:"business_models"`
}

func (e Entry) clone() Entry {
	e.Fits.BusinessTypes = slices.Clone(e.Fits.BusinessTypes)
	e.Fits.BusinessModels = slices.Clone(e.Fits.BusinessModels)
	e.Stages = slices.Clone(e.Stages)
	e.TailorQuestions = slices.Clone(e.TailorQuestions)
	e.ChannelTypes = slices.Clone(e.ChannelTypes)
	e.KPIs = slices.Clone(e.KPIs)
	return e
}

//go:embed ideas/*.yaml
var ideasFS embed.FS

var (
	loadOnce sync.Once
	bank     []Entry
	byKey    map[string]int
)

// entries returns the validated bank, loading it on first use. The embedded
// files are fixed at build time and the tests validate them, so a load
// failure is a broken build and panics rather than serving a partial bank.
func entries() []Entry {
	loadOnce.Do(func() {
		es, err := load(ideasFS, "ideas")
		if err != nil {
			panic("gtm/library: invalid embedded idea bank: " + err.Error())
		}
		bank = es
		byKey = make(map[string]int, len(es))
		for i, e := range es {
			byKey[e.Key] = i
		}
	})
	return bank
}

// All returns every entry, ordered by category (Categories order) and then
// by the curated order within the category's file. The result is a copy.
func All() []Entry {
	es := entries()
	out := make([]Entry, len(es))
	for i, e := range es {
		out[i] = e.clone()
	}
	return out
}

// Get returns the entry with the given key.
func Get(key string) (Entry, bool) {
	es := entries()
	i, ok := byKey[key]
	if !ok {
		return Entry{}, false
	}
	return es[i].clone(), true
}

// Query selects entries. Every set field narrows the result; a zero Query
// matches everything.
type Query struct {
	// Categories: the entry's category is one of these.
	Categories []string
	// BusinessTypes: the entry fits any type, or one of these.
	BusinessTypes []string
	// BusinessModels: the entry fits any model, or one of these.
	BusinessModels []string
	// Stage: the entry fits any stage, or this one.
	Stage string
	// Objective, Effort, Cost: exact match.
	Objective string
	Effort    string
	Cost      string
	// Text: every whitespace-separated term appears, case-insensitively, in
	// the entry's name or summary.
	Text string
}

// ValidateQuery reports the first value in q that is not in its vocabulary,
// so a caller can refuse a bad filter instead of silently matching nothing.
// Text is free-form and never invalid.
// A repeated value is accepted (an RPC filter can repeat one): it is
// de-duplicated before checking, and matching ignores repeats anyway.
func ValidateQuery(q Query) error {
	if err := checkList("categories", dedupe(q.Categories), categories); err != nil {
		return err
	}
	if err := checkList("business_types", dedupe(q.BusinessTypes), businessTypes); err != nil {
		return err
	}
	if err := checkList("business_models", dedupe(q.BusinessModels), businessModels); err != nil {
		return err
	}
	for _, f := range []struct {
		name, v string
		vocab   []string
	}{
		{"stage", q.Stage, stages},
		{"objective", q.Objective, objectives},
		{"effort", q.Effort, efforts},
		{"cost", q.Cost, costs},
	} {
		if f.v != "" {
			if err := checkOne(f.name, f.v, f.vocab); err != nil {
				return err
			}
		}
	}
	return nil
}

func (q Query) matches(e Entry, terms []string) bool {
	if len(q.Categories) > 0 && !slices.Contains(q.Categories, e.Category) {
		return false
	}
	if !anyOrOverlap(e.Fits.BusinessTypes, q.BusinessTypes) {
		return false
	}
	if !anyOrOverlap(e.Fits.BusinessModels, q.BusinessModels) {
		return false
	}
	if q.Stage != "" && len(e.Stages) > 0 && !slices.Contains(e.Stages, q.Stage) {
		return false
	}
	if q.Objective != "" && e.Objective != q.Objective {
		return false
	}
	if q.Effort != "" && e.Effort != q.Effort {
		return false
	}
	if q.Cost != "" && e.Cost != q.Cost {
		return false
	}
	if len(terms) > 0 {
		hay := strings.ToLower(e.Name + "\n" + e.Summary)
		for _, t := range terms {
			if !strings.Contains(hay, t) {
				return false
			}
		}
	}
	return true
}

// anyOrOverlap: the entry's list is empty (fits any), the query's is empty
// (no filter), or they share a value.
func anyOrOverlap(entry, query []string) bool {
	if len(entry) == 0 || len(query) == 0 {
		return true
	}
	for _, v := range query {
		if slices.Contains(entry, v) {
			return true
		}
	}
	return false
}

// Filter returns the entries q matches, in All order.
func Filter(q Query) []Entry {
	terms := strings.Fields(strings.ToLower(q.Text))
	var out []Entry
	for _, e := range entries() {
		if q.matches(e, terms) {
			out = append(out, e.clone())
		}
	}
	return out
}

// Diverse returns up to n entries matching q, spread across categories: it
// takes the first entry of each category with a match (in Categories order),
// then the second of each, and so on, taking at most ceil(n / k) from any one
// category, where k is the number of categories with a match. The result is
// deterministic: the same query and n always return the same entries in the
// same order. It can hold fewer than n entries when the per-category cap
// binds.
func Diverse(q Query, n int) []Entry {
	return diverse(Filter(q), n)
}

// diverse is Diverse over an already-filtered list (in All order), so the
// spreading and the cap can be tested on synthetic entries.
func diverse(es []Entry, n int) []Entry {
	if n <= 0 {
		return nil
	}
	groups := make(map[string][]Entry)
	for _, e := range es {
		groups[e.Category] = append(groups[e.Category], e)
	}
	if len(groups) == 0 {
		return nil
	}
	perCategory := (n + len(groups) - 1) / len(groups)
	out := make([]Entry, 0, n)
	for round := 0; round < perCategory; round++ {
		took := false
		for _, c := range categories {
			g := groups[c]
			if round >= len(g) {
				continue
			}
			out = append(out, g[round])
			took = true
			if len(out) == n {
				return out
			}
		}
		if !took {
			break
		}
	}
	return out
}

// Fits reports whether e fits a business with the given type and models:
// (e has no business models, or models is empty, or they overlap) and
// (e has no business types, or businessType is empty, or e lists it).
// An unset side of the org's profile never excludes an entry.
func Fits(e Entry, businessType string, models []string) bool {
	if !anyOrOverlap(e.Fits.BusinessModels, models) {
		return false
	}
	if businessType != "" && len(e.Fits.BusinessTypes) > 0 && !slices.Contains(e.Fits.BusinessTypes, businessType) {
		return false
	}
	return true
}
