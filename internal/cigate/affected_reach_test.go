package cigate

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// reachListed is a chain a <- b <- e <- f, plus c alone: a change to a
// reaches b directly and e, f only transitively.
func reachListed(root string) func(context.Context, string, []string) (io.Reader, error) {
	pkg := func(name string, imports ...string) listedPackage {
		return listedPackage{ImportPath: "ex.com/m/" + name, Name: name, Dir: filepath.Join(root, name), Imports: imports}
	}
	test := func(name string, deps ...string) listedPackage {
		return listedPackage{ImportPath: "ex.com/m/" + name + ".test", Name: "main", Dir: filepath.Join(root, name), Deps: deps}
	}
	pkgs := []listedPackage{
		pkg("a"), pkg("b", "ex.com/m/a"), {ImportPath: "ex.com/m/e", Name: "e", Dir: filepath.Join(root, "e"), Imports: []string{"ex.com/m/b"}, EmbedFiles: []string{"tmpl/e.tmpl"}}, pkg("f", "ex.com/m/e"), pkg("c"),
		// b's external test imports a too; e's test variant of b is not e's import.
		{ImportPath: "ex.com/m/b_test [ex.com/m/b.test]", Name: "b_test", Dir: filepath.Join(root, "b"), ForTest: "ex.com/m/b", Imports: []string{"ex.com/m/a"}},
		{ImportPath: "ex.com/m/a [ex.com/m/e.test]", Name: "a", Dir: filepath.Join(root, "a"), ForTest: "ex.com/m/e", Imports: []string{"ex.com/m/x"}},
		test("a", "ex.com/m/a"),
		test("b", "ex.com/m/a", "ex.com/m/b"),
		test("e", "ex.com/m/a", "ex.com/m/b", "ex.com/m/e"),
		test("f", "ex.com/m/a", "ex.com/m/b", "ex.com/m/e", "ex.com/m/f"),
		test("c", "ex.com/m/c"),
	}
	return func(context.Context, string, []string) (io.Reader, error) {
		var sb strings.Builder
		enc := json.NewEncoder(&sb)
		for _, p := range pkgs {
			_ = enc.Encode(p)
		}
		return strings.NewReader(sb.String()), nil
	}
}

func TestComputeAffected_Reach(t *testing.T) {
	for _, tc := range []struct {
		name           string
		reach          *config.CIGateAffectedReach
		changed        string
		want, deferred []string
		wantReach      string
	}{
		{"unset is full transitive reach", nil, "a/a.go", []string{"a", "b", "e", "f"}, nil, ReachAll},
		{"mode all", &config.CIGateAffectedReach{Mode: "all"}, "a/a.go", []string{"a", "b", "e", "f"}, nil, ReachAll},
		{"direct defers transitive past the cap", &config.CIGateAffectedReach{Mode: "direct", TransitiveCap: 1}, "a/a.go", []string{"a", "b"}, []string{"e", "f"}, ReachDirect},
		{"direct cap 0 defers every transitive importer", &config.CIGateAffectedReach{Mode: "direct"}, "a/a.go", []string{"a", "b"}, []string{"e", "f"}, ReachDirect},
		{"direct within the cap runs them all", &config.CIGateAffectedReach{Mode: "direct", TransitiveCap: 2}, "a/a.go", []string{"a", "b", "e", "f"}, nil, ReachDirect},
		// f links a only through b and e: deferred and named, never dropped.
		{"deep link is deferred and named", &config.CIGateAffectedReach{Mode: "direct", TransitiveCap: 1}, "a/a.go", []string{"a", "b"}, []string{"e", "f"}, ReachDirect},
		// A package's own change or an embedded file is direct whatever the cap.
		{"own file changed is direct", &config.CIGateAffectedReach{Mode: "direct"}, "f/f.go", []string{"f"}, nil, ReachDirect},
		{"embedded file changed is direct", &config.CIGateAffectedReach{Mode: "direct"}, "e/tmpl/e.tmpl", []string{"e", "f"}, nil, ReachDirect},
		{"direct with nothing transitive", &config.CIGateAffectedReach{Mode: "direct"}, "e/e.go", []string{"e", "f"}, nil, ReachDirect},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			a := ComputeAffected(context.Background(), AffectedInput{Root: root, Git: fakeGit{branch: "feat", changed: []string{tc.changed}}.run, GoList: reachListed(root), Reach: tc.reach})
			if a.Full {
				t.Fatalf("full: %s", a.Reason)
			}
			full := func(ns []string) []string {
				var out []string
				for _, n := range ns {
					out = append(out, "ex.com/m/"+n)
				}
				return out
			}
			if !slices.Equal(a.Packages, full(tc.want)) || !slices.Equal(a.Deferred, full(tc.deferred)) || a.Reach != tc.wantReach {
				t.Fatalf("packages %v deferred %v reach %s; want %v, %v, %s", a.Packages, a.Deferred, a.Reach, tc.want, tc.deferred, tc.wantReach)
			}
			if len(tc.deferred) > 0 {
				if line := a.Line(); !strings.Contains(line, "deferred to the full gate") || !strings.Contains(line, "ex.com/m/f") {
					t.Errorf("Line does not name the deferred packages: %s", line)
				}
				doc := a.Document()
				if doc == nil || !slices.Equal(doc.Deferred, a.Deferred) || doc.Reach != ReachDirect || !doc.FullGateRequired {
					t.Errorf("document affected = %+v", doc)
				}
				rows := ApplyAffected([]Check{{Name: UnitTests, Argv: []string{"task", "test"}}}, a)
				if !strings.Contains(rows[0].Note, "2 deferred to the full gate") {
					t.Errorf("unit row note = %q", rows[0].Note)
				}
			}
		})
	}
}

// A package an affected rule selects is a change target: its direct
// importer stays direct, even when the cap would defer it.
func TestComputeAffected_ReachRuleSelectedImporterIsDirect(t *testing.T) {
	root := t.TempDir()
	rules := []config.CIGateAffectedRule{{Paths: []string{"docs/**"}, Packages: []string{"./e"}}}
	a := ComputeAffected(context.Background(), AffectedInput{Root: root,
		Git:    fakeGit{branch: "feat", changed: []string{"a/a.go", "docs/x.md"}}.run,
		GoList: reachListed(root), Rules: rules, Reach: &config.CIGateAffectedReach{Mode: "direct"}})
	if a.Full {
		t.Fatalf("full: %s", a.Reason)
	}
	if !slices.Contains(a.Packages, "ex.com/m/f") || slices.Contains(a.Deferred, "ex.com/m/f") {
		t.Fatalf("f imports rule-selected e and must be direct: packages %v deferred %v", a.Packages, a.Deferred)
	}
	if !slices.Contains(a.Packages, "ex.com/m/e") {
		t.Fatalf("rule-selected e missing: %v", a.Packages)
	}
}
