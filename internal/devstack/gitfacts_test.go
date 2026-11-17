package devstack

import (
	"testing"
)

func TestSanitize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"feat/up-generate", "feat-up-generate"},
		{"Feat/Up_Generate", "feat-up-generate"},
		{"  spaces  ", "spaces"},
		{"chore/bump-forge-93067142", "chore-bump-forge-9306714"}, // bounded to 24 chars
		{"---weird---", "weird"},
		{"!!!", ""},
		{"UPPER", "upper"},
		{"a.b.c", "a-b-c"},
		{"", ""},
	}
	for _, c := range cases {
		if got := Sanitize(c.in); got != c.want {
			t.Errorf("Sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
		// Sanitize is idempotent: re-sanitizing a sanitized value is a no-op.
		if got := Sanitize(Sanitize(c.in)); got != c.want {
			t.Errorf("Sanitize not idempotent for %q", c.in)
		}
	}
}

func TestOptionsDArgs(t *testing.T) {
	// The default stack (no worktree, no branch) pushes NO options — the
	// byte-identical render contract.
	if d := (Options{}).DArgs(); d != nil {
		t.Errorf("default stack must emit no -D args, got %v", d)
	}
	// Only worktree set → only that arg, QUOTED.
	if d := (Options{Worktree: "wt-feat"}).DArgs(); len(d) != 1 || d[0] != `worktree="wt-feat"` {
		t.Errorf("worktree-only DArgs = %v, want [worktree=\"wt-feat\"]", d)
	}
	// Both set → both args, in worktree,branch order, QUOTED.
	got := Options{Worktree: "wt-feat", Branch: "feature-x"}.DArgs()
	want := []string{`worktree="wt-feat"`, `branch="feature-x"`}
	if len(got) != len(want) {
		t.Fatalf("DArgs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("DArgs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// An all-digit fact must stay a str (quoted), never coerced to int.
	if d := (Options{Branch: "12345"}).DArgs(); d[0] != `branch="12345"` {
		t.Errorf("all-digit branch must be quoted str, got %q", d[0])
	}
}

func TestActiveDefaultIsByteIdentical(t *testing.T) {
	// The process-global default (never SetActive'd) emits no args, so any
	// command that doesn't opt into a dev stack renders exactly as before.
	SetActive(Options{})
	if d := ActiveDArgs(); d != nil {
		t.Errorf("unset active options must emit no -D args, got %v", d)
	}
	SetActive(Options{Worktree: "x"})
	if d := ActiveDArgs(); len(d) != 1 {
		t.Errorf("active worktree must emit 1 -D arg, got %v", d)
	}
	SetActive(Options{}) // reset for other tests
}
