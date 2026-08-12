import { beforeEach, describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { SequenceHint } from "../SequenceHint";
import { useShortcutsStore } from "../../../store/shortcutsStore";
import { defaultShortcuts } from "../../../store/shortcutsData.generated";
import { ShortcutRegistry } from "../../../lib/keyboard/registry";
import { parseBinding, sequencePrefix } from "../../../lib/keyboard/chord";
import { detectPlatform } from "../../../lib/keyboard/platform";

/**
 * The armed prefix in canonical form for whichever platform the test runs on.
 * jsdom reports a non-Mac navigator, so "Cmd+K" resolves to `ctrl+K` here and
 * `meta+K` on a real Mac — hardcoding either makes the test lie on one of them.
 */
const PREFIX = sequencePrefix(
  parseBinding("Cmd+K J", detectPlatform().isMac),
);

/** Load the real generated shortcuts into the store, as the app does at boot. */
function hydrateStore() {
  const shortcuts = Object.fromEntries(
    Object.entries(defaultShortcuts).map(([id, shortcut]) => [
      id,
      { ...shortcut, currentBinding: null },
    ]),
  );
  useShortcutsStore.setState({
    shortcuts,
    registry: new ShortcutRegistry([]),
    _initialized: true,
  });
}

describe("SequenceHint", () => {
  beforeEach(() => {
    hydrateStore();
  });

  it("renders nothing when no prefix is armed", () => {
    const { container } = render(<SequenceHint pending="" />);
    expect(container.textContent).toBe("");
    expect(document.body.querySelector('[role="status"]')).toBeNull();
  });

  it("renders nothing for a prefix no shortcut uses", () => {
    render(<SequenceHint pending={sequencePrefix(parseBinding("Cmd+Q X", detectPlatform().isMac))} />);
    expect(document.body.querySelector('[role="status"]')).toBeNull();
  });

  it("lists the completions available for an armed prefix", () => {
    render(<SequenceHint pending={PREFIX} />);

    // Real bindings from config/shortcuts.yaml: Cmd+K J focuses the terminal,
    // Cmd+K A the chat list.
    expect(screen.getByText("Focus Terminal")).toBeTruthy();
    expect(screen.getByText("Focus Chat List")).toBeTruthy();
  });

  it("shows only the completing key, not the whole binding", () => {
    render(<SequenceHint pending={PREFIX} />);

    const keys = Array.from(
      document.body.querySelectorAll("kbd"),
    ).map((el) => el.textContent);

    // The prefix appears once in the header; completions are bare keys.
    expect(keys).toContain("J");
    // The prefix chord itself appears exactly once, in the header.
    const { isMac } = detectPlatform();
    const prefixLabel = isMac ? "⌘K" : "Ctrl+K";
    expect(keys.filter((k) => k === prefixLabel)).toHaveLength(1);
  });

  it("groups completions by category", () => {
    render(<SequenceHint pending={PREFIX} />);
    expect(screen.getByText("Focus")).toBeTruthy();
  });

  it("is non-interactive so it cannot swallow the next keystroke", () => {
    render(<SequenceHint pending={PREFIX} />);

    const overlay = document.body.querySelector('[role="status"]');
    expect(overlay?.className).toContain("pointer-events-none");
  });

  it("announces politely rather than interrupting screen readers", () => {
    render(<SequenceHint pending={PREFIX} />);

    const overlay = document.body.querySelector('[role="status"]');
    expect(overlay?.getAttribute("aria-live")).toBe("polite");
  });

  it("reflects a user's remapped binding", () => {
    // Move "focus terminal" onto a different sequence and confirm the hint
    // follows the remap rather than the default.
    const shortcuts = useShortcutsStore.getState().shortcuts;
    useShortcutsStore.setState({
      shortcuts: {
        ...shortcuts,
        focusTerminal: {
          ...shortcuts.focusTerminal,
          currentBinding: "Cmd+G T",
        },
      },
    });

    render(<SequenceHint pending={PREFIX} />);
    expect(screen.queryByText("Focus Terminal")).toBeNull();
  });
});
