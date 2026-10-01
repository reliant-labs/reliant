// Copyright (c) 2025 Reliant Labs

package geminiwire

import "testing"

func TestStepNeedsSignatureStandIn(t *testing.T) {
	cases := []struct {
		name       string
		callSigned []bool
		want       bool
	}{
		{"no calls", nil, false},
		{"single unsigned call", []bool{false}, true},
		{"parallel unsigned calls", []bool{false, false, false}, true},
		{"single signed call", []bool{true}, false},
		{"Gemini's own parallel shape: first signed", []bool{true, false}, false},
		{"real signature on a later call", []bool{false, true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StepNeedsSignatureStandIn(tc.callSigned); got != tc.want {
				t.Errorf("StepNeedsSignatureStandIn(%v) = %v, want %v", tc.callSigned, got, tc.want)
			}
		})
	}
}

// The stand-in must be the exact documented literal: the server matches the
// string, and anything else is validated as a real (and therefore invalid)
// signature.
func TestSkipThoughtSignatureValidatorLiteral(t *testing.T) {
	if SkipThoughtSignatureValidator != "skip_thought_signature_validator" {
		t.Fatalf("SkipThoughtSignatureValidator = %q, want the documented literal", SkipThoughtSignatureValidator)
	}
}
