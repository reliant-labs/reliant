package cli

import "testing"

// A FileSecrets secret_provider supplies the Secrets it projects, even though it
// carries no SecretProvider.Secrets (that field is populated only for
// Type=="rendered"). Forge renders these CLI-side from the declared cluster
// refs and applies them before the Deployments roll out, so a mount backed by
// one is declared, not undeclared.
//
// Regression: without the value-provider branch in secretSupplyForPreflight, the
// scaffold's own dev bundle reported every `sensitive` config field (e.g.
// DATABASE_URL → "<project>-secrets") as an undeclared mount, so
// `forge project audit` came back ERROR on a pristine `forge project new`.
func TestSecretSupplyForPreflight_FileProviderSuppliesDeclaredRefs(t *testing.T) {
	e, err := parseKCLEntities([]byte(sampleSecretProviderJSON))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}

	supply := secretSupplyForPreflight(e)

	got := map[string]bool{}
	for _, s := range supply {
		got[s.Name] = true
	}

	// The cluster service "api" declares both; the host service's "stripe" is
	// not a k8s mount and must NOT be claimed as cluster supply.
	for _, want := range []string{"github-oauth", "creds"} {
		if !got[want] {
			t.Errorf("provider supply missing Secret %q; got %v", want, got)
		}
	}
	if got["stripe"] {
		t.Error("host-only secret ref \"stripe\" must not be reported as cluster supply")
	}
}

// Two env vars pointing at the same Secret are one Secret, not two.
func TestSecretSupplyForPreflight_FileDedupesBySecretName(t *testing.T) {
	const sameSecretTwiceJSON = `{
  "output": {
    "secret_provider": {
      "type": "file",
      "path": "secrets/dev.yaml"
    },
    "workloads": [
      {
        "name": "api",
        "kind": "service",
        "runtime": {
          "type": "cluster",
          "cluster": "c",
          "namespace": "dev"
        },
        "spec": {
          "kind": "service",
          "env": [
            {
              "name": "DATABASE_URL",
              "secretRef": {
                "name": "app-secrets",
                "key": "database_url"
              }
            },
            {
              "name": "JWT_SECRET",
              "secretRef": {
                "name": "app-secrets",
                "key": "jwt_secret"
              }
            }
          ]
        }
      }
    ]
  }
}`

	e, err := parseKCLEntities([]byte(sameSecretTwiceJSON))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}

	n := 0
	for _, s := range secretSupplyForPreflight(e) {
		if s.Name == "app-secrets" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("app-secrets supplied %d times, want 1 (deduped by Secret name)", n)
	}
}

// An external provider must NOT have its refs auto-credited: those Secrets are
// provisioned out of band, and an ExternalSecret declaration is the only thing
// that vouches for them. Silently crediting them would defeat the gate.
func TestSecretSupplyForPreflight_ExternalProviderDoesNotSelfSupply(t *testing.T) {
	const externalJSON = `{
  "output": {
    "secret_provider": {
      "type": "external"
    },
    "workloads": [
      {
        "name": "api",
        "kind": "service",
        "runtime": {
          "type": "cluster",
          "cluster": "c",
          "namespace": "prod"
        },
        "spec": {
          "kind": "service",
          "env": [
            {
              "name": "DATABASE_URL",
              "secretRef": {
                "name": "app-secrets",
                "key": "database_url"
              }
            }
          ]
        }
      }
    ]
  }
}`

	e, err := parseKCLEntities([]byte(externalJSON))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}

	for _, s := range secretSupplyForPreflight(e) {
		if s.Name == "app-secrets" {
			t.Error("external provider must not self-supply its refs; declare a forge.ExternalSecret instead")
		}
	}
}
