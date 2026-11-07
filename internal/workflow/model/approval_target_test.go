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
		// Lookalikes: the method and the service must be whole path segments.
		"https://api.reliantlabs.io/reliant.v1.ApprovalService/ApproveEvil":      false,
		"https://api.reliantlabs.io/reliant.v1.ApprovalService/ApproveSomething": false,
		"https://api.reliantlabs.io/reliant.v1.ApprovalService/Denylist":         false,
		"https://api.reliantlabs.io/reliant.v1.ApprovalServiceX/Approve":         false,
		"https://api.reliantlabs.io/evilreliant.v1.ApprovalService/Approve":      false,
		"echo see /reliant.v1.ApprovalService/ApproveAll docs":                   false,
		// Embedded text: the service named in a query or fragment is not the target.
		"https://example.com/proxy?next=/reliant.v1.ApprovalService/Approve": false,
		"https://example.com/docs#reliant.v1.ApprovalService/Deny":           false,
		// Real targets inside a command, with a query, or a templated method.
		"https://api.reliantlabs.io/reliant.v1.ApprovalService/Approve?trace=1":            true,
		"https://api.reliantlabs.io/reliant.v1.ApprovalService/Deny/":                      true,
		`curl -s "$API/reliant.v1.ApprovalService/BatchDeny" -H 'Authorization: Bearer x'`: true,
		"curl --url=https://h/reliant.v1.ApprovalService/Approve":                          true,
		"curl $API/reliant.v1.ApprovalService/$METHOD":                                     true,
		"x=$(curl -sf https://h/reliant.v1.ApprovalService/Approve)&&echo ok":              true,
		"https://api.reliantlabs.io/reliant.v1.ApprovalService/":                           true,
	} {
		if got := TargetsApprovalDecision(text); got != want {
			t.Errorf("TargetsApprovalDecision(%q) = %v, want %v", text, got, want)
		}
	}
}
