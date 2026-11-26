package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const noObserveContract = "package billing\n\n// forge:no-observe\ntype Service interface{ Charge() }\n"

const observeChainSeam = "package billing\n\nimport \"github.com/reliant-labs/forge/pkg/observe\"\n\nfunc newObserveChain() *observe.ComponentChain { return observe.DefaultComponentChain(\"billing\") }\n"

const staleDecorator = "package billing\n\nvar _ = newObserveChain()\n"

func writeObservePackage(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A package that opts out with forge:no-observe loses its generated decorator,
// the only caller of its owned newObserveChain seam. forge must name the seam
// (golangci `unused` fails on it otherwise) and must not delete it.
func TestRemoveObservedDecorator_NoObserveNamesUnusedSeam(t *testing.T) {
	t.Parallel()
	dir := writeObservePackage(t, map[string]string{
		"contract.go":       noObserveContract,
		"observe_chain.go":  observeChainSeam,
		"middleware_gen.go": staleDecorator,
	})
	var out bytes.Buffer
	if err := removeObservedDecorator(dir, "internal/billing", &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "middleware_gen.go")); !os.IsNotExist(err) {
		t.Fatalf("middleware_gen.go should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "observe_chain.go")); err != nil {
		t.Fatalf("observe_chain.go is the user's file and must survive: %v", err)
	}
	got := out.String()
	for _, want := range []string{"internal/billing/observe_chain.go", "forge:no-observe", "newObserveChain"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning %q does not mention %q", got, want)
		}
	}
}

func TestRemoveObservedDecorator_NoWarningWithoutUnusedSeam(t *testing.T) {
	t.Parallel()
	cases := map[string]map[string]string{
		"no seam": {
			"contract.go":       noObserveContract,
			"middleware_gen.go": staleDecorator,
		},
		"seam still called by hand-written code": {
			"contract.go":      noObserveContract,
			"observe_chain.go": observeChainSeam,
			"service.go":       "package billing\n\nvar chain = newObserveChain()\n",
		},
		// Not opted out (e.g. a handler-shaped package): the warning's
		// forge:no-observe explanation would be false.
		"no forge:no-observe": {
			"contract.go":      "package billing\n\ntype Service interface{ Charge() }\n",
			"observe_chain.go": observeChainSeam,
		},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			if err := removeObservedDecorator(writeObservePackage(t, files), "internal/billing", &out); err != nil {
				t.Fatal(err)
			}
			if out.Len() != 0 {
				t.Fatalf("unexpected warning: %q", out.String())
			}
		})
	}
}
