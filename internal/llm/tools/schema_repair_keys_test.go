// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeSchemaJSON returns the real, generated write-tool parameter schema so
// these tests exercise the schema the model actually sees rather than a
// hand-written stand-in that could drift from it.
func writeSchemaJSON(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(NewWriteTool().ParamSchema())
	if err != nil {
		t.Fatalf("marshal write schema: %v", err)
	}
	return b
}

// TestValidateJSONWithRepair_WriteFileAliasIsRepaired reproduces the observed
// production failure: the model emitted {"file": ...} instead of
// {"file_path": ...} and the write was hard-rejected with
// `unexpected additional properties ["file"]`, costing a full turn to retry.
func TestValidateJSONWithRepair_WriteFileAliasIsRepaired(t *testing.T) {
	t.Parallel()
	schema := writeSchemaJSON(t)
	input := `{"file": "/tmp/scratchpad/x.md", "content": "hello"}`

	repaired, err := ValidateJSONWithRepair("write", input, schema)
	if err != nil {
		t.Fatalf("expected {\"file\": ...} to be repaired to file_path, got error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(repaired), &got); err != nil {
		t.Fatalf("repaired output is not valid JSON: %v", err)
	}
	if got["file_path"] != "/tmp/scratchpad/x.md" {
		t.Errorf("file_path = %v, want /tmp/scratchpad/x.md", got["file_path"])
	}
	if _, still := got["file"]; still {
		t.Errorf("aliased key %q should have been renamed away, got: %s", "file", repaired)
	}
	if got["content"] != "hello" {
		t.Errorf("content = %v, want hello", got["content"])
	}
}

// TestValidateJSONWithRepair_WriteCamelCaseAliasIsRepaired covers the other
// spelling models reach for: filePath instead of file_path.
func TestValidateJSONWithRepair_WriteCamelCaseAliasIsRepaired(t *testing.T) {
	t.Parallel()
	schema := writeSchemaJSON(t)
	input := `{"filePath": "/tmp/scratchpad/y.md", "content": "hi"}`

	repaired, err := ValidateJSONWithRepair("write", input, schema)
	if err != nil {
		t.Fatalf("expected filePath to be repaired to file_path, got error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(repaired), &got); err != nil {
		t.Fatalf("repaired output is not valid JSON: %v", err)
	}
	if got["file_path"] != "/tmp/scratchpad/y.md" {
		t.Errorf("file_path = %v, want /tmp/scratchpad/y.md", got["file_path"])
	}
}

// TestValidateJSONWithRepair_NeverClobbersProvidedValue asserts the repair
// refuses to overwrite a value the model actually supplied. Renaming "file"
// onto an already-present "file_path" would silently write to a different path
// than the one requested — strictly worse than a clean rejection.
func TestValidateJSONWithRepair_NeverClobbersProvidedValue(t *testing.T) {
	t.Parallel()
	schema := writeSchemaJSON(t)
	input := `{"file": "/tmp/decoy.md", "file_path": "/tmp/real.md", "content": "hi"}`

	if _, err := ValidateJSONWithRepair("write", input, schema); err == nil {
		t.Fatal("expected validation to fail rather than clobber a provided file_path")
	}
}

// TestRepairAliasedKeys_AmbiguousMatchIsRejected asserts that when an unknown
// key could plausibly mean two different schema properties, the repair declines
// rather than guessing.
func TestRepairAliasedKeys_AmbiguousMatchIsRejected(t *testing.T) {
	t.Parallel()
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"source_path": map[string]any{"type": "string"},
			"target_path": map[string]any{"type": "string"},
		},
	}
	input := map[string]any{"path": "/tmp/x"}

	_, renamed := repairAliasedKeys(input, schema)
	if len(renamed) != 0 {
		t.Errorf("ambiguous key should not be renamed, got renames: %v", renamed)
	}
}

// TestRepairAliasedKeys_TypeMismatchIsRejected asserts a lexical match alone is
// not enough: the supplied value must also fit the target property's type.
func TestRepairAliasedKeys_TypeMismatchIsRejected(t *testing.T) {
	t.Parallel()
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string"},
		},
	}
	input := map[string]any{"file": []any{"not", "a", "string"}}

	_, renamed := repairAliasedKeys(input, schema)
	if len(renamed) != 0 {
		t.Errorf("type-mismatched value should not be renamed, got renames: %v", renamed)
	}
}

// TestRepairAliasedKeys_OpenSchemaIsLeftAlone asserts we only rename where an
// unknown key is actually illegal. When additionalProperties is permitted the
// key is a legitimate value and renaming it would destroy data.
func TestRepairAliasedKeys_OpenSchemaIsLeftAlone(t *testing.T) {
	t.Parallel()
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string"},
		},
	}
	input := map[string]any{"file": "/tmp/x"}

	_, renamed := repairAliasedKeys(input, schema)
	if len(renamed) != 0 {
		t.Errorf("open schema should not be renamed, got renames: %v", renamed)
	}
}

// TestRepairAliasedKeys_UnrelatedKeyIsLeftAlone asserts the matcher is lexical,
// not semantic: it must not invent a mapping between unrelated names.
func TestRepairAliasedKeys_UnrelatedKeyIsLeftAlone(t *testing.T) {
	t.Parallel()
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string"},
		},
	}
	input := map[string]any{"destination": "/tmp/x"}

	_, renamed := repairAliasedKeys(input, schema)
	if len(renamed) != 0 {
		t.Errorf("unrelated key should not be renamed, got renames: %v", renamed)
	}
}

// TestRepairAliasedKeys_ReportsRenamePath asserts the telemetry string names
// both the wrong key and what it was resolved to, so the log answers "which
// alias did the model reach for" without re-deriving it.
func TestRepairAliasedKeys_ReportsRenamePath(t *testing.T) {
	t.Parallel()
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string"},
		},
	}
	input := map[string]any{"file": "/tmp/x"}

	_, renamed := repairAliasedKeys(input, schema)
	if len(renamed) != 1 {
		t.Fatalf("expected exactly one rename, got %v", renamed)
	}
	if !strings.Contains(renamed[0], "file") || !strings.Contains(renamed[0], "file_path") {
		t.Errorf("rename report %q should name both the alias and its target", renamed[0])
	}
}

// TestValidateJSONWithRepair_ValidInputUntouched guards the common path: a
// correct call must not be perturbed by the repair layer.
func TestValidateJSONWithRepair_ValidInputUntouched(t *testing.T) {
	t.Parallel()
	schema := writeSchemaJSON(t)
	input := `{"file_path": "/tmp/ok.md", "content": "hi"}`

	repaired, err := ValidateJSONWithRepair("write", input, schema)
	if err != nil {
		t.Fatalf("valid input must not error: %v", err)
	}
	if repaired != input {
		t.Errorf("valid input must be returned byte-identical, got %q", repaired)
	}
}

// TestWriteTool_FileAliasWritesTheFile is the end-to-end proof: it drives the
// real wrapper Run path with the exact payload that failed in production and
// asserts a file actually lands on disk. The repair is only worth anything if
// it survives the wrapper's strict DisallowUnknownFields decode after
// validation, which this exercises and the unit tests above do not.
func TestWriteTool_FileAliasWritesTheFile(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	target := filepath.Join(tempDir, "aliased.md")

	chatID := "write-alias-" + t.Name()
	thread := "0"
	ClearFileRecordsForThread(chatID, thread)
	defer ClearFileRecordsForThread(chatID, thread)

	tool := NewWriteTool()
	ctx := newTestToolContext(t, tempDir, chatID, thread)

	resp, err := tool.Run(ctx, ToolCall{
		ID:    "call-1",
		Name:  WriteToolName,
		Input: `{"file": "` + target + `", "content": "aliased content\n"}`,
	})
	require.NoError(t, err)
	assert.False(t, resp.IsError, "aliased file key should be repaired, not rejected: %s", resp.Content)

	got, err := os.ReadFile(target)
	require.NoError(t, err, "the write must actually reach disk")
	assert.Equal(t, "aliased content\n", string(got))
}

// TestWriteTool_AmbiguousAliasStillRejected pins the safety boundary at the
// wrapper level: when the model supplies BOTH the alias and the real key, the
// call must fail rather than silently write to one of the two paths.
func TestWriteTool_AmbiguousAliasStillRejected(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	decoy := filepath.Join(tempDir, "decoy.md")
	real := filepath.Join(tempDir, "real.md")

	chatID := "write-alias-ambig-" + t.Name()
	thread := "0"
	ClearFileRecordsForThread(chatID, thread)
	defer ClearFileRecordsForThread(chatID, thread)

	tool := NewWriteTool()
	ctx := newTestToolContext(t, tempDir, chatID, thread)

	resp, err := tool.Run(ctx, ToolCall{
		ID:    "call-2",
		Name:  WriteToolName,
		Input: `{"file": "` + decoy + `", "file_path": "` + real + `", "content": "x"}`,
	})
	require.NoError(t, err)
	assert.True(t, resp.IsError, "conflicting keys must be rejected, not guessed")

	_, decoyErr := os.Stat(decoy)
	assert.True(t, os.IsNotExist(decoyErr), "the decoy path must never be written")
}
