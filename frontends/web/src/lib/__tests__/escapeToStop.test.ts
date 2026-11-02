import { describe, expect, it } from "vitest";
import { ESCAPE_STOP_WINDOW_MS, escapeStopDecision, isTypingIn } from "../escapeToStop";

describe("Escape to stop a run", () => {
  it("stops at once when nothing is being typed", () => {
    expect(escapeStopDecision(false, null, 1000)).toBe("stop");
  });

  it("only arms the stop on the first Escape while typing, and stops on a quick second one", () => {
    expect(escapeStopDecision(true, null, 1000)).toBe("arm");
    expect(escapeStopDecision(true, 1000, 1000 + ESCAPE_STOP_WINDOW_MS - 1)).toBe("stop");
    // Too late: it arms again rather than stopping.
    expect(escapeStopDecision(true, 1000, 1000 + ESCAPE_STOP_WINDOW_MS + 1)).toBe("arm");
  });

  it("counts a text field with something in it as typing", () => {
    const textarea = document.createElement("textarea");
    expect(isTypingIn(textarea)).toBe(false);
    textarea.value = "stop, wrong branch";
    expect(isTypingIn(textarea)).toBe(true);

    const input = document.createElement("input");
    input.value = "x";
    expect(isTypingIn(input)).toBe(true);
    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    expect(isTypingIn(checkbox)).toBe(false);

    expect(isTypingIn(document.createElement("button"))).toBe(false);
    expect(isTypingIn(null)).toBe(false);
  });
});
