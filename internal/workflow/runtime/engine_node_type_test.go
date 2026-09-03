package runtime

import "testing"

func TestIsActivityType_KnownActivityTypes(t *testing.T) {
	t.Parallel()
	activityTypes := []string{
		"call_llm",
		"execute_tools",
		"compact",
		"approval",
		"save_message",
		"create_worktree",
	}
	for _, nt := range activityTypes {
		if !isActivityType(nt) {
			t.Errorf("isActivityType(%q) = false, want true", nt)
		}
	}
}

func TestIsActivityType_StructuralTypes(t *testing.T) {
	t.Parallel()
	structuralTypes := []string{
		"run",
		"workflow",
		"loop",
		"join",
		"router",
	}
	for _, nt := range structuralTypes {
		if isActivityType(nt) {
			t.Errorf("isActivityType(%q) = true, want false (structural type)", nt)
		}
	}
}

func TestIsActivityType_RejectsUnknownTypes(t *testing.T) {
	t.Parallel()
	// These are the critical cases: unknown strings that previously would
	// have been dispatched as Temporal activities, causing ActivityNotRegisteredError.
	unknownTypes := []string{
		"null", // YAML null parsed as string → "Null" activity → crash
		"Null", // PascalCase variant
		"nonexistent",
		"foo_bar",
		"call_llm_v2", // Typo/future type
		"",            // Empty string
	}
	for _, nt := range unknownTypes {
		if isActivityType(nt) {
			t.Errorf("isActivityType(%q) = true, want false (unknown type should be rejected)", nt)
		}
	}
}

func TestNodeTypeToActivityName_KnownTypes(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"call_llm":        "CallLLM",
		"execute_tools":   "ExecuteTools",
		"save_message":    "SaveMessage",
		"compact":         "Compact",
		"approval":        "Approval",
		"create_worktree": "CreateWorktree",
	}
	for input, want := range tests {
		got := nodeTypeToActivityName(input)
		if got != want {
			t.Errorf("nodeTypeToActivityName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNodeTypeToActivityName_EmptyReturnsEmpty(t *testing.T) {
	t.Parallel()
	if got := nodeTypeToActivityName(""); got != "" {
		t.Errorf("nodeTypeToActivityName(\"\") = %q, want empty", got)
	}
}

// TestNodeTypeToActivityName_NullProducesBadName demonstrates why isActivityType
// must reject unknown types: snakeToPascal("null") produces "Null", which is
// not a registered activity name.
func TestNodeTypeToActivityName_NullProducesBadName(t *testing.T) {
	t.Parallel()
	got := nodeTypeToActivityName("null")
	if got != "Null" {
		t.Fatalf("expected nodeTypeToActivityName(\"null\") = \"Null\", got %q", got)
	}
	// This is why isActivityType must reject "null" — the activity name "Null" isn't registered.
}
