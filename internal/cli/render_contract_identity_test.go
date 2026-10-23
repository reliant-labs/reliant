package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/pkg/release"
)

// The identity goldens (testdata/render_contract/identity-*.json) are the KCL
// render of a managed identity. These tests drive the production consumers from
// them: the strict decoder, the shape projection, and the shape's own
// validation, so a render the control plane could not read fails here.

func identityShapeOf(t *testing.T, golden string) release.Shape {
	t.Helper()
	e, _ := loadContract(t, golden)
	shape, err := bundle.ProjectShape(bundle.ShapeInput{
		Kind:                release.EnvSelfManaged,
		Workloads:           shapeWorkloadsOf(e),
		ControlPlaneSecrets: shapeControlPlaneSecretsOf(e),
		Identity:            shapeIdentityOf(e),
	})
	if err != nil {
		t.Fatalf("%s: ProjectShape: %v", golden, err)
	}
	return shape
}

func TestRenderContract_IdentityGoldensDecodeAndValidate(t *testing.T) {
	for _, name := range []string{"identity-hosted", "identity-cluster", "identity-share-with", "identity-host"} {
		t.Run(name, func(t *testing.T) {
			e, _ := loadContract(t, name)
			if e.Identity == nil {
				t.Fatal("the render has no identity")
			}
			shape := identityShapeOf(t, name)
			if err := shape.Validate(); err != nil {
				t.Fatalf("the projected shape is invalid: %v", err)
			}
			// The shape records every delivery as a control-plane secret, so the
			// existing delivery path sees it.
			byKey := map[string]release.ShapeControlPlaneSecret{}
			for _, c := range shape.ControlPlaneSecrets {
				byKey[c.Key()] = c
			}
			for _, d := range shape.Identity.Deliveries {
				c, ok := byKey[d.Cluster+"/"+d.Namespace+"/"+d.Name]
				if !ok || c.Kind != d.Kind {
					t.Errorf("delivery %+v is not among the shape's control-plane secrets", d)
				}
			}
		})
	}
}

func TestRenderContract_IdentityCluster(t *testing.T) {
	e, _ := loadContract(t, "identity-cluster")
	shape := identityShapeOf(t, "identity-cluster")
	id := shape.Identity
	if id.Runtime != release.IdentityRuntimeHosted || id.Login.Mode != release.IdentityLoginNative {
		t.Errorf("runtime/login = %s/%s, want hosted/native", id.Runtime, id.Login.Mode)
	}
	if len(id.Applications) != 1 || id.Applications[0].Name != "prod:api" || id.Applications[0].Workload != "api" {
		t.Errorf("applications = %+v", id.Applications)
	}
	if len(id.Consumers) != 1 || id.Consumers[0].Workload != "api" || id.Consumers[0].Application != "prod:api" {
		t.Errorf("consumers = %+v: the worker is not a consumer", id.Consumers)
	}
	kinds := map[string]bool{}
	for _, c := range shape.ControlPlaneSecrets {
		kinds[c.Kind] = true
		if c.Kind == release.ControlPlaneSecretIdentity && !c.Required {
			t.Errorf("%s must be required: the pods wait for the first delivery", c.Name)
		}
		if c.Kind == release.ControlPlaneSecretIdentityJWKS && c.Required {
			t.Errorf("%s must be optional: a snapshot is a fallback", c.Name)
		}
	}
	if !kinds[release.ControlPlaneSecretIdentity] || !kinds[release.ControlPlaneSecretIdentityJWKS] {
		t.Errorf("kinds = %v, want identity and identity_jwks", kinds)
	}

	// THE PROJECTION: the API's typed config is filled with no hand-written
	// line. The worker, which consumes nothing, is untouched.
	api := e.Workloads[0]
	if api.Name != "api" {
		api = e.Workloads[1]
	}
	env := map[string]string{}
	for _, ev := range api.Spec.Env {
		switch {
		case ev.SecretRef != nil:
			env[ev.Name] = "secret:" + ev.SecretRef.Name + "/" + ev.SecretRef.Key
		default:
			env[ev.Name] = "value:" + ev.Value
		}
	}
	for name, want := range map[string]string{
		"JWT_ISSUER":             "secret:forge-identity-identity/ISSUER",
		"JWT_AUDIENCE":           "secret:forge-identity-identity/AUDIENCE",
		"OIDC_CLIENT_ID":         "secret:forge-identity-identity/PROD_API_CLIENT_ID",
		"OIDC_REDIRECT_URI":      "secret:forge-identity-identity/PROD_API_REDIRECT_URI",
		"IDP_BASE":               "secret:forge-identity-identity/ISSUER",
		"JWT_ROLES_CLAIM":        "value:auto",
		"IDP_BROKER_TOKEN_FILE":  "value:/var/run/secrets/forge/identity/broker_token",
		"JWT_JWKS_SNAPSHOT_FILE": "value:/var/run/secrets/forge/identity-jwks/jwks.json",
	} {
		if env[name] != want {
			t.Errorf("%s = %q, want %q", name, env[name], want)
		}
	}
	if _, plain := env["IDP_BROKER_TOKEN"]; plain {
		t.Error("the broker token is a file on a cluster consumer; a plain env var beside it would pin a rotated token")
	}
}

func TestRenderContract_IdentityHostedLowersToReservedManagedSecrets(t *testing.T) {
	e, _ := loadContract(t, "identity-hosted")
	shape := identityShapeOf(t, "identity-hosted")
	managed := map[string]string{}
	for _, ev := range e.Workloads[0].Spec.Env {
		if ev.ManagedSecret != nil {
			managed[ev.Name] = ev.ManagedSecret.Name
		}
	}
	if managed["JWT_ISSUER"] != "RELIANT_IDENTITY_IDENTITY_ISSUER" {
		t.Errorf("JWT_ISSUER reads %q", managed["JWT_ISSUER"])
	}
	for _, n := range managed {
		if strings.HasPrefix(n, release.IdentityManagedSecretPrefix) {
			found := false
			for _, r := range shape.Identity.ManagedSecrets {
				found = found || r == n
			}
			if !found {
				t.Errorf("%s is read by the workload but not recorded in the shape's managed_secrets", n)
			}
		}
	}
	// Only the machine user's key is delivered to a cluster.
	if len(shape.ControlPlaneSecrets) != 1 || shape.ControlPlaneSecrets[0].Kind != release.ControlPlaneSecretIdentityMachineKey {
		t.Errorf("control-plane secrets = %+v, want only the machine user's key", shape.ControlPlaneSecrets)
	}
	if shape.Identity.SMTP == nil || shape.Identity.SMTP.PasswordSecret != "SMTP_PASSWORD" {
		t.Errorf("smtp = %+v: the password is a NAME", shape.Identity.SMTP)
	}
}

// Required-ness reaches `forge env shape`'s JSON and survives a round trip.
func TestRenderContract_IdentityShapeRoundTrips(t *testing.T) {
	shape := identityShapeOf(t, "identity-cluster")
	b, err := shape.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"required":true`) || !strings.Contains(string(b), `"identity":{`) {
		t.Errorf("encoded shape lacks required/identity: %s", b)
	}
}
