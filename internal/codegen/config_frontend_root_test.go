// A FRONTEND-bound config message must never become the project's root
// Config.
//
// The root selection skipped binary-bound messages but not frontend-bound
// ones. That asymmetry was invisible for as long as no SCAFFOLDED project
// declared a frontend config: the only projects that had one had written it
// by hand, alongside an AppConfig that happened to sort first.
//
// Scaffolding the frontend config annotation made it reachable. Config
// messages reach the generators through the forge descriptor, whose
// fragments merge in filename order, so `admin_config.proto` sorts BEFORE
// `config.proto` — AdminConfig arrived first, became RootConfigMessage, and
// pkg/config/config_gen.go emitted `type Config = configv1.AdminConfig`.
// Every cfg.Jwt* read in internal/app then failed to compile, so `forge
// generate` aborted at its own build-validation step.
//
// The failure is name-ordering dependent, which is what makes it worth
// pinning: a frontend named "web" sorts AFTER "config" and works by
// accident, while one named "admin" does not.
package codegen

import "testing"

// frontendAndAppMessages is the shape a scaffolded project has once it
// declares a frontend: one project-global AppConfig and one frontend-bound
// message. The frontend message is FIRST, reproducing the descriptor order
// a frontend whose name sorts before "config" produces.
func frontendAndAppMessages() []ConfigMessage {
	return []ConfigMessage{
		{
			Name:     "AdminConfig",
			Frontend: "admin",
			Fields: []ConfigField{
				{Name: "api_url", GoName: "ApiUrl", GoType: "string", ProtoType: "string", EnvVar: "API_URL"},
				{Name: "oidc_issuer", GoName: "OidcIssuer", GoType: "string", ProtoType: "string", EnvVar: "OIDC_ISSUER"},
			},
		},
		{
			Name: "AppConfig",
			Fields: []ConfigField{
				{Name: "port", GoName: "Port", GoType: "int32", ProtoType: "int32", EnvVar: "PORT"},
				{Name: "jwt_issuer", GoName: "JwtIssuer", GoType: "string", ProtoType: "string", EnvVar: "JWT_ISSUER"},
			},
		},
	}
}

// The reproduction: the root Config must be the project-global message,
// even when a frontend-bound message is encountered first.
func TestBuildConfigTemplateData_FrontendMessageIsNeverTheRootConfig(t *testing.T) {
	data := BuildConfigTemplateData(frontendAndAppMessages())

	if data.RootConfigMessage != "AppConfig" {
		t.Fatalf("RootConfigMessage = %q, want \"AppConfig\" — a frontend's config is projected to "+
			"TypeScript and KCL, not to Go, so aliasing the backend's Config to it types the whole "+
			"server against a browser's config surface and stops internal/app compiling",
			data.RootConfigMessage)
	}
}

// The other half: a frontend's fields must not be flattened onto the root
// Config either. They would become backend flags and env vars for values
// only a browser reads.
func TestBuildConfigTemplateData_FrontendFieldsStayOffTheRootConfig(t *testing.T) {
	data := BuildConfigTemplateData(frontendAndAppMessages())

	for _, f := range data.Fields {
		if f.EnvVar == "OIDC_ISSUER" || f.EnvVar == "API_URL" {
			t.Errorf("root config carries field %q, which is declared on a FRONTEND-bound message — "+
				"it would register a backend flag for a value only the browser reads", f.EnvVar)
		}
	}
	// Guard against the test passing because nothing was collected at all.
	var sawRoot bool
	for _, f := range data.Fields {
		if f.EnvVar == "JWT_ISSUER" {
			sawRoot = true
		}
	}
	if !sawRoot {
		t.Fatal("root config carries none of AppConfig's own fields — the collection produced nothing, " +
			"so the assertion above passed vacuously")
	}
}
