package cli

// `forge project schema <name>`: print one of forge's published JSON Schemas.
//
// The env schema is the public API of an env's declaration
// (env_document_schema.go): an env document validates against it, and an
// editor or an agent writing one reads it. It is printed from the copy
// embedded in this binary, so it is exactly the schema this forge validates
// with.

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/schemagen"
	"github.com/reliant-labs/forge/internal/testcollect"
	"github.com/reliant-labs/forge/internal/vulnpatch"
	"github.com/reliant-labs/forge/internal/vulnscan"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// SchemaSpec names one schema forge publishes, for `forge project
// capabilities`.
type SchemaSpec struct {
	Name       string `json:"name"`
	APIVersion string `json:"api_version"`
	ID         string `json:"id"`
	Command    string `json:"command"`
	Describes  string `json:"describes"`
}

// publishedSchemas is every schema `forge project schema` prints.
func publishedSchemas() map[string]struct {
	spec SchemaSpec
	body func() []byte
} {
	return map[string]struct {
		spec SchemaSpec
		body func() []byte
	}{
		"env": {
			spec: SchemaSpec{
				Name: "env", APIVersion: envDocumentAPIVersion, ID: envSchemaID,
				Command:   "forge project schema env",
				Describes: "one env's declaration: what deploy/kcl/<env>/main.k renders to, or a hand-written env document (deploy/envs/<env>.yaml)",
			},
			body: envSchemaJSON,
		},
		"vuln-list": {
			spec: SchemaSpec{
				Name: "vuln-list", APIVersion: vulnscan.DocumentSchema, ID: vulnListSchemaID,
				Command:   "forge project schema vuln-list",
				Describes: "`forge vuln list|show|scan --json`: a release's vulnerability findings and the env's vuln_gate verdict",
			},
			body: vulnListSchemaJSON,
		},
		"test-results":       testDocumentSchema("test-results", testreport.SchemaResults, reflect.TypeOf(testreport.Results{}), "one lane run's test cases, normalised from go test -json, JUnit or Vitest JSON (.forge/tests/runs/<id>/<lane>/)"),
		"test-coverage":      testDocumentSchema("test-coverage", testreport.SchemaCoverage, reflect.TypeOf(testreport.Coverage{}), "the stored per-commit coverage document: covered files, their statement blocks and hits per lane, each file's repo path and git blob id; embedded as `coverage` in `forge test coverage --json`"),
		"test-report":        testDocumentSchema("test-report", testreport.SchemaReport, reflect.TypeOf(testreport.Report{}), "a summary of test runs on one tree: totals, coverage figures, failures and flake signals; `forge test report --json`"),
		"test-flakes":        testDocumentSchema("test-flakes", testreport.SchemaFlakes, reflect.TypeOf(testreport.Flakes{}), "each test's flake state and events, with host and infra failures kept out of the scores; `forge test flakes --json`"),
		"test-coverage-view": testDocumentSchema("test-coverage-view", schemaTestCoverageView, reflect.TypeOf(testCoverageDoc{}), "recorded coverage as `forge test coverage --json` shows it: a path or lane filter, the per-package or per-file rollup, diff coverage, and the forge.test.coverage/v1 document they come from"),
		"test-runs":          testDocumentSchema("test-runs", schemaTestRuns, reflect.TypeOf(testRunsDoc{}), "the local test runs, newest first, with each lane's exit code; `forge test runs --json`"),
		"test-collect":       testDocumentSchema("test-collect", testcollect.Schema, reflect.TypeOf(testcollect.Document{}), "the coverage `forge test collect <env> --json` pulled from each workload of an env: where from, its build, the stale builds kept apart"),
		"test-run-result":    testDocumentSchema("test-run-result", schemaTestRunResult, reflect.TypeOf(testRunResultDoc{}), "the Taskfile lanes `forge test run` ran as one run, each with its own exit code, and the lanes they recorded; `forge test run --json`"),
		"vuln-patch": {
			spec: SchemaSpec{
				Name: "vuln-patch", APIVersion: vulnpatch.DocumentSchema, ID: vulnPatchSchemaID,
				Command:   "forge project schema vuln-patch",
				Describes: "`forge vuln patch --json`: what the patcher bumped per finding, the verification, and the exit code",
			},
			body: vulnPatchSchemaJSON,
		},
	}
}

// testDocumentSchema publishes one of pkg/testreport's documents. Its schema
// is generated from the Go types the documents are written with, at the
// moment it is printed, so it cannot drift from them.
func testDocumentSchema(name, apiVersion string, root reflect.Type, describes string) struct {
	spec SchemaSpec
	body func() []byte
} {
	return struct {
		spec SchemaSpec
		body func() []byte
	}{
		spec: SchemaSpec{Name: name, APIVersion: apiVersion, ID: apiVersion, Command: "forge project schema " + name, Describes: describes},
		body: func() []byte {
			b, err := testDocumentSchemaJSON(apiVersion, root, describes)
			if err != nil {
				// The types are fixed at compile time and a test generates
				// every schema, so this is a forge bug, not a user error.
				panic(fmt.Sprintf("forge project schema %s: %v", name, err))
			}
			return b
		},
	}
}

func testDocumentSchemaJSON(apiVersion string, root reflect.Type, describes string) ([]byte, error) {
	str := func(*schemagen.Generator) (*schemagen.Schema, error) {
		return &schemagen.Schema{Types: []string{"string"}, Description: "a test id, <lang>:<package>:<name>"}, nil
	}
	s, err := schemagen.Generate(root, schemagen.Options{
		ID: apiVersion, Title: apiVersion, Description: describes,
		Types: map[reflect.Type]func(*schemagen.Generator) (*schemagen.Schema, error){
			reflect.TypeOf(testreport.TestID{}): str,
		},
	})
	if err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// schemaSpecs lists the published schemas, sorted by name.
func schemaSpecs() []SchemaSpec {
	var out []SchemaSpec
	for _, s := range publishedSchemas() {
		out = append(out, s.spec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func newProjectSchemaCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "schema [name]",
		Short: "Print a JSON Schema forge publishes — `env` is the env document's (the API of an env's declaration)",
		Long: `Print one of forge's published JSON Schemas (draft 2020-12).

  env   forge.dev/env/v1: one env's declaration. It is the document a KCL env
        (deploy/kcl/<env>/main.k) renders to, and the shape of a hand-written
        env document (deploy/envs/<env>.yaml or .json), which declares an env
        without KCL. Generated from the Go types forge decodes it with.

  vuln-list  forge.dev/vuln-list/v1: the document ` + "`forge vuln list|show|scan --json`" + `
        prints — a release's vulnerability findings and the env's vuln_gate
        verdict. Generated from the Go type forge encodes it with.

  test-results, test-coverage, test-report, test-flakes
        forge.test.results/v1, forge.test.coverage/v1, forge.test.report/v1,
        forge.test.flakes/v1: the test record's documents, generated from the
        pkg/testreport types forge and the control plane write them with.

  test-coverage-view, test-runs
        forge.test.coverage-view/v1, forge.test.runs/v1: what ` + "`forge test\n        coverage --json`" + ` and ` + "`forge test runs --json`" + ` print.
  test-collect  forge.test.collect/v1: what ` + "`forge test collect <env> --json`" + `
        pulled from each workload of an env.

  test-run-result
        forge.test.run-result/v1: what ` + "`forge test run --json`" + ` prints.

  vuln-patch  forge.dev/vuln-patch/v1: the document ` + "`forge vuln patch --json`" + ` prints —
        what was bumped per finding, the verification, and the exit code the
        vuln-patch workflow branches on.

With no name, lists the schemas. The schema printed is the one embedded in
this binary, so it is exactly what this forge validates against. Point an
editor at it with "$schema" (JSON) or a yaml-language-server comment (YAML).

Examples:
  forge project schema
  forge project schema env > env.v1.schema.json
  forge project schema vuln-list
  forge project schema test-report`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return writeSchemaList(cmd.OutOrStdout())
			}
			return runProjectSchema(cmd.OutOrStdout(), args[0])
		},
	}
	// --json is the only output: the flag exists so `--json` is accepted the
	// way every other introspection verb accepts it.
	cmd.Flags().BoolVar(&asJSON, "json", true, "emit the schema as JSON (the default and only format)")
	return cmd
}

func runProjectSchema(w io.Writer, name string) error {
	s, ok := publishedSchemas()[strings.TrimSpace(name)]
	if !ok {
		names := make([]string, 0)
		for _, spec := range schemaSpecs() {
			names = append(names, spec.Name)
		}
		return fmt.Errorf("no schema %q: forge publishes %s", name, strings.Join(names, ", "))
	}
	_, err := w.Write(s.body())
	return err
}

// writeSchemaList prints one entry per schema, its name padded to the longest
// name so a new registration needs no change here.
func writeSchemaList(w io.Writer) error {
	specs := schemaSpecs()
	width := 0
	for _, s := range specs {
		width = max(width, len(s.Name))
	}
	for _, s := range specs {
		if _, err := fmt.Fprintf(w, "%-*s %s  %s\n%*s%s\n", width, s.Name, s.APIVersion, s.Command, width+1, "", s.Describes); err != nil {
			return err
		}
	}
	return nil
}
