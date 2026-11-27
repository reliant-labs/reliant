package prpolicy

import (
	"reflect"
	"testing"
)

func TestSupersedesLinks(t *testing.T) {
	cases := []struct {
		body string
		want []int
	}{
		{"", nil},
		{"Supersedes #12", []int{12}},
		{"supersedes: #12, #13 and #14 & #15", []int{12, 13, 14, 15}},
		{"This supersedes #7.\n\nAlso Supersedes #7 and #8", []int{7, 8}},
		{"Fixes #12", nil},
		{"superseded by #12", nil},
		{"supersedes 12", nil},
		{"supersedes #0", nil},
	}
	for _, tc := range cases {
		if got := SupersedesLinks(tc.body); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("SupersedesLinks(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}

func TestDetectSupersession(t *testing.T) {
	base := PRSummary{Repo: "acme/app", Number: 20, Author: "ana", State: PRStateOpen, BaseRef: "main",
		HeadSHA: "h20", PatchID: "p20", Commits: []string{"c1", "h20"}}
	with := func(f func(*PRSummary)) PRSummary { p := base; f(&p); return p }
	other := func(f func(*PRSummary)) PRSummary {
		o := PRSummary{Repo: "acme/app", Number: 10, Author: "ana", State: PRStateOpen, BaseRef: "main",
			HeadSHA: "h10", PatchID: "p10", Commits: []string{"h10"}}
		f(&o)
		return o
	}
	cases := []struct {
		name     string
		pr       PRSummary
		others   []PRSummary
		want     Supersession
		evidence []EvidenceKind
	}{
		{name: "no others", pr: base, want: Supersession{Kind: SupersessionNone}},
		{name: "merged same patch-id", pr: base,
			others: []PRSummary{other(func(o *PRSummary) { o.State = PRStateMerged; o.PatchID = "p20" })},
			want:   Supersession{SupersessionSuperseded, 10}, evidence: []EvidenceKind{EvidencePatchID}},
		{name: "merged contains head", pr: base,
			others: []PRSummary{other(func(o *PRSummary) { o.State = PRStateMerged; o.Commits = []string{"c1", "h20", "h10"} })},
			want:   Supersession{SupersessionSuperseded, 10}, evidence: []EvidenceKind{EvidenceAncestry}},
		{name: "merged same head", pr: base,
			others: []PRSummary{other(func(o *PRSummary) { o.State = PRStateMerged; o.HeadSHA = "h20"; o.Commits = []string{"h20"} })},
			want:   Supersession{SupersessionSuperseded, 10}, evidence: []EvidenceKind{EvidenceSameHead}},
		{name: "merged but reverted is no evidence", pr: base,
			others: []PRSummary{other(func(o *PRSummary) { o.State = PRStateMerged; o.PatchID = "p20"; o.Reverted = true })},
			want:   Supersession{Kind: SupersessionNone}},
		{name: "closed unmerged is ignored", pr: base,
			others: []PRSummary{other(func(o *PRSummary) { o.State = PRStateClosed; o.PatchID = "p20" })},
			want:   Supersession{Kind: SupersessionNone}},
		{name: "other base branch (backport) is ignored", pr: base,
			others: []PRSummary{other(func(o *PRSummary) { o.State = PRStateMerged; o.PatchID = "p20"; o.BaseRef = "release/v1" })},
			want:   Supersession{Kind: SupersessionNone}},
		{name: "other repo is ignored", pr: base,
			others: []PRSummary{other(func(o *PRSummary) { o.State = PRStateMerged; o.PatchID = "p20"; o.Repo = "acme/other" })},
			want:   Supersession{Kind: SupersessionNone}},
		{name: "repo compared case-insensitively", pr: base,
			others: []PRSummary{other(func(o *PRSummary) { o.State = PRStateMerged; o.PatchID = "p20"; o.Repo = "Acme/App" })},
			want:   Supersession{SupersessionSuperseded, 10}, evidence: []EvidenceKind{EvidencePatchID}},
		{name: "empty patch-id is never evidence", pr: with(func(p *PRSummary) { p.PatchID = "" }),
			others: []PRSummary{other(func(o *PRSummary) { o.State = PRStateMerged; o.PatchID = "" })},
			want:   Supersession{Kind: SupersessionNone}},
		{name: "same number is itself", pr: base,
			others: []PRSummary{with(func(p *PRSummary) { p.State = PRStateMerged })},
			want:   Supersession{Kind: SupersessionNone}},
		{name: "open link by same author", pr: base,
			others: []PRSummary{other(func(o *PRSummary) { o.Number = 30; o.Body = "Supersedes #20" })},
			want:   Supersession{SupersessionSuperseded, 30}, evidence: []EvidenceKind{EvidenceLink}},
		{name: "open link author compared case-insensitively", pr: base,
			others: []PRSummary{other(func(o *PRSummary) { o.Number = 30; o.Author = "Ana"; o.Body = "Supersedes #20" })},
			want:   Supersession{SupersessionSuperseded, 30}, evidence: []EvidenceKind{EvidenceLink}},
		{name: "link by another author is no evidence", pr: base,
			others: []PRSummary{other(func(o *PRSummary) { o.Number = 30; o.Author = "mallory"; o.Body = "Supersedes #20" })},
			want:   Supersession{Kind: SupersessionNone}},
		{name: "mutual open links prove nothing", pr: with(func(p *PRSummary) { p.Body = "supersedes #30" }),
			others: []PRSummary{other(func(o *PRSummary) { o.Number = 30; o.Body = "Supersedes #20" })},
			want:   Supersession{Kind: SupersessionNone}},
		{name: "title-only match is no evidence", pr: base,
			others: []PRSummary{other(func(o *PRSummary) { o.Body = "same title, different change" })},
			want:   Supersession{Kind: SupersessionNone}},
		{name: "open contains head is a stack, not supersession", pr: base,
			others: []PRSummary{other(func(o *PRSummary) { o.Number = 30; o.Commits = []string{"c1", "h20", "h30"} })},
			want:   Supersession{Kind: SupersessionNone}},
		{name: "duplicate of older open same patch-id", pr: base,
			others: []PRSummary{other(func(o *PRSummary) { o.PatchID = "p20" })},
			want:   Supersession{SupersessionDuplicate, 10}, evidence: []EvidenceKind{EvidencePatchID}},
		{name: "duplicate of older open same head", pr: base,
			others: []PRSummary{other(func(o *PRSummary) { o.HeadSHA = "h20" })},
			want:   Supersession{SupersessionDuplicate, 10}, evidence: []EvidenceKind{EvidenceSameHead}},
		{name: "the older of two duplicates stays open", pr: base,
			others: []PRSummary{other(func(o *PRSummary) { o.Number = 30; o.PatchID = "p20" })},
			want:   Supersession{Kind: SupersessionNone}},
		{name: "own supersedes link keeps the newer open", pr: with(func(p *PRSummary) { p.Body = "Supersedes #10" }),
			others: []PRSummary{other(func(o *PRSummary) { o.PatchID = "p20" })},
			want:   Supersession{Kind: SupersessionNone}},
		{name: "a PR that is not open is never judged", pr: with(func(p *PRSummary) { p.State = PRStateMerged }),
			others: []PRSummary{other(func(o *PRSummary) { o.State = PRStateMerged; o.PatchID = "p20" })},
			want:   Supersession{Kind: SupersessionNone}},
		{name: "merged wins over open link and duplicate", pr: base,
			others: []PRSummary{
				other(func(o *PRSummary) { o.Number = 5; o.PatchID = "p20" }),
				other(func(o *PRSummary) { o.Number = 40; o.Body = "supersedes #20" }),
				other(func(o *PRSummary) { o.Number = 50; o.State = PRStateMerged; o.PatchID = "p20" }),
			},
			want: Supersession{SupersessionSuperseded, 50}, evidence: []EvidenceKind{EvidencePatchID}},
		{name: "open superseded wins over duplicate", pr: base,
			others: []PRSummary{
				other(func(o *PRSummary) { o.Number = 5; o.PatchID = "p20" }),
				other(func(o *PRSummary) { o.Number = 40; o.Body = "supersedes #20" }),
			},
			want: Supersession{SupersessionSuperseded, 40}, evidence: []EvidenceKind{EvidenceLink}},
		{name: "lowest number among merged", pr: base,
			others: []PRSummary{
				other(func(o *PRSummary) { o.Number = 15; o.State = PRStateMerged; o.PatchID = "p20" }),
				other(func(o *PRSummary) { o.Number = 12; o.State = PRStateMerged; o.PatchID = "p20" }),
			},
			want: Supersession{SupersessionSuperseded, 12}, evidence: []EvidenceKind{EvidencePatchID}},
		{name: "every fact against the chosen PR is cited", pr: base,
			others: []PRSummary{other(func(o *PRSummary) {
				o.State = PRStateMerged
				o.PatchID = "p20"
				o.Body = "Supersedes #20"
				o.Commits = []string{"c1", "h20", "h10"}
			})},
			want: Supersession{SupersessionSuperseded, 10}, evidence: []EvidenceKind{EvidencePatchID, EvidenceLink, EvidenceAncestry}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ev := DetectSupersession(tc.pr, tc.others)
			if got != tc.want {
				t.Fatalf("DetectSupersession = %+v, want %+v", got, tc.want)
			}
			var kinds []EvidenceKind
			for _, e := range ev {
				kinds = append(kinds, e.Kind)
				if e.PR != got.By || e.Detail == "" {
					t.Errorf("evidence %+v does not cite #%d with a detail", e, got.By)
				}
			}
			if !reflect.DeepEqual(kinds, tc.evidence) {
				t.Errorf("evidence kinds = %v, want %v", kinds, tc.evidence)
			}
		})
	}
}
