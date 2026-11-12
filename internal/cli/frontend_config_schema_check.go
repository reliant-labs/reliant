package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
)

// checkFrontendRuntimeConfigKeys refuses a frontend `runtime_config` key that
// the frontend's own config schema does not declare.
//
// # Why
//
// runtime_config is a free-form `{str: str | WorkloadURL}`: KCL can check a
// key is a JavaScript identifier, but not that the bundle READS it. The
// typed fields live in the frontend's (forge.v1.frontend_config) message,
// and when the frontend's code is in another repository — a sibling checkout
// in dev, a source pin when building — the project declaring its
// environments cannot see that proto. So `APII_URL = …` rendered into
// config.js, config_gen.ts never looked at it, and the field sat on its
// default with nothing on screen saying why.
//
// The frontend's repo now writes the schema beside config_gen.ts
// (codegen.FrontendConfigSchemaFile), and this reads it from the frontend's
// directory. The same check covers an in-tree frontend, where the schema is
// this project's own output.
//
// # What is allowed
//
// The schema's keys, plus this forge's own codegen.FrontendRuntimeKeys — the
// keys forge's generated frontend code reads and KCL's render derives (the
// telemetry knobs). Allowing this forge's set as well as the schema's keeps
// a frontend repo on an OLDER forge from being refused a runtime key a newer
// declaring forge adds by itself.
//
// # When there is nothing to check
//
// A frontend with no directory yet (a source pin not materialized; checked
// again by resolveFrontendEntitySources once it is), no schema file (no
// config message, or a frontend repo whose forge predates the schema), or a
// schema of a newer format is skipped: the check refuses only what it can
// prove is wrong.
func checkFrontendRuntimeConfigKeys(projectDir string, frontends []FrontendEntity) error {
	var problems []string
	for _, fe := range frontends {
		if p := frontendRuntimeConfigKeyProblem(projectDir, fe); p != "" {
			problems = append(problems, p)
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(problems, "\n\n"))
}

// frontendRuntimeConfigKeyProblem is one frontend's refusal, or "".
func frontendRuntimeConfigKeyProblem(projectDir string, fe FrontendEntity) string {
	keys := map[string]bool{}
	for k := range fe.RuntimeConfig {
		keys[k] = true
	}
	for k := range fe.RuntimeConfigSpec {
		keys[k] = true
	}
	// No early return on an empty runtime_config: a schema that REQUIRES a
	// key is violated by a frontend that sets none.
	if strings.TrimSpace(fe.Path) == "" {
		return ""
	}
	dir := fe.Path
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(projectDir, dir)
	}
	schemaPath := filepath.Join(dir, filepath.FromSlash(codegen.FrontendConfigSchemaFile))
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		return "" // no schema to check against (see the doc comment)
	}
	schema, ok, err := codegen.ParseFrontendConfigSchema(data)
	if err != nil {
		return fmt.Sprintf("Frontend '%s'.runtime_config: cannot read its config schema %s: %v — "+
			"run `forge generate` in the frontend's project to rewrite it", fe.Name, displayPath(projectDir, schemaPath), err)
	}
	if !ok {
		return ""
	}

	allowed := map[string]bool{}
	for _, k := range schema.Keys {
		allowed[k.Name] = true
	}
	for _, k := range codegen.FrontendRuntimeKeys {
		allowed[k] = true
	}
	vocabulary := make([]string, 0, len(allowed))
	for k := range allowed {
		vocabulary = append(vocabulary, k)
	}
	sort.Strings(vocabulary)

	var unknown []string
	for k := range keys {
		if !allowed[k] {
			unknown = append(unknown, k)
		}
	}
	missing := frontendRuntimeConfigMissing(projectDir, fe, schema)
	if len(unknown) == 0 && len(missing) == 0 {
		return ""
	}
	if len(unknown) == 0 {
		return formatMissingRuntimeConfig(projectDir, fe, schemaPath, schema, missing)
	}
	sort.Strings(unknown)

	var b strings.Builder
	fmt.Fprintf(&b, "Frontend '%s'.runtime_config sets %s, which this frontend does not read: "+
		"its config schema (%s, generated from the %s message) declares no such key, so the value "+
		"would be rendered into config.js and ignored, leaving the field on its default.",
		fe.Name, quoteList(unknown), displayPath(projectDir, schemaPath), schemaMessageName(schema))
	for _, k := range unknown {
		if s := codegen.ClosestFrom(k, vocabulary); s != "" {
			fmt.Fprintf(&b, "\n  %s: did you mean %s?", k, s)
		}
	}
	fmt.Fprintf(&b, "\n  keys it reads: %s", strings.Join(vocabulary, ", "))
	b.WriteString("\n  Fix the key in this environment's KCL, or — if the frontend should read it — add the field to " +
		"its (forge.v1.frontend_config) message and run `forge generate` in the frontend's project.")
	if len(missing) > 0 {
		b.WriteString("\n\n")
		b.WriteString(formatMissingRuntimeConfig(projectDir, fe, schemaPath, schema, missing))
	}
	return b.String()
}

// frontendRuntimeConfigMissing is every key fe's schema requires that its
// runtime_config does not carry — each mode selector it leaves unstated
// (codegen.FrontendConfigSchema.UnstatedSelectors), then each key required in
// the mode it states (MissingRequired) —
// for a FOREIGN frontend only: one whose code — and so its config message —
// is outside this project (a source pin, or a path out of the project root).
// That document is runtime_config alone, so a missing key is missing. An
// in-tree frontend's document also carries its typed config.k values, which
// its generated KCL schema already checks.
func frontendRuntimeConfigMissing(projectDir string, fe FrontendEntity, schema codegen.FrontendConfigSchema) []codegen.MissingKey {
	if !frontendIsForeign(projectDir, fe) {
		return nil
	}
	present := func(key string) bool {
		if v, ok := fe.RuntimeConfigSpec[key]; ok {
			return v.Value == nil || strings.TrimSpace(*v.Value) != ""
		}
		v, ok := fe.RuntimeConfig[key]
		return ok && strings.TrimSpace(v) != ""
	}
	literal := func(key string) (string, bool) {
		if v, ok := fe.RuntimeConfigSpec[key]; ok {
			if v.Value == nil {
				return "", false // another workload's URL: known at deploy time only
			}
			return strings.TrimSpace(*v.Value), true
		}
		v, ok := fe.RuntimeConfig[key]
		return strings.TrimSpace(v), ok
	}
	// The document must name its mode before the mode's keys can be judged:
	// an unstated selector satisfies every condition vacuously.
	return append(schema.UnstatedSelectors(present), schema.MissingRequired(present, literal)...)
}

// frontendIsForeign reports whether fe's code is outside this project: a
// source pin, or a path that leaves the project root.
func frontendIsForeign(projectDir string, fe FrontendEntity) bool {
	if fe.Source != nil && fe.Source.Repo != "" {
		return true
	}
	dir := fe.Path
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(projectDir, dir)
	}
	rel, err := filepath.Rel(projectDir, dir)
	return err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// formatMissingRuntimeConfig is the refusal for keys the schema requires.
func formatMissingRuntimeConfig(projectDir string, fe FrontendEntity, schemaPath string, schema codegen.FrontendConfigSchema, missing []codegen.MissingKey) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Frontend '%s'.runtime_config is missing %s, which its config schema (%s, generated from the %s message) requires: "+
		"the bundle would refuse to boot, or boot in a mode this environment does not mean.",
		fe.Name, fmt.Sprintf("%d %s", len(missing), plural(len(missing), "key", "keys")), displayPath(projectDir, schemaPath), schemaMessageName(schema))
	for _, m := range missing {
		fmt.Fprintf(&b, "\n  %s", m.Reason())
	}
	b.WriteString("\n  Set each in this environment's runtime_config; a mode-dependent key needs a value other than the field's default, " +
		"and a mode selector needs a value even for the default mode (a frontend whose default mode has no spelling gives it one in the field's allowed_values).")
	return b.String()
}

func schemaMessageName(s codegen.FrontendConfigSchema) string {
	if s.Message == "" {
		return "frontend config"
	}
	return s.Message
}

func quoteList(keys []string) string {
	q := make([]string, len(keys))
	for i, k := range keys {
		q[i] = "`" + k + "`"
	}
	return strings.Join(q, ", ")
}

// displayPath is path relative to projectDir when that is shorter to read
// (`../reliant/web/src/lib/config_schema_gen.json`), else as given.
func displayPath(projectDir, path string) string {
	if rel, err := filepath.Rel(projectDir, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return path
}
