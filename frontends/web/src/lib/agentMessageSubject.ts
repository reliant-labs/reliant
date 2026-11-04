/**
 * The one line an agent-to-agent message collapses to in the transcript.
 *
 * spawn_send requires a `subject` (a short summary the sending agent writes),
 * and both sides of the exchange show only that line until the user expands
 * the message: the sender's spawn_send row and the receiver's message card.
 * A message with no subject (sent before the field existed) falls back to the
 * first non-empty line of its body, stripped of the markdown that would read
 * as noise on one line, and truncated.
 */

/** Longest one-line label shown before truncation; matches the tool's cap. */
export const AGENT_MESSAGE_SUBJECT_MAX_CHARS = 100;

function truncate(text: string, max: number): string {
  return text.length > max ? `${text.slice(0, max - 1).trimEnd()}…` : text;
}

/** The first non-empty line of a markdown body, as plain-ish text. */
function firstLine(body: string): string {
  for (const raw of body.split("\n")) {
    const line = raw
      .replace(/^\s*(?:#{1,6}\s+|[-*+]\s+|>\s*|\d+\.\s+)/, "")
      .replace(/[*_`]+/g, "")
      .trim();
    if (line && !/^```/.test(raw.trim())) return line;
  }
  return "";
}

/**
 * The collapsed label for an agent message: its subject when it has one,
 * otherwise a truncated first line of the body. Empty only when both are.
 */
export function agentMessageSubject(
  subject: string | undefined | null,
  body: string | undefined | null,
  max = AGENT_MESSAGE_SUBJECT_MAX_CHARS,
): string {
  const trimmed = (subject ?? "").replace(/\s+/g, " ").trim();
  if (trimmed) return truncate(trimmed, max);
  return truncate(firstLine(body ?? ""), max);
}
