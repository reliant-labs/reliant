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
