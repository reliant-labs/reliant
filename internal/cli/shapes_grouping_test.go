// Copyright (c) 2025 Reliant Labs
package cli

import "testing"

// TestGrepBranches pins how a --grep pattern is split for per-entity grouping.
//
// The command already accepted a regex, so `--grep 'Invoice|Payment'` always
// worked — and a measured fan-out still asked one entity per turn (11 turns of
// --grep Property then --grep Customer, the old grep loop wearing a new
// command's name). A flat merged list gave the caller no reason to prefer the
// single call. Grouping is what makes the batched form the easier one to read.
func TestGrepBranches(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pattern string
		want    []string
	}{
		{"empty pattern groups nothing", "", nil},
		{"single word is one branch", "Estimate", []string{"Estimate"}},
		{"alternation splits", "Invoice|Payment", []string{"Invoice", "Payment"}},
		{"alternation trims spaces", "Invoice | Payment", []string{"Invoice", "Payment"}},
		{"three branches", "Job|Crew|Material", []string{"Job", "Crew", "Material"}},

		// A pattern doing something STRUCTURAL must stay whole: splitting
		// `List(Invoice|Payment)s` on | yields "List(Invoice" and "Payment)s",
		// neither of which matches anything, so every shape would fall into
		// "other" and the grouping would be actively misleading.
		{"grouped alternation stays whole", "List(Invoice|Payment)s", []string{"List(Invoice|Payment)s"}},
		{"anchors stay whole", "^Invoice|Payment$", []string{"^Invoice|Payment$"}},
		{"empty branch stays whole", "Invoice||Payment", []string{"Invoice||Payment"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := grepBranches(tc.pattern)
			if len(got) != len(tc.want) {
				t.Fatalf("grepBranches(%q) = %v, want %v", tc.pattern, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("branch %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestBranchIndex pins the bucket a shape lands in. A shape matching no branch
// must land in the trailing "other" bucket rather than being dropped — a
// detail-only hit (the pattern matched in=/out= text, not the name) is still a
// real answer, and a grouped view that silently loses rows is worse than an
// ungrouped one.
func TestBranchIndex(t *testing.T) {
	branches := []string{"Invoice", "Payment"}

	if got := branchIndex(branches, shape{Name: "ListInvoices"}); got != 0 {
		t.Errorf("ListInvoices -> branch %d, want 0 (Invoice)", got)
	}
	if got := branchIndex(branches, shape{Name: "RecordPayment"}); got != 1 {
		t.Errorf("RecordPayment -> branch %d, want 1 (Payment)", got)
	}
	// Case-insensitive, matching the (?i) the command compiles with.
	if got := branchIndex(branches, shape{Name: "invoices"}); got != 0 {
		t.Errorf("invoices (lowercase table name) -> branch %d, want 0", got)
	}
	if got := branchIndex(branches, shape{Name: "Estimate"}); got != len(branches) {
		t.Errorf("an unmatched name must fall into the trailing bucket, got %d", got)
	}
}
