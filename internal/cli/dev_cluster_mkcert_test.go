package cli

import (
	"strings"
	"testing"
)

// TestProjectMkcertSecrets_NoMkcertGateways covers the skip-when-empty
// path — a KCLEntities with no Gateways, or only Gateways without
// mkcert-mode TLS, projects to an empty slice with no error. This is
// the hot path on every cluster-up: provisionMkcertSecrets has to
// stay free of work when the user hasn't opted in.
func TestProjectMkcertSecrets_NoMkcertGateways(t *testing.T) {
	cases := []struct {
		name     string
		entities *KCLEntities
	}{
		{"nil entities", nil},
		{"no gateways", &KCLEntities{}},
		{
			"gateway with no tls",
			&KCLEntities{Gateways: []GatewayEntity{
				{Name: "public", Host: "example.com"},
			}},
		},
		{
			"gateway with cert_manager tls",
			&KCLEntities{Gateways: []GatewayEntity{
				{
					Name: "public",
					Host: "example.com",
					TLS: &GatewayTLSEntity{
						CertIssuer: "letsencrypt-prod",
						SecretName: "public-tls",
						Mode:       "cert_manager",
					},
				},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			specs, err := projectMkcertSecrets(tc.entities, "example-dev")
			if err != nil {
				t.Fatalf("projectMkcertSecrets: %v", err)
			}
			if len(specs) != 0 {
				t.Errorf("expected no specs, got %d: %+v", len(specs), specs)
			}
		})
	}
}

// TestProjectMkcertSecrets_PicksMkcertOnly verifies the projection
// surfaces ONLY mkcert-mode gateways and carries the hostname, secret
// name, and caller-supplied namespace through unmangled.
func TestProjectMkcertSecrets_PicksMkcertOnly(t *testing.T) {
	entities := &KCLEntities{Gateways: []GatewayEntity{
		{
			Name: "public",
			Host: "myapp.localhost",
			TLS: &GatewayTLSEntity{
				SecretName: "myapp-dev-tls",
				Mode:       "mkcert",
			},
		},
		{
			Name: "internal",
			Host: "internal.example.com",
			TLS: &GatewayTLSEntity{
				CertIssuer: "letsencrypt-prod",
				SecretName: "internal-tls",
				Mode:       "cert_manager",
			},
		},
		{
			Name: "webhooks",
			Host: "webhooks.localhost",
			TLS: &GatewayTLSEntity{
				SecretName: "webhooks-dev-tls",
				Mode:       "mkcert",
			},
		},
	}}

	specs, err := projectMkcertSecrets(entities, "myapp-dev")
	if err != nil {
		t.Fatalf("projectMkcertSecrets: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("got %d specs, want 2: %+v", len(specs), specs)
	}

	byHost := map[string]mkcertSecretSpec{}
	for _, s := range specs {
		byHost[s.Hostname] = s
	}
	if got, ok := byHost["myapp.localhost"]; !ok || got.SecretName != "myapp-dev-tls" || got.Namespace != "myapp-dev" {
		t.Errorf("myapp spec wrong: %+v", got)
	}
	if got, ok := byHost["webhooks.localhost"]; !ok || got.SecretName != "webhooks-dev-tls" || got.Namespace != "myapp-dev" {
		t.Errorf("webhooks spec wrong: %+v", got)
	}
	if _, ok := byHost["internal.example.com"]; ok {
		t.Errorf("cert_manager-mode gateway should NOT appear in mkcert specs")
	}
}

// TestProjectMkcertSecrets_EmptyHostError covers the misconfig path —
// mkcert needs a hostname to sign for. We surface a clear error rather
// than silently invoking mkcert with no arg (which would error
// opaquely from the binary).
func TestProjectMkcertSecrets_EmptyHostError(t *testing.T) {
	entities := &KCLEntities{Gateways: []GatewayEntity{
		{
			Name: "public",
			Host: "", // missing — schema allows it for plain-HTTP gateways
			TLS: &GatewayTLSEntity{
				SecretName: "public-tls",
				Mode:       "mkcert",
			},
		},
	}}
	specs, err := projectMkcertSecrets(entities, "example-dev")
	if err == nil {
		t.Fatalf("expected error for mkcert gateway with empty host, got specs=%+v", specs)
	}
	if !strings.Contains(err.Error(), "public") || !strings.Contains(err.Error(), "host") {
		t.Errorf("error should name the gateway and call out the empty host; got: %v", err)
	}
}

// TestProjectMkcertSecrets_EmptySecretError catches the other half of
// the misconfig: mkcert mode without a secret_name. Schema's check:
// block already requires secret_name, but defending in the Go
// projection means cluster-up surfaces the issue early rather than
// crashing later when the empty Secret name reaches kubectl.
func TestProjectMkcertSecrets_EmptySecretError(t *testing.T) {
	entities := &KCLEntities{Gateways: []GatewayEntity{
		{
			Name: "public",
			Host: "myapp.localhost",
			TLS: &GatewayTLSEntity{
				SecretName: "",
				Mode:       "mkcert",
			},
		},
	}}
	_, err := projectMkcertSecrets(entities, "example-dev")
	if err == nil {
		t.Fatalf("expected error for empty secret_name, got nil")
	}
}

// TestRenderTLSSecretYAML_Shape sanity-checks the Secret manifest the
// kubectl-apply path consumes: kubernetes.io/tls type, base64-encoded
// data fields, and the forge-management labels.
func TestRenderTLSSecretYAML_Shape(t *testing.T) {
	out, err := renderTLSSecretYAML(
		"myapp-dev-tls",
		"myapp-dev",
		[]byte("CERT-PEM-BYTES"),
		[]byte("KEY-PEM-BYTES"),
	)
	if err != nil {
		t.Fatalf("renderTLSSecretYAML: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		"kind: Secret",
		"type: kubernetes.io/tls",
		"name: myapp-dev-tls",
		"namespace: myapp-dev",
		"forge.dev/tls-source: mkcert",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("Secret YAML missing %q:\n%s", want, s)
		}
	}
}
