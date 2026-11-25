package cli

// The vuln-list schema: the JSON Schema of `forge vuln list|show|scan --json`
// (vulnscan.Document, forge.dev/vuln-list/v1), published beside the env
// schema (`forge project schema vuln-list`).
//
// It is GENERATED from vulnscan.Document, the type the command encodes, so it
// cannot describe a field the document lacks. Committed at
// schema/vuln-list.v1.schema.json and embedded in the binary;
// TestVulnListSchemaIsCurrent fails on drift, and
// `FORGE_UPDATE_GOLDEN=1 go test ./internal/cli -run TestVulnListSchemaIsCurrent`
// rewrites it after a field is added.

import (
	"encoding/json"
	"reflect"

	"github.com/reliant-labs/forge/internal/schemagen"
	"github.com/reliant-labs/forge/internal/vulnscan"
	forgeschema "github.com/reliant-labs/forge/schema"
)

// vulnListSchemaID is where each release publishes the schema.
const vulnListSchemaID = "https://raw.githubusercontent.com/reliant-labs/forge/main/schema/vuln-list.v1.schema.json"

func generateVulnListSchema(describe func(reflect.Type, string) string) ([]byte, error) {
	s, err := schemagen.Generate(reflect.TypeOf(vulnscan.Document{}), schemagen.Options{
		ID:    vulnListSchemaID,
		Title: "forge vuln list document (" + vulnscan.DocumentSchema + ")",
		Description: "What a release's artifacts are known to be vulnerable to, and what the env's vuln_gate makes of it: " +
			"the output of `forge vuln list|show|scan --json`. Generated from the Go type forge encodes it with.",
		Required: map[reflect.Type][]string{
			reflect.TypeOf(vulnscan.Document{}):        {"schema", "env", "release", "coverage", "db", "gate", "counts", "findings"},
			reflect.TypeOf(vulnscan.DocumentFinding{}): {"id", "severity", "kev", "package", "artifacts", "action"},
			reflect.TypeOf(vulnscan.GateResult{}):      {"mode", "graded_as", "verdict", "blocking", "determined"},
		},
		Describe: describe,
	})
	if err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// vulnListSchemaJSON is the published schema, as embedded in this binary.
func vulnListSchemaJSON() []byte { return forgeschema.VulnListV1() }
