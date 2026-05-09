package worktreepath

import (
	"path"
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"  ", ""},
		{"My Project", "my-project"},
		{"Hello, World!", "hello-world"},
		{"___foo___bar___", "foo-bar"},
		{"already-fine", "already-fine"},
		{"Über cool", "ber-cool"}, // non-ASCII collapsed to a separator
		{strings.Repeat("a", 100), strings.Repeat("a", 40)},
	}
	for _, tc := range cases {
		if got := slug(tc.in); got != tc.want {
			t.Errorf("slug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWorkspaceDirName(t *testing.T) {
	dir := WorkspaceDirName("My Project", "feature/login-flow")
	parts := strings.Split(dir, "/")
	if len(parts) != 2 {
		t.Fatalf("expected 2 path segments, got %q", dir)
	}
	if parts[0] != "my-project" {
		t.Errorf("project segment = %q, want %q", parts[0], "my-project")
	}
	// worktree segment is `<slug>-<8-char-hex>`. Don't pin the suffix; just
	// confirm the name slug is the prefix and there's a trailing disambiguator.
	if !strings.HasPrefix(parts[1], "feature-login-flow-") {
		t.Errorf("worktree segment = %q, want prefix %q", parts[1], "feature-login-flow-")
	}
	if len(parts[1]) <= len("feature-login-flow-") {
		t.Errorf("worktree segment %q is missing a unique suffix", parts[1])
	}
}

func TestWorkspaceDirName_EmptyInputsFallBack(t *testing.T) {
	dir := WorkspaceDirName("", "")
	if !strings.HasPrefix(dir, "workspace"+string('/')+"worktree-") {
		t.Errorf("dir for empty inputs = %q, want fallback %q", dir, "workspace/worktree-…")
	}
}

func TestWorkspaceDirName_Unique(t *testing.T) {
	a := WorkspaceDirName("p", "wt")
	b := WorkspaceDirName("p", "wt")
	if a == b {
		t.Errorf("expected unique paths, got %q twice", a)
	}
}

// path.Join sanity: none of the slug results should escape the base dir.
func TestWorkspaceDirName_NoTraversal(t *testing.T) {
	dir := WorkspaceDirName("../etc", "../../passwd")
	clean := path.Clean(dir)
	if strings.HasPrefix(clean, "..") || strings.HasPrefix(clean, "/") {
		t.Errorf("dir %q escapes the worktree base", clean)
	}
}
