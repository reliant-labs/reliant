// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"strings"
	"testing"
)

func TestCheckOutputSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		toolName    string
		output      string
		expectError bool
		expectTrunc bool
	}{
		{
			name:        "small output",
			toolName:    ShellToolName,
			output:      "hello world",
			expectError: false,
			expectTrunc: false,
		},
		{
			name:        "exact limit",
			toolName:    ShellToolName,
			output:      strings.Repeat("x", MaxOutputSize),
			expectError: false,
			expectTrunc: false,
		},
		{
			name:        "over limit",
			toolName:    ShellToolName,
			output:      strings.Repeat("x", MaxOutputSize+1),
			expectError: true,
			expectTrunc: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, truncated, err := CheckOutputSize(tt.toolName, tt.output)

			if tt.expectError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if tt.expectTrunc != truncated {
				t.Errorf("expected truncated=%v but got %v", tt.expectTrunc, truncated)
			}
			if !tt.expectError && output != tt.output {
				t.Errorf("output was modified when it shouldn't have been")
			}
		})
	}
}

func TestTruncateOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		toolName string
		output   string
		expected string
	}{
		{
			name:     "bash output truncation",
			toolName: ShellToolName,
			output:   strings.Repeat("line\n", 25000), // Large output
			expected: func() string {
				// Should keep beginning and end
				output := strings.Repeat("line\n", 25000)
				if len(output) > MaxOutputSize {
					keepSize := MaxOutputSize - 500
					headSize := keepSize * 3 / 4
					tailSize := keepSize / 4
					return output[:headSize] +
						"\n\n... [OUTPUT TRUNCATED - Showing first " + strings.TrimSpace(strings.Repeat("0", 0)) + // This is a simplification
						" and last " + strings.TrimSpace(strings.Repeat("0", 0)) + " bytes] ...\n\n" +
						output[len(output)-tailSize:]
				}
				return output
			}(),
		},
		{
			name:     "grep output truncation",
			toolName: "grep",
			output:   strings.Repeat("file.txt:line content\n", 10000),
			expected: func() string {
				// Should truncate at line boundaries
				output := strings.Repeat("file.txt:line content\n", 10000)
				if len(output) <= MaxOutputSize {
					return output
				}
				// This is complex to calculate exactly, but it should end with truncation message
				return output // simplified for test
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TruncateOutput(tt.toolName, tt.output, false)

			// Just verify it's truncated to the right size
			if len(result) > MaxOutputSize {
				t.Errorf("truncated output (%d bytes) exceeds max size (%d bytes)",
					len(result), MaxOutputSize)
			}

			// Verify truncation message is present for large outputs
			if len(tt.output) > MaxOutputSize {
				// Should contain either "TRUNCATED" or "omitted" indicating truncation
				if !strings.Contains(result, "TRUNCATED") && !strings.Contains(result, "omitted") {
					t.Error("truncation message not found in truncated output")
				}
			}
		})
	}
}

func TestGetToolSpecificSuggestions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		toolName    string
		expectCount int // minimum expected suggestions
	}{
		{ShellToolName, 3},
		{"view", 3},
		{"ls", 2},
		{"fetch", 2},
		{"unknown", 2},
	}

	for _, tt := range tests {
		t.Run(tt.toolName, func(t *testing.T) {
			suggestions := getToolSpecificSuggestions(tt.toolName)
			if len(suggestions) < tt.expectCount {
				t.Errorf("expected at least %d suggestions for %s, got %d",
					tt.expectCount, tt.toolName, len(suggestions))
			}
		})
	}
}

func TestOutputLimitError(t *testing.T) {
	t.Parallel()
	err := &OutputLimitError{
		ToolName:   ShellToolName,
		OutputSize: 60000,
		MaxSize:    MaxOutputSize,
		Suggestions: []string{
			"Use head or tail to limit output",
			"Redirect verbose output to /dev/null",
		},
	}

	errStr := err.Error()
	if !strings.Contains(errStr, ShellToolName) {
		t.Error("error message doesn't contain tool name")
	}
	if !strings.Contains(errStr, "60000") {
		t.Error("error message doesn't contain output size")
	}
	if !strings.Contains(errStr, "head or tail") {
		t.Error("error message doesn't contain suggestions")
	}
}

func TestViewToolTruncation(t *testing.T) {
	t.Parallel()
	// View tool should use head+tail truncation like bash
	largeOutput := strings.Repeat("line of code\n", 5000)
	result := TruncateOutput("view", largeOutput, true)

	// Should contain truncation message
	if !strings.Contains(result, "bytes omitted") {
		t.Error("view truncation should indicate bytes omitted")
	}

	// Should contain offset hint in warning
	if !strings.Contains(result, "offset") {
		t.Error("view truncation warning should mention offset parameter")
	}

	// Should be within the ceiling that applies to THIS tool (view carries a
	// larger one than the shared MaxOutputSize).
	if len(result) > outputCeilingFor(ViewToolName)+1000 { // overhead for the warning
		t.Errorf("truncated output (%d bytes) exceeds max size", len(result))
	}
}

func TestViewSuggestionsExist(t *testing.T) {
	t.Parallel()
	suggestions := getToolSpecificSuggestions("view")
	if len(suggestions) < 2 {
		t.Errorf("expected at least 2 suggestions for view, got %d", len(suggestions))
	}

	// Should mention offset parameter
	found := false
	for _, s := range suggestions {
		if strings.Contains(strings.ToLower(s), "offset") {
			found = true
			break
		}
	}
	if !found {
		t.Error("view suggestions should mention offset parameter")
	}
}

// A skill that fits must pass through untouched — no notice, no reformatting.
func TestCapSkillContent_UnderBudgetIsUntouched(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("a", MaxSkillBodySize)
	got, truncated := CapSkillContent(body)
	if truncated {
		t.Fatalf("skill exactly at budget must not be truncated")
	}
	if got != body {
		t.Fatalf("under-budget skill must be returned verbatim")
	}
}

// Dropping content must be announced in the delivered text. The tail of a skill
// load is where the tool appends its sub-skill / related-skill / suggested-tools
// pointers, so a silent cut severs the links to the rest of the skill tree.
func TestCapSkillContent_OverBudgetIsMarkedAndFits(t *testing.T) {
	t.Parallel()
	// Size the fixture FROM the budget rather than to a fixed line count: a
	// literal count silently stops exercising truncation the moment the
	// ceiling is raised, and the test then passes while proving nothing.
	const filler = "filler line of skill guidance\n"
	reps := MaxSkillBodySize/len(filler) + 10
	body := "HEAD MARKER\n" + strings.Repeat(filler, reps) + "TAIL MARKER\n"
	if len(body) <= MaxSkillBodySize {
		t.Fatalf("fixture must exceed the budget, got %d for a %d-byte budget", len(body), MaxSkillBodySize)
	}

	got, truncated := CapSkillContent(body)
	if !truncated {
		t.Fatalf("over-budget skill must report truncation")
	}
	if len(got) > MaxSkillBodySize {
		t.Fatalf("capped skill is %d bytes, over the %d budget", len(got), MaxSkillBodySize)
	}
	if !strings.Contains(got, "HEAD MARKER") {
		t.Fatalf("head of the skill must survive")
	}
	if strings.Contains(got, "TAIL MARKER") {
		t.Fatalf("tail should have been dropped by the cap")
	}
	// The notice must name the tool parameters that fetch the remainder. It
	// used to point at SKILL.md on disk, which the reader often cannot open —
	// skills arrive through the config pipeline, not the filesystem.
	for _, want := range []string{"SKILL TRUNCATED", "END SKILL TRUNCATION NOTICE", "offset=", "section="} {
		if !strings.Contains(got, want) {
			t.Fatalf("truncation notice must contain %q; got tail:\n%s", want, got[len(got)-800:])
		}
	}
	// The reported byte counts must be the real ones, not an estimate.
	if !strings.Contains(got, fmt.Sprintf("OF %d BYTES", len(body))) {
		t.Fatalf("notice must state the true original size %d", len(body))
	}
}

// The notice length varies with the digits of the numbers it reports, so the
// fitting loop has to converge for any input size, never overshooting.
func TestCapSkillContent_FitsAtEveryOversizeBoundary(t *testing.T) {
	t.Parallel()
	for _, extra := range []int{1, 9, 99, 999, 9_999, 99_999, 1_000_000} {
		body := strings.Repeat("x", MaxSkillBodySize+extra)
		got, truncated := CapSkillContent(body)
		if !truncated {
			t.Fatalf("size %d must truncate", len(body))
		}
		if len(got) > MaxSkillBodySize {
			t.Fatalf("size %d capped to %d, over the %d budget", len(body), len(got), MaxSkillBodySize)
		}
	}
}

// TruncateOutput must route the skill tool to the skill-aware cap rather than
// the generic tail cut, so both skill-delivery paths emit identical bytes.
func TestTruncateOutput_SkillUsesSkillCap(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("skill content line\n", 2000)
	viaTruncate := TruncateOutput(ToolSkill, body, true)
	viaCap, _ := CapSkillContent(body)
	if viaTruncate != viaCap {
		t.Fatalf("TruncateOutput(skill) must delegate to CapSkillContent")
	}
	if strings.Contains(viaTruncate, "OUTPUT TRUNCATED") {
		t.Fatalf("skill output must not fall through to the generic truncation marker")
	}
}

// A file read is ONE artifact the agent named, so a cut middle is a wrong
// answer rather than a smaller one. view therefore delivers up to MaxReadSize
// while every other tool keeps the shared MaxOutputSize ceiling.
//
// This pins the WRAPPER half. Raising MaxReadSize alone is a no-op: the
// wrapper re-truncates every result at the shared ceiling, so a 52KB read
// would still have arrived cut at 24KB — which is exactly what drove eleven
// fan-out agents to page a generated proto by `grep`, one message at a time.
func TestOutputCeiling_ViewReadsMoreThanOtherTools(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("x", 40_000) // over MaxOutputSize, under MaxReadSize

	t.Run("view delivers it whole", func(t *testing.T) {
		if got := TruncateOutput(ViewToolName, big, true); len(got) != len(big) {
			t.Errorf("view truncated a %d-byte read to %d; it must deliver up to MaxReadSize (%d)",
				len(big), len(got), MaxReadSize)
		}
		if _, truncated, err := CheckOutputSize(ViewToolName, big); truncated || err != nil {
			t.Errorf("CheckOutputSize rejected a %d-byte view result; ceiling is %d", len(big), MaxReadSize)
		}
	})

	t.Run("shell still capped at the shared ceiling", func(t *testing.T) {
		if got := TruncateOutput("bash", big, true); len(got) > MaxOutputSize {
			t.Errorf("bash output was %d bytes; it must stay within MaxOutputSize (%d) — "+
				"command output volume is not something the agent chose", len(got), MaxOutputSize)
		}
	})

	t.Run("a read past MaxReadSize still truncates", func(t *testing.T) {
		huge := strings.Repeat("y", MaxReadSize+10_000)
		if got := TruncateOutput(ViewToolName, huge, true); len(got) > MaxReadSize {
			t.Errorf("view delivered %d bytes, past its own %d ceiling", len(got), MaxReadSize)
		}
	})
}
