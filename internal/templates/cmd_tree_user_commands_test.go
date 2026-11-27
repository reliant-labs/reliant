package templates_test

import (
	"slices"
	"strings"
	"testing"
)

func TestCmdTreeUserCommands_DependenciesPointAwayFromGraph(t *testing.T) {
	data := cmdTreeData{Module: "example.com/demo", Name: "demo"}
	usercmd := renderCmdTree(t, "cmd-tree-user-commands.go.tmpl", data)
	if imports := dbFileImports(t, usercmd); !slices.Equal(imports, []string{"github.com/spf13/cobra"}) {
		t.Fatalf("the user command package must start without any graph dependency; imports = %v", imports)
	}
	if !strings.Contains(usercmd, "package usercmd\n") || !strings.Contains(usercmd, "func Commands() []*cobra.Command") {
		t.Fatalf("expected usercmd.Commands entry point:\n%s", usercmd)
	}

	bridge := renderCmdTree(t, "cmd-tree-commands-bridge.go.tmpl", data)
	if !slices.Contains(dbFileImports(t, bridge), "example.com/demo/cmd/demo/usercmd") ||
		!strings.Contains(bridge, "return usercmd.Commands()") {
		t.Fatalf("the assembled command tree must call the sibling entry point:\n%s", bridge)
	}
}
