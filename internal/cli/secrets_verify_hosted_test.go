package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// TestSecretsVerifyHostedChecksPresence pins NS FG-37: on a hosted env,
// `forge env secrets verify <env>` checks that every declared secret holds a
// value in the control plane's store (by name; values never leave it),
// where it used to say "nothing to verify" and exit 1.
func TestSecretsVerifyHostedChecksPresence(t *testing.T) {
	entities := &KCLEntities{
		SecretProvider: &SecretProviderEntity{Type: "hosted"},
		Workloads: []WorkloadEntity{{Name: "api", Spec: deployv1alpha1.WorkloadSpec{Env: []deployv1alpha1.EnvVar{
			{Name: "STRIPE_KEY", SecretRef: &deployv1alpha1.SecretKeyRef{Name: "api", Key: "STRIPE_KEY"}},
			{Name: "SENDGRID_KEY", SecretRef: &deployv1alpha1.SecretKeyRef{Name: "api", Key: "SENDGRID_KEY"}},
			{Name: "OPTIONAL_KEY", SecretRef: &deployv1alpha1.SecretKeyRef{Name: "api", Key: "OPTIONAL_KEY", Optional: true}},
		}}}},
	}
	prevRender, prevStore := renderEntitiesForSecrets, hostedSecretListStoreFn
	t.Cleanup(func() { renderEntitiesForSecrets, hostedSecretListStoreFn = prevRender, prevStore })
	renderEntitiesForSecrets = func(context.Context, string) (*KCLEntities, error) { return entities, nil }

	present := map[string]bool{"STRIPE_KEY": true}
	hostedSecretListStoreFn = func(context.Context, string, *KCLEntities) (secretListStore, error) {
		return secretListStore{provider: "hosted", verifiable: true, present: present}, nil
	}
	var out bytes.Buffer
	err := runSecretsVerify(context.Background(), "prod", "", secretsVerifyOptions{}, &out)
	if err == nil || !strings.Contains(err.Error(), "SENDGRID_KEY") {
		t.Fatalf("a missing declared secret: err = %v\n%s", err, out.String())
	}
	for _, want := range []string{"STRIPE_KEY", "set", "SENDGRID_KEY", "MISSING", "secret set --env prod"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "OPTIONAL_KEY") {
		t.Errorf("an optional secret is not required:\n%s", out.String())
	}

	present["SENDGRID_KEY"] = true
	out.Reset()
	if err := runSecretsVerify(context.Background(), "prod", "", secretsVerifyOptions{}, &out); err != nil {
		t.Fatalf("every declared secret set: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "all 2 declared secret(s) are set") {
		t.Errorf("verdict:\n%s", out.String())
	}
}
