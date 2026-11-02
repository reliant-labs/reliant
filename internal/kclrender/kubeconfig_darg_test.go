package kclrender

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestWithKubeconfigDArg_BindsTheResolvedPath pins that every render gets the
// `kubeconfig` binding that backs forge.default_kubeconfig(), and through it
// Cluster.kubeconfig.
//
// Quoted, for the same reason image_tag is quoted (internal/cluster.renderDArgs):
// an unquoted `-D` value is type-inferred by KCL, and the field it lands in is
// typed `str`.
func TestWithKubeconfigDArg_BindsTheResolvedPath(t *testing.T) {
	want := filepath.Join(t.TempDir(), "config")
	t.Setenv("KUBECONFIG", want)

	got := withKubeconfigDArg([]string{"env=dev"})
	if len(got) != 2 || got[0] != "env=dev" {
		t.Fatalf("dArgs = %v; the caller's own bindings must be preserved in order", got)
	}
	if got[1] != "kubeconfig="+strconv.Quote(want) {
		t.Fatalf("dArgs[1] = %q; want a QUOTED kubeconfig binding for %q", got[1], want)
	}
}

// TestWithKubeconfigDArg_DoesNotOverrideAnExplicitBinding: an explicit
// `kubeconfig=` from a caller wins. Nothing passes one today, but a derived
// value silently replacing a stated one is the failure mode that makes a
// declaration untrustworthy.
func TestWithKubeconfigDArg_DoesNotOverrideAnExplicitBinding(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "derived"))

	in := []string{"env=dev", `kubeconfig="/explicit/config"`}
	got := withKubeconfigDArg(in)
	if len(got) != len(in) {
		t.Fatalf("dArgs = %v; an explicit binding must not be joined by a derived one", got)
	}
	if got[1] != `kubeconfig="/explicit/config"` {
		t.Fatalf("dArgs[1] = %q; the explicit binding was overwritten", got[1])
	}
}

// TestWithKubeconfigDArg_DoesNotMutateTheCallersSlice. Both render paths build
// their dArgs by appending to a shared slice (devstack.ActiveDArgs's result),
// so appending in place here could let one render's binding leak into the
// next — a cross-render contamination that would appear only under a command
// that renders several envs in a loop.
func TestWithKubeconfigDArg_DoesNotMutateTheCallersSlice(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "config"))

	// Capacity to spare is what makes an in-place append observable.
	in := make([]string, 1, 8)
	in[0] = "env=dev"

	out := withKubeconfigDArg(in)
	if len(in) != 1 {
		t.Fatalf("caller's slice grew to %v", in)
	}
	if len(out) != 2 {
		t.Fatalf("result = %v; want the caller's arg plus the binding", out)
	}
	// Same backing array would mean a second call over a different slice could
	// stomp this result. `in` has spare capacity, so this append writes into
	// index 1 of its backing array — the very slot out[1] occupies if the two
	// share one. Assert the probe landed there, otherwise the check below
	// could pass because nothing was written rather than because the arrays
	// are distinct.
	in = append(in, "sentinel")
	if in[1] != "sentinel" {
		t.Fatalf("probe did not write the slot under test: in = %v", in)
	}
	if strings.HasPrefix(out[1], "sentinel") {
		t.Fatal("result shares a backing array with the caller's slice")
	}
}
