import React, { useState, useLayoutEffect, useRef, memo } from 'react';
import { Terminal, Code, FileText, ChevronDown, ChevronUp } from "lucide-react";
import { logger } from "../../lib/logger";
import { MarkdownRenderer } from "./MarkdownRenderer";
import { LightweightDiffViewer } from './LightweightDiffViewer';
import { cn } from '../../lib/utils';
import { tabSwitchProfiler } from '../../lib/tabSwitchProfiler';

interface ToolContentRendererProps {
  toolName: string;
  params: Record<string, unknown>;
  approvalId?: string;
  worktreeId?: string; // Worktree context for file links
  isResolved?: boolean; // Whether the tool execution is resolved
}

// Type for task items that can be either strings or objects
type TaskItem = string | {
  title?: string;
  description?: string;
}

// Collapsible diff viewer wrapper - no lazy loading needed for lightweight viewer
const CollapsibleDiffViewer = memo(function CollapsibleDiffViewer({
  children,
}: {
  children: React.ReactNode;
}) {
  const [isExpanded, setIsExpanded] = useState(false);
  const [needsExpansion, setNeedsExpansion] = useState(false);
  const contentRef = useRef<HTMLDivElement>(null);

  // Check if content height exceeds collapsed height
  useLayoutEffect(() => {
    if (contentRef.current) {
      const contentHeight = contentRef.current.scrollHeight;
      const collapsedHeight = 80; // max-h-[80px]
      // Only show expand button if there's meaningful overflow (>20px hidden)
      // This prevents showing expand for tiny overflows due to padding/borders
      setNeedsExpansion(contentHeight > collapsedHeight + 20);
    }
  }, [children]);

  return (
    <div className="relative border border-border rounded-md overflow-hidden">
      <div
        ref={contentRef}
        className={cn(
          "overflow-hidden",
          isExpanded ? "" : "max-h-[80px]"
        )}
      >
        {children}
      </div>

      {/* Expand/Collapse button - only show if content exceeds collapsed height */}
      {needsExpansion && (
        <div className="flex justify-center border-t border-border">
          <button
            onClick={() => setIsExpanded(!isExpanded)}
            className={cn(
              "flex items-center gap-1 px-2 py-1 text-xs font-medium w-full",
              "bg-muted/80 hover:bg-muted transition-colors",
              "text-muted-foreground hover:text-foreground justify-center"
            )}
          >
            {isExpanded ? (
              <>
                <ChevronUp className="w-3 h-3" />
                Collapse diff
              </>
            ) : (
              <>
                <ChevronDown className="w-3 h-3" />
                Expand diff
              </>
            )}
          </button>
        </div>
      )}
    </div>
  );
});


// Create a synthetic diff for new file creation
function createNewFileDiff(filePath: string, content: string): string {
  return `--- /dev/null
+++ b/${filePath}
@@ -0,0 +1,${content.split("\n").length} @@
${content
  .split("\n")
  .map((line) => `+${line}`)
  .join("\n")}`;
}

// Create a synthetic diff for file editing
function createEditDiff(
  filePath: string,
  oldContent: string,
  newContent: string
): string {
  const oldLines = oldContent.split("\n");
  const newLines = newContent.split("\n");

  return `--- a/${filePath}
+++ b/${filePath}
@@ -1,${oldLines.length} +1,${newLines.length} @@
${oldLines.map((line) => `-${line}`).join("\n")}
${newLines.map((line) => `+${line}`).join("\n")}`;
}

export const ToolContentRenderer = memo(function ToolContentRenderer({
  toolName,
  params,
  approvalId,
  worktreeId,
}: ToolContentRendererProps) {
  // PERFORMANCE: Track ToolContentRenderer mount timing
  const mountStartRef = useRef<number | undefined>(undefined);
  if (!mountStartRef.current && tabSwitchProfiler.isEnabled()) {
    mountStartRef.current = performance.now();
  }

  // PERFORMANCE: Track when ToolContentRenderer finishes mounting
  useLayoutEffect(() => {
    if (mountStartRef.current && tabSwitchProfiler.isEnabled()) {
      const mountTime = performance.now() - mountStartRef.current;
      if (mountTime > 10) { // Only log slow mounts (>10ms)
      }
      mountStartRef.current = undefined;
    }
  });

  const renderToolContent = () => {
    switch (toolName.toLowerCase()) {
      case "write": {
        // Extract write data from params - handle both direct object and JSON string
        let writeData = params;

        // If params has a command property with JSON string, parse it
        if (params?.command && typeof params.command === 'string') {
          try {
            writeData = JSON.parse(params.command);
          } catch (e) {
            writeData = params;
          }
        }

        const filePath = (writeData?.file_path || writeData?.FilePath) as string;
        let content = writeData?.content as string;
        const diff = (writeData?.diff || writeData?.Diff) as string;
        const oldString = writeData?.old_string as string;
        const newString = writeData?.new_string as string;

        // Try different content extraction methods
        if (!content) {
          content = writeData?.Content as string;
        }
        if (!content && typeof writeData?.content === "object") {
          // Content might be nested in an object
          content = JSON.stringify(writeData.content);
        }

        // If we still don't have content but have diff, parse the diff
        if (!content && diff) {
          // Parse diff to extract the new content (lines starting with +)
          const diffLines = diff.split("\n");
          const newContentLines: string[] = [];

          for (const line of diffLines) {
            if (line.startsWith("+") && !line.startsWith("+++")) {
              // Skip diff headers (+++ b/file) but include content lines (+ content)
              newContentLines.push(line.substring(1));
            }
          }

          content = newContentLines.join("\n");
        }

        // Determine if this is a new file or editing existing file
        // Note: isNewFile detection is handled by diff viewer internally
        const originalContent = oldString || '';
        const modifiedContent = newString || content || '';

        // Debug logging - only when we have meaningful data to avoid infinite loops
        if (
          filePath ||
          (content && content !== "undefined") ||
          (diff && diff !== "undefined")
        ) {
          // Valid write operation - continue with rendering
        }

        // Generate diff content if not provided
        return (
          <CollapsibleDiffViewer>
            <LightweightDiffViewer
              original={originalContent}
              modified={modifiedContent}
              filename={filePath}
            />
          </CollapsibleDiffViewer>
        );
      }

      case "edit":
      case "patch": {
        // Extract edit data from params - handle both direct object and JSON string
        let editData = params;

        // If params has a command property with JSON string, parse it
        if (params?.command && typeof params.command === 'string') {
          try {
            editData = JSON.parse(params.command);
          } catch (e) {
            editData = params;
          }
        }

        // Handle both single edit and MultiEdit formats
        let filePath: string;
        let diff: string;
        let oldString: string;
        let newString: string;

        if (editData?.edits && Array.isArray(editData.edits) && editData.edits.length > 0) {
          // MultiEdit format - use the first edit for now
          const firstEdit = editData.edits[0];
          filePath = firstEdit?.file_path || firstEdit?.FilePath;
          diff = firstEdit?.diff || firstEdit?.Diff;
          oldString = firstEdit?.old_string;
          newString = firstEdit?.new_string;
        } else {
          // Single edit format
          filePath = (editData?.file_path || editData?.FilePath) as string;
          diff = (editData?.diff || editData?.Diff) as string;
          oldString = editData?.old_string as string;
          newString = editData?.new_string as string;
        }

        // Debug logging - only log when we have meaningful data and avoid infinite loops
        if (
          filePath ||
          diff ||
          (oldString && oldString !== "undefined") ||
          (newString && newString !== "undefined")
        ) {
          // Valid edit operation - continue with rendering
        }

        // Generate diff content if not provided
        const diffContent =
          diff ||
          (oldString && newString && filePath
            ? createEditDiff(filePath, oldString, newString)
            : null);

        // Parse the diff content to extract original and modified
        let originalContent = "";
        let modifiedContent = "";

        if (diffContent) {
          const lines = diffContent.split("\n");
          const original: string[] = [];
          const modified: string[] = [];

          for (const line of lines) {
            if (
              line.startsWith("---") ||
              line.startsWith("+++") ||
              line.startsWith("@@")
            ) {
              // Skip diff headers
              continue;
            } else if (line.startsWith("-")) {
              // Removed line (original) - only add if it has meaningful content
              const content = line.substring(1);
              if (
                content.trim() &&
                !content.includes("\\ No newline at end of file")
              ) {
                original.push(content);
              }
            } else if (line.startsWith("+")) {
              // Added line (modified) - only add if it has meaningful content
              const content = line.substring(1);
              if (
                content.trim() &&
                !content.includes("\\ No newline at end of file")
              ) {
                modified.push(content);
              }
            } else if (line.startsWith(" ")) {
              // Unchanged line - only add if it has meaningful content
              const content = line.substring(1);
              if (content.trim()) {
                original.push(content);
                modified.push(content);
              }
            }
            // Skip empty lines and metadata lines
          }

          originalContent = original.join("\n");
          modifiedContent = modified.join("\n");
        } else if (oldString !== undefined && newString !== undefined) {
          // Fallback to direct strings if no diff - handle empty strings too
          originalContent = oldString || "";
          modifiedContent = newString || "";
        }

        return (
          <>
            <CollapsibleDiffViewer>
              <LightweightDiffViewer
                original={originalContent}
                modified={modifiedContent}
                filename={filePath}
              />
            </CollapsibleDiffViewer>

            {/* Show additional edits if this is a MultiEdit */}
            {editData?.edits && Array.isArray(editData.edits) && editData.edits.length > 1 && (
              <div className="space-y-2">
                {editData.edits.slice(1).map((edit: { old_string: string; new_string: string; file_path: string; FilePath: string; }, index: number) => (
                  <CollapsibleDiffViewer key={`diff-additional-${index}-${approvalId}`}>
                    <LightweightDiffViewer
                      original={edit.old_string}
                      modified={edit.new_string}
                      filename={edit.file_path || edit.FilePath || ''}
                    />
                  </CollapsibleDiffViewer>
                ))}
              </div>
            )}
          </>
        );
      }

      case "bash":
      case "powershell": {
        // Extract command from params - handle both direct string and object with command property
        let command: string = '';

        if (typeof params === 'string') {
          command = params;
        } else if (params?.command) {
          command = params.command as string;
        } else if (params?.Command) {
          command = params.Command as string;
        }
        // Note: We no longer fall back to JSON.stringify(params) as it shows raw JSON
        // If no command is found, we'll show a "No command provided" message below

        // If command itself looks like JSON with a "command" field, extract it
        if (command && typeof command === 'string' && command.trim().startsWith('{"command":')) {
          try {
            const parsed = JSON.parse(command);
            if (parsed.command) {
              command = parsed.command;
            }
          } catch (e) {
            logger.error("Error parsing command:", e);
            // Keep original if parsing fails
          }
        }

        // Ensure command is a string and not undefined
        if (!command || typeof command !== 'string') {
          return (
            <div className="space-y-2">
              <div className="flex items-center gap-2">
                <Terminal className="w-4 h-4 text-primary" />
                <span className="font-semibold text-sm text-primary">
                  Execute command
                </span>
              </div>
              <div className="text-sm text-muted-foreground p-3 bg-muted rounded">
                No command provided
              </div>
            </div>
          );
        }

        // Parse shell command to show meaningful action (heredoc pattern)
        // Detect heredoc by << with common delimiters, regardless of redirection order
        const hasHeredoc = command.includes("<<") && /<<\s*['"-]?(EOF|END|HEREDOC)['"-]?/i.test(command);
        if (hasHeredoc) {
          // Match filename from either "cat << 'EOF' > file" or "cat > file << 'EOF'"
          const fileMatch = command.match(/>\s*([^\s<>]+\.\w+)/) || command.match(/cat\s+([^\s<>]+\.\w+)/);
          const contentMatch = command.match(
            /<<\s*['"]?EOF['"]?\n([\s\S]*?)\nEOF/
          );

          // Generate diff content for shell file creation
          const diffContent =
            fileMatch && contentMatch
              ? createNewFileDiff(fileMatch[1], contentMatch[1])
              : null;

          return (
            <>
              {/* Show diff using DiffViewer with collapsible wrapper */}
              {diffContent && fileMatch ? (
                <CollapsibleDiffViewer>
                  <LightweightDiffViewer
                    original=""
                    modified={contentMatch?.[1] || ""}
                    filename={fileMatch[1]}
                  />
                </CollapsibleDiffViewer>
              ) : null}

              {/* Collapsible command section */}
              <details className="border border-border rounded mt-1">
                <summary className="px-1.5 py-1 text-xs text-muted-foreground hover:bg-muted cursor-pointer bg-card transition-colors">
                  View shell command
                </summary>
                <div className="bg-muted p-1.5 border-t border-border max-w-full">
                  <pre className="text-xs font-mono text-muted-foreground overflow-x-auto whitespace-pre-wrap break-all">
                    <span className="text-muted-foreground">$ </span>
                    {command}
                  </pre>
                </div>
              </details>
            </>
          );
        }

        // Parse and format the command for better readability using theme colors
        const formatCommand = (cmd: string) => {
          // More comprehensive regex to split command while preserving all parts
          const parts = cmd.split(/(--?[\w-]+=?|&&|\|\||;|\||>>?|<|2>&1|2>|1>|"[^"]*"|'[^']*'|\s+)/);

          return parts.filter(part => part).map((part, index) => {
            // Remove leading/trailing whitespace for checking, but preserve it for display
            const trimmed = part.trim();

            // Commands and programs (first word or after operators)
            if (index === 0 || (index > 0 && parts[index - 1]?.match(/&&|\|\||;|\|/))) {
              if (trimmed && !trimmed.startsWith('-') && !trimmed.match(/^[<>|&;]/)) {
                return (
                  <span key={index} className="text-primary font-semibold">
                    {part}
                  </span>
                );
              }
            }

            // Flags and options with or without values
            if (trimmed.match(/^--?[\w-]+/)) {
              return (
                <span key={index} className="text-info">
                  {part}
                </span>
              );
            }

            // Logical operators
            if (trimmed.match(/^(&&|\|\||;)$/)) {
              return (
                <span key={index} className="text-warning font-bold mx-1">
                  {part}
                </span>
              );
            }

            // Pipe and redirection operators
            if (trimmed.match(/^(\||>>?|<|2>&1|2>|1>)$/)) {
              return (
                <span key={index} className="text-primary font-bold mx-1">
                  {part}
                </span>
              );
            }

            // Quoted strings
            if (trimmed.startsWith('"') && trimmed.endsWith('"')) {
              return (
                <span key={index} className="text-success">
                  {part}
                </span>
              );
            }
            if (trimmed.startsWith("'") && trimmed.endsWith("'")) {
              return (
                <span key={index} className="text-success">
                  {part}
                </span>
              );
            }

            // File paths and URLs
            if (trimmed.includes('/') || trimmed.startsWith('http')) {
              return (
                <span key={index} className="text-info underline decoration-current/30">
                  {part}
                </span>
              );
            }

            // Environment variables
            if (trimmed.match(/^\$\w+/) || trimmed.match(/^\*.+\*/)) {
              return (
                <span key={index} className="text-warning">
                  {part}
                </span>
              );
            }

            // Whitespace - preserve it
            if (!trimmed) {
              return <span key={index}>{part}</span>;
            }

            // Default text
            return (
              <span key={index} className="text-foreground/80">
                {part}
              </span>
            );
          });
        };

        // Default shell command display with rich formatting - wrapped in collapsible viewer
        return (
          <CollapsibleDiffViewer>
            <div className="p-1.5 max-w-full overflow-hidden" style={{ backgroundColor: 'var(--code-bg)' }}>
              <div className="flex items-start gap-1.5">
                <span className="select-none flex-shrink-0 font-mono text-xs" style={{ color: 'var(--code-accent)' }}>$</span>
                <div className="font-mono text-xs overflow-x-auto flex-1">
                  <code className="block whitespace-pre-wrap break-all" style={{ color: 'var(--code-fg)' }}>
                    {formatCommand(command)}
                  </code>
                </div>
              </div>
            </div>
          </CollapsibleDiffViewer>
        );
      }

      case "create_plan": {
        // Extract plan data from params - handle both direct object and JSON string
        let planData = params;

        // If params has a command property with JSON string, parse it
        if (params?.command && typeof params.command === 'string') {
          try {
            planData = JSON.parse(params.command);
          } catch (e) {
            planData = params;
          }
        }

        const title = (planData?.title || planData?.Title) as string;
        const description = (planData?.description ||
          planData?.Description) as string;
        const complexity = (planData?.complexity || planData?.Complexity) as string;
        const tasks = (planData?.tasks || planData?.Tasks) as TaskItem[];

        return (
          <div className="space-y-2 p-2 border border-border rounded-md bg-card">
            <div className="flex items-center gap-2">
              <FileText className="w-4 h-4 text-info" />
              <span className="font-semibold text-sm text-info">
                Create Plan
              </span>
            </div>

            {/* Scrollable content area with max height */}
            <div className="max-h-[500px] overflow-y-auto space-y-3 px-1">
              {/* Plan Header */}
              <div className="space-y-2">
                {title && (
                  <h3 className="text-base font-semibold text-foreground">
                    {title}
                  </h3>
                )}

                {complexity && (
                  <div className="inline-flex items-center px-2 py-1 rounded-full text-xs font-medium bg-info/10 text-info border border-info/30">
                    {complexity.charAt(0).toUpperCase() + complexity.slice(1)}{" "}
                    Complexity
                  </div>
                )}
              </div>

              {/* Plan Description */}
              {description && (
                <div className="text-sm">
                  <MarkdownRenderer content={description} worktreeId={worktreeId} />
                </div>
              )}

              {/* Tasks List */}
              {tasks && tasks.length > 0 && (
                <div className="space-y-2">
                  <h4 className="font-medium text-foreground text-sm">
                    Tasks ({tasks.length})
                  </h4>
                  <div className="space-y-2">
                    {tasks.map((task, index) => (
                      <div key={index} className="flex items-start gap-2">
                        <div className="flex-shrink-0 w-4 h-4 mt-0.5">
                          <div className="w-3 h-3 border-2 border-foreground rounded-sm bg-transparent"></div>
                        </div>
                        <div className="flex-1">
                          <span className="text-sm text-foreground leading-relaxed">
                            {typeof task === "string"
                              ? task
                              : task.title || task.description || JSON.stringify(task)}
                          </span>
                        </div>
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {/* Fallback: show minimal UI if no content is detected */}
              {/* IMPORTANT: Never show raw JSON - it looks broken to users */}
              {!title && !description && (!tasks || tasks.length === 0) && (
                <div className="text-sm text-muted-foreground p-3 bg-muted/50 rounded border">
                  <span>Plan data is being prepared...</span>
                </div>
              )}
            </div>
          </div>
        );
      }

      case "update_task": {
        // Extract update_task data from params
        let taskData = params;
        
        if (params?.command && typeof params.command === 'string') {
          try {
            taskData = JSON.parse(params.command);
          } catch (e) {
            taskData = params;
          }
        }
        
        const descriptionUpdate = (taskData?.description || taskData?.Description) as string;
        const metadata = taskData?.metadata as Record<string, unknown> | undefined;
        const notes = metadata?.notes as string | undefined;
        
        // Only show expanded content if there's a description or notes
        // The header already shows title and status, so we don't need to repeat that
        if (!descriptionUpdate && !notes) {
          return null; // Nothing meaningful to show
        }
        
        // Show a polished card with the description/notes
        return (
          <div className="text-sm text-muted-foreground leading-relaxed">
            {descriptionUpdate && (
              <p>{descriptionUpdate}</p>
            )}
            {notes && (
              <p className={descriptionUpdate ? "mt-2 text-xs" : ""}>
                {notes}
              </p>
            )}
          </div>
        );
      }

      case "add_task": {
        // Extract add_task data from params
        let taskData = params;
        
        if (params?.command && typeof params.command === 'string') {
          try {
            taskData = JSON.parse(params.command);
          } catch (e) {
            taskData = params;
          }
        }
        
        const description = (taskData?.description || taskData?.Description) as string;
        
        // Only show expanded content if there's a description
        // The header already shows title, so we don't need to repeat that
        if (!description) {
          return null; // Nothing meaningful to show
        }
        
        // Show the description cleanly
        return (
          <div className="text-sm text-muted-foreground leading-relaxed">
            <p>{description}</p>
          </div>
        );
      }

      default:
        // For unknown tools or tools still being prepared, show a minimal UI
        // IMPORTANT: Never show raw JSON to users - it's confusing and looks broken
        return (
          <div className="flex items-center gap-2 p-2 rounded border border-border/50 bg-muted/30">
            <Code className="w-4 h-4 text-muted-foreground" />
            <span className="text-sm text-muted-foreground">
              {toolName ? `${toolName} tool` : 'Tool execution'}
            </span>
          </div>
        );
    }
  };

  return renderToolContent();
});