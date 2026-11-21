// File: internal/cli/scaffold/entity_removed_forms_test.go
//
// The `field:type` flag grammar was removed: the proto
// is the only place an entity is declared. These tests pin the REFUSALS,
// because a removal whose old spelling fails obscurely is worse than the
// feature it removed — the user typed something forge understood last week
// and needs to be told what replaced it, not handed a cobra usage dump.
//
// What the refusal owes the caller (all asserted below):
//   - it names the removed grammar, so the failure is legible as a removal
//     rather than a bug;
//   - it hands back the proto-first equivalent for the entity they named,
//     spelled out, including the `// forge:entity` marker;
//   - it echoes the fields they typed as proto fields, so the message they
//     were reaching for is already written;
//   - it names the command that finishes the job.

package scaffold

import (
	"strings"
	"testing"
)

func TestEntity_FieldListFormRefusesWithTheProtoEquivalent(t *testing.T) {
	err := entityWithoutProtoRefusal([]string{"bookmark", "url:string", "done:bool", "tags:[]string"})
	if err == nil {
		t.Fatal("the removed `field:type` form must refuse, not scaffold")
	}
	got := err.Error()

	for _, want := range []string{
		// The removal, named.
		"`field:type` flag grammar was removed",
		// The proto-first equivalent, spelled out for THIS entity.
		"// forge:entity",
		"message Bookmark {",
		// The fields the user typed, as the proto fields that replace them.
		"string url = 2;",
		"bool done = 3;",
		"repeated string tags = 4;",
		// The command that finishes it.
		"forge scaffold",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal is missing %q — the user cannot act on it:\n%s", want, got)
		}
	}
}

// A bare `forge scaffold entity <name>` never carried a field list, so the
// refusal must not blame a grammar the user did not use — but it still owes
// them the proto-first path.
func TestEntity_BareNameRefusesWithoutBlamingTheFieldList(t *testing.T) {
	err := entityWithoutProtoRefusal([]string{"bookmark"})
	if err == nil {
		t.Fatal("a bare entity name must refuse — the proto is where an entity is declared")
	}
	got := err.Error()

	if strings.Contains(got, "`field:type` flag grammar was removed") {
		t.Errorf("no field list was typed; the refusal must not blame that grammar:\n%s", got)
	}
	for _, want := range []string{
		"the proto is the only place an entity is declared",
		"// forge:entity",
		"message Bookmark {",
		"forge scaffold",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal is missing %q:\n%s", want, got)
		}
	}
}

// The entity name reaches the suggested message through the same PascalCase
// derivation a real birth uses, so a multi-word name is not echoed back in a
// spelling forge itself would reject.
func TestEntity_RefusalNamesTheMessageForgeWouldBirth(t *testing.T) {
	err := entityWithoutProtoRefusal([]string{"module_config"})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if got := err.Error(); !strings.Contains(got, "message ModuleConfig {") {
		t.Errorf("multi-word entity must be suggested as its PascalCase message:\n%s", got)
	}
}

// `forge scaffold entity` needs a name unless --from-proto sweeps a service.
func TestEntity_ArgsCheck(t *testing.T) {
	cmd := newEntityCmd(testFactory())
	if err := cmd.Args(cmd, []string{"order"}); err != nil {
		t.Errorf("a named entity was refused by the args check: %v", err)
	}
	if err := cmd.Args(cmd, nil); err == nil {
		t.Error("a bare `scaffold entity` must still be an args error, so usage still prints")
	}
}
