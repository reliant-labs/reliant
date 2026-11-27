// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/reliant-labs/reliant/internal/gtm/library"
	"github.com/reliant-labs/reliant/internal/store"
)

// The strategy library is the embedded idea bank (internal/gtm/library):
// generic, read-only, the same for every org GTM admits. An org's own
// strategies and ideas are its rows; they name an entry by library_key.

var libraryKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)

const maxLibraryQueryLen = 200

var (
	businessTypes = []BusinessType{
		BusinessB2BSaaS, BusinessB2CApp, BusinessDeveloperTools, BusinessEcommerce, BusinessMarketplace,
		BusinessAgencyServices, BusinessCreatorMedia, BusinessLocalBusiness, BusinessOpenSource, BusinessHardware,
	}
	businessModels = []BusinessModel{
		ModelSubscription, ModelUsageBased, ModelOneTime, ModelFreemium,
		ModelFreeOSS, ModelMarketplace, ModelServices, ModelAdSupported,
	}
)

// The bank's vocabularies are lowercase slugs; the domain's (and the
// proto's and the table's) are the enum names: <prefix><SLUG UPPERCASED>.
const (
	categoryPrefix      = "GTM_STRATEGY_CATEGORY_"
	businessTypePrefix  = "GTM_BUSINESS_TYPE_"
	businessModelPrefix = "GTM_BUSINESS_MODEL_"
)

func toEnum(prefix, slug string) string { return prefix + strings.ToUpper(slug) }

// toSlug is enum's bank slug, or "" when enum does not carry prefix.
func toSlug(prefix, enum string) string {
	rest, ok := strings.CutPrefix(enum, prefix)
	if !ok {
		return ""
	}
	return strings.ToLower(rest)
}

func bankCategory(c StrategyCategory) string { return toSlug(categoryPrefix, string(c)) }
func bankBusinessType(t BusinessType) string { return toSlug(businessTypePrefix, string(t)) }
func bankBusinessModel(m BusinessModel) string {
	return toSlug(businessModelPrefix, string(m))
}

// libraryEntry is a bank entry in domain terms.
func libraryEntry(e library.Entry) LibraryEntry {
	out := LibraryEntry{
		Key:                 e.Key,
		Category:            StrategyCategory(toEnum(categoryPrefix, e.Category)),
		Name:                e.Name,
		SummaryMD:           e.Summary,
		WhyItWorksMD:        e.WhyItWorks,
		Stages:              slices.Clone(e.Stages),
		Objective:           e.Objective,
		Effort:              e.Effort,
		Cost:                e.Cost,
		SignalDays:          e.SignalDays,
		TailorQuestions:     slices.Clone(e.TailorQuestions),
		SuggestedWorkflowMD: e.SuggestedWorkflow,
		ChannelTypes:        slices.Clone(e.ChannelTypes),
		KPIs:                slices.Clone(e.KPIs),
	}
	for _, t := range e.Fits.BusinessTypes {
		out.BusinessTypes = append(out.BusinessTypes, BusinessType(toEnum(businessTypePrefix, t)))
	}
	for _, m := range e.Fits.BusinessModels {
		out.BusinessModels = append(out.BusinessModels, BusinessModel(toEnum(businessModelPrefix, m)))
	}
	return out
}

// checkSlug is v empty or one of vocab.
func checkSlug(field, v string, vocab []string) error {
	if v != "" && !slices.Contains(vocab, v) {
		return invalid(field, "one of "+strings.Join(vocab, ", "))
	}
	return nil
}

// The library filter lists' caps, the proto's max_items: a list holds each
// value of its vocabulary at most once.
const (
	MaxLibraryCategories     = 13
	MaxLibraryBusinessTypes  = 10
	MaxLibraryBusinessModels = 8
)

// libraryList checks one filter list: at most maxLen values (counted as
// sent, as the proto counts them), each in vocab; it returns them mapped to
// bank values with repeats dropped, in first-seen order.
func libraryList[T ~string](field string, vs []T, maxLen int, vocab []T, slug func(T) string) ([]string, error) {
	if len(vs) > maxLen {
		return nil, invalid(field, fmt.Sprintf("at most %d", maxLen))
	}
	var out []string
	for _, v := range vs {
		if !slices.Contains(vocab, v) {
			return nil, invalid(field, fmt.Sprintf("unknown value %q", v))
		}
		if b := slug(v); !slices.Contains(out, b) {
			out = append(out, b)
		}
	}
	return out, nil
}

// libraryQuery checks in's filters and maps them to a bank query.
func libraryQuery(in ListStrategyLibraryInput) (library.Query, error) {
	var q library.Query
	var err error
	if q.Categories, err = libraryList("categories", in.Categories, MaxLibraryCategories, strategyCategories, bankCategory); err != nil {
		return q, err
	}
	if q.BusinessTypes, err = libraryList("business_types", in.BusinessTypes, MaxLibraryBusinessTypes, businessTypes, bankBusinessType); err != nil {
		return q, err
	}
	if q.BusinessModels, err = libraryList("business_models", in.BusinessModels, MaxLibraryBusinessModels, businessModels, bankBusinessModel); err != nil {
		return q, err
	}
	if err := firstErr(
		checkSlug("stage", in.Stage, library.Stages()),
		checkSlug("objective", in.Objective, library.Objectives()),
		checkSlug("effort", in.Effort, library.Efforts()),
		checkSlug("cost", in.Cost, library.Costs()),
		checkLen("query", in.Query, 0, maxLibraryQueryLen),
	); err != nil {
		return q, err
	}
	q.Stage, q.Objective, q.Effort, q.Cost, q.Text = in.Stage, in.Objective, in.Effort, in.Cost, in.Query
	// The domain's checks are the bank's, so this never refuses; it keeps
	// the two from drifting apart silently.
	if err := library.ValidateQuery(q); err != nil {
		return q, invalid("query", err.Error())
	}
	return q, nil
}

// ListStrategyLibrary lists the idea bank: by category (the bank's stable
// order) then name, paged; or, with Diverse, a spread across categories.
// FitsMyBusiness reads the org's positioning.
func (s *service) ListStrategyLibrary(ctx context.Context, actor Actor, in ListStrategyLibraryInput) (ListStrategyLibraryResult, error) {
	if err := s.admit(ctx, actor); err != nil {
		return ListStrategyLibraryResult{}, err
	}
	token := in.PageToken
	if in.Diverse {
		token = "" // a spread has no pages: the token is ignored
	}
	limit, offset, err := page(in.PageSize, token, DefaultLibraryPageSize, MaxLibraryPageSize)
	if err != nil {
		return ListStrategyLibraryResult{}, err
	}
	q, err := libraryQuery(in)
	if err != nil {
		return ListStrategyLibraryResult{}, err
	}
	keep := func(library.Entry) bool { return true }
	if in.FitsMyBusiness {
		var btype string
		var models []string
		pos, err := s.store.GetPositioning(ctx, actor.OrgID)
		switch {
		case errors.Is(err, ErrNotFound):
		case err != nil:
			return ListStrategyLibraryResult{}, err
		default:
			btype = bankBusinessType(BusinessType(pos.BusinessType))
			for _, m := range pos.BusinessModels {
				models = append(models, bankBusinessModel(BusinessModel(m)))
			}
		}
		keep = func(e library.Entry) bool { return library.Fits(e, btype, models) }
	}
	var matched []library.Entry
	for _, e := range library.Filter(q) {
		if keep(e) {
			matched = append(matched, e)
		}
	}
	if in.Diverse {
		// No paging: up to page_size entries (limit carries nextToken's
		// extra row).
		out := []LibraryEntry{}
		for _, e := range diverseOf(matched, limit-1) {
			out = append(out, libraryEntry(e))
		}
		return ListStrategyLibraryResult{Entries: out}, nil
	}
	cat := map[string]int{}
	for i, c := range library.Categories() {
		cat[c] = i
	}
	sort.SliceStable(matched, func(i, j int) bool {
		a, b := matched[i], matched[j]
		if cat[a.Category] != cat[b.Category] {
			return cat[a.Category] < cat[b.Category]
		}
		return a.Name < b.Name
	})
	if offset > len(matched) {
		offset = len(matched)
	}
	rows, next := nextToken(matched[offset:min(len(matched), offset+limit)], limit, offset)
	out := make([]LibraryEntry, 0, len(rows))
	for _, e := range rows {
		out = append(out, libraryEntry(e))
	}
	return ListStrategyLibraryResult{Entries: out, NextPageToken: next}, nil
}

// diverseOf is library.Diverse over an already-filtered list in the bank's
// order (here: the query's matches that also fit the org): round-robin
// across categories in the bank's stable order, at most ceil(n/k) from any
// one of the k categories with a match. A test holds it to library.Diverse.
func diverseOf(es []library.Entry, n int) []library.Entry {
	if n <= 0 {
		return nil
	}
	groups := map[string][]library.Entry{}
	for _, e := range es {
		groups[e.Category] = append(groups[e.Category], e)
	}
	if len(groups) == 0 {
		return nil
	}
	per := (n + len(groups) - 1) / len(groups)
	out := make([]library.Entry, 0, n)
	for round := 0; round < per; round++ {
		took := false
		for _, c := range library.Categories() {
			if g := groups[c]; round < len(g) {
				out = append(out, g[round])
				took = true
				if len(out) == n {
					return out
				}
			}
		}
		if !took {
			break
		}
	}
	return out
}

// GetStrategyLibraryEntry returns one bank entry by key.
func (s *service) GetStrategyLibraryEntry(ctx context.Context, actor Actor, in GetStrategyLibraryEntryInput) (LibraryEntry, error) {
	if err := s.admit(ctx, actor); err != nil {
		return LibraryEntry{}, err
	}
	if !libraryKey.MatchString(in.Key) {
		return LibraryEntry{}, invalid("key", "a library key")
	}
	e, ok := library.Get(in.Key)
	if !ok {
		return LibraryEntry{}, s.notFound(ctx, ErrNotFound, "library entry", in.Key)
	}
	return libraryEntry(e), nil
}

// UseLibraryStrategy makes a DRAFT strategy from a bank entry, and
// optionally a first DRAFT campaign planning the org's ACTIVE channels that
// fit the entry's channel types.
func (s *service) UseLibraryStrategy(ctx context.Context, actor Actor, in UseLibraryStrategyInput) (UseLibraryStrategyResult, error) {
	if err := s.admit(ctx, actor); err != nil {
		return UseLibraryStrategyResult{}, err
	}
	if !libraryKey.MatchString(in.LibraryKey) {
		return UseLibraryStrategyResult{}, invalid("library_key", "a library key")
	}
	if in.CampaignName != nil {
		if !in.CreateFirstCampaign {
			return UseLibraryStrategyResult{}, invalid("campaign_name", "only with create_first_campaign")
		}
		if err := checkLen("campaign_name", *in.CampaignName, 1, MaxCampaignNameLen); err != nil {
			return UseLibraryStrategyResult{}, err
		}
	}
	be, ok := library.Get(in.LibraryKey)
	if !ok {
		return UseLibraryStrategyResult{}, s.notFound(ctx, ErrNotFound, "library entry", in.LibraryKey)
	}
	entry := libraryEntry(be)
	if in.CreateFirstCampaign {
		if err := s.ensureChannels(ctx, actor.OrgID); err != nil {
			return UseLibraryStrategyResult{}, err
		}
	}
	var out UseLibraryStrategyResult
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		out = UseLibraryStrategyResult{}
		now := s.now()
		srow := &store.GtmStrategy{
			OrgID:        actor.OrgID,
			Name:         clip(entry.Name, maxStrategyNameLen),
			GoalMd:       clip(joinNonEmpty("\n\n", entry.SummaryMD, objectiveLine(entry.Objective)), MaxBodyLen),
			TargetMetric: clip(strings.Join(entry.KPIs, ", "), maxTargetMetricLen),
			Status:       string(StrategyDraft),
			Category:     strPtrOrNil(string(entry.Category)),
			LibraryKey:   strPtrOrNil(entry.Key),
			Revision:     1,
			CreatedBy:    actor.UserID,
			UpdatedBy:    actor.UserID,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := s.store.CreateStrategy(ctx, srow); err != nil {
			return err
		}
		out.Strategy = strategyFromRow(srow)
		if !in.CreateFirstCampaign {
			return nil
		}
		channels, err := s.store.ListChannels(ctx, ChannelFilter{OrgID: actor.OrgID, Status: ChannelActive, Limit: MaxChannels})
		if err != nil {
			return err
		}
		name := entry.Name
		if in.CampaignName != nil {
			name = *in.CampaignName
		}
		crow := &store.GtmCampaign{
			OrgID:             actor.OrgID,
			Name:              clip(name, MaxCampaignNameLen),
			Status:            string(CampaignDraft),
			StrategyID:        srow.ID,
			PlannedChannelIds: plannedChannels(entry.ChannelTypes, channels),
			PlannedFormats:    []string{},
			Revision:          1,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		if err := s.store.CreateCampaign(ctx, crow); err != nil {
			return err
		}
		c := campaignFromRow(crow)
		out.Campaign = &c
		return nil
	})
	return out, err
}

// objectiveLine is the goal's objective sentence, "" for none.
func objectiveLine(objective string) string {
	if objective == "" {
		return ""
	}
	return "Objective: " + objective
}

// plannedChannels maps an entry's channel_types to the given channels: in
// channel_types order, each channel whose key is the type or whose types
// include it, once, capped at MaxPlannedChannels.
func plannedChannels(types []string, channels []*store.GtmChannel) []string {
	ids := []string{}
	seen := map[string]bool{}
	for _, t := range types {
		for _, ch := range channels {
			if seen[ch.ID] || len(ids) >= MaxPlannedChannels {
				continue
			}
			if ch.Key == t || slices.Contains(channelTypesOf(ch), t) {
				seen[ch.ID] = true
				ids = append(ids, ch.ID)
			}
		}
	}
	return ids
}

// clip cuts s to at most n runes.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func joinNonEmpty(sep string, parts ...string) string {
	var keep []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, sep)
}
