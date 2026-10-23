// File: internal/cli/scaffold/entity_removed_forms_test.go
//
// The `field:type` flag grammar and `--from-schema` were removed: the proto
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

// `--from-schema` stays PARSEABLE so it can be refused with an explanation.
// Dropped outright it would answer with cobra's `unknown flag`, which tells
// the caller nothing about why forge no longer adopts an existing database —
// and that "why" is not something they can guess.
func TestEntity_FromSchemaRefusesWithTheReasonAndTheAlternative(t *testing.T) {
	cmd := newEntityCmd(testFactory())

	if f := cmd.Flags().Lookup("from-schema"); f == nil {
		t.Fatal("--from-schema must still PARSE so it can be refused; dropping it yields cobra's `unknown flag`")
	} else if !f.Hidden {
		t.Error("--from-schema exists only to be refused, so it must not appear in --help")
	}

	if err := cmd.Flags().Set("from-schema", "invoices"); err != nil {
		t.Fatal(err)
	}
	// Through Args, not RunE: with no entity name the arity check would
	// otherwise fire first and print usage.
	err := cmd.Args(cmd, nil)
	if err == nil {
		t.Fatal("--from-schema must refuse, ahead of the arity check")
	}
	got := err.Error()

	for _, want := range []string{
		// The removal and the reason — a product decision the caller
		// cannot infer from a flag disappearing.
		"--from-schema was removed",
		"adopt a database forge did not create",
		// The proto-first path, named for the table they asked about
		// (singularized: `invoices` → message Invoice).
		"// forge:entity",
		"message Invoice {",
		"forge scaffold",
		// The collision an already-applied table will cause, and the way
		// to keep it — without this the advice is a trap.
		"ALREADY-APPLIED table will collide",
		"forge generate",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal is missing %q — the user cannot act on it:\n%s", want, got)
		}
	}

	// An Args error normally keeps cobra's usage block. This one must not:
	// the flag list says nothing about why --from-schema is gone, and
	// putting it between the user and the explanation is the dump this
	// refusal exists to replace.
	if !cmd.SilenceUsage {
		t.Error("the refusal must suppress the usage dump — it is an explanation, not an arity mistake")
	}
}

// The refusal must not fire for anyone who did not ask for it: it sits in
// the Args validator, which runs on every `forge scaffold entity`.
func TestEntity_WithoutFromSchemaTheArgsCheckIsUnchanged(t *testing.T) {
	cmd := newEntityCmd(testFactory())
	if err := cmd.Args(cmd, []string{"order"}); err != nil {
		t.Errorf("the --from-schema refusal fired without the flag: %v", err)
	}
	// And the arity check it precedes still works.
	if err := cmd.Args(cmd, nil); err == nil {
		t.Error("a bare `scaffold entity` must still be an args error, so usage still prints")
	}
}
