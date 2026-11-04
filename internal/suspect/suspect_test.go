package suspect

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, subject string, files map[string]string) string {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", subject)
	return git(t, dir, "rev-parse", "HEAD")
}

// repo builds: c1 adds both files, c2 touches db.go (PR 11), c3 touches
// other.go only, c4 touches db.go (PR 12), c5 touches db.go with no PR number.
func repo(t *testing.T) (dir string, shas []string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir = t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	shas = append(shas,
		commit(t, dir, "initial", map[string]string{"internal/store/db.go": "1", "internal/other/other.go": "1"}),
		commit(t, dir, "fix pool leak (#11)", map[string]string{"internal/store/db.go": "2"}),
		commit(t, dir, "unrelated", map[string]string{"internal/other/other.go": "2"}),
		commit(t, dir, "retry on conflict (#12)", map[string]string{"internal/store/db.go": "3"}),
		commit(t, dir, "tweak", map[string]string{"internal/store/db.go": "4"}),
	)
	return dir, shas
}

func TestFindRestrictsToTheRangeAndTheCulpritFiles(t *testing.T) {
	dir, sh := repo(t)
	got, err := Finder{Dir: dir}.Find(context.Background(), sh[1]+".."+sh[4], []string{"/app/internal/store/db.go"})
	if err != nil {
		t.Fatal(err)
	}
	var subjects []string
	for _, c := range got {
		subjects = append(subjects, c.Subject)
	}
	if want := []string{"tweak", "retry on conflict (#12)"}; !reflect.DeepEqual(subjects, want) {
		t.Fatalf("subjects = %v, want %v (newest first; the commit that only touched other.go and the one before the range are out)", subjects, want)
	}
	if got[1].PR != 12 || got[0].PR != 0 {
		t.Errorf("PR numbers = %d, %d; want 0 for a subject with none and 12 for a squash subject", got[0].PR, got[1].PR)
	}
	c := got[1]
	if c.SHA != sh[3] || c.Author != "Ada" || c.Time.IsZero() || !reflect.DeepEqual(c.Files, []string{"internal/store/db.go"}) {
		t.Errorf("commit = %+v", c)
	}
}

func TestFindWithoutARangeIsTheHistoryOfHead(t *testing.T) {
	dir, _ := repo(t)
	all, err := Finder{Dir: dir}.Find(context.Background(), "", []string{"internal/store/db.go"})
	if err != nil || len(all) != 4 || all[0].Subject != "tweak" || all[2].PR != 11 {
		t.Errorf("got %d commits (%v), want the 4 touching db.go, newest first: %+v", len(all), err, all)
	}
}

func TestFindDegradesOutsideACheckoutAndForUntrackedFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir()))
	if _, err := (Finder{Dir: t.TempDir()}).Find(context.Background(), "", []string{"a.go"}); !errors.Is(err, ErrNotCheckout) {
		t.Errorf("outside a checkout: err = %v, want ErrNotCheckout", err)
	}
	dir, _ := repo(t)
	if _, err := (Finder{Dir: dir}).Find(context.Background(), "", []string{"/usr/lib/go/src/runtime/panic.go"}); !errors.Is(err, ErrNoFiles) {
		t.Errorf("untracked culprit: err = %v, want ErrNoFiles", err)
	}
	if _, err := (Finder{Dir: dir}).Find(context.Background(), "main; rm -rf", []string{"internal/store/db.go"}); err == nil {
		t.Error("a range that is not two commit ids must be refused, not passed to git")
	}
}

func TestMatchPrefersTheLongestTrackedSuffix(t *testing.T) {
	tracked := []string{"a/x.go", "internal/a/x.go", "main.go", "pkg/main.go"}
	for _, tc := range []struct{ in, want string }{
		{"/build/src/github.com/acme/app/internal/a/x.go", "internal/a/x.go"},
		{"/build/a/x.go", "a/x.go"},
		{"internal/a/x.go", "internal/a/x.go"},
		{"./main.go", "main.go"},
		{"/build/main.go", ""}, // a bare file name matches only exactly
		{"/other/y.go", ""},
		{`C:\build\a\x.go`, "a/x.go"},
	} {
		got := Match(tracked, []string{tc.in})
		if tc.want == "" && len(got) != 0 || tc.want != "" && !reflect.DeepEqual(got, []string{tc.want}) {
			t.Errorf("Match(%q) = %v, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseRangeAndCulpritFile(t *testing.T) {
	if f, to, ok := ParseRange("abc1234..def5678"); !ok || f != "abc1234" || to != "def5678" {
		t.Errorf("ParseRange = %q %q %v", f, to, ok)
	}
	if _, to, ok := ParseRange("..def5678"); !ok || to != "def5678" {
		t.Error("a range with no start is the history of its end")
	}
	for _, bad := range []string{"", "main..HEAD", "abc1234", "abc1234..--output=x", "abc..def"} {
		if _, _, ok := ParseRange(bad); ok {
			t.Errorf("ParseRange(%q) accepted", bad)
		}
	}
	if got := CulpritFile("internal/store/db.go in store.(*DB).Exec"); got != "internal/store/db.go" {
		t.Errorf("CulpritFile = %q", got)
	}
	if got := CulpritFile("store.(*DB).Exec"); got != "" {
		t.Errorf("a function-only culprit has no file, got %q", got)
	}
}
