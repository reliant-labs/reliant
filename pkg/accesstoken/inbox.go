package accesstoken

// ── Inbox and approvals ───────────────────────────────────────────────
//
// inbox:read, inbox:manage, approvals:decide and questions:answer let an SDK
// caller act on a person's inbox without holding the person's whole API
// (reliant:api). Like the runs and triggers scopes, this package knows no rpc
// and imports no Reliant code; the table below is the contract a Reliant API
// enforces with [RequireScope].
//
//	InboxService
//	  ListInbox                                        inbox:read
//	  DismissInboxItem, RestoreInboxItem               inbox:manage
//	ApprovalService
//	  Approve, Deny, BatchApprove, BatchDeny           approvals:decide
//	  ListApprovalsByChat                              no narrow scope yet
//	QuestionService
//	  AnswerQuestion                                   questions:answer
//
// WHICH SCOPE SATISFIES WHICH ([Set.Permits]):
//
//   - reliant:api permits all four, so an existing token keeps every
//     permission it had. Its stored grant is not widened.
//   - inbox:manage implies inbox:read, so a caller that dismisses an item can
//     list the inbox it dismisses from.
//   - approvals:decide and questions:answer imply nothing and are implied by
//     nothing: deciding an approval is a different authority from seeing it,
//     and answering a question is not approving a tool call.
//
// ACCESS IS GATED BY PERMISSION, NOT BY WHO HOLDS THE TOKEN. This package
// cannot tell an agent from a person; that a run must not decide an approval
// it raised is a check the Reliant API makes with the credential's identity.
//
// All four act as a person ([RequiresActingUser]) and cannot be bound to a
// resource.

// InboxScopes are the four scopes above, narrowest first.
var InboxScopes = []Scope{
	ScopeInboxRead,
	ScopeInboxManage,
	ScopeApprovalsDecide,
	ScopeQuestionsAnswer,
}

func isInboxScope(s Scope) bool {
	for _, known := range InboxScopes {
		if known == s {
			return true
		}
	}
	return false
}
