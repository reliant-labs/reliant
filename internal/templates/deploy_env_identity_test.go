package templates

import (
	"strings"
	"testing"
)

// TestEnvTemplatesWireIdentityOnlyWhenDeclared pins NoIdentity (`forge project
// new --auth-mode none`): an env with a frontend and no users keeps its
// frontend, and loses every piece of sign-in wiring — the identity import and
// declaration, and in dev the identity provider, the mail catcher and the
// credentialed CORS a session cookie needs. With users (the default) each is
// still there.
//
// The render tests (scaffold_identity_test.go) prove the result evaluates;
// this one runs on every `go test -short` and names the line that regressed.
func TestEnvTemplatesWireIdentityOnlyWhenDeclared(t *testing.T) {
	t.Parallel()
	identityLines := map[string][]string{
		"kcl/dev/main.k.tmpl": {
			"import ..identity as idn",
			"identity = idn.IDENTITY",
			`engine = "zitadel"`,
			`engine = "mailpit"`,
		},
		"kcl/cloud/main.k.tmpl": {
			"import ..identity as idn",
			"identity = idn.IDENTITY",
		},
	}
	for tmpl, lines := range identityLines {
		for _, noIdentity := range []bool{false, true} {
			out, err := DeployTemplates().Render(tmpl, EnvTemplateData{
				ProjectName: "shop", EnvName: "prod", Roots: true, IngressEnabled: true,
				HasFrontend: true, FrontendName: "web", NoIdentity: noIdentity,
				Bindings: "        _hosted(_api)",
			})
			if err != nil {
				t.Fatalf("%s (NoIdentity=%v): render: %v", tmpl, noIdentity, err)
			}
			rendered := string(out)
			for _, line := range lines {
				if got := strings.Contains(rendered, line); got == noIdentity {
					t.Errorf("%s (NoIdentity=%v): contains %q = %v, want %v", tmpl, noIdentity, line, got, !noIdentity)
				}
			}
			// The frontend is declared either way: no users is not no site.
			if !strings.Contains(rendered, "_web_frontend = forge.Frontend {") {
				t.Errorf("%s (NoIdentity=%v): the frontend is no longer declared", tmpl, noIdentity)
			}
			// The frontend's transport fetches with credentials "include"
			// whether or not anyone signs in, so the dev API must allow
			// credentials either way, or the browser refuses every
			// cross-origin response (FORGE_DEV_API_PROXY=off), public RPCs
			// included. TestPublicFrontendTransportNeedsCredentialedCORS pins
			// the browser side.
			if tmpl == "kcl/dev/main.k.tmpl" && !strings.Contains(rendered, `CORS_ALLOW_CREDENTIALS = "true"`) {
				t.Errorf("%s (NoIdentity=%v): the API no longer allows credentials, but the frontend still sends them", tmpl, noIdentity)
			}
		}
	}
}
