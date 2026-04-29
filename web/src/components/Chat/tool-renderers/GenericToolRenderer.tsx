/**
 * Generic renderer for unknown or miscellaneous tools
 * Shows JSON input and any output
 */

import { memo } from 'react';
import type { ToolContentProps } from './types';
import { LightweightCodeViewer } from '../LightweightCodeViewer';
import { formatErrorMessage } from '../../../lib/utils';
import { CopyButton } from './CopyButton';

function GenericToolRendererComponent({ ctx }: ToolContentProps) {
  const { input, result } = ctx;

  // Check if we have any content to show
  const hasInput = input !== undefined && 
    (typeof input === 'object' ? Object.keys(input).length > 0 : input !== '');
  const hasResult = result?.content;

  if (!hasInput && !hasResult) {
    return null;
  }

  return (
    <div className="tool-content-generic">
      {/* Input display */}
      {hasInput && (
        <div className="border-b border-border/30 last:border-0">
          <div className="px-2 py-0.5 text-[10px] text-muted-foreground bg-muted/40">
            Input
          </div>
          <LightweightCodeViewer
            content={typeof input === 'string' ? input : JSON.stringify(input, null, 2)}
            language="json"
            maxHeight={300}
            minHeight={0}
            showLineNumbers={false}
            noBorder
          />
        </div>
      )}

      {/* Result display */}
      {hasResult && (
        <div className={result.is_error ? 'bg-warning/5' : ''}>
          <div className={`px-2 py-1 text-[10px] ${result.is_error ? 'text-warning' : 'text-muted-foreground'} bg-muted/40 border-b border-border/20 flex items-center justify-between`}>
            <span>{result.is_error ? 'Warning' : 'Output'}</span>
            {!result.is_error && result.content && (
              <CopyButton content={result.content} className="opacity-100" />
            )}
          </div>
          {result.is_error ? (
            <div className="px-2 py-1.5 text-[11px] text-warning">
              {formatErrorMessage(result.content)}
            </div>
          ) : (
            <LightweightCodeViewer
              content={result.content}
              language="text"
              maxHeight={500}
              minHeight={0}
              showLineNumbers={false}
              noBorder
            />
          )}
        </div>
      )}
    </div>
  );
}

export const GenericToolRenderer = memo(GenericToolRendererComponent);