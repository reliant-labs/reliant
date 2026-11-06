// Copyright (c) 2025 Reliant Labs
package model

import (
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// PermissionApprovalsDecide is the one value of a workflow's `permissions:`
// today: its steps may approve or deny requests through ApprovalService.
const PermissionApprovalsDecide = "approvals:decide"

// WorkflowPermissions are the values `permissions:` accepts.
var WorkflowPermissions = []string{PermissionApprovalsDecide}

// DeclaresPermission reports whether wf lists permission.
func DeclaresPermission(wf *reliantv1.Workflow, permission string) bool {
	for _, p := range wf.GetPermissions() {
		if p == permission {
			return true
		}
	}
	return false
}

const approvalServicePath = "reliant.v1.approvalservice/"

// approvalDecideMethods are the ApprovalService rpcs that decide an approval,
// lowercased.
var approvalDecideMethods = []string{"approve", "deny", "batchapprove", "batchdeny"}

// TargetsApprovalDecision reports whether text (an http/request@1 url, or a
// shell command) names an ApprovalService rpc that approves or denies, or
// leaves the rpc to a template ("…/reliant.v1.ApprovalService/{{ x }}"). A
// host written as a template still matches, since the path is what names
// the service.
func TargetsApprovalDecision(text string) bool {
	lower := strings.ToLower(text)
	for from := 0; ; {
		i := strings.Index(lower[from:], approvalServicePath)
		if i < 0 {
			return false
		}
		rest := lower[from+i+len(approvalServicePath):]
		if rest == "" || strings.HasPrefix(rest, "{{") {
			return true
		}
		for _, m := range approvalDecideMethods {
			if strings.HasPrefix(rest, m) {
				return true
			}
		}
		from += i + len(approvalServicePath)
	}
}

// ActionURL is the literal text of an action's with.url, "" when it has none.
func ActionURL(a *reliantv1.ActionArgs) string {
	return a.GetWith()["url"].GetStringValue()
}
