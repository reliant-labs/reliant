/**
 * Parse ask_user question metadata from a question's metadata string.
 * Handles both new format (questions at top level) and old envelope format
 * (questions nested in a JSON-encoded "input" field with __reliant_tool_meta__ wrapper).
 *
 * Returns the parsed metadata with a `questions` array, or null if not an ask_user question.
 */
export function parseAskUserMetadata(metadata: string | undefined | null): AskUserMetadata | null {
  if (!metadata) return null;
  try {
    const parsed = JSON.parse(metadata);
    if (parsed?.type !== "ask_user") return null;
    // New format: questions at top level
    const topLevelQuestions = normalizeQuestions(parsed.questions);
    if (topLevelQuestions) {
      return { ...parsed, questions: topLevelQuestions } as AskUserMetadata;
    }
    // Old envelope format: questions inside nested "input" string
    if (typeof parsed.input === "string") {
      try {
        const inner = JSON.parse(parsed.input);
        const innerQuestions = normalizeQuestions(inner?.questions);
        if (innerQuestions) {
          return { ...parsed, questions: innerQuestions } as AskUserMetadata;
        }
      } catch { /* not valid inner JSON */ }
    }
  } catch {
    // Not valid JSON or not an ask_user question
  }
  return null;
}

/**
 * Normalize a raw `questions` value from server metadata into a validated
 * AskUserQuestion array. The backend has been observed to persist `questions`
 * as a double-encoded JSON string (or other non-array shapes) verbatim from
 * LLM tool input, so this must never trust the shape:
 * - If the value is a string, attempt to JSON.parse it (double-encoded case).
 * - Only accept arrays; filter entries down to objects with a string
 *   `question` field, coercing a missing/invalid `options` to [].
 * Returns null when nothing valid remains.
 */
function normalizeQuestions(raw: unknown): AskUserQuestion[] | null {
  let value = raw;
  if (typeof value === "string") {
    try {
      value = JSON.parse(value);
    } catch {
      return null;
    }
  }
  if (!Array.isArray(value)) return null;
  const questions = value
    .filter(
      (q): q is Record<string, unknown> =>
        typeof q === "object" && q !== null && typeof (q as { question?: unknown }).question === "string"
    )
    .map((q) => ({
      ...q,
      options: Array.isArray(q.options) ? q.options : [],
    })) as unknown as AskUserQuestion[];
  return questions.length > 0 ? questions : null;
}

export interface AskUserQuestionOption {
  label: string;
  description: string;
  preview?: string;
}

export interface AskUserQuestion {
  question: string;
  options: AskUserQuestionOption[];
  allow_multiple?: boolean;
}

export interface AskUserMetadata {
  type: "ask_user";
  tool_call_id?: string;
  questions: AskUserQuestion[];
  /** Asked without waiting (ask_user without blocking: true). */
  async?: boolean;
  /**
   * A to-do for the user (ask_user task: true), not a question: each entry of
   * `questions` is one thing to do, with no options, which the user marks
   * Done or Not done (TASK_DONE / TASK_NOT_DONE), with an optional note.
   */
  task?: boolean;
}

/** The answer labels of a to-do item; the server's askuser package uses the same. */
export const TASK_DONE = "Done";
export const TASK_NOT_DONE = "Not done";

/** Whether a question row's ask_user metadata is a to-do (task: true). */
export function isTaskAskUserMetadata(metadata: string | undefined | null): boolean {
  return parseAskUserMetadata(metadata)?.task === true;
}

/** One to-do item's outcome, as the user marked it. */
export interface TaskOutcome {
  done: boolean;
  /** Why it was not done, or any note on it; optional. */
  note?: string;
}

/**
 * The ResolveQuestion answer for a to-do: one ask_user answer per item, in
 * order, selecting Done or Not done, with the note as freetext. The same wire
 * shape as a question's answer, so every resolver path delivers it unchanged.
 */
export function taskAnswers(
  tasks: string[],
  outcomes: TaskOutcome[],
): { answers: Array<{ question: string; selected: string[]; freetext?: string }> } {
  return {
    answers: tasks.map((task, i) => {
      const outcome = outcomes[i];
      const answer: { question: string; selected: string[]; freetext?: string } = {
        question: task,
        selected: [outcome?.done ? TASK_DONE : TASK_NOT_DONE],
      };
      const note = outcome?.note?.trim();
      if (note) answer.freetext = note;
      return answer;
    }),
  };
}

/**
 * A to-do's answers from its ResolveQuestion response_data: each item's text,
 * whether it was marked done, and the note. `done` is null when an answer
 * selects neither label: an older client, which renders a to-do as a question
 * with no options, answered it in free text, and the note is all there is.
 * Empty when there is no answer.
 */
export function taskOutcomeLines(
  responseData: string | undefined | null,
): Array<{ task: string; done: boolean | null; note: string }> {
  if (!responseData) return [];
  try {
    const parsed = JSON.parse(responseData) as { answers?: Array<{ question?: string; selected?: string[]; freetext?: string }> };
    if (!Array.isArray(parsed?.answers)) return [];
    return parsed.answers.map((a) => {
      const selected = a.selected ?? [];
      return {
        task: a.question ?? "",
        done: selected.includes(TASK_DONE) ? true : selected.includes(TASK_NOT_DONE) ? false : null,
        note: a.freetext?.trim() ?? "",
      };
    });
  } catch {
    return [];
  }
}

/** "Done", "Not done", or either with its note: one to-do's outcome as text. */
export function taskOutcomeText(line: { done: boolean | null; note: string }): string {
  if (line.done === null) return line.note;
  const label = line.done ? TASK_DONE : TASK_NOT_DONE;
  return line.note ? `${label} — ${line.note}` : label;
}

/**
 * The ask_user metadata as QuestionPrompt's items, or null when it is not an
 * ask_user question. The chat composer and the Inbox both render a question
 * from metadata, so the mapping lives here once.
 */
export function askUserQuestionItems(
  metadata: string | undefined | null,
): Array<{ question: string; options: AskUserQuestionOption[]; allowMultiple: boolean }> | null {
  const parsed = parseAskUserMetadata(metadata);
  if (!parsed) return null;
  return parsed.questions.map((q) => ({
    question: q.question,
    options: q.options || [],
    allowMultiple: q.allow_multiple ?? false,
  }));
}

/**
 * An ask_user tool call's input as an object: it may arrive parsed, as a JSON
 * string, or inside the __reliant_tool_meta__ envelope whose `input` is the
 * JSON string the model wrote. Null when it is none of those.
 */
function askUserInputObject(input: unknown): Record<string, unknown> | null {
  let value = input;
  if (typeof value === "string") {
    try {
      value = JSON.parse(value);
    } catch {
      return null;
    }
  }
  if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
  const obj = value as Record<string, unknown>;
  if ("__reliant_tool_meta__" in obj && typeof obj.input === "string") {
    return askUserInputObject(obj.input);
  }
  return obj;
}

/**
 * Whether an ask_user call's input asks without waiting — the default: only
 * input blocking: true waits. A quoted "true" counts, as it does on the
 * server. Only a hint: the call's async question row, when there is one, is
 * the authority (AskUserCard). A call recorded before async became the
 * default also reads as async here, and has no such row.
 */
export function isAsyncAskUserInput(input: unknown): boolean {
  const obj = askUserInputObject(input);
  const flag = obj?.blocking;
  return !isTrueFlag(flag);
}

/**
 * Whether an ask_user call's input is a to-do (task: true; a quoted "true"
 * counts, as on the server). Read before the call's question row exists; the
 * row's metadata (isTaskAskUserMetadata) says the same once it does.
 */
export function isTaskAskUserInput(input: unknown): boolean {
  return isTrueFlag(askUserInputObject(input)?.task);
}

function isTrueFlag(flag: unknown): boolean {
  return flag === true || (typeof flag === "string" && flag.trim().toLowerCase() === "true");
}

/**
 * An ask_user call's questions as QuestionPrompt's items, from its tool input.
 * Models write `questions` in more shapes than the schema's array: as a
 * JSON-encoded string of it (handled by askUserQuestionItems), as one question
 * object, or as the bare question text; and some write a single `question`.
 * Each still yields the question, so the user always sees what was asked.
 */
export function askUserQuestionItemsFromInput(
  input: unknown,
): Array<{ question: string; options: AskUserQuestionOption[]; allowMultiple: boolean }> | null {
  const obj = askUserInputObject(input);
  if (!obj) return null;
  const raw = obj.questions;
  const single = typeof raw === "object" && raw !== null && !Array.isArray(raw) ? [raw] : raw;
  const items = askUserQuestionItems(JSON.stringify({ type: "ask_user", questions: single }));
  if (items) return items;
  const text = [raw, obj.question].find((v): v is string => typeof v === "string" && v.trim() !== "");
  return text ? [{ question: text.trim(), options: [], allowMultiple: false }] : null;
}

/**
 * The answers in a ResolveQuestion response_data, as "question → answer"
 * lines for display. A bare "Continue" is no answer. Empty when there is none.
 */
export function askUserAnswerLines(responseData: string | undefined | null): Array<{ question: string; answer: string }> {
  if (!responseData) return [];
  try {
    const parsed = JSON.parse(responseData) as { answers?: Array<{ question?: string; selected?: string[]; freetext?: string }> };
    if (!Array.isArray(parsed?.answers)) return [{ question: "", answer: responseData }];
    return parsed.answers
      .map((a) => {
        const said = [...(a.selected ?? []).filter((s) => s && s !== "Continue")];
        if (a.freetext?.trim()) said.push(a.freetext.trim());
        return { question: a.question ?? "", answer: said.join(" — ") };
      })
      .filter((l) => l.answer !== "");
  } catch {
    return [{ question: "", answer: responseData }];
  }
}
