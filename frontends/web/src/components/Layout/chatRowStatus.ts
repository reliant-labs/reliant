import type { ChatListIndicators } from "../../lib/runStatus";

/**
 * The status word a chat row shows, as text — "Running", "Needs you",
 * "Failed" — rather than a row tint and a dot whose meaning lived in a hover
 * tooltip. Green is not one of them: green means a success, and a row is never
 * painted for one.
 *
 *   running    work in progress: a small spinner, no chip and no word (the
 *              label is its accessible name and hover title)
 *   attention  the user is needed (a question, an approval): a warning chip,
 *              and the only state that tints the row
 *   failed     the run stopped on an error: a destructive chip
 *   neutral    anything else in flight (paused, waiting for a machine)
 */
export type ChatRowTone = "running" | "attention" | "failed" | "neutral";

export interface ChatRowChip {
  label: string;
  tone: ChatRowTone;
  /** The fuller phrase, for the chip's accessible description. */
  detail?: string;
}

export function chatRowChip({ needsInput, status }: Pick<ChatListIndicators, "needsInput" | "status">): ChatRowChip | null {
  if (needsInput) return { label: "Needs you", tone: "attention", detail: needsInput.label };
  if (!status) return null;
  switch (status.key) {
    case "running":
      return { label: "Running", tone: "running" };
    case "failed":
      return { label: "Failed", tone: "failed" };
    case "needs_you":
      return { label: "Needs you", tone: "attention", detail: status.label };
    default:
      return { label: status.label, tone: "neutral" };
  }
}

/** Chip colours. Running has none: it renders as a small spinner, not a chip. */
export const CHAT_ROW_CHIP_CLASS: Record<Exclude<ChatRowTone, "running">, string> = {
  attention: "border-warning/40 bg-warning/10 text-warning-ink",
  failed: "border-destructive/40 bg-destructive/10 text-destructive-ink",
  neutral: "border-border text-muted-foreground",
};
