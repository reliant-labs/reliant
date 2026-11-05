package kclplugin_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// fp.dev_stacks() is consumed from KCL, so the contract that matters is what a
// MODULE sees when it calls it — not what the Go hook returns. These tests
// evaluate real KCL through forge's own render seam (kclrender.Run, the path
// every forge command takes) rather than asserting on the hook directly.
//
// What it guards: control-plane's dev NATS generator emits one account per
// active dev stack. It used to derive that roster by parsing
// .forge/blocks.json, which memoizes a block for ANY allocate_port key — so
// prod's reliant-web port key ("prod") rendered a dev NATS account named
// CP_prod into a TRACKED file, and a `forge generate` on a machine that had
// once run `forge env up prod` produced different bytes than one on a fresh
// clone.

// writeModule lays down a minimal KCL module whose output is driven by
// fp.dev_stacks(), mirroring how the real generator consumes it.
func writeModule(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"kcl.mod": "[package]\nname = \"devstacks_probe\"\n",
		"main.k": `import kcl_plugin.forge as fp

stacks = fp.dev_stacks()
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

// renderStacks evaluates the probe module and returns the rendered YAML.
func renderStacks(t *testing.T, dir string) string {
	t.Helper()
	out, err := kclrender.Run(dir, dir, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return string(out)
}

// TestDevStacksEmptyWhenUnarmed is the purity lock. `forge generate` and
// `forge ci` never arm the hook, so a module that generates a TRACKED file
// from this builtin must produce the same bytes on every machine regardless of
// which worktrees happen to exist locally.
func TestDevStacksEmptyWhenUnarmed(t *testing.T) {
	kclplugin.UseDevStacks(nil) // the generate-path state
	t.Cleanup(func() { kclplugin.UseDevStacks(nil) })

	dir := t.TempDir()
	writeModule(t, dir)

	got := renderStacks(t, dir)
	if !strings.Contains(strings.ReplaceAll(got, " ", ""), `"stacks":[]`) {
		t.Errorf("unarmed dev_stacks() must render an EMPTY roster so generate stays a pure\n"+
			"function of committed inputs; got:\n%s", got)
	}
}

// TestDevStacksReturnsRosterWhenArmed is the other half: on the up/deploy path
// the builtin must actually deliver the stacks, or scoping the roster would
// simply break per-stack config generation.
func TestDevStacksReturnsRosterWhenArmed(t *testing.T) {
	kclplugin.UseDevStacks(func() ([]string, error) {
		return []string{"wt-feat", "wt-other"}, nil
	})
	t.Cleanup(func() { kclplugin.UseDevStacks(nil) })

	dir := t.TempDir()
	writeModule(t, dir)

	got := renderStacks(t, dir)
	for _, want := range []string{"wt-feat", "wt-other"} {
		if !strings.Contains(got, want) {
			t.Errorf("armed dev_stacks() dropped stack %q; got:\n%s", want, got)
		}
	}
	// The incident signature: a port-block key must never reach KCL as a
	// stack. The hook is what filters it, so a leak here means the roster is
	// being sourced from the raw registry again.
	if strings.Contains(got, "prod") {
		t.Errorf("a port-block key leaked into the KCL-visible roster:\n%s", got)
	}
}
