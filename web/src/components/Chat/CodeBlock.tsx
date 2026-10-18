import { useState, type ReactNode } from 'react';
import { Tooltip } from "../ui/Tooltip";
import { useSurface } from '../../lib/surfaceContext';
import { MobileChatSheet } from './MobileChatSheet';
import { toast } from 'sonner';
import { Copy, Check, Maximize2 } from 'lucide-react';

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
  const isMobile = useSurface() === "mobile";
  const [expanded, setExpanded] = useState(false);
  const [copied, setCopied] = useState(false);

  const handleCopy = async () => {
    // Extract text content from children
    const textContent = extractTextContent(children);
    
    try {
      await navigator.clipboard.writeText(textContent);
      setCopied(true);
      if (isMobile) toast.success("Copied");
      setTimeout(() => setCopied(false), 2000);
    } catch (error) {
      console.error('Failed to copy code:', error);
      if (isMobile) toast.error('Could not copy code. Please try again.');
    }
  };

  if (isMobile) return (
    <div data-message-interactive className="not-prose my-2 min-w-0 w-full overflow-hidden rounded-lg border border-border bg-[var(--code-bg)]">
      <div className="flex min-h-[44px] items-center gap-2 px-2">
        <span className="min-w-0 flex-1 break-words text-xs text-muted-foreground">{language || "Code"}</span>
        <button type="button" aria-label={copied ? "Copied" : "Copy code"} onClick={() => void handleCopy()} className="flex min-h-[44px] min-w-[44px] items-center justify-center">{copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}</button>
        <button type="button" aria-label="Expand code" onClick={() => setExpanded(true)} className="flex min-h-[44px] min-w-[44px] items-center justify-center"><Maximize2 className="h-4 w-4" /></button>
      </div>
      <pre className="mobile-thread-code m-0 overflow-x-auto p-3 text-sm leading-relaxed"><code className={`language-${language || ''} whitespace-pre font-mono ${className || ''}`}>{children}</code></pre>
      {expanded && <MobileChatSheet closeLabel="Close code" title={language || "Code"} onClose={() => setExpanded(false)}>
        <button type="button" className="min-h-[44px] px-3" onClick={() => void handleCopy()}>{copied ? "Copied" : "Copy code"}</button>
        <pre className="mobile-thread-code overflow-x-auto p-3 text-sm leading-relaxed select-text"><code className="whitespace-pre font-mono">{children}</code></pre>
      </MobileChatSheet>}
    </div>
  );
  return (
    <div className="not-prose relative my-4 w-full group/codeblock">
      <pre className="hljs relative w-full overflow-x-auto rounded-md border border-black/10 dark:border-white/10 bg-[var(--code-bg)] text-[var(--code-fg)] whitespace-pre-wrap break-words">
        {/* Header controls */}
        <div className="absolute top-2 right-2 z-10 flex items-center gap-1.5">
          <Tooltip content={copied ? 'Copied!' : 'Copy code'} placement="bottom" delay={300} wrapperClassName="inline-flex">
<button
            onClick={handleCopy}
            className="flex h-6 w-6 items-center justify-center rounded bg-black/5 dark:bg-white/10 hover:bg-black/10 dark:hover:bg-white/20 transition-opacity opacity-0 group-hover/codeblock:opacity-100 focus:opacity-100"
            aria-label={copied ? 'Copied!' : 'Copy code'}
          >
            {copied ? (
              <Check className="w-3.5 h-3.5 text-green-500" />
            ) : (
              <Copy className="w-3.5 h-3.5 text-muted-foreground" />
            )}
          </button>
</Tooltip>
          {language && (
            <span className="pointer-events-none flex items-center rounded bg-black/5 dark:bg-white/10 px-1.5 py-0.5 text-xs text-muted-foreground font-mono">
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
