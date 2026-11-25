/**
 * Compact renderer for the `load_tool` tool
 * Shows tool name + success/warning/denied status as a small notification
 */

import { memo } from 'react';
import { Wrench, AlertCircle, CheckCircle, XCircle } from 'lucide-react';
import type { ToolContentProps } from './types';

function LoadToolRendererComponent({ ctx }: ToolContentProps) {
  const { input, result } = ctx;
  const data = typeof input === 'string' ? {} : (input || {});
  const toolName = (data.name as string) || (data.tool_name as string) || '';

  const isError = result?.is_error ?? false;
  const resultContent = result?.content || '';

  // Try to determine if it was denied vs a non-fatal warning
  const isDenied = isError && resultContent.toLowerCase().includes('denied');

  return (
    <div className="tool-content-load-tool">
      <div className="px-2 py-1.5 flex items-center gap-2">
        <Wrench className="w-3.5 h-3.5 text-muted-foreground flex-shrink-0" />
        <span className="text-xs font-medium text-foreground truncate">
          {toolName || 'Loading tool...'}
        </span>
        {result && (
          isError ? (
            <span className="inline-flex items-center gap-1 text-2xs text-muted-foreground">
              {isDenied ? (
                <XCircle className="size-3 text-destructive-ink" aria-hidden="true" />
              ) : (
                <AlertCircle className="size-3 text-warning-ink" aria-hidden="true" />
              )}
              {isDenied ? 'denied' : 'warning'}
            </span>
          ) : (
            <span className="inline-flex items-center gap-1 text-2xs text-muted-foreground">
              <CheckCircle className="size-3 text-success-ink" aria-hidden="true" />
              loaded
            </span>
          )
        )}
      </div>
      {/* Show denial/warning reason if present */}
      {isError && resultContent && (
        <div data-testid="load-tool-warning-reason" className="truncate px-2 pb-1.5 pl-7 text-2xs text-muted-foreground">
          {resultContent}
        </div>
      )}
    </div>
  );
}

export const LoadToolRenderer = memo(LoadToolRendererComponent);