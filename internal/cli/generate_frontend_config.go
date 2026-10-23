package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// generateFrontendConfigModules emits, for every frontend bound to a config
// message by (forge.v1.frontend_config):
//
//   - src/lib/config_gen.ts — the typed, validated module the app imports
//     instead of touching process.env / import.meta.env (Tier-1).
//   - <static>/config.js — the DEV runtime document, rendered from the DEV
//     ENVIRONMENT'S OWN KCL, with the proto defaults supplying any field
//     that environment does not pin.
//
// Reading the environment matters because this file is forge-OWNED: it is
// rewritten on every `forge generate`, so a wrong value here cannot be
// hand-corrected durably. Rendering it from proto defaults alone meant a
// dev environment declaring `environment = "dev"` in deploy/kcl/dev/config.k
// still booted against a document saying "production" — silently wrong, in
// the one environment a developer never deploys and therefore never gets a
// second chance to notice.
//
// The values come from the SAME projection lambda a deploy uses
// (frontend_config_gen.k's <name>Runtime), evaluated against the same
// config.k — see loadFrontendRuntimeConfig. That is what keeps the document
// a developer runs against and the document an environment ships the same
// KIND of artifact rather than two independently-derived guesses.
func generateFrontendConfigModules(
	cfg *config.ProjectConfig,
	messages []codegen.ConfigMessage,
	projectDir string,
	cs *checksums.FileChecksums,
) error {
	frontendConfigs := codegen.FrontendConfigsFromMessages(messages)
	if len(frontendConfigs) == 0 {
		return nil
	}

	// The OIDC client id reaches this render the same way every other
	// declared value does — by reading deploy/kcl/<env>/config.k. A dev
	// identity (forge.ManagedIdentity) is converged by `forge env up` into
	// the secret store and consumed by the backend; the browser holds no
	// OIDC config under native sign-in.

	// The dev environment's declared values, when it has any. A project
	// with no dev env yet (fresh scaffold), no frontend instance in its
	// config.k, or a KCL render that fails degrades to proto defaults —
	// `forge generate` must not require a renderable environment to emit
	// a frontend's typed module.
	devValues, err := loadFrontendRuntimeConfig(projectDir, frontendConfigDevEnv, frontendConfigs)
	if err != nil {
		fmt.Printf("  ⚠️  frontend config: could not read deploy/kcl/%s/config.k (%v); "+
			"rendering config.js from proto defaults.\n", frontendConfigDevEnv, err)
		devValues = nil
	}

	byName := make(map[string]config.FrontendConfig, len(cfg.Frontends))
	for _, fe := range cfg.Frontends {
		byName[fe.Name] = fe
	}

	for _, fc := range frontendConfigs {
		fe, ok := byName[fc.Frontend]
		if !ok {
			// A config bound to a frontend that does not exist is a
			// generate-time error, not a silently unused message — the
			// same rule (forge.v1.binary_config) enforces for a missing
			// cmd/<name>. Silence here would mean an app reading defaults
			// forever while its real values sat in a file nothing loads.
			return fmt.Errorf(
				"config message %s is annotated (forge.v1.frontend_config) = {frontend: %q}, "+
					"but forge.yaml declares no frontend named %q%s\n\n"+
					"Either correct the annotation in proto/<project>/config/v1/config.proto, or add the "+
					"frontend with `forge scaffold frontend %s`",
				fc.MessageName, fc.Frontend, fc.Frontend, knownFrontendsSuffix(cfg), fc.Frontend)
		}

		feDir, ok := fe.Dir(projectDir)
		if !ok {
			// No directory in this repository — a cross-repo
			// source pin, or a path outside the project root.
			continue
		}

		// ── Tier-1: src/lib/config_gen.ts ──
		tsBody, err := codegen.GenerateFrontendConfigTS(fc, cfg.Name, frontendPlatform(fe.Type))
		if err != nil {
			return fmt.Errorf("render config_gen.ts for %s: %w", fe.Name, err)
		}
		tsRel := filepath.Join(feDir, filepath.FromSlash(codegen.FrontendConfigTSFile))
		if err := os.MkdirAll(filepath.Join(projectDir, filepath.Dir(tsRel)), 0o755); err != nil {
			return fmt.Errorf("create lib dir for %s: %w", fe.Name, err)
		}
		if _, err := checksums.WriteGeneratedFileTier1(projectDir, tsRel, []byte(tsBody), cs, true); err != nil {
			return fmt.Errorf("write config_gen.ts for %s: %w", fe.Name, err)
		}

		// ── Tier-1: src/lib/config_schema_gen.json ──
		// The keys a runtime config document for this frontend may carry.
		// Read at render by whichever project declares this frontend's
		// environments — possibly another repo, which cannot see this
		// proto — to refuse a runtime_config key the bundle never reads
		// (checkFrontendRuntimeConfigKeys).
		schemaBody, err := codegen.GenerateFrontendConfigSchema(fc)
		if err != nil {
			return err
		}
		schemaRel := filepath.Join(feDir, filepath.FromSlash(codegen.FrontendConfigSchemaFile))
		if _, err := checksums.WriteGeneratedFileTier1(projectDir, schemaRel, []byte(schemaBody), cs, true); err != nil {
			return fmt.Errorf("write config_schema_gen.json for %s: %w", fe.Name, err)
		}

		// ── Tier-1: the dev runtime document ──
		staticDir := frontendStaticDir(fe.Type)
		if staticDir == "" {
			continue // react-native has no served static asset root
		}
		values := withRuntimeEnvironment(frontendRuntimeValues(fc, devValues[fc.Frontend]), frontendConfigDevEnv)
		encoded, err := json.MarshalIndent(values, "", "  ")
		if err != nil {
			return fmt.Errorf("encode dev config for %s: %w", fe.Name, err)
		}
		jsBody := codegen.GenerateFrontendConfigJS(fe.Name, frontendConfigDevEnv, string(encoded))
		jsRel := filepath.Join(feDir, staticDir, codegen.FrontendConfigJSFile)
		if err := os.MkdirAll(filepath.Join(projectDir, feDir, staticDir), 0o755); err != nil {
			return fmt.Errorf("create static dir for %s: %w", fe.Name, err)
		}
		if _, err := checksums.WriteGeneratedFileTier1(projectDir, jsRel, []byte(jsBody), cs, true); err != nil {
			return fmt.Errorf("write config.js for %s: %w", fe.Name, err)
		}
	}

	return nil
}

// frontendConfigDevEnv is the environment whose values the checked-in
// runtime document carries. It is the one environment forge can render a
// document for at generate time without being told which to pick — every
// other environment's document is produced on the deploy path, where the
// environment is named explicitly.
const frontendConfigDevEnv = "dev"

// frontendRuntimeValues renders one frontend's runtime document contents:
// the environment's declared KCL values, with the proto's own defaults
// supplying every field the environment does not pin.
//
// The layering is what makes a sparse config.k work the way its comment
// promises ("only fields this env pins are set — every other field inherits
// its schema default"). envValues is the rendered projection, so a field
// the env DOES pin arrives here already carrying that env's value and
// overwrites the default.
//
// A field with neither a default nor an env value is omitted rather than
// emitted empty, so the generated TypeScript module's own default and
// validation rules decide what happens — in one place, rather than here and
// there disagreeing about what "unset" means.
func frontendRuntimeValues(fc codegen.FrontendConfig, envValues map[string]any) map[string]any {
	out := make(map[string]any, len(fc.Fields))
	for _, f := range fc.Fields {
		if f.MessageType != "" {
			continue
		}
		key := f.EnvVar
		if key == "" {
			key = f.Name
		}
		if v, ok := envValues[key]; ok {
			out[key] = v
			continue
		}
		if f.DefaultValue == "" {
			continue
		}
		out[key] = f.DefaultValue
	}
	return dropHalfConfiguredOIDC(out)
}

// frontendEnvironmentKey is the runtime-document key that carries the forge env
// name. src/lib/otel_gen.ts reads it first and reports it as the browser's
// deployment.environment.name, the key the backend uses too.
const frontendEnvironmentKey = "ENVIRONMENT"

// withRuntimeEnvironment adds the env name the document is rendered for, unless
// the document already states one (a typed `environment` field wins).
//
// It is in the DOCUMENT, not only in the dev server's env, because a hosted
// release is built once and promoted: anything inlined at build time would
// carry the build's environment into every other one. The document is the
// part that differs per environment.
func withRuntimeEnvironment(values map[string]any, envName string) map[string]any {
	if envName == "" {
		return values
	}
	if v, _ := stringValue(values[frontendEnvironmentKey]); v != "" {
		return values
	}
	if values == nil {
		values = map[string]any{}
	}
	values[frontendEnvironmentKey] = envName
	return values
}

// OIDC env-var names the frontend's auth provider reads as a matched pair.
const (
	oidcIssuerKey   = "OIDC_ISSUER"
	oidcClientIDKey = "OIDC_CLIENT_ID"
)

// dropHalfConfiguredOIDC enforces the invariant the frontend's auth provider
// actually has: an issuer and a client id are set TOGETHER or not at all.
//
// readOidcConfig() reads "neither" as the no-auth posture and "both" as real
// auth, but THROWS on one without the other — from inside the provider that
// wraps the entire app, so the user sees "Application error" instead of any
// UI. A config.k that pins oidc_issuer to a literal while the client id is
// still empty (nothing has converged an IdP yet) produces precisely that pair.
//
// Both halves of that reasoning are right; they were just never true at the
// same time. Empty selecting "the frontend's no-auth posture" holds only
// when the issuer is empty too, and nothing dropped the issuer. Projection
// is the last moment before the value is written into a shipped artifact,
// so the pair is completed here: an unusable issuer is cleared rather than
// published.
//
// Clearing the ISSUER (not synthesizing a client id) is the only safe
// direction: a fabricated client id would get a user all the way to a real
// IdP before failing, while no-auth is a working, obviously-local posture.
func dropHalfConfiguredOIDC(values map[string]any) map[string]any {
	issuer, hasIssuer := stringValue(values[oidcIssuerKey])
	clientID, _ := stringValue(values[oidcClientIDKey])
	if !hasIssuer || issuer == "" || clientID != "" {
		return values
	}
	values[oidcIssuerKey] = ""
	// Say so. Silently dropping the issuer would leave a developer who
	// EXPECTS a sign-in screen staring at an app that never asks for one,
	// with nothing anywhere naming the reason.
	fmt.Printf("  ⚠️  frontend config: issuer %q has no client id (the dev IdP was "+
		"unreachable and none is stored), so this render selects the NO-AUTH posture "+
		"rather than a half-configured one the app cannot start with.\n"+
		"      For a real sign-in: run `forge env up`, which brings the dev IdP up and converges the "+
		"env's identity.\n", issuer)
	return values
}

// stringValue reads a projected config value as a string. Values arrive from
// the KCL render as `any`, so a non-string (a number, a bool) is reported as
// "not a string here" rather than coerced.
func stringValue(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// frontendStaticDir names the directory a frontend serves verbatim at its
// root, where the runtime config document has to land to be reachable as
// <basePath>/config.js.
func frontendStaticDir(feType string) string {
	switch strings.ToLower(strings.TrimSpace(feType)) {
	case "vite", "vite-spa":
		return "public"
	case "rn", "react-native", "expo":
		return ""
	default: // nextjs
		return "public"
	}
}

// frontendPlatform maps a forge.yaml frontend type to the platform label
// the TypeScript generator branches on.
func frontendPlatform(feType string) string {
	switch strings.ToLower(strings.TrimSpace(feType)) {
	case "vite", "vite-spa":
		return "vite-spa"
	case "rn", "react-native", "expo":
		return "react-native"
	default:
		return "nextjs"
	}
}

// knownFrontendsSuffix lists the frontends that DO exist, so the refusal
// above shows the available names rather than only the missing one.
func knownFrontendsSuffix(cfg *config.ProjectConfig) string {
	if len(cfg.Frontends) == 0 {
		return " (it declares no frontends at all)"
	}
	names := make([]string, 0, len(cfg.Frontends))
	for _, fe := range cfg.Frontends {
		names = append(names, fe.Name)
	}
	sort.Strings(names)
	return " (it declares: " + strings.Join(names, ", ") + ")"
}
