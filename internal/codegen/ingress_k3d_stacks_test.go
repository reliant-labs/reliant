package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The k3d LB can only forward a host port that was mapped at cluster-CREATE
// time, so every port a parallel dev stack might bind has to be pre-mapped.
//
// Incident this locks (control-plane): block 0's ports were DERIVED from the
// dev render into deploy/k3d-ports.yaml, while blocks 1..7 were a
// hand-maintained `ports:` list in the user-owned deploy/k3d.yaml, under a
// comment ending "raise it here alone if a stack lands past block 7". So the
// parallel-stack ceiling was stated twice — implicitly as "unbounded" in the
// allocator, literally as a hand-written range in k3d.yaml — with nothing
// tying them together. The 8th worktree allocated a block cleanly and then
// failed as an unrelated "gateway unreachable", because its ports were never
// mapped.
//
// Deriving the whole range from dev_stack.max_stacks makes the pre-map and the
// allocator two views of ONE number.

// readFragment returns the generated fragment for a set of listeners.
func readFragment(t *testing.T, in K3dPortsGenInput) string {
	t.Helper()
	if err := GenerateK3dPorts(in); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(in.ProjectDir, "deploy", "k3d-ports.yaml"))
	if err != nil {
		t.Fatalf("read fragment: %v", err)
	}
	return string(raw)
}

// TestGenerateK3dPorts_MapsEveryReachableStack is the core lock: with a
// ceiling of N, every listener must be mapped at every offset a stack can
// actually take, or that stack is unreachable.
func TestGenerateK3dPorts_MapsEveryReachableStack(t *testing.T) {
	tmp := t.TempDir()
	got := readFragment(t, K3dPortsGenInput{
		GenContext: GenContext{ProjectDir: tmp},
		Listeners: []K3dListener{
			{GatewayName: "public", ListenerName: "http", Port: 28080},
			{GatewayName: "public", ListenerName: "grpc", Port: 29190},
		},
		MaxStacks: 4,
		BlockSize: 100,
	})

	// Every listener at every reachable offset.
	for stack := 0; stack < 4; stack++ {
		for _, base := range []int{28080, 29190} {
			port := base + stack*100
			want := fmt.Sprintf("- port: %d:%d", port, port)
			if !strings.Contains(got, want) {
				t.Errorf("stack %d's port %d is NOT pre-mapped — that stack's gateway would be "+
					"unreachable with no indication why.\nfragment:\n%s", stack, port, got)
			}
		}
	}

	// And nothing past the ceiling: mapping ports no stack can be issued
	// would silently reserve host ports other software could want.
	for _, beyond := range []int{28480, 29590} {
		if strings.Contains(got, fmt.Sprintf("- port: %d:%d", beyond, beyond)) {
			t.Errorf("port %d is past the %d-stack ceiling but was mapped anyway", beyond, 4)
		}
	}
}

// TestGenerateK3dPorts_DefaultStackOnlyIsHistorical pins that a project which
// does not use parallel stacks gets exactly the old single-block output. This
// is what makes adopting the ceiling a no-op for existing projects.
func TestGenerateK3dPorts_DefaultStackOnlyIsHistorical(t *testing.T) {
	listeners := []K3dListener{
		{GatewayName: "public", ListenerName: "http", Port: 28080},
	}

	unsetDir := t.TempDir()
	unset := readFragment(t, K3dPortsGenInput{
		GenContext: GenContext{ProjectDir: unsetDir},
		Listeners:  listeners,
		// MaxStacks/BlockSize deliberately zero — the legacy caller shape.
	})
	oneDir := t.TempDir()
	one := readFragment(t, K3dPortsGenInput{
		GenContext: GenContext{ProjectDir: oneDir},
		Listeners:  listeners,
		MaxStacks:  1,
		BlockSize:  100,
	})

	if unset != one {
		t.Errorf("an unset ceiling must render exactly like a 1-stack ceiling.\nunset:\n%s\none:\n%s", unset, one)
	}
	if strings.Contains(unset, "28180") {
		t.Errorf("a project with no parallel stacks must not gain extra port mappings:\n%s", unset)
	}
}

// TestGenerateK3dPorts_RespectsBlockSize pins that the offset arithmetic uses
// the configured quantum rather than a hardcoded 100 — the two numbers are
// halves of one invariant (see the allocate-port-spacing lint).
func TestGenerateK3dPorts_RespectsBlockSize(t *testing.T) {
	tmp := t.TempDir()
	got := readFragment(t, K3dPortsGenInput{
		GenContext: GenContext{ProjectDir: tmp},
		Listeners:  []K3dListener{{GatewayName: "public", ListenerName: "http", Port: 30000}},
		MaxStacks:  3,
		BlockSize:  250,
	})
	for _, want := range []int{30000, 30250, 30500} {
		if !strings.Contains(got, fmt.Sprintf("- port: %d:%d", want, want)) {
			t.Errorf("port %d missing with block_size=250:\n%s", want, got)
		}
	}
	if strings.Contains(got, "30100") {
		t.Errorf("block_size=250 was ignored; output used the default 100:\n%s", got)
	}
}

// TestGenerateK3dPorts_StackRangeDeterministic keeps the fragment idempotent
// across renders — it is a tracked generated file, so a nondeterministic
// ordering would show up as phantom diffs in every checkout.
func TestGenerateK3dPorts_StackRangeDeterministic(t *testing.T) {
	in := func(dir string) K3dPortsGenInput {
		return K3dPortsGenInput{
			GenContext: GenContext{ProjectDir: dir},
			Listeners: []K3dListener{
				{GatewayName: "public", ListenerName: "grpc", Port: 29190},
				{GatewayName: "public", ListenerName: "http", Port: 28080},
				{GatewayName: "public", ListenerName: "controller", Port: 28085},
			},
			MaxStacks: 5,
			BlockSize: 100,
		}
	}
	a := readFragment(t, in(t.TempDir()))
	b := readFragment(t, in(t.TempDir()))
	if a != b {
		t.Errorf("fragment is not deterministic across renders:\n--- a ---\n%s\n--- b ---\n%s", a, b)
	}
}
