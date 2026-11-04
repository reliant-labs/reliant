/**
 * Reusable copy button for tool output sections.
 * Shows a subtle copy icon that changes to a checkmark on success.
 */

import Tooltip from "../../forge-ui/tooltip";
import { memo, useState, useCallback, useEffect, useRef } from 'react';
import { Copy, Check } from 'lucide-react';
import { cn } from '../../../lib/utils';

interface CopyButtonProps {
  /** Text content to copy to clipboard */
  content: string;
  /**
   * Resolves the text to copy, used instead of `content` when given: a tool
   * output the transcript holds only a preview of is fetched in full first.
   */
  resolveContent?: () => Promise<string>;
  /** Additional CSS classes */
  className?: string;
}

function CopyButtonComponent({ content, resolveContent, className }: CopyButtonProps) {
  const [copied, setCopied] = useState(false);
  const mounted = useRef(false);
  const resetTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      if (resetTimer.current !== null) clearTimeout(resetTimer.current);
      resetTimer.current = null;
    };
  }, []);

  const handleCopy = useCallback(async (e: React.MouseEvent) => {
    e.stopPropagation();
    try {
      const text = resolveContent ? await resolveContent() : content;
      if (!mounted.current) return;
      await navigator.clipboard.writeText(text);
      // An in-flight clipboard write can finish after the button unmounts.
      if (!mounted.current) return;
      setCopied(true);
      if (resetTimer.current !== null) clearTimeout(resetTimer.current);
      resetTimer.current = setTimeout(() => {
        resetTimer.current = null;
        setCopied(false);
      }, 2000);
    } catch {
      // Clipboard API may fail in some contexts
    }
  }, [content, resolveContent]);

  return (
    <Tooltip content={copied ? 'Copied!' : 'Copy to clipboard'} side="bottom" delay={300} triggerClassName="inline-flex">
<button
      type="button"
      onClick={handleCopy}
      className={cn(
        "p-0.5 rounded hover:bg-muted/80 text-muted-foreground hover:text-foreground transition-colors opacity-0 group-hover/tool-output:opacity-100 focus:opacity-100",
        className,
      )}
      aria-label={copied ? 'Copied!' : 'Copy to clipboard'}
    >
      {copied ? (
        <Check className="w-3 h-3 text-green-500" />
      ) : (
        <Copy className="w-3 h-3" />
      )}
    </button>
</Tooltip>
  );
}

export const CopyButton = memo(CopyButtonComponent);
