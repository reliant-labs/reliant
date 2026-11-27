// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/gtm/library"
	"github.com/reliant-labs/reliant/internal/store"
)

// Every bank vocabulary value maps to a domain (proto, table) value and
// back, and the domain has no value the bank lacks.
func TestBankVocabulariesMapBothWays(t *testing.T) {
	require.Len(t, library.Categories(), len(strategyCategories))
	for _, slug := range library.Categories() {
		c := StrategyCategory(toEnum(categoryPrefix, slug))
		assert.Contains(t, strategyCategories, c, slug)
		assert.Equal(t, slug, bankCategory(c))
	}
	for _, c := range strategyCategories {
		assert.Contains(t, library.Categories(), bankCategory(c), c)
	}

	require.Len(t, library.BusinessTypes(), len(businessTypes))
	for _, slug := range library.BusinessTypes() {
		bt := BusinessType(toEnum(businessTypePrefix, slug))
		assert.Contains(t, businessTypes, bt, slug)
		assert.Equal(t, slug, bankBusinessType(bt))
	}
	for _, bt := range businessTypes {
		assert.Contains(t, library.BusinessTypes(), bankBusinessType(bt), bt)
	}

	require.Len(t, library.BusinessModels(), len(businessModels))
	for _, slug := range library.BusinessModels() {
		m := BusinessModel(toEnum(businessModelPrefix, slug))
		assert.Contains(t, businessModels, m, slug)
		assert.Equal(t, slug, bankBusinessModel(m))
	}
	for _, m := range businessModels {
		assert.Contains(t, library.BusinessModels(), bankBusinessModel(m), m)
	}

	assert.Empty(t, bankCategory("NOT_A_CATEGORY"), "a value without the prefix has no slug")
}

func libraryKeys(es []LibraryEntry) []string {
	var keys []string
	for _, e := range es {
		keys = append(keys, e.Key)
	}
	return keys
}

// bankEntry is the first bank entry keep accepts.
func bankEntry(t *testing.T, keep func(library.Entry) bool) library.Entry {
	t.Helper()
	for _, e := range library.All() {
		if keep(e) {
			return e
		}
	}
	t.Fatal("no bank entry fits")
	return library.Entry{}
}

func TestStrategyLibrary(t *testing.T) {
	all := library.All()
	t.Run("lists the whole bank by category then name, paged", func(t *testing.T) {
		f := newFixture(t)
		var got []LibraryEntry
		in := ListStrategyLibraryInput{PageSize: 17}
		for {
			res, err := f.svc.ListStrategyLibrary(ctx(), agent, in)
			require.NoError(t, err)
			require.LessOrEqual(t, len(res.Entries), 17)
			got = append(got, res.Entries...)
			if res.NextPageToken == "" {
				break
			}
			in.PageToken = res.NextPageToken
		}
		require.Len(t, got, len(all))
		order := map[StrategyCategory]int{}
		for i, c := range library.Categories() {
			order[StrategyCategory(toEnum(categoryPrefix, c))] = i
		}
		for i := 1; i < len(got); i++ {
			a, b := got[i-1], got[i]
			assert.True(t, order[a.Category] < order[b.Category] || (a.Category == b.Category && a.Name <= b.Name), "%s before %s", a.Key, b.Key)
		}
		var want []string
		for _, e := range all {
			want = append(want, e.Key)
		}
		assert.ElementsMatch(t, want, libraryKeys(got))
		assert.Zero(t, f.store.r2().seeds, "the bank is read from memory, not the store")
	})
	t.Run("filters are the bank's, in domain values", func(t *testing.T) {
		f := newFixture(t)
		res, err := f.svc.ListStrategyLibrary(ctx(), agent, ListStrategyLibraryInput{Categories: []StrategyCategory{CategoryCommunityEvents}, PageSize: MaxLibraryPageSize})
		require.NoError(t, err)
		require.NotEmpty(t, res.Entries)
		for _, e := range res.Entries {
			assert.Equal(t, CategoryCommunityEvents, e.Category)
		}
		assert.Len(t, res.Entries, len(library.Filter(library.Query{Categories: []string{library.CategoryCommunityEvents}})))

		res, err = f.svc.ListStrategyLibrary(ctx(), agent, ListStrategyLibraryInput{BusinessModels: []BusinessModel{ModelFreeOSS}, Stage: "launch", Effort: "low", PageSize: MaxLibraryPageSize})
		require.NoError(t, err)
		want := library.Filter(library.Query{BusinessModels: []string{library.BusinessModelFreeOSS}, Stage: "launch", Effort: "low"})
		assert.Len(t, res.Entries, len(want))
		for _, e := range res.Entries {
			assert.True(t, len(e.BusinessModels) == 0 || slices.Contains(e.BusinessModels, ModelFreeOSS), e.Key)
			assert.Equal(t, "low", e.Effort)
		}

		for name, in := range map[string]ListStrategyLibraryInput{
			"category":  {Categories: []StrategyCategory{"GTM_STRATEGY_CATEGORY_NOPE"}},
			"type":      {BusinessTypes: []BusinessType{"b2b_saas"}},
			"model":     {BusinessModels: []BusinessModel{"GTM_BUSINESS_MODEL_BARTER"}},
			"stage":     {Stage: "LAUNCH"},
			"objective": {Objective: "fame"},
			"effort":    {Effort: "huge"},
			"cost":      {Cost: "pricey"},
			"query":     {Query: strings.Repeat("q", maxLibraryQueryLen+1)},
			"page size": {PageSize: MaxLibraryPageSize + 1},
		} {
			_, err := f.svc.ListStrategyLibrary(ctx(), agent, in)
			require.ErrorIs(t, err, ErrInvalidArgument, name)
		}
	})
	t.Run("filter lists are capped as the proto caps them, and deduplicated", func(t *testing.T) {
		f := newFixture(t)
		assert.Equal(t, len(library.Categories()), MaxLibraryCategories)
		assert.Equal(t, len(library.BusinessTypes()), MaxLibraryBusinessTypes)
		assert.Equal(t, len(library.BusinessModels()), MaxLibraryBusinessModels)

		full, err := f.svc.ListStrategyLibrary(ctx(), agent, ListStrategyLibraryInput{
			Categories: strategyCategories, BusinessTypes: businessTypes, BusinessModels: businessModels, PageSize: MaxLibraryPageSize,
		})
		require.NoError(t, err, "every value once is exactly at the caps")
		assert.Len(t, full.Entries, min(len(all), MaxLibraryPageSize))

		for name, in := range map[string]ListStrategyLibraryInput{
			"categories":      {Categories: append(slices.Clone(strategyCategories), CategoryLaunchesPR)},
			"business_types":  {BusinessTypes: append(slices.Clone(businessTypes), BusinessHardware)},
			"business_models": {BusinessModels: append(slices.Clone(businessModels), ModelFreemium)},
		} {
			_, err := f.svc.ListStrategyLibrary(ctx(), agent, in)
			require.ErrorIs(t, err, ErrInvalidArgument, name)
			assert.Contains(t, err.Error(), name, "names the list")
		}

		q, err := libraryQuery(ListStrategyLibraryInput{
			Categories:     []StrategyCategory{CategoryLaunchesPR, CategoryCommunityEvents, CategoryLaunchesPR},
			BusinessTypes:  []BusinessType{BusinessB2BSaaS, BusinessB2BSaaS},
			BusinessModels: []BusinessModel{ModelFreemium, ModelSubscription, ModelFreemium},
		})
		require.NoError(t, err)
		assert.Equal(t, []string{library.CategoryLaunchesPR, library.CategoryCommunityEvents}, q.Categories)
		assert.Equal(t, []string{library.BusinessTypeB2BSaaS}, q.BusinessTypes)
		assert.Equal(t, []string{library.BusinessModelFreemium, library.BusinessModelSubscription}, q.BusinessModels)
		dup, err := f.svc.ListStrategyLibrary(ctx(), agent, ListStrategyLibraryInput{Categories: []StrategyCategory{CategoryLaunchesPR, CategoryLaunchesPR}, PageSize: MaxLibraryPageSize})
		require.NoError(t, err)
		one, err := f.svc.ListStrategyLibrary(ctx(), agent, ListStrategyLibraryInput{Categories: []StrategyCategory{CategoryLaunchesPR}, PageSize: MaxLibraryPageSize})
		require.NoError(t, err)
		assert.Equal(t, libraryKeys(one.Entries), libraryKeys(dup.Entries), "a repeat counts once")
	})
	t.Run("fits my business reads the org's positioning", func(t *testing.T) {
		f := newFixture(t)
		in := ListStrategyLibraryInput{FitsMyBusiness: true, PageSize: MaxLibraryPageSize}
		res, err := f.svc.ListStrategyLibrary(ctx(), agent, in)
		require.NoError(t, err)
		assert.Len(t, res.Entries, min(len(all), MaxLibraryPageSize), "no positioning: an unset profile excludes nothing")

		_, err = f.svc.UpdatePositioning(ctx(), person, UpdatePositioningInput{BusinessType: BusinessLocalBusiness, BusinessModels: []BusinessModel{ModelServices}})
		require.NoError(t, err)
		var want []string
		for _, e := range all {
			if library.Fits(e, library.BusinessTypeLocalBusiness, []string{library.BusinessModelServices}) {
				want = append(want, e.Key)
			}
		}
		require.Less(t, len(want), len(all), "the profile excludes some entries")
		res, err = f.svc.ListStrategyLibrary(ctx(), agent, in)
		require.NoError(t, err)
		assert.ElementsMatch(t, want, libraryKeys(res.Entries))

		in.Categories = []StrategyCategory{CategoryCommunityEvents}
		res, err = f.svc.ListStrategyLibrary(ctx(), agent, in)
		require.NoError(t, err)
		for _, e := range res.Entries {
			assert.Equal(t, CategoryCommunityEvents, e.Category, "combines with the filters")
			assert.Contains(t, want, e.Key)
		}
	})
	t.Run("diverse is a deterministic spread, without paging", func(t *testing.T) {
		f := newFixture(t)
		in := ListStrategyLibraryInput{Diverse: true, PageSize: 13, PageToken: "ignored"}
		res, err := f.svc.ListStrategyLibrary(ctx(), agent, in)
		require.NoError(t, err)
		assert.Empty(t, res.NextPageToken)
		require.Len(t, res.Entries, 13)
		cats := map[StrategyCategory]bool{}
		for _, e := range res.Entries {
			cats[e.Category] = true
		}
		assert.Len(t, cats, 13, "one per category")
		var want []string
		for _, e := range library.Diverse(library.Query{}, 13) {
			want = append(want, e.Key)
		}
		assert.Equal(t, want, libraryKeys(res.Entries), "library.Diverse's spread")
		again, err := f.svc.ListStrategyLibrary(ctx(), agent, in)
		require.NoError(t, err)
		assert.Equal(t, libraryKeys(res.Entries), libraryKeys(again.Entries))

		def, err := f.svc.ListStrategyLibrary(ctx(), agent, ListStrategyLibraryInput{Diverse: true})
		require.NoError(t, err)
		assert.Len(t, def.Entries, min(DefaultLibraryPageSize, len(library.Diverse(library.Query{}, DefaultLibraryPageSize))), "page_size 0 is 50")

		_, err = f.svc.UpdatePositioning(ctx(), person, UpdatePositioningInput{BusinessType: BusinessHardware, BusinessModels: []BusinessModel{ModelOneTime}})
		require.NoError(t, err)
		fit, err := f.svc.ListStrategyLibrary(ctx(), agent, ListStrategyLibraryInput{Diverse: true, FitsMyBusiness: true, PageSize: 20})
		require.NoError(t, err)
		require.NotEmpty(t, fit.Entries)
		for _, e := range fit.Entries {
			be, _ := library.Get(e.Key)
			assert.True(t, library.Fits(be, library.BusinessTypeHardware, []string{library.BusinessModelOneTime}), e.Key)
		}
	})
	t.Run("diverseOf is library.Diverse over a filtered list", func(t *testing.T) {
		for _, q := range []library.Query{{}, {Objective: "acquisition"}, {BusinessModels: []string{library.BusinessModelSubscription}}, {Categories: []string{library.CategoryLaunchesPR}}} {
			for _, n := range []int{0, 1, 5, 13, 14, 40, 500} {
				want, got := library.Diverse(q, n), diverseOf(library.Filter(q), n)
				assert.Equal(t, len(want), len(got), "%+v n=%d", q, n)
				for i := range want {
					assert.Equal(t, want[i].Key, got[i].Key, "%+v n=%d", q, n)
				}
			}
		}
	})
	t.Run("get maps an entry to domain values", func(t *testing.T) {
		f := newFixture(t)
		be := bankEntry(t, func(e library.Entry) bool { return len(e.Fits.BusinessTypes) > 0 && len(e.Fits.BusinessModels) > 0 })
		e, err := f.svc.GetStrategyLibraryEntry(ctx(), agent, GetStrategyLibraryEntryInput{Key: be.Key})
		require.NoError(t, err)
		assert.Equal(t, StrategyCategory(toEnum(categoryPrefix, be.Category)), e.Category)
		assert.Equal(t, be.Name, e.Name)
		assert.Equal(t, be.Summary, e.SummaryMD)
		assert.Equal(t, be.WhyItWorks, e.WhyItWorksMD)
		assert.Equal(t, be.TailorQuestions, e.TailorQuestions)
		assert.Equal(t, be.SuggestedWorkflow, e.SuggestedWorkflowMD)
		assert.Equal(t, be.SignalDays, e.SignalDays)
		require.Len(t, e.BusinessTypes, len(be.Fits.BusinessTypes))
		for i, bt := range e.BusinessTypes {
			assert.Contains(t, businessTypes, bt)
			assert.Equal(t, be.Fits.BusinessTypes[i], bankBusinessType(bt))
		}
		for i, m := range e.BusinessModels {
			assert.Contains(t, businessModels, m)
			assert.Equal(t, be.Fits.BusinessModels[i], bankBusinessModel(m))
		}
		_, err = f.svc.GetStrategyLibraryEntry(ctx(), agent, GetStrategyLibraryEntryInput{Key: "no-such-entry"})
		require.ErrorIs(t, err, ErrNotFound)
		assert.Equal(t, "not found", err.Error())
		_, err = f.svc.GetStrategyLibraryEntry(ctx(), agent, GetStrategyLibraryEntryInput{Key: "Not A Key"})
		require.ErrorIs(t, err, ErrInvalidArgument)
	})
	t.Run("use makes a DRAFT strategy, and a first campaign planning the fitting channels", func(t *testing.T) {
		f := newFixture(t)
		be := bankEntry(t, func(e library.Entry) bool { return slices.Contains(e.ChannelTypes, "forum") && len(e.KPIs) > 0 })
		res, err := f.svc.UseLibraryStrategy(ctx(), agent, UseLibraryStrategyInput{LibraryKey: be.Key, CreateFirstCampaign: true})
		require.NoError(t, err)
		st := res.Strategy
		assert.Equal(t, StrategyDraft, st.Status)
		assert.Equal(t, be.Name, st.Name)
		assert.Equal(t, be.Key, st.LibraryKey)
		assert.Equal(t, StrategyCategory(toEnum(categoryPrefix, be.Category)), st.Category)
		assert.Contains(t, st.GoalMD, be.Summary)
		assert.Equal(t, strings.Join(be.KPIs, ", "), st.TargetMetric)
		require.NotNil(t, res.Campaign)
		assert.Equal(t, CampaignDraft, res.Campaign.Status)
		assert.Equal(t, st.ID, res.Campaign.StrategyID)
		assert.Subset(t, res.Campaign.PlannedChannelIDs, []string{f.channel(t, "reddit").ID, f.channel(t, "hacker-news").ID}, "the forum channels")

		bare, err := f.svc.UseLibraryStrategy(ctx(), person, UseLibraryStrategyInput{LibraryKey: be.Key})
		require.NoError(t, err)
		assert.Nil(t, bare.Campaign)
		named, err := f.svc.UseLibraryStrategy(ctx(), person, UseLibraryStrategyInput{LibraryKey: be.Key, CreateFirstCampaign: true, CampaignName: ptr("Q1 push")})
		require.NoError(t, err)
		assert.Equal(t, "Q1 push", named.Campaign.Name)
		_, err = f.svc.UseLibraryStrategy(ctx(), person, UseLibraryStrategyInput{LibraryKey: be.Key, CampaignName: ptr("x")})
		require.ErrorIs(t, err, ErrInvalidArgument, "a campaign name needs create_first_campaign")
		_, err = f.svc.UseLibraryStrategy(ctx(), person, UseLibraryStrategyInput{LibraryKey: "no-such-entry"})
		require.ErrorIs(t, err, ErrNotFound)
	})
	t.Run("plannedChannels matches by key or by a shared channel type", func(t *testing.T) {
		chs := []*store.GtmChannel{{ID: "1", Key: "reddit", ChannelTypes: []string{"forum"}}, {ID: "2", Key: "hacker-news", ChannelTypes: []string{"forum"}}, {ID: "3", Key: "blog"}}
		assert.Equal(t, []string{"3", "1"}, plannedChannels([]string{"blog", "reddit", "blog"}, chs))
		assert.Equal(t, []string{"1", "2"}, plannedChannels([]string{"forum"}, chs))
		assert.Empty(t, plannedChannels([]string{"podcast"}, chs))
	})
}

func TestPositioningBusinessProfile(t *testing.T) {
	f := newFixture(t)
	p, err := f.svc.UpdatePositioning(ctx(), agent, UpdatePositioningInput{PositioningMD: "We sell tools", BusinessType: BusinessB2BSaaS, BusinessModels: []BusinessModel{ModelSubscription, ModelUsageBased}})
	require.NoError(t, err, "a person and an agent alike may")
	assert.Equal(t, BusinessB2BSaaS, p.BusinessType)
	assert.Equal(t, []BusinessModel{ModelSubscription, ModelUsageBased}, p.BusinessModels)
	got, err := f.svc.GetPositioning(ctx(), person)
	require.NoError(t, err)
	assert.Equal(t, p, got)

	p, err = f.svc.UpdatePositioning(ctx(), person, UpdatePositioningInput{ExpectedRevision: p.Revision, PositioningMD: "x"})
	require.NoError(t, err)
	assert.Empty(t, p.BusinessType, "replaced whole: unset clears it")
	assert.Empty(t, p.BusinessModels)

	for name, in := range map[string]UpdatePositioningInput{
		"unknown type":    {BusinessType: "b2b_saas"},
		"unknown model":   {BusinessModels: []BusinessModel{"GTM_BUSINESS_MODEL_UNSPECIFIED"}},
		"duplicate model": {BusinessModels: []BusinessModel{ModelFreemium, ModelFreemium}},
	} {
		in.ExpectedRevision = p.Revision
		_, err := f.svc.UpdatePositioning(ctx(), person, in)
		require.ErrorIs(t, err, ErrInvalidArgument, name)
	}
}

func TestIdeasFromTheBank(t *testing.T) {
	be := library.All()[0]
	answers := make([]TailorAnswer, 0, len(be.TailorQuestions))
	for _, q := range be.TailorQuestions {
		answers = append(answers, TailorAnswer{Question: q, Answer: "For us: developers at small teams."})
	}
	t.Run("an agent and a person cite an entry and answer its questions", func(t *testing.T) {
		f := newFixture(t)
		for _, a := range []Actor{agent, person} {
			i, err := f.svc.CreateIdea(ctx(), a, CreateIdeaInput{Kind: IdeaKindGTM, Title: be.Name, LibraryKey: be.Key, TailorAnswers: answers, SuggestedWorkflowMD: be.SuggestedWorkflow})
			require.NoError(t, err, a.Kind)
			got, err := f.svc.GetIdea(ctx(), person, GetIdeaInput{ID: i.ID})
			require.NoError(t, err)
			assert.Equal(t, be.Key, got.LibraryKey)
			assert.Equal(t, answers, got.TailorAnswers)
			assert.Equal(t, be.SuggestedWorkflow, got.SuggestedWorkflowMD)
		}
	})
	t.Run("update sets, replaces and clears them", func(t *testing.T) {
		f := newFixture(t)
		i, err := f.svc.CreateIdea(ctx(), agent, CreateIdeaInput{Kind: IdeaKindGTM, Title: "t"})
		require.NoError(t, err)
		assert.Empty(t, i.LibraryKey)
		i, err = f.svc.UpdateIdea(ctx(), agent, UpdateIdeaInput{ID: i.ID, ExpectedRevision: i.Revision, LibraryKey: &be.Key, TailorAnswers: &answers, SuggestedWorkflowMD: ptr("Every Monday, draft the post.")})
		require.NoError(t, err)
		assert.Equal(t, be.Key, i.LibraryKey)
		assert.Equal(t, answers, i.TailorAnswers)
		i, err = f.svc.UpdateIdea(ctx(), person, UpdateIdeaInput{ID: i.ID, ExpectedRevision: i.Revision, Title: ptr("renamed")})
		require.NoError(t, err)
		assert.Equal(t, be.Key, i.LibraryKey, "a nil field is left alone")
		assert.Equal(t, answers, i.TailorAnswers)
		i, err = f.svc.UpdateIdea(ctx(), person, UpdateIdeaInput{ID: i.ID, ExpectedRevision: i.Revision, LibraryKey: ptr(""), TailorAnswers: &[]TailorAnswer{}, SuggestedWorkflowMD: ptr("")})
		require.NoError(t, err)
		assert.Empty(t, i.LibraryKey)
		assert.Empty(t, i.TailorAnswers)
		assert.Empty(t, i.SuggestedWorkflowMD)
	})
	t.Run("each is validated, on create and update", func(t *testing.T) {
		f := newFixture(t)
		many := make([]TailorAnswer, MaxTailorAnswers+1)
		for k := range many {
			many[k] = TailorAnswer{Question: "q", Answer: "a"}
		}
		for name, in := range map[string]CreateIdeaInput{
			"unknown key":   {LibraryKey: "no-such-entry"},
			"too many":      {TailorAnswers: many},
			"no question":   {TailorAnswers: []TailorAnswer{{Question: " ", Answer: "a"}}},
			"long question": {TailorAnswers: []TailorAnswer{{Question: strings.Repeat("q", MaxTailorAnswerLen+1)}}},
			"long answer":   {TailorAnswers: []TailorAnswer{{Question: "q", Answer: strings.Repeat("a", MaxTailorAnswerLen+1)}}},
			"long workflow": {SuggestedWorkflowMD: strings.Repeat("w", MaxSuggestedWorkflowLen+1)},
		} {
			in.Kind, in.Title = IdeaKindGTM, "t"
			_, err := f.svc.CreateIdea(ctx(), agent, in)
			require.ErrorIs(t, err, ErrInvalidArgument, name)
		}
		ok := make([]TailorAnswer, MaxTailorAnswers)
		for k := range ok {
			ok[k] = TailorAnswer{Question: "q", Answer: strings.Repeat("a", MaxTailorAnswerLen)}
		}
		i, err := f.svc.CreateIdea(ctx(), agent, CreateIdeaInput{Kind: IdeaKindGTM, Title: "t", TailorAnswers: ok})
		require.NoError(t, err, "exactly at the caps")
		_, err = f.svc.UpdateIdea(ctx(), agent, UpdateIdeaInput{ID: i.ID, ExpectedRevision: i.Revision, LibraryKey: ptr("no-such-entry")})
		require.ErrorIs(t, err, ErrInvalidArgument)
		_, err = f.svc.UpdateIdea(ctx(), agent, UpdateIdeaInput{ID: i.ID, ExpectedRevision: i.Revision, TailorAnswers: &many})
		require.ErrorIs(t, err, ErrInvalidArgument)
	})
}
