/**
 * Escape stops (pauses) a running chat — but not on the first press while the
 * user is typing. Pressing Escape to dismiss something in the composer (an
 * autocomplete, a draft, a habit) used to stop a 20-minute run with no
 * confirmation. Now, with text in the focused field, the first Escape only
 * arms the stop and says so; a second Escape within the window stops it.
 * With nothing typed, one Escape still stops, as before.
 */

export const ESCAPE_STOP_WINDOW_MS = 1500;

/** The focused element is a text field with something typed in it. */
export function isTypingIn(element: Element | null): boolean {
  if (!element) return false;
  if (element instanceof HTMLTextAreaElement) return element.value.trim().length > 0;
  if (element instanceof HTMLInputElement) {
    const textual = ["text", "search", "url", "email", "tel", "password", "number", ""].includes(element.type);
    return textual && element.value.trim().length > 0;
  }
  if (element instanceof HTMLElement && element.isContentEditable) {
    return (element.textContent ?? "").trim().length > 0;
  }
  return false;
}

/**
 * What an Escape does to a busy run: "stop" now, or "arm" (say that a second
 * press stops it). `armedAt` is when the previous Escape armed it, if one did.
 */
export function escapeStopDecision(typing: boolean, armedAt: number | null, now: number): "stop" | "arm" {
  if (!typing) return "stop";
  if (armedAt !== null && now - armedAt <= ESCAPE_STOP_WINDOW_MS) return "stop";
  return "arm";
}
