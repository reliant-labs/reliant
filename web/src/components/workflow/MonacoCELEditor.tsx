import { useEffect, useRef, useCallback, useState } from 'react';
import { useMonaco } from '../../lib/monacoManager';
import { registerCELEditorContext } from '../../lib/monaco-cel-completions';
import type { CELCompletionContext } from '../../lib/monaco-cel-completions';
import { ensureCELCompletionsCached } from '../../lib/cel-completion-service';
import { getCurrentMonacoTheme, configureMonacoTheme } from '../../lib/monacoTheme';
import { cn } from '../../lib/utils';
import type { Monaco } from '@monaco-editor/react';

export interface MonacoCELEditorProps {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  multiline?: boolean;
  rows?: number;
  disabled?: boolean;
  pureExpression?: boolean;
  /** DOM id applied to the underlying textarea for `<label htmlFor>` association */
  id?: string;
  nodeIds?: string[];
  nodeTypeMap?: Record<string, string>;
  inputParams?: Record<string, { type: string; description?: string }>;
  celContext?: 'default' | 'loop_while' | 'edge_condition' | 'save_message' | 'thread';
  currentNodeType?: string;
  nodeDeclaredOutputs?: Record<string, string[]>;
  className?: string;
}

type IStandaloneCodeEditor = ReturnType<Monaco['editor']['create']>;

export function MonacoCELEditor({
  value,
  onChange,
  placeholder = 'Enter value or CEL expression...',
  multiline = false,
  rows = 2,
  disabled = false,
  pureExpression = false,
  id,
  nodeIds = [],
  nodeTypeMap = {},
  inputParams = {},
  celContext = 'default',
  currentNodeType,
  nodeDeclaredOutputs,
  className,
}: MonacoCELEditorProps) {
  const monaco = useMonaco();
  const containerRef = useRef<HTMLDivElement>(null);
  const editorRef = useRef<IStandaloneCodeEditor | null>(null);
  const completionDisposableRef = useRef<{ dispose(): void } | null>(null);
  const isUpdatingRef = useRef(false);
  const [isFocused, setIsFocused] = useState(false);
  const horizontalPadding = 10;

  // Keep onChange in a ref so the Monaco listener always calls the latest callback
  const onChangeRef = useRef(onChange);
  useEffect(() => {
    onChangeRef.current = onChange;
  });

  // Keep context in a ref so the completion provider callback always sees fresh values
  const contextRef = useRef<CELCompletionContext>({
    nodeIds,
    nodeTypeMap,
    inputParams,
    celContext,
    pureExpression,
    currentNodeType,
    nodeDeclaredOutputs,
  });

  // Update context ref when props change
  useEffect(() => {
    contextRef.current = {
      nodeIds,
      nodeTypeMap,
      inputParams,
      celContext,
      pureExpression,
      currentNodeType,
      nodeDeclaredOutputs,
    };
  }, [nodeIds, nodeTypeMap, inputParams, celContext, pureExpression, currentNodeType, nodeDeclaredOutputs]);

  const height = multiline ? rows * 20 : 32;

  // Ensure CEL completion data is fetched (singleton, safe to call multiple times)
  useEffect(() => {
    ensureCELCompletionsCached();
  }, []);

  // Create editor when Monaco is ready
  useEffect(() => {
    if (!monaco || !containerRef.current) return;

    const editor = monaco.editor.create(containerRef.current, {
      value,
      language: 'cel',
      theme: getCurrentMonacoTheme(),
      fontSize: 13,
      fontFamily: 'ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace',
      lineNumbers: 'off',
      minimap: { enabled: false },
      scrollBeyondLastLine: false,
      overviewRulerLanes: 0,
      wordWrap: multiline ? 'on' : 'off',
      folding: false,
      renderLineHighlight: 'none',
      glyphMargin: false,
      lineDecorationsWidth: horizontalPadding,
      lineNumbersMinChars: 0,
      automaticLayout: true,
      readOnly: disabled,
      scrollbar: {
        vertical: multiline ? 'auto' : 'hidden',
        horizontal: multiline ? 'hidden' : 'auto',
        useShadows: false,
        verticalScrollbarSize: 6,
        horizontalScrollbarSize: 6,
      },
      padding: { top: 6, bottom: 6 },
      contextmenu: false,
      suggest: {
        showIcons: true,
        showStatusBar: false,
        preview: false,
      },
      quickSuggestions: {
        other: true,
        comments: false,
        strings: true,
      },
      // Hide the vertical overview ruler on the right
      overviewRulerBorder: false,
      // Hide vertical guides
      guides: {
        indentation: false,
        bracketPairs: false,
      },
      // Prevent the find widget from showing
      find: {
        addExtraSpaceOnTop: false,
        autoFindInSelection: 'never',
        seedSearchStringFromSelection: 'never',
      },
      fixedOverflowWidgets: true,
    });

    editorRef.current = editor;

    // Apply DOM id to Monaco's internal textarea so `<label htmlFor>` (and
    // accessibility tools / `getByLabelText`) can locate the focusable input.
    if (id) {
      const textarea = containerRef.current.querySelector('textarea.inputarea');
      if (textarea instanceof HTMLTextAreaElement) {
        textarea.id = id;
      }
    }

    // Register completion context for this editor's model
    const modelUri = editor.getModel()?.uri.toString();
    if (modelUri) {
      completionDisposableRef.current = registerCELEditorContext(
        monaco,
        modelUri,
        () => contextRef.current,
      );
    }

    // Listen for content changes
    const contentDisposable = editor.onDidChangeModelContent(() => {
      if (isUpdatingRef.current) return;
      const currentValue = editor.getValue();
      onChangeRef.current(currentValue);
    });

    // Focus / blur tracking
    const focusDisposable = editor.onDidFocusEditorText(() => {
      setIsFocused(true);
    });

    const blurDisposable = editor.onDidBlurEditorText(() => {
      setIsFocused(false);
    });

    // Single-line mode: Enter key behavior
    let keyDisposable: { dispose(): void } | null = null;
    if (!multiline) {
      keyDisposable = editor.onKeyDown((e: import('monaco-editor').IKeyboardEvent) => {
        if (e.keyCode !== monaco.KeyCode.Enter) return;

        // If suggestion widget is visible, let Monaco handle it (accept suggestion)
        const suggestController = (editor as any).getContribution?.('editor.contrib.suggestController');
        if (suggestController) {
          const widget = suggestController.widget;
          if (widget?.value?.state === 3 /* State.Open */) {
            return;
          }
        }

        // Prevent newline insertion
        e.preventDefault();
        e.stopPropagation();

        // Blur the editor
        editor.trigger('keyboard', 'blur', null);
        (containerRef.current?.closest('form') as HTMLElement | null)
          ?.dispatchEvent(new Event('submit', { bubbles: true }));
        // Fallback: just blur
        if (document.activeElement instanceof HTMLElement) {
          document.activeElement.blur();
        }
      });
    }

    return () => {
      contentDisposable.dispose();
      focusDisposable.dispose();
      blurDisposable.dispose();
      keyDisposable?.dispose();
      completionDisposableRef.current?.dispose();
      completionDisposableRef.current = null;
      editor.dispose();
      editorRef.current = null;
    };
    // We intentionally only create the editor once when monaco loads.
    // Value sync is handled separately below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [monaco, multiline, disabled]);

  // Sync value prop → Monaco model (controlled component)
  useEffect(() => {
    const editor = editorRef.current;
    if (!editor) return;

    const currentValue = editor.getValue();
    if (currentValue !== value) {
      isUpdatingRef.current = true;
      editor.setValue(value);
      isUpdatingRef.current = false;
    }
  }, [value]);

  // Sync disabled prop → readOnly
  useEffect(() => {
    editorRef.current?.updateOptions({ readOnly: disabled });
  }, [disabled]);

  // Theme switching
  useEffect(() => {
    const handleThemeChange = () => {
      if (!monaco) return;
      configureMonacoTheme(monaco);
      const themeName = getCurrentMonacoTheme();
      monaco.editor.setTheme(themeName);
    };

    window.addEventListener('theme-applied', handleThemeChange);
    window.addEventListener('appearance-updated', handleThemeChange);
    return () => {
      window.removeEventListener('theme-applied', handleThemeChange);
      window.removeEventListener('appearance-updated', handleThemeChange);
    };
  }, [monaco]);

  const showPlaceholder = !isFocused && !value;

  // Fallback while Monaco loads
  if (!monaco) {
    return (
      <FallbackInput
        value={value}
        onChange={onChange}
        placeholder={placeholder}
        multiline={multiline}
        rows={rows}
        disabled={disabled}
        className={className}
        id={id}
      />
    );
  }

  return (
    <div
      className={cn(
        'relative rounded-[6px] border text-xs',
        'bg-[hsl(var(--config-input-bg))]',
        isFocused && 'border-ring shadow-[0_0_0_2px_hsl(var(--ring)/0.15)]',
        disabled && 'opacity-50 cursor-not-allowed',
        !isFocused && !disabled && 'border-[hsl(var(--config-input-border))] hover:border-ring/50',
        className,
      )}
      style={{ height }}
    >
      <div
        ref={containerRef}
        className="h-full w-full overflow-hidden rounded-[6px]"
      />
      {showPlaceholder && (
        <div
          className="absolute inset-0 flex items-center text-muted-foreground pointer-events-none select-none text-xs font-mono truncate"
          style={{ paddingLeft: horizontalPadding, paddingRight: horizontalPadding }}
          aria-hidden
        >
          {placeholder}
        </div>
      )}
    </div>
  );
}

/** Simple textarea/input fallback while Monaco is loading */
function FallbackInput({
  value,
  onChange,
  placeholder,
  multiline,
  rows,
  disabled,
  className,
  id,
}: Pick<
  MonacoCELEditorProps,
  'value' | 'onChange' | 'placeholder' | 'multiline' | 'rows' | 'disabled' | 'className' | 'id'
>) {
  const handleChange = useCallback(
    (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => {
      onChange(e.target.value);
    },
    [onChange],
  );

  const classes = cn(
    'w-full px-2.5 py-1.5 border rounded-[6px] text-xs font-mono',
    'focus:outline-none focus:border-ring focus:shadow-[0_0_0_2px_hsl(var(--ring)/0.15)]',
    'bg-[hsl(var(--config-input-bg))] text-foreground border-[hsl(var(--config-input-border))]',
    disabled && 'opacity-50 cursor-not-allowed',
    className,
  );

  if (multiline) {
    return (
      <textarea
        id={id}
        value={value}
        onChange={handleChange}
        placeholder={placeholder}
        rows={rows}
        disabled={disabled}
        className={classes}
      />
    );
  }

  return (
    <input
      id={id}
      type="text"
      value={value}
      onChange={handleChange}
      placeholder={placeholder}
      disabled={disabled}
      className={classes}
    />
  );
}