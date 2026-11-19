import type { ChatListIndicators } from "../../lib/runStatus";

/**
 * A chat row's state, in words and a tone. The row draws it as one mark in
 * its 16px slot (sidebar/primitives ChatStateSlot), never as a chip or a
 * tinted row; the words are the mark's accessible name and its tooltip.
 * Green is not one of them: green means a success, and a row is never
 * painted for one.
 *
 *   running    work in progress: a small spinner
 *   attention  the user is needed (a question, an approval): a warning dot
 *   failed     the run stopped on an error: a destructive dot
 *   neutral    anything else in flight (paused, waiting for a machine): a muted dot
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

/** Dot colours. Running has none: it renders as a small spinner. */
export const CHAT_ROW_DOT_CLASS: Record<Exclude<ChatRowTone, "running">, string> = {
  attention: "bg-warning",
  failed: "bg-destructive",
  neutral: "bg-muted-foreground",
};
