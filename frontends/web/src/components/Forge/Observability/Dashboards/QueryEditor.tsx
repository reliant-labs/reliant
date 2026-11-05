// Copyright (c) 2025 Reliant Labs

/**
 * A statement editor for sql and postgres panels: Monaco (already in the
 * bundle for the file viewer, loaded on first use here), with a monospace
 * textarea standing in while it loads, so the statement is editable at once.
 *
 *   ⌘/Ctrl+Enter   runs the statement (`onRun`)
 *   insert(text)   puts text at the caret, replacing the selection: the
 *                  schema browser's columns and parameters go where the
 *                  author is typing.
 */

import { forwardRef, lazy, Suspense, useEffect, useImperativeHandle, useRef, type Ref } from "react";
import type { OnMount } from "@monaco-editor/react";

import { configureMonacoTheme, getCurrentMonacoTheme, MONACO_FONT_FAMILY } from "@/lib/monacoTheme";
import { cn } from "@/lib/utils";

const MonacoEditor = lazy(() => import("@monaco-editor/react").then((m) => ({ default: m.Editor })));

export interface QueryEditorHandle {
  insert: (text: string) => void;
}

export interface QueryEditorProps {
  value: string;
  onChange: (value: string) => void;
  onRun?: () => void;
  /** Monaco's language id: "sql" (ClickHouse) or "pgsql". */
  language: "sql" | "pgsql";
  label: string;
  readOnly?: boolean;
  testId?: string;
}

const HEIGHT = 200;
const FRAME = "overflow-hidden rounded-md border border-border/60 bg-surface-sunken focus-within:ring-2 focus-within:ring-ring";

type MonacoEditorInstance = Parameters<OnMount>[0];

export const QueryEditor = forwardRef(function QueryEditor({ value, onChange, onRun, language, label, readOnly = false, testId }: QueryEditorProps, ref: Ref<QueryEditorHandle>) {
  const editor = useRef<MonacoEditorInstance | null>(null);
  const namespace = useRef<Parameters<OnMount>[1] | null>(null);
  const textarea = useRef<HTMLTextAreaElement | null>(null);
  // The handlers Monaco captured at mount must see the latest props.
  const latest = useRef({ onRun, onChange });
  latest.current = { onRun, onChange };

  useImperativeHandle(ref, () => ({
    insert(text: string) {
      const monaco = editor.current;
      if (monaco) {
        const selection = monaco.getSelection();
        if (selection) monaco.executeEdits("schema-browser", [{ range: selection, text, forceMoveMarkers: true }]);
        monaco.focus();
        return;
      }
      const area = textarea.current;
      const start = area?.selectionStart ?? value.length;
      const end = area?.selectionEnd ?? value.length;
      onChange(value.slice(0, start) + text + value.slice(end));
      requestAnimationFrame(() => {
        area?.focus();
        area?.setSelectionRange(start + text.length, start + text.length);
      });
    },
  }));

  useEffect(() => {
    const retheme = () => {
      const monaco = namespace.current;
      if (!monaco) return;
      configureMonacoTheme(monaco);
      monaco.editor.setTheme(getCurrentMonacoTheme());
    };
    window.addEventListener("theme-applied", retheme);
    return () => window.removeEventListener("theme-applied", retheme);
  }, []);

  const fallback = (
    <textarea
      ref={textarea}
      aria-label={label}
      value={value}
      readOnly={readOnly}
      spellCheck={false}
      onChange={(e) => onChange(e.target.value)}
      onKeyDown={(e) => {
        if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
          e.preventDefault();
          onRun?.();
        }
      }}
      style={{ height: HEIGHT }}
      className="block w-full resize-y bg-transparent p-3 font-mono text-xs leading-relaxed text-ink focus:outline-none"
      data-testid={testId}
    />
  );

  return (
    <div className={cn(FRAME)} data-testid={testId ? `${testId}-frame` : undefined}>
      <Suspense fallback={fallback}>
        <MonacoEditor
          height={HEIGHT}
          language={language}
          value={value}
          theme={getCurrentMonacoTheme()}
          loading={fallback}
          onChange={(next) => latest.current.onChange(next ?? "")}
          onMount={(instance, monaco) => {
            editor.current = instance;
            namespace.current = monaco;
            configureMonacoTheme(monaco);
            monaco.editor.setTheme(getCurrentMonacoTheme());
            instance.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.Enter, () => latest.current.onRun?.());
          }}
          options={{
            readOnly,
            ariaLabel: label,
            minimap: { enabled: false },
            lineNumbers: "off",
            folding: false,
            glyphMargin: false,
            lineDecorationsWidth: 8,
            scrollBeyondLastLine: false,
            wordWrap: "on",
            fontFamily: MONACO_FONT_FAMILY,
            fontSize: 12,
            renderLineHighlight: "none",
            overviewRulerLanes: 0,
            automaticLayout: true,
            padding: { top: 8, bottom: 8 },
          }}
        />
      </Suspense>
    </div>
  );
});
