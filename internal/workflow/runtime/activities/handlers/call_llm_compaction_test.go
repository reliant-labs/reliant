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

// TestEffectiveCompactionThresholdPrecedence documents the layering applied in
// executeCore: an explicit positive per-node arg wins outright; otherwise the
// threshold is DERIVED from the resolved model's real context window (with a
// per-model explicit override honored if one is declared); otherwise the global
// default when the window is unknown.
func TestEffectiveCompactionThresholdPrecedence(t *testing.T) {
	tests := []struct {
		name string
		arg  *reliantv1.CelInt
		def  *models.ModelDefinition
		want int32
	}{
		{
			name: "explicit per-node arg beats derived value",
			arg:  celIntLiteral(300000),
			def:  &models.ModelDefinition{Capabilities: models.ModelCapabilities{MaxContextWindow: 1_000_000}},
			want: 300000,
		},
		{
			name: "1M-window model derives 850k when arg unset",
			arg:  nil,
			def:  &models.ModelDefinition{Capabilities: models.ModelCapabilities{MaxContextWindow: 1_000_000}},
			want: 850_000,
		},
		{
			name: "200k-window model derives 170k when arg unset",
			arg:  celIntLiteral(0),
			def:  &models.ModelDefinition{Capabilities: models.ModelCapabilities{MaxContextWindow: 200_000}},
			want: 170_000,
		},
		{
			name: "per-model override wins over derivation",
			arg:  nil,
			def:  &models.ModelDefinition{Capabilities: models.ModelCapabilities{MaxContextWindow: 1_000_000}, DefaultCompactionThreshold: ptrInt(950000)},
			want: 950000,
		},
		{
			name: "global default when arg unset and window unknown",
			arg:  nil,
			def:  &models.ModelDefinition{Capabilities: models.ModelCapabilities{MaxContextWindow: 0}},
			want: DefaultCompactionThreshold,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := &reliantv1.CallLLMArgs{CompactionThreshold: tc.arg}

			// Mirror executeCore's precedence for a resolved definition.
			effective := explicitCompactionThresholdArg(args)
			if !explicitCompactionThresholdIsSet(args) {
				effective = int32(models.CompactionThresholdForDefinition(tc.def))
			}

			if effective != tc.want {
				t.Errorf("effective compaction threshold = %d, want %d", effective, tc.want)
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
