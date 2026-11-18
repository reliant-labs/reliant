// File: internal/cli/lint/lint_typed_access_guard.go
//
// What the typed-config guardrail lane checks before it runs forbidigo, and
// what it counts as a finding afterwards.
//
// ── Before: is forbidigo configured to be the guardrail? ──────────────────
//
// The lane is forbidigo run on its own (--enable-only=forbidigo) with
// whatever the project's golangci-lint config tells forbidigo to forbid.
// forge scaffolds that config with the env-read patterns (os.Getenv,
// os.LookupEnv, os.Environ), but .golangci.yml is the project's file: a
// project that adopted forge with its own config, or rewrote the scaffolded
// one, can have no forbidigo settings at all. forbidigo then falls back to
// its built-in default, which forbids fmt.Print* and print/println. The lane
// reported every fmt.Println in such a project as a "typed-config guardrail"
// warning and never looked at an os.Getenv. Measured on reliant: twenty
// fmt.Print findings in cmd/reliant/commands and none of its env reads.
//
// So the lane reads the config golangci-lint will load and confirms that a
// forbid pattern covers os.Getenv. When none does, forbidigo does not run:
// the lane reports that it could not (the -unavailable rule), naming the file
// and the patterns to add.
//
// ── After: what is a finding? ─────────────────────────────────────────────
//
// golangci-lint ends every run with a summary ("0 issues." on a clean one;
// "N issues:" and a per-linter count otherwise) and prints a source snippet
// under each issue. Every non-blank line used to become a warning, so a
// clean run reported "typed-config guardrail — 1 warning" over the single
// line "0 issues.". A finding is now a file-anchored issue and nothing else.

package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/checksums"
)

// typedAccessGuardEnvRead is the identifier a forbid pattern must match for
// forbidigo to be acting as the typed-config guardrail. os.Getenv is the read
// every project has; a config that covers it was written for this purpose.
const typedAccessGuardEnvRead = "os.Getenv"

// typedAccessGuardUnconfiguredHint is the remediation for a golangci-lint
// config that does not tell forbidigo to forbid env reads.
const typedAccessGuardUnconfiguredHint = "add forge's env-read patterns to forbidigo in the golangci-lint config — " +
	`linters.settings.forbidigo.forbid: [{pattern: 'os\.Getenv'}, {pattern: 'os\.LookupEnv'}, {pattern: 'os\.Environ'}] ` +
	"(the block `forge project new` scaffolds; forbidigo stays out of linters.enable in warn mode) — " +
	"or set config.enforce_typed_access: off in forge.yaml if this project does not use the guardrail"

// typedAccessGuardUnconfigured reports why forbidigo, as configured for a run
// started in dir, would not check env reads. It returns "" when a forbid
// pattern covers os.Getenv, and also when the config cannot be read here (a
// TOML file, or one that does not parse): golangci-lint reports a config it
// cannot load itself, and guessing would replace its error with ours.
func typedAccessGuardUnconfigured(dir string) string {
	path := findGolangciConfig(dir)
	if path == "" {
		return "this project has no golangci-lint config (.golangci.yml), so forbidigo would check its built-in " +
			"patterns (fmt.Print*, print, println) instead of env reads"
	}
	if filepath.Ext(path) == ".toml" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	patterns, ok := forbidigoPatterns(raw)
	if !ok {
		return ""
	}
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err == nil && re.MatchString(typedAccessGuardEnvRead) {
			return ""
		}
	}
	name := filepath.Base(path)
	if len(patterns) == 0 {
		return fmt.Sprintf("%s gives forbidigo no forbid patterns, so it would check its built-in ones "+
			"(fmt.Print*, print, println) instead of env reads", name)
	}
	return fmt.Sprintf("%s gives forbidigo no pattern that forbids %s, so it would check only the patterns "+
		"listed there, not env reads", name, typedAccessGuardEnvRead)
}

// findGolangciConfig returns the config file golangci-lint loads for a run
// started in dir: the first of checksums.GolangciConfigNames in dir, else in the
// nearest ancestor that has one. "" when there is none.
func findGolangciConfig(dir string) string { return checksums.FindGolangciConfig(dir) }

// forbidigoPatterns returns the forbid patterns a golangci-lint config gives
// forbidigo, from either config version: v2 nests them under
// linters.settings.forbidigo, v1 under linters-settings.forbidigo. An entry
// is a bare pattern string or a map carrying `pattern` (v1 also spelled it
// `p`). ok is false when the document does not parse. JSON is YAML, so a
// .golangci.json reads the same way.
func forbidigoPatterns(raw []byte) (patterns []string, ok bool) {
	var doc struct {
		Linters struct {
			Settings struct {
				Forbidigo forbidigoSettings `yaml:"forbidigo"`
			} `yaml:"settings"`
		} `yaml:"linters"`
		LintersSettings struct {
			Forbidigo forbidigoSettings `yaml:"forbidigo"`
		} `yaml:"linters-settings"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, false
	}
	for _, list := range [][]any{doc.Linters.Settings.Forbidigo.Forbid, doc.LintersSettings.Forbidigo.Forbid} {
		for _, entry := range list {
			switch e := entry.(type) {
			case string:
				patterns = append(patterns, e)
			case map[string]any:
				for _, key := range []string{"pattern", "p"} {
					if s, isStr := e[key].(string); isStr && s != "" {
						patterns = append(patterns, s)
						break
					}
				}
			}
		}
	}
	return patterns, true
}

type forbidigoSettings struct {
	Forbid []any `yaml:"forbid"`
}

// anchoredFindings keeps the findings that name a file. Applied to a
// golangci-lint run that completed, it drops the run's summary ("0 issues.",
// "N issues:", "* forbidigo: N") and the source snippet under each issue,
// which externalLinesToFindings turns into file-less findings of their own.
func anchoredFindings(fs []lintJSONFinding) []lintJSONFinding {
	var out []lintJSONFinding
	for _, f := range fs {
		if f.File != "" {
			out = append(out, f)
		}
	}
	return out
}
