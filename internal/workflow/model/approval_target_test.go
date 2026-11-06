// Copyright (c) 2025 Reliant Labs
package model

import "testing"

func TestTargetsApprovalDecision(t *testing.T) {
	for text, want := range map[string]bool{
		"https://api.reliantlabs.io/reliant.v1.ApprovalService/Approve":       true,
		"https://api.reliantlabs.io/reliant.v1.ApprovalService/Deny":          true,
		"https://api.reliantlabs.io/reliant.v1.ApprovalService/BatchApprove":  true,
		"https://api.reliantlabs.io/reliant.v1.ApprovalService/BatchDeny":     true,
		"https://api.reliantlabs.io/reliant.v1.approvalservice/approve":       true,
		"{{ inputs.api }}/reliant.v1.ApprovalService/{{ inputs.method }}":     true,
		"curl -X POST $API/reliant.v1.ApprovalService/Approve -d '{}'":        true,
		"https://api.reliantlabs.io/reliant.v1.ApprovalService/ListApprovals": false,
		"https://api.reliantlabs.io/reliant.v1.ApprovalService/GetApproval":   false,
		"https://api.reliantlabs.io/reliant.v1.RunService/StartRun":           false,
		"https://example.com/approve":                                         false,
		"":                                                                    false,
	} {
		if got := TargetsApprovalDecision(text); got != want {
			t.Errorf("TargetsApprovalDecision(%q) = %v, want %v", text, got, want)
		}
	}
}
