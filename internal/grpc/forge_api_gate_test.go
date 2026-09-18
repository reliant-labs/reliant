// Copyright (c) 2025 Reliant Labs
package grpc

import "testing"

// The forge RPC surface's server-side kill switch.
//
// WHY IT EXISTS ALONGSIDE THE FRONTEND FLAG. The web app gates the forge UI on a
// per-browser localStorage flag, which is right for "let me try the unfinished
// thing" but is not a security boundary: anyone who can craft a Connect request
// bypasses it entirely. StartDeploy applies manifests to a live cluster, so the
// operator needs a way to make the surface unroutable at the server no matter
// what any browser believes.
//
// IT IS OPT-OUT, DELIBERATELY. Unset means available. An opt-in spelling would
// mean a deployment that forgot the variable would find the feature mysteriously
// missing — a worse and much more confusing failure than the one being guarded
// against, and the operator has already expressed intent by enabling the UI.
func TestForgeAPIDisabled(t *testing.T) {
	t.Run("unset leaves the surface available", func(t *testing.T) {
		t.Setenv("RELIANT_DISABLE_FORGE_API", "")

		if forgeAPIDisabled() {
			t.Error("forgeAPIDisabled() = true with the variable unset; the kill switch " +
				"must be opt-OUT, or a deployment that never sets it loses the feature")
		}
	})

	// Every conventional truthy spelling, because an operator reaching for a kill
	// switch under pressure should not have to guess which one this flag wants.
	for _, value := range []string{"1", "true", "yes", "on", "TRUE", "On", " true "} {
		t.Run("disabled by "+value, func(t *testing.T) {
			t.Setenv("RELIANT_DISABLE_FORGE_API", value)

			if !forgeAPIDisabled() {
				t.Errorf("forgeAPIDisabled() = false for %q; every conventional truthy "+
					"spelling must disable the surface", value)
			}
		})
	}

	// A value that is not a recognised truthy spelling leaves the surface up.
	// This direction is the safe one to be lenient about: the failure mode of
	// mis-reading "false" as truthy would be a feature that vanishes for no
	// visible reason.
	for _, value := range []string{"0", "false", "no", "off", "maybe", "disabled"} {
		t.Run("not disabled by "+value, func(t *testing.T) {
			t.Setenv("RELIANT_DISABLE_FORGE_API", value)

			if forgeAPIDisabled() {
				t.Errorf("forgeAPIDisabled() = true for %q; only an explicit truthy "+
					"value may remove the surface", value)
			}
		})
	}
}
