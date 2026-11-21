package release

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testTreeBudget is the hashing budget of every test that asserts WHAT a
// dirty capture hashes. CaptureTreeTimeout is wall-clock, and on a loaded
// machine three git process starts outlast it on a two-file tree, so a test
// under the default budget passed or failed with the machine's load. The
// budget path itself is pinned, deterministically, by
// TestCaptureProvenanceReportsAnUnhashedTree.
const testTreeBudget = 10 * time.Minute

// The capture tests run git for real, so they are slow tests: a git process
// start costs about a second on a loaded machine. Each takes its repository
// from a per-process template (gitfixture_test.go) and runs in parallel with
// the others, and the capture itself runs its independent git reads at once.
func TestCaptureProvenance(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	ctx := context.Background()
	opts := CaptureOptions{ForgeVersion: "v0.1.44", WorktreeKey: "", Host: "h1", treeBudget: testTreeBudget}

	t.Run("clean", func(t *testing.T) {
		t.Parallel()
		dir, headTree := initRepo(t)
		clean, err := CaptureProvenance(ctx, dir, opts)
		if err != nil {
			t.Fatal(err)
		}
		if clean.Dirty || clean.Branch != "main" || clean.Repo != "github.com/acme/app" || clean.ForgeVersion != "v0.1.44" || clean.Worktree.Label != "main" {
			t.Fatalf("clean capture: %+v", clean)
		}
		if clean.CommitSubject != "init" || clean.CommitAuthor != "t" || clean.CommitTime == nil {
			t.Fatalf("clean capture must name its commit: subject %q author %q time %v", clean.CommitSubject, clean.CommitAuthor, clean.CommitTime)
		}
		if clean.Tree != headTree || clean.Tree != treeHash(map[string]string{"a.txt": "one\n"}) {
			t.Fatalf("a clean tree must equal HEAD^{tree}: %s vs %s", clean.Tree, headTree)
		}
		if err := clean.Validate(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("dirty", func(t *testing.T) {
		t.Parallel()
		dir, headTree := initRepo(t)
		// Stage something, so we can prove the user's index is untouched.
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(t, dir, "add", "a.txt")
		if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("u\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		index := filepath.Join(dir, ".git", "index")
		indexBefore, err := os.ReadFile(index)
		if err != nil {
			t.Fatal(err)
		}

		// Two captures of one dirty state, at once — two builds of one
		// checkout — each through its own temporary index.
		var caps [2]Provenance
		var errs [2]error
		var wg sync.WaitGroup
		for i := range caps {
			wg.Go(func() { caps[i], errs[i] = CaptureProvenance(ctx, dir, opts) })
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		dirty, again := caps[0], caps[1]
		want := treeHash(map[string]string{"a.txt": "two\n", "untracked.txt": "u\n"})
		if !dirty.Dirty || dirty.Tree != want || dirty.Tree == headTree || dirty.CommitTime != nil {
			t.Fatalf("dirty capture must hash the built content %s: %+v", want, dirty)
		}
		if again.Tree != dirty.Tree {
			t.Fatalf("two captures of one dirty state must agree: %s vs %s", again.Tree, dirty.Tree)
		}
		if err := dirty.Validate(); err != nil {
			t.Fatal(err)
		}
		// Byte-identical, not merely the same staged names: the capture
		// neither stages nor refreshes anything in the user's index.
		indexAfter, err := os.ReadFile(index)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(indexAfter, indexBefore) {
			t.Fatal("capture rewrote the user's index")
		}
	})

	t.Run("not a checkout", func(t *testing.T) {
		t.Parallel()
		// No commit, no error.
		plain, err := CaptureProvenance(ctx, t.TempDir(), opts)
		if err != nil || plain.Commit != "" || plain.Tree != "" {
			t.Fatalf("non-git dir: %+v %v", plain, err)
		}
	})
}

// loosePathPresent confirms a `show-ref --exists` exit 2 (which git version
// returns it differs: newer git reports a ref path that is a directory as
// absent, 2.43 fails the lookup), so it is pinned here on its own rather than
// through whichever git the test machine has.
func TestLoosePathPresent(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a real git repository; runs in task test")
	}
	ctx := context.Background()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	heads := filepath.Join(dir, ".git", "refs", "heads")
	if loosePathPresent(ctx, dir, "refs/heads/main") {
		t.Error("an unborn branch has no loose path")
	}
	if err := os.MkdirAll(filepath.Join(heads, "main", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !loosePathPresent(ctx, dir, "refs/heads/main") {
		t.Error("a ref path that is a directory is present (damage), never unborn")
	}
	if err := os.WriteFile(filepath.Join(heads, "stub"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if loosePathPresent(ctx, dir, "refs/heads/stub/main") {
		t.Error("a ref under a non-directory parent (reftable's stub) is not present")
	}
}

// Only a genuinely unborn HEAD is "no commits yet": a HEAD that resolves to a
// commit git cannot read, or a broken branch ref, is an error, never an empty
// provenance that passes for a fresh repository.
func TestCaptureProvenanceTellsAnUnbornHeadFromAnUnreadableCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories; runs in task test")
	}
	ctx := context.Background()
	opts := CaptureOptions{treeBudget: testTreeBudget}

	t.Run("unborn", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		gitRun(t, dir, "init", "-q", "-b", "main")
		p, err := CaptureProvenance(ctx, dir, opts)
		if err != nil || p.Commit != "" || p.Tree != "" || p.Worktree.Label != filepath.Base(dir) {
			t.Fatalf("an unborn HEAD is an empty capture, not an error: %+v %v", p, err)
		}
	})
	t.Run("orphan branch", func(t *testing.T) {
		t.Parallel()
		// Unborn beside a branch that exists: only HEAD's own target is
		// absent.
		dir, _ := initRepo(t)
		gitRun(t, dir, "checkout", "-q", "--orphan", "fresh")
		p, err := CaptureProvenance(ctx, dir, opts)
		if err != nil || p.Commit != "" || p.Tree != "" {
			t.Fatalf("an orphan branch is an empty capture, not an error: %+v %v", p, err)
		}
	})

	// headObject is the loose object file of the fixture's HEAD commit.
	headObject := func(t *testing.T, dir string) string {
		t.Helper()
		ref, err := os.ReadFile(filepath.Join(dir, ".git", "refs", "heads", "main"))
		if err != nil {
			t.Fatal(err)
		}
		sha := strings.TrimSpace(string(ref))
		return filepath.Join(dir, ".git", "objects", sha[:2], sha[2:])
	}
	for _, tc := range []struct {
		name   string
		damage func(t *testing.T, dir string)
	}{
		{"missing commit object", func(t *testing.T, dir string) {
			if err := os.Remove(headObject(t, dir)); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt commit object", func(t *testing.T, dir string) {
			obj := headObject(t, dir)
			if err := os.Remove(obj); err != nil { // loose objects are read-only
				t.Fatal(err)
			}
			if err := os.WriteFile(obj, []byte("not zlib"), 0o444); err != nil {
				t.Fatal(err)
			}
		}},
		// Malformed loose refs. Git 2.43's symbolic-ref happens to refuse
		// the first two; it ACCEPTS the third (HEAD is a valid symbolic ref
		// to refs/heads/main) while `rev-parse --verify` fails exactly as
		// for an absent ref — the case only a store lookup tells apart.
		{"garbage branch ref", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "main"), []byte("garbage\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"empty branch ref", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "main"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"branch ref is a directory", func(t *testing.T, dir string) {
			ref := filepath.Join(dir, ".git", "refs", "heads", "main")
			if err := os.Remove(ref); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(ref, "x"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, _ := initRepo(t)
			tc.damage(t, dir)
			p, err := CaptureProvenance(ctx, dir, opts)
			if err == nil || errors.Is(err, ErrUnhashed) || !strings.Contains(err.Error(), "read HEAD commit") {
				t.Fatalf("an unreadable HEAD commit must be readHead's error, got %+v %v", p, err)
			}
			if p.Commit != "" || p.Tree != "" {
				t.Fatalf("no commit may be recorded from an unreadable HEAD: %+v", p)
			}
		})
	}
}

// A user's log.showSignature=true makes `git log` print a signed commit's
// verification ahead of the --format record, colored under color.ui=always.
// The capture and the commit range must read a signed history exactly as an
// unsigned one. Signs for real with a throwaway ssh key; the parse itself is
// covered without signing tools by TestLogParsersIgnoreTextOutsideRecords.
func TestCaptureReadsASignedHistoryWithShowSignatureOn(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and signs commits; runs in task test")
	}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not on PATH, so no commit can be signed here; TestLogParsersIgnoreTextOutsideRecords covers the parse with an injected signature")
	}
	t.Parallel()
	ctx := context.Background()
	dir, headTree := initRepo(t)
	keys := t.TempDir()
	key := filepath.Join(keys, "key")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "t", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(keys, "allowed_signers")
	if err := os.WriteFile(allowed, []byte(`t namespaces="git" `+string(pub)), 0o644); err != nil {
		t.Fatal(err)
	}
	// The user configuration under test, written straight into the
	// repository's config rather than through five `git config` processes.
	cfg, err := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(cfg, "[user]\n\tsigningkey = %s\n[gpg]\n\tformat = ssh\n[gpg \"ssh\"]\n\tallowedSignersFile = %s\n[log]\n\tshowSignature = true\n[color]\n\tui = always\n[commit]\n\tgpgSign = true\n", key+".pub", allowed)
	if cerr := cfg.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "commit", "-q", "--amend", "--no-edit")
	gitRun(t, dir, "commit", "-q", "--allow-empty", "-m", "second")

	// The fixture really does what a signing user's checkout does: plain
	// `git log` prints the signature before the record.
	plain, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%x1e%H").Output()
	if err != nil {
		t.Fatal(err)
	}
	preamble, head, _ := strings.Cut(string(plain), "\x1e")
	head = strings.TrimSpace(head)
	if !strings.Contains(preamble, "Good \"git\" signature") || !ValidObjectID(head) {
		t.Fatalf("fixture shows no signature, so it tests nothing: %q", plain)
	}

	p, err := CaptureProvenance(ctx, dir, CaptureOptions{treeBudget: testTreeBudget})
	if err != nil {
		t.Fatal(err)
	}
	if p.Commit != head || p.Tree != headTree || p.CommitSubject != "second" || p.CommitAuthor != "t" || p.Dirty {
		t.Fatalf("signed HEAD %s: %+v", head, p)
	}
	r, err := CaptureCommitRange(ctx, dir, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(subjects(r.Commits), ","); got != "second,init" || r.Commits[0].SHA != head || r.Total != 2 {
		t.Fatalf("signed range = %v (total %d), want second,init", got, r.Total)
	}
}

// rec frames fields as `git log --format=logFormat(...)` prints one commit:
// a NUL, each field followed by a NUL, then git's newline.
func rec(fields ...string) string { return "\x00" + strings.Join(fields, "\x00") + "\x00\n" }

// The log parsers frame records by NUL alone, the one byte git keeps out of
// every field: text git prints between records — a signature check, colored
// — cannot shift a field, every other control byte in a subject is content,
// and output that does not divide into whole records is an error rather
// than a dropped commit.
func TestLogParsersIgnoreTextOutsideRecords(t *testing.T) {
	commit, tree := strings.Repeat("a", 40), strings.Repeat("b", 40)
	sig := "\x1b[32mGood \"git\" signature for t with ED25519 key SHA256:abc\x1b[m\n"
	subject := "fix: a\x1db\x1ec\x1fd"

	f, err := parseHead([]byte(sig + rec(commit, tree, "1700000000", "bob", subject)))
	if err != nil {
		t.Fatal(err)
	}
	if f.commit != commit || f.tree != tree || f.author != "bob" || f.subject != subject || f.time == nil || f.time.Unix() != 1700000000 {
		t.Fatalf("parseHead = %+v", f)
	}

	cs, err := parseCommits([]byte(sig + rec(commit, "1700000060", "bob", subject) + sig + rec(tree, "1700000000", "alice", "c1")))
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].SHA != commit || cs[0].Subject != CommitText(subject, MaxCommitSubjectBytes) || cs[1].SHA != tree || cs[1].Author != "alice" {
		t.Fatalf("parseCommits = %+v", cs)
	}
	if none, err := parseCommits(nil); err != nil || len(none) != 0 {
		t.Fatalf("an empty range = %v %v, want no commits", none, err)
	}

	whole := rec(commit, tree, "1", "a", "s")
	for name, out := range map[string]string{
		"truncated":     strings.TrimSuffix(whole, "\x00\n"),
		"a field short": rec(commit, tree, "1", "a"),
		"no record":     sig,
		"two records":   whole + whole,
	} {
		if _, err := parseHead([]byte(out)); err == nil {
			t.Errorf("parseHead(%s) = nil error", name)
		}
	}
	if _, err := parseCommits([]byte(rec(commit, "1", "bob", "c2") + rec(tree, "1", "bob"))); err == nil {
		t.Error("parseCommits of a record a field short = nil error")
	}
}

// Every C0 separator byte is legal in a commit subject, so none may frame a
// record: a real commit whose subject holds them is captured whole, both as
// HEAD and in a commit range.
func TestCaptureReadsSeparatorBytesInARealSubject(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories; runs in task test")
	}
	t.Parallel()
	ctx := context.Background()
	dir, _ := initRepo(t)
	subject := "one\x1dtwo\x1ethree\x1ffour"
	gitRun(t, dir, "commit", "-q", "--allow-empty", "-m", subject)
	want := CommitText(subject, MaxCommitSubjectBytes)
	if want != "one two three four" {
		t.Fatalf("CommitText(%q) = %q: the expectation below is wrong", subject, want)
	}

	p, err := CaptureProvenance(ctx, dir, CaptureOptions{treeBudget: testTreeBudget})
	if err != nil {
		t.Fatal(err)
	}
	if p.CommitSubject != want || p.CommitAuthor != "t" {
		t.Fatalf("HEAD subject = %q author %q, want %q", p.CommitSubject, p.CommitAuthor, want)
	}
	r, err := CaptureCommitRange(ctx, dir, p.Commit, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(subjects(r.Commits), "|"); got != want+"|init" || r.Commits[0].SHA != p.Commit {
		t.Fatalf("range subjects = %q, want %q", got, want+"|init")
	}
}

// Git before 2.43 has no `show-ref --exists`, so unbornHead reads the files
// ref store itself. Absent is a positive finding only: a loose path in any
// form, a packed line for the ref, or a packed-refs it cannot vouch for is
// not absent.
func TestRefFileAbsent(t *testing.T) {
	const ref = "refs/heads/main"
	sha := strings.Repeat("c", 40)
	for _, tc := range []struct {
		name          string
		loose, packed *string // nil: the file does not exist
		looseDir      bool
		want          bool
	}{
		{name: "nothing at all", want: true},
		{name: "other refs packed", packed: ptr("# pack-refs with: peeled\n" + sha + " refs/heads/other\n^" + sha + "\n"), want: true},
		{name: "loose file", loose: ptr(sha + "\n")},
		{name: "garbage loose file", loose: ptr("garbage\n")},
		{name: "loose directory", looseDir: true},
		{name: "packed", packed: ptr(sha + " " + ref + "\n")},
		{name: "garbage packed-refs", packed: ptr("zzzz " + ref + "\n")},
		{name: "unparseable packed line", packed: ptr(sha + "\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			loose, packed := filepath.Join(dir, "loose"), filepath.Join(dir, "packed-refs")
			if tc.loose != nil {
				if err := os.WriteFile(loose, []byte(*tc.loose), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.looseDir {
				if err := os.Mkdir(loose, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if tc.packed != nil {
				if err := os.WriteFile(packed, []byte(*tc.packed), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := refFileAbsent(loose, packed, ref); got != tc.want {
				t.Fatalf("refFileAbsent = %v, want %v", got, tc.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }

// A dirty tree the budget stopped from hashing is an ERROR naming
// ErrUnhashed, never an empty tree reported as success: under load the
// budget used to expire on a two-file tree and the capture said nothing. The
// rest of the provenance is still returned, and the caller's own context is
// told apart from the capture's budget.
func TestCaptureProvenanceReportsAnUnhashedTree(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories; runs in task test")
	}
	t.Parallel()
	dir, _ := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	p, err := CaptureProvenance(ctx, dir, CaptureOptions{treeBudget: time.Nanosecond})
	if !errors.Is(err, ErrUnhashed) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("an exhausted budget must return ErrUnhashed, got %v", err)
	}
	if !p.Unhashed() || !p.Dirty || p.Branch != "main" || p.CommitSubject != "init" {
		t.Fatalf("the rest of the provenance must still be captured: %+v", p)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = CaptureProvenance(cancelled, dir, CaptureOptions{})
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnhashed) {
		t.Fatalf("a cancelled caller must get its own error, not ErrUnhashed: %v", err)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// A parked worktree or cloned repo inside the project is another checkout's
// content: it must not make this checkout dirty (a 2026-10-12 prod deploy cut
// and built a release for an unchanged tree because of one), but every other
// untracked file still counts.
func TestInspectCheckout_NestedCheckoutsAreNotDirt(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	t.Parallel()
	dir := gitInit(t)
	ctx := context.Background()
	opts := CaptureOptions{ForgeVersion: "v0.1.44", treeBudget: testTreeBudget}
	clean, err := CaptureProvenance(ctx, dir, opts)
	if err != nil || clean.Dirty {
		t.Fatalf("baseline: %+v %v", clean, err)
	}

	gitRun(t, dir, "worktree", "add", "-q", "-b", "other", filepath.Join(dir, "linked-worktree"))
	if err := os.MkdirAll(filepath.Join(dir, "cloned"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, filepath.Join(dir, "cloned"), "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "cloned", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	changes, err := InspectCheckout(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if changes.Dirty() || len(changes.Paths) != 0 {
		t.Fatalf("nested checkouts counted as changes: %+v", changes)
	}
	if got := strings.Join(changes.Nested, " "); got != "cloned/ linked-worktree/" {
		t.Fatalf("nested = %q, want both checkouts listed", got)
	}
	withNested, err := CaptureProvenance(ctx, dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if withNested.Dirty || withNested.Tree != clean.Tree {
		t.Fatalf("a tree with only nested checkouts must equal the clean one: %+v vs %+v", withNested, clean)
	}

	// A real untracked file still counts, nested checkouts or not, and the
	// hashed tree contains it but neither checkout.
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("r"), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, _ = InspectCheckout(ctx, dir)
	if !changes.Dirty() || strings.Join(changes.Paths, " ") != "real.txt" {
		t.Fatalf("paths = %v, want [real.txt]", changes.Paths)
	}
	dirty, err := CaptureProvenance(ctx, dir, opts)
	if err != nil || !dirty.Dirty || dirty.Tree == clean.Tree {
		t.Fatalf("dirty capture: %+v %v", dirty, err)
	}
	listing, _ := gitOut(ctx, dir, "ls-tree", "-r", "--name-only", dirty.Tree)
	if strings.Contains(listing, "cloned") || strings.Contains(listing, "linked-worktree") || !strings.Contains(listing, "real.txt") {
		t.Fatalf("hashed tree = %q: want real.txt and no nested checkout", listing)
	}
}

// A rename is two porcelain entries in -z mode; the origin must not be read
// as a path of its own.
func TestInspectCheckout_ParsesRenamesAndLeadingSpace(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories; runs in task test")
	}
	t.Parallel()
	dir := gitInit(t)
	ctx := context.Background()
	gitRun(t, dir, "mv", "a.txt", "b b.txt")
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err := InspectCheckout(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(changes.Paths, "|"); got != "b b.txt|c.txt" {
		t.Fatalf("paths = %q", got)
	}
}

// The commit time is the committer date of HEAD in UTC, recorded for a clean
// checkout only: a dirty tree's bytes are no commit's, so the time of HEAD
// would be a claim about content it does not describe.
func TestCaptureProvenance_CommitTime(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories; runs in task test")
	}
	t.Parallel()
	dir := gitInit(t)
	ctx := context.Background()
	opts := CaptureOptions{ForgeVersion: "v0.1.44", treeBudget: testTreeBudget}

	// A committer date in a non-UTC zone, distinct from the author date.
	cmd := exec.Command("git", "-C", dir, "-c", "user.name=t", "-c", "user.email=t@t",
		"commit", "-q", "--amend", "--no-edit", "--date", "2026-01-02T03:04:05+00:00")
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE=2026-03-04T10:30:00+05:30")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("amend: %v\n%s", err, out)
	}
	want := time.Date(2026, 3, 4, 5, 0, 0, 0, time.UTC)

	clean, err := CaptureProvenance(ctx, dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if clean.CommitTime == nil || !clean.CommitTime.Equal(want) || clean.CommitTime.Location() != time.UTC {
		t.Fatalf("clean commit time = %v, want %v (committer date, UTC)", clean.CommitTime, want)
	}
	if err := clean.Validate(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err := CaptureProvenance(ctx, dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty.Dirty || dirty.CommitTime != nil {
		t.Fatalf("a dirty capture must leave the commit time unset: %+v", dirty)
	}

	plain, err := CaptureProvenance(ctx, t.TempDir(), opts)
	if err != nil || plain.CommitTime != nil {
		t.Fatalf("non-git dir: %+v %v", plain, err)
	}
}

// D4: a release names its commit's subject and author, dirty or not — a dirty
// build's commit is still the base it was built on.
func TestCaptureProvenanceRecordsCommitSubjectAndAuthor(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories; runs in task test")
	}
	t.Parallel()
	dir, shas := gitHistory(t, 2)
	ctx := context.Background()
	clean, err := CaptureProvenance(ctx, dir, CaptureOptions{treeBudget: testTreeBudget})
	if err != nil {
		t.Fatal(err)
	}
	if clean.Commit != shas[1] || clean.CommitSubject != "c2" || clean.CommitAuthor != "bob" {
		t.Fatalf("clean capture: commit %s subject %q author %q", clean.Commit, clean.CommitSubject, clean.CommitAuthor)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err := CaptureProvenance(ctx, dir, CaptureOptions{treeBudget: testTreeBudget})
	if err != nil {
		t.Fatal(err)
	}
	if !dirty.Dirty || dirty.CommitSubject != "c2" || dirty.CommitAuthor != "bob" || dirty.CommitTime != nil {
		t.Fatalf("dirty capture: dirty %v subject %q author %q commit time %v", dirty.Dirty, dirty.CommitSubject, dirty.CommitAuthor, dirty.CommitTime)
	}
	if err := dirty.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestProvenanceValidateCommitText(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name string
		p    Provenance
		ok   bool
	}{
		{"subject and author", Provenance{Commit: commit, CommitSubject: "fix: x", CommitAuthor: "alice"}, true},
		{"subject without commit", Provenance{CommitSubject: "fix: x"}, false},
		{"author without commit", Provenance{CommitAuthor: "alice"}, false},
		{"two-line subject", Provenance{Commit: commit, CommitSubject: "fix\nmore"}, false},
		{"over-long author", Provenance{Commit: commit, CommitAuthor: strings.Repeat("a", MaxCommitAuthorBytes+1)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.p.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}
