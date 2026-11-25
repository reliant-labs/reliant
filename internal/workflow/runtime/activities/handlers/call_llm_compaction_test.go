package handlers

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

func celIntLiteral(v int64) *reliantv1.CelInt {
	return &reliantv1.CelInt{Value: &reliantv1.CelInt_Literal{Literal: v}}
}

func celIntExpr(e string) *reliantv1.CelInt {
	return &reliantv1.CelInt{Value: &reliantv1.CelInt_Expr{Expr: e}}
}

func TestExplicitCompactionThreshold(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		arg       *reliantv1.CelInt
		wantIsSet bool
		wantValue int32
	}{
		{name: "unset uses global default", arg: nil, wantIsSet: false, wantValue: DefaultCompactionThreshold},
		{name: "zero literal treated as unset", arg: celIntLiteral(0), wantIsSet: false, wantValue: DefaultCompactionThreshold},
		{name: "negative literal treated as unset", arg: celIntLiteral(-5), wantIsSet: false, wantValue: DefaultCompactionThreshold},
		{name: "positive literal is explicit", arg: celIntLiteral(250000), wantIsSet: true, wantValue: 250000},
		{name: "unresolved expr treated as unset", arg: celIntExpr("inputs.compaction_threshold"), wantIsSet: false, wantValue: DefaultCompactionThreshold},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := &reliantv1.CallLLMArgs{CompactionThreshold: tc.arg}

			if got := explicitCompactionThresholdIsSet(args); got != tc.wantIsSet {
				t.Errorf("explicitCompactionThresholdIsSet = %v, want %v", got, tc.wantIsSet)
			}
			if got := explicitCompactionThresholdArg(args); got != tc.wantValue {
				t.Errorf("explicitCompactionThresholdArg = %d, want %d", got, tc.wantValue)
			}
		})
	}
}

// TestEffectiveCompactionThresholdPrecedence checks the derived default and
// that explicit overrides above 600k remain valid below the pin ceiling.
func TestEffectiveCompactionThresholdPrecedence(t *testing.T) {
	t.Parallel()

	large := &models.ModelDefinition{Capabilities: models.ModelCapabilities{MaxContextWindow: 1_000_000}}
	custom := &models.ModelDefinition{
		Capabilities:               large.Capabilities,
		DefaultCompactionThreshold: ptrInt(950_000),
	}
	tests := []struct {
		name        string
		arg         *reliantv1.CelInt
		selector    *reliantv1.CelModelSelector
		tag         int32
		def         *models.ModelDefinition
		promptLimit int64
		want        int32
	}{
		{name: "node pin beats selector and tag", arg: celIntLiteral(800_000), selector: selectorWithThreshold(750_000), tag: 700_000, def: large, promptLimit: 1_000_000, want: 800_000},
		{name: "selector pin beats tag", selector: selectorWithThreshold(750_000), tag: 700_000, def: large, promptLimit: 1_000_000, want: 750_000},
		{name: "tag pin beats derived default", tag: 700_000, def: large, promptLimit: 1_000_000, want: 700_000},
		{name: "large model defaults to 600k", def: large, promptLimit: 1_000_000, want: 600_000},
		{name: "injected resolver with known ceiling defaults to 600k", promptLimit: 1_000_000, want: 600_000},
		{name: "explicit per-model override beats derived default", def: custom, promptLimit: 1_000_000, want: 950_000},
		{name: "unknown ceiling uses fallback", want: 185_000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := &reliantv1.CallLLMArgs{CompactionThreshold: tc.arg, Model: tc.selector}
			if got := resolveCompactionThreshold(args, tc.tag, tc.def, "codex", tc.promptLimit); got != tc.want {
				t.Errorf("effective compaction threshold = %d, want %d", got, tc.want)
			}
		})
	}
}

func ptrInt(v int) *int { return &v }

func selectorWithThreshold(ct int32) *reliantv1.CelModelSelector {
	return &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Literal{
		Literal: &reliantv1.ModelSelector{Tags: []string{"moderate"}, CompactionThreshold: &ct},
	}}
}

// TestResolveCompactionThreshold_PinnedNeverExceedsTheWindow is the prod
// incident of 2026-10-09: chat d221a691's model selector was
// {"compaction_threshold":1000000,"tags":["moderate"]}, and `moderate` resolved
// to gpt-5.6-terra (codex, 272k window). The pinned 1M was honored verbatim, so
// thread_token_count (500k-700k) never crossed it, compaction never ran in four
// hours, and the trim backstop shredded the conversation on every turn instead.
//
// A pinned threshold chooses WHEN to compact; it cannot be past the point where
// the model runs out of room. It is capped at the derived threshold for the
// model's real window. A pin below that is honored as before.
func TestResolveCompactionThreshold_PinnedNeverExceedsTheWindow(t *testing.T) {
	t.Parallel()

	terra := &models.ModelDefinition{Capabilities: models.ModelCapabilities{MaxContextWindow: 272_000}}
	const terraCeiling = 231_200 // 0.85 × 272k

	tests := []struct {
		name   string
		args   *reliantv1.CallLLMArgs
		tag    int32
		def    *models.ModelDefinition
		window int64
		want   int32
	}{
		{
			name:   "incident: selector pins 1M on a 272k model",
			args:   &reliantv1.CallLLMArgs{Model: selectorWithThreshold(1_000_000)},
			def:    terra,
			window: 272_000,
			want:   terraCeiling,
		},
		{
			name:   "node arg pinned past the window",
			args:   &reliantv1.CallLLMArgs{CompactionThreshold: celIntLiteral(900_000)},
			def:    terra,
			window: 272_000,
			want:   terraCeiling,
		},
		{
			name:   "tag preference pinned past the window",
			args:   &reliantv1.CallLLMArgs{},
			tag:    500_000,
			def:    terra,
			window: 272_000,
			want:   terraCeiling,
		},
		{
			name:   "injected resolver (no definition) still capped by the model's window",
			args:   &reliantv1.CallLLMArgs{Model: selectorWithThreshold(1_000_000)},
			window: 200_000,
			want:   170_000,
		},
		{
			name:   "pin below the ceiling is honored",
			args:   &reliantv1.CallLLMArgs{Model: selectorWithThreshold(150_000)},
			def:    terra,
			window: 272_000,
			want:   150_000,
		},
		{
			name:   "1M pin on a 1M-window model is capped at 850k",
			args:   &reliantv1.CallLLMArgs{Model: selectorWithThreshold(1_000_000)},
			def:    &models.ModelDefinition{Capabilities: models.ModelCapabilities{MaxContextWindow: 1_000_000}},
			window: 1_000_000,
			want:   850_000,
		},
		{
			name:   "unknown window: a pin has nothing to be capped against",
			args:   &reliantv1.CallLLMArgs{Model: selectorWithThreshold(1_000_000)},
			window: 0,
			want:   1_000_000,
		},
		{
			name:   "unpinned still derives from the window",
			args:   &reliantv1.CallLLMArgs{},
			def:    terra,
			window: 272_000,
			want:   terraCeiling,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveCompactionThreshold(tc.args, tc.tag, tc.def, "codex", tc.window)
			if got != tc.want {
				t.Errorf("resolveCompactionThreshold = %d, want %d", got, tc.want)
			}
		})
	}
}
