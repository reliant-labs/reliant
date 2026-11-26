package cigate

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

func TestAffectedBuildAndWhy(t *testing.T) {
	rules := []config.CIGateAffectedRule{{Paths: []string{"docs/**"}, Packages: []string{"./c"}}}
	tests := []struct {
		name      string
		changed   []string
		ignore    []string
		wantPkgs  []string
		wantBuild []string
		wantWhy   []PackageWhy
		wantFiles []FileReport
	}{
		{
			name:      "a change reaches its importers by the test binary and the build",
			changed:   []string{"a/x.go"},
			wantPkgs:  []string{"ex.com/m/a", "ex.com/m/b"},
			wantBuild: []string{"ex.com/m/a", "ex.com/m/b"},
			wantWhy: []PackageWhy{
				{Package: "ex.com/m/a", File: "a/x.go", Edge: "changed"},
				{Package: "ex.com/m/b", File: "a/x.go", Edge: "imports ex.com/m/a"},
			},
			wantFiles: []FileReport{{Path: "a/x.go", Package: "ex.com/m/a", Note: "package ex.com/m/a"}},
		},
		{
			name: "a package with no tests is in the build set only",
			// nested/d has no test binary: never tested, still compiled.
			changed:   []string{"nested/d/d.go"},
			wantBuild: []string{"ex.com/m/nested/d"},
			wantWhy:   []PackageWhy{},
			wantFiles: []FileReport{{Path: "nested/d/d.go", Package: "ex.com/m/nested/d", Note: "package ex.com/m/nested/d"}},
		},
		{
			name:     "a rule's package names the rule and the file it claimed",
			changed:  []string{"README.md", "docs/a.md"},
			ignore:   []string{"*.md"},
			wantPkgs: []string{"ex.com/m/c"},
			wantWhy:  []PackageWhy{{Package: "ex.com/m/c", File: "docs/a.md", Edge: "ci.gate.affected_rules[0]"}},
			wantFiles: []FileReport{
				{Path: "README.md", Ignored: true, Note: "ci.gate.affected_ignore"},
				{Path: "docs/a.md", Rules: []int{0}, Ignored: true, Note: "ci.gate.affected_rules[0]; ci.gate.affected_ignore"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			a := ComputeAffected(context.Background(), AffectedInput{Root: root, Git: fakeGit{branch: "feature", changed: tc.changed}.run,
				GoList: listed(root), Ignore: tc.ignore, Rules: rules})
			if a.Full {
				t.Fatalf("full: %s", a.Reason)
			}
			if !reflect.DeepEqual(a.Packages, tc.wantPkgs) {
				t.Errorf("packages = %v, want %v", a.Packages, tc.wantPkgs)
			}
			if !reflect.DeepEqual(a.Build, tc.wantBuild) {
				t.Errorf("build = %v, want %v", a.Build, tc.wantBuild)
			}
			if !reflect.DeepEqual(a.Why, tc.wantWhy) {
				t.Errorf("why = %+v, want %+v", a.Why, tc.wantWhy)
			}
			if !reflect.DeepEqual(a.Files, tc.wantFiles) {
				t.Errorf("files = %+v, want %+v", a.Files, tc.wantFiles)
			}
		})
	}
}

func TestAffectedFilesWhatIf(t *testing.T) {
	root := t.TempDir()
	git := func(context.Context, string, ...string) (string, error) {
		t.Fatal("a what-if must not run git")
		return "", nil
	}
	a := ComputeAffected(context.Background(), AffectedInput{Root: root, Git: git, GoList: listed(root),
		Files: []string{"b/b.go", "b/b.go"}})
	if a.Full || !reflect.DeepEqual(a.Packages, []string{"ex.com/m/b"}) || !reflect.DeepEqual(a.Changed, []string{"b/b.go"}) {
		t.Fatalf("got full=%v %q packages=%v changed=%v", a.Full, a.Reason, a.Packages, a.Changed)
	}
	// An unclaimed file still sends it whole, and its file report says so.
	a = ComputeAffected(context.Background(), AffectedInput{Root: root, Git: git, GoList: listed(root), Files: []string{"go.mod"}})
	if !a.Full || len(a.Files) != 1 || !strings.Contains(a.Files[0].Note, "unclaimed") {
		t.Fatalf("got full=%v files=%+v", a.Full, a.Files)
	}
}

func TestDocumentCarriesAffected(t *testing.T) {
	if doc := NewDocument(Report{}, DocumentInput{Ran: true}); doc.Affected != nil {
		t.Fatalf("no decision: affected = %+v, want absent", doc.Affected)
	}
	a := &Affected{BaseRef: "origin/main", Base: "0123", Changed: []string{"a/x.go"}, Packages: []string{"ex.com/m/a"},
		Build: []string{"ex.com/m/a"}, Why: []PackageWhy{{Package: "ex.com/m/a", File: "a/x.go", Edge: "changed"}}}
	b, err := json.Marshal(NewDocument(Report{}, DocumentInput{Ran: true, Affected: a}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"affected":{`, `"build":["ex.com/m/a"]`, `"edge":"changed"`, `"files":[]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("document lacks %s: %s", want, b)
		}
	}
}
