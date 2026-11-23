package opindex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestMapRangeGit maps a real repository's history: per-commit attribution,
// the uncommitted pseudo-commit (untracked files included), PR numbers, and
// a project that is a subdirectory of its repository (paths relative to the
// project, files outside it left out).
func TestMapRangeGit(t *testing.T) {
	testkit.Slow(t, "execs git")
	repo := t.TempDir()
	app := filepath.Join(repo, "app")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	edit := func(rel, old, new string) {
		t.Helper()
		p := filepath.Join(app, filepath.FromSlash(rel))
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), old) {
			t.Fatalf("%q not in %s", old, rel)
		}
		if err := os.WriteFile(p, []byte(strings.Replace(string(b), old, new, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(subject string) string {
		git("add", "-A")
		git("commit", "-q", "-m", subject)
		return git("rev-parse", "HEAD")
	}

	git("init", "-q")
	writeTree(t, app, fixture)
	writeTree(t, repo, map[string]string{"README.md": "outside the project\n"})
	c1 := commit("chore: initial")

	edit("internal/billing/service.go", `"r-" + id`, `"refund-" + id`)
	writeTree(t, repo, map[string]string{"README.md": "still outside\n"})
	c2 := commit("fix(billing): refund ids (#42)")

	edit("proto/todo/v1/todo.proto", "string title = 1;", "string title = 1;\n  bool done = 2;")
	edit("internal/handlers/todo/rpc_create_todo.go", "return s.save(normalize(req))", "return s.save(normalize(req + \"!\"))")
	c3 := commit("feat(todo): done flag")

	edit("internal/billing/service.go", "if n > limit {", "if n >= limit {")
	writeTree(t, app, map[string]string{
		"internal/billing/extra.go": "package billing\n\nfunc extra() {}\n",
		// Untracked and not Go: accounted for as unattributed, not dropped.
		"internal/billing/NOTES.txt": "why refunds round down\n",
		// Untracked but ignored: never read.
		"internal/billing/scratch.log": "noise\n",
	})
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	m, err := MapRange(ctx, app, c1, "")
	if err != nil {
		t.Fatal(err)
	}
	if m.Base != c1 || m.Head != "" || m.Total != 2 || m.Truncated {
		t.Errorf("range header = base %s head %q total %d truncated %v", m.Base, m.Head, m.Total, m.Truncated)
	}
	uncommitted := Commit{Subject: UncommittedSubject}
	wantCommits := []Commit{uncommitted, {SHA: c3, Subject: "feat(todo): done flag"}, {SHA: c2, Subject: "fix(billing): refund ids (#42)", PR: 42}}
	if !reflect.DeepEqual(m.Commits, wantCommits) {
		t.Errorf("commits = %+v, want %+v", m.Commits, wantCommits)
	}

	got := map[string][]string{}
	for _, op := range m.Ops {
		for _, c := range op.Commits {
			got[op.Name] = append(got[op.Name], c.Subject)
		}
	}
	want := map[string][]string{
		// validate (helper) uncommitted, extra.go (package) untracked.
		"billing.Charge": {UncommittedSubject},
		"billing.Refund": {UncommittedSubject, "fix(billing): refund ids (#42)"},
		// proto message and handler method in one commit.
		"todo.v1.TodoService/CreateTodo": {"feat(todo): done flag"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("op commits = %v, want %v", got, want)
	}
	for _, op := range m.Ops {
		if op.Name != "todo.v1.TodoService/CreateTodo" {
			continue
		}
		var vias []string
		for _, r := range op.Reasons {
			vias = append(vias, r.Via+" "+r.Function+" "+r.File)
		}
		wantVias := []string{
			"handler (*Service).CreateTodo internal/handlers/todo/rpc_create_todo.go",
			"proto message CreateTodoRequest proto/todo/v1/todo.proto",
		}
		if !reflect.DeepEqual(vias, wantVias) {
			t.Errorf("CreateTodo reasons = %v, want %v", vias, wantVias)
		}
	}
	notes := Unattributed{File: "internal/billing/NOTES.txt", Reason: ReasonNotGo, Commits: []Commit{uncommitted}}
	var sawNotes bool
	for _, u := range m.Unattributed {
		if strings.Contains(u.File, "README") {
			t.Errorf("a file outside the project was mapped: %+v", u)
		}
		if strings.HasSuffix(u.File, ".log") {
			t.Errorf("an ignored untracked file was mapped: %+v", u)
		}
		if u.File == notes.File {
			sawNotes = true
			if !reflect.DeepEqual(u, notes) {
				t.Errorf("untracked non-Go file = %+v, want %+v", u, notes)
			}
		}
	}
	if !sawNotes {
		t.Errorf("untracked non-Go file missing from unattributed: %+v", m.Unattributed)
	}

	one, err := MapCommit(ctx, app, c2)
	if err != nil {
		t.Fatal(err)
	}
	if one.Base != c1 || one.Head != c2 || len(one.Ops) != 1 || one.Ops[0].Name != "billing.Refund" || one.Ops[0].Commits[0].PR != 42 {
		t.Errorf("MapCommit(c2) = %s", js(one))
	}
	root, err := MapCommit(ctx, app, c1)
	if err != nil {
		t.Fatal(err)
	}
	if root.Base != "" || len(root.Ops) != 4 {
		t.Errorf("MapCommit(root) maps %d ops from base %q, want every op from the empty tree", len(root.Ops), root.Base)
	}

	committed, err := MapRange(ctx, app, c1, c3)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Head != c3 || len(committed.Commits) != 2 {
		t.Errorf("MapRange(c1, c3) = head %s, %d commits", committed.Head, len(committed.Commits))
	}

	if mb, err := MergeBase(ctx, app, c2, ""); err != nil || mb != c2 {
		t.Errorf("MergeBase(c2, HEAD) = %s, %v; want %s", mb, err, c2)
	}
}
