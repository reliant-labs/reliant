package v1alpha1

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSecretRefOptionalInheritsFullOnly pins how SecretKeyRef.Optional is
// classified. It has no FieldProfiles entry of its own, on purpose: env.secretRef
// is Full-only and refused WHOLE under Restricted, so its children (name, key,
// optional) inherit that, and TestFieldProfilesClassifyEverySpecField rejects
// an entry for them as a stale path. The default-deny guarantee still holds:
// if secretRef were ever made Restricted, that test would demand a
// classification for optional as well.
//
// So an optional secretRef is exactly as refused as a required one, and the
// refusal names secretRef with the reason and the alternative.
func TestSecretRefOptionalInheritsFullOnly(t *testing.T) {
	spec := WorkloadSpec{
		Image: "ghcr.io/acme/app:v1",
		Env:   []EnvVar{{Name: "MAYBE", SecretRef: &SecretKeyRef{Name: "s", Key: "k", Optional: true}}},
	}
	if err := spec.Validate(ProfileFull); err != nil {
		t.Fatalf("Full must accept an optional secretRef: %v", err)
	}
	err := spec.Validate(ProfileRestricted)
	if err == nil || !strings.Contains(err.Error(), "env[MAYBE].secretRef: not allowed under the restricted profile") || !strings.Contains(err.Error(), "use managedSecret") {
		t.Fatalf("Restricted must refuse an optional secretRef as secretRef, got: %v", err)
	}
	if _, ok := FieldProfiles["env.secretRef.optional"]; ok {
		t.Error("env.secretRef.optional must inherit env.secretRef's Full-only classification, not carry its own")
	}

	// Wire shape: optional,omitempty.
	b, _ := json.Marshal(SecretKeyRef{Name: "s", Key: "k"})
	if strings.Contains(string(b), "optional") {
		t.Errorf("a required ref must not serialize optional: %s", b)
	}
	b, _ = json.Marshal(SecretKeyRef{Name: "s", Key: "k", Optional: true})
	if string(b) != `{"name":"s","key":"k","optional":true}` {
		t.Errorf("optional ref = %s", b)
	}
}
