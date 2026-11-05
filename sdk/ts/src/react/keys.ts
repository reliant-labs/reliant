// Query keys of the reads the hooks make. They are built by the same factory
// forge's generated hooks use (service-hooks serviceKeys), under the same
// service names, so an app that also has generated hooks for these services
// shares one cache entry per request instead of two.

import { serviceKeys } from "@reliantlabs/forge-web-runtime/service-hooks";
import { ListApprovalsByChatRequestSchema } from "#gen/reliant/v1/approval_pb.js";
import { ListInboxRequestSchema } from "#gen/reliant/v1/inbox_pb.js";
import { ListAsyncQuestionsRequestSchema, ListPendingQuestionsRequestSchema } from "#gen/reliant/v1/question_pb.js";
import { GetRunRequestSchema } from "#gen/reliant/v1/run_pb.js";

const approval = serviceKeys("approvalService");
const question = serviceKeys("questionService");
const inbox = serviceKeys("inboxService");
const run = serviceKeys("runService");

export const approvalKeys = {
  /** Every ApprovalService read; a write invalidates this. */
  all: approval.all,
  listByChat: approval.query("listApprovalsByChat", ListApprovalsByChatRequestSchema),
};

export const questionKeys = {
  all: question.all,
  listPending: question.query("listPendingQuestions", ListPendingQuestionsRequestSchema),
  listAsync: question.query("listAsyncQuestions", ListAsyncQuestionsRequestSchema),
};

export const inboxKeys = {
  all: inbox.all,
  list: inbox.query("listInbox", ListInboxRequestSchema),
};

export const runKeys = {
  all: run.all,
  get: run.query("getRun", GetRunRequestSchema, "run"),
};
