// Copyright (c) 2025 Reliant Labs

package catalogindex

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// The scale the index is designed for (INTEGRATIONS_V1_BRIEF.md §3a): hundreds
// of integrations, each with tens of actions.
const (
	benchIntegrations = 500
	benchActions      = 20
)

var (
	benchNouns = []string{"issue", "message", "contact", "deal", "invoice", "file", "event", "task", "order", "customer",
		"ticket", "record", "row", "page", "user", "channel", "comment", "project", "lead", "payment"}
	benchVerbs      = []string{"create", "get", "list", "update", "delete", "search", "send", "archive"}
	benchCategories = []string{"engineering", "communication", "crm", "finance", "productivity", "marketing", "support", "data"}
)

// syntheticCatalog builds n integrations × m actions with realistic text:
// display names, summaries, descriptions and keywords drawn from a shared
// vocabulary, so queries hit many entries across tiers (the expensive case)
// rather than one.
func syntheticCatalog(n, m int) []*reliantv1.IntegrationManifest {
	rng := rand.New(rand.NewSource(1))
	params, _ := structpb.NewStruct(map[string]any{"type": "object"})
	ms := make([]*reliantv1.IntegrationManifest, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("app%03d", i)
		mf := &reliantv1.IntegrationManifest{
			Id: id, Version: 1, DisplayName: fmt.Sprintf("App %03d", i),
			Description: "A synthetic integration for benchmarking the catalog index.",
			Category:    benchCategories[i%len(benchCategories)],
			Keywords:    []string{benchNouns[rng.Intn(len(benchNouns))], "saas"},
			Connection: &reliantv1.ConnectionSpec{Auth: []*reliantv1.AuthMethod{
				{Method: &reliantv1.AuthMethod_ApiKey{ApiKey: &reliantv1.ApiKeyAuth{In: "header", Name: "Authorization"}}},
			}},
		}
		for j := 0; j < m; j++ {
			noun := benchNouns[j%len(benchNouns)]
			verb := benchVerbs[(i+j)%len(benchVerbs)]
			mf.Actions = append(mf.Actions, &reliantv1.ActionSpec{
				Id:          fmt.Sprintf("%s.%s", noun, verb),
				DisplayName: fmt.Sprintf("%s %s", verb, noun),
				Summary:     fmt.Sprintf("%s a %s in App %03d.", verb, noun, i),
				Description: fmt.Sprintf("Use this to %s a %s. It returns the %s and its id, and fails when the %s does not exist.", verb, noun, noun, noun),
				Keywords:    []string{benchNouns[rng.Intn(len(benchNouns))]},
				Params:      params,
			})
		}
		ms = append(ms, mf)
	}
	return ms
}

// benchQueries mixes the shapes a picker and an agent send: one common word
// (thousands of hits), two words, a prefix being typed, an exact ref, a
// filtered browse, and a miss.
var benchQueries = []Query{
	{Text: "issue"},
	{Text: "create issue"},
	{Text: "cre"},
	{Text: "send message"},
	{Text: "app042/issue.create@1"},
	{Text: "invoice", Category: "finance"},
	{Text: ""},
	{Text: "", ConnectedOnly: true},
	{Text: "zzzz nothing matches"},
	{Text: "customer record update"},
}

func benchIndex(b *testing.B) (*Index, map[string]bool) {
	b.Helper()
	idx, err := Build(syntheticCatalog(benchIntegrations, benchActions))
	if err != nil {
		b.Fatal(err)
	}
	usable := map[string]bool{}
	for i := 0; i < benchIntegrations; i += 10 {
		usable[fmt.Sprintf("app%03d", i)] = true
	}
	return idx, usable
}

// BenchmarkSearch reports ns/op over the query mix, plus p50/p95/p99 of the
// per-query latency, at 500 integrations × 20 actions.
func BenchmarkSearch(b *testing.B) {
	idx, usable := benchIndex(b)
	if idx.Len() != benchIntegrations*benchActions {
		b.Fatalf("index has %d entries, want %d", idx.Len(), benchIntegrations*benchActions)
	}
	durations := make([]time.Duration, 0, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := benchQueries[i%len(benchQueries)]
		q.Usable = usable
		start := time.Now()
		if _, err := idx.Search(q); err != nil {
			b.Fatal(err)
		}
		durations = append(durations, time.Since(start))
	}
	b.StopTimer()
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	pct := func(p float64) float64 {
		return float64(durations[int(float64(len(durations)-1)*p)].Microseconds())
	}
	b.ReportMetric(pct(0.50), "p50-µs")
	b.ReportMetric(pct(0.95), "p95-µs")
	b.ReportMetric(pct(0.99), "p99-µs")
}

// BenchmarkSearchPerQuery breaks the mix down so a slow shape is visible.
func BenchmarkSearchPerQuery(b *testing.B) {
	idx, usable := benchIndex(b)
	for _, q := range benchQueries {
		q := q
		q.Usable = usable
		name := q.Text
		if name == "" {
			name = "<browse>"
		}
		if q.ConnectedOnly {
			name += "+connected_only"
		}
		if q.Category != "" {
			name += "+category"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := idx.Search(q); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkBuild(b *testing.B) {
	ms := syntheticCatalog(benchIntegrations, benchActions)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Build(ms); err != nil {
			b.Fatal(err)
		}
	}
}

// TestSearchIsFastAtScale is the budget the benchmark is reported against,
// enforced: at 500×20 the median query must stay well under the 50ms a
// picker's keystroke can afford. It is skipped under -short and -race, where
// timing is not meaningful.
func TestSearchIsFastAtScale(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing budget is checked in full, non-race runs")
	}
	idx, err := Build(syntheticCatalog(benchIntegrations, benchActions))
	if err != nil {
		t.Fatal(err)
	}
	var durations []time.Duration
	for round := 0; round < 20; round++ {
		for _, q := range benchQueries {
			start := time.Now()
			if _, err := idx.Search(q); err != nil {
				t.Fatal(err)
			}
			durations = append(durations, time.Since(start))
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p50 := durations[len(durations)/2]
	t.Logf("p50 over %d queries at %d entries: %s", len(durations), idx.Len(), p50)
	if p50 > 50*time.Millisecond {
		t.Fatalf("p50 search latency %s exceeds 50ms at %d entries", p50, idx.Len())
	}
}
