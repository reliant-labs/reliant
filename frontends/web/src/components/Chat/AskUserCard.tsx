// Copyright (c) 2025 Reliant Labs

/**
 * Every ask_user tool call, in the transcript. An ask_user call NEVER renders
 * as nothing: whatever the client can or cannot work out about it, the user
 * sees at least the question text.
 *
 * The question row is the authority, not the input. An async ask_user (the
 * default — any call without blocking: true; research/ASYNC_ASK_USER.md) has a
 * row marked async, keyed by this tool call's id, and renders here as a card
 * the user can answer whenever they like — never in the composer, which still
 * talks to the agent. The answer goes through ResolveQuestion, the same call
 * the Inbox's answer form makes, and the server delivers it to the asking
 * thread as a message from the user.
 *
 * Without an async row the card still shows what was asked:
 *   - posting: an async call whose row has not arrived yet;
 *   - waiting: a blocking call still running — the composer holds its form;
 *   - asked:   a finished call with no async row — it blocked (recorded
 *              before async became the default, or blocking: true) or ran
 *              unattended.
 *
 * Classifying from the input alone once hid questions entirely: a page still
 * running a bundle from before async became the default read an unflagged
 * call as blocking and dropped it from the transcript, while the server — for
 * which it was async — never put it in the composer either.
 *
 * Rendered inline in the transcript (ChatMessage), outside the tool rows and
 * even when tool rows are hidden, like RequestMachineCard: it is a question to
 * the user, not a record of work.
 *
 * Once settled — answered, or withdrawn by its agent — the card collapses to
 * one line: the question, then the answer (or "Withdrawn: <reason>"). A
 * settled question is history, and a transcript full of full-height answered
 * cards buries the conversation around them. The line is a button (44px,
 * aria-expanded) that opens the full card, whose header closes it again. An
 * unanswered card is always full: it is waiting on the user.
 */

import { memo, useId, useMemo, useState } from "react";
import { CheckCircle2, ChevronDown, ChevronRight, MessageCircleQuestion, Undo2 } from "lucide-react";
import { toast } from "sonner";
import { answerQuestion, useAsyncQuestions, type QuestionInfo } from "@/hooks/question-queries";
import { CardInset } from "../forge-ui/card";
import { cn } from "@/lib/utils";
import { QuestionPrompt, type QuestionAnswer } from "./QuestionPrompt";
import {
  askUserAnswerLines,
  askUserQuestionItems,
  askUserQuestionItemsFromInput,
  isAsyncAskUserInput,
} from "./askUserUtils";

interface AskUserCardProps {
  chatId?: string;
  /** The ask_user tool call; its question row carries the same id. */
  toolCallId: string;
  /** The tool call's input: the questions, shown before the row exists. */
  input: unknown;
  /** The tool call has its result: an async one's question row exists by then. */
  finished?: boolean;
}

export type AskUserCardMode = "async" | "posting" | "waiting" | "asked";

/**
 * Which card an ask_user call gets. Pure, so the table is testable without a
 * render: the async row wins; without one, the input and whether the call has
 * finished decide. `listed` is whether the chat's async questions have
 * loaded — until they have, a finished async call may still have a row.
 */
export function askUserCardMode(args: {
  question: QuestionInfo | undefined;
  input: unknown;
  finished: boolean;
  listed: boolean;
}): AskUserCardMode {
  if (args.question) return "async";
  if (!isAsyncAskUserInput(args.input)) return args.finished ? "asked" : "waiting";
  if (args.finished && args.listed) return "asked";
  return "posting";
}

const HEADINGS: Record<Exclude<AskUserCardMode, "async">, { title: string; status: string }> = {
  posting: {
    title: "Question for you · not blocking",
    status: "The agent kept working on its best guess. Answer any time — it gets your answer as a message.",
  },
  waiting: {
    title: "Question for you",
    status: "The agent is waiting for your answer. Answer it below.",
  },
  asked: {
    title: "Question asked",
    status: "The agent asked this earlier in the conversation.",
  },
};

function AskUserCardComponent({ chatId, toolCallId, input, finished = false }: AskUserCardProps) {
  const { data: asyncList } = useAsyncQuestions(chatId);
  const asyncQuestions = asyncList?.questions;
  const question = asyncQuestions?.find((q) => q.toolCallId === toolCallId);
  // The input as the model wrote it, else the row's metadata, which the
  // server has already normalized.
  const items = useMemo(
    () => askUserQuestionItemsFromInput(input) ?? askUserQuestionItems(question?.metadata),
    [input, question?.metadata],
  );
  // QuestionPrompt locks itself after submit; a remount (new key) re-arms it
  // when the answer could not be delivered.
  const [attempt, setAttempt] = useState(0);

  const mode = askUserCardMode({ question, input, finished, listed: asyncQuestions !== undefined });
  const resolved = mode === "async" && question?.status === "resolved";
  // Withdrawn by the agent (withdraw_question): moot, not answered.
  const withdrawnReason = resolved ? question?.withdrawnReason : undefined;
  const answers = resolved && !withdrawnReason ? askUserAnswerLines(question?.responseData) : [];
  // A settled card starts collapsed; the user can open it and close it again.
  const [expanded, setExpanded] = useState(false);
  const bodyId = useId();

  const onSubmit = async (answer: { answers: QuestionAnswer[] }) => {
    if (!chatId || !question) return;
    try {
      await answerQuestion(chatId, question.questionId, answer);
    } catch (error) {
      toast.error("Could not send your answer", {
        description: error instanceof Error ? error.message : String(error),
      });
      setAttempt((n) => n + 1);
    }
  };

  const heading =
    mode === "async"
      ? withdrawnReason
        ? { title: "Question withdrawn", status: `Withdrawn: ${withdrawnReason}` }
        : resolved
          ? { title: "Question answered", status: "Your answer was sent to the agent as a message." }
          : HEADINGS.posting
      : HEADINGS[mode];

  // The question text, as a plain list: every state but the answer form and
  // the answers shows it.
  const questionList = (testId: string, itemClassName: string) => (
    <ul className="mt-2 space-y-1" data-testid={testId}>
      {items ? (
        items.map((item, i) => (
          <li key={i} className={itemClassName}>
            {item.question}
          </li>
        ))
      ) : (
        <li className="text-sm text-muted-foreground">
          {input === undefined ? "Writing the question…" : "The question's text could not be read."}
        </li>
      )}
    </ul>
  );

  const icon = withdrawnReason ? (
    <Undo2
      className="mt-0.5 h-4 w-4 flex-shrink-0 text-muted-foreground"
      aria-hidden="true"
    />
  ) : resolved ? (
    <CheckCircle2
      className="mt-0.5 h-4 w-4 flex-shrink-0 text-success-ink"
      aria-hidden="true"
    />
  ) : (
    <MessageCircleQuestion
      className="mt-0.5 h-4 w-4 flex-shrink-0 text-muted-foreground"
      aria-hidden="true"
    />
  );

  if (resolved && !expanded) {
    const asked = settledQuestionText(items);
    const outcome = withdrawnReason
      ? `Withdrawn: ${withdrawnReason}`
      : answers.length > 0
        ? answers.map((line) => line.answer).join(" · ")
        : "No answer given.";
    return (
      <div
        data-testid="ask-user-card"
        data-mode={mode}
        data-collapsed="true"
        className="my-1.5 rounded-lg border border-border bg-card"
      >
        <button
          type="button"
          aria-expanded={false}
          onClick={() => setExpanded(true)}
          data-testid="ask-user-summary"
          className={cn(
            "flex min-h-[44px] w-full items-center gap-2.5 rounded-lg px-3 py-2 text-left text-sm",
            "transition-colors hover:bg-muted/40 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40",
          )}
        >
          <span className="flex-shrink-0 [&>svg]:mt-0">{icon}</span>
          <span className="sr-only">
            {withdrawnReason ? "Question withdrawn: " : "Question answered: "}
          </span>
          <span className="min-w-0 flex-1 truncate">
            <span
              className={cn(
                "text-muted-foreground",
                withdrawnReason && "line-through",
              )}
            >
              {asked}
            </span>
            <span
              aria-hidden="true"
              className="px-1.5 text-muted-foreground/60"
            >
              →
            </span>
            <span className="sr-only">, </span>
            <span
              className={
                withdrawnReason ? "text-muted-foreground" : "text-foreground"
              }
            >
              {outcome}
            </span>
          </span>
          <ChevronRight
            className="h-4 w-4 flex-shrink-0 text-muted-foreground"
            aria-hidden="true"
          />
        </button>
      </div>
    );
  }

  const header = (
    <>
      {icon}
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium text-foreground">{heading.title}</p>
        <p
          className="mt-0.5 text-xs text-muted-foreground"
          data-testid="ask-user-status"
        >
          {heading.status}
        </p>
      </div>
    </>
  );

  return (
    <div
      data-testid="ask-user-card"
      data-mode={mode}
      data-collapsed={resolved ? "false" : undefined}
      className="my-1.5 rounded-lg border border-border bg-card px-3 py-2.5"
    >
      {resolved ? (
        // The open settled card: its header closes it again.
        <button
          type="button"
          aria-expanded={true}
          aria-controls={bodyId}
          onClick={() => setExpanded(false)}
          className="-mx-1 flex min-h-[44px] w-[calc(100%+0.5rem)] items-start gap-2.5 rounded-md px-1 text-left hover:bg-muted/40 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          {header}
          <ChevronDown
            className="mt-0.5 h-4 w-4 flex-shrink-0 text-muted-foreground"
            aria-hidden="true"
          />
        </button>
      ) : (
        <div className="flex items-start gap-2.5">{header}</div>
      )}

      <div id={bodyId}>
        {withdrawnReason ? (
          questionList(
            "ask-user-withdrawn",
            "text-sm text-muted-foreground line-through",
          )
        ) : resolved ? (
          <ul className="mt-2 space-y-1.5" data-testid="ask-user-answers">
            {(answers.length > 0
              ? answers
              : [{ question: "", answer: "No answer given." }]
            ).map((line, i) => (
              <li key={i} className="text-sm">
                {line.question && (
                  <span className="text-muted-foreground">
                    {line.question}{" "}
                  </span>
                )}
                <span className="text-foreground">{line.answer}</span>
              </li>
            ))}
          </ul>
        ) : mode === "async" && items ? (
          <CardInset padding="none" className="mt-2">
            <QuestionPrompt
              key={attempt}
              inline
              questions={items}
              onSubmit={(a) => void onSubmit(a)}
            />
          </CardInset>
        ) : (
          // No form here: the row has not arrived (posting), the composer holds
          // it (waiting), or nothing is left to answer (asked).
          questionList("ask-user-questions", "text-sm text-foreground")
        )}
      </div>
    </div>
  );
}

/** The collapsed line's question: the first one, and how many more there are. */
export function settledQuestionText(
  items: ReturnType<typeof askUserQuestionItems>,
): string {
  if (!items || items.length === 0) return "Question";
  const first = items[0]!.question;
  return items.length > 1 ? `${first} (+${items.length - 1} more)` : first;
}

export const AskUserCard = memo(AskUserCardComponent);
