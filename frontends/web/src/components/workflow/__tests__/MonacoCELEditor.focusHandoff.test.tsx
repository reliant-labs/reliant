// Copyright (c) 2025 Reliant Labs

/**
 * Monaco loads after a field first renders, so a field focused as its panel
 * opens — "Go to problem", a problem picked in the problems list — is focused
 * while it is still the plain fallback input. The editor that replaces it must
 * take that focus over, or the jump lands nowhere.
 */

import { act, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const monacoState = vi.hoisted(() => ({ value: null as unknown, listeners: new Set<() => void>() }));
vi.mock("../../../lib/monacoManager", async () => {
  const React = await import("react");
  return {
    useMonaco: () => {
      const [monaco, setMonaco] = React.useState(monacoState.value);
      React.useEffect(() => {
        const listener = () => setMonaco(monacoState.value);
        monacoState.listeners.add(listener);
        return () => {
          monacoState.listeners.delete(listener);
        };
      }, []);
      return monaco;
    },
  };
});
vi.mock("../../../lib/monaco-cel-completions", () => ({ registerCELEditorContext: () => ({ dispose() {} }) }));
vi.mock("../../../lib/cel-completion-service", () => ({ ensureCELCompletionsCached: () => {} }));
vi.mock("../../../lib/monacoTheme", () => ({
  getCurrentMonacoTheme: () => "vs",
  configureMonacoTheme: () => {},
  MONACO_FONT_FAMILY: "monospace",
}));

import { MonacoCELEditor } from "../MonacoCELEditor";

/** A Monaco whose editor renders the focusable textarea the real one does. */
function fakeMonaco() {
  const focus = vi.fn();
  const monaco = {
    editor: {
      create: (container: HTMLElement) => {
        const textarea = document.createElement("textarea");
        textarea.className = "inputarea";
        container.appendChild(textarea);
        return {
          focus: () => {
            focus();
            textarea.focus();
          },
          getModel: () => null,
          getValue: () => "",
          setValue: () => {},
          updateOptions: () => {},
          onDidChangeModelContent: () => ({ dispose() {} }),
          onDidFocusEditorText: () => ({ dispose() {} }),
          onDidBlurEditorText: () => ({ dispose() {} }),
          onKeyDown: () => ({ dispose() {} }),
          dispose: () => textarea.remove(),
        };
      },
      setTheme: () => {},
    },
    KeyCode: { Enter: 3 },
  };
  return { monaco, focus };
}

function loadMonaco(monaco: unknown) {
  act(() => {
    monacoState.value = monaco;
    monacoState.listeners.forEach((listener) => listener());
  });
}

describe("MonacoCELEditor focus handoff", () => {
  it("gives the editor the focus the fallback input had when Monaco arrived", () => {
    monacoState.value = null;
    render(<MonacoCELEditor id="system_prompt" value="{{ inputs.ch }}" onChange={() => {}} multiline />);
    const fallback = screen.getByRole("textbox");
    act(() => fallback.focus());
    expect(fallback).toHaveFocus();

    const { monaco, focus } = fakeMonaco();
    loadMonaco(monaco);

    expect(focus).toHaveBeenCalledTimes(1);
    expect(document.getElementById("system_prompt")).toHaveFocus();
  });

  it("takes no focus when the fallback did not have it", () => {
    monacoState.value = null;
    render(<MonacoCELEditor id="system_prompt" value="" onChange={() => {}} />);

    const { monaco, focus } = fakeMonaco();
    loadMonaco(monaco);

    expect(focus).not.toHaveBeenCalled();
  });
});
