// Copyright (c) 2025 Reliant Labs

/**
 * The form for a to-do an agent gave the user (ask_user task: true): each
 * item is something only the user can do — sign in, approve elsewhere, run a
 * command — and the user marks it Done or Not done, with an optional note
 * saying why not. It answers through the same onSubmit as QuestionPrompt, in
 * the ask_user answers shape (askUserUtils.taskAnswers), so the timeline card,
 * the composer and the Inbox all post it the way they post an answer.
 *
 * One item: "Mark done" sends at once; "Not done" opens the note, and "Send"
 * sends it. Several: each is marked on its own, then one Submit sends them
 * all, once every item is marked.
 *
 * Like QuestionPrompt, the form locks itself after submitting; its parent
 * remounts it (a new key) when the update could not be delivered.
 */

import { useId, useState, type ReactNode } from "react";
import { Check, Send } from "lucide-react";
import { cn } from "../../lib/utils";
import { LinkifiedText } from "./LinkifiedText";
import type { QuestionAnswer } from "./QuestionPrompt";
import { taskAnswers, type TaskOutcome } from "./askUserUtils";

export interface TaskPromptProps {
  /** Each to-do item's text, in order. */
  tasks: string[];
  onSubmit: (answers: { answers: QuestionAnswer[] }) => void;
  /** Rendered inside a page that already scrolls (see QuestionPrompt). */
  inline?: boolean;
}

type Mark = "done" | "not_done" | null;

const primaryButtonClass = cn(
  "inline-flex h-9 items-center gap-1.5 rounded-md px-4 text-ui font-medium",
  "touch:min-h-[44px] touch:px-5",
  "bg-primary text-primary-foreground transition-colors hover:bg-primary/90",
  "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 focus-visible:ring-offset-background",
  "disabled:cursor-not-allowed disabled:opacity-60",
);

const secondaryButtonClass = cn(
  "inline-flex h-9 items-center gap-1.5 rounded-md border px-3 text-ui font-medium",
  "touch:min-h-[44px] touch:px-4",
  "transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
  "disabled:cursor-not-allowed disabled:opacity-60",
);

export function TaskPrompt({ tasks, onSubmit, inline = false }: TaskPromptProps) {
  const single = tasks.length === 1;
  const [marks, setMarks] = useState<Mark[]>(() => tasks.map(() => null));
  const [notes, setNotes] = useState<string[]>(() => tasks.map(() => ""));
  const [submitting, setSubmitting] = useState(false);
  const baseId = useId();

  const submit = (finalMarks: Mark[]) => {
    if (submitting || finalMarks.some((m) => m === null)) return;
    setSubmitting(true);
    const outcomes: TaskOutcome[] = finalMarks.map((m, i) => ({ done: m === "done", note: notes[i] }));
    onSubmit(taskAnswers(tasks, outcomes));
  };

  const mark = (index: number, value: Mark) => {
    const next = marks.map((m, i) => (i === index ? value : m));
    setMarks(next);
    // One item: marking it done is the whole answer.
    if (single && value === "done") submit(next);
  };

  const allMarked = marks.every((m) => m !== null);
  const markedCount = marks.filter((m) => m !== null).length;

  return (
    <div
      className={cn(!inline && "max-h-[40vh] overflow-y-auto overscroll-contain px-2 py-3")}
      data-testid="task-prompt"
    >
      <ul className="space-y-4">
        {tasks.map((task, i) => {
          const itemMark = marks[i];
          const textId = `${baseId}-task-${i}`;
          const noteId = `${baseId}-note-${i}`;
          return (
            <li key={i}>
              <p id={textId} className="mb-3 text-ui font-medium leading-relaxed text-foreground">
                <LinkifiedText text={task} />
              </p>

              {itemMark === "not_done" && (
                <div className="mb-3">
                  <label htmlFor={noteId} className="mb-1 block text-meta text-muted-foreground">
                    Why not? (optional)
                  </label>
                  <textarea
                    id={noteId}
                    autoFocus
                    value={notes[i]}
                    disabled={submitting}
                    onChange={(e) => {
                      const value = e.target.value;
                      setNotes((prev) => prev.map((n, j) => (j === i ? value : n)));
                    }}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" && !e.shiftKey) {
                        e.preventDefault();
                        submit(marks);
                      }
                    }}
                    rows={2}
                    placeholder="Say what got in the way, or what to do instead"
                    className={cn(
                      "w-full resize-none rounded-md border border-border bg-background px-3 py-2 text-ui",
                      "placeholder:text-muted-foreground/60",
                      "focus:border-ring focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                    )}
                  />
                </div>
              )}

              {single ? (
                <div className="flex flex-wrap items-center justify-end gap-2">
                  {itemMark === "not_done" ? (
                    <>
                      <button
                        type="button"
                        disabled={submitting}
                        onClick={() => mark(i, null)}
                        className={cn(secondaryButtonClass, "border-transparent text-muted-foreground hover:text-foreground")}
                      >
                        Back
                      </button>
                      <button type="button" disabled={submitting} onClick={() => submit(marks)} className={primaryButtonClass}>
                        <Send className="h-3.5 w-3.5" aria-hidden="true" />
                        {submitting ? "Sending…" : "Send"}
                      </button>
                    </>
                  ) : (
                    <>
                      <button
                        type="button"
                        disabled={submitting}
                        onClick={() => mark(i, "not_done")}
                        className={cn(secondaryButtonClass, "border-border text-foreground hover:bg-foreground/5")}
                      >
                        Not done
                      </button>
                      <button type="button" disabled={submitting} onClick={() => mark(i, "done")} className={primaryButtonClass}>
                        <Check className="h-3.5 w-3.5" aria-hidden="true" />
                        {submitting ? "Sending…" : "Mark done"}
                      </button>
                    </>
                  )}
                </div>
              ) : (
                <div role="group" aria-labelledby={textId} className="flex flex-wrap gap-2">
                  <MarkButton pressed={itemMark === "done"} disabled={submitting} onClick={() => mark(i, "done")}>
                    <Check className="h-3.5 w-3.5" aria-hidden="true" />
                    Done
                  </MarkButton>
                  <MarkButton
                    pressed={itemMark === "not_done"}
                    disabled={submitting}
                    onClick={() => mark(i, "not_done")}
                  >
                    Not done
                  </MarkButton>
                </div>
              )}
            </li>
          );
        })}
      </ul>

      {!single && (
        <div className="mt-4 flex items-center justify-between gap-3">
          <span className="text-meta tabular-nums text-muted-foreground">
            {markedCount} of {tasks.length} marked
          </span>
          <button type="button" disabled={!allMarked || submitting} onClick={() => submit(marks)} className={primaryButtonClass}>
            <Send className="h-3.5 w-3.5" aria-hidden="true" />
            {submitting ? "Sending…" : "Submit"}
          </button>
        </div>
      )}
    </div>
  );
}

/** One of an item's two marks, when there are several items: a toggle. */
function MarkButton({
  pressed,
  disabled,
  onClick,
  children,
}: {
  pressed: boolean;
  disabled: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      aria-pressed={pressed}
      disabled={disabled}
      onClick={onClick}
      className={cn(
        secondaryButtonClass,
        pressed ? "border-ring bg-foreground/5 text-foreground" : "border-border text-foreground/80 hover:bg-foreground/5",
      )}
    >
      {children}
    </button>
  );
}
