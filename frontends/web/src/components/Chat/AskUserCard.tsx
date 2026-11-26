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
 *
 * A to-do (ask_user task: true) is the same card in other words: the agent
 * gave the user something to do rather than a question, so it reads "To do",
 * holds TaskPrompt (Mark done / Not done, with an optional note) where a
 * question holds QuestionPrompt, and once settled shows each item's outcome:
 * "Done", or "Not done — <note>". The input's flag says so before the row
 * exists; the row's metadata after.
 */

import { memo, useId, useMemo, useState } from "react";
import { CheckCircle2, ChevronDown, ChevronRight, CircleSlash, ListTodo, MessageCircleQuestion, Undo2 } from "lucide-react";
import { toast } from "sonner";
import { answerQuestion, useAsyncQuestions, type QuestionInfo } from "@/hooks/question-queries";
import { cn } from "@/lib/utils";
import { LinkifiedText } from "./LinkifiedText";
import { QuestionPrompt, type QuestionAnswer } from "./QuestionPrompt";
import { TaskPrompt } from "./TaskPrompt";
import {
  askUserAnswerLines,
  askUserQuestionItems,
  askUserQuestionItemsFromInput,
  isAsyncAskUserInput,
  isTaskAskUserInput,
  isTaskAskUserMetadata,
  taskOutcomeLines,
  taskOutcomeText,
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

const TASK_HEADINGS: typeof HEADINGS = {
  posting: {
    title: "To do for you · not blocking",
    status: "The agent kept working. Mark it when you have done it — the agent gets your update as a message.",
  },
  waiting: {
    title: "To do for you",
    status: "The agent is waiting until you mark this done. Mark it below.",
  },
  asked: {
    title: "To-do given",
    status: "The agent gave you this to-do earlier in the conversation.",
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
  // A to-do rather than a question, by the row's metadata or, before the row
  // exists (or from a server that did not record it), by the input.
  const isTask = isTaskAskUserMetadata(question?.metadata) || isTaskAskUserInput(input);
  const resolved = mode === "async" && question?.status === "resolved";
  // Withdrawn by the agent (withdraw_question): moot, not answered.
  const withdrawnReason = resolved ? question?.withdrawnReason : undefined;
  const settled = resolved && !withdrawnReason;
  const answers = settled && !isTask ? askUserAnswerLines(question?.responseData) : [];
  // A to-do's outcomes as "question → answer" lines, so the views below show
  // either kind the same way; `done` picks each line's mark.
  const taskLines = settled && isTask ? taskOutcomeLines(question?.responseData) : [];
  const outcomeLines: Array<{ question: string; answer: string; done?: boolean }> = isTask
    ? taskLines
        .map((line) => ({ question: line.task, answer: taskOutcomeText(line), done: line.done ?? undefined }))
        .filter((line) => line.answer !== "")
    : answers;
  const allDone = taskLines.length > 0 && taskLines.every((line) => line.done === true);
  const anyNotDone = taskLines.some((line) => line.done === false);
  const noun = isTask ? "To-do" : "Question";
  // A settled card starts collapsed; the user can open it and close it again.
  const [expanded, setExpanded] = useState(false);
  const bodyId = useId();

  const onSubmit = async (answer: { answers: QuestionAnswer[] }) => {
    if (!chatId || !question) return;
    try {
      await answerQuestion(chatId, question.questionId, answer);
    } catch (error) {
      toast.error(isTask ? "Could not send your update" : "Could not send your answer", {
        description: error instanceof Error ? error.message : String(error),
      });
      setAttempt((n) => n + 1);
    }
  };

  const headings = isTask ? TASK_HEADINGS : HEADINGS;
  const settledTitle = isTask
    ? allDone
      ? "To-do done"
      : anyNotDone
        ? "To-do not done"
        : "To-do updated"
    : "Question answered";
  const heading =
    mode === "async"
      ? withdrawnReason
        ? { title: `${noun} withdrawn`, status: `Withdrawn: ${withdrawnReason}` }
        : resolved
          ? {
              title: settledTitle,
              status: isTask
                ? "Your update was sent to the agent as a message."
                : "Your answer was sent to the agent as a message.",
            }
          : headings.posting
      : headings[mode];

  // The question text, as a plain list: every state but the answer form and
  // the answers shows it.
  const questionList = (testId: string, itemClassName: string) => (
    <ul className="mt-2 space-y-1" data-testid={testId}>
      {items ? (
        items.map((item, i) => (
          <li key={i} className={itemClassName}>
            <LinkifiedText text={item.question} />
          </li>
        ))
      ) : (
        <li className="text-ui text-muted-foreground">
          {input === undefined
            ? `Writing the ${isTask ? "to-do" : "question"}…`
            : `The ${isTask ? "to-do" : "question"}'s text could not be read.`}
        </li>
      )}
    </ul>
  );

  const icon = withdrawnReason ? (
    <Undo2
      className="mt-0.5 h-4 w-4 flex-shrink-0 text-muted-foreground"
      aria-hidden="true"
    />
  ) : resolved && isTask && anyNotDone ? (
    <CircleSlash
      className="mt-0.5 h-4 w-4 flex-shrink-0 text-muted-foreground"
      aria-hidden="true"
    />
  ) : resolved ? (
    <CheckCircle2
      className="mt-0.5 h-4 w-4 flex-shrink-0 text-success-ink"
      aria-hidden="true"
    />
  ) : isTask ? (
    <ListTodo
      className="mt-0.5 h-4 w-4 flex-shrink-0 text-muted-foreground"
      aria-hidden="true"
    />
  ) : (
    <MessageCircleQuestion
      className="mt-0.5 h-4 w-4 flex-shrink-0 text-muted-foreground"
      aria-hidden="true"
    />
  );

  if (resolved && !expanded) {
    const asked = settledQuestionText(items, noun);
    const outcome = withdrawnReason
      ? `Withdrawn: ${withdrawnReason}`
      : outcomeLines.length > 0
        ? outcomeLines.map((line) => line.answer).join(" · ")
        : isTask
          ? "No update given."
          : "No answer given.";
    return (
      <div
        data-testid="ask-user-card"
        data-mode={mode}
        data-collapsed="true"
        className="min-w-0"
      >
        <button
          type="button"
          aria-expanded={false}
          onClick={() => setExpanded(true)}
          data-testid="ask-user-summary"
          className={cn(
            // Answered, the question is a quiet row like a tool call's.
            "-mx-1.5 flex min-h-7 w-[calc(100%+0.75rem)] items-center gap-2 rounded-md px-1.5 text-left text-ui touch:min-h-[44px]",
            "text-muted-foreground transition-colors hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
          )}
        >
          <span className="flex-shrink-0 [&>svg]:mt-0">{icon}</span>
          <span className="sr-only">
            {withdrawnReason ? `${noun} withdrawn: ` : `${settledTitle}: `}
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
            className="size-3.5 flex-shrink-0 opacity-70"
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
        <p className="text-ui font-medium text-foreground">{heading.title}</p>
        <p
          className="mt-0.5 text-meta text-muted-foreground"
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
      // break-words: a question or answer is agent-written text, and an
      // unbroken URL or path in it ran past the card instead of wrapping.
      // w-full: the transcript's message column sizes children to fit, and
      // a question card takes the whole column, like an approval.
      className="w-full rounded-xl border border-border bg-background p-4 break-words sm:p-5"
    >
      {resolved ? (
        // The open settled card: its header closes it again.
        <button
          type="button"
          aria-expanded={true}
          aria-controls={bodyId}
          onClick={() => setExpanded(false)}
          className="-mx-1 flex min-h-[44px] w-[calc(100%+0.5rem)] items-start gap-2.5 rounded-md px-1 text-left focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
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
            "text-ui text-muted-foreground line-through",
          )
        ) : resolved ? (
          <ul className="mt-3 space-y-1.5" data-testid="ask-user-answers">
            {(outcomeLines.length > 0
              ? outcomeLines
              : [{ question: "", answer: isTask ? "No update given." : "No answer given." }]
            ).map((line: { question: string; answer: string; done?: boolean }, i) => (
              <li key={i} className="text-ui">
                {line.done !== undefined && (
                  <span className="mr-1.5 inline-flex align-[-2px]">
                    {line.done ? (
                      <CheckCircle2 className="h-3.5 w-3.5 text-success-ink" aria-hidden="true" />
                    ) : (
                      <CircleSlash className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
                    )}
                  </span>
                )}
                {line.question && (
                  <span className="text-muted-foreground">
                    <LinkifiedText text={line.question} />{" "}
                  </span>
                )}
                <span className="text-foreground">
                  <LinkifiedText text={line.answer} />
                </span>
              </li>
            ))}
          </ul>
        ) : mode === "async" && items ? (
          <div className="mt-3">
            {isTask ? (
              <TaskPrompt
                key={attempt}
                inline
                tasks={items.map((item) => item.question)}
                onSubmit={(a) => void onSubmit(a)}
              />
            ) : (
              <QuestionPrompt
                key={attempt}
                inline
                questions={items}
                onSubmit={(a) => void onSubmit(a)}
              />
            )}
          </div>
        ) : (
          // No form here: the row has not arrived (posting), the composer holds
          // it (waiting), or nothing is left to answer (asked).
          questionList("ask-user-questions", "text-ui text-foreground")
        )}
      </div>
    </div>
  );
}

/** The collapsed line's question (or to-do): the first one, and how many more there are. */
export function settledQuestionText(
  items: ReturnType<typeof askUserQuestionItems>,
  fallback = "Question",
): string {
  if (!items || items.length === 0) return fallback;
  const first = items[0]!.question;
  return items.length > 1 ? `${first} (+${items.length - 1} more)` : first;
}

export const AskUserCard = memo(AskUserCardComponent);
