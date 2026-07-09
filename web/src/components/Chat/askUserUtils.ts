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
}