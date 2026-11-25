package templates_test

import (
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// serverRootStub is the ROOTS block `forge generate` appends to a graph-mode
// project's config_gen.k, for the default `server` root reaching PORT and the
// given credentials (the first one required). The config_gen.k stubs the env
// renders import carry it, as a generated module does.
//
// It renders through codegen.RootsKCL — the function generate calls — rather
// than handing project/config-roots.k.tmpl a data struct of its own: a
// hand-built struct missed every field the template later grew (.Process,
// #2683), and each such miss failed every scaffold render test at once.
func serverRootStub(t *testing.T, secrets ...string) string {
	t.Helper()
	leaves := []codegen.ConfigLeaf{{Path: "port", EnvVar: "PORT", Type: "int32"}}
	for i, s := range secrets {
		leaves = append(leaves, codegen.ConfigLeaf{Path: s, EnvVar: s, Sensitive: true, Required: i == 0, Type: "string"})
	}
	out := codegen.RootsKCL([]codegen.GraphRoot{{Name: "server", Args: []string{"server"}, Config: leaves}})
	if out == "" {
		t.Fatal("codegen.RootsKCL rendered no ROOTS block for the default server root")
	}
	return out
}
