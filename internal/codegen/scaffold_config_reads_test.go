package codegen

import (
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// jwtFields is every field auth.go's JWT validator reads, under env names
// prefixed with prefix.
func jwtFields(prefix string) []ConfigField {
	var out []ConfigField
	for _, g := range jwtConfigFields {
		out = append(out, ConfigField{Name: strings.ToLower(g), GoName: g, GoType: "string", ProtoType: "string", EnvVar: prefix + strings.ToUpper(g)})
	}
	return out
}

// auth.go and login_broker.go read the config that declares their fields: a
// command config's, or — when no config the binary loads declares them — none.
func TestConfigDeclaring_TheConfigThatDeclaresTheScaffoldsFields(t *testing.T) {
	dir := t.TempDir()
	writeRootGo(t, dir)
	msgs := reliantShapedMessages()
	if typ, found := configDeclaringIn(msgs, dir, jwtConfigFields); found {
		t.Fatalf("no config declares the jwt_* fields, yet configDeclaringIn found %q", typ)
	}
	for i := range msgs {
		if msgs[i].Name == "ApiConfig" {
			msgs[i].Fields = append(msgs[i].Fields, jwtFields("API_")...)
		}
	}
	if typ, found := configDeclaringIn(msgs, dir, jwtConfigFields); !found || typ != "ApiConfig" {
		t.Fatalf("configDeclaringIn = %q, %v; want ApiConfig, the command config that declares them", typ, found)
	}
	app := []ConfigMessage{{Name: "AppConfig", Fields: jwtFields("")}}
	if typ, found := configDeclaringIn(app, dir, jwtConfigFields); !found || typ != "" {
		t.Fatalf("an AppConfig that declares them is the root config: %q, %v", typ, found)
	}
}

func renderFormatted(t *testing.T, tmpl string, data any) string {
	t.Helper()
	out, err := templates.ProjectTemplates().Render(tmpl, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := format.Source(out); err != nil {
		t.Fatalf("%s does not format: %v\n%s", tmpl, err, out)
	}
	return string(out)
}

func TestAuthScaffold_ReadsOnlyWhatItsConfigDeclares(t *testing.T) {
	none := renderFormatted(t, "app-auth.go.tmpl", AuthSetupTemplateData{Module: "example.com/rbc", Graph: true, NoJWTConfig: true})
	if regexp.MustCompile(`\bcfg\.[A-Z]`).MatchString(none) || strings.Contains(none, "oauth2") || !strings.Contains(none, "func SetupAuth(cfg *config.Config) (*auth.Authenticator, error) {\n\treturn nil, nil\n}") {
		t.Errorf("with no jwt_* settings SetupAuth reads none and builds no validator:\n%s", none)
	}
	cmd := renderFormatted(t, "app-auth.go.tmpl", AuthSetupTemplateData{Module: "example.com/rbc", Graph: true, AuthConfigType: "ApiConfig"})
	if !strings.Contains(cmd, "func SetupAuth(cfg *config.ApiConfig) (*auth.Authenticator, error) {") || !strings.Contains(cmd, "jwksURL := cfg.JwtJwksUrl") {
		t.Errorf("SetupAuth reads the command config that declares the jwt_* fields:\n%s", cmd)
	}
	providers := renderFormatted(t, "providers-graph.go.tmpl", providersGraphData{Module: "example.com/rbc", ConfigFields: map[string]bool{}, AuthConfigType: "ApiConfig", NoBrokerConfig: true})
	if !strings.Contains(providers, "Config *config.ApiConfig") || strings.Contains(providers, "LoginBroker") {
		t.Errorf("UserAuth takes the config SetupAuth reads, and no sign-in routes are wired without a broker config:\n%s", providers)
	}

	// A project whose own pkg/middleware declares no NewAuthInterceptor
	// gets forge's authn interceptor over the generated allow-list.
	own := renderFormatted(t, "providers-graph.go.tmpl", providersGraphData{Module: "example.com/rbc", ConfigFields: map[string]bool{"RateLimitRps": true, "RateLimitBurst": true}, NoProjectAuthInterceptor: true})
	if strings.Contains(own, "middleware.NewAuthInterceptor") || strings.Contains(own, "middleware.AuthDeps") || strings.Contains(own, "middleware.ClaimsFromContext") ||
		!strings.Contains(own, "authn.NewInterceptor(policy)") || !strings.Contains(own, "middleware.UnauthenticatedProcedures") {
		t.Errorf("without the project's NewAuthInterceptor, UserAuth calls forge's:\n%s", own)
	}
}

func TestProjectDeclaresAuthInterceptor(t *testing.T) {
	dir := t.TempDir()
	if !projectDeclaresAuthInterceptor(dir) {
		t.Error("a project with no pkg/middleware yet is about to get forge's")
	}
	mw := filepath.Join(dir, "pkg", "middleware")
	if err := os.MkdirAll(mw, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(mw, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("procedures_gen.go", "package middleware\n")
	write("claims.go", "package middleware\n\nfunc Other() {}\n")
	if projectDeclaresAuthInterceptor(dir) {
		t.Error("a middleware package of the project's own without NewAuthInterceptor")
	}
	write("middleware.go", "package middleware\n\nfunc NewAuthInterceptor(deps AuthDeps) {}\n")
	if !projectDeclaresAuthInterceptor(dir) {
		t.Error("forge's scaffolded middleware.go declares it")
	}
}
