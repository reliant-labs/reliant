/**
 * Renderer for shell tool calls
 * 
 * Design decisions:
 * - Short single-line commands (<80 chars, no newlines) don't show command input
 *   since the header already displays it as shell(command)
 * - Long or multi-line commands show up to 3 lines with vertical scroll
 * - Output is always shown when present
 */

import { memo } from 'react';
import type { ToolContentProps } from './types';
import { LightweightCodeViewer } from '../LightweightCodeViewer';

// Threshold for showing command in expanded area vs relying on header
const SHORT_COMMAND_THRESHOLD = 80;
// Max height for command input before scrolling (~3-4 lines)
const COMMAND_MAX_HEIGHT = 64;

function ShellToolRendererComponent({ ctx }: ToolContentProps) {
  const { input, result } = ctx;
  
  // Extract command
  let command = '';
  if (typeof input === 'string') {
    command = input;
  } else if (input?.command) {
    command = input.command as string;
  } else if (input?.Command) {
    command = input.Command as string;
  }

  // Parse nested JSON if needed
  if (command && typeof command === 'string' && command.trim().startsWith('{"command":')) {
    try {
      const parsed = JSON.parse(command);
      if (parsed.command) {
        command = parsed.command;
      }
    } catch {
      // Keep original
    }
  }

  if (!command) {
    return null;
  }

  // Check for heredoc pattern (cat << 'EOF' > file or similar)
  // Must detect << with common delimiters, regardless of redirection order
  const hasHeredoc = command.includes("<<") && /<<\s*['"-]?(EOF|END|HEREDOC)['"-]?/i.test(command);

  // Determine if command is "short" (single line, under threshold)
  // Short commands are adequately displayed in the header
  const isMultiLine = command.includes('\n');
  const isShortCommand = !isMultiLine && command.length < SHORT_COMMAND_THRESHOLD;

  // Show command input for long/multi-line commands, or during execution
  const showCommandInput = !isShortCommand || (ctx.isExecuting && !result);

  // Prepend $ to command for display
  const displayCommand = `$ ${command}`;

  // If short command with no output and not executing, render nothing (header is enough)
  if (isShortCommand && !result && !ctx.isExecuting) {
    return null;
  }

  return (
    <div className="tool-content-shell">
      {/* Command input display - for long/multi-line commands or during execution */}
      {showCommandInput && (
        <LightweightCodeViewer
          content={displayCommand}
          language="bash"
          maxHeight={COMMAND_MAX_HEIGHT}
          minHeight={0}
          showLineNumbers={false}
          noBorder
          wordWrap
        />
      )}

      {/* Result output - only if not a heredoc (which shows file content) */}
      {result && !hasHeredoc && (
        <div className={showCommandInput ? "border-t border-border/50" : ""}>
          {/* Output label */}
          <div className="px-2 py-0.5 text-[9px] text-muted-foreground uppercase tracking-wider bg-muted/30">
            output
          </div>
          {result.is_error ? (
            <div className="px-2 py-1.5 text-[11px] text-destructive bg-destructive/5">
              {result.content}
            </div>
          ) : (
            <LightweightCodeViewer
              content={result.content || ''}
              language="text"
              maxHeight={200}
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

export const ShellToolRenderer = memo(ShellToolRendererComponent);
