import { useState, type ReactNode } from 'react';
import Tooltip from "../forge-ui/tooltip";
import { Copy, Check } from 'lucide-react';
import { useSurface } from '../../lib/surfaceContext';
import { cn } from '../../lib/utils';

interface CodeBlockProps {
  language?: string;
  children: ReactNode;
  className?: string;
}

/**
 * A block longer than this many lines opens clipped (to `CLIPPED_BODY_CLASS`,
 * about 16 lines) behind "Show all N lines": a long listing should not push
 * the rest of the answer a screen away.
 */
export const LONG_CODE_BLOCK_LINES = 24;
const CLIPPED_BODY_CLASS = 'max-h-[20rem]';

/**
 * CodeBlock - a fenced code block in an answer: a header with the language
 * and a copy button, then the code. Used by MarkdownRenderer.
 */
export function CodeBlock({ language, children, className }: CodeBlockProps) {
  const [copied, setCopied] = useState(false);
  const [showAll, setShowAll] = useState(false);
  const isMobile = useSurface() === 'mobile';

  const textContent = extractTextContent(children);
  const lineCount = textContent.replace(/\n$/, '').split('\n').length;
  const isLong = lineCount > LONG_CODE_BLOCK_LINES;
  const clipped = isLong && !showAll;

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(textContent);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch (error) {
      console.error('Failed to copy code:', error);
    }
  };

  return (
    <div className="not-prose my-3 w-full min-w-0 max-w-full overflow-hidden rounded-xl border border-border bg-card">
      {/* Controls stay outside the horizontal scroller, so copy remains reachable. */}
      <div
        className={cn(
          'flex items-center justify-between gap-2 border-b border-border-subtle pl-3 pr-1 text-meta text-muted-foreground',
          isMobile ? 'h-11' : 'h-9',
        )}
      >
        <span className="truncate">{language || 'code'}</span>
        <Tooltip content={copied ? 'Copied' : 'Copy code'} side="bottom" delay={300} triggerClassName="inline-flex">
          <button
            onClick={handleCopy}
            type="button"
            className={cn(
              'flex items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              isMobile ? 'min-h-[44px] min-w-[44px]' : 'size-7',
            )}
            aria-label={copied ? 'Copied' : 'Copy code'}
          >
            {copied ? (
              <Check className="h-3.5 w-3.5 text-success-ink" />
            ) : (
              <Copy className="h-3.5 w-3.5" />
            )}
          </button>
        </Tooltip>
      </div>
      <div className={cn('relative', clipped && cn(CLIPPED_BODY_CLASS, 'overflow-hidden'))}>
        <pre tabIndex={isMobile ? 0 : undefined} aria-label={isMobile ? `${language || 'Code'} code block` : undefined}
          className={cn("relative w-full max-w-full overflow-x-auto bg-transparent text-[var(--code-fg)]", isMobile ? "whitespace-pre" : "whitespace-pre-wrap break-words")}>
          <code className={cn(`language-${language || ''} block px-4 py-3 font-mono text-code`, isMobile ? "whitespace-pre" : "whitespace-pre-wrap break-words", className)}>
            {children}
          </code>
        </pre>
        {clipped && (
          <div aria-hidden="true" className="pointer-events-none absolute inset-x-0 bottom-0 h-16 bg-gradient-to-t from-card" />
        )}
      </div>
      {isLong && (
        <button
          type="button"
          aria-expanded={showAll}
          onClick={() => setShowAll((open) => !open)}
          className="flex h-8 w-full items-center justify-center border-t border-border-subtle text-meta text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
        >
          {showAll ? 'Show less' : `Show all ${lineCount} lines`}
        </button>
      )}
    </div>
  );
}

/**
 * Recursively extract text content from React children
 */
function extractTextContent(children: ReactNode): string {
  if (typeof children === 'string') {
    return children;
  }
  if (typeof children === 'number') {
    return String(children);
  }
  if (Array.isArray(children)) {
    return children.map(extractTextContent).join('');
  }
  if (children && typeof children === 'object' && 'props' in children) {
    return extractTextContent((children as { props: { children: React.ReactNode } }).props.children);
  }
  return '';
}
