// Copyright (c) 2025 Reliant Labs

package approvalkind_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/approvalkind"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

func ptr(s string) *string { return &s }

// Every tool that raises an approval card, and the two workflow-step sources,
// are classified as chat-collaboration.md §2.4 decides. The tool names are
// internal/llm/tools' own constants, so a rename there fails here.
func TestFor(t *testing.T) {
	cases := []struct {
		name   string
		source approvalkind.Source
		want   approvalkind.Role
	}{
		// The two workflow-step sources: both reach ApprovalCreate with no
		// tool call (the manual-mode gate is an approval node in agent.yaml).
		{"workflow approval node", approvalkind.Source{}, approvalkind.Reply},
		{"manual-mode tool gate (Approve tool execution?)", approvalkind.Source{}, approvalkind.Reply},

		// Workflow changes: manage.
		{tools.ToolCreateTriggerActivation, approvalkind.Source{ToolCall: true, ToolName: tools.ToolCreateTriggerActivation}, approvalkind.Manage},
		{tools.ToolUpdateTriggerActivation, approvalkind.Source{ToolCall: true, ToolName: tools.ToolUpdateTriggerActivation}, approvalkind.Manage},
		{tools.ToolEnableTrigger, approvalkind.Source{ToolCall: true, ToolName: tools.ToolEnableTrigger}, approvalkind.Manage},
		{tools.ToolDeleteTrigger, approvalkind.Source{ToolCall: true, ToolName: tools.ToolDeleteTrigger}, approvalkind.Manage},
		// Never raises a card today; classified with its siblings.
		{tools.ToolPauseTrigger, approvalkind.Source{ToolCall: true, ToolName: tools.ToolPauseTrigger}, approvalkind.Manage},

		// The machine: owner.
		{tools.ToolManageMachine, approvalkind.Source{ToolCall: true, ToolName: tools.ToolManageMachine}, approvalkind.Owner},
		{tools.ToolManageMachinePool, approvalkind.Source{ToolCall: true, ToolName: tools.ToolManageMachinePool}, approvalkind.Owner},
		// A Forge Change: owner (design open question 3).
		{tools.ToolManageChange, approvalkind.Source{ToolCall: true, ToolName: tools.ToolManageChange}, approvalkind.Owner},
		// A connection proposal: owner, forced.
		{tools.ToolProposeConnection, approvalkind.Source{ToolCall: true, ToolName: tools.ToolProposeConnection, ConnectionProposal: true}, approvalkind.Owner},
		{"a connection proposal under any tool name", approvalkind.Source{ToolCall: true, ToolName: tools.ToolEnableTrigger, ConnectionProposal: true}, approvalkind.Owner},
		{"a connection proposal on a step", approvalkind.Source{ConnectionProposal: true}, approvalkind.Owner},

		// Fail closed.
		{"an unclassified tool", approvalkind.Source{ToolCall: true, ToolName: "some_new_tool"}, approvalkind.Owner},
		{"a tool call from workflow code that sends no tool name", approvalkind.Source{ToolCall: true}, approvalkind.Owner},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := approvalkind.For(tc.source); got != tc.want {
				t.Fatalf("For(%+v) = %q, want %q", tc.source, got, tc.want)
			}
		})
	}
}

// A stored min_role reads back as itself; NULL, empty and anything unknown
// mean owner, so a row an older (or newer) worker wrote never widens who may
// decide it.
func TestStored(t *testing.T) {
	cases := []struct {
		name string
		in   *string
		want approvalkind.Role
	}{
		{"NULL means owner", nil, approvalkind.Owner},
		{"empty", ptr(""), approvalkind.Owner},
		{"reply", ptr("reply"), approvalkind.Reply},
		{"manage", ptr("manage"), approvalkind.Manage},
		{"owner", ptr("owner"), approvalkind.Owner},
		{"unknown", ptr("view"), approvalkind.Owner},
		{"case matters", ptr("Reply"), approvalkind.Owner},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := approvalkind.Stored(tc.in); got != tc.want {
				t.Fatalf("Stored = %q, want %q", got, tc.want)
			}
		})
	}
	// What For writes, Stored reads back unchanged.
	for _, r := range []approvalkind.Role{approvalkind.Reply, approvalkind.Manage, approvalkind.Owner} {
		s := r.String()
		if got := approvalkind.Stored(&s); got != r {
			t.Fatalf("Stored(%q) = %q, want it back", s, got)
		}
	}
}

// Levels nest, and an unknown level or requirement fails closed.
func TestAllows(t *testing.T) {
	levels := []approvalkind.Role{approvalkind.Reply, approvalkind.Manage, approvalkind.Owner, "view", ""}
	want := map[[2]approvalkind.Role]bool{
		{approvalkind.Reply, approvalkind.Reply}:   true,
		{approvalkind.Manage, approvalkind.Reply}:  true,
		{approvalkind.Manage, approvalkind.Manage}: true,
		{approvalkind.Owner, approvalkind.Reply}:   true,
		{approvalkind.Owner, approvalkind.Manage}:  true,
		{approvalkind.Owner, approvalkind.Owner}:   true,
		// An unknown requirement is owner's.
		{approvalkind.Owner, "view"}: true,
		{approvalkind.Owner, ""}:     true,
	}
	for _, have := range levels {
		for _, need := range levels {
			if got := have.Allows(need); got != want[[2]approvalkind.Role{have, need}] {
				t.Errorf("%q.Allows(%q) = %v, want %v", have, need, got, !got)
			}
		}
	}
}

// Every tool type that raises an approval card (implements
// tools.AutomationApprovalTool's PrepareAutomation) must be classified here
// on purpose. A new one would silently be owner-only, which is safe but
// probably not what its author meant, so it fails this test until it is
// added to approvalkind's table and to TestFor.
func TestEveryCardRaisingToolIsClassified(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "llm", "tools", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("listing internal/llm/tools: %v (%d files)", err, len(files))
	}
	impl := regexp.MustCompile(`(?m)^func \(\w+ \*?(\w+)\) PrepareAutomation\(`)
	var got []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range impl.FindAllSubmatch(src, -1) {
			got = append(got, string(m[1]))
		}
	}
	sort.Strings(got)
	// Each receiver, with the tools it serves in approvalkind's table.
	classified := []string{
		"activationTool",        // create/update/enable/pause/delete_trigger: manage
		"changeTool",            // manage_change: owner
		"machinePoolTool",       // manage_machine_pool: owner
		"machineTool",           // manage_machine: owner
		"proposeConnectionTool", // propose_connection: owner
	}
	if len(got) != len(classified) {
		t.Fatalf("PrepareAutomation implementers = %v, want %v: classify the new card-raising tool in internal/approvalkind", got, classified)
	}
	for i := range got {
		if got[i] != classified[i] {
			t.Fatalf("PrepareAutomation implementers = %v, want %v: classify the new card-raising tool in internal/approvalkind", got, classified)
		}
	}
}
