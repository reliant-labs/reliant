package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

func TestGraphCommandsRetireOnlyOwnedBindingsWithRemovedInputs(t *testing.T) {
	root := t.TempDir()
	writeAccessorFixture(t, root, map[string]string{
		"go.mod":                    "module example.com/app\n\ngo 1.26\n",
		"internal/app/providers.go": "package app\n\ntype Infra struct{}\n",
		"internal/app/lifecycle.go": ownedCompositionApp(""),
		// Customized owned ServeSpec is not evidence of generator ownership.
		"cmd/app/cmd/workers/mailer.go":    "package workers\n\nvar _ = workerMailer(nil) // custom ServeSpec remains owned\n",
		"cmd/app/cmd/workers/active.go":    "package workers\n\nvar _ = workerActive(nil)\n",
		"cmd/app/cmd/operators/fleet.go":   "package operators\n\nvar _ = operatorFleet(nil)\n",
		"cmd/app/cmd/workers/keep_test.go": "package workers\n// retain owned assertions\n",
	})
	cs, err := checksums.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := GenerateComponentAccessors(root, "app", cs); err != nil {
		t.Fatal(err)
	}
	mailerPath := componentAccessorPath("app", "workers", "mailer")
	original, err := os.ReadFile(filepath.Join(root, mailerPath))
	if err != nil {
		t.Fatal(err)
	}
	// Legacy projects are not swept even when an owned command disappears.
	if err := os.Remove(filepath.Join(root, "cmd/app/cmd/workers/mailer.go")); err != nil {
		t.Fatal(err)
	}
	if err := GenerateCmdGroups(CmdServiceGroupInput{Bin: "app"}, root, cs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, mailerPath)); err != nil {
		t.Fatal("legacy binding unexpectedly retired", err)
	}
	// The actual input transition: legacy construction is removed, explicit roots remain.
	writeAccessorFixture(t, root, map[string]string{"internal/app/providers.go": "package app\n\n//forge:root jobs command=jobs\ntype Jobs struct{}\n"})
	if err := os.Remove(filepath.Join(root, "cmd/app/cmd/operators/fleet.go")); err != nil {
		t.Fatal(err)
	}
	// Matching suffix/header alone is not enough: source identity and variable must match.
	forged := strings.ReplaceAll(string(original), "mailer", "custom")
	writeAccessorFixture(t, root, map[string]string{
		"cmd/app/cmd/workers/custom_accessor_gen.go": strings.ReplaceAll(forged, "workerMailer", "workerDifferent"),
		"cmd/app/cmd/workers/hand_accessor_gen.go":   "package workers\nvar workerHand = 1\n",
	})
	// A stamped file edited by its owner is not forge's to delete.
	edited := componentAccessorPath("app", "workers", "edited")
	if _, err := checksums.WriteDerivedFileOutcome(root, edited, []byte(strings.ReplaceAll(strings.ReplaceAll(string(original), "mailer", "edited"), "workerMailer", "workerEdited")), cs, false); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(root, edited), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("\n// owner edit\n"); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	// Explicit disown wins even for otherwise pristine template content.
	disowned := componentAccessorPath("app", "workers", "disowned")
	writeAccessorFixture(t, root, map[string]string{disowned: strings.ReplaceAll(strings.ReplaceAll(string(original), "mailer", "disowned"), "workerMailer", "workerDisowned")})
	cs.Disowned[disowned] = checksums.DisownedEntry{}
	for i := 0; i < 2; i++ {
		if err := GenerateCmdGroups(CmdServiceGroupInput{Bin: "app"}, root, cs); err != nil {
			t.Fatal(err)
		}
		for _, rel := range []string{mailerPath, componentAccessorPath("app", "operators", "fleet")} {
			if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
				t.Fatalf("retired binding still present %s: %v", rel, err)
			}
		}
		for _, rel := range []string{componentAccessorPath("app", "workers", "active"), "cmd/app/cmd/workers/active.go", "cmd/app/cmd/workers/keep_test.go", "cmd/app/cmd/workers/custom_accessor_gen.go", "cmd/app/cmd/workers/hand_accessor_gen.go", edited, disowned} {
			if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
				t.Fatalf("owned/unrelated input lost %s: %v", rel, err)
			}
		}
	}
}
