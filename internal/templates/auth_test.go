package templates

import (
	"github.com/reliant-labs/forge/internal/configproto"

	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/auth"
)

// bundleTokenRead matches a credential pulled out of a frontend bundle's
// PUBLIC env (NEXT_PUBLIC_* / VITE_* / EXPO_PUBLIC_* are inlined at build
// time and readable by anyone who loads the page).
var bundleTokenRead = regexp.MustCompile(`(?:process\.env|import\.meta\.env)\.[A-Z0-9_]*(?:TOKEN|SECRET|KEY)`)

// TestRenderAppAuthTemplate verifies the OWNED internal/app/auth.go
// scaffold (app-auth.go.tmpl): it parses as Go, is owned (no DO-NOT-EDIT
// banner), exposes SetupAuth with the validator-returning signature the
// cmd serve wiring threads in, reads the per-environment JWT values off the
// TYPED CONFIG, and always returns a real JWT validator so a fresh project
// does real auth out of the box (no dev-auth bypass).
func TestRenderAppAuthTemplate(t *testing.T) {
	data := struct {
		Module             string
		Graph, HasDatabase bool
		NoJWTConfig        bool
		AuthConfigType     string
	}{Module: "example.com/myproject"}

	content, err := ProjectTemplates().Render("app-auth.go.tmpl", data)
	if err != nil {
		t.Fatalf("render app-auth.go.tmpl: %v", err)
	}
	s := string(content)

	fset := token.NewFileSet()
	if _, perr := parser.ParseFile(fset, "auth.go", s, parser.AllErrors); perr != nil {
		t.Fatalf("rendered auth.go does not parse: %v\n%s", perr, s)
	}

	checks := []struct {
		name     string
		contains string
		want     bool
	}{
		// Owned, scaffold-once — NOT a forge-owned generated file.
		{"no generated banner", "DO NOT EDIT", false},
		{"package app", "package app", true},
		// The authenticator whose Authenticate the generated cmd serve
		// threads into AuthDeps.Validate, and on which it installs the
		// API-key validator once infra is open.
		{"SetupAuth signature", "func SetupAuth(cfg *config.Config) (*auth.Authenticator, error) {", true},
		{"SetupAuth wraps the JWT validator", "return auth.NewAuthenticator(validator.Validate), nil", true},
		{"APIKeyValidator seam", "func APIKeyValidator(_ *Infra) auth.KeyValidator {", true},
		{"module config import", "example.com/myproject/pkg/config", true},
		{"forge auth import", "github.com/reliant-labs/forge/pkg/auth", true},
		{"default JWT validator", `Provider: "jwt"`, true},
		// All five per-deployment JWT values arrive on the typed config, the
		// same channel every other per-environment value in a forge app uses.
		// SigningMethod is the one a local run needs most: leave it unwired
		// and the scaffold takes JWTConfig's RS256 default and cannot
		// validate an HS256 token signed with the secret the same file reads.
		{"signing method from config", "cfg.JwtSigningMethod", true},
		{"issuer from config", "cfg.JwtIssuer", true},
		{"audience from config", "cfg.JwtAudience", true},
		{"jwks from config", "cfg.JwtJwksUrl", true},
		{"secret from config", "cfg.JwtSecret", true},
		// No auth bypass: SetupAuth always builds a real validator and never
		// returns a nil (auth-off) validator to the interceptor.
		{"no auth-off nil validator", "return nil, nil", false},
	}
	for _, tc := range checks {
		if found := strings.Contains(s, tc.contains); found != tc.want {
			if tc.want {
				t.Errorf("%s: expected %q to be present in output", tc.name, tc.contains)
			} else {
				t.Errorf("%s: expected %q to NOT be present in output", tc.name, tc.contains)
			}
		}
	}

	// The scaffold is the only file in a forge app that ever carried a
	// forbidigo suppression, and it carried FIVE. Configuration reaches this
	// app one way; a file that opts out of the guardrail is the app teaching
	// its next reader that opting out is normal. Assert on the MECHANISM
	// (os.* environment reads and the suppression that hid them), not on the
	// five variable names — a sixth knob added the old way must fail here too.
	for _, banned := range []string{"os.Getenv", "os.LookupEnv", "os.Environ", "nolint:forbidigo", `"os"`} {
		if strings.Contains(s, banned) {
			t.Errorf("internal/app/auth.go must read configuration off the typed cfg, never %s:\n%s", banned, s)
		}
	}
}

// TestConfigProtoTemplate_DeclaresJWTFields is the other half of Fix 2: the
// typed config has to actually CARRY the values SetupAuth reads, or the
// scaffold does not compile. It also pins the one property that is a
// security decision rather than a naming one — jwt_secret is `sensitive`, so
// the deploy projection binds it to a secretKeyRef instead of writing an
// HS256 signing key into a manifest as a literal.
func TestConfigProtoTemplate_DeclaresJWTFields(t *testing.T) {
	content, err := ProjectTemplates().Render("config.proto.tmpl", configproto.For("myproject").TemplateData("example.com/myproject"))
	if err != nil {
		t.Fatalf("render config.proto.tmpl: %v", err)
	}
	s := string(content)

	for _, decl := range []string{
		"string jwt_signing_method",
		"string jwt_issuer",
		"string jwt_audience",
		"string jwt_jwks_url",
		"string jwt_secret",
	} {
		if !strings.Contains(s, decl) {
			t.Errorf("config.proto declares no %q field — SetupAuth reads it off the typed config and would not compile", decl)
		}
	}

	secretField := protoFieldBlock(t, s, "string jwt_secret")
	if !strings.Contains(secretField, "sensitive: true") {
		t.Errorf("jwt_secret must be `sensitive: true` — an HS* value both verifies AND mints, so it can never be an inline manifest value:\n%s", secretField)
	}
	// The other four are ordinary config: routing a JWKS URL or an issuer
	// through the secret store would demand a Secret entry for a value that
	// is public, and a dev loop that cannot boot without one.
	for _, name := range []string{"jwt_signing_method", "jwt_issuer", "jwt_audience", "jwt_jwks_url"} {
		if block := protoFieldBlock(t, s, "string "+name); strings.Contains(block, "sensitive: true") {
			t.Errorf("%s is public configuration and must not be marked sensitive:\n%s", name, block)
		}
	}
}

// protoFieldBlock returns the option block of the proto field whose
// declaration starts with decl, failing loudly when the field is gone.
func protoFieldBlock(t *testing.T, proto, decl string) string {
	t.Helper()
	i := strings.Index(proto, decl)
	if i < 0 {
		t.Fatalf("config.proto declares no %q field", decl)
	}
	rest := proto[i:]
	j := strings.Index(rest, "}];")
	if j < 0 {
		t.Fatalf("config.proto field %q has no option block", decl)
	}
	return rest[:j]
}

// TestCmdServeTemplate_CallsSetupAuth pins the call-site half of the
// owned-auth contract: the generated runServer calls the OWNED
// app.SetupAuth (unconditionally — auth is code now, not a forge.yaml
// provider gate), threads its validator into AuthDeps.Validate, carries no
// dev-auth bypass, and never references the retired InstallGeneratedAuth /
// GeneratedAuthInterceptor surface.
func TestCmdServeTemplate_CallsSetupAuth(t *testing.T) {
	data := struct {
		Module       string
		ConfigFields map[string]bool
		RESTEnabled  bool
	}{
		Module:       "example.com/myproject",
		ConfigFields: map[string]bool{},
	}

	content, err := ProjectTemplates().Render("cmd-tree-serve.go.tmpl", data)
	if err != nil {
		t.Fatalf("render cmd-tree-serve.go.tmpl: %v", err)
	}
	s := string(content)

	if !strings.Contains(s, "app.SetupAuth(cfg)") {
		t.Error("runServer must call the owned app.SetupAuth(cfg) to build the validator")
	}
	if !strings.Contains(s, "authDeps.Validate = authenticator.Authenticate") {
		t.Error("the authenticator from SetupAuth must be threaded into AuthDeps.Validate")
	}
	// The auth policy is exactly {AnonymousOK: false} — fail-closed, and no
	// auth-bypass field. Pinning the literal locks out re-introducing a
	// bypass toggle AND the non-gating posture: with AnonymousOK: true a
	// credential-less request reaches every handler claim-less, and
	// `auth_required: true` in the proto gates nothing. One measured app
	// served 17 of its 20 CRUD RPCs to anonymous callers that way, with
	// every one of them declaring auth_required: true.
	if !strings.Contains(s, "middleware.AuthDeps{AnonymousOK: false}") {
		t.Error("serve template must build authDeps as middleware.AuthDeps{AnonymousOK: false} — " +
			"fail-closed, with the published RPCs coming from the protos' auth_required declarations")
	}
	if !strings.Contains(s, "middleware.NewAuthInterceptor(authDeps)") {
		t.Error("auth is constructed up front so a server with no validator refuses to start")
	}
	for _, retired := range []string{"InstallGeneratedAuth", "GeneratedAuthInterceptor", "AuthProvider"} {
		if strings.Contains(s, retired) {
			t.Errorf("serve template still references the retired auth surface %q", retired)
		}
	}
}

// TestCmdServeTemplate_InstallsAPIKeyValidatorAfterOpenInfra pins the ORDER
// the API-key wiring depends on (issue #746). A key validator reads the
// database, so it can only be built after app.OpenInfra; the interceptor is
// built before anything is opened, so a server with no authenticator
// refuses to start before it touches a dependency. The two meet through the
// authenticator: built first, key validator installed on it later — and
// before any RPC is mounted, so no request is ever served by an
// authenticator still waiting for its key validator.
func TestCmdServeTemplate_InstallsAPIKeyValidatorAfterOpenInfra(t *testing.T) {
	for _, fields := range []map[string]bool{{}, {"DatabaseUrl": true, "AutoMigrate": true}} {
		content, err := ProjectTemplates().Render("cmd-tree-serve.go.tmpl", struct {
			Module       string
			ConfigFields map[string]bool
			RESTEnabled  bool
		}{Module: "example.com/myproject", ConfigFields: fields})
		if err != nil {
			t.Fatalf("render cmd-tree-serve.go.tmpl: %v", err)
		}
		s := string(content)

		at := func(needle string) int {
			t.Helper()
			i := strings.Index(s, needle)
			if i < 0 {
				t.Fatalf("serve.go (config fields %v) has no %q", fields, needle)
			}
			return i
		}
		setup := at("app.SetupAuth(cfg)")
		interceptor := at("middleware.NewAuthInterceptor(authDeps)")
		openInfra := at("app.OpenInfra(ctx, cfg, logger)")
		install := at("authenticator.UseKeyValidator(app.APIKeyValidator(infra))")
		mount := at("spec.Mount(components")

		if !(setup < interceptor && interceptor < openInfra) {
			t.Errorf("SetupAuth and the interceptor must come BEFORE OpenInfra (refuse to start before touching a dependency)")
		}
		if !(openInfra < install && install < mount) {
			t.Errorf("the API-key validator must be installed AFTER OpenInfra (it reads the DB) and BEFORE any RPC is mounted")
		}
	}
}

// TestMiddlewareTemplate_ReadsTheAPIKeyHeader is the root cause of issue
// #746 at the source: the generated credentialFrom read only
// "Authorization: Bearer" and the session cookie, so an API key sent the way
// forge/pkg/auth names it — X-API-Key — never reached any validator. Parsed
// as Go, so a comment mentioning the header cannot satisfy it.
func TestMiddlewareTemplate_ReadsTheAPIKeyHeader(t *testing.T) {
	content, err := ProjectTemplates().Render("middleware.go.tmpl", struct{ Name string }{Name: "myproject"})
	if err != nil {
		t.Fatalf("render middleware.go.tmpl: %v", err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "middleware.go", content, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("rendered middleware.go does not parse: %v", err)
	}

	// The header is the library's default, declared once, under a name the
	// auth/api-keys skill can point at.
	var header string
	var credentialFrom, newAuthInterceptor *ast.FuncDecl
	var authDeps *ast.StructType
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch spec := spec.(type) {
				case *ast.ValueSpec:
					for i, n := range spec.Names {
						if n.Name == "APIKeyHeader" && i < len(spec.Values) {
							if lit, ok := spec.Values[i].(*ast.BasicLit); ok {
								header, _ = strconv.Unquote(lit.Value)
							}
						}
					}
				case *ast.TypeSpec:
					if st, ok := spec.Type.(*ast.StructType); ok && spec.Name.Name == "AuthDeps" {
						authDeps = st
					}
				}
			}
		case *ast.FuncDecl:
			switch d.Name.Name {
			case "credentialFrom":
				credentialFrom = d
			case "NewAuthInterceptor":
				newAuthInterceptor = d
			}
		}
	}
	if want := (auth.APIKeyConfig{}).EffectiveHeader(); header != want {
		t.Fatalf("middleware.go must declare const APIKeyHeader = %q (forge/pkg/auth's default), got %q", want, header)
	}
	if credentialFrom == nil || newAuthInterceptor == nil || authDeps == nil {
		t.Fatal("middleware.go must declare credentialFrom, NewAuthInterceptor and AuthDeps")
	}

	// credentialFrom READS it — a call h.Get(APIKeyHeader), not a mention.
	readsHeader := false
	ast.Inspect(credentialFrom.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Get" {
			if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == "APIKeyHeader" {
				readsHeader = true
			}
		}
		return true
	})
	if !readsHeader {
		t.Error("credentialFrom must read the API key header (h.Get(APIKeyHeader)); an API key sent on it reaches no validator otherwise")
	}

	// The validator gets the request context: a key check is a DB read.
	src := string(content)
	if !strings.Contains(src, "ValidateContext:     deps.Validate,") {
		t.Error("NewAuthInterceptor must pass AuthDeps.Validate as authn.Policy.ValidateContext, so the validator gets the request's context")
	}
	for _, field := range authDeps.Fields.List {
		if len(field.Names) != 1 || field.Names[0].Name != "Validate" {
			continue
		}
		fn, ok := field.Type.(*ast.FuncType)
		if !ok || len(fn.Params.List) == 0 {
			t.Fatal("AuthDeps.Validate must be a func taking the request context first")
		}
		if sel, ok := fn.Params.List[0].Type.(*ast.SelectorExpr); !ok || sel.Sel.Name != "Context" {
			t.Errorf("AuthDeps.Validate's first parameter must be a context.Context")
		}
	}
}

// TestAPIKeysSkillDescribesTheScaffold keeps the auth/api-keys skill's
// "What the scaffold wires" table honest (issue #746: the skill described a
// SetupAuth wiring the scaffold had no place for, and the header name was
// only findable in forge's source). Each row names a scaffolded file; every
// code span in the row that is a code FRAGMENT — it has a call, an
// assignment or a selector — must appear verbatim in that file as forge
// renders it, and every bare identifier must be declared or used there.
func TestAPIKeysSkillDescribesTheScaffold(t *testing.T) {
	skill, err := ProjectTemplates().Get("skills/forge/auth/api-keys/SKILL.md")
	if err != nil {
		t.Fatalf("the auth/api-keys skill is missing: %v", err)
	}
	render := func(name string, data any) string {
		t.Helper()
		out, err := ProjectTemplates().Render(name, data)
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		return string(out)
	}
	files := map[string]string{
		"pkg/middleware/middleware.go": render("middleware.go.tmpl", struct{ Name string }{Name: "demo"}),
		"internal/app/auth.go": render("app-auth.go.tmpl", struct {
			Module             string
			Graph, HasDatabase bool
			NoJWTConfig        bool
			AuthConfigType     string
		}{Module: "example.com/demo"}),
		"cmd/<bin>/cmd/serve.go": render("cmd-tree-serve.go.tmpl", struct {
			Module       string
			ConfigFields map[string]bool
			RESTEnabled  bool
		}{Module: "example.com/demo", ConfigFields: map[string]bool{}}),
	}

	section := string(skill)
	start := strings.Index(section, "## What the scaffold wires")
	if start < 0 {
		t.Fatal(`the auth/api-keys skill has no "## What the scaffold wires" section`)
	}
	// The table runs to the next heading of any depth.
	section = section[start:]
	if end := regexp.MustCompile(`\n#{2,} `).FindStringIndex(section[3:]); end != nil {
		section = section[:end[0]+3]
	}

	span := regexp.MustCompile("`([^`]+)`")
	ident := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	rows := 0
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		spans := span.FindAllStringSubmatch(line, -1)
		file := spans[0][1]
		src, ok := files[file]
		if !ok {
			t.Errorf("the skill's table names %q, which this test does not know how to render — add it here", file)
			continue
		}
		rows++
		for _, s := range spans[1:] {
			claim := s[1]
			switch {
			case strings.Contains(claim, "…") || claim == "nil":
				// Prose about a value, not a quote of the file.
			case strings.ContainsAny(claim, "(=."):
				if !strings.Contains(src, claim) {
					t.Errorf("the skill says %s contains `%s`; it does not", file, claim)
				}
			case ident.MatchString(claim):
				if !regexp.MustCompile(`\b` + claim + `\b`).MatchString(src) {
					t.Errorf("the skill names `%s` in %s; that file never mentions it", claim, file)
				}
			}
		}
	}
	if rows != len(files) {
		t.Errorf("the skill's table describes %d scaffolded files, want all %d (middleware.go, auth.go, serve.go)", rows, len(files))
	}
}

// TestSessionProviderReadsEachPlatformsEnv pins the one thing a template
// conditional can get wrong SILENTLY: the scaffolded auth provider is a
// single shared file, and each frontend kind exposes public env under a
// different accessor. A Vite tree rendered with `process.env.NEXT_PUBLIC_*`
// still typechecks, still lints, still builds — and reads undefined at
// runtime, so the app is permanently signed out with nothing to point at.
//
// It also asserts the negative: no tree may carry another tree's accessor,
// which is what a stray branch or a copy-paste produces.
//
// And it pins what this provider must NEVER contain: a token. Forge issues
// no credentials, so a scaffolded provider that held one would be a
// credential inlined into a bundle and a session model production does not
// have. Signed-out against a real backend is the honest state.
func TestSessionProviderReadsEachPlatformsEnv(t *testing.T) {
	accessors := map[string]string{
		"nextjs":       "process.env.NEXT_PUBLIC_MOCK_API",
		"vite-spa":     "import.meta.env.VITE_MOCK_API",
		"react-native": "process.env.EXPO_PUBLIC_MOCK_API",
	}
	for platform, want := range accessors {
		t.Run(platform, func(t *testing.T) {
			content, err := FrontendTemplates().Render(
				"shared/src/lib/auth/session-provider.ts.tmpl",
				FrontendTemplateData{Platform: platform},
			)
			if err != nil {
				t.Fatalf("render session-provider for %s: %v", platform, err)
			}
			src := string(content)
			if !strings.Contains(src, want) {
				t.Errorf("%s must read mock mode via %s:\n%s", platform, want, src)
			}
			for other, accessor := range accessors {
				if other == platform {
					continue
				}
				if strings.Contains(src, accessor) {
					t.Errorf("%s carries %s's accessor %s — it reads undefined there, so the app renders the wrong session state", platform, other, accessor)
				}
			}
			// The MECHANISM, not one variable name: ANY token read out of the
			// bundle's public env is a credential forge put in a browser.
			if m := bundleTokenRead.FindString(src); m != "" {
				t.Errorf("%s reads a credential (%s) out of the bundle env — forge issues no tokens, so the scaffolded provider holds none:\n%s", platform, m, src)
			}
		})
	}
}
