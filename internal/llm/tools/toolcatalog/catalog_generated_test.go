// Copyright (c) 2025 Reliant Labs
package toolcatalog

import "testing"

// The catalog is generated, so the risk is not that it is wrong today — it is
// that it goes stale. A tool gains a parameter, nobody re-runs the generator,
// and a binding for the new parameter is rejected as unknown.
//
// The drift check that catches THAT lives in
// internal/llm/tools/catalog_drift_test.go, because it needs the tool registry
// and this package deliberately imports nothing. What lives here are the
// invariants of the file itself.

// A catalog that generated empty would make every guard downstream vacuous:
// every check is "look the name up and complain if absent", and an empty map
// makes the first branch reject everything while an empty ENTRY accepts
// nothing. Either way the failure is silent, so pin the shape.
func TestCatalogIsNotEmpty(t *testing.T) {
	names := Tools()
	if len(names) < 40 {
		t.Fatalf("catalog has %d tools, expected the full registry (~50); "+
			"did the generator run against an empty registry?", len(names))
	}
}

// generate_image is the proving case for the whole feature: `model` is bound
// by DEFAULT, so it is absent from the model-visible ParamSchema. A generator
// that read only ParamSchema would omit it, and the one parameter this feature
// exists to let people configure would be unconfigurable.
func TestModelIsBindableOnGenerateImageDespiteBeingBoundByDefault(t *testing.T) {
	if !IsBindable("generate_image", "model") {
		t.Fatal("generate_image.model must be bindable; it is bound by default, " +
			"which means the generator read the model-visible schema instead of the full one")
	}
}

// The unbindable list is the one hand-written thing here, so pin that it
// actually reaches the catalog with its reason attached.
func TestUnbindableParametersCarryReasons(t *testing.T) {
	reason, unbindable := UnbindableReason("generate_image", "save_to")
	if !unbindable {
		t.Fatal("generate_image.save_to should be unbindable")
	}
	if reason == "" {
		t.Fatal("an unbindable parameter without a reason is indistinguishable from a typo")
	}
	if IsBindable("generate_image", "save_to") {
		t.Fatal("save_to must not be in both the bindable and unbindable sets")
	}
}

// The shell tool is ONE static name on every platform (ShellToolName =
// "shell"); the daemon's OS selects the tool's description at request time,
// not its name. This test previously asserted that both "bash" and
// "powershell" appeared, because the name used to be a build-tagged constant
// and a catalog generated on Windows would otherwise differ from one generated
// on Linux. That reproducibility hazard is gone with the static name, so the
// assertion is now the inverse: the platform names must NOT appear, or a
// workflow could bind a tool that no registry actually serves.
func TestShellToolHasOneStaticName(t *testing.T) {
	if _, ok := Lookup("shell"); !ok {
		t.Error(`catalog is missing "shell"`)
	}
	for _, stale := range []string{"bash", "powershell"} {
		if _, ok := Lookup(stale); ok {
			t.Errorf("catalog still carries the retired platform name %q", stale)
		}
	}
}
