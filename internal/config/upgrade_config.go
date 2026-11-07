package config

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// UpgradeConfig is the `upgrade:` block: what `forge project upgrade` does for
// the project beyond forge's own codemods and playbooks.
type UpgradeConfig struct {
	// VendoredContracts declare contracts the project vendors from another
	// repository, and how to catch the vendored copy up. See
	// [VendoredContract].
	VendoredContracts []VendoredContract `yaml:"vendored_contracts,omitempty"`
}

// VendoredContract is a contract (protos, a schema, generated clients) the
// project copies from a repository it does not own, with a check that reports
// how far the copy lags and a command sequence that catches it up.
//
// forge does not know what is vendored or from where. After a forge bump the
// vendored copy is what most often goes stale, so `forge project upgrade` reads
// the check's report and, when it says the copy lags, runs the declared
// commands before it regenerates.
//
//	upgrade:
//	  vendored_contracts:
//	    - name: controlplane
//	      report: tmp/ci/reliant-vendored-proto.json
//	      schema: reliant-vendored-proto/v1
//	      env_path: revendor.env
//	      commands:
//	        - [node, .github/scripts/sync-controlplane-proto.mjs]
//	        - [npm, run, "proto:generate:controlplane"]
//
// The report is a JSON document with a top-level `status` that is "in-sync" or
// "lag" (any other value, or a `schema` other than the one declared, is handed
// to a human rather than guessed at). `env_path` optionally names an object in
// the report, as a dotted path, whose string values are exported to the
// commands.
type VendoredContract struct {
	// Name labels the contract in upgrade's output. Required and unique.
	Name string `yaml:"name"`
	// Report is the project-relative path of the lag report the project's own
	// check writes.
	Report string `yaml:"report"`
	// Schema is the report's expected `schema` value. A report claiming
	// another is not trusted.
	Schema string `yaml:"schema"`
	// EnvPath is a dotted path to an object in the report whose string values
	// become environment variables for the commands (for example
	// "revendor.env").
	EnvPath string `yaml:"env_path,omitempty"`
	// Commands are the argv lists that re-vendor, run in order from the
	// project root. No shell: spell a pipeline as [sh, -c, "..."].
	Commands [][]string `yaml:"commands"`
}

// validateUpgradeConfig checks the `upgrade.vendored_contracts` list.
func validateUpgradeConfig(cfg *ProjectConfig, root *yaml.Node) []validationIssue {
	var out []validationIssue
	seen := map[string]bool{}
	for i, v := range cfg.Upgrade.VendoredContracts {
		label := fmt.Sprintf("upgrade.vendored_contracts[%d]", i)
		at := func(key string) (int, int) {
			segs := []string{"upgrade", "vendored_contracts", fmt.Sprintf("[%d]", i)}
			if key != "" {
				segs = append(segs, key)
			}
			return findNodePos(root, segs)
		}
		add := func(key, msg, fix string) {
			line, col := at(key)
			if line == 0 {
				line, col = at("")
			}
			out = append(out, validationIssue{line: line, column: col, msg: label + " " + msg, fix: fix})
		}
		name := strings.TrimSpace(v.Name)
		switch {
		case name == "":
			add("", "has no name", "add `name:`, the label upgrade prints.")
		case seen[name]:
			add("name", fmt.Sprintf("name %q is declared twice", name), "give each contract a unique name.")
		default:
			seen[name] = true
		}
		if !isProjectRelative(v.Report, false) {
			add("report", fmt.Sprintf("report %q is not a file inside the project", v.Report),
				"name the project-relative path of the lag report your check writes.")
		}
		if strings.TrimSpace(v.Schema) == "" {
			add("schema", "has no schema", "set `schema:` to the report's `schema` value, so a report of another shape is never acted on.")
		}
		if len(v.Commands) == 0 {
			add("commands", "has no commands", "list the argv lists that re-vendor, e.g. [[node, sync.mjs]].")
		}
		for j, c := range v.Commands {
			if len(c) == 0 || strings.TrimSpace(c[0]) == "" {
				add("commands", fmt.Sprintf("command %d is empty", j), "each command is an argv list; a shell line is [sh, -c, \"...\"].")
			}
		}
	}
	return out
}
