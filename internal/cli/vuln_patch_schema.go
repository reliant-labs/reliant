package cli

// The vuln-patch schema: the JSON Schema of `forge vuln patch --json`
// (vulnpatch.Document, forge.dev/vuln-patch/v1), published beside the
// vuln-list schema (`forge project schema vuln-patch`).
//
// It is GENERATED from vulnpatch.Document, the type the command encodes.
// Committed at schema/vuln-patch.v1.schema.json and embedded in the binary;
// TestVulnPatchSchemaIsCurrent fails on drift, and
// `FORGE_UPDATE_GOLDEN=1 go test ./internal/cli -run TestVulnPatchSchemaIsCurrent`
// rewrites it after a field is added.

import (
	"encoding/json"
	"reflect"

	"github.com/reliant-labs/forge/internal/schemagen"
	"github.com/reliant-labs/forge/internal/vulnpatch"
	forgeschema "github.com/reliant-labs/forge/schema"
)

// vulnPatchSchemaID is where each release publishes the schema.
const vulnPatchSchemaID = "https://raw.githubusercontent.com/reliant-labs/forge/main/schema/vuln-patch.v1.schema.json"

func generateVulnPatchSchema(describe func(reflect.Type, string) string) ([]byte, error) {
	s, err := schemagen.Generate(reflect.TypeOf(vulnpatch.Document{}), schemagen.Options{
		ID:    vulnPatchSchemaID,
		Title: "forge vuln patch document (" + vulnpatch.DocumentSchema + ")",
		Description: "What `forge vuln patch --json` did to each requested finding — bumped, verified, fixed, still present, " +
			"or skipped for an agent with the reason — and the exit code it chose. Generated from the Go type forge encodes it with.",
		Required: map[reflect.Type][]string{
			reflect.TypeOf(vulnpatch.Document{}):     {"schema", "env", "release", "dir", "allow_major", "changed", "findings", "bumps", "summary", "exit_code"},
			reflect.TypeOf(vulnpatch.Result{}):       {"id", "severity", "package", "installed", "status"},
			reflect.TypeOf(vulnpatch.Bump{}):         {"ecosystem", "manifest", "package", "from", "to", "direct", "commands"},
			reflect.TypeOf(vulnpatch.VerifyResult{}): {"command", "source", "passed", "rescanned"},
			reflect.TypeOf(vulnpatch.PRResult{}):     {"branch", "committed"},
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

// vulnPatchSchemaJSON is the published schema, as embedded in this binary.
func vulnPatchSchemaJSON() []byte { return forgeschema.VulnPatchV1() }
