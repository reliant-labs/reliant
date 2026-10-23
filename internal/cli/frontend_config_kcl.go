package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// loadFrontendRuntimeConfig renders each frontend's runtime config
// document for ONE environment: the exact JSON object the browser receives
// as window.__FORGE_CONFIG__, keyed by frontend name.
//
// It is the frontend twin of loadProjectConfigEnvMap and works the same
// way, deliberately. Both answer "what does THIS environment declare?" by
// evaluating the environment's own config.k through the generated
// projection lambda — the backend's appConfigEnvMap, the frontend's
// <name>Runtime — rather than re-deriving values Go-side. One evaluator,
// one set of defaults, one set of KCL type rules: a value forge writes into
// config.js is by construction the value a deploy would project.
//
// The probe is a throwaway single-file module written into the env dir and
// removed on return, so only its import graph (frontend_config_gen +
// config.k) is pulled — the env's main.k, and every component it imports,
// is not needed. That matters at GENERATE time, where main.k may reference
// workloads that do not exist yet.
//
// A missing env dir, a config.k that declares no frontend instance, or a
// render failure all return an empty map with no error: the caller falls
// back to proto defaults, which is the correct answer for a project that
// has not authored per-env frontend values yet.
func loadFrontendRuntimeConfig(projectDir, env string, configs []codegen.FrontendConfig) (map[string]map[string]any, error) {
	if env == "" || len(configs) == 0 {
		return nil, nil
	}
	// The probe path is handed to a renderer that resolves it against
	// projectDir, so a RELATIVE projectDir is joined twice — `forge project
	// new demo-app` run from the parent dir asked KCL for
	// demo-app/demo-app/deploy/kcl/dev/… and fell back to proto defaults.
	// Anchor it once, here.
	if abs, err := filepath.Abs(projectDir); err == nil {
		projectDir = abs
	}
	envDir := filepath.Join(projectDir, "deploy", "kcl", env)
	if _, err := os.Stat(filepath.Join(envDir, "config.k")); err != nil {
		return nil, nil
	}
	// The projection lambdas live in the generated module; without it
	// there is nothing to project through.
	if _, err := os.Stat(filepath.Join(projectDir, "deploy", "kcl", codegen.FrontendConfigModule+".k")); err != nil {
		return nil, nil
	}

	src, err := os.ReadFile(filepath.Join(envDir, "config.k"))
	if err != nil {
		return nil, fmt.Errorf("read %s/config.k: %w", env, err)
	}
	probe, instances := frontendConfigProbeSource(string(src), configs)
	if len(instances) == 0 {
		return nil, nil
	}

	probePath, cleanup, err := writeEnvProbe(envDir, "frontend_config_probe", probe)
	if err != nil {
		return nil, fmt.Errorf("write frontend config probe: %w", err)
	}
	defer cleanup()

	out, err := kclrender.Run(projectDir, probePath, []string{"env=" + env})
	if err != nil {
		return nil, fmt.Errorf("render frontend config projection for %s: %w", env, err)
	}
	var doc struct {
		Frontends map[string]map[string]any `json:"forge_frontend_config"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("parse frontend config projection: %w", err)
	}
	return doc.Frontends, nil
}

// renderFrontendRuntimeDocs renders one environment's frontend runtime
// config DOCUMENTS — the config.js bodies, keyed by frontend name — from
// that environment's KCL.
//
// This is the deploy-time consumer of the KCL runtime projection, and the
// reason runtime injection was chosen over build-time inlining in the first
// place. A bundle with NEXT_PUBLIC_* / VITE_* inlined is frozen to the
// environment it was built against, which makes `forge env deploy` a lie;
// a bundle that reads window.__FORGE_CONFIG__ is environment-agnostic, and
// this function produces the one file that differs between environments.
//
// The values come from the same probe the generate path uses, so the
// document a developer runs against and the document an environment ships
// are produced by one evaluator against one set of declarations. Proto
// defaults fill any field the environment does not pin — identical
// layering to the dev document, so a promoted bundle can never see a
// DIFFERENT set of fields than the one it was developed against.
//
// An environment that declares no frontend config yields an empty map and
// the deploy is unchanged.
func renderFrontendRuntimeDocs(projectDir, envName string) (map[string]string, error) {
	return renderFrontendRuntimeDocsWith(projectDir, envName, nil)
}

// frontendRuntimeOverlays collects each frontend's RESOLVED runtime_config
// (kcl/schema.k Frontend.runtime_config, forge.WorkloadURL references already
// lowered to URLs by the render) keyed by frontend name. Empty on a hosted
// env: the render leaves references unresolved there, and the control plane
// writes the document.
func frontendRuntimeOverlays(entities *KCLEntities) map[string]map[string]string {
	if entities == nil {
		return nil
	}
	out := map[string]map[string]string{}
	for _, f := range entities.Frontends {
		if len(f.RuntimeConfig) > 0 {
			out[f.Name] = f.RuntimeConfig
		}
	}
	return out
}

// renderFrontendRuntimeDocsWith is renderFrontendRuntimeDocs with each
// frontend's resolved runtime_config layered OVER the typed per-env values,
// key by key. The typed layer (config.k through the generated projection)
// stays the source of every field the proto declares; runtime_config is how
// an env says "this value is another workload's URL" without restating a
// hostname. A key both declare takes runtime_config's value — it is the more
// specific, render-resolved statement.
//
// A frontend with runtime_config but NO typed config message still gets a
// document holding just those keys. Rendering nothing would make the
// declaration a silent no-op, and the bundle would read undefined.
func renderFrontendRuntimeDocsWith(projectDir, envName string, overlays map[string]map[string]string) (map[string]string, error) {
	out := map[string]string{}
	typed, err := renderTypedFrontendRuntimeValues(projectDir, envName)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for name := range typed {
		names[name] = true
	}
	for name := range overlays {
		names[name] = true
	}
	for name := range names {
		merged := map[string]any{}
		for k, v := range typed[name] {
			merged[k] = v
		}
		for k, v := range overlays[name] {
			merged[k] = v
		}
		encoded, err := json.MarshalIndent(merged, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("encode runtime config for %s: %w", name, err)
		}
		out[name] = codegen.GenerateFrontendConfigJS(name, envName, string(encoded))
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// renderTypedFrontendRuntimeValues is the TYPED half of a runtime document:
// the env's config.k values through the generated projection, with proto
// defaults filling every field the env does not pin.
func renderTypedFrontendRuntimeValues(projectDir, envName string) (map[string]map[string]any, error) {
	messages, err := codegen.ParseConfigProtosFromDir(filepath.Join(projectDir, "proto", "config"))
	if err != nil {
		// A project with no readable config protos has no frontend config
		// to project. That is not a deploy-blocking condition — the
		// sensitive-field guard that DOES block runs at generate time.
		return nil, nil
	}
	configs := codegen.FrontendConfigsFromMessages(messages)
	if len(configs) == 0 {
		return nil, nil
	}

	// The refusal is re-asserted here rather than trusted from generate
	// time: this is the last moment before a value is written into a
	// shipped artifact, and a published secret cannot be withdrawn.
	if err := codegen.ValidateFrontendConfigs(messages); err != nil {
		return nil, err
	}

	values, err := loadFrontendRuntimeConfig(projectDir, envName, configs)
	if err != nil {
		return nil, fmt.Errorf("frontend runtime config for %s: %w", envName, err)
	}

	out := make(map[string]map[string]any, len(configs))
	for _, fc := range configs {
		out[fc.Frontend] = withRuntimeEnvironment(frontendRuntimeValues(fc, values[fc.Frontend]), envName)
	}
	return out, nil
}

// frontendConfigProbeSource builds the throwaway KCL module that renders
// every frontend instance an env's config.k declares.
//
// Like configProbeSource, the shape is read off the env's own config.k:
// the `<var>: <alias>.<Schema>` bindings it declares ARE the frontends this
// env has values for. Each is matched back to its FrontendConfig by SCHEMA
// name (the proto message name), which is what ties a KCL instance to the
// frontend it configures — the variable name is the author's choice and
// carries no meaning.
//
// The probe imports the generated module under its own name rather than
// reusing the env file's alias, so an env that aliases it (a common style)
// still resolves.
func frontendConfigProbeSource(configK string, configs []codegen.FrontendConfig) (string, []frontendConfigInstance) {
	bySchema := make(map[string]codegen.FrontendConfig, len(configs))
	for _, fc := range configs {
		schema, _ := codegen.KCLFrontendConfigName(fc.MessageName)
		bySchema[schema] = fc
	}

	instances := frontendConfigKInstances(configK, bySchema)
	if len(instances) == 0 {
		return "", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "import %s as forge_fcfg\n", codegen.FrontendConfigModule)
	b.WriteString("import " + envProbeConfigImport + " as forge_envcfg\n\n")
	b.WriteString("forge_frontend_config = {\n")
	for _, inst := range instances {
		_, lambda := codegen.KCLFrontendConfigName(inst.messageName)
		fmt.Fprintf(&b, "    %q = forge_fcfg.%s(forge_envcfg.%s)\n", inst.frontend, lambda, inst.varName)
	}
	b.WriteString("}\n")
	return b.String(), instances
}

// frontendConfigInstance is one `<var>: <module>.<Schema> = {` binding in
// an env's config.k that resolves to a known frontend config.
type frontendConfigInstance struct {
	varName     string
	messageName string
	frontend    string
}

// frontendConfigKInstances finds the frontend config instances an env's
// config.k declares.
//
// It matches on the SCHEMA name rather than a module prefix because the
// import may be aliased (`import frontend_config_gen as fcfg`), and because
// the schema set is known: only a binding whose schema is one this project
// actually generates is a frontend config. That also keeps a backend
// `app_config: config_gen.AppConfig` binding — which shares the line shape
// — from being mistaken for one.
func frontendConfigKInstances(src string, bySchema map[string]codegen.FrontendConfig) []frontendConfigInstance {
	var out []frontendConfigInstance
	seen := map[string]bool{}
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		// Keep only the type identifier: `fcfg.WebConfig = {` -> `WebConfig`.
		rest = strings.TrimSpace(rest)
		typeRef := strings.TrimSpace(strings.Split(strings.Split(rest, "=")[0], " ")[0])
		dot := strings.LastIndex(typeRef, ".")
		if dot < 0 {
			continue // unqualified — not a reference into the generated module
		}
		schema := typeRef[dot+1:]
		fc, known := bySchema[schema]
		if name == "" || !known || seen[fc.Frontend] {
			continue
		}
		seen[fc.Frontend] = true
		out = append(out, frontendConfigInstance{
			varName:     name,
			messageName: fc.MessageName,
			frontend:    fc.Frontend,
		})
	}
	return out
}
