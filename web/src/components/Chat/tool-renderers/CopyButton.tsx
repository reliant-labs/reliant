/**
 * Reusable copy button for tool output sections.
 * Shows a subtle copy icon that changes to a checkmark on success.
 */

import { memo, useState, useCallback } from 'react';
import { Copy, Check } from 'lucide-react';
import { cn } from '../../../lib/utils';

interface CopyButtonProps {
  /** Text content to copy to clipboard */
  content: string;
  /** Additional CSS classes */
  className?: string;
}

function CopyButtonComponent({ content, className }: CopyButtonProps) {
  const [copied, setCopied] = useState(false);

  const handleCopy = useCallback(async (e: React.MouseEvent) => {
    e.stopPropagation();
    try {
      await navigator.clipboard.writeText(content);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Clipboard API may fail in some contexts
    }
  }, [content]);

  return (
    <button
      type="button"
      onClick={handleCopy}
      className={cn(
        "p-0.5 rounded hover:bg-muted/80 text-muted-foreground hover:text-foreground transition-colors opacity-0 group-hover/tool-output:opacity-100 focus:opacity-100",
        className,
      )}
      title={copied ? 'Copied!' : 'Copy to clipboard'}
      aria-label={copied ? 'Copied!' : 'Copy to clipboard'}
    >
      {copied ? (
        <Check className="w-3 h-3 text-green-500" />
      ) : (
        <Copy className="w-3 h-3" />
      )}
    </button>
  );
}

export const CopyButton = memo(CopyButtonComponent);
