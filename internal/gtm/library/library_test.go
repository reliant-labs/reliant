// Copyright (c) 2026 Reliant Labs

package library

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedBankLoadsAndValidates(t *testing.T) {
	es, err := load(ideasFS, "ideas")
	if err != nil {
		t.Fatalf("load embedded bank: %v", err)
	}
	if n := len(es); n < 80 || n > 300 {
		t.Errorf("bank has %d entries, want 80..300", n)
	}
	if err := validateBank(All()); err != nil {
		t.Errorf("All() does not validate: %v", err)
	}
}

func TestKeysUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range All() {
		if seen[e.Key] {
			t.Errorf("duplicate key %q", e.Key)
		}
		seen[e.Key] = true
	}
}

func TestAllOrderedByCategory(t *testing.T) {
	es := All()
	last := -1
	for _, e := range es {
		i := slices.Index(categories, e.Category)
		if i < last {
			t.Fatalf("entry %q (category %s) is out of category order", e.Key, e.Category)
		}
		last = i
	}
}

func TestCoverage(t *testing.T) {
	es := All()
	perCategory := map[string]int{}
	for _, e := range es {
		perCategory[e.Category]++
	}
	for _, c := range categories {
		if perCategory[c] < 5 {
			t.Errorf("category %s has %d entries, want at least 5", c, perCategory[c])
		}
	}

	// Every business model and type is named by some entry's fits, so a
	// "fits my business" filter never leaves an org with only the
	// fits-anything entries.
	for _, m := range businessModels {
		if !slices.ContainsFunc(es, func(e Entry) bool { return slices.Contains(e.Fits.BusinessModels, m) }) {
			t.Errorf("no entry names business model %s", m)
		}
	}
	for _, bt := range businessTypes {
		if !slices.ContainsFunc(es, func(e Entry) bool { return slices.Contains(e.Fits.BusinessTypes, bt) }) {
			t.Errorf("no entry names business type %s", bt)
		}
	}

	// Each business model has a pricing idea and a retention idea written
	// for it (not only ones that fit any model).
	for _, cat := range []string{CategoryPricingOffersIncentives, CategoryRetentionExpansion} {
		for _, m := range businessModels {
			if !slices.ContainsFunc(es, func(e Entry) bool {
				return e.Category == cat && slices.Contains(e.Fits.BusinessModels, m)
			}) {
				t.Errorf("no %s entry written for business model %s", cat, m)
			}
		}
	}

	// In-person and community work is well represented.
	inPerson := regexp.MustCompile(`(?i)\b(in[- ]person|meetups?|conferences?|hackathons?|booths?|dinners?|user groups?|networking|workshops?|pop-ups?|hallway)\b`)
	n := 0
	for _, e := range es {
		if e.Category == CategoryCommunityEvents && inPerson.MatchString(e.Name+" "+e.Summary) {
			n++
		}
	}
	if n < 8 {
		t.Errorf("%d community_events entries mention in-person formats, want at least 8", n)
	}
	for _, term := range []string{"meetup", "conference", "hackathon", "networking", "user group", "dinner"} {
		if !slices.ContainsFunc(es, func(e Entry) bool {
			return strings.Contains(strings.ToLower(e.Name+" "+e.Summary), term)
		}) {
			t.Errorf("no entry covers %q", term)
		}
	}

	// Founder-led and high-touch outreach, and long-form content.
	for _, key := range []string{"founder-led-sales-first-customers", "founder-hosted-dinners", "handwritten-notes-and-gifts", "founder-long-form-blog"} {
		if _, ok := Get(key); !ok {
			t.Errorf("missing coverage entry %q", key)
		}
	}

	// Roughly a third of entries describe an automation.
	withWorkflow := 0
	for _, e := range es {
		if e.SuggestedWorkflow != "" {
			withWorkflow++
		}
	}
	if withWorkflow*4 < len(es) {
		t.Errorf("%d of %d entries have a suggested_workflow, want at least a quarter", withWorkflow, len(es))
	}
}

// writeFS builds a one-file bank for the loader tests.
func writeFS(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys["ideas/"+name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

const validEntryYAML = `
  - key: %s
    category: launches_pr
    name: A test idea
    summary: What it is.
    why_it_works: Why it works.
    fits:
      business_models: [subscription]
    stages: [launch]
    objective: awareness
    effort: low
    cost: free
    signal_days: 7
    tailor_questions:
      - First question?
      - Second question?
    channel_types: [blog]
    kpis: [sign-ups]
`

func validEntry(key string) string { return fmt.Sprintf(validEntryYAML, key) }

func TestLoadValidFixture(t *testing.T) {
	es, err := load(writeFS(map[string]string{"launches_pr.yaml": "entries:" + validEntry("a") + validEntry("b")}), "ideas")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(es) != 2 || es[0].Key != "a" || es[1].Key != "b" {
		t.Fatalf("got %+v", es)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	valid := validEntry("ok")
	cases := []struct {
		name    string
		files   map[string]string
		wantErr string
	}{
		{"no files", map[string]string{}, "no idea files"},
		{"unknown file category", map[string]string{"misc.yaml": "entries:" + valid}, "file category"},
		{"no entries", map[string]string{"launches_pr.yaml": "entries: []"}, "no entries"},
		{"second YAML document", map[string]string{"launches_pr.yaml": "entries:" + valid + "---\nentries:" + validEntry("hidden")}, "more than one YAML document"},
		{"unknown field", map[string]string{"launches_pr.yaml": "entries:" + valid + "    depth: deep\n"}, "field depth not found"},
		{"category differs from file", map[string]string{"launches_pr.yaml": "entries:" + strings.Replace(valid, "category: launches_pr", "category: paid_acquisition", 1)}, "not the file's category"},
		{"duplicate key across files", map[string]string{
			"launches_pr.yaml":      "entries:" + valid,
			"paid_acquisition.yaml": "entries:" + strings.Replace(valid, "category: launches_pr", "category: paid_acquisition", 1),
		}, "duplicate key"},
		{"bad key", map[string]string{"launches_pr.yaml": "entries:" + validEntry("Bad Key")}, "does not match"},
		{"empty name", map[string]string{"launches_pr.yaml": "entries:" + strings.Replace(valid, "name: A test idea", "name: ''", 1)}, "name: empty"},
		{"unknown business model", map[string]string{"launches_pr.yaml": "entries:" + strings.Replace(valid, "[subscription]", "[barter]", 1)}, "fits.business_models"},
		{"duplicate business model", map[string]string{"launches_pr.yaml": "entries:" + strings.Replace(valid, "[subscription]", "[subscription, subscription]", 1)}, "duplicate"},
		{"unknown stage", map[string]string{"launches_pr.yaml": "entries:" + strings.Replace(valid, "stages: [launch]", "stages: [someday]", 1)}, "stages"},
		{"unknown objective", map[string]string{"launches_pr.yaml": "entries:" + strings.Replace(valid, "objective: awareness", "objective: fame", 1)}, "objective"},
		{"missing effort", map[string]string{"launches_pr.yaml": "entries:" + strings.Replace(valid, "    effort: low\n", "", 1)}, "effort"},
		{"unknown cost", map[string]string{"launches_pr.yaml": "entries:" + strings.Replace(valid, "cost: free", "cost: priceless", 1)}, "cost"},
		{"zero signal days", map[string]string{"launches_pr.yaml": "entries:" + strings.Replace(valid, "signal_days: 7", "signal_days: 0", 1)}, "signal_days"},
		{"one tailor question", map[string]string{"launches_pr.yaml": "entries:" + strings.Replace(valid, "      - Second question?\n", "", 1)}, "tailor_questions"},
		{"tailor question without question mark", map[string]string{"launches_pr.yaml": "entries:" + strings.Replace(valid, "Second question?", "Second question", 1)}, "not a question"},
		{"unknown channel type", map[string]string{"launches_pr.yaml": "entries:" + strings.Replace(valid, "channel_types: [blog]", "channel_types: [carrier-pigeon]", 1)}, "channel_types"},
		{"malformed channel type", map[string]string{"launches_pr.yaml": "entries:" + strings.Replace(valid, "channel_types: [blog]", "channel_types: [Blog Posts]", 1)}, "channel_types"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(writeFS(tc.files), "ideas")
			if err == nil {
				t.Fatalf("load succeeded, want an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestGet(t *testing.T) {
	e, ok := Get("host-local-meetup-series")
	if !ok || e.Category != CategoryCommunityEvents {
		t.Fatalf("Get(host-local-meetup-series) = %+v, %v", e, ok)
	}
	if _, ok := Get("no-such-idea"); ok {
		t.Fatal("Get(no-such-idea) found an entry")
	}
	// Results are copies: changing one does not change the bank.
	e.TailorQuestions[0] = "changed?"
	e.Fits.BusinessTypes = append(e.Fits.BusinessTypes[:0], "hardware")
	again, _ := Get("host-local-meetup-series")
	if again.TailorQuestions[0] == "changed?" || again.Fits.BusinessTypes[0] == "hardware" {
		t.Fatal("mutating a returned entry changed the bank")
	}
}

func keys(es []Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Key
	}
	return out
}

func TestFilter(t *testing.T) {
	all := All()
	if got := Filter(Query{}); !slices.Equal(keys(got), keys(all)) {
		t.Fatal("a zero query does not return the whole bank in All order")
	}

	t.Run("categories", func(t *testing.T) {
		got := Filter(Query{Categories: []string{CategoryLaunchesPR, CategoryTrustReputation}})
		if len(got) == 0 {
			t.Fatal("no matches")
		}
		for _, e := range got {
			if e.Category != CategoryLaunchesPR && e.Category != CategoryTrustReputation {
				t.Errorf("%s has category %s", e.Key, e.Category)
			}
		}
	})

	t.Run("business model includes fits-any entries", func(t *testing.T) {
		got := Filter(Query{BusinessModels: []string{BusinessModelAdSupported}})
		var named, anyModel int
		for _, e := range got {
			switch {
			case len(e.Fits.BusinessModels) == 0:
				anyModel++
			case slices.Contains(e.Fits.BusinessModels, BusinessModelAdSupported):
				named++
			default:
				t.Errorf("%s does not fit ad_supported: %v", e.Key, e.Fits.BusinessModels)
			}
		}
		if named == 0 || anyModel == 0 {
			t.Errorf("named=%d anyModel=%d, want both > 0", named, anyModel)
		}
	})

	t.Run("business type", func(t *testing.T) {
		for _, e := range Filter(Query{BusinessTypes: []string{BusinessTypeLocalBusiness}}) {
			if len(e.Fits.BusinessTypes) > 0 && !slices.Contains(e.Fits.BusinessTypes, BusinessTypeLocalBusiness) {
				t.Errorf("%s does not fit local_business", e.Key)
			}
		}
	})

	t.Run("stage", func(t *testing.T) {
		got := Filter(Query{Stage: "pre_launch"})
		if len(got) == 0 {
			t.Fatal("no matches")
		}
		for _, e := range got {
			if len(e.Stages) > 0 && !slices.Contains(e.Stages, "pre_launch") {
				t.Errorf("%s does not fit pre_launch: %v", e.Key, e.Stages)
			}
		}
		if slices.Contains(keys(got), "annual-customer-day") {
			t.Error("a scale-only entry matched pre_launch")
		}
	})

	t.Run("empty stages fit any stage", func(t *testing.T) {
		anyStage := Entry{Key: "any", Category: CategoryLaunchesPR}
		scaleOnly := Entry{Key: "scale", Category: CategoryLaunchesPR, Stages: []string{"scale"}}
		for _, st := range stages {
			if !(Query{Stage: st}).matches(anyStage, nil) {
				t.Errorf("an entry with no stages does not match stage %s", st)
			}
		}
		if (Query{Stage: "pre_launch"}).matches(scaleOnly, nil) {
			t.Error("a scale-only entry matched pre_launch")
		}
		if !(Query{}).matches(scaleOnly, nil) {
			t.Error("a query with no stage excluded a staged entry")
		}
	})

	t.Run("objective effort cost", func(t *testing.T) {
		got := Filter(Query{Objective: "research", Effort: "low", Cost: "free"})
		if len(got) == 0 {
			t.Fatal("no matches")
		}
		for _, e := range got {
			if e.Objective != "research" || e.Effort != "low" || e.Cost != "free" {
				t.Errorf("%s: %s/%s/%s", e.Key, e.Objective, e.Effort, e.Cost)
			}
		}
	})

	t.Run("text over name and summary", func(t *testing.T) {
		got := Filter(Query{Text: "  HACKATHON  "})
		if !slices.Contains(keys(got), "run-or-sponsor-a-hackathon") {
			t.Fatalf("text query missed the hackathon entry: %v", keys(got))
		}
		for _, e := range got {
			if !strings.Contains(strings.ToLower(e.Name+e.Summary), "hackathon") {
				t.Errorf("%s does not mention hackathon in name or summary", e.Key)
			}
		}
		if got := Filter(Query{Text: "hackathon dinner"}); len(got) != 0 {
			t.Errorf("all terms must match; got %v", keys(got))
		}
		if got := Filter(Query{Text: "zzzz-no-match"}); len(got) != 0 {
			t.Errorf("got %v", keys(got))
		}
	})

	t.Run("unknown values match nothing", func(t *testing.T) {
		if got := Filter(Query{Categories: []string{"nope"}}); len(got) != 0 {
			t.Errorf("got %v", keys(got))
		}
	})
}

func TestQueryValidate(t *testing.T) {
	if err := ValidateQuery(Query{}); err != nil {
		t.Errorf("zero query: %v", err)
	}
	ok := Query{
		Categories:     []string{CategoryPaidAcquisition},
		BusinessTypes:  []string{BusinessTypeHardware},
		BusinessModels: []string{BusinessModelOneTime},
		Stage:          "growth", Objective: "conversion", Effort: "high", Cost: "medium",
		Text: "anything",
	}
	if err := ValidateQuery(ok); err != nil {
		t.Errorf("valid query: %v", err)
	}
	dup := Query{
		Categories:     []string{CategoryPaidAcquisition, CategoryPaidAcquisition},
		BusinessTypes:  []string{BusinessTypeHardware, BusinessTypeHardware},
		BusinessModels: []string{BusinessModelOneTime, BusinessModelOneTime},
	}
	if err := ValidateQuery(dup); err != nil {
		t.Errorf("repeated values: %v", err)
	}
	if got, want := keys(Filter(dup)), keys(Filter(Query{
		Categories:     []string{CategoryPaidAcquisition},
		BusinessTypes:  []string{BusinessTypeHardware},
		BusinessModels: []string{BusinessModelOneTime},
	})); !slices.Equal(got, want) {
		t.Errorf("repeated values change the result: %v vs %v", got, want)
	}
	for _, q := range []Query{
		{Categories: []string{"nope"}},
		{Categories: []string{CategoryPaidAcquisition, "nope", CategoryPaidAcquisition}},
		{BusinessTypes: []string{"nope"}},
		{BusinessModels: []string{"nope"}},
		{Stage: "nope"},
		{Objective: "nope"},
		{Effort: "nope"},
		{Cost: "nope"},
	} {
		if err := ValidateQuery(q); err == nil {
			t.Errorf("%+v validated", q)
		}
	}
}

func TestDiverse(t *testing.T) {
	if got := Diverse(Query{}, 0); got != nil {
		t.Errorf("n=0: got %v", keys(got))
	}
	if got := Diverse(Query{Text: "zzzz-no-match"}, 10); got != nil {
		t.Errorf("no matches: got %v", keys(got))
	}

	t.Run("deterministic", func(t *testing.T) {
		q := Query{BusinessModels: []string{BusinessModelSubscription}, Stage: "growth"}
		a, b := Diverse(q, 20), Diverse(q, 20)
		if !slices.Equal(keys(a), keys(b)) {
			t.Fatalf("two calls differ:\n%v\n%v", keys(a), keys(b))
		}
	})

	t.Run("one per category, in category order", func(t *testing.T) {
		got := Diverse(Query{}, len(categories))
		if len(got) != len(categories) {
			t.Fatalf("got %d entries, want %d", len(got), len(categories))
		}
		for i, e := range got {
			if e.Category != categories[i] {
				t.Errorf("position %d: category %s, want %s", i, e.Category, categories[i])
			}
		}
	})

	t.Run("spreads before repeating", func(t *testing.T) {
		got := Diverse(Query{}, 8)
		seen := map[string]bool{}
		for _, e := range got {
			if seen[e.Category] {
				t.Errorf("category %s repeated within the first 8 of 13 categories", e.Category)
			}
			seen[e.Category] = true
		}
	})

	t.Run("per-category cap", func(t *testing.T) {
		n := 30
		got := Diverse(Query{}, n)
		if len(got) != n {
			t.Fatalf("got %d, want %d", len(got), n)
		}
		limit := (n + len(categories) - 1) / len(categories)
		count := map[string]int{}
		for _, e := range got {
			count[e.Category]++
			if count[e.Category] > limit {
				t.Errorf("category %s has more than %d entries", e.Category, limit)
			}
		}
		// Round-robin: the second entry of a category comes only after the
		// first of every category.
		for i := 0; i < len(categories); i++ {
			if c := countCategory(got[:len(categories)], got[i].Category); c != 1 {
				t.Errorf("first round repeats category %s", got[i].Category)
			}
		}
	})

	t.Run("cap binds with few categories", func(t *testing.T) {
		q := Query{Categories: []string{CategoryLaunchesPR, CategoryTrustReputation}}
		got := Diverse(q, 5)
		if len(got) != 5 {
			t.Fatalf("got %d, want 5", len(got))
		}
		launches := countCategory(got, CategoryLaunchesPR)
		trust := countCategory(got, CategoryTrustReputation)
		if launches != 3 || trust != 2 {
			t.Errorf("launches=%d trust=%d, want 3 and 2", launches, trust)
		}
		// Entries come from each category in All order.
		want := Filter(Query{Categories: []string{CategoryLaunchesPR}})
		if got[0].Key != want[0].Key || got[2].Key != want[1].Key {
			t.Errorf("not in All order within the category: %v", keys(got))
		}
	})

	t.Run("cap with one sparse and one dense category", func(t *testing.T) {
		// k = 2 categories with matches, so the cap is ceil(6/2) = 3: one
		// entry from the sparse category and three from the dense one, four
		// in all, even though the dense category alone could fill n.
		es := []Entry{{Key: "sparse-1", Category: CategoryPaidAcquisition}}
		for i := 1; i <= 10; i++ {
			es = append(es, Entry{Key: fmt.Sprintf("dense-%d", i), Category: CategoryTrustReputation})
		}
		got := diverse(es, 6)
		want := []string{"sparse-1", "dense-1", "dense-2", "dense-3"}
		if !slices.Equal(keys(got), want) {
			t.Fatalf("diverse = %v, want %v", keys(got), want)
		}
	})

	t.Run("matches the filter", func(t *testing.T) {
		q := Query{BusinessTypes: []string{BusinessTypeOpenSource}, BusinessModels: []string{BusinessModelFreeOSS}}
		filtered := keys(Filter(q))
		for _, e := range Diverse(q, 15) {
			if !slices.Contains(filtered, e.Key) {
				t.Errorf("%s is not in Filter's result", e.Key)
			}
		}
	})
}

func countCategory(es []Entry, c string) int {
	n := 0
	for _, e := range es {
		if e.Category == c {
			n++
		}
	}
	return n
}

func TestFits(t *testing.T) {
	anyEntry := Entry{}
	narrow := Entry{Fits: Fit{BusinessTypes: []string{BusinessTypeOpenSource}, BusinessModels: []string{BusinessModelFreeOSS, BusinessModelSubscription}}}
	cases := []struct {
		name   string
		e      Entry
		bt     string
		models []string
		want   bool
	}{
		{"fits-any entry, empty profile", anyEntry, "", nil, true},
		{"fits-any entry, full profile", anyEntry, BusinessTypeHardware, []string{BusinessModelOneTime}, true},
		{"empty profile never excludes", narrow, "", nil, true},
		{"type and model match", narrow, BusinessTypeOpenSource, []string{BusinessModelFreeOSS}, true},
		{"one overlapping model is enough", narrow, BusinessTypeOpenSource, []string{BusinessModelOneTime, BusinessModelSubscription}, true},
		{"type unset, model matches", narrow, "", []string{BusinessModelSubscription}, true},
		{"models unset, type matches", narrow, BusinessTypeOpenSource, nil, true},
		{"type mismatch", narrow, BusinessTypeHardware, []string{BusinessModelFreeOSS}, false},
		{"model mismatch", narrow, BusinessTypeOpenSource, []string{BusinessModelAdSupported}, false},
		{"type unset, model mismatch", narrow, "", []string{BusinessModelAdSupported}, false},
	}
	for _, tc := range cases {
		if got := Fits(tc.e, tc.bt, tc.models); got != tc.want {
			t.Errorf("%s: Fits = %v, want %v", tc.name, got, tc.want)
		}
	}

	// Fits agrees with Filter over the real bank.
	bt, models := BusinessTypeLocalBusiness, []string{BusinessModelServices}
	var viaFits []string
	for _, e := range All() {
		if Fits(e, bt, models) {
			viaFits = append(viaFits, e.Key)
		}
	}
	viaFilter := keys(Filter(Query{BusinessTypes: []string{bt}, BusinessModels: models}))
	if !slices.Equal(viaFits, viaFilter) {
		t.Errorf("Fits and Filter disagree:\nFits:   %v\nFilter: %v", viaFits, viaFilter)
	}
}

func TestVocabulariesAreCopies(t *testing.T) {
	c := Categories()
	c[0] = "changed"
	if Categories()[0] == "changed" {
		t.Fatal("Categories returned the internal slice")
	}
	for _, vocab := range [][]string{Categories(), BusinessTypes(), BusinessModels(), Stages(), Objectives(), Efforts(), Costs(), ChannelTypes()} {
		if len(vocab) == 0 || firstDuplicate(vocab) != "" {
			t.Errorf("bad vocabulary %v", vocab)
		}
		for _, v := range vocab {
			if !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`).MatchString(v) {
				t.Errorf("vocabulary value %q is not a slug", v)
			}
		}
	}
}
