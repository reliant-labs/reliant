package cli

// `forge project schema <name>`: print one of forge's published JSON Schemas.
//
// The env schema is the public API of an env's declaration
// (env_document_schema.go): an env document validates against it, and an
// editor or an agent writing one reads it. It is printed from the copy
// embedded in this binary, so it is exactly the schema this forge validates
// with.

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
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
	}
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

With no name, lists the schemas. The schema printed is the one embedded in
this binary, so it is exactly what this forge validates against. Point an
editor at it with "$schema" (JSON) or a yaml-language-server comment (YAML).

Examples:
  forge project schema
  forge project schema env > env.v1.schema.json`,
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

func writeSchemaList(w io.Writer) error {
	for _, s := range schemaSpecs() {
		if _, err := fmt.Fprintf(w, "%-6s %s  %s\n       %s\n", s.Name, s.APIVersion, s.Command, s.Describes); err != nil {
			return err
		}
	}
	return nil
}
