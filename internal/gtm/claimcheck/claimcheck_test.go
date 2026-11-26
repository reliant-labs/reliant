package claimcheck_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/gtm/claimcheck"
)

var now = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

func kinds(ms []claimcheck.Mention) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Kind+":"+m.Text)
	}
	return out
}

func TestExtract(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"price per month", "Services start at $7.25/mo.", []string{"price:$7.25/mo"}},
		{"price per word", "Postgres is $16.40 per month.", []string{"price:$16.40 per month"}},
		{"duration", "Trial deploys last 36 hours, then 10 days.", []string{"duration:36 hours", "duration:10 days"}},
		{"short duration", "Live for 36h.", []string{"duration:36h"}},
		{"free and no account", "Deploy free with no account.", []string{"free:free", "no_account:no account"}},
		{"license", "Acme Studio is Apache-2.0 and Acme Cloud is BSL 1.1.", []string{"license:Apache-2.0", "competitor:placeholder" /* placeholder replaced below */}},
		{"percent and multiplier", "30% better, 3x smaller.", []string{"percent:30%", "multiplier:3x"}},
		{"superlative", "The fastest way to ship.", []string{"superlative:fastest"}},
		{"absolute", "Acme Bot never merges.", []string{"absolute:never"}},
		{"competitor", "Unlike Vercel, we host it.", []string{"competitor:Vercel"}},
		{"version number is not a claim", "Uses Go 1.27 and v2.0.", nil},
		{"small count is not a claim", "Follow these 3 steps.", nil},
		{"urls and fenced code are not prose", "See https://example.com/v2/2026 and\n```\n$5 per month\n```\n", nil},
		{"inline code is read", "Costs `$5 per month` here.", []string{"price:$5 per month"}},
		{"plain prose", "We build tools for people who ship code.", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "license" {
				tc.want = []string{"license:Apache-2.0", "license:BSL 1.1"}
			}
			got := kinds(claimcheck.Rules{}.Extract(tc.text))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("Extract(%q)\n got %v\nwant %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestExtractTenantCompetitors(t *testing.T) {
	got := kinds(claimcheck.Rules{Competitors: []string{"Acme Deploy"}}.Extract("We outperform Acme Deploy on price."))
	want := []string{"superlative:outperform", "competitor:Acme Deploy"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestExtractContextIsTheSentence(t *testing.T) {
	ms := claimcheck.Rules{}.Extract("Intro line. Trial deploys last 36 hours! Outro.")
	if len(ms) != 1 || ms[0].Context != "Trial deploys last 36 hours!" {
		t.Fatalf("got %+v", ms)
	}
}

func TestExtractContextKeepsDecimalsInsideTheSentence(t *testing.T) {
	ms := claimcheck.Rules{}.Extract("Acme Cloud is BSL 1.1 source-available. It costs $7.25/mo.")
	if len(ms) != 3 {
		t.Fatalf("got %+v", ms)
	}
	for _, m := range ms[:2] {
		if m.Context != "Acme Cloud is BSL 1.1 source-available." {
			t.Errorf("context of %q = %q", m.Text, m.Context)
		}
	}
	if ms[2].Context != "It costs $7.25/mo." {
		t.Errorf("context = %q", ms[2].Context)
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"  Acme Studio is  Apache-2.0. ": "acme studio is apache-2.0",
		"One FREE Docs site!!":           "one free docs site",
		"a\tb\nc":                        "a b c",
	} {
		if got := claimcheck.Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q want %q", in, got, want)
		}
	}
}

func claim(id, text string, status claimcheck.Status) claimcheck.Claim {
	return claimcheck.Claim{ID: id, Text: text, Status: status}
}

func TestCheck(t *testing.T) {
	overdue := now.Add(-time.Hour)
	future := now.Add(24 * time.Hour)
	registry := []claimcheck.Claim{
		claim("apache", "Acme Studio is open source under the Apache License 2.0", claimcheck.StatusVerified),
		claim("trial", "trial deploys last 36h and are deleted 10 days after that", claimcheck.StatusVerified),
		claim("site", "one free Docs site per workspace", claimcheck.StatusVerified),
		claim("service", "service $7.25/mo", claimcheck.StatusVerified),
		claim("bsl", "Acme Cloud is BSL 1.1 source-available, not OSI open source", claimcheck.StatusVerified),
		claim("merge", "Acme Bot never merges", claimcheck.StatusVerified),
		claim("draft", "Postgres is $15/mo", claimcheck.StatusUnverified),
		claim("old", "cloud machines from $12/mo", claimcheck.StatusRetired),
		{ID: "stale", Text: "the free tier includes 20 GiB of traffic", Status: claimcheck.StatusVerified, RecheckAt: &overdue},
		{ID: "fresh", Text: "credit top-ups carry a 3% service fee", Status: claimcheck.StatusVerified, RecheckAt: &future},
	}
	tests := []struct {
		name   string
		text   string
		kind   claimcheck.FindingKind
		claim  string
		reason string
	}{
		{"verified license", "Acme Studio is Apache-2.0 licensed.", claimcheck.MatchedVerified, "apache", ""},
		{"license spelled differently", "Acme Studio is Apache 2.0.", claimcheck.MatchedVerified, "apache", ""},
		{"open source studio", "Acme Studio is open source.", claimcheck.MatchedVerified, "apache", ""},
		{"verified duration spelled differently", "Trial deploys last 36 hours.", claimcheck.MatchedVerified, "trial", ""},
		{"verified price", "Service is roughly $7.25/mo.", claimcheck.MatchedVerified, "service", ""},
		{"verified free site", "Your first Docs site is free.", claimcheck.MatchedVerified, "site", ""},
		{"verified negation", "Acme Cloud is not open source.", claimcheck.MatchedVerified, "bsl", ""},
		{"verified absolute", "Acme Bot never merges.", claimcheck.MatchedVerified, "merge", ""},
		{"unverified claim", "Postgres is $15/mo.", claimcheck.MatchedUnverified, "draft", claimcheck.ReasonUnverified},
		{"retired claim", "Cloud machines from $12/mo.", claimcheck.MatchedUnverified, "old", claimcheck.ReasonRetired},
		{"recheck overdue", "The free tier includes 20 GiB of traffic.", claimcheck.MatchedUnverified, "stale", claimcheck.ReasonRecheckOverdue},
		{"recheck in the future is fine", "Credit top-ups carry a 3% service fee.", claimcheck.MatchedVerified, "fresh", ""},
		{"unregistered price", "Everything is $99 a month.", claimcheck.UnmatchedRisky, "", ""},
		{"same value, different subject", "Postgres is free.", claimcheck.UnmatchedRisky, "", ""},
		{"affirming what the claim negates", "Acme Cloud is open source.", claimcheck.UnmatchedRisky, "", ""},
		{"unregistered competitor", "Better than Cursor.", claimcheck.UnmatchedRisky, "", ""},
		{"different number", "Trial deploys last 48 hours.", claimcheck.UnmatchedRisky, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := claimcheck.Check(claimcheck.Rules{}, tc.text, registry, now)
			if len(fs) == 0 {
				t.Fatalf("no findings for %q", tc.text)
			}
			f := fs[0]
			if tc.kind == claimcheck.UnmatchedRisky {
				for _, f := range fs {
					if f.Kind != claimcheck.UnmatchedRisky {
						t.Fatalf("%q: %+v, want all unmatched", tc.text, f)
					}
				}
				return
			}
			if f.Kind != tc.kind || f.Claim == nil || f.Claim.ID != tc.claim || f.Reason != tc.reason {
				t.Fatalf("%q: got kind=%s claim=%v reason=%q, want kind=%s claim=%s reason=%q", tc.text, f.Kind, f.Claim, f.Reason, tc.kind, tc.claim, tc.reason)
			}
		})
	}
}

func TestCheckQuotingAClaimDoesNotCoverOtherMentionsInTheSentence(t *testing.T) {
	registry := []claimcheck.Claim{claim("apache", "Acme Studio is Apache-2.0", claimcheck.StatusVerified)}
	fs := claimcheck.Check(claimcheck.Rules{}, "Acme Studio is Apache-2.0 and costs $5.", registry, now)
	if !claimcheck.Blocking(fs) {
		t.Fatalf("an unregistered price must block: %+v", fs)
	}
	var price *claimcheck.Finding
	for i := range fs {
		if fs[i].Mention.Text == "$5" {
			price = &fs[i]
		}
	}
	if price == nil || price.Kind != claimcheck.UnmatchedRisky {
		t.Fatalf("the $5 must be unmatched: %+v", fs)
	}
	// Apart from the clause that says more, the quote still vouches.
	fs = claimcheck.Check(claimcheck.Rules{}, "Acme Studio is Apache-2.0. It costs $5.", registry, now)
	if len(fs) != 2 || fs[0].Kind != claimcheck.MatchedVerified || fs[1].Kind != claimcheck.UnmatchedRisky {
		t.Fatalf("got %+v", fs)
	}
}

func TestCheckShortClaimDoesNotCoverUnrelatedSentences(t *testing.T) {
	registry := []claimcheck.Claim{claim("x", "5", claimcheck.StatusVerified)}
	fs := claimcheck.Check(claimcheck.Rules{}, "We shipped 15 releases.", registry, now)
	if len(fs) != 1 || fs[0].Kind != claimcheck.UnmatchedRisky {
		t.Fatalf("got %+v", fs)
	}
}

// referenceRegistry is a verified registry for the fictional Acme Cloud
// product.
func referenceRegistry() []claimcheck.Claim {
	texts := []string{
		"Acme Studio is Apache-2.0",
		"trial deploys stay live for 36 hours, then are suspended and deleted 10 days later unless upgraded",
		"Docs sites are free, any number of them, within one team per account",
		"$4 monthly minimum for hosting in a month with usage; a month with no usage is never billed",
		"a hosted service of 0.5 vCPU and 2 GiB is about $6.10/mo, estimated at 720 hours a month",
		"a hosted service of 1 vCPU and 4 GiB is about $12.20/mo, estimated at 720 hours a month",
		"managed Postgres is about $16.40/mo, estimated at 720 hours a month",
		"cloud machines start from $24.90/mo",
		"Acme Cloud is BSL 1.1 source-available, not OSI open source",
		"Acme Bot never merges",
		"Acme Bot's security review inspects source only: it is not a stress test and never contacts or pings servers",
	}
	out := make([]claimcheck.Claim, len(texts))
	for i, t := range texts {
		out[i] = claim("ref", t, claimcheck.StatusVerified)
	}
	return out
}

// Absolutes and look-alike prices must not ride on a reference claim that merely
// shares a word or a number with them.
func TestReferenceClaimsDoNotCoverOverreachingSentences(t *testing.T) {
	reg := referenceRegistry()
	for _, text := range []string{
		"Acme Bot never fails a build.",
		"Our security review never misses a vulnerability.",
		"Docs hosting for every app is free.",
		"Hosting costs $4 a month.",
		"Deleted apps are restored within 10 days.",
		"Acme Bot always merges.",
		"Every Docs site is free.",
		"Unlimited hosting from $24.90/mo.",
	} {
		fs := claimcheck.Check(claimcheck.Rules{}, text, reg, now)
		if len(fs) == 0 {
			t.Errorf("%q: no mentions found", text)
			continue
		}
		if !claimcheck.Blocking(fs) {
			t.Errorf("%q must block against the reference registry, got %+v", text, fs)
		}
	}
}

// A word-for-word quote vouches for the quoted words and for a sentence that
// adds nothing outside the claim. A tail that extends the claim to something
// else ("and so is Acme Cloud", ", with GPUs included") is not vouched for, with
// or without a comma.
func TestQuotingAClaimDoesNotVouchForWhatTheSentenceAddsToIt(t *testing.T) {
	reg := []claimcheck.Claim{
		claim("apache", "Acme Studio is Apache-2.0", claimcheck.StatusVerified),
		claim("merge", "Acme Bot never merges", claimcheck.StatusVerified),
		claim("site", "One Docs site per workspace is free", claimcheck.StatusVerified),
		claim("gpu", "Cloud machines start from $24.90/mo", claimcheck.StatusVerified),
		claim("bsl", "Acme Cloud is BSL 1.1", claimcheck.StatusVerified),
	}
	for _, text := range []string{
		"Acme Studio is Apache-2.0 and so is Acme Cloud.",
		"Acme Studio is Apache-2.0, and Acme Cloud too.",
		"Acme Bot never merges or touches prod.",
		"One Docs site per workspace is free and so is Postgres.",
		"Cloud machines start from $24.90/mo, with GPUs included.",
	} {
		fs := claimcheck.Check(claimcheck.Rules{}, text, reg, now)
		if len(fs) == 0 {
			t.Errorf("%q: no mentions found", text)
			continue
		}
		if !claimcheck.Blocking(fs) {
			t.Errorf("%q must block, got %+v", text, fs)
		}
	}
	for _, text := range []string{
		"Acme Studio is Apache-2.0.",
		"Acme Studio is Apache-2.0, Acme Cloud is BSL 1.1.",
		"Acme Bot never merges.",
		"One Docs site per workspace is free.",
		"Cloud machines start from $24.90/mo.",
	} {
		fs := claimcheck.Check(claimcheck.Rules{}, text, reg, now)
		if len(fs) == 0 || claimcheck.Blocking(fs) {
			t.Errorf("%q should pass, got %+v", text, fs)
		}
	}
}

func TestReferenceClaimsStillCoverTheirOwnFacts(t *testing.T) {
	reg := referenceRegistry()
	for _, text := range []string{
		"Acme Bot never merges.",
		"Docs sites are free, any number of them, within one team per account.",
		"Trial deploys stay live for 36 hours.",
		"Cloud machines start from $24.90/mo.",
		"Acme Cloud is BSL 1.1 source-available, not OSI open source.",
		"Acme Studio is Apache-2.0.",
	} {
		fs := claimcheck.Check(claimcheck.Rules{}, text, reg, now)
		if len(fs) == 0 || claimcheck.Blocking(fs) {
			t.Errorf("%q should be covered by the reference registry, got %+v", text, fs)
		}
	}
}

func TestExtractInlineCode(t *testing.T) {
	got := kinds(claimcheck.Rules{}.Extract("It is `free` and `never` fails, costs `$99`."))
	want := []string{"free:free", "absolute:never", "price:$99"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestExtractSpelledOutNumbers(t *testing.T) {
	tests := []struct{ text, want string }{
		{"It is ten times faster.", "multiplier:ten times"},
		{"Deploys in five minutes.", "duration:five minutes"},
		{"Works ninety-nine percent of the time.", "percent:ninety-nine percent"},
		{"Costs twenty dollars.", "price:twenty dollars"},
		{"Lasts thirty-six hours.", "duration:thirty-six hours"},
	}
	for _, tc := range tests {
		got := kinds(claimcheck.Rules{}.Extract(tc.text))
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%q: got %v want [%s]", tc.text, got, tc.want)
		}
	}
}

func TestSpelledNumbersMatchTheirDigitClaims(t *testing.T) {
	reg := []claimcheck.Claim{
		claim("t", "trial deploys stay live for 36 hours", claimcheck.StatusVerified),
		claim("p", "Acme Cloud never fails 99% of builds", claimcheck.StatusVerified),
	}
	if fs := claimcheck.Check(claimcheck.Rules{}, "Trial deploys stay live for thirty-six hours.", reg, now); claimcheck.Blocking(fs) || len(fs) != 1 {
		t.Fatalf("spelled-out 36 hours should match its claim: %+v", fs)
	}
	if fs := claimcheck.Check(claimcheck.Rules{}, "Trial deploys stay live for thirty hours.", reg, now); !claimcheck.Blocking(fs) {
		t.Fatalf("thirty hours must block: %+v", fs)
	}
}

func TestExtractFalsePositivesDoNotFlag(t *testing.T) {
	for _, text := range []string{
		"We continue to render the page with linear gradients.",
		"Factory settings are restored. Bolt the shelf down.",
		"Follow best practices for the first step.",
		"Most people agree.",
		"Updated Nov 19, 2026 and on 3 March 2026.",
		"Deploy to AWS or Kubernetes.",
		"Feel free to ask.",
		"We run on Vercel.",
		"Use Cursor daily.",
	} {
		if got := kinds(claimcheck.Rules{}.Extract(text)); len(got) != 0 {
			t.Errorf("%q should not flag, got %v", text, got)
		}
	}
}

func TestExtractStillFlagsComparisons(t *testing.T) {
	for _, text := range []string{
		"Better than Vercel.",
		"Cheaper than Heroku for small apps.",
		"Cursor vs Windsurf.",
		"An alternative to Netlify.",
	} {
		found := false
		for _, m := range (claimcheck.Rules{}).Extract(text) {
			if m.Kind == claimcheck.KindCompetitor {
				found = true
			}
		}
		if !found {
			t.Errorf("%q should flag a competitor", text)
		}
	}
	// "the best" and "the first ever" still flag.
	for _, text := range []string{"The best deploy tool.", "The first ever agent host.", "Most reliable host."} {
		if got := kinds(claimcheck.Rules{}.Extract(text)); len(got) == 0 {
			t.Errorf("%q should flag a superlative", text)
		}
	}
}

func TestCheckLargeUnbrokenInputHasBoundedContexts(t *testing.T) {
	chunk := "we ship 10x faster and it is free for every app and costs $99 per month with never any limits "
	text := strings.Repeat(chunk, 300) // ~28 KB, no sentence break
	mentions := (claimcheck.Rules{}).Extract(text)
	if len(mentions) != 4*300 {
		t.Fatalf("got %d mentions, want %d", len(mentions), 4*300)
	}
	for _, m := range mentions {
		// The matcher must not process the entire unbroken input per mention.
		// Allow the mention and its trimmed trailing whitespace as well as the window.
		if len(m.Context) > 324+len(m.Text) {
			t.Fatalf("unbounded context: %d bytes for %q", len(m.Context), m.Text)
		}
		if len(m.Siblings) > len(m.Context) {
			t.Fatalf("unbounded siblings: %d for %d-byte context", len(m.Siblings), len(m.Context))
		}
	}
	fs := claimcheck.Match(claimcheck.Rules{}, mentions, referenceRegistry(), now)
	if len(fs) != len(mentions) || !claimcheck.Blocking(fs) {
		t.Fatalf("expected a finding for every mention and at least one blocking finding; got %d", len(fs))
	}
}

// BenchmarkCheckLargeUnbrokenInput tracks runtime separately from the
// deterministic bounded-context test, without a CI wall-clock deadline.
func BenchmarkCheckLargeUnbrokenInput(b *testing.B) {
	text := strings.Repeat("we ship 10x faster and it is free for every app and costs $99 per month with never any limits ", 300)
	reg := referenceRegistry()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		claimcheck.Check(claimcheck.Rules{}, text, reg, now)
	}
}

func TestCheckVerifiedOutranksStaleDuplicate(t *testing.T) {
	registry := []claimcheck.Claim{
		claim("old", "Acme Studio is Apache-2.0", claimcheck.StatusUnverified),
		claim("new", "Acme Studio is Apache-2.0 licensed", claimcheck.StatusVerified),
	}
	fs := claimcheck.Check(claimcheck.Rules{}, "Acme Studio is Apache-2.0.", registry, now)
	if len(fs) != 1 || fs[0].Kind != claimcheck.MatchedVerified || fs[0].Claim.ID != "new" {
		t.Fatalf("got %+v", fs)
	}
}

func TestBlocking(t *testing.T) {
	registry := []claimcheck.Claim{claim("apache", "Acme Studio is Apache-2.0", claimcheck.StatusVerified)}
	if claimcheck.Blocking(claimcheck.Check(claimcheck.Rules{}, "Acme Studio is Apache-2.0.", registry, now)) {
		t.Fatal("a fully verified asset must not block")
	}
	if !claimcheck.Blocking(claimcheck.Check(claimcheck.Rules{}, "Acme Studio is Apache-2.0 and costs $5.", registry, now)) {
		t.Fatal("an unmatched price must block")
	}
	if claimcheck.Blocking(nil) {
		t.Fatal("no findings must not block")
	}
}
