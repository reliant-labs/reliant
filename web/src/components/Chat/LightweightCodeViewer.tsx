import { Highlight } from 'prism-react-renderer';
import { useEffect, useState } from 'react';
import { reliantDarkTheme, reliantLightTheme } from '../../lib/prismTheme';

interface LightweightCodeViewerProps {
  content: string;
  language?: string;
  filename?: string;
  maxHeight?: number;
  minHeight?: number;
  showLineNumbers?: boolean;
  noBorder?: boolean; // Remove border when used inside a parent with its own border
  wordWrap?: boolean; // Enable word wrapping for long lines
}

/**
 * LightweightCodeViewer - A Prism-based code viewer that looks like Monaco
 * but is 100x smaller and faster for read-only displays
 * 
 * Features:
 * - Syntax highlighting (same colors as Monaco/VSCode)
 * - Line numbers
 * - Theme-aware (auto-switches with app theme)
 * - Dynamic height calculation
 * - Minimal bundle size (~50KB vs Monaco's 5-10MB)
 */
export function LightweightCodeViewer({
  content,
  language = 'plaintext',
  filename: _filename,
  maxHeight = 400,
  minHeight = 0,
  showLineNumbers = true,
  noBorder = false,
  wordWrap = false,
}: LightweightCodeViewerProps) {
  const [isDark, setIsDark] = useState(
    document.documentElement.classList.contains('dark')
  );

  // Listen for theme changes (same events as Monaco)
  useEffect(() => {
    const handleThemeChange = () => {
      setIsDark(document.documentElement.classList.contains('dark'));
    };

    window.addEventListener('theme-applied', handleThemeChange);
    window.addEventListener('appearance-updated', handleThemeChange);

    return () => {
      window.removeEventListener('theme-applied', handleThemeChange);
      window.removeEventListener('appearance-updated', handleThemeChange);
    };
  }, []);

  const theme = isDark ? reliantDarkTheme : reliantLightTheme;

  // Calculate height based on content lines (same as Monaco)
  // When wordWrap is enabled, we use auto height since we can't predict wrapped lines
  const lines = content.split('\n').length;
  const lineHeight = 16; // Same as Monaco
  const padding = 8; // Same as Monaco
  const contentHeight = lines * lineHeight + padding;
  const calculatedHeight = wordWrap 
    ? undefined // Let CSS handle it with auto height
    : Math.min(
        minHeight > 0 ? Math.max(contentHeight, minHeight) : contentHeight,
        maxHeight
      );

  // Map common language aliases to Prism language identifiers
  const getPrismLanguage = (lang: string): string => {
    const langMap: Record<string, string> = {
      'typescript': 'typescript',
      'javascript': 'javascript',
      'python': 'python',
      'shell': 'bash',
      'bash': 'bash',
      'sh': 'bash',
      'json': 'json',
      'yaml': 'yaml',
      'yml': 'yaml',
      'markdown': 'markdown',
      'md': 'markdown',
      'html': 'html',
      'css': 'css',
      'sql': 'sql',
      'go': 'go',
      'rust': 'rust',
      'java': 'java',
      'cpp': 'cpp',
      'c': 'c',
      'csharp': 'csharp',
      'php': 'php',
      'ruby': 'ruby',
      'swift': 'swift',
      'kotlin': 'kotlin',
      'xml': 'xml',
      'plaintext': 'text',
    };
    return langMap[lang.toLowerCase()] || 'text';
  };

  const prismLanguage = getPrismLanguage(language);

  const bgColor = isDark ? '#1E1E1E' : '#FFFFFF'; // Monaco's exact background colors
  const lineNumberColor = isDark ? '#858585' : '#237893'; // Monaco's line number colors
  const lineNumberWidth = `${Math.max(2, String(lines).length) + 1}em`;

  return (
    <div
      className={`monaco-viewer-container ${noBorder ? '' : 'rounded-md border border-border'}`}
      style={{
        ...(calculatedHeight !== undefined ? { height: `${calculatedHeight}px` } : { maxHeight: `${maxHeight}px` }),
        backgroundColor: bgColor,
        overflow: 'hidden',
        overflowY: 'auto',
      }}
    >
      <Highlight theme={theme} code={content} language={prismLanguage}>
        {({ tokens, getLineProps, getTokenProps }) => (
          <pre
            style={{
              margin: 0,
              padding: '2px 0',
              fontSize: '11px',
              lineHeight: '16px',
              fontFamily: 'var(--font-mono)',
              overflowX: wordWrap ? 'hidden' : 'auto',
              overflowY: wordWrap ? 'auto' : 'auto',
              ...(wordWrap ? {} : { height: '100%' }),
              backgroundColor: bgColor,
              color: isDark ? '#D4D4D4' : '#000000',
            }}
          >
            {tokens.map((line, i) => (
              <div
                key={i}
                {...getLineProps({ line })}
                style={{
                  display: 'flex',
                  minHeight: '16px',
                }}
              >
                {showLineNumbers ? (
                  <span
                    style={{
                      display: 'inline-block',
                      width: lineNumberWidth,
                      userSelect: 'none',
                      textAlign: 'right',
                      paddingRight: '1em',
                      paddingLeft: '0.5em',
                      color: lineNumberColor,
                    }}
                  >
                    {i + 1}
                  </span>
                ) : (
                  <span style={{ paddingLeft: '8px' }} />
                )}
                <span style={{ 
                  paddingRight: '0.5em', 
                  flex: 1,
                  ...(wordWrap ? {
                    whiteSpace: 'pre-wrap',
                    wordBreak: 'break-word',
                    overflowWrap: 'break-word',
                  } : {})
                }}>
                  {line.map((token, key) => (
                    <span key={key} {...getTokenProps({ token })} />
                  ))}
                </span>
              </div>
            ))}
          </pre>
        )}
      </Highlight>
    </div>
  );
}

/**
 * InlineLightweightCodeViewer - For inline code blocks without container styling
 */
export function InlineLightweightCodeViewer({
  content,
  language,
  maxHeight = 200,
}: Omit<LightweightCodeViewerProps, 'minHeight' | 'showLineNumbers'>) {
  return (
    <div className="inline-code-viewer">
      <LightweightCodeViewer
        content={content}
        language={language}
        maxHeight={maxHeight}
        minHeight={60}
        showLineNumbers={true}
      />
    </div>
  );
}
