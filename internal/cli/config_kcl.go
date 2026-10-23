package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// kclEnvSource is one entry of the config projection
// (config_gen.appConfigEnvMap: `{str: str | forge.SecretRef}`): a plain
// value, rendered as a bare JSON string, or a Secret reference, rendered as
// the forge.SecretRef object. Exactly one of Value / Secret is set. (Value is
// a pointer so an explicit empty-string value is distinguishable from a
// secret-backed entry.)
type kclEnvSource struct {
	Value  *string
	Secret *kclEnvSecretRef
}

// kclEnvSecretRef is forge.SecretRef as the projection emits it: the k8s
// Secret key a cluster workload reads, plus the store name it is rendered
// from (store_key, set by config codegen).
type kclEnvSecretRef struct {
	Name     string `json:"name"`
	Key      string `json:"key"`
	StoreKey string `json:"store_key,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// UnmarshalJSON accepts the projection's union: a string is a plain value, an
// object is a forge.SecretRef. Anything else is refused rather than read as an
// absent value — an unknown shape silently dropping a config var is the drift
// this one projection exists to prevent.
func (s *kclEnvSource) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		s.Value, s.Secret = &str, nil
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var ref kclEnvSecretRef
	if err := dec.Decode(&ref); err != nil {
		return fmt.Errorf("config env entry is neither a string nor a forge.SecretRef {name, key}: %s: %w", b, err)
	}
	if ref.Name == "" {
		return fmt.Errorf("config env entry is a forge.SecretRef with no name: %s", b)
	}
	s.Value, s.Secret = nil, &ref
	return nil
}

// loadProjectConfigEnvMap renders config_gen.appConfigEnvMap(app_config)
// from deploy/kcl/<env>/config.k — the SAME projection cluster mode renders
// into every workload's env — and returns the ENV_VAR-keyed source map. Reading
// this one source keeps host-run injection, the parity report, and the seed
// gate from drifting off what a cluster deploy would inject.
//
// It evaluates a throwaway probe module (written under the env dir by writeEnvProbe, removed on
// return) as a SINGLE file, so only the probe's import graph —
// config_gen + config.k + the forge module — is pulled;
// the env's main.k (and the components it imports) is not needed. Any
// failure is returned to the caller, which treats config as absent (non-fatal),
// matching how a missing per-env config always behaved.
func loadProjectConfigEnvMap(projectDir, env string) (map[string]kclEnvSource, error) {
	if env == "" {
		return nil, fmt.Errorf("config env: env required")
	}
	envDir := filepath.Join(projectDir, "deploy", "kcl", env)
	if _, err := os.Stat(filepath.Join(envDir, "config.k")); err != nil {
		return nil, err
	}

	// The probe outputs the projected env map under a public top-level so KCL
	// emits it. `import config_gen` resolves from the deploy/kcl package
	// root; `import ..config` (the probe sits one directory down) is the env's own AppConfig instance.
	//
	// With PER-BINARY configs there is no single app_config to project, so
	// the probe merges every binary's env map instead. That is the honest
	// host-mode answer: `forge env up` starts every binary on one host, so the
	// host env is the union of what those processes read — while each
	// binary's CLUSTER Deployment still carries only its own vars, which is
	// where the isolation matters. A name set by two binaries resolves to
	// one host value (map-merge, last wins); they are separate values only
	// once they are separate processes with separate environments.
	probe, perr := configProbeSource(projectDir, envDir)
	if perr != nil {
		return nil, perr
	}
	probePath, cleanup, err := writeEnvProbe(envDir, "config_probe", probe)
	if err != nil {
		return nil, fmt.Errorf("write config probe: %w", err)
	}
	defer cleanup()

	out, err := kclrender.Run(projectDir, probePath, []string{"env=" + env})
	if err != nil {
		return nil, fmt.Errorf("render config projection for %s: %w", env, err)
	}
	var doc struct {
		Env map[string]kclEnvSource `json:"forge_config_env"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("parse config projection: %w", err)
	}
	return doc.Env, nil
}

// configProbeSource builds the throwaway KCL module that renders an env's
// config projection, shaped to whatever the project declares.
//
// The shape is read off the env's own config.k — the instances it declares
// ARE the configs this env has values for. A `<name>: config_gen.<Schema>`
// binding names both the variable to project and the schema whose lambda
// projects it, so the probe needs no second source of truth. A project with
// the classic single `app_config: config_gen.AppConfig` gets exactly the
// probe it always did.
func configProbeSource(projectDir, envDir string) (string, error) {
	src, err := os.ReadFile(filepath.Join(envDir, "config.k"))
	if err != nil {
		return "", fmt.Errorf("read config.k: %w", err)
	}

	instances := configKInstances(string(src))
	if len(instances) == 0 {
		return "", fmt.Errorf("no config instances found in %s/config.k", envDir)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "import %s\n", codegen.ConfigSchemaModule)
	b.WriteString("import " + envProbeConfigImport + " as appcfg\n\n")
	b.WriteString("forge_config_env = ")
	for i, inst := range instances {
		_, lambda := codegen.KCLConfigName(inst.schema)
		if i > 0 {
			b.WriteString(" | ")
		}
		// `config_secrets` = ALL_SENSITIVE, the generated list of every sensitive
		// var this config declares — the one caller that legitimately wants
		// the whole set.
		//
		// A CLUSTER workload names only the credentials it reads, so its
		// manifest carries only those secretKeyRefs. This probe is not a
		// workload: it answers "what does the config resolve to in this env"
		// for host-run injection, the parity report and the seed gate. Host
		// mode runs every binary as a process on one machine off one
		// environment, so the honest host answer is the union — and the
		// parity report specifically exists to compare against the cluster,
		// which it cannot do for a var it declined to look at. Nothing is
		// leaked by asking: a forge.SecretRef entry surfaces here as a
		// REFERENCE (name+key), never a value, and buildForgeConfigValues records it as
		// a placeholder precisely because the host cannot dereference it
		// either.
		fmt.Fprintf(&b, "%s.%s(appcfg.%s, %s.%s)", codegen.ConfigSchemaModule, lambda, inst.varName, codegen.ConfigSchemaModule, codegen.KCLAllSensitiveName(inst.schema))
	}
	b.WriteString("\n")
	return b.String(), nil
}

// configInstance is one `<var>: config_gen.<Schema> = {` binding in an env's
// config.k: the variable holding the values and the schema naming its
// projection lambda.
type configInstance struct {
	varName string
	schema  string
}

// configKInstances finds the typed config instances an env's config.k
// declares. It matches the binding form the scaffolder emits (and that a
// hand-edited file keeps), ignoring comments and any other statement.
func configKInstances(src string) []configInstance {
	prefix := codegen.ConfigSchemaModule + "."
	var out []configInstance
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
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, prefix) {
			continue
		}
		schema := strings.TrimPrefix(rest, prefix)
		// Keep only the schema identifier: `AdminConfig = {` -> `AdminConfig`.
		schema = strings.TrimSpace(strings.Split(strings.Split(schema, "=")[0], " ")[0])
		if name == "" || schema == "" {
			continue
		}
		out = append(out, configInstance{varName: name, schema: schema})
	}
	return out
}
