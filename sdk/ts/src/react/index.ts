// @reliantlabs/sdk/react — React hooks over /client.
//
// useChat / useThread / useRun read a chat live from its stream; useApprovals,
// useQuestions and useInbox read and settle what is waiting on a person. React
// and @tanstack/react-query are peers.

export { ReliantProvider, setDefaultReliantClient, useReliantClient } from "./context.js";
export { useChat, useThread, type UseChatOptions, type UseChatResult, type UseThreadResult } from "./useChat.js";
export { useRun, type UseRunResult } from "./useRun.js";
export {
  useApprovalActions,
  useApprovals,
  type UseApprovalActionsOptions,
  type UseApprovalActionsResult,
  type UseApprovalsOptions,
  type UseApprovalsResult,
} from "./useApprovals.js";
export {
  ApprovalStatus,
  ApprovalType,
  applyApprovalUpdate,
  approvalStatusFromFeed,
  createApprovalActions,
  approvalsFromCache,
  isApprovalNotActionable,
  notActionableMessage,
  patchApprovals,
  upsertApproval,
  type Approval,
  type ApprovalActions,
  type ApprovalActionsOptions,
  type ApprovalFields,
} from "./approvals.js";
export { useQuestions, type UseQuestionsOptions, type UseQuestionsResult } from "./useQuestions.js";
export { applyQuestionUpdate, resolveQuestion, type QuestionAnswers, type QuestionInfo } from "./questions.js";
export { useInbox, inboxInvalidatingUpdate, type InboxCounts, type InboxItem, type UseInboxOptions, type UseInboxResult } from "./useInbox.js";
export { approvalKeys, inboxKeys, questionKeys, runKeys } from "./keys.js";
