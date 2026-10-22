// Copyright (c) 2025 Reliant Labs

/**
 * An async ask_user (input async: true; research/ASYNC_ASK_USER.md): the agent
 * asked without waiting and kept working. The question renders here, on its
 * tool call in the timeline, as a card the user can answer whenever they like
 * — never in the composer, which still talks to the agent. The answer goes
 * through ResolveQuestion, the same call the Inbox's answer form makes, and
 * the server delivers it to the asking thread as a message from the user.
 *
 * Rendered inline in the transcript (ChatMessage), outside the tool rows and
 * even when tool rows are hidden, like RequestMachineCard: it is a question to
 * the user, not a record of work.
 */

import { memo, useMemo, useState } from "react";
import { CheckCircle2, MessageCircleQuestion, Undo2 } from "lucide-react";
import { toast } from "sonner";
import { answerQuestion, useAsyncQuestions } from "@/hooks/approval-queries";
import { CardInset } from "../forge-ui/card";
import { QuestionPrompt, type QuestionAnswer } from "./QuestionPrompt";
import { askUserAnswerLines, askUserQuestionItemsFromInput } from "./askUserUtils";

interface AsyncQuestionCardProps {
  chatId?: string;
  /** The ask_user tool call; its question row carries the same id. */
  toolCallId: string;
  /** The tool call's input: the questions, shown before the row exists. */
  input: unknown;
}

function AsyncQuestionCardComponent({ chatId, toolCallId, input }: AsyncQuestionCardProps) {
  const { data: asyncQuestions } = useAsyncQuestions(chatId);
  const question = asyncQuestions?.find((q) => q.tool_call_id === toolCallId);
  const items = useMemo(() => askUserQuestionItemsFromInput(input), [input]);
  // QuestionPrompt locks itself after submit; a remount (new key) re-arms it
  // when the answer could not be delivered.
  const [attempt, setAttempt] = useState(0);

  if (!items) return null;

  const resolved = question?.status === "resolved";
  // Withdrawn by the agent (withdraw_question): moot, not answered.
  const withdrawnReason = resolved ? question?.withdrawn_reason : undefined;
  const answers = resolved && !withdrawnReason ? askUserAnswerLines(question?.response_data) : [];

  const onSubmit = async (answer: { answers: QuestionAnswer[] }) => {
    if (!chatId || !question) return;
    try {
      await answerQuestion(chatId, question.question_id, answer);
    } catch (error) {
      toast.error("Could not send your answer", {
        description: error instanceof Error ? error.message : String(error),
      });
      setAttempt((n) => n + 1);
    }
  };

  return (
    <div data-testid="async-question-card" className="my-1.5 rounded-lg border border-border bg-card px-3 py-2.5">
      <div className="flex items-start gap-2.5">
        {withdrawnReason ? (
          <Undo2 className="mt-0.5 h-4 w-4 flex-shrink-0 text-muted-foreground" aria-hidden="true" />
        ) : resolved ? (
          <CheckCircle2 className="mt-0.5 h-4 w-4 flex-shrink-0 text-success-ink" aria-hidden="true" />
        ) : (
          <MessageCircleQuestion className="mt-0.5 h-4 w-4 flex-shrink-0 text-muted-foreground" aria-hidden="true" />
        )}
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium text-foreground">
            {withdrawnReason ? "Question withdrawn" : resolved ? "Question answered" : "Question for you · not blocking"}
          </p>
          <p className="mt-0.5 text-xs text-muted-foreground" data-testid="async-question-status">
            {withdrawnReason
              ? `Withdrawn: ${withdrawnReason}`
              : resolved
                ? "Your answer was sent to the agent as a message."
                : "The agent kept working on its best guess. Answer any time — it gets your answer as a message."}
          </p>
        </div>
      </div>

      {withdrawnReason ? (
        <ul className="mt-2 space-y-1" data-testid="async-question-withdrawn">
          {items.map((item, i) => (
            <li key={i} className="text-sm text-muted-foreground line-through">
              {item.question}
            </li>
          ))}
        </ul>
      ) : resolved ? (
        <ul className="mt-2 space-y-1.5" data-testid="async-question-answers">
          {(answers.length > 0 ? answers : [{ question: "", answer: "No answer given." }]).map((line, i) => (
            <li key={i} className="text-sm">
              {line.question && <span className="text-muted-foreground">{line.question} </span>}
              <span className="text-foreground">{line.answer}</span>
            </li>
          ))}
        </ul>
      ) : question ? (
        <CardInset padding="none" className="mt-2">
          <QuestionPrompt key={attempt} questions={items} onSubmit={(a) => void onSubmit(a)} />
        </CardInset>
      ) : (
        // The tool call is in the timeline before its question row is: show
        // what is being asked until the row (and so the form) arrives.
        <ul className="mt-2 space-y-1" data-testid="async-question-posting">
          {items.map((item, i) => (
            <li key={i} className="text-sm text-foreground">
              {item.question}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

export const AsyncQuestionCard = memo(AsyncQuestionCardComponent);
