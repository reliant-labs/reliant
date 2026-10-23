package release

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// gitHistory builds a repository whose main is n commits ("c1" … "cn", by
// alice then bob alternately) in one fast-import, and returns it with the
// shas oldest first.
func gitHistory(t *testing.T, n int) (string, []string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	var stream strings.Builder
	for i := 1; i <= n; i++ {
		author := "alice"
		if i%2 == 0 {
			author = "bob"
		}
		msg := fmt.Sprintf("c%d\n\nbody line that is not the subject\n", i)
		content := fmt.Sprintf("%d\n", i)
		fmt.Fprintf(&stream, "commit refs/heads/main\nauthor %s <%s@example.com> %d +0000\ncommitter %s <%s@example.com> %d +0000\ndata %d\n%sM 644 inline f.txt\ndata %d\n%s\n",
			author, author, 1700000000+i*60, author, author, 1700000000+i*60, len(msg), msg, len(content), content)
	}
	gitDo(t, dir, "", "init", "-q", "-b", "main")
	gitDo(t, dir, stream.String(), "fast-import", "--quiet")
	gitDo(t, dir, "", "reset", "-q", "--hard", "main")
	out := gitDo(t, dir, "", "rev-list", "--reverse", "main")
	return dir, strings.Fields(out)
}

func gitDo(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	// No identity is needed: fast-import states every author and committer,
	// and nothing else here commits.
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func relAt(version, commit string, cut int, dirty bool) Release {
	return Release{Version: version, Git: Git{Commit: commit, Dirty: dirty}, CreatedAt: time.Unix(int64(1800000000+cut), 0)}
}

func subjects(cs []Commit) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Subject
	}
	return out
}

func TestCaptureCommitRangeStartsAtTheNewestAncestorRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories; runs in task test")
	}
	dir, shas := gitHistory(t, 6)
	ctx := context.Background()
	earlier := []Release{
		relAt("v1", shas[0], 1, false),
		relAt("v2", shas[2], 2, false),
		// Newer, but dirty: its commit does not describe its bytes.
		relAt("v3-dirty", shas[3], 3, true),
		// Newest of all, but not in HEAD's history.
		relAt("v-elsewhere", strings.Repeat("ab", 20), 4, false),
	}
	got, err := CaptureCommitRange(ctx, dir, shas[5], earlier)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseRelease != "v2" || got.BaseCommit != shas[2] {
		t.Fatalf("base = %s@%s, want v2@%s", got.BaseRelease, got.BaseCommit, shas[2])
	}
	if want := []string{"c6", "c5", "c4"}; strings.Join(subjects(got.Commits), ",") != strings.Join(want, ",") {
		t.Fatalf("commits = %v, want %v (newest first, subject line only)", subjects(got.Commits), want)
	}
	if got.Total != 3 || got.Truncated {
		t.Fatalf("total=%d truncated=%v, want a complete range of 3", got.Total, got.Truncated)
	}
	if c := got.Commits[0]; c.SHA != shas[5] || c.Author != "bob" || !c.Time.Equal(time.Unix(1700000000+6*60, 0)) {
		t.Fatalf("newest commit = %+v", c)
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}

	// Re-cutting the base's own commit adds nothing, and says so.
	same, err := CaptureCommitRange(ctx, dir, shas[2], earlier)
	if err != nil {
		t.Fatal(err)
	}
	if same.BaseRelease != "v2" || len(same.Commits) != 0 || same.Commits == nil || same.Total != 0 || same.Truncated {
		t.Fatalf("re-cut of the base commit = %+v", same)
	}
}

func TestCaptureCommitRangeWithNoEarlierReleaseCountsTheWholeHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories; runs in task test")
	}
	dir, shas := gitHistory(t, MaxRangeCommits+5)
	got, err := CaptureCommitRange(context.Background(), dir, shas[len(shas)-1], nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseRelease != "" || len(got.Commits) != MaxRangeCommits || got.Total != MaxRangeCommits+5 || !got.Truncated {
		t.Fatalf("first release over the cap: base=%q commits=%d total=%d truncated=%v; want %d of %d, truncated",
			got.BaseRelease, len(got.Commits), got.Total, got.Truncated, MaxRangeCommits, MaxRangeCommits+5)
	}
	if got.Commits[0].SHA != shas[len(shas)-1] {
		t.Fatalf("the cap must keep the NEWEST commits")
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
}

// CI checks out shallow clones: the earlier release's commit is not in the
// history, so the range is whatever is visible, marked truncated with an
// unknown total — never a short list that reads as complete.
func TestCaptureCommitRangeInAShallowCloneIsTruncatedWithAnUnknownTotal(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories; runs in task test")
	}
	origin, shas := gitHistory(t, 6)
	clone := t.TempDir()
	gitDo(t, clone, "", "clone", "-q", "--depth", "2", "file://"+origin, ".")
	got, err := CaptureCommitRange(context.Background(), clone, shas[5], []Release{relAt("v1", shas[1], 1, false)})
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseRelease != "" || !got.Truncated || got.Total != 0 || got.CountKnown() {
		t.Fatalf("shallow capture = base %q truncated %v total %d; want no base, truncated, unknown total", got.BaseRelease, got.Truncated, got.Total)
	}
	if want := "c6,c5"; strings.Join(subjects(got.Commits), ",") != want {
		t.Fatalf("visible commits = %v, want %s", subjects(got.Commits), want)
	}

	// A base the shallow history DOES reach is still a complete range.
	near, err := CaptureCommitRange(context.Background(), clone, shas[5], []Release{relAt("v5", shas[4], 1, false)})
	if err != nil {
		t.Fatal(err)
	}
	if near.BaseRelease != "v5" || near.Truncated || near.Total != 1 {
		t.Fatalf("shallow capture with a visible base = %+v", near)
	}
}

func TestCaptureCommitRangeOutsideGitRecordsNothing(t *testing.T) {
	got, err := CaptureCommitRange(context.Background(), t.TempDir(), strings.Repeat("a", 40), nil)
	if err != nil || got != nil {
		t.Fatalf("non-git dir: %+v, %v", got, err)
	}
	if got, err := CaptureCommitRange(context.Background(), t.TempDir(), "", nil); err != nil || got != nil {
		t.Fatalf("no commit: %+v, %v", got, err)
	}
}

func TestCommitRangeValidate(t *testing.T) {
	sha := func(c byte) string { return strings.Repeat(string(c), 40) }
	one := []Commit{{SHA: sha('a'), Subject: "fix: a", Author: "alice"}}
	for _, tc := range []struct {
		name string
		r    CommitRange
		ok   bool
	}{
		{"complete", CommitRange{BaseRelease: "v1", BaseCommit: sha('b'), Commits: one, Total: 1}, true},
		{"capped", CommitRange{Commits: one, Total: 7, Truncated: true}, true},
		{"shallow", CommitRange{Commits: one, Truncated: true}, true},
		{"empty", CommitRange{BaseRelease: "v1", BaseCommit: sha('b'), Commits: []Commit{}}, true},
		{"base release without commit", CommitRange{BaseRelease: "v1", Commits: one, Total: 1}, false},
		{"bad base", CommitRange{BaseRelease: "v1", BaseCommit: "abc", Commits: one, Total: 1}, false},
		{"complete but miscounted", CommitRange{Commits: one, Total: 3}, false},
		{"truncated total not past list", CommitRange{Commits: one, Total: 1, Truncated: true}, false},
		{"bad sha", CommitRange{Commits: []Commit{{SHA: "abc"}}, Total: 1}, false},
		{"duplicate", CommitRange{Commits: append(append([]Commit{}, one...), one...), Total: 2}, false},
		{"multi-line subject", CommitRange{Commits: []Commit{{SHA: sha('a'), Subject: "a\nb"}}, Total: 1}, false},
		{"long subject", CommitRange{Commits: []Commit{{SHA: sha('a'), Subject: strings.Repeat("x", MaxCommitSubjectBytes+1)}}, Total: 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.r.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestCommitText(t *testing.T) {
	if got := CommitText("fix:\tthe  thing\r\nnext", 100); got != "fix: the thing next" {
		t.Fatalf("CommitText = %q", got)
	}
	long := CommitText(strings.Repeat("é", 200), 11)
	if len(long) > 11 || !strings.HasSuffix(long, "…") || validateCommitText("x", long, 11) != nil {
		t.Fatalf("over-long text = %q (%d bytes)", long, len(long))
	}
}

func TestReleaseValidateRefusesASelfBasedRange(t *testing.T) {
	r := Release{
		Version: "v1", Artifacts: map[string]Artifact{"api": {Kind: KindOCI, Mode: ModeShared, Digests: map[string]string{SharedVariant: "sha256:" + strings.Repeat("a", 64)}}},
		Commits: &CommitRange{BaseRelease: "v1", BaseCommit: strings.Repeat("b", 40), Commits: []Commit{}},
	}
	if err := r.Validate(); err == nil {
		t.Fatal("a release cannot be the base of its own range")
	}
	r.Commits.BaseRelease = "v0"
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	// Evidence, not identity: a re-cut with a different range is the same release.
	other := r
	other.Commits = nil
	if !r.SameContent(other) {
		t.Fatal("SameContent must ignore the commit range")
	}
}
