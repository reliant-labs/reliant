package v1alpha1

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestManagedSecretRef pins the managed-secret channel's wire shape
// (managedSecret: {name, optional}) and its validation: a bare logical name,
// one channel at most, and optional allowed under Restricted (it is the hosted
// spelling of an optional credential).
func TestManagedSecretRef(t *testing.T) {
	b, _ := json.Marshal(EnvVar{Name: "DSN", ManagedSecret: &ManagedSecretRef{Name: "SENTRY_DSN", Optional: true}})
	if string(b) != `{"name":"DSN","managedSecret":{"name":"SENTRY_DSN","optional":true}}` {
		t.Errorf("optional wire shape = %s", b)
	}
	b, _ = json.Marshal(EnvVar{Name: "KEY", ManagedSecret: &ManagedSecretRef{Name: "STRIPE_KEY"}})
	if string(b) != `{"name":"KEY","managedSecret":{"name":"STRIPE_KEY"}}` {
		t.Errorf("required wire shape = %s", b)
	}

	for name, c := range map[string]struct {
		ev   EnvVar
		want string
	}{
		"empty name":   {EnvVar{Name: "X", ManagedSecret: &ManagedSecretRef{}}, `managedSecret.name "" must be a bare logical name`},
		"path name":    {EnvVar{Name: "X", ManagedSecret: &ManagedSecretRef{Name: "org/KEY"}}, `managedSecret.name "org/KEY" must be a bare logical name`},
		"two channels": {EnvVar{Name: "X", Value: "v", ManagedSecret: &ManagedSecretRef{Name: "K", Optional: true}}, "sets more than one"},
	} {
		if err := c.ev.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if err := (EnvVar{Name: "X", ManagedSecret: &ManagedSecretRef{Name: "K", Optional: true}}).Validate(); err != nil {
		t.Errorf("valid optional managed secret refused: %v", err)
	}

	spec := WorkloadSpec{Image: "ghcr.io/acme/app:v1", Env: []EnvVar{{Name: "DSN", ManagedSecret: &ManagedSecretRef{Name: "SENTRY_DSN", Optional: true}}}}
	if err := spec.Validate(ProfileRestricted); err != nil {
		t.Errorf("Restricted must accept an optional managed secret: %v", err)
	}
}
