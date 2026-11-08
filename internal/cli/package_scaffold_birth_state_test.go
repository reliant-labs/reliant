package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/contractcheck"
)

// TestScaffoldedPackagesPassTheirOwnConventionRules is the birth-state guard:
// every shape `forge scaffold package` can emit must survive the contract-check
// rules forge runs against it, on the day it is born, before the author has
// typed anything.
//
// It exists because a rule and a scaffold drift apart silently. When
// forgeconv-deps-are-interfaces stopped being opt-in and started applying to
// every package, the adapter scaffold's own `HTTPClient *http.Client` started
// tripping it — forge telling the author that the file forge had just written
// was wrong. A warning nobody can act on (the scaffold IS the recommended
// shape) is how a linter gets muted.
//
// The set of shapes is enumerated from the flags themselves, so a shape added
// later is covered the day the flag accepts it.
func TestScaffoldedPackagesPassTheirOwnConventionRules(t *testing.T) {
	shapes := []struct {
		flag  string // "type" or "kind"
		value string
	}{
		{"type", "service"},
		{"type", "adapter"},
		{"kind", "eventbus"},
		{"kind", "client"},
	}

	// Guard the guard: every accepted --type / --kind value must appear above,
	// or a newly added shape would be born unchecked.
	covered := map[string]bool{}
	for _, s := range shapes {
		covered[s.flag+"="+s.value] = true
	}
	for v := range validPackageTypes {
		if !covered["type="+v] {
			t.Fatalf("--type=%s is accepted but not covered here; add it so its birth state is checked", v)
		}
	}
	for v := range validPackageKinds {
		if !covered["kind="+v] {
			t.Fatalf("--kind=%s is accepted but not covered here; add it so its birth state is checked", v)
		}
	}

	for _, shape := range shapes {
		t.Run(shape.flag+"="+shape.value, func(t *testing.T) {
			dir := t.TempDir()
			orig, _ := os.Getwd()
			defer func() { _ = os.Chdir(orig) }()
			if err := os.Chdir(dir); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile("forge.yaml",
				[]byte("name: testproject\nmodule_path: example.com/testproject\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile("go.mod",
				[]byte("module example.com/testproject\n\ngo 1.24\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			writeAdapterConfigFixture(t, dir)
			cmd := &cobra.Command{Use: "new <name>", Args: cobra.ExactArgs(1), RunE: runPackageNew}
			cmd.Flags().String("kind", "", "")
			cmd.Flags().String("type", "service", "")
			if err := cmd.Flags().Set(shape.flag, shape.value); err != nil {
				t.Fatal(err)
			}
			if err := cmd.RunE(cmd, []string{"widgets"}); err != nil {
				t.Fatalf("scaffold --%s %s: %v", shape.flag, shape.value, err)
			}

			fs, err := contractcheck.Inspect(context.Background(), dir, contractcheck.Options{})
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if len(fs) != 0 {
				t.Errorf("a freshly scaffolded --%s %s package must produce ZERO contract-check findings; got %d:\n%s\n"+
					"Fix the RULE if the scaffold is the shape forge recommends, or fix the SCAFFOLD if the rule is right — "+
					"never leave forge warning about the file forge just wrote.",
					shape.flag, shape.value, len(fs), contractcheck.AsResult(fs).FormatText())
			}

			// The stub mock must exist for every shape that got a contract.go.
			// Scaffolding without it is how a hand-rolled fake gets written
			// next to a mock that was one command away.
			if fileExists(filepath.Join(dir, "internal", "widgets", "contract.go")) {
				mock := filepath.Join(dir, "internal", "widgets", "mock_gen.go")
				if !fileExists(mock) {
					t.Errorf("--%s %s scaffolded a contract.go but no mock_gen.go; an author who sees no mock writes a fake",
						shape.flag, shape.value)
				}
			}
		})
	}
}
