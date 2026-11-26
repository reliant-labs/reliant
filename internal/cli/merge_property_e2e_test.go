//go:build e2e

package cli

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// TestE2EMergeProperty is forge's merge invariant at project scale: on a real
// scaffolded project (two services, a frontend, an entity), two branches that
// change INDEPENDENT inputs, each regenerated with forge, merge with plain
// `git merge` — no merge driver, no attributes, exactly as GitHub's
// server-side merge does — and a `forge generate` on the merge changes
// nothing: the merge already IS what forge writes for the merged inputs.
//
// Each generator's unit-level case (internal/mergeproperty) proves one output;
// this proves they compose, over everything a project commits.
func TestE2EMergeProperty(t *testing.T) {
	t.Parallel() // independent projects in their own t.TempDirs; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)

	// A declared gap no case reaches is stale: the generator was fixed and
	// its row must say so. Checked once every case has passed.
	hits := &gapHits{seen: map[string]bool{}}
	t.Cleanup(func() {
		if t.Failed() {
			return
		}
		for step, c := range mergeCases {
			for _, g := range c.gapPaths {
				if !hits.seen[g] {
					t.Errorf("known merge gap %q of step %q neither conflicted nor regenerated in any case: it is closed, so mark the step's merge case for what now holds", g, step)
				}
			}
		}
	})

	for _, tc := range []struct {
		name         string
		ours, theirs mergeEdit
	}{
		{
			name:   "custom rpcs on two services",
			ours:   mergeEdit{service: "users"},
			theirs: mergeEdit{service: "orders"},
		},
		{
			name:   "an entity on each of two services",
			ours:   mergeEdit{service: "users", proto: entityProto("Profile", "  string bio = 2;\n")},
			theirs: mergeEdit{service: "orders", proto: entityProto("Order", "  string sku = 2;\n")},
		},
		{
			name:   "two entities referencing the same parent",
			ours:   mergeEdit{service: "users", proto: entityProto("Membership", "  string account_id = 2;\n")},
			theirs: mergeEdit{service: "orders", proto: entityProto("Order", "  string account_id = 2;\n  string sku = 3;\n")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runMergeProperty(t, forgeBin, hits, tc.ours, tc.theirs)
		})
	}
}

// mergeEdit is one branch's change: proto text appended to a service's proto
// (entities are born by `forge scaffold`), and, unless proto is a CRUD
// entity, one custom rpc on the service.
type mergeEdit struct {
	service string
	proto   string
}

func entityProto(name, fields string) string {
	return "\n// forge:entity\nmessage " + name + " {\n  string id = 1;\n" + fields + "}\n"
}

// gapHits records which declared gap globs a case actually hit.
type gapHits struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (h *gapHits) hit(p string) (step string, ok bool) {
	for name, c := range mergeCases {
		for _, g := range c.gapPaths {
			if m, _ := path.Match(g, p); m {
				h.mu.Lock()
				h.seen[g] = true
				h.mu.Unlock()
				return name, true
			}
		}
	}
	return "", false
}

func runMergeProperty(t *testing.T, forgeBin string, hits *gapHits, ours, theirs mergeEdit) {
	t.Helper()
	dir := t.TempDir()
	env := append(os.Environ(),
		// Plain git: no user or system config, so no merge driver and no
		// attributes beyond the project's own .gitattributes, whose driver
		// name then resolves to git's built-in text merge.
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"FORGE_NO_MERGE_DRIVER=1", "FORGE_NO_GIT_HOOKS=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	run := func(dir string, name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(dir string, name string, args ...string) string {
		t.Helper()
		out, err := run(dir, name, args...)
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
		}
		return out
	}

	must(dir, forgeBin, "project", "new", "mergeapp", "--mod", "example.com/mergeapp",
		"--service", "users,orders", "--frontend", "web")
	project := filepath.Join(dir, "mergeapp")
	addCorpusForgePkgReplace(t, project)
	// The parent the FK case references, in the base.
	appendProto(t, project, "users", entityProto("Account", "  string name = 2;\n"))
	must(project, forgeBin, "scaffold")
	must(project, forgeBin, "generate")

	if _, err := os.Stat(filepath.Join(project, ".git")); err != nil {
		must(project, "git", "init", "-q", "-b", "main")
	}
	must(project, "git", "checkout", "-q", "-B", "main")
	must(project, "git", "add", "-A")
	must(project, "git", "commit", "-qm", "base", "--allow-empty")

	apply := func(branch string, e mergeEdit) {
		t.Helper()
		must(project, "git", "checkout", "-q", "-b", branch, "main")
		if e.proto != "" {
			appendProto(t, project, e.service, e.proto)
		}
		if !strings.Contains(e.proto, "forge:entity") {
			addCustomRPC(t, project, e.service, "Ping"+strings.ToUpper(e.service[:1])+e.service[1:])
		}
		must(project, forgeBin, "scaffold")
		must(project, forgeBin, "generate")
		must(project, "git", "add", "-A")
		must(project, "git", "commit", "-qm", branch)
	}
	apply("ours", ours)
	apply("theirs", theirs)

	must(project, "git", "checkout", "-q", "ours")
	if out, err := run(project, "git", "merge", "--no-edit", "theirs"); err != nil {
		conflicted := strings.Fields(must(project, "git", "diff", "--name-only", "--diff-filter=U"))
		sort.Strings(conflicted)
		// A conflict is tolerated only in a path a step declares as a known
		// gap (mergeCases); anywhere else it is a regression of the invariant.
		var undeclared []string
		for _, f := range conflicted {
			if step, ok := hits.hit(f); ok {
				t.Logf("known merge gap (%s): %s conflicts", step, f)
				continue
			}
			undeclared = append(undeclared, f)
		}
		if len(undeclared) > 0 || len(conflicted) == 0 {
			// The hunks, so a failure says WHICH lines collided, not just where.
			var hunks strings.Builder
			for _, f := range undeclared {
				d, _ := run(project, "git", "diff", "--", f)
				if lines := strings.Split(d, "\n"); len(lines) > 60 {
					d = strings.Join(lines[:60], "\n") + "\n…"
				}
				hunks.WriteString(d + "\n")
			}
			t.Fatalf("plain git merge of two independent changes CONFLICTED in %d file(s) no step declares as a known gap:\n  %s\n\n%s\n%s",
				len(undeclared), strings.Join(undeclared, "\n  "), out, hunks.String())
		}
		// Settle the declared gaps on one side, so the regeneration below
		// still proves every OTHER file is already forge's output.
		must(project, "git", append([]string{"checkout", "--ours", "--"}, conflicted...)...)
		must(project, "git", "add", "-A")
		must(project, "git", "commit", "-qm", "merge (known gaps settled to ours)")
	}

	// The merge must already be what forge writes for the merged inputs,
	// except in the declared known-gap paths.
	must(project, forgeBin, "generate")
	var drift []string
	for _, line := range strings.Split(strings.TrimSpace(must(project, "git", "status", "--porcelain", "--untracked-files=all")), "\n") {
		if line == "" {
			continue
		}
		f := strings.TrimSpace(line[2:])
		if i := strings.Index(f, " -> "); i >= 0 {
			f = f[i+4:]
		}
		if step, ok := hits.hit(f); ok {
			t.Logf("known merge gap (%s): %s regenerated after the merge", step, f)
			continue
		}
		drift = append(drift, line)
	}
	if len(drift) > 0 {
		diff, _ := run(project, "git", "diff")
		if lines := strings.Split(diff, "\n"); len(lines) > 200 {
			diff = strings.Join(lines[:200], "\n") + "\n…"
		}
		t.Fatalf("forge generate on the merge changed files no step declares as a known gap, so the plain merge was not forge's output for the merged inputs:\n%s\n\n%s",
			strings.Join(drift, "\n"), diff)
	}
}

func serviceProtoPath(project, service string) string {
	return filepath.Join(project, "proto", "services", service, "v1", service+".proto")
}

func appendProto(t *testing.T, project, service, text string) {
	t.Helper()
	p := serviceProtoPath(project, service)
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append(body, []byte(text)...), 0o644); err != nil {
		t.Fatal(err)
	}
}

// addCustomRPC declares rpc name (with its message pair) in service's proto.
func addCustomRPC(t *testing.T, project, service, name string) {
	t.Helper()
	p := serviceProtoPath(project, service)
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	i := strings.Index(s, "service ")
	j := strings.Index(s[i:], "{")
	if i < 0 || j < 0 {
		t.Fatalf("no service block in %s", p)
	}
	at := i + j + 1
	s = s[:at] + "\n  rpc " + name + "(" + name + "Request) returns (" + name + "Response);" + s[at:]
	s += "\nmessage " + name + "Request {}\nmessage " + name + "Response {}\n"
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
