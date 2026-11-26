package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Forge report d542c517: `forge project libraries observe/genai` failed "is
// not a forge/pkg package" and `libraries all` had no genai entry, though
// pkg/observe/genai exists. A package's own subpackages are listed, and
// selectable, by their path under pkg/; an internal/ tree is not.
func TestLibraries_NestedPackagesAreListedAndSelectable(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("observe/observe.go", "// Package observe is the parent.\npackage observe\n")
	write("observe/genai/genai.go", "// Package genai instruments model calls.\npackage genai\n\n// Tracer traces.\ntype Tracer struct{ Name string }\n\n// Start starts.\nfunc (t *Tracer) Start() {}\n")
	write("observe/genai/deeper/deeper.go", "// Package deeper is two levels down.\npackage deeper\n\nfunc Deep() {}\n")
	write("appkit/operatorkit/kit.go", "// Package operatorkit is under a grouping dir.\npackage operatorkit\n")
	write("fluxdelivery/fluxdelivery.go", "// Package fluxdelivery delivers.\npackage fluxdelivery\n")
	write("fluxdelivery/internal/kcleval/eval.go", "// Package kcleval is internal.\npackage kcleval\n")
	write("observe/genai/testdata/x.go", "package x\n")

	pkgs, err := readForgePkgPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range pkgs {
		names = append(names, p.Name)
		if p.ImportPath != forgePkgModule+"/"+p.Name {
			t.Errorf("%s: import path %q", p.Name, p.ImportPath)
		}
	}
	want := "appkit/operatorkit,fluxdelivery,observe,observe/genai,observe/genai/deeper"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("packages = %s, want %s", got, want)
	}

	spec := LibrariesSpec{Module: forgePkgModule, Dir: root, Packages: pkgs}
	if err := attachSignatures(&spec, "observe/genai.Tracer,"+forgePkgModule+"/observe/genai/deeper"); err != nil {
		t.Fatalf("selecting nested packages: %v", err)
	}
	got := map[string][]string{}
	for _, p := range spec.Packages {
		for _, s := range p.Symbols {
			got[p.Name] = append(got[p.Name], s.Name)
		}
	}
	if strings.Join(got["observe/genai"], ",") != "Tracer,Tracer.Start" {
		t.Errorf("observe/genai.Tracer selected %v, want the type and its method", got["observe/genai"])
	}
	if strings.Join(got["observe/genai/deeper"], ",") != "Deep" {
		t.Errorf("observe/genai/deeper selected %v", got["observe/genai/deeper"])
	}
	if len(got["observe"]) != 0 {
		t.Errorf("selecting a subpackage widened to its parent: %v", got["observe"])
	}
	if err := attachSignatures(&spec, "fluxdelivery/internal/kcleval"); err == nil {
		t.Error("an internal package was selectable")
	}
}

// Ranger friction #39: `forge project libraries pkg/observe/metrics` and
// `... metrics` failed "not a forge/pkg package" with a hundred-name list,
// though both name the one registry every project's internal/metrics uses,
// and `observe` printed observe's API without a word about observe/metrics.
// Every spelling naming one real package resolves; a selected package names
// its subpackages; a miss leads with the closest spellings.
func TestLibraries_EverySpellingOfANestedPackageResolves(t *testing.T) {
	t.Parallel()
	dir := repoPkgDir(t)
	pkgs, err := readForgePkgPackages(dir)
	if err != nil {
		t.Fatal(err)
	}
	selected := func(t *testing.T, spec LibrariesSpec) []string {
		t.Helper()
		var names []string
		for _, p := range spec.Packages {
			if len(p.Symbols) > 0 {
				names = append(names, p.Name)
			}
		}
		return names
	}

	for _, spelling := range []string{
		"observe/metrics",
		"pkg/observe/metrics",
		"forge/pkg/observe/metrics",
		forgePkgModule + "/observe/metrics",
		"metrics",
		"observe/metrics/",
	} {
		t.Run(spelling, func(t *testing.T) {
			spec := LibrariesSpec{Module: forgePkgModule, Dir: dir, Packages: pkgs}
			if err := attachSignatures(&spec, spelling); err != nil {
				t.Fatalf("%s names pkg/observe/metrics but was refused: %v", spelling, err)
			}
			if got := strings.Join(selected(t, spec), ","); got != "observe/metrics" {
				t.Errorf("%s selected %q, want observe/metrics", spelling, got)
			}
		})
	}

	t.Run("symbol on a bare last element", func(t *testing.T) {
		spec := LibrariesSpec{Module: forgePkgModule, Dir: dir, Packages: pkgs}
		if err := attachSignatures(&spec, "metrics.Registry"); err != nil {
			t.Fatalf("metrics.Registry: %v", err)
		}
		if got := strings.Join(selected(t, spec), ","); got != "observe/metrics" {
			t.Errorf("metrics.Registry selected %q", got)
		}
	})

	t.Run("a parent names its subpackages", func(t *testing.T) {
		spec := LibrariesSpec{Module: forgePkgModule, Dir: dir, Packages: pkgs}
		if err := attachSignatures(&spec, "observe"); err != nil {
			t.Fatal(err)
		}
		var subs []string
		for _, p := range spec.Packages {
			if p.Name == "observe" {
				subs = p.Subpackages
			} else if len(p.Subpackages) > 0 {
				t.Errorf("unselected %s carries subpackages %v", p.Name, p.Subpackages)
			}
		}
		if !slices.Contains(subs, "observe/metrics") || !slices.Contains(subs, "observe/genai") {
			t.Fatalf("observe's subpackages = %v, want observe/metrics and observe/genai among them", subs)
		}
		var buf bytes.Buffer
		if err := writeLibraries(&buf, spec, false); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), "Subpackages of observe") || !strings.Contains(buf.String(), "observe/metrics") {
			t.Errorf("text output does not name observe's subpackages:\n%s", buf.String())
		}
	})

	t.Run("a miss leads with the closest spellings", func(t *testing.T) {
		for _, tc := range []struct{ arg, want string }{
			{"obsreve/metrics", "Did you mean: observe/metrics"},
			{"pkg/observe/metric", "Did you mean: observe/metrics"},
		} {
			spec := LibrariesSpec{Module: forgePkgModule, Dir: dir, Packages: pkgs}
			err := attachSignatures(&spec, tc.arg)
			if err == nil {
				t.Fatalf("%s matched nothing but was accepted", tc.arg)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.want) {
				t.Errorf("%s: error does not lead with %q:\n%s", tc.arg, tc.want, msg)
			}
			// The full list still follows, for the scripts that read it.
			if !strings.Contains(msg, "Available: ") {
				t.Errorf("%s: error dropped the Available list:\n%s", tc.arg, msg)
			}
		}
	})
}

// A grouping directory and a shared last element, on a synthetic tree: the
// first is refused with its packages listed, the second refused rather than
// guessed, with both candidates named; an exact name still wins.
func TestLibraries_AmbiguousAndGroupingNamesAreRefusedWithCandidates(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for rel, body := range map[string]string{
		"group/alpha/a.go":  "// Package alpha is one.\npackage alpha\n\nfunc A() {}\n",
		"group/beta/b.go":   "// Package beta is two.\npackage beta\n\nfunc B() {}\n",
		"one/native/n.go":   "// Package native is first.\npackage native\n\nfunc N() {}\n",
		"two/native/n.go":   "// Package native is second.\npackage native\n\nfunc N() {}\n",
		"components/c.go":   "// Package components is top-level.\npackage components\n\nfunc C() {}\n",
		"x/components/c.go": "// Package components is nested.\npackage components\n\nfunc C() {}\n",
		"one/one.go":        "// Package one is a parent.\npackage one\n\nfunc O() {}\n",
		"two/two.go":        "// Package two is a parent.\npackage two\n\nfunc T() {}\n",
		"x/x.go":            "// Package x is a parent.\npackage x\n\nfunc X() {}\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pkgs, err := readForgePkgPackages(root)
	if err != nil {
		t.Fatal(err)
	}

	spec := LibrariesSpec{Module: forgePkgModule, Dir: root, Packages: pkgs}
	err = attachSignatures(&spec, "group")
	if err == nil || !strings.Contains(err.Error(), "group holds these packages — name one: group/alpha, group/beta") {
		t.Errorf("a grouping directory did not list its packages: %v", err)
	}

	spec = LibrariesSpec{Module: forgePkgModule, Dir: root, Packages: pkgs}
	err = attachSignatures(&spec, "native")
	if err == nil {
		t.Fatal("a last element two packages share was resolved to one of them")
	}
	if !strings.Contains(err.Error(), "one/native") || !strings.Contains(err.Error(), "two/native") {
		t.Errorf("the ambiguity error does not name both candidates: %v", err)
	}

	spec = LibrariesSpec{Module: forgePkgModule, Dir: root, Packages: pkgs}
	if err := attachSignatures(&spec, "components"); err != nil {
		t.Fatalf("an exact top-level name was refused: %v", err)
	}
	for _, p := range spec.Packages {
		if (len(p.Symbols) > 0) != (p.Name == "components") {
			t.Errorf("components selected %s=%v; want only the top-level package", p.Name, len(p.Symbols) > 0)
		}
	}
}

// The real tree: observe/genai is in the inventory.
func TestReadForgePkgPackages_ListsObserveGenai(t *testing.T) {
	t.Parallel()
	pkgs, err := readForgePkgPackages(repoPkgDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		if p.Name == "observe/genai" {
			return
		}
	}
	t.Fatal("observe/genai is missing from the inventory")
}
