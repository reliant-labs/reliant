package generator

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/assets"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/configproto"
	"github.com/reliant-labs/forge/internal/naming"
)

// FrontendConfigProtoRelPath is where a frontend's config message is
// declared, relative to the project root: <name>_config.proto beside the
// project's root config.proto, in layout.Dir().
//
// ONE FILE PER CONSUMER, ONE SHARED PACKAGE. The file is per-frontend so
// that adding a frontend WRITES a new file rather than editing a file that
// already has content in it — which is what makes `forge scaffold frontend`
// safe on an existing project without parsing or rewriting anyone's proto.
//
// The DIRECTORY is shared (layout.Dir(), proto/<project>/config/v1/), so
// every config message stays in the project's one config package and
// therefore in one generated Go package. That is not tidiness: buf's
// STANDARD lint enforces PACKAGE_DIRECTORY_MATCH, so a per-consumer
// directory forces a per-consumer proto package, which forces a
// per-consumer Go package — and pkg/config/config_gen.go imports exactly
// one generated config package and names every config message through it.
// A directory-per-consumer layout does not merely reorganize files; it
// stops the generated Go config shim from compiling.
//
// Sharing the package also keeps shared DEFINITIONS free: a block declared
// in config.proto composes onto a message here by bare name, with no
// cross-package import, so one fact declared once projects into both the
// backend's config and the browser's.
//
// The base name goes through naming.GoPackage, not naming.ToSnakeCase.
// ToSnakeCase splits camelCase/PascalCase boundaries but passes a HYPHEN
// straight through, and `forge scaffold frontend` accepts hyphenated names
// (ValidateFrontendName rejects only a leading "-"). A frontend named
// "internal-console" therefore produced "internal-console_config.proto",
// which buf's STANDARD lint rejects with `Filename ... should be
// lower_snake_case.proto` — a red `buf lint` in the generated CI workflow
// on the project's first push, fixable only by renaming the frontend or
// hand-editing a forge-owned path. GoPackage is the canonical CLI-name →
// snake_case identifier rule (hyphens fold, repeated separators collapse),
// already used for the Go package, the KCL binding and the handler dir, so
// routing through it keeps this filename agreeing with them by construction.
func FrontendConfigProtoRelPath(layout configproto.Layout, frontend string) string {
	return filepath.Join(filepath.FromSlash(layout.Dir()), naming.GoPackage(frontend)+"_config.proto")
}

// FrontendConfigMessageName is the proto message declaring a frontend's
// config. It names the generated KCL schema and the TypeScript type, so it
// is derived once here rather than spelled independently at each site.
func FrontendConfigMessageName(frontend string) string {
	return naming.ToPascalCase(frontend) + "Config"
}

// frontendConfigEnvVars is the env_var set the scaffolded config template
// declares, in the file's own declaration order.
//
// It exists so the SCAFFOLD paths can build a FrontendTypedConfig without
// reading the descriptor. Both scaffold paths write this proto and render
// the frontend's templates in the same pass — before `forge generate` has
// ever run, so there is no descriptor to parse and no generated module to
// inspect. Deriving the presence set from the template's own field list is
// what lets a freshly scaffolded session-provider.ts import the typed module
// it is about to be given.
//
// It must stay in agreement with frontend_config.proto.tmpl;
// TestFrontendConfigTemplateMatchesDeclaredEnvVars is the gate.
//
// NO OIDC FIELDS, AND THAT IS THE POINT. Sign-in is NATIVE: the browser
// POSTs credentials to this app's own API and gets an HttpOnly session
// cookie back, while the server runs the entire OIDC flow. The browser
// never contacts the identity provider, so an issuer URL, client id,
// redirect URI, scope list and resource indicator are all BACKEND config
// (config.proto.tmpl) and none of them belongs in a document delivered to
// a browser.
//
// Shipping them anyway would not merely be redundant. These values ARE the
// browser-side flow: any code handed an issuer and a client id can mount a
// PKCE authorization-code exchange in JavaScript and end up holding a token
// that script can read — which is precisely the architecture the native
// flow removed. Tags 5-9 are `reserved` in the template so an upgraded
// project cannot re-bind an old NEXT_PUBLIC_OIDC_* value to a new meaning.
var frontendConfigEnvVars = []string{
	"API_URL",
	"MOCK_API",
	"APP_VERSION",
	"OTEL_ENDPOINT",
}

// frontendConfigDescriptions carries each scaffolded field's `description`
// verbatim from frontend_config.proto.tmpl.
//
// The descriptions become the TSDoc on the generated config_gen.ts members,
// so without them the module this package writes at scaffold time would
// differ from the one `forge generate` derives from the descriptor — the
// same file, rewritten on the next generate, for no reason a reader could
// see. TestFrontendConfigTemplateMatchesDeclaredEnvVars covers the key set;
// keeping the text here in step with the template is what keeps the two
// renders byte-identical.
var frontendConfigDescriptions = map[string]string{
	"API_URL":       "Origin of the Connect RPC endpoint this frontend calls. Every service mounts onto the binary's one Connect mux, so this is one origin for the whole API. Deployed environments pin their own in deploy/kcl/<env>/config.k. IN THE DEV LOOP THIS DEFAULT IS STALE AND MUST NOT BE TRUSTED: `forge env up` gives the backend a kernel-assigned port and injects the live origin as NEXT_PUBLIC_API_URL / VITE_API_URL / EXPO_PUBLIC_API_URL, which src/lib/connect.ts prefers over everything here. Build a client from the generated transport rather than reading this value, or you will dial the port the default happens to name — which in a project with a dev IdP is the IdP.",
	"MOCK_API":      "Mock-transport mode: `true` answers every RPC locally from fixtures and signs the UI in as a fixture user, so no IdP and no /auth/session are needed. `hybrid` overlays ?scenario= fixtures over a real backend; the UI shows the same fixture user, but forwarded RPCs carry only the browser's real session cookie. Empty means the real backend, which is the deployed value.",
	"APP_VERSION":   "Build identifier surfaced in the UI and as a telemetry attribute. Empty renders as a development placeholder.",
	"OTEL_ENDPOINT": "Ingest base for BROWSER telemetry (errors, console, network, traces): a path on the app's own origin, normally `/_otel`, which the dev server proxies — and a deployed environment's ingress must route — to a collector's OTLP/HTTP port. The browser never holds a collector address or a secret. Empty or `off` disables browser telemetry; the dev default is `/_otel`, a frontend on platform hosting (forge.OnHosted) defaults to `/_otel` too, and any other deployed runtime defaults to empty.",
}

// ScaffoldedFrontendTypedConfig is the presence set a frontend gets from
// the config proto this package scaffolds for it.
func ScaffoldedFrontendTypedConfig() FrontendTypedConfig {
	return FrontendTypedConfigFrom(frontendConfigEnvVars)
}

// ScaffoldedFrontendConfig is the codegen view of that same declaration:
// the FrontendConfig `forge generate` would derive from the descriptor once
// the proto above has been compiled.
//
// It exists because the two are needed at the SAME moment and only one of
// them used to be produced. `forge scaffold frontend` renders templates that
// import the typed module (src/lib/auth/session-provider.ts does so
// unconditionally: `import ... from "@/lib/config_gen"`), and it
// gates that on ScaffoldedFrontendTypedConfig — but it never wrote the
// module those imports name, because writing it lived only in the generate
// pipeline's "frontend typed config" step. The add verb deliberately does
// NOT run generate (staging a frontend must not trigger project-wide codegen
// churn), so a freshly added frontend shipped a tree that could not
// typecheck: `Cannot find module '@/lib/config_gen'`, from the scaffold's own
// files, before the user had written a line. This is the same bug class as
// connect.ts referencing a mock-transport only generate emitted.
//
// The field set is derived from frontendConfigEnvVars so it cannot drift
// from the proto beside it; the values here mirror the template's own
// declarations. `forge generate` later re-derives this from the compiled
// descriptor and rewrites the module — the point is only that the tree is
// coherent before it does.
func ScaffoldedFrontendConfig(frontend string, apiPort int) codegen.FrontendConfig {
	fields := make([]codegen.ConfigField, 0, len(frontendConfigEnvVars))
	for _, env := range frontendConfigEnvVars {
		f := codegen.ConfigField{
			Name:        naming.ToSnakeCase(env),
			ProtoType:   "string",
			GoType:      "string",
			EnvVar:      env,
			Description: frontendConfigDescriptions[env],
		}
		// api_url is the one scaffolded field carrying a default, and it
		// embeds the API port the same way the template does.
		if env == "API_URL" {
			f.DefaultValue = fmt.Sprintf("http://localhost:%d", apiPort)
		}
		f.GoName = naming.ToPascalCase(f.Name)
		fields = append(fields, f)
	}
	return codegen.FrontendConfig{
		Frontend:    frontend,
		MessageName: FrontendConfigMessageName(frontend),
		Fields:      fields,
	}
}

// WriteScaffoldedFrontendConfigTS writes src/lib/config_gen.ts for a
// just-scaffolded frontend, so the templates that import "@/lib/config_gen"
// resolve in the tree the add verb leaves behind.
//
// platform is the same label the generate pipeline passes
// (nextjs / vite-spa / react-native): it selects how the module reads its
// values server-side, so it must match the frontend's kind.
//
// Written through the Tier-1 writer with a nil checksum tracker, exactly as
// the generate step does — the next `forge generate` re-derives the module
// from the compiled descriptor and overwrites this one, which is why nothing
// here needs to be preserved.
func WriteScaffoldedFrontendConfigTS(root, projectName, frontend, platform string, apiPort int) error {
	fc := ScaffoldedFrontendConfig(frontend, apiPort)
	body, err := codegen.GenerateFrontendConfigTS(fc, projectName, platform)
	if err != nil {
		return fmt.Errorf("render config_gen.ts for %s: %w", frontend, err)
	}
	rel := filepath.Join("frontends", frontend, filepath.FromSlash(codegen.FrontendConfigTSFile))
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(rel)), 0o755); err != nil {
		return fmt.Errorf("create lib dir for %s: %w", frontend, err)
	}
	if _, err := checksums.WriteGeneratedFileTier1(root, rel, []byte(body), nil, true); err != nil {
		return fmt.Errorf("write config_gen.ts for %s: %w", frontend, err)
	}
	return nil
}

// frontendConfigProtoData is the render input for frontend_config.proto.tmpl.
type frontendConfigProtoData struct {
	configproto.TemplateData
	Frontend    string
	MessageName string
	APIPort     int
}

// WriteFrontendConfigProto declares the named frontend's public runtime
// config, with the message-level (forge.v1.frontend_config) annotation that
// activates the three projections: the typed zod module the bundle imports,
// the KCL schema per-env values are authored against, and the config.js
// document the browser reads at boot.
//
// Without this file the config system is complete and unreachable — every
// generator keys on the annotation, so a project that declares none
// correctly emits nothing and its frontend keeps reading build-time
// process.env.NEXT_PUBLIC_*.
//
// APPEND-ONLY BY CONSTRUCTION. Adding a frontend to an existing project
// writes a NEW file, so no existing config content is read, rewritten or
// reordered — the failure mode the workloads.k append helper has to guard
// against does not arise here, because there is nothing to append INTO.
//
// It refuses to overwrite an existing file. A project that already declares
// this frontend's config has values in it (an issuer, a client id) that a
// re-scaffold must not silently discard.
func WriteFrontendConfigProto(root string, layout configproto.Layout, modulePath, frontend string, apiPort int) error {
	if frontend == "" {
		return nil
	}
	rel := FrontendConfigProtoRelPath(layout, frontend)
	dest := filepath.Join(root, rel)

	if _, err := os.Stat(dest); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", rel, err)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(rel), err)
	}

	return assets.WriteTemplateWithData("frontend_config.proto.tmpl", dest, frontendConfigProtoData{
		TemplateData: layout.TemplateData(modulePath),
		Frontend:     frontend,
		MessageName:  FrontendConfigMessageName(frontend),
		APIPort:      apiPort,
	})
}
