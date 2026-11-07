package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/devstack"
)

// `forge env devstack list` PRINTED ONE LINE FOR EIGHT HELD BLOCKS.
//
// The ceiling counts BLOCKS, and its error message tells the reader to run
// `devstack list` to see who holds them. But list printed only Stack == true
// entries, so in control-plane — eight blocks held, one of them a dev stack —
// it printed a single line. The command the error recommended contradicted the
// error, and the reader had no way to see the other seven without hand-reading
// .forge/blocks.json.
//
// list now prints every holder, labelled by kind and by whether prune could
// ever reclaim it. The unlabelled stacks-only roster a per-stack config
// generator consumes moves behind --stacks-only, because that consumer must
// keep seeing strictly worktrees: a generator that enumerated the raw registry
// and treated every key as a worktree is what put a dev NATS account for a prod
// web port into a tracked config file.

// devstackListIn runs `forge env devstack list` with cwd set to a project dir
// holding a registry, and returns stdout.
func devstackListIn(t *testing.T, projectDir string, args ...string) string {
	t.Helper()
	t.Chdir(projectDir)
	cmd := newDevStackListCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("devstack list %v: %v\n%s", args, err, out.String())
	}
	return out.String()
}

// seedRegistry writes a forge.yaml (so projectDirForKCL resolves here) and a
// registry with the exact control-plane shape: a default block, a dev stack, a
// standalone port-block key, and a derived key whose worktree is recorded.
func seedRegistry(t *testing.T, jsonText string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: listtest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".forge", "blocks.json"), []byte(jsonText), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const controlPlaneRegistry = `{
  "": {"block": 0},
  "prod": {"block": 1},
  "prod-cp-obs": {"block": 5, "origin": "cp-obs"},
  "my-new-feature-50f77334": {"block": 6, "stack": true}
}`

// TestDevStackListShowsEveryBlockHolder is the regression lock: every held
// block appears, not just the dev stacks.
func TestDevStackListShowsEveryBlockHolder(t *testing.T) {
	out := devstackListIn(t, seedRegistry(t, controlPlaneRegistry))

	for _, want := range []string{"prod", "prod-cp-obs", "my-new-feature-50f77334", "(default stack)"} {
		if !strings.Contains(out, want) {
			t.Errorf("`devstack list` does not show block holder %q — this is the "+
				"one-line-for-eight-blocks defect:\n%s", want, out)
		}
	}
	if lines := strings.Count(strings.TrimSpace(out), "\n") + 1; lines != 4 {
		t.Errorf("`devstack list` printed %d lines for 4 held blocks:\n%s", lines, out)
	}
}

// TestDevStackListLabelsReclaimability: a reader at a full ceiling needs to
// know which holders prune could act on, which is the question the old output
// could not answer at all.
func TestDevStackListLabelsReclaimability(t *testing.T) {
	out := devstackListIn(t, seedRegistry(t, controlPlaneRegistry))

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		switch {
		case strings.Contains(line, "prod-cp-obs"):
			if !strings.Contains(line, "cp-obs") || !strings.Contains(line, "derived") {
				t.Errorf("derived key's line does not name its origin worktree: %q", line)
			}
		case strings.Contains(line, "block 1:"): // the standalone "prod"
			if !strings.Contains(line, "NOT reclaim") {
				t.Errorf("standalone key's line does not say prune will never reclaim it: %q", line)
			}
		case strings.Contains(line, "my-new-feature-50f77334"):
			if !strings.Contains(line, "dev stack") {
				t.Errorf("dev-stack line is not labelled as one: %q", line)
			}
		}
	}
}

// TestDevStackListStacksOnlyIsTheUnchangedGeneratorRoster: --stacks-only must
// remain byte-identical to the old default output — bare worktree keys, one per
// line, no labels, no default stack — because a per-stack config generator
// parses it and feeding it a port-block key is a tracked-file corruption bug.
func TestDevStackListStacksOnlyIsTheUnchangedGeneratorRoster(t *testing.T) {
	out := devstackListIn(t, seedRegistry(t, controlPlaneRegistry), "--stacks-only")

	if out != "my-new-feature-50f77334\n" {
		t.Fatalf("--stacks-only output = %q, want exactly the bare dev-stack roster "+
			"(a generator parses this; a port-block key here renders junk into tracked config)", out)
	}
}

// TestDevStackListEmptyRegistrySaysSo: an empty registry printing nothing was
// indistinguishable from the command failing — the exact ambiguity that made
// "the ceiling says 8, list says nothing" so hard to read.
func TestDevStackListEmptyRegistrySaysSo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: listtest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := devstackListIn(t, dir)
	if !strings.Contains(out, "no port blocks allocated") {
		t.Errorf("empty registry printed %q, want an explicit statement that nothing is allocated", out)
	}
}

// TestBlockKindDistinguishesDerivedFromStandalone pins the labelling itself at
// the package boundary, since both `devstack list` and the ceiling message
// render through it — the two disagreeing is the defect this shares code to
// prevent.
func TestBlockKindDistinguishesDerivedFromStandalone(t *testing.T) {
	standalone := devstack.Block{Key: "prod", Index: 1}
	derived := devstack.Block{Key: "prod-cp-obs", Index: 5, Origin: "cp-obs"}

	if !strings.Contains(standalone.Kind(), "NOT reclaim") {
		t.Errorf("standalone key kind = %q, want it to say prune will not reclaim", standalone.Kind())
	}
	if strings.Contains(derived.Kind(), "NOT reclaim") {
		t.Errorf("derived key kind = %q, but its block IS reclaimable once cp-obs is gone", derived.Kind())
	}
	if !strings.Contains(derived.Kind(), "cp-obs") {
		t.Errorf("derived key kind = %q, want it to name the origin worktree", derived.Kind())
	}
}
