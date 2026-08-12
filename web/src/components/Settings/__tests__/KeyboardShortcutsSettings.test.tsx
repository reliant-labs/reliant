/**
 * Capture behavior for the shortcut remap UI.
 *
 * The interesting case is sequences: 24 of the shipped defaults are two-key
 * bindings, so the capture box has to be able to express them or a user could
 * never rebind onto — or away from — that shape.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { KeyboardShortcutsSettings } from "../KeyboardShortcutsSettings";
import { useShortcutsStore } from "../../../store/shortcutsStore";
import { defaultShortcuts } from "../../../store/shortcutsData.generated";
import { ShortcutRegistry } from "../../../lib/keyboard/registry";

vi.mock("../../../lib/toast-manager", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}));

function hydrate() {
  const shortcuts = Object.fromEntries(
    Object.entries(defaultShortcuts).map(([id, shortcut]) => [
      id,
      { ...shortcut, currentBinding: null },
    ]),
  );
  useShortcutsStore.setState({
    shortcuts,
    registry: new ShortcutRegistry([]),
    isEditing: null,
    isLoading: false,
    _initialized: true,
  });
}

/**
 * Open the capture box for the first shortcut on screen.
 *
 * The component renders a desktop and a mobile layout at once (hidden by CSS,
 * but both in the DOM), so every control appears twice. Tests use getAllBy* and
 * assert on the first match rather than trying to disambiguate by breakpoint.
 */
function startEditing() {
  const editButtons = screen.getAllByRole("button", { name: "" });
  fireEvent.click(editButtons[0]);
}

const press = (init: Partial<KeyboardEventInit> & { key: string }) =>
  fireEvent.keyDown(document, { bubbles: true, ...init });

describe("KeyboardShortcutsSettings capture", () => {
  beforeEach(() => {
    hydrate();
  });

  it("renders the shortcut list", () => {
    render(<KeyboardShortcutsSettings />);
    expect(screen.getAllByText("New Chat").length).toBeGreaterThan(0);
  });

  it("captures a plain chord in one keystroke", () => {
    render(<KeyboardShortcutsSettings />);
    startEditing();

    press({ key: "q", ctrlKey: true });

    // Ctrl+Q renders as Ctrl+Q off macOS (jsdom reports a non-Mac navigator).
    expect(screen.getAllByText(/Ctrl\+Q|⌃Q/).length).toBeGreaterThan(0);
  });

  describe("sequences", () => {
    it("waits for a second key after a known sequence prefix", () => {
      render(<KeyboardShortcutsSettings />);
      startEditing();

      // Cmd+K opens sequences throughout the app, so capture should arm rather
      // than accept it as a complete binding.
      press({ key: "k", ctrlKey: true });

      expect(screen.getAllByText(/Now press the second key/).length).toBeGreaterThan(0);
    });

    it("completes the sequence on the next key", () => {
      render(<KeyboardShortcutsSettings />);
      startEditing();

      press({ key: "k", ctrlKey: true });
      press({ key: "t" });

      expect(screen.getAllByText(/then T/).length).toBeGreaterThan(0);
      expect(screen.queryAllByText(/Now press the second key/)).toHaveLength(0);
    });

    it("cannot be saved while only the prefix is captured", () => {
      // Saving a bare prefix would shadow every sequence starting with it.
      render(<KeyboardShortcutsSettings />);
      startEditing();

      press({ key: "k", ctrlKey: true });

      const save = screen
        .getAllByRole("button")
        .find((b) => b.querySelector("svg.lucide-check"));
      expect(save?.hasAttribute("disabled")).toBe(true);
    });

    it("backs out of a half-typed sequence without discarding the edit", () => {
      render(<KeyboardShortcutsSettings />);
      startEditing();

      press({ key: "k", ctrlKey: true });
      press({ key: "Escape" });

      // Still capturing — Escape only undid the prefix.
      expect(
        screen.getAllByText(/Listening for key combination/).length,
      ).toBeGreaterThan(0);
    });

    it("treats an ordinary chord as a complete binding", () => {
      render(<KeyboardShortcutsSettings />);
      startEditing();

      // Ctrl+J is not a sequence prefix, so it should not arm a second capture.
      press({ key: "j", ctrlKey: true });

      expect(screen.queryAllByText(/Now press the second key/)).toHaveLength(0);
    });
  });

  it("cancels the whole edit on Escape when nothing is pending", () => {
    render(<KeyboardShortcutsSettings />);
    startEditing();

    press({ key: "Escape" });

    expect(screen.queryAllByText(/Listening for key combination/)).toHaveLength(
      0,
    );
  });
});
