// Copyright (c) 2025 Reliant Labs
import Tooltip from "../forge-ui/tooltip";
import { useState, memo, useRef, useEffect } from "react";
import {
  ChevronDown,
  ChevronUp,
  AlertCircle,
  Clock,
  Terminal,
  Maximize2,
  Folder,
  XCircle,
} from "lucide-react";
import { QuietRow } from "./QuietRow";
import { cn } from "../../lib/utils";
import { focusChatInput } from "../../hooks/useFocusManager";
import Modal from "../forge-ui/modal";
import { LightweightCodeViewer } from "./LightweightCodeViewer";
import type { RunOutputUpdate } from "../../types/streaming";

// Collapsible content container with max-height and expand/collapse
function CollapsibleContent({
  children,
  maxHeight = 200,
}: {
  children: React.ReactNode;
  maxHeight?: number;
}) {
  const [isFullyExpanded, setIsFullyExpanded] = useState(false);
  const contentRef = useRef<HTMLDivElement>(null);
  const [needsExpand, setNeedsExpand] = useState(false);

  useEffect(() => {
    if (contentRef.current) {
      const hasOverflow = contentRef.current.scrollHeight > maxHeight;
      setNeedsExpand(hasOverflow);
    }
  }, [children, maxHeight]);

  return (
    <div>
      <div
        ref={contentRef}
        className="overflow-hidden"
        style={!isFullyExpanded ? { maxHeight: `${maxHeight}px` } : undefined}
      >
        {children}
      </div>
      
      {needsExpand && !isFullyExpanded && (
        <div className="flex py-1">
          <button
            onClick={() => setIsFullyExpanded(true)}
            className="flex items-center gap-1 rounded-md text-ui text-muted-foreground transition-colors hover:text-foreground"
          >
            <ChevronDown className="size-3.5" />
            Show all
          </button>
        </div>
      )}
      
      {needsExpand && isFullyExpanded && (
        <div className="flex py-1">
          <button
            onClick={() => setIsFullyExpanded(false)}
            className="flex items-center gap-1 rounded-md text-ui text-muted-foreground transition-colors hover:text-foreground"
          >
            <ChevronUp className="size-3.5" />
            Show less
          </button>
        </div>
      )}
    </div>
  );
}

interface RunStepExecutionProps {
  runOutput: RunOutputUpdate;
}

function RunStepExecutionComponent({ runOutput }: RunStepExecutionProps) {
  const [isExpanded, setIsExpanded] = useState(false); // Collapsed, like a tool row
  const [showModal, setShowModal] = useState(false);

  const isSuccess = runOutput.exit_code === 0;
  const hasOutput = runOutput.output && runOutput.output.trim().length > 0;

  // A run step is a quiet row like a tool call: the command, how it ended,
  // and its output one click away.
  const icon = runOutput.interrupted ? (
    <AlertCircle className="size-4 text-warning-ink" />
  ) : isSuccess ? (
    <Terminal className="size-4" />
  ) : (
    <XCircle className="size-4 text-destructive-ink" />
  );

  const statusText = runOutput.interrupted
    ? "Interrupted"
    : isSuccess
      ? `Completed (${runOutput.duration}ms)`
      : `Failed (exit ${runOutput.exit_code})`;

  // Format command for display - truncate if too long
  const formatCommand = (command: string): string => {
    const trimmed = command.trim();
    if (trimmed.length > 80) {
      return trimmed.substring(0, 77) + "...";
    }
    return trimmed;
  };

  return (
    <>
      <QuietRow
        className="run-step-execution-bubble"
        icon={icon}
        expanded={isExpanded && !!hasOutput}
        onToggle={hasOutput ? () => setIsExpanded(!isExpanded) : undefined}
        ariaLabel={`Toggle output of ${formatCommand(runOutput.command)}`}
        trailing={
          <>
            <span className={cn("text-meta", isSuccess ? "sr-only" : runOutput.interrupted ? "text-warning-ink" : "text-destructive-ink")}>
              {statusText}
            </span>
            {hasOutput && (
              <Tooltip content="Expand in modal" side="bottom" delay={300} triggerClassName="inline-flex">
                <button
                  type="button"
                  onClick={() => setShowModal(true)}
                  className="inline-flex size-6 items-center justify-center rounded-md text-muted-foreground opacity-0 transition-[opacity,color] hover:text-foreground group-hover/row:opacity-100 focus-visible:opacity-100 touch:opacity-100"
                  aria-label="Expand in modal"
                >
                  <Maximize2 className="size-3.5" />
                </button>
              </Tooltip>
            )}
          </>
        }
        detail={
          <>
            <CollapsibleContent maxHeight={200}>
              <LightweightCodeViewer content={runOutput.output} language="bash" maxHeight={400} minHeight={0} />
            </CollapsibleContent>
            {/* Show stderr separately if different from combined output */}
            {runOutput.stderr && runOutput.stderr !== runOutput.output && runOutput.stderr !== runOutput.stdout && (
              <div className="mt-2">
                <div className="pb-1 text-meta text-destructive-ink">stderr</div>
                <LightweightCodeViewer content={runOutput.stderr} language="bash" maxHeight={200} minHeight={0} />
              </div>
            )}
          </>
        }
      >
        <span className="shrink-0">Ran</span>
        <span className="min-w-0 truncate font-mono text-code" title={runOutput.working_dir || undefined}>
          {formatCommand(runOutput.command)}
        </span>
      </QuietRow>

      {/* Modal for expanded view */}
      <Modal
        size="xl"
        open={showModal}
        fallbackFocus={focusChatInput}
        onClose={() => setShowModal(false)}
        title={`Run: ${formatCommand(runOutput.command)} - ${statusText}`}
      >
        <div className="space-y-4">
          {/* Command */}
          <div>
            <div className="pb-1">
              <span className="text-meta text-muted-foreground">
                Command:
              </span>
            </div>
            <LightweightCodeViewer
              content={runOutput.command}
              language="bash"
              maxHeight={100}
              minHeight={40}
            />
          </div>

          {/* Working Directory */}
          {runOutput.working_dir && (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Folder className="w-4 h-4" />
              <span className="font-mono">{runOutput.working_dir}</span>
            </div>
          )}

          {/* Output */}
          {hasOutput && (
            <div>
              <div className="pb-1">
                <span className="text-meta text-muted-foreground">
                  Output:
                </span>
              </div>
              <LightweightCodeViewer
                content={runOutput.output}
                language="bash"
                maxHeight={500}
                minHeight={100}
              />
            </div>
          )}

          {/* Metadata */}
          <div className="flex flex-wrap gap-4 text-sm text-muted-foreground">
            <div className="flex items-center gap-1">
              <Clock className="w-4 h-4" />
              <span>{runOutput.duration}ms</span>
            </div>
            <div className="flex items-center gap-1">
              <span className={cn(
                "font-mono",
                isSuccess ? "text-success-ink" : "text-destructive-ink"
              )}>
                Exit code: {runOutput.exit_code}
              </span>
            </div>
            {runOutput.worktree_id && (
              <div className="flex items-center gap-1">
                <span className="font-mono text-muted-foreground">
                  Worktree: {runOutput.worktree_id}
                </span>
              </div>
            )}
          </div>
        </div>
      </Modal>
    </>
  );
}

export const RunStepExecution = memo(RunStepExecutionComponent);
