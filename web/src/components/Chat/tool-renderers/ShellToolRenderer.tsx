/**
 * Renderer for shell tool calls
 * 
 * Design decisions:
 * - Short single-line commands (<80 chars, no newlines) don't show command input
 *   since the header already displays it as shell(command)
 * - Long or multi-line commands show up to 3 lines with vertical scroll
 * - Output is always shown when present
 * - Structured JSON output: { stdout, stderr, exit_code } displayed with
 *   separated stdout/stderr and exit code badge
 */

import { memo, useMemo } from 'react';
import type { ToolContentProps } from './types';
import { LightweightCodeViewer } from '../LightweightCodeViewer';
import { CopyButton } from './CopyButton';

// Threshold for showing command in expanded area vs relying on header
const SHORT_COMMAND_THRESHOLD = 80;
// Max height for command input before scrolling (~3-4 lines)
const COMMAND_MAX_HEIGHT = 64;

interface ParsedBashOutput {
  stdout: string;
  stderr: string;
  exit_code: number;
}

interface ParsedBackgroundOutput {
  process_id: string;
  command: string;
  backgrounded: true;
}

type ParsedShellOutput =
  | { type: 'structured'; data: ParsedBashOutput }
  | { type: 'background'; data: ParsedBackgroundOutput }
  | { type: 'legacy' };

function parseShellOutput(content: string): ParsedShellOutput {
  if (!content || !content.startsWith('{')) return { type: 'legacy' };
  try {
    const parsed = JSON.parse(content);
    if ('stdout' in parsed && 'exit_code' in parsed) {
      return { type: 'structured', data: parsed as ParsedBashOutput };
    }
    if ('backgrounded' in parsed && 'process_id' in parsed) {
      return { type: 'background', data: parsed as ParsedBackgroundOutput };
    }
  } catch {
    // Not JSON
  }
  return { type: 'legacy' };
}

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

  // Parse structured output (must be before any early returns to satisfy Rules of Hooks)
  const parsed = useMemo(
    () => (result ? parseShellOutput(result.content) : null),
    [result]
  );

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

  // Determine what to display for structured output
  const structured = parsed?.type === 'structured' ? parsed.data : null;
  const background = parsed?.type === 'background' ? parsed.data : null;
  const isLegacy = parsed?.type === 'legacy';

  const hasStdout = structured ? structured.stdout !== '' : false;
  const hasStderr = structured ? structured.stderr !== '' : false;
  const hasNonZeroExit = structured ? structured.exit_code !== 0 : false;
  const hasAnyOutput = hasStdout || hasStderr || hasNonZeroExit;

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
        <>
          {/* Background process result */}
          {background && (
            <div className={showCommandInput ? "border-t border-border/50" : ""}>
              <div className="px-2 py-1.5 text-[11px] text-muted-foreground font-mono">
                Started background process <span className="text-foreground font-medium">{background.process_id}</span>
                <br />
                <span className="text-[10px]">Use BashOutput to check output, BashKill to terminate</span>
              </div>
            </div>
          )}

          {/* Legacy plain text format (backwards compat for old results in DB, and bash_output tool) */}
          {isLegacy && (
            <div className={showCommandInput ? "border-t border-border/50" : ""}>
              <div className="px-2 py-1 text-[9px] text-muted-foreground uppercase tracking-wider bg-muted/40 border-b border-border/20 flex items-center justify-between">
                <span>output</span>
                {!result.is_error && result.content && (
                  <CopyButton content={result.content} className="opacity-100" />
                )}
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

          {/* Structured JSON format - only render section when there's something to show */}
          {structured && hasAnyOutput && (
            <div className={showCommandInput ? "border-t border-border/50" : ""}>
              {/* Output label with optional exit code badge */}
              <div className="px-2 py-1 text-[9px] text-muted-foreground uppercase tracking-wider bg-muted/40 border-b border-border/20 flex items-center gap-1.5">
                <span>output</span>
                {hasNonZeroExit && (
                  <span className="text-destructive font-medium normal-case tracking-normal">
                    exit code {structured.exit_code}
                  </span>
                )}
                <span className="flex-1" />
                {hasStdout && (
                  <CopyButton content={structured.stdout} className="opacity-100" />
                )}
              </div>

              {/* Stdout */}
              {hasStdout && (
                <LightweightCodeViewer
                  content={structured.stdout}
                  language="text"
                  maxHeight={200}
                  minHeight={0}
                  showLineNumbers={false}
                  noBorder
                />
              )}

              {/* Stderr */}
              {hasStderr && (
                <div className={hasStdout ? "border-t border-border/50" : ""}>
                  <div className="px-2 py-0.5 text-[9px] text-destructive/70 uppercase tracking-wider bg-destructive/5 flex items-center justify-between">
                    <span>stderr</span>
                    <CopyButton content={structured.stderr} className="opacity-100" />
                  </div>
                  <div className="px-2 py-1.5 text-[11px] text-destructive bg-destructive/5 whitespace-pre-wrap font-mono max-h-[400px] overflow-y-auto">
                    {structured.stderr}
                  </div>
                </div>
              )}
            </div>
          )}
        </>
      )}
    </div>
  );
}

export const ShellToolRenderer = memo(ShellToolRendererComponent);