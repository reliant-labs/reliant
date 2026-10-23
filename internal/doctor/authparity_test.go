package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// parityEnv builds the Environment CheckAuthParity reads, with the KCL
// render INJECTED rather than evaluated.
//
// deployRenders memoises through Environment.deployOnce, so consuming the
// Once with a no-op and then writing deployCache hands the check exactly the
// renders a test describes. That is the whole point of these tests: the
// check's answer must be a function of the RENDERED manifests, and pinning
// it to a real KCL evaluation would make every case need a package that
// resolves the forge module.
func parityEnv(t *testing.T, projectDir, envName string, renders []envRender) *Environment {
	t.Helper()
	e := &Environment{ProjectDir: projectDir, Env: envName}
	e.deployOnce.Do(func() {})
	e.deployCache = renders
	return e
}

// hostRender is an environment whose workload runs on the HOST — the normal
// shape of a dev env, where the app is a process on the developer's machine
// and only the infrastructure is in the cluster. Its env lands on the
// `output` contract's deploy block, never in a pod spec.
func hostRender(envName string, vars map[string]string) envRender {
	svc := renderedService{Name: "api"}
	for _, name := range sortedKeys(vars) {
		svc.Deploy.EnvVars = append(svc.Deploy.EnvVars, renderedEnv{Name: name, Value: vars[name]})
	}
	return envRender{env: envName, hasManifestRoot: true, hostServices: []renderedService{svc}}
}

// clusterRender is the same environment deployed as a Deployment, so the
// check is pinned against BOTH shapes a workload's environment can take.
// secretRefs name variables bound through valueFrom rather than a literal.
func clusterRender(envName string, vars map[string]string, secretRefs ...string) envRender {
	env := make([]any, 0, len(vars)+len(secretRefs))
	for _, name := range sortedKeys(vars) {
		env = append(env, map[string]any{"name": name, "value": vars[name]})
	}
	for _, name := range secretRefs {
		env = append(env, map[string]any{
			"name":      name,
			"valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "app-secrets", "key": name}},
		})
	}
	return envRender{
		env:             envName,
		hasManifestRoot: true,
		objects: []k8sObject{{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
			Metadata:   objectMeta{Name: "api", Namespace: "app"},
			Spec: map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"containers": []any{map[string]any{"name": "api", "env": env}},
					},
				},
			},
		}},
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// frontendSourceProject writes an env's KCL config.k declaring a frontend
// issuer the way `forge generate` scaffolds it — a typed instance with an
// oidc_issuer field. A frontend's typed config never becomes a k8s object,
// so this half is still read from source.
func frontendSourceProject(t *testing.T, envName, body string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "deploy", "kcl", envName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.k"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// The scaffold's default — no identity anywhere — is a clean SKIP, not a
// warning. No key material is the correct closed-and-bootable state.
func TestAuthParity_UnconfiguredSkips(t *testing.T) {
	env := parityEnv(t, t.TempDir(), "dev", []envRender{
		hostRender("dev", map[string]string{"DATABASE_URL": "postgres://localhost/app"}),
	})
	got := CheckAuthParity(context.Background(), env)
	if got.Status != StatusSkip {
		t.Fatalf("status = %q (%s), want skip", got.Status, got.Message)
	}
}

// Agreeing issuers pass, and a trailing slash is not a disagreement.
func TestAuthParity_MatchingIssuersPass(t *testing.T) {
	env := parityEnv(t, t.TempDir(), "dev", []envRender{
		hostRender("dev", map[string]string{
			"JWT_ISSUER":              "http://localhost:3001/oidc",
			"NEXT_PUBLIC_OIDC_ISSUER": "http://localhost:3001/oidc/",
		}),
	})
	got := CheckAuthParity(context.Background(), env)
	if got.Status != StatusPass {
		t.Fatalf("status = %q (%s), want pass", got.Status, got.Message)
	}
}

// The same agreement, rendered as a Deployment rather than a host service —
// a check that read only one of the two shapes would answer differently for
// the same project deployed two ways.
func TestAuthParity_MatchingIssuersPassInClusterManifest(t *testing.T) {
	env := parityEnv(t, t.TempDir(), "prod", []envRender{
		clusterRender("prod", map[string]string{
			"OIDC_ISSUER":        "https://issuer.example.com/oidc",
			"VITE_OIDC_ISSUER":   "https://issuer.example.com/oidc",
			"DEPLOY_ENVIRONMENT": "prod",
		}),
	})
	got := CheckAuthParity(context.Background(), env)
	if got.Status != StatusPass {
		t.Fatalf("status = %q (%s), want pass", got.Status, got.Message)
	}
}

// The failure this check exists for: both sides configured, pointing at
// different issuers. Sign-in succeeds and every RPC still 401s.
func TestAuthParity_MismatchWarns(t *testing.T) {
	env := parityEnv(t, t.TempDir(), "dev", []envRender{
		hostRender("dev", map[string]string{
			"JWT_ISSUER":              "https://issuer.example.com/oidc",
			"NEXT_PUBLIC_OIDC_ISSUER": "http://localhost:3001/oidc",
		}),
	})
	got := CheckAuthParity(context.Background(), env)
	if got.Status != StatusWarn {
		t.Fatalf("status = %q (%s), want warn", got.Status, got.Message)
	}
	if got.Evidence == "" {
		t.Error("a mismatch must report both issuers as evidence")
	}
	if !strings.Contains(got.Evidence, "two-hostname") {
		t.Error("the mismatch evidence must keep the two-hostname note — it is the usual cause")
	}
}

// A frontend naming an issuer the backend cannot validate is the incident
// this check was written for, and stays a warning.
func TestAuthParity_FrontendOnlyWarns(t *testing.T) {
	env := parityEnv(t, t.TempDir(), "dev", []envRender{
		hostRender("dev", map[string]string{"NEXT_PUBLIC_OIDC_ISSUER": "http://localhost:3001/oidc"}),
	})
	got := CheckAuthParity(context.Background(), env)
	if got.Status != StatusWarn {
		t.Fatalf("status = %q (%s), want warn", got.Status, got.Message)
	}
	if !strings.Contains(got.Message, "backend validates against none") {
		t.Errorf("message = %q, want the frontend-only wording", got.Message)
	}
}

// The frontend half of a forge-scaffolded project is declared as a typed
// config instance, which never becomes a k8s object — so it is read from the
// env's KCL source, and a backend that renders no issuer still warns.
func TestAuthParity_FrontendOnlyFromKCLSourceWarns(t *testing.T) {
	root := frontendSourceProject(t, "dev",
		"web_config: fc.WebConfig = {\n    oidc_issuer = \"http://localhost:3001/oidc\"\n}\n")
	env := parityEnv(t, root, "dev", []envRender{
		hostRender("dev", map[string]string{"DATABASE_URL": "postgres://localhost/app"}),
	})
	got := CheckAuthParity(context.Background(), env)
	if got.Status != StatusWarn {
		t.Fatalf("status = %q (%s), want warn", got.Status, got.Message)
	}
}

// THE control-plane REGRESSION.
//
// That project's backend validates SUPABASE_JWT_ISSUER (end users) and
// ZITADEL_ISSUER (operators) and has no field called `jwt_issuer` anywhere.
// The old line scan looked for exactly that one scaffold spelling, found
// nothing, and reported "the backend validates against none" on a stack
// whose authenticated RPCs demonstrably succeed. Recognising the issuer by
// SHAPE, off the render, is what makes this a pass.
func TestAuthParity_BackendIssuerRecognisedByShapeNotScaffoldSpelling(t *testing.T) {
	env := parityEnv(t, t.TempDir(), "dev", []envRender{
		hostRender("dev", map[string]string{
			"SUPABASE_JWT_ISSUER":     "https://project.supabase.co/auth/v1",
			"NEXT_PUBLIC_OIDC_ISSUER": "https://project.supabase.co/auth/v1",
		}),
	})
	got := CheckAuthParity(context.Background(), env)
	if got.Status != StatusPass {
		t.Fatalf("status = %q (%s), want pass — SUPABASE_JWT_ISSUER is an issuer", got.Status, got.Message)
	}
}

// A backend may accept SEVERAL issuers (auth.Config carries a list of
// TokenValidators; control-plane validates a Supabase issuer for end users
// and a Zitadel issuer for operators). A frontend naming ONE of them is
// correct, not a mismatch.
func TestAuthParity_MultipleBackendIssuersAcceptFrontend(t *testing.T) {
	env := parityEnv(t, t.TempDir(), "dev", []envRender{
		hostRender("dev", map[string]string{
			"SUPABASE_JWT_ISSUER":     "https://project.supabase.co/auth/v1",
			"ZITADEL_ISSUER":          "http://localhost:8098",
			"NEXT_PUBLIC_OIDC_ISSUER": "http://localhost:8098",
		}),
	})
	got := CheckAuthParity(context.Background(), env)
	if got.Status != StatusPass {
		t.Fatalf("status = %q (%s), want pass", got.Status, got.Message)
	}
}

// A comma-separated *_ISSUERS list is one variable naming several issuers.
func TestAuthParity_IssuerListIsSplit(t *testing.T) {
	env := parityEnv(t, t.TempDir(), "dev", []envRender{
		hostRender("dev", map[string]string{
			"MCP_OAUTH_ISSUERS":       "https://a.example.com/auth/v1, https://b.example.com/auth/v1",
			"NEXT_PUBLIC_OIDC_ISSUER": "https://b.example.com/auth/v1",
		}),
	})
	got := CheckAuthParity(context.Background(), env)
	if got.Status != StatusPass {
		t.Fatalf("status = %q (%s), want pass", got.Status, got.Message)
	}
}

// Backend-only is SKIP, not a warning — deliberately asymmetric with the
// frontend-only case above.
//
// The validator's issuer is forge-visible (it is the env var the process
// reads); the browser's is not. control-plane's reliant-web signs in through
// the Supabase SDK from VITE_SUPABASE_URL and declares no issuer variable at
// all, and it works. Warning on "no frontend names one" would re-create the
// cry-wolf failure on every API-only and SDK-authenticated project — so the
// issuer is reported as evidence and nothing is asserted.
func TestAuthParity_BackendOnlyIsNothingToCompare(t *testing.T) {
	env := parityEnv(t, t.TempDir(), "dev", []envRender{
		hostRender("dev", map[string]string{"SUPABASE_JWT_ISSUER": "https://project.supabase.co/auth/v1"}),
	})
	got := CheckAuthParity(context.Background(), env)
	if got.Status != StatusSkip {
		t.Fatalf("status = %q (%s), want skip", got.Status, got.Message)
	}
	if !strings.Contains(got.Evidence, "https://project.supabase.co/auth/v1") {
		t.Errorf("evidence = %q, must still name the issuer it read", got.Evidence)
	}
}

// An issuer bound through valueFrom has no value in the manifest. That is
// "forge cannot read it", not "the backend has none" — the difference
// between StatusUnknown and a confident warning, and the whole reason
// doctor.go keeps the two statuses apart.
func TestAuthParity_BackendIssuerFromSecretIsUndetermined(t *testing.T) {
	r := clusterRender("prod",
		map[string]string{"NEXT_PUBLIC_OIDC_ISSUER": "https://issuer.example.com/oidc"},
		"JWT_ISSUER")
	env := parityEnv(t, t.TempDir(), "prod", []envRender{r})
	got := CheckAuthParity(context.Background(), env)
	if got.Status != StatusUnknown {
		t.Fatalf("status = %q (%s), want unknown", got.Status, got.Message)
	}
}

// A render that failed leaves the check with no facts. It must not answer
// "the backend validates against none" — that assertion, made from a source
// grep that found nothing, is the bug this check was rewritten to kill.
func TestAuthParity_RenderFailureIsUndetermined(t *testing.T) {
	root := frontendSourceProject(t, "dev",
		"web_config: fc.WebConfig = {\n    oidc_issuer = \"http://localhost:3001/oidc\"\n}\n")
	env := parityEnv(t, root, "dev", []envRender{{env: "dev", err: errors.New("kcl: undefined variable")}})
	got := CheckAuthParity(context.Background(), env)
	if got.Status != StatusUnknown {
		t.Fatalf("status = %q (%s), want unknown", got.Status, got.Message)
	}
	if got.Status == StatusWarn {
		t.Error("a check with no facts must not warn")
	}
}

// A project that declares no environments has nothing to render and nothing
// to compare — the --kind cli / library shape, which must stay quiet.
func TestAuthParity_NoEnvironmentsSkips(t *testing.T) {
	env := parityEnv(t, t.TempDir(), "", nil)
	got := CheckAuthParity(context.Background(), env)
	if got.Status != StatusSkip {
		t.Fatalf("status = %q (%s), want skip", got.Status, got.Message)
	}
}

// With no Env set (the `forge doctor` project-health set) every declared
// environment is checked, and the most serious verdict wins — an issuer typo
// in prod is the same incident as one in dev, on the environment where it
// costs most.
func TestAuthParity_SpansEveryEnvironmentWhenUnscoped(t *testing.T) {
	renders := []envRender{
		hostRender("dev", map[string]string{
			"JWT_ISSUER":              "http://localhost:3001/oidc",
			"NEXT_PUBLIC_OIDC_ISSUER": "http://localhost:3001/oidc",
		}),
		hostRender("prod", map[string]string{
			"JWT_ISSUER":              "https://issuer.example.com/oidc",
			"NEXT_PUBLIC_OIDC_ISSUER": "https://typo.example.com/oidc",
		}),
	}
	got := CheckAuthParity(context.Background(), parityEnv(t, t.TempDir(), "", renders))
	if got.Status != StatusWarn {
		t.Fatalf("status = %q (%s), want warn", got.Status, got.Message)
	}
	if !strings.HasPrefix(got.Message, "prod: ") {
		t.Errorf("message = %q, want the offending environment named", got.Message)
	}

	// Scoped to dev, the same project is a pass — and the message carries no
	// env prefix, because `forge env up` embeds it in a one-line banner.
	scoped := CheckAuthParity(context.Background(), parityEnv(t, t.TempDir(), "dev", renders))
	if scoped.Status != StatusPass {
		t.Fatalf("dev-scoped status = %q (%s), want pass", scoped.Status, scoped.Message)
	}
	if strings.HasPrefix(scoped.Message, "dev: ") {
		t.Errorf("single-env message = %q, want no env prefix", scoped.Message)
	}
}

// cert-manager's Issuer/ClusterIssuer names a certificate authority and has
// nothing to do with a token's `iss` claim. An auth report that lists
// CERT_ISSUER is a report people learn to skip.
func TestAuthParity_CertManagerIssuerIsNotAnAuthIssuer(t *testing.T) {
	env := parityEnv(t, t.TempDir(), "prod", []envRender{
		clusterRender("prod", map[string]string{
			"CERT_ISSUER":         "letsencrypt-dns01",
			"CLUSTER_ISSUER_NAME": "letsencrypt-dns01",
		}),
	})
	got := CheckAuthParity(context.Background(), env)
	if got.Status != StatusSkip {
		t.Fatalf("status = %q (%s), want skip", got.Status, got.Message)
	}
}

// isIssuerVarName is the generalisation that replaced the hard-coded
// `jwt_issuer` lookup, so its boundaries are pinned.
func TestIsIssuerVarName(t *testing.T) {
	for _, name := range []string{
		"JWT_ISSUER", "OIDC_ISSUER", "SUPABASE_JWT_ISSUER", "ZITADEL_ISSUER",
		"MCP_OAUTH_ISSUERS", "issuer", "NEXT_PUBLIC_OIDC_ISSUER",
	} {
		if !isIssuerVarName(name) {
			t.Errorf("isIssuerVarName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{
		"", "JWT_JWKS_URL", "ISSUER_AUDIENCE", "CERT_ISSUER", "CLUSTER_ISSUER",
		"TLS_ISSUER", "ACME_ISSUER", "DATABASE_URL",
	} {
		if isIssuerVarName(name) {
			t.Errorf("isIssuerVarName(%q) = true, want false", name)
		}
	}
}

// The KCL reader has to handle both spellings an issuer appears in — a dict
// entry in a generated identity fragment and a schema field in a
// hand-authored config — plus comments, which a naive scan would happily
// read a commented-out issuer from, plus REFERENCES, which it must refuse:
// control-plane's dev config.k binds oidc_issuer to a lookup in a generated
// dict, and returning that expression verbatim is what made this check
// compare a KCL snippet against a URL and report a mismatch.
func TestReadKCLIssuerLiterals(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name string
		body string
		want []kclLiteral
	}{
		{
			// A dict entry, the spelling a projection map uses. Kept
			// readable alongside the schema-field form below because the
			// check must not care which of the two a project authored.
			name: "dict entry",
			body: "# \"OIDC_ISSUER\" = \"http://commented-out\"\n" +
				"identity = {\n    \"OIDC_ISSUER\" = \"http://real/oidc\"\n}\n",
			want: []kclLiteral{{key: "OIDC_ISSUER", value: "http://real/oidc"}},
		},
		{
			// The proto spells the field oidc_issuer and the projection key
			// OIDC_ISSUER; matching case-insensitively means a hand-authored
			// config counts too, rather than the check going quiet on
			// exactly the setup most likely to have drifted.
			name: "schema field (hand-authored config)",
			body: "web_config: fc.WebConfig = {\n    oidc_issuer = \"http://schema/oidc\"\n}\n",
			want: []kclLiteral{{key: "oidc_issuer", value: "http://schema/oidc"}},
		},
		{
			// The regression in one line: a project that names its field
			// anything other than the forge scaffold's jwt_issuer is still
			// declaring an issuer, and the old reader could not see it.
			name: "any issuer-shaped field, not one scaffold spelling",
			body: "_supabase_jwt_issuer = \"https://project.supabase.co/auth/v1\"\n" +
				"zitadel_issuer = \"http://localhost:8098\"\n",
			want: []kclLiteral{
				{key: "_supabase_jwt_issuer", value: "https://project.supabase.co/auth/v1"},
				{key: "zitadel_issuer", value: "http://localhost:8098"},
			},
		},
		{
			name: "reference is not a literal",
			body: "web_config: fc.WebConfig = {\n" +
				"    oidc_issuer = idp.dev_frontend_identity[\"NEXT_PUBLIC_OIDC_ISSUER\"]\n}\n",
			want: nil,
		},
		{
			// A reference first, a literal later: the scan keeps going
			// rather than stopping at the thing it cannot read.
			name: "reference then literal",
			body: "a = {\n    oidc_issuer = idp.identity[\"X\"]\n}\n" +
				"b = {\n    oidc_issuer = \"http://later/oidc\"\n}\n",
			want: []kclLiteral{{key: "oidc_issuer", value: "http://later/oidc"}},
		},
		{
			// cert-manager's issuer is a different thing entirely.
			name: "cert issuer is not an auth issuer",
			body: "route = {\n    cert_issuer = \"letsencrypt-dns01\"\n}\n",
			want: nil,
		},
		{
			name: "absent",
			body: "other = \"x\"\n",
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".k")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			got := readKCLIssuerLiterals(path)
			if len(got) != len(tc.want) {
				t.Fatalf("readKCLIssuerLiterals = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("literal %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// A project whose deploy package does not render yet still gets the one
// warning that matters, read from its KCL source — and the source is read by
// SHAPE, so a backend field spelled anything other than jwt_issuer counts.
// This is `forge env up`'s banner path on a half-scaffolded project.
func TestAuthParity_SourceOnlyWhenNothingRenders(t *testing.T) {
	t.Run("frontend alone still warns", func(t *testing.T) {
		root := frontendSourceProject(t, "dev",
			"web_config: fc.WebConfig = {\n    oidc_issuer = \"http://localhost:8080\"\n}\n")
		got := CheckAuthParity(context.Background(), parityEnv(t, root, "dev", nil))
		if got.Status != StatusWarn {
			t.Fatalf("status = %q (%s), want warn", got.Status, got.Message)
		}
	})

	t.Run("a non-scaffold backend spelling is still an issuer", func(t *testing.T) {
		root := frontendSourceProject(t, "dev",
			"app_config: cg.AppConfig = {\n    supabase_jwt_issuer = \"http://localhost:8080\"\n}\n"+
				"web_config: fc.WebConfig = {\n    oidc_issuer = \"http://localhost:8080\"\n}\n")
		got := CheckAuthParity(context.Background(), parityEnv(t, root, "dev", nil))
		if got.Status != StatusPass {
			t.Fatalf("status = %q (%s), want pass", got.Status, got.Message)
		}
	})

	t.Run("nothing declared stays quiet", func(t *testing.T) {
		root := frontendSourceProject(t, "dev", "app_config: cg.AppConfig = {\n    log_level = \"debug\"\n}\n")
		got := CheckAuthParity(context.Background(), parityEnv(t, root, "dev", nil))
		if got.Status != StatusSkip {
			t.Fatalf("status = %q (%s), want skip", got.Status, got.Message)
		}
	})
}

// A render that DOES describe the backend is authoritative: a stale literal
// left in a .k file must not get a second opinion in. Reversing that
// precedence is the bug this check carried.
func TestAuthParity_RenderBeatsStaleSource(t *testing.T) {
	root := frontendSourceProject(t, "dev",
		"app_config: cg.AppConfig = {\n    jwt_issuer = \"http://stale.example.com/oidc\"\n}\n")
	env := parityEnv(t, root, "dev", []envRender{
		hostRender("dev", map[string]string{
			"JWT_ISSUER":              "http://localhost:3001/oidc",
			"NEXT_PUBLIC_OIDC_ISSUER": "http://localhost:3001/oidc",
		}),
	})
	got := CheckAuthParity(context.Background(), env)
	if got.Status != StatusPass {
		t.Fatalf("status = %q (%s), want pass — the render, not the stale .k literal, is the issuer", got.Status, got.Message)
	}
	if strings.Contains(got.Evidence, "stale.example.com") {
		t.Errorf("evidence quoted the stale source literal: %q", got.Evidence)
	}
}
