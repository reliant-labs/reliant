import { Editor } from '@monaco-editor/react';
import { getMonacoLanguage, configureMonacoTheme, getCurrentMonacoTheme } from '../../lib/monacoTheme';
import { useEffect, useLayoutEffect, useRef } from 'react';
import { logger } from '../../lib/logger';
import { tabSwitchProfiler } from '../../lib/tabSwitchProfiler';

interface MonacoViewerProps {
  content: string;
  language?: string;
  filename?: string;
  maxHeight?: number;
  minHeight?: number;
  readOnly?: boolean;
}

/**
 * MonacoViewer - A unified component for displaying code/text content with Monaco editor
 * Provides syntax highlighting, line numbers, and proper theming
 */
export function MonacoViewer({
  content,
  language,
  filename,
  maxHeight = 400,
  minHeight = 0,
  readOnly = true,
}: MonacoViewerProps) {
  // PERFORMANCE: Track Monaco viewer mount timing
  const mountStartRef = useRef<number | undefined>(undefined);
  if (!mountStartRef.current && tabSwitchProfiler.isEnabled()) {
    mountStartRef.current = performance.now();
  }

  const monacoRef = useRef<any>(null);
  const editorRef = useRef<any>(null);

  // Listen for theme changes
  useEffect(() => {
    const handleThemeChange = () => {
      if (monacoRef.current) {
        // Reconfigure theme to pick up light/dark mode changes
        configureMonacoTheme(monacoRef.current);
        const themeName = getCurrentMonacoTheme();
        monacoRef.current.editor.setTheme(themeName);
      }
    };

    window.addEventListener('theme-applied', handleThemeChange);
    window.addEventListener('appearance-updated', handleThemeChange);

    return () => {
      window.removeEventListener('theme-applied', handleThemeChange);
      window.removeEventListener('appearance-updated', handleThemeChange);
    };
  }, []);

  // Determine language from filename or explicit language prop
  const detectedLanguage = language || (filename ? getMonacoLanguage(filename) : 'plaintext');

  // Calculate height based on content lines
  const lines = content.split('\n').length;
  const lineHeight = 16; // Very compact line height
  const padding = 8; // Minimal padding for top/bottom
  const contentHeight = (lines * lineHeight) + padding;
  const calculatedHeight = Math.min(
    minHeight > 0 ? Math.max(contentHeight, minHeight) : contentHeight,
    maxHeight
  );

  // PERFORMANCE: Track when Monaco viewer finishes mounting
  useLayoutEffect(() => {
    if (mountStartRef.current && tabSwitchProfiler.isEnabled()) {
      const mountTime = performance.now() - mountStartRef.current;
      if (mountTime > 10) { // Only log slow mounts (>10ms)
        logger.info('[MonacoViewer] Mount complete', {
          mountTime: mountTime.toFixed(2) + 'ms',
          language: detectedLanguage,
          lines,
        });
      }
      mountStartRef.current = undefined;
    }
  });

  return (
    <div className="monaco-viewer-container rounded-md overflow-hidden">
      <Editor
        height={`${calculatedHeight}px`}
        language={detectedLanguage}
        value={content}
        theme={getCurrentMonacoTheme()}
        beforeMount={(monaco) => {
          monacoRef.current = monaco;
          // Ensure theme is configured
          configureMonacoTheme(monaco);
        }}
        onMount={(editor) => {
          editorRef.current = editor;
        }}
        options={{
          readOnly,
          minimap: { enabled: false },
          scrollBeyondLastLine: false,
          fontSize: 11,
          lineHeight: 16,
          lineNumbers: 'on',
          folding: true,
          wordWrap: 'on',
          automaticLayout: true,
          renderWhitespace: 'selection',
          bracketPairColorization: { enabled: true },
          scrollbar: {
            alwaysConsumeMouseWheel: false,
            vertical: 'auto',
            horizontal: 'auto',
          },
          padding: {
            top: 2,
            bottom: 2,
          },
        }}
      />
    </div>
  );
}

/**
 * Lightweight Monaco viewer for inline display (without border/container)
 */
export function InlineMonacoViewer({
  content,
  language,
  filename,
  maxHeight = 200,
}: Omit<MonacoViewerProps, 'minHeight' | 'readOnly'>) {
  return (
    <div className="inline-monaco-viewer">
      <MonacoViewer
        content={content}
        language={language}
        filename={filename}
        maxHeight={maxHeight}
        minHeight={60}
        readOnly={true}
      />
    </div>
  );
}
