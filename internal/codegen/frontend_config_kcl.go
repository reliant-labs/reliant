package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/naming"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// FrontendConfigModule is the generated KCL module name carrying every
// frontend's typed schema plus its runtime-config projection.
//
// It is a SEPARATE module from config_gen.k (the backend's) because the two
// have different audiences and different rules: config_gen.k models secret
// references and projects to a workload's env, while this one models a
// strictly public surface and projects to a JSON document served to a
// browser. Keeping them apart makes the boundary legible in the file tree —
// anything in here is public by construction.
const FrontendConfigModule = "frontend_config_gen"

// GenerateFrontendConfigNative emits the project-level, forge-owned
// frontend config module at <kclDirAbs>/frontend_config_gen.k.
//
// It mirrors GenerateConfigNativeShared's ownership handling so both KCL
// config modules land under the same tier and the same checksum ledger.
// When no frontend is annotated, any previously-emitted module is removed
// rather than left behind stale.
func GenerateFrontendConfigNative(configs []FrontendConfig, projectName, projectDir, kclDirAbs string, cs *checksums.FileChecksums) error {
	outPath := filepath.Join(kclDirAbs, FrontendConfigModule+".k")

	if len(configs) == 0 {
		// Nothing annotated: withdraw a module from a previous run so a
		// removed annotation cannot leave a schema nothing projects into.
		// Journaled so an aborted run restores it.
		if err := checksums.RemoveJournaled(outPath); err != nil {
			return fmt.Errorf("remove %s: %w", outPath, err)
		}
		return nil
	}

	body, err := GenerateFrontendConfigKCL(configs, projectName)
	if err != nil {
		return fmt.Errorf("generate frontend config module: %w", err)
	}

	if cs != nil && projectDir != "" {
		if rel, rerr := filepath.Rel(projectDir, outPath); rerr == nil {
			if werr := writeForgeOwned(projectDir, rel, []byte(body), cs); werr != nil {
				return fmt.Errorf("write %s: %w", outPath, werr)
			}
			return nil
		}
	}
	if werr := writeUserScaffold(outPath, []byte(body)); werr != nil {
		return fmt.Errorf("write %s: %w", outPath, werr)
	}
	return nil
}

// EnsureFrontendConfigInstances makes sure an env's user-owned config.k
// declares one typed VALUES instance per frontend config — the half the
// runtime projection reads.
//
// It appends rather than scaffolding a whole file, because config.k is
// shared with the backend's app_config and is written once
// (write-if-absent). A frontend annotated AFTER the env was created — the
// normal order, since a project scaffolds its envs long before it declares
// a frontend config — would otherwise never get an instance, leaving the
// projection nothing to project and every environment silently on proto
// defaults.
//
// Only MISSING instances are appended, matched by schema name, so this is
// idempotent and never touches values an author has edited. Returns the
// frontends it added an instance for.
func EnsureFrontendConfigInstances(configs []FrontendConfig, kclDirAbs, envName, projectName string) ([]string, error) {
	if len(configs) == 0 {
		return nil, nil
	}
	path := filepath.Join(kclDirAbs, envName, "config.k")
	src, err := os.ReadFile(path)
	if err != nil {
		// No config.k yet: the backend scaffolder owns creating it, and it
		// runs in the same pass. Nothing to append to is not an error.
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	body := string(src)
	var (
		added   []string
		appends strings.Builder
	)
	for _, fc := range configs {
		schemaName, _ := KCLFrontendConfigName(fc.MessageName)
		if frontendConfigInstanceDeclared(body, schemaName) {
			continue
		}
		appends.WriteString(renderFrontendConfigInstance(fc, schemaName, envName, projectName))
		added = append(added, fc.Frontend)
	}
	if len(added) == 0 {
		return nil, nil
	}

	var out strings.Builder
	out.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		out.WriteString("\n")
	}
	if !strings.Contains(body, "import "+FrontendConfigModule) {
		out.WriteString("\n# ── Frontend runtime config (PUBLIC — served to the browser) ──────────\n")
		out.WriteString("# Projected into each frontend's config.js. These values reach the browser\n")
		out.WriteString("# at RUNTIME, not baked into the bundle, so one built bundle can be\n")
		out.WriteString("# promoted between environments.\n")
		fmt.Fprintf(&out, "import %s\n", FrontendConfigModule)
	}
	out.WriteString(appends.String())

	// The plugin import, checked against the FINAL content — the
	// resolve_port call lives in the appended instance, not in the body
	// that was read from disk, so testing `body` alone always missed it and
	// emitted a file that referenced an unimported module.
	final := out.String()
	if strings.Contains(final, "plugin.resolve_port") && !strings.Contains(final, kclPluginImport) {
		final = kclPluginImport + "\n" + final
	}

	if err := atomicfile.WriteFile(path, []byte(final), 0o644); err != nil {

		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return added, nil
}

// frontendConfigInstanceDeclared reports whether config.k already binds a
// variable to the given schema. Matched on the SCHEMA (the qualified type
// reference), not the variable name, because the variable name is the
// author's choice — renaming it must not make forge append a duplicate.
func frontendConfigInstanceDeclared(src, schemaName string) bool {
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		_, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		typeRef := strings.TrimSpace(strings.Split(strings.Split(strings.TrimSpace(rest), "=")[0], " ")[0])
		if dot := strings.LastIndex(typeRef, "."); dot >= 0 && typeRef[dot+1:] == schemaName {
			return true
		}
	}
	return false
}

// renderFrontendConfigInstance emits one frontend's typed VALUES instance.
//
// It is SPARSE on the same rule as the backend's per-env scaffold: only a
// field KCL would otherwise reject the instance for — required with no
// schema default — is pinned, and everything else inherits the schema
// default projected from the proto. An env that wants to differ says so by
// editing this block, which is the one place a per-env frontend value is
// authored.
func renderFrontendConfigInstance(fc FrontendConfig, schemaName, envName, projectName string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s: %s.%s = {\n", naming.ToSnakeCase(fc.MessageName), FrontendConfigModule, schemaName)
	for _, f := range fc.Fields {
		if f.MessageType != "" {
			continue
		}
		switch {
		case f.EnvVar == configAPIURLEnvVar && envName == configDevEnvName:
			// THE API's OWN ORIGIN, pinned for dev.
			//
			// The proto default is :8080, which in a project with a dev IdP
			// is the ISSUER's port, not the API's — so a frontend that took
			// the default sent every call, including its own auth requests,
			// to the identity provider. That surfaced as "those credentials
			// were not accepted" from a server that had never seen them.
			//
			// _api_port is declared in main.k and is the same value the
			// service binds, so the two cannot drift.
			// resolve_port with the SAME key main.k uses, so both files get
			// the identical number: the plugin is deterministic per key and
			// caches its answer in .forge/ports-dev.json. config.k cannot
			// reference main.k's _api_port variable — different module — so
			// the shared key is what ties them together.
			// A plain concatenation, not a "${...}" interpolation: KCL
			// cannot parse a quoted string inside an interpolation, so the
			// nested resolve_port call has to sit outside the literal.
			fmt.Fprintf(&b, "    %s = \"http://localhost:\" + str(plugin.resolve_port(%q, 8085))\n", f.Name, projectName+"-dev-api")
		case f.Role == configModeRole && envName == configDevEnvName:
			// The mode/environment knob is the one value a dev env must
			// not inherit from a proto default authored for production —
			// the exact divergence that made a dev bundle announce itself
			// as "production".
			fmt.Fprintf(&b, "    %s = %q\n", f.Name, configDevModeValue)
		case isKCLMandatory(f):
			fmt.Fprintf(&b, "    %s = %s\n", f.Name, kclConfigZeroLiteral(f))
		}
	}
	b.WriteString("}\n")
	return b.String()
}

// kclPluginImport makes forge's port primitives available in config.k.
// Needed because the dev api_url is composed from resolve_port with the
// same key main.k uses — config.k is a separate module and cannot read
// main.k's variables, so the shared KEY is what makes both files agree.
const kclPluginImport = "import kcl_plugin.forge as plugin"

// KCLFrontendConfigName maps a frontend-bound config MESSAGE name to the
// KCL schema and projection lambda it generates: `WebConfig` -> schema
// `WebConfig`, lambda `webConfigRuntime`. The `Runtime` suffix (rather than
// the backend's `EnvMap`) says what the projection produces: the runtime
// config document the bundle reads, not a set of env vars.
func KCLFrontendConfigName(messageName string) (schema, lambda string) {
	if messageName == "" {
		return "FrontendConfig", "frontendConfigRuntime"
	}
	lower := strings.ToLower(messageName[:1]) + messageName[1:]
	return messageName, lower + "Runtime"
}

// GenerateFrontendConfigKCL emits the frontend config module: one typed
// schema + one runtime projection lambda per frontend.
//
// The projection returns a plain dict — the exact JSON object the browser
// receives as window.__FORGE_CONFIG__ — keyed by the field's env_var name
// where it declares one, falling back to the proto field name. Using the
// env_var as the key keeps ONE spelling of each fact across the proto, the
// KCL and the generated TypeScript, so a rename cannot half-land.
//
// Sensitive fields never appear here: ValidateFrontendConfigs refuses them
// before generation reaches this point, which is why (unlike the backend
// schema) there is no ConfigSecretRef in the emitted module.
func GenerateFrontendConfigKCL(configs []FrontendConfig, projectName string) (string, error) {
	var b strings.Builder

	b.WriteString("# Code generated by forge. DO NOT EDIT.\n")
	b.WriteString("# Typed frontend config schemas + runtime projections, projected from\n")
	b.WriteString("# " + configProtoDir(projectName) + "/config.proto — author per-env values in\n")
	b.WriteString("# deploy/kcl/<env>/config.k as a `<Frontend>Config { ... }` instance; add or\n")
	b.WriteString("# change fields in the PROTO, never here.\n")
	b.WriteString("#\n")
	b.WriteString("# EVERYTHING IN THIS FILE IS PUBLIC. These values are served to a browser\n")
	b.WriteString("# as /config.js and are readable by anyone who opens devtools. A field\n")
	b.WriteString("# marked `sensitive` in the proto is refused at generate time rather than\n")
	b.WriteString("# projected here — see ValidateFrontendConfigs.\n\n")

	for i, fc := range configs {
		schemaName, lambdaName := KCLFrontendConfigName(fc.MessageName)

		schema, err := renderFrontendConfigSchema(fc, projectName, schemaName)
		if err != nil {
			return "", fmt.Errorf("render frontend config schema for %s: %w", fc.Frontend, err)
		}
		// A blank line BETWEEN blocks, none after the last: the file must end
		// in exactly one newline, or pre-commit's end-of-file-fixer rewrites
		// it and verify-generated reports the rewrite as drift.
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(schema)
		b.WriteString("\n")
		b.WriteString(renderFrontendRuntimeProjection(fc, schemaName, lambdaName))
	}

	return b.String(), nil
}

// renderFrontendConfigSchema emits one frontend's typed KCL schema. Every
// field is a plain typed value with its proto default — there is no secret
// variant, by construction.
func renderFrontendConfigSchema(fc FrontendConfig, projectName, schemaName string) (string, error) {
	// Same refusal as the backend schema: a repeated field name in one KCL
	// suite is silently last-wins, and a frontend schema is no less exposed
	// to it than AppConfig is.
	if err := CheckDuplicateConfigFields(fc.Fields, FrontendConfigModule, schemaName); err != nil {
		return "", err
	}

	var b strings.Builder

	fmt.Fprintf(&b, "schema %s:\n", schemaName)
	fmt.Fprintf(&b, "    \"\"\"Public runtime config for frontend %q (project %s).\n\n", fc.Frontend, projectName)
	b.WriteString("    Served to the browser — every value here is public.\n")
	b.WriteString("    \"\"\"\n")

	emitted := 0
	var body strings.Builder
	for _, f := range fc.Fields {
		if f.MessageType != "" {
			continue // block references are flattened before this point
		}
		if d := strings.TrimSpace(f.Description); d != "" {
			d = strings.Join(strings.Fields(d), " ")
			fmt.Fprintf(&body, "    # %s\n", d)
		}
		kt := kclTypeForProtoConfig(f)
		// Defaults follow the SAME rules as the backend schema
		// (kclConfigDefaultLiteral): a proto default is emitted literally, a
		// required field with none is left undefaulted so KCL makes it
		// mandatory, and anything else takes its type-zero so an author can
		// instantiate the schema without spelling every field.
		if lit, ok := kclConfigDefaultLiteral(f); ok {
			fmt.Fprintf(&body, "    %s: %s = %s\n", f.Name, kt, lit)
		} else {
			fmt.Fprintf(&body, "    %s: %s\n", f.Name, kt)
		}
		emitted++
	}

	if emitted == 0 {
		b.WriteString("    # No (forge.v1.config)-annotated fields on this frontend's config message.\n")
		return b.String(), nil
	}
	b.WriteString(body.String())
	return b.String(), nil
}

// renderFrontendRuntimeProjection emits the lambda turning a schema
// instance into the runtime config dict the browser receives.
func renderFrontendRuntimeProjection(fc FrontendConfig, schemaName, lambdaName string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Runtime config document for frontend %q. The consumer serves this as\n", fc.Frontend)
	b.WriteString("# JSON inside /config.js, which the bundle reads at boot.\n")
	fmt.Fprintf(&b, "%s = lambda c: %s -> any {\n", lambdaName, schemaName)
	b.WriteString("    {\n")

	for _, f := range fc.Fields {
		if f.MessageType != "" {
			continue
		}
		key := f.EnvVar
		if key == "" {
			key = f.Name
		}
		fmt.Fprintf(&b, "        %q = c.%s\n", key, f.Name)
	}

	b.WriteString("    }\n")
	b.WriteString("}\n")
	return b.String()
}
