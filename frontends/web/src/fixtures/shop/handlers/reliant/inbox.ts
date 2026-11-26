// Copyright (c) 2025 Reliant Labs

/**
 * InboxService: what is waiting on Alex, all of it in shop, and all of it
 * what the rest of the story already shows.
 *
 *   approval    the fixture chat's shell call (`git push`), waiting for
 *               approval in its transcript (chats.ts)
 *   questions   the fixture chat's async question (the agent kept working),
 *               and the rate-limit chat's blocking one (it is awaiting input)
 *   automation  Nightly CI triage, failing two nights in a row
 *               (workflows.ts; its last run and chat show the failure)
 *
 * So the nav badge counts 2 (the approval and the blocking question) and
 * shows the dot (the async question and the failing automation).
 */
import { create } from "@bufbuild/protobuf";

import { InboxCategory, InboxItemKind, InboxItemSchema, InboxService, type InboxItem } from "@/gen/reliant/v1/inbox_pb";

import { HOUR } from "../../shopStory";
import { CHAT_IDS, CHAT_TITLES, PROJECT_IDS, isoAgo } from "../../workspace";
import { serve } from "../serve";
import { cartTotalsApproval, cartTotalsQuestion, rateLimitQuestion } from "./chats";
import { NIGHTLY_FAILURE, TRIGGER_IDS, nightlyHealth } from "./workflows";

const SHOP = { projectId: PROJECT_IDS.shop, projectName: "shop" } as const;

/** The first question's text of an ask_user metadata blob. */
const promptOf = (metadata: string) => (JSON.parse(metadata) as { questions: { question: string }[] }).questions[0]?.question ?? "";

/** Every item, in the server's order: kind (priority), then longest-waiting first. */
function inboxItems(): InboxItem[] {
  const approval = cartTotalsApproval();
  const cartQuestion = cartTotalsQuestion();
  const rateQuestion = rateLimitQuestion();
  return [
    create(InboxItemSchema, {
      kind: InboxItemKind.APPROVAL, category: InboxCategory.NEEDS_YOU, itemId: `approval:${approval.id}`, chatId: approval.chatId, runId: approval.chatId, ...SHOP,
      workflowName: "builtin://get-it-right", chatTitle: CHAT_TITLES.cartTotals, waitingSince: approval.createdAt,
      payload: { case: "approval", value: { approvalId: approval.id, approvalType: approval.approvalType, title: approval.title, batchId: "" } },
    }),
    create(InboxItemSchema, {
      kind: InboxItemKind.QUESTION, category: InboxCategory.NEEDS_YOU, itemId: `question:${cartQuestion.questionId}`, chatId: cartQuestion.chatId, runId: cartQuestion.chatId, ...SHOP,
      workflowName: "builtin://get-it-right", chatTitle: CHAT_TITLES.cartTotals, waitingSince: cartQuestion.createdAt,
      payload: { case: "question", value: { questionId: cartQuestion.questionId, threadId: cartQuestion.threadId, prompt: promptOf(cartQuestion.metadata), metadata: cartQuestion.metadata, async: true } },
    }),
    create(InboxItemSchema, {
      kind: InboxItemKind.QUESTION, category: InboxCategory.NEEDS_YOU, itemId: `question:${rateQuestion.questionId}`, chatId: rateQuestion.chatId, runId: rateQuestion.chatId, ...SHOP,
      workflowName: "builtin://agent", chatTitle: CHAT_TITLES.rateLimit, waitingSince: rateQuestion.createdAt,
      payload: { case: "question", value: { questionId: rateQuestion.questionId, threadId: rateQuestion.threadId, prompt: promptOf(rateQuestion.metadata), metadata: rateQuestion.metadata, async: false } },
    }),
    create(InboxItemSchema, {
      kind: InboxItemKind.AUTOMATION_FAILING, category: InboxCategory.AUTOMATION, itemId: `automation_failing:${TRIGGER_IDS.nightly}:shop-trgev-nightly-1`, ...SHOP,
      triggerId: TRIGGER_IDS.nightly, triggerName: "Nightly CI triage", workflowName: "nightly-ci-triage", waitingSince: isoAgo(9 * HOUR),
      payload: {
        case: "automationFailing",
        value: { health: nightlyHealth(), lastRunChatId: CHAT_IDS.nightlyCi, failureClass: "execution_error", detail: NIGHTLY_FAILURE },
      },
    }),
  ];
}

/** The items blocking a live run: approvals, blocking questions, waiting for a machine. */
const blocks = (item: InboxItem) =>
  item.kind === InboxItemKind.APPROVAL || item.kind === InboxItemKind.WAITING_FOR_MACHINE || (item.payload.case === "question" && !item.payload.value.async);

export function inboxHandlers() {
  return serve(InboxService, {
    // Scoped to one project when asked (docs has nothing waiting); `limit: 0` is the badge's counts-only read.
    // `categories` (a machine's page asks for housekeeping and alerts) lists exactly those: shop has none.
    listInbox: (req) => {
      const all = req.categories.length ? inboxItems().filter((item) => req.categories.includes(item.category)) : inboxItems();
      const items = req.projectId ? all.filter((item) => item.projectId === req.projectId) : all;
      const limit = req.limit ?? 100;
      return {
        items: items.slice(0, limit), blockingCount: items.filter(blocks).length, hasInformational: items.some((item) => !blocks(item)),
        truncated: items.length > limit && limit > 0, otherProjectsCount: req.projectId ? all.length - items.length : 0,
      };
    },
  });
}
