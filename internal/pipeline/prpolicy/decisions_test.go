package prpolicy

import (
	"reflect"
	"testing"

	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

func TestRequiredDecisions(t *testing.T) {
	proto := config.OwnerApproval{Kind: config.ApproveBreakingProto, Paths: []string{"proto/**"}, WhenCmd: "scripts/buf-breaking.sh"}
	migr := config.OwnerApproval{Kind: config.ApproveBreaksRollback, Paths: []string{"db/migrations/**"}}
	prodFix := config.OwnerApproval{Kind: config.ApproveProdFix, When: "incident.source in ['prod_alert','obs_issue']"}
	cmds := func(m map[int]bool) ApprovalConditions { return ApprovalConditions{WhenCmd: m} }
	cases := []struct {
		name      string
		paths     []string
		source    IncidentSource
		approvals []config.OwnerApproval
		conds     ApprovalConditions
		want      []RequiredDecision
	}{
		{name: "no entries", paths: []string{"proto/a.proto"}},
		{name: "paths miss", paths: []string{"internal/x.go"}, approvals: []config.OwnerApproval{migr}},
		{name: "paths match", paths: []string{"db/migrations/1_a.up.sql", "internal/x.go"},
			approvals: []config.OwnerApproval{migr},
			want:      []RequiredDecision{{Kind: config.ApproveBreaksRollback, Head: "h1", Entries: []int{0}, Paths: []string{"db/migrations/1_a.up.sql"}}}},
		{name: "paths AND when_cmd true", paths: []string{"proto/a/v1/a.proto"},
			approvals: []config.OwnerApproval{proto}, conds: cmds(map[int]bool{0: true}),
			want: []RequiredDecision{{Kind: config.ApproveBreakingProto, Head: "h1", Entries: []int{0}, Paths: []string{"proto/a/v1/a.proto"}}}},
		{name: "paths AND when_cmd false", paths: []string{"proto/a/v1/a.proto"},
			approvals: []config.OwnerApproval{proto}, conds: cmds(map[int]bool{0: false})},
		{name: "when_cmd true but paths miss", paths: []string{"README.md"},
			approvals: []config.OwnerApproval{proto}, conds: cmds(map[int]bool{0: true})},
		{name: "unevaluated when_cmd fails closed", paths: []string{"proto/a/v1/a.proto"},
			approvals: []config.OwnerApproval{proto},
			want:      []RequiredDecision{{Kind: config.ApproveBreakingProto, Head: "h1", Entries: []int{0}, Paths: []string{"proto/a/v1/a.proto"}, Unevaluated: true}}},
		{name: "no condition applies to every head", paths: []string{"README.md"},
			approvals: []config.OwnerApproval{{Kind: config.ApproveBreaksRollback}},
			want:      []RequiredDecision{{Kind: config.ApproveBreaksRollback, Head: "h1", Entries: []int{0}}}},
		{name: "no condition applies to an empty diff", paths: nil,
			approvals: []config.OwnerApproval{{Kind: config.ApproveBreaksRollback}},
			want:      []RequiredDecision{{Kind: config.ApproveBreaksRollback, Head: "h1", Entries: []int{0}}}},
		{name: "prod fix needs an incident", paths: []string{"x.go"},
			approvals: []config.OwnerApproval{{Kind: config.ApproveProdFix}}},
		{name: "prod fix without when applies to any incident fix", paths: []string{"x.go"}, source: IncidentSourceMain,
			approvals: []config.OwnerApproval{{Kind: config.ApproveProdFix}},
			want:      []RequiredDecision{{Kind: config.ApproveProdFix, Head: "h1", Entries: []int{0}}}},
		{name: "prod fix when true", paths: []string{"x.go"}, source: IncidentSourceProdAlert,
			approvals: []config.OwnerApproval{prodFix}, conds: ApprovalConditions{When: map[int]bool{0: true}},
			want: []RequiredDecision{{Kind: config.ApproveProdFix, Head: "h1", Entries: []int{0}}}},
		{name: "prod fix when false", paths: []string{"x.go"}, source: IncidentSourceFlake,
			approvals: []config.OwnerApproval{prodFix}, conds: ApprovalConditions{When: map[int]bool{0: false}}},
		{name: "prod fix when unevaluated", paths: []string{"x.go"}, source: IncidentSourceProdAlert,
			approvals: []config.OwnerApproval{prodFix},
			want:      []RequiredDecision{{Kind: config.ApproveProdFix, Head: "h1", Entries: []int{0}, Unevaluated: true}}},
		{name: "entries of one kind OR, one decision per kind", paths: []string{"db/migrations/1.up.sql", "internal/db/schema.sql"},
			approvals: []config.OwnerApproval{
				migr,
				{Kind: config.ApproveBreaksRollback, Paths: []string{"internal/db/**"}, WhenCmd: "x"},
				{Kind: config.ApproveBreaksRollback, Paths: []string{"nowhere/**"}},
			},
			conds: cmds(map[int]bool{1: true}),
			want: []RequiredDecision{{Kind: config.ApproveBreaksRollback, Head: "h1", Entries: []int{0, 1},
				Paths: []string{"db/migrations/1.up.sql", "internal/db/schema.sql"}}}},
		{name: "one false entry does not cancel another of its kind", paths: []string{"db/migrations/1.up.sql"},
			approvals: []config.OwnerApproval{
				{Kind: config.ApproveBreaksRollback, Paths: []string{"db/**"}, WhenCmd: "x"},
				migr,
			},
			conds: cmds(map[int]bool{0: false}),
			want:  []RequiredDecision{{Kind: config.ApproveBreaksRollback, Head: "h1", Entries: []int{1}, Paths: []string{"db/migrations/1.up.sql"}}}},
		{name: "kinds come back in ApprovalKinds order", paths: []string{"db/migrations/1.up.sql", "proto/a.proto"},
			source:    IncidentSourceProdAlert,
			approvals: []config.OwnerApproval{prodFix, migr, proto},
			conds:     ApprovalConditions{WhenCmd: map[int]bool{2: true}, When: map[int]bool{0: true}},
			want: []RequiredDecision{
				{Kind: config.ApproveBreakingProto, Head: "h1", Entries: []int{2}, Paths: []string{"proto/a.proto"}},
				{Kind: config.ApproveBreaksRollback, Head: "h1", Entries: []int{1}, Paths: []string{"db/migrations/1.up.sql"}},
				{Kind: config.ApproveProdFix, Head: "h1", Entries: []int{0}},
			}},
		{name: "a malformed glob matches nothing", paths: []string{"a[b"},
			approvals: []config.OwnerApproval{{Kind: config.ApproveBreaksRollback, Paths: []string{"a[b"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RequiredDecisions("h1", tc.paths, tc.source, tc.approvals, tc.conds)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("RequiredDecisions =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}

func TestRequiredDecisionsFromResolvedConfig(t *testing.T) {
	r, err := config.Load([]byte(`version: 1
gate: "task test"
owner_approval:
  - {kind: approve_breaks_rollback, paths: ["db/migrations/**"], when_cmd: "scripts/breaks-rollback.sh"}
  - {kind: approve_prod_fix, when: "incident.source in ['prod_alert','obs_issue']"}
`))
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"db/migrations/20261126_x.up.sql"}
	whenCmd, when := PendingConditions(paths, IncidentSourceObsIssue, r.OwnerApproval)
	if !reflect.DeepEqual(whenCmd, []int{0}) || !reflect.DeepEqual(when, []int{1}) {
		t.Fatalf("PendingConditions = %v, %v; want [0], [1]", whenCmd, when)
	}
	got := RequiredDecisions("abc", paths, IncidentSourceObsIssue, r.OwnerApproval,
		ApprovalConditions{WhenCmd: map[int]bool{0: true}, When: map[int]bool{1: true}})
	want := []RequiredDecision{
		{Kind: config.ApproveBreaksRollback, Head: "abc", Entries: []int{0}, Paths: paths},
		{Kind: config.ApproveProdFix, Head: "abc", Entries: []int{1}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RequiredDecisions =\n%+v\nwant\n%+v", got, want)
	}
}

func TestPendingConditions(t *testing.T) {
	approvals := []config.OwnerApproval{
		{Kind: config.ApproveBreakingProto, Paths: []string{"proto/**"}, WhenCmd: "a"},
		{Kind: config.ApproveBreaksRollback, Paths: []string{"db/**"}, WhenCmd: "b"},
		{Kind: config.ApproveBreaksRollback, WhenCmd: "c", When: "true"},
		{Kind: config.ApproveProdFix, When: "incident.source == 'prod_alert'"},
	}
	whenCmd, when := PendingConditions([]string{"proto/x.proto"}, "", approvals)
	if !reflect.DeepEqual(whenCmd, []int{0, 2}) || !reflect.DeepEqual(when, []int{2}) {
		t.Errorf("no incident: PendingConditions = %v, %v; want [0 2], [2]", whenCmd, when)
	}
	whenCmd, when = PendingConditions([]string{"README.md"}, IncidentSourceProdAlert, approvals)
	if !reflect.DeepEqual(whenCmd, []int{2}) || !reflect.DeepEqual(when, []int{2, 3}) {
		t.Errorf("incident fix: PendingConditions = %v, %v; want [2], [2 3]", whenCmd, when)
	}
}

func TestUnmetDecisions(t *testing.T) {
	req := []RequiredDecision{
		{Kind: config.ApproveBreakingProto, Head: "h2"},
		{Kind: config.ApproveBreaksRollback, Head: "h2"},
	}
	cases := []struct {
		name     string
		approved []ApprovedDecision
		want     []config.ApprovalKind
	}{
		{name: "none approved", want: []config.ApprovalKind{config.ApproveBreakingProto, config.ApproveBreaksRollback}},
		{name: "one approved on this head", approved: []ApprovedDecision{{config.ApproveBreakingProto, "h2"}},
			want: []config.ApprovalKind{config.ApproveBreaksRollback}},
		{name: "approval of an earlier head is void", approved: []ApprovedDecision{{config.ApproveBreakingProto, "h1"}, {config.ApproveBreaksRollback, "h2"}},
			want: []config.ApprovalKind{config.ApproveBreakingProto}},
		{name: "another kind does not count", approved: []ApprovedDecision{{config.ApproveProdFix, "h2"}},
			want: []config.ApprovalKind{config.ApproveBreakingProto, config.ApproveBreaksRollback}},
		{name: "all met", approved: []ApprovedDecision{{config.ApproveBreaksRollback, "h2"}, {config.ApproveBreakingProto, "h2"}}},
	}
	for _, tc := range cases {
		var got []config.ApprovalKind
		for _, d := range UnmetDecisions(req, tc.approved) {
			got = append(got, d.Kind)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: UnmetDecisions = %v, want %v", tc.name, got, tc.want)
		}
	}
	if got := UnmetDecisions([]RequiredDecision{{Kind: config.ApproveBreakingProto}}, []ApprovedDecision{{Kind: config.ApproveBreakingProto}}); len(got) != 1 {
		t.Errorf("a requirement with no head is never met, got %v", got)
	}
}
