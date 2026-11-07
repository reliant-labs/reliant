// Copyright (c) 2025 Reliant Labs
package model

import (
	"net/url"
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

// approvalServiceSegment is ApprovalService's path segment, lowercased.
const approvalServiceSegment = "reliant.v1.approvalservice"

// approvalDecideMethods are the ApprovalService rpcs that decide an approval,
// lowercased.
var approvalDecideMethods = map[string]bool{"approve": true, "deny": true, "batchapprove": true, "batchdeny": true}

// TargetsApprovalDecision reports whether text (an http/request@1 url, or a
// shell command) targets an ApprovalService rpc that approves or denies, or
// leaves the rpc to a template ("…/reliant.v1.ApprovalService/{{ x }}",
// "…/$METHOD"). Each word of text is parsed as a target and its path is
// read segment by segment: the service and the method must each be a whole
// segment, so a lookalike (ApproveEvil, ApprovalServiceX) or the service
// named only in a query string is not a decision. A host written as a
// template still matches, since the path is what names the service.
func TargetsApprovalDecision(text string) bool {
	for _, word := range strings.FieldsFunc(text, isTargetSeparator) {
		if pathDecidesApproval(targetPath(word)) {
			return true
		}
	}
	return false
}

// isTargetSeparator splits a shell command into words a target can be: space,
// quotes and the shell's control characters.
func isTargetSeparator(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\'', '"', '`', ';', '|', '&', '(', ')', '<', '>':
		return true
	}
	return false
}

// targetPath is word's path: the parsed URL's when word parses as one (so a
// query or fragment is not read as path), else word up to its query or
// fragment.
func targetPath(word string) string {
	if u, err := url.Parse(word); err == nil && u.Opaque == "" {
		return u.Path
	}
	if i := strings.IndexAny(word, "?#"); i >= 0 {
		return word[:i]
	}
	return word
}

// pathDecidesApproval reports whether path holds the ApprovalService segment
// followed by a deciding method, a templated method, or nothing (the method
// is appended elsewhere).
func pathDecidesApproval(path string) bool {
	segs := strings.Split(strings.ToLower(path), "/")
	for i, seg := range segs {
		if seg != approvalServiceSegment {
			continue
		}
		if i+1 == len(segs) {
			return true
		}
		method := segs[i+1]
		if method == "" || strings.HasPrefix(method, "{{") || strings.HasPrefix(method, "$") || approvalDecideMethods[method] {
			return true
		}
	}
	return false
}

// ActionURL is the literal text of an action's with.url, "" when it has none.
func ActionURL(a *reliantv1.ActionArgs) string {
	return a.GetWith()["url"].GetStringValue()
}
