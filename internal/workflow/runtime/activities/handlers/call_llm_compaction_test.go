package handlers

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
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
// executeCore: an explicit positive arg wins outright; otherwise the resolved
// model's DefaultCompactionThreshold wins; otherwise the global default.
func TestEffectiveCompactionThresholdPrecedence(t *testing.T) {
	tests := []struct {
		name         string
		arg          *reliantv1.CelInt
		modelDefault *int64
		want         int32
	}{
		{name: "explicit arg beats model default", arg: celIntLiteral(300000), modelDefault: ptrInt64(900000), want: 300000},
		{name: "model default when arg unset", arg: nil, modelDefault: ptrInt64(900000), want: 900000},
		{name: "1M model returns its large default", arg: celIntLiteral(0), modelDefault: ptrInt64(950000), want: 950000},
		{name: "global default when arg unset and no model default", arg: nil, modelDefault: nil, want: DefaultCompactionThreshold},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := &reliantv1.CallLLMArgs{CompactionThreshold: tc.arg}

			// Mirror executeCore's precedence.
			effective := explicitCompactionThresholdArg(args)
			if !explicitCompactionThresholdIsSet(args) && tc.modelDefault != nil {
				effective = int32(*tc.modelDefault)
			}

			if effective != tc.want {
				t.Errorf("effective compaction threshold = %d, want %d", effective, tc.want)
			}
		})
	}
}

func ptrInt64(v int64) *int64 { return &v }
