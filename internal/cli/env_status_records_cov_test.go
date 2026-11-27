package cli

import "testing"

func TestProvenanceLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   envStatusProvenance
		want string
	}{
		{"release only", envStatusProvenance{Release: "v1"}, "v1"},
		{"branch and commit are shortened", envStatusProvenance{Release: "v1.2.0", Branch: "main", Commit: "abcdef0123456789"}, "v1.2.0, from main@abcdef0"},
		{"commit without branch", envStatusProvenance{Release: "v1", Commit: "abcdef0123"}, "v1, from abcdef0"},
		{"a branch with no commit says nothing about the source", envStatusProvenance{Release: "v1", Branch: "main"}, "v1"},
		{"tag", envStatusProvenance{Release: "v1", Tag: "v1.0.0"}, "v1, tag v1.0.0"},
		{"dirty is reported, not asserted", envStatusProvenance{Release: "v1", Dirty: true}, "v1, dirty tree (reported by forge at build time)"},
		{"everything", envStatusProvenance{Release: "v2", Branch: "b", Commit: "1234567890", Tag: "t", Dirty: true, ForgeVersion: "v1.11.0"},
			"v2, from b@1234567, tag t, dirty tree (reported by forge at build time), forge v1.11.0"},
	} {
		if got := provenanceLine(tc.in); got != tc.want {
			t.Errorf("%s: provenanceLine = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestBoundByLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   envStatusBinding
		want string
	}{
		{"nothing recorded", envStatusBinding{}, "(no promotion detail recorded)"},
		{"kind only", envStatusBinding{Kind: "promote"}, "promote"},
		{"a promotion between envs", envStatusBinding{Kind: "promote", FromEnv: "staging", PromotedBy: "ana", PromotedAt: "2026-11-01T10:00:00Z", ApprovedBy: "bo"},
			"promote, from staging, by ana, at 2026-11-01T10:00:00Z, approved by bo"},
		{"supersede and note", envStatusBinding{Kind: "promote", SupersededInFlight: true, Note: "hotfix"}, "promote, superseded a rollout in flight, note: hotfix"},
	} {
		if got := boundByLine(tc.in); got != tc.want {
			t.Errorf("%s: boundByLine = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSessionLineAndShortID(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   envStatusSession
		want string
	}{
		{"worktree only", envStatusSession{Worktree: "wt", State: sessionStateStopped}, "wt"},
		{"branch at commit, dirty, last seen while running", envStatusSession{Worktree: "wt", Branch: "feat", Commit: "abcdef0123", Dirty: true, State: sessionStateRunning, LastSeenAt: "10:00"},
			"wt, feat@abcdef0, dirty, last seen 10:00"},
		{"branch without commit", envStatusSession{Worktree: "wt", Branch: "feat", State: sessionStateQuiet}, "wt, feat"},
		{"a stopped session has no last-seen", envStatusSession{Worktree: "wt", State: sessionStateStopped, LastSeenAt: "10:00"}, "wt"},
	} {
		if got := sessionLine(tc.in); got != tc.want {
			t.Errorf("%s: sessionLine = %q, want %q", tc.name, got, tc.want)
		}
	}
	for in, want := range map[string]string{"": "", "abc": "abc", "abcdefg": "abcdefg", "abcdefgh": "abcdefg"} {
		if got := shortID(in); got != want {
			t.Errorf("shortID(%q) = %q, want %q", in, got, want)
		}
	}
	if got := convergenceWhen(envStatusConvergence{}); got != "" {
		t.Errorf("no observation time -> nothing, got %q", got)
	}
	if got := convergenceWhen(envStatusConvergence{ObservedAt: "T"}); got != ", observed T" {
		t.Errorf("convergenceWhen = %q; the time is labelled as an observation", got)
	}
	if got := revisionDigestOf("latest@sha256:abc"); got != "sha256:abc" {
		t.Errorf("revisionDigestOf = %q", got)
	}
	if got := revisionDigestOf("rev-7"); got != "rev-7" {
		t.Errorf("a revision with no digest is returned verbatim, got %q", got)
	}
}
