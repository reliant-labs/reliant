/**
 * Parse ask_user question metadata from a yield's metadata string.
 * Handles both new format (questions at top level) and old envelope format
 * (questions nested in a JSON-encoded "input" field with __reliant_tool_meta__ wrapper).
 *
 * Returns the parsed metadata with a `questions` array, or null if not an ask_user yield.
 */
export function parseAskUserMetadata(metadata: string | undefined | null): AskUserMetadata | null {
  if (!metadata) return null;
  try {
    const parsed = JSON.parse(metadata);
    if (parsed.type !== "ask_user") return null;
    // New format: questions at top level
    if (parsed.questions?.length > 0) return parsed as AskUserMetadata;
    // Old envelope format: questions inside nested "input" string
    if (typeof parsed.input === "string") {
      try {
        const inner = JSON.parse(parsed.input);
        if (inner.questions?.length > 0) {
          return { ...parsed, questions: inner.questions } as AskUserMetadata;
        }
      } catch { /* not valid inner JSON */ }
    }
  } catch {
    // Not valid JSON or not an ask_user question
  }
  return null;
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