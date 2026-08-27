import { diffLines } from 'diff';
import { memo, useEffect, useMemo, useState, useRef, useCallback } from 'react';
import Prism from 'prismjs';
import 'prismjs/components/prism-typescript';
import 'prismjs/components/prism-javascript';
import 'prismjs/components/prism-jsx';
import 'prismjs/components/prism-tsx';
import 'prismjs/components/prism-python';
import 'prismjs/components/prism-bash';
import 'prismjs/components/prism-json';
import 'prismjs/components/prism-yaml';
import 'prismjs/components/prism-markdown';
import 'prismjs/components/prism-css';
import 'prismjs/components/prism-go';
import 'prismjs/components/prism-rust';
import 'prismjs/components/prism-sql';
import { reliantDarkTheme, reliantLightTheme } from '../../lib/prismTheme';

interface LightweightDiffViewerProps {
  original: string;
  modified: string;
  filename?: string;
  maxHeight?: number;
  showLineNumbers?: boolean;
  noBorder?: boolean; // Remove border when used inside a parent with its own border
}

// Pre-computed diff line data for virtualization
interface DiffLine {
  key: string;
  highlightedHtml: string;
  bgColor: string;
  borderColor: string;
  originalNum: number;
  modifiedNum: number;
}

// LRU cache for syntax highlighting - prevents re-highlighting same lines
const HIGHLIGHT_CACHE_SIZE = 10000;
const highlightCache = new Map<string, string>();

const addToHighlightCache = (key: string, value: string): void => {
  // Evict oldest entries if at capacity
  if (highlightCache.size >= HIGHLIGHT_CACHE_SIZE) {
    const firstKey = highlightCache.keys().next().value;
    if (firstKey) highlightCache.delete(firstKey);
  }
  highlightCache.set(key, value);
};

const getFromHighlightCache = (key: string): string | undefined => {
  const value = highlightCache.get(key);
  if (value !== undefined) {
    // Move to end (most recently used)
    highlightCache.delete(key);
    highlightCache.set(key, value);
  }
  return value;
};

// Virtualization threshold - use virtual scrolling for diffs larger than this
const VIRTUALIZATION_THRESHOLD = 100;
// Fixed px, and deliberately not tied to the Appearance font-size preference:
// the virtualizer converts scrollTop into a line index by dividing by this
// value, so a line height that changed with the preference would desync the
// windowing math from what is actually painted. Making this viewer scale means
// measuring the real line box and feeding that to the virtualizer — a separate
// change from the UI-wide font-size work.
const LINE_HEIGHT = 16;
const OVERSCAN = 10; // Render extra lines above/below viewport for smooth scrolling

// Detect language from filename
const detectLanguage = (filename?: string): string => {
  if (!filename) return 'text';

  const ext = filename.split('.').pop()?.toLowerCase();
  const langMap: Record<string, string> = {
    'ts': 'typescript',
    'tsx': 'tsx',
    'js': 'javascript',
    'jsx': 'jsx',
    'py': 'python',
    'sh': 'bash',
    'bash': 'bash',
    'json': 'json',
    'yaml': 'yaml',
    'yml': 'yaml',
    'md': 'markdown',
    'css': 'css',
    'scss': 'css',
    'go': 'go',
    'rs': 'rust',
    'sql': 'sql',
    'html': 'markup',
    'xml': 'markup',
  };

  return langMap[ext || ''] || 'text';
};

// Get color for a token type from theme
const getTokenColor = (types: string[], isDark: boolean): string | undefined => {
  const theme = isDark ? reliantDarkTheme : reliantLightTheme;
  
  for (const style of theme.styles) {
    if (style.types.some(type => types.includes(type))) {
      return style.style.color as string;
    }
  }
  
  return theme.plain.color;
};

// Escape HTML entities
const escapeHtml = (text: string): string => {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#039;');
};

// Stringify tokens with inline styles instead of classes
const stringifyTokens = (tokens: Array<string | Prism.Token>, theme: typeof reliantDarkTheme): string => {
  return tokens.map(token => {
    if (typeof token === 'string') {
      return escapeHtml(token);
    }
    
    const types = Array.isArray(token.type) ? token.type : [token.type];
    const color = getTokenColor(types, theme === reliantDarkTheme);
    
    let style = '';
    if (color) {
      style = `color: ${color};`;
    }
    
    // Handle nested tokens
    const content = typeof token.content === 'string'
      ? escapeHtml(token.content)
      : stringifyTokens(Array.isArray(token.content) ? token.content : [token.content], theme);
    
    return `<span style="${style}">${content}</span>`;
  }).join('');
};

// Apply syntax highlighting to a line of code with theme colors (with caching)
const highlightLine = (line: string, language: string, isDark: boolean): string => {
  if (language === 'text' || !line.trim()) {
    return escapeHtml(line);
  }

  // Check cache first
  const cacheKey = `${language}:${isDark ? 'd' : 'l'}:${line}`;
  const cached = getFromHighlightCache(cacheKey);
  if (cached !== undefined) {
    return cached;
  }

  try {
    const grammar = Prism.languages[language];
    if (!grammar) return escapeHtml(line);

    const tokens = Prism.tokenize(line, grammar);
    
    // Convert tokens to HTML with inline styles
    const theme = isDark ? reliantDarkTheme : reliantLightTheme;
    const result = stringifyTokens(tokens, theme);
    
    // Cache the result
    addToHighlightCache(cacheKey, result);
    
    return result;
  } catch {
    return escapeHtml(line);
  }
};

/**
 * Reduces context lines in diff to only show a few lines around changes
 * Similar to git diff -U3 (3 lines of context)
 */
const reduceContext = (changes: ReturnType<typeof diffLines>, contextLines = 3): ReturnType<typeof diffLines> => {
  const result: ReturnType<typeof diffLines> = [];
  
  for (let i = 0; i < changes.length; i++) {
    const change = changes[i];
    
    // Always include added/removed lines
    if (change.added || change.removed) {
      result.push(change);
      continue;
    }
    
    // This is a context chunk - decide how much to keep
    const lines = change.value.split('\n');
    if (lines[lines.length - 1] === '') lines.pop();
    
    const prevChange = changes[i - 1];
    const nextChange = changes[i + 1];
    const hasPrev = prevChange && (prevChange.added || prevChange.removed);
    const hasNext = nextChange && (nextChange.added || nextChange.removed);
    
    // If no adjacent changes, skip this context entirely (unless it's the only chunk)
    if (!hasPrev && !hasNext && changes.length > 1) {
      continue;
    }
    
    // Calculate how many lines to show
    let startLine = 0;
    let endLine = lines.length;
    
    if (hasPrev && hasNext) {
      // Show context before and after
      const totalContext = contextLines * 2;
      if (lines.length > totalContext) {
        startLine = 0;
        endLine = Math.min(contextLines, lines.length);
        
        // Add ellipsis marker if we're skipping middle lines
        if (lines.length > contextLines * 2) {
          const firstPart = lines.slice(startLine, contextLines).join('\n');
          const lastPart = lines.slice(-contextLines).join('\n');
          result.push({ value: firstPart + '\n', added: false, removed: false, count: contextLines });
          result.push({ value: '\n', added: false, removed: false, count: 1 });
          result.push({ value: lastPart + '\n', added: false, removed: false, count: contextLines });
          continue;
        }
      }
    } else if (hasPrev) {
      // Show context after previous change
      if (lines.length > contextLines) {
        endLine = contextLines;
      }
    } else if (hasNext) {
      // Show context before next change
      if (lines.length > contextLines) {
        startLine = Math.max(0, lines.length - contextLines);
      }
    }
    
    // Add the reduced context
    const contextLines_arr = lines.slice(startLine, endLine);
    if (contextLines_arr.length > 0) {
      result.push({ value: contextLines_arr.join('\n') + '\n', added: false, removed: false, count: contextLines_arr.length });
    }
  }
  
  return result;
};

// Color schemes
const getDarkColors = () => ({
  // VSCode Dark+ uses very subtle overlays on dark background
  added: '#1a4d2e',                      // Dark green background (not rgba overlay)
  addedBorder: '#28a745',                // Green border
  addedText: '#D4D4D4',                  // Standard text color
  removed: '#5c1f1f',                    // Dark red background (not rgba overlay)
  removedBorder: '#d73a49',              // Red border
  removedText: '#D4D4D4',                // Standard text color
  context: '#1E1E1E',                    // Dark background for context
  lineNumber: '#858585',                 // Line number color
  text: '#D4D4D4',                       // Default text
  background: '#1E1E1E',                 // Editor background
});

const getLightColors = () => ({
  // VSCode Light theme
  added: '#e6ffec',                      // Light green background
  addedBorder: 'rgba(46, 160, 67, 0.3)',
  addedText: '#000000',
  removed: '#ffebe9',                    // Light red background
  removedBorder: 'rgba(248, 81, 73, 0.3)',
  removedText: '#000000',
  context: '#FFFFFF',                    // White background for context
  lineNumber: '#237893',
  text: '#000000',
  background: '#FFFFFF',
});

/**
 * LightweightDiffViewer - A simple diff viewer with syntax highlighting
 * Markdown-like appearance with VSCode-style colors
 *
 * Features:
 * - Syntax highlighting with Prism.js (cached for performance)
 * - Side-by-side diff view
 * - Line numbers
 * - Theme-aware (auto-switches with app theme)
 * - GitHub-style diff colors
 * - Minimal bundle size (~60KB vs Monaco's 5-10MB)
 * - Smart context reduction (only shows 3 lines of context around changes)
 * - Virtualized rendering for large diffs (>100 lines)
 * - Memoized diff computation and line rendering
 */
export const LightweightDiffViewer = memo(function LightweightDiffViewer({
  original,
  modified,
  filename,
  maxHeight = 250,
  showLineNumbers = true,
  noBorder: _noBorder = false,
}: LightweightDiffViewerProps) {
  const [isDark, setIsDark] = useState(
    document.documentElement.classList.contains('dark')
  );
  const [scrollTop, setScrollTop] = useState(0);
  const containerRef = useRef<HTMLPreElement>(null);

  // Listen for theme changes
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

  // Memoize language detection
  const language = useMemo(() => detectLanguage(filename), [filename]);

  // Memoize color scheme
  const colors = useMemo(() => isDark ? getDarkColors() : getLightColors(), [isDark]);

  // Memoize diff computation - this is the expensive part
  const changes = useMemo(() => {
    const rawChanges = diffLines(original, modified);
    return reduceContext(rawChanges, 3);
  }, [original, modified]);

  // Pre-compute all line data with memoized highlighting
  const { allLines, totalLines } = useMemo(() => {
    const lines: DiffLine[] = [];
    let originalLineNum = 1;
    let modifiedLineNum = 1;

    changes.forEach((change, changeIdx) => {
      const changeLines = change.value.split('\n');
      // Remove last empty line if it exists
      if (changeLines[changeLines.length - 1] === '') {
        changeLines.pop();
      }

      changeLines.forEach((line, lineIdx) => {
        let bgColor = colors.context;
        let borderColor = 'transparent';
        let originalNum = originalLineNum;
        let modifiedNum = modifiedLineNum;

        if (change.added) {
          bgColor = colors.added;
          borderColor = colors.addedBorder;
          originalNum = 0;
          modifiedNum = modifiedLineNum;
          modifiedLineNum++;
        } else if (change.removed) {
          bgColor = colors.removed;
          borderColor = colors.removedBorder;
          originalNum = originalLineNum;
          modifiedNum = 0;
          originalLineNum++;
        } else {
          originalNum = originalLineNum;
          modifiedNum = modifiedLineNum;
          originalLineNum++;
          modifiedLineNum++;
        }

        lines.push({
          key: `${changeIdx}-${lineIdx}`,
          highlightedHtml: highlightLine(line, language, isDark),
          bgColor,
          borderColor,
          originalNum,
          modifiedNum,
        });
      });
    });

    return { allLines: lines, totalLines: lines.length };
  }, [changes, colors, language, isDark]);

  // Handle scroll for virtualization
  const handleScroll = useCallback((e: React.UIEvent<HTMLPreElement>) => {
    setScrollTop(e.currentTarget.scrollTop);
  }, []);

  // Calculate virtualization parameters
  const shouldVirtualize = totalLines > VIRTUALIZATION_THRESHOLD;
  const viewportHeight = maxHeight;
  const visibleCount = Math.ceil(viewportHeight / LINE_HEIGHT) + OVERSCAN * 2;
  const startIndex = shouldVirtualize 
    ? Math.max(0, Math.floor(scrollTop / LINE_HEIGHT) - OVERSCAN)
    : 0;
  const endIndex = shouldVirtualize 
    ? Math.min(totalLines, startIndex + visibleCount)
    : totalLines;

  // Get visible lines (all lines if not virtualizing)
  const visibleLines = shouldVirtualize 
    ? allLines.slice(startIndex, endIndex)
    : allLines;

  // Calculate heights for virtualization
  const paddingTop = shouldVirtualize ? startIndex * LINE_HEIGHT : 0;
  const paddingBottom = shouldVirtualize 
    ? Math.max(0, (totalLines - endIndex) * LINE_HEIGHT)
    : 0;

  // Calculate dynamic height (for non-virtualized or smaller diffs)
  const padding = 8;
  const calculatedHeight = Math.min(totalLines * LINE_HEIGHT + padding, maxHeight);

  return (
    <div
      className="diff-viewer overflow-hidden"
      style={{
        maxHeight: `${calculatedHeight}px`,
        backgroundColor: colors.background,
      }}
    >
      <pre
        ref={containerRef}
        onScroll={shouldVirtualize ? handleScroll : undefined}
        style={{
          margin: 0,
          padding: '2px 0',
          fontSize: '11px',
          lineHeight: `${LINE_HEIGHT}px`,
          fontFamily: 'var(--font-mono)',
          overflow: 'auto',
          maxHeight: `${calculatedHeight}px`,
          backgroundColor: colors.background,
          color: colors.text,
        }}
      >
        {/* Virtual scroll spacer - top */}
        {shouldVirtualize && paddingTop > 0 && (
          <div style={{ height: paddingTop }} />
        )}
        
        {/* Render visible lines */}
        {visibleLines.map((line) => (
          <div
            key={line.key}
            style={{
              display: 'flex',
              minHeight: `${LINE_HEIGHT}px`,
              backgroundColor: line.bgColor,
              borderLeft: `3px solid ${line.borderColor}`,
            }}
          >
            {/* Original line number */}
            {showLineNumbers && (
              <span
                style={{
                  display: 'inline-block',
                  width: '2.5em',
                  userSelect: 'none',
                  opacity: line.originalNum ? 0.5 : 0.2,
                  textAlign: 'right',
                  paddingRight: '0.5em',
                  paddingLeft: '0.5em',
                  color: colors.lineNumber,
                }}
              >
                {line.originalNum || ''}
              </span>
            )}
            {/* Modified line number */}
            {showLineNumbers && (
              <span
                style={{
                  display: 'inline-block',
                  width: '2.5em',
                  userSelect: 'none',
                  opacity: line.modifiedNum ? 0.5 : 0.2,
                  textAlign: 'right',
                  paddingRight: '0.5em',
                  color: colors.lineNumber,
                }}
              >
                {line.modifiedNum || ''}
              </span>
            )}
            {/* Line content with syntax highlighting */}
            <span
              className="token-line"
              style={{
                paddingLeft: '0.75em',
                paddingRight: '0.5em',
                flex: 1,
                whiteSpace: 'pre',
              }}
              dangerouslySetInnerHTML={{
                __html: line.highlightedHtml
              }}
            />
          </div>
        ))}
        
        {/* Virtual scroll spacer - bottom */}
        {shouldVirtualize && paddingBottom > 0 && (
          <div style={{ height: paddingBottom }} />
        )}
      </pre>
    </div>
  );
});
