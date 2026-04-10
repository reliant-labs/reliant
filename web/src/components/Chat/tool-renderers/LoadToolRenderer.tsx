/**
 * Compact renderer for the `load_tool` tool
 * Shows tool name + success/denied status as a small notification
 */

import { memo } from 'react';
import { Wrench, CheckCircle, XCircle } from 'lucide-react';
import type { ToolContentProps } from './types';

function LoadToolRendererComponent({ ctx }: ToolContentProps) {
  const { input, result } = ctx;
  const data = typeof input === 'string' ? {} : (input || {});
  const toolName = (data.name as string) || (data.tool_name as string) || '';

  const isError = result?.is_error ?? false;
  const resultContent = result?.content || '';

  // Try to determine if it was denied vs failed
  const isDenied = isError && resultContent.toLowerCase().includes('denied');

  return (
    <div className="tool-content-load-tool">
      <div className="px-2 py-1.5 flex items-center gap-2">
        <Wrench className="w-3.5 h-3.5 text-muted-foreground flex-shrink-0" />
        <span className="text-[11px] font-medium text-foreground truncate">
          {toolName || 'Loading tool...'}
        </span>
        {result && (
          isError ? (
            <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded-full text-[9px] font-medium bg-destructive/10 text-destructive border border-destructive/20">
              <XCircle className="w-2.5 h-2.5" />
              {isDenied ? 'denied' : 'failed'}
            </span>
          ) : (
            <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded-full text-[9px] font-medium bg-success/10 text-success border border-success/20">
              <CheckCircle className="w-2.5 h-2.5" />
              loaded
            </span>
          )
        )}
      </div>
      {/* Show denial/error reason if present */}
      {isError && resultContent && (
        <div className="px-2 pb-1.5 text-[10px] text-destructive/80 truncate">
          {resultContent}
        </div>
      )}
    </div>
  );
}

export const LoadToolRenderer = memo(LoadToolRendererComponent);
