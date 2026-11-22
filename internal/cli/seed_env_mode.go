package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// The dev gate (`forge db seed apply|reset`, `forge db reset`, env up's
// auto-seed and dev defaults) asks one question: does this environment run
// its processes in development MODE? The answer is the value the config
// field carrying the MODE takes once the env's config.k is EVALUATED — the
// same value the config projection hands the process — not whatever a text
// match over the file happens to see.
//
// The text match (envModeFromKCLConfig) found only `environment = "..."` at
// the start of a line, so a typed config that nests the field —
// `reliant_config: config_gen.ReliantConfig = { runtime = {environment = "dev"} }` —
// read as "no mode" and the gate refused a real dev env with the misleading
// `environment "dev" is not dev`. It is kept, but only as the fallback for an
// env forge cannot evaluate (no generated config_gen.k, no kcl.mod, no typed
// instance, or an evaluation error).

// envModeReading is what the dev classifier read and concluded. Source says
// where the verdict came from, in words a refusal can print.
type envModeReading struct {
	Dev    bool
	Source string
}

// envModeFinding is one config field that carries the runtime MODE in an
// evaluated config instance: its KCL path and evaluated value.
type envModeFinding struct {
	Path  string
	Value string
}

// errEnvConfigNotEvaluable marks an env whose config forge cannot evaluate
// at all (as opposed to an evaluation that ran and failed). Both fall back
// to the text match; this one says why without a KCL error in it.
var errEnvConfigNotEvaluable = errors.New("not evaluable")

// classifySeedEnv is the FAIL-CLOSED dev classifier: Dev is true only when
// forge positively read a development MODE for env. The evaluated config is
// authoritative whenever it is available; the text match is consulted only
// when it is not.
func classifySeedEnv(projectDir, env string) envModeReading {
	// Seed and reset must classify the declared environment, not a path
	// that resolves to a different directory or aliases another env.
	if err := checkLifecycleEnvName(env); err != nil {
		return envModeReading{Source: err.Error()}
	}
	rel := filepath.ToSlash(filepath.Join("deploy", "kcl", env, "config.k"))
	if _, err := os.Stat(filepath.Join(projectDir, rel)); err != nil {
		return envModeReading{Source: rel + " does not exist, so nothing declares this environment's runtime MODE"}
	}

	instances, err := evaluateEnvConfigInstances(projectDir, env)
	if err != nil {
		why := "it could not be evaluated: " + err.Error()
		if errors.Is(err, errEnvConfigNotEvaluable) {
			why = strings.TrimPrefix(err.Error(), errEnvConfigNotEvaluable.Error()+": ")
		}
		mode, ok := envModeFromKCLConfig(projectDir, env)
		if !ok {
			return envModeReading{Source: fmt.Sprintf("%s has no line starting `environment = \"...\"` (read as text, because %s)", rel, why)}
		}
		return envModeReading{
			Dev:    isDevMode(mode),
			Source: fmt.Sprintf("%s sets environment = %q (read as text, because %s)", rel, mode, why),
		}
	}

	findings := envModeFindings(instances, modeRolePaths(projectDir))
	return envModeVerdict(rel, findings)
}

// envModeVerdict turns the MODE findings of one env into a verdict. Dev
// needs at least one finding and EVERY finding to name development: two
// configs disagreeing about the mode is not something to resolve in the
// permissive direction.
func envModeVerdict(rel string, findings []envModeFinding) envModeReading {
	if len(findings) == 0 {
		return envModeReading{Source: fmt.Sprintf("%s (evaluated) sets no runtime MODE: no field annotated role: CONFIG_FIELD_ROLE_MODE, and no field named `environment` with a value", rel)}
	}
	dev := true
	parts := make([]string, 0, len(findings))
	for _, f := range findings {
		if !isDevMode(f.Value) {
			dev = false
		}
		parts = append(parts, fmt.Sprintf("%s = %q", f.Path, f.Value))
	}
	return envModeReading{Dev: dev, Source: fmt.Sprintf("%s (evaluated) sets %s", rel, strings.Join(parts, ", "))}
}

// envModeFindings locates the MODE in evaluated config instances (instance
// variable -> its evaluated attributes).
//
// By ANNOTATION first: rolePaths are the KCL paths of the fields tagged
// role=CONFIG_FIELD_ROLE_MODE (modeRolePaths), the field config.Mode() reads
// at runtime. Only when no instance holds one of those does the name
// convention apply: a string field named `environment` on an instance or on
// one of its composed blocks — exactly what the text match looked for, now
// read structurally, so nesting no longer hides it. By name, an EMPTY value is
// skipped: an unannotated field called `environment` that nothing set is no
// evidence of a mode (reliant's observability.environment, an OTel label,
// sits beside its runtime.environment), and the text match never saw it
// either. Unset still refuses: with no non-empty finding there is no dev
// reading. An annotated MODE field is authoritative, so its empty value
// counts — unset is production to config.Mode().
func envModeFindings(instances map[string]map[string]any, rolePaths []string) []envModeFinding {
	names := make([]string, 0, len(instances))
	for n := range instances {
		names = append(names, n)
	}
	sort.Strings(names)

	var byRole []envModeFinding
	for _, inst := range names {
		for _, p := range rolePaths {
			if v, ok := lookupString(instances[inst], p); ok {
				byRole = append(byRole, envModeFinding{Path: inst + "." + p, Value: v})
			}
		}
	}
	if len(byRole) > 0 {
		return byRole
	}

	var byName []envModeFinding
	for _, inst := range names {
		attrs := instances[inst]
		if v, ok := attrs["environment"].(string); ok && v != "" {
			byName = append(byName, envModeFinding{Path: inst + ".environment", Value: v})
		}
		blocks := make([]string, 0, len(attrs))
		for k := range attrs {
			blocks = append(blocks, k)
		}
		sort.Strings(blocks)
		for _, b := range blocks {
			block, ok := attrs[b].(map[string]any)
			if !ok {
				continue
			}
			if v, ok := block["environment"].(string); ok && v != "" {
				byName = append(byName, envModeFinding{Path: inst + "." + b + ".environment", Value: v})
			}
		}
	}
	return byName
}

// lookupString reads a dotted path of string keys from evaluated attributes.
func lookupString(attrs map[string]any, path string) (string, bool) {
	var cur any = attrs
	for _, key := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		if cur, ok = m[key]; !ok {
			return "", false
		}
	}
	s, ok := cur.(string)
	return s, ok
}

// modeRolePaths returns the KCL paths, relative to a config instance, of
// every string field tagged role=CONFIG_FIELD_ROLE_MODE: `environment` for a
// root field, `runtime.environment` for the leaf of a composed block (config
// codegen nests one level). It reads the project's forge descriptor and
// never rebuilds it; an absent or stale descriptor yields none, and the
// caller falls back to the name convention.
func modeRolePaths(projectDir string) []string {
	desc, err := codegen.ReadDescriptor(projectDir)
	if err != nil || desc == nil || codegen.DescriptorDrift(projectDir, desc.Sources) != "" {
		return nil
	}
	return modeRolePathsFrom(desc.Configs)
}

// modeRolePathsFrom is modeRolePaths over already-read config messages.
func modeRolePathsFrom(configs []codegen.ConfigMessage) []string {
	modeFields := map[string][]string{} // message name -> its MODE field names
	for _, m := range configs {
		for _, f := range m.Fields {
			if f.Role == "CONFIG_FIELD_ROLE_MODE" && f.ProtoType == "string" {
				modeFields[m.Name] = append(modeFields[m.Name], f.Name)
			}
		}
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, m := range configs {
		for _, name := range modeFields[m.Name] {
			add(name)
		}
		for _, f := range m.Fields {
			if f.ProtoType != "message" {
				continue
			}
			for _, name := range modeFields[f.MessageType] {
				add(f.Name + "." + name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// envConfigEvalCache memoizes evaluateEnvConfigInstances per process. `forge
// env up` classifies its env once per workload and job; the key includes the
// bytes of config.k and config_gen.k, so an edit to either is re-evaluated.
var envConfigEvalCache = struct {
	sync.Mutex
	m map[string]map[string]map[string]any
}{m: map[string]map[string]map[string]any{}}

// evaluateEnvConfigInstances evaluates deploy/kcl/<env>/config.k and returns
// each typed config instance it declares (`<var>: config_gen.<Schema> = {…}`)
// as its evaluated attributes, schema defaults included.
//
// It uses the same throwaway-probe mechanism as loadProjectConfigEnvMap, so
// only config.k's own import graph is pulled, never the env's main.k. It
// refuses (errEnvConfigNotEvaluable) before starting KCL when the project
// has no generated config schema or no kcl.mod: there is nothing typed to
// evaluate, and the caller's text fallback is the honest answer.
func evaluateEnvConfigInstances(projectDir, env string) (map[string]map[string]any, error) {
	if abs, err := filepath.Abs(projectDir); err == nil {
		projectDir = abs
	}
	kclDir := filepath.Join(projectDir, "deploy", "kcl")
	envDir := filepath.Join(kclDir, env)
	src, err := os.ReadFile(filepath.Join(envDir, "config.k"))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errEnvConfigNotEvaluable, err)
	}
	schemaSrc, err := os.ReadFile(filepath.Join(kclDir, codegen.ConfigSchemaModule+".k"))
	if err != nil {
		return nil, fmt.Errorf("%w: deploy/kcl/%s.k (the generated config schema) does not exist", errEnvConfigNotEvaluable, codegen.ConfigSchemaModule)
	}
	if _, err := os.Stat(filepath.Join(kclDir, "kcl.mod")); err != nil {
		return nil, fmt.Errorf("%w: deploy/kcl/kcl.mod does not exist", errEnvConfigNotEvaluable)
	}
	instances := configKInstances(string(src))
	if len(instances) == 0 {
		return nil, fmt.Errorf("%w: it declares no typed `<name>: %s.<Schema>` config instance", errEnvConfigNotEvaluable, codegen.ConfigSchemaModule)
	}

	sum := sha256.New()
	for _, part := range [][]byte{[]byte(projectDir), []byte(env), src, schemaSrc} {
		sum.Write(part)
		sum.Write([]byte{0})
	}
	key := hex.EncodeToString(sum.Sum(nil))
	envConfigEvalCache.Lock()
	cached, ok := envConfigEvalCache.m[key]
	envConfigEvalCache.Unlock()
	if ok {
		return cached, nil
	}

	var b strings.Builder
	b.WriteString("import " + envProbeConfigImport + " as appcfg\n\n")
	b.WriteString("forge_config_instances = {\n")
	for _, inst := range instances {
		fmt.Fprintf(&b, "    %q = appcfg.%s\n", inst.varName, inst.varName)
	}
	b.WriteString("}\n")

	probePath, cleanup, err := writeEnvProbe(envDir, "mode_probe", b.String())
	if err != nil {
		return nil, fmt.Errorf("write config probe: %w", err)
	}
	defer cleanup()
	out, err := kclrender.Run(projectDir, probePath, []string{"env=" + env})
	if err != nil {
		return nil, err
	}
	var doc struct {
		Instances map[string]map[string]any `json:"forge_config_instances"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("parse evaluated config: %w", err)
	}
	if doc.Instances == nil {
		doc.Instances = map[string]map[string]any{}
	}
	envConfigEvalCache.Lock()
	envConfigEvalCache.m[key] = doc.Instances
	envConfigEvalCache.Unlock()
	return doc.Instances, nil
}
