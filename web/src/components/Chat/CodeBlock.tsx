import { useState, type ReactNode } from 'react';
import { Copy, Check } from 'lucide-react';

interface CodeBlockProps {
  language?: string;
  children: ReactNode;
  className?: string;
}

/**
 * CodeBlock - A code block component with copy-to-clipboard functionality
 * Used by MarkdownRenderer for fenced code blocks
 */
export function CodeBlock({ language, children, className }: CodeBlockProps) {
  const [copied, setCopied] = useState(false);

  const handleCopy = async () => {
    // Extract text content from children
    const textContent = extractTextContent(children);
    
    try {
      await navigator.clipboard.writeText(textContent);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch (error) {
      console.error('Failed to copy code:', error);
    }
  };

  return (
    <div className="not-prose relative my-4 w-full group/codeblock">
      <pre className="hljs relative w-full overflow-x-auto rounded-md border border-black/10 dark:border-white/10 bg-[var(--code-bg)] text-[var(--code-fg)] whitespace-pre-wrap break-words">
        {/* Header controls */}
        <div className="absolute top-2 right-2 z-10 flex items-center gap-1.5">
          <button
            onClick={handleCopy}
            className="flex h-6 w-6 items-center justify-center rounded bg-black/5 dark:bg-white/10 hover:bg-black/10 dark:hover:bg-white/20 transition-opacity opacity-0 group-hover/codeblock:opacity-100 focus:opacity-100"
            title={copied ? 'Copied!' : 'Copy code'}
            aria-label={copied ? 'Copied!' : 'Copy code'}
          >
            {copied ? (
              <Check className="w-3.5 h-3.5 text-green-500" />
            ) : (
              <Copy className="w-3.5 h-3.5 text-muted-foreground" />
            )}
          </button>
          {language && (
            <span className="pointer-events-none flex items-center rounded bg-black/5 dark:bg-white/10 px-1.5 py-0.5 text-[11px] text-muted-foreground font-mono">
              {language}
            </span>
          )}
        </div>
        <code className={`language-${language || ''} block p-4 pr-20 text-sm leading-relaxed font-mono whitespace-pre-wrap break-words ${className || ''}`}>
          {children}
        </code>
      </pre>
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
