// Copyright (c) 2025 Reliant Labs
import { useState, memo, useRef, useEffect } from "react";
import {
  ChevronDown,
  ChevronRight,
  ChevronUp,
  CheckCircle,
  AlertCircle,
  Clock,
  Terminal,
  Maximize2,
  Folder,
} from "lucide-react";
import { cn } from "../../lib/utils";
import { Modal } from "../ui/Modal";
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
    <div className="rounded-md border border-border/50 bg-background/50">
      <div
        ref={contentRef}
        className={cn(
          "overflow-hidden transition-all duration-200",
          !isFullyExpanded && `max-h-[${maxHeight}px]`
        )}
        style={!isFullyExpanded ? { maxHeight: `${maxHeight}px` } : undefined}
      >
        {children}
      </div>
      
      {needsExpand && !isFullyExpanded && (
        <div className="flex justify-center py-1 border-t border-border/50">
          <button
            onClick={() => setIsFullyExpanded(true)}
            className="flex items-center gap-1 px-2 py-0.5 text-[10px] font-medium rounded bg-muted/80 hover:bg-muted transition-colors text-muted-foreground hover:text-foreground"
          >
            <ChevronDown className="w-3 h-3" />
            Expand
          </button>
        </div>
      )}
      
      {needsExpand && isFullyExpanded && (
        <div className="flex justify-center py-1 border-t border-border/50">
          <button
            onClick={() => setIsFullyExpanded(false)}
            className="flex items-center gap-1 px-2 py-0.5 text-[10px] font-medium rounded bg-muted/80 hover:bg-muted transition-colors text-muted-foreground hover:text-foreground"
          >
            <ChevronUp className="w-3 h-3" />
            Collapse
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
  const [isExpanded, setIsExpanded] = useState(true); // Default expanded like action tools
  const [showModal, setShowModal] = useState(false);

  const isSuccess = runOutput.exit_code === 0;
  const hasOutput = runOutput.output && runOutput.output.trim().length > 0;

  const getIcon = () => {
    if (runOutput.interrupted) {
      return <AlertCircle className="w-3 h-3 text-warning" />;
    }
    if (isSuccess) {
      return <CheckCircle className="w-3 h-3 text-success" />;
    }
    return <AlertCircle className="w-3 h-3 text-destructive" />;
  };

  const getStatusText = () => {
    if (runOutput.interrupted) {
      return "Interrupted";
    }
    if (isSuccess) {
      return `Completed (${runOutput.duration}ms)`;
    }
    return `Failed (exit ${runOutput.exit_code})`;
  };

  // Format command for display - truncate if too long
  const formatCommand = (command: string): string => {
    const trimmed = command.trim();
    if (trimmed.length > 80) {
      return trimmed.substring(0, 77) + "...";
    }
    return trimmed;
  };

  // Get a short display name for the working directory
  const formatWorkingDir = (path: string): string => {
    if (!path) return "";
    const parts = path.split("/");
    // Show last 2 parts of the path
    if (parts.length > 2) {
      return ".../" + parts.slice(-2).join("/");
    }
    return path;
  };

  const borderColor = runOutput.interrupted
    ? "border-warning/30"
    : !isSuccess
    ? "border-destructive/30"
    : "border-success/20";

  return (
    <>
      <div
        className={cn(
          "run-step-execution-bubble border rounded overflow-hidden font-mono",
          borderColor,
          "bg-transparent"
        )}
      >
        {/* Header */}
        <div
          className={cn(
            "flex items-center justify-between px-2 py-1.5 bg-muted/30",
            hasOutput && "cursor-pointer hover:bg-muted/50"
          )}
          onClick={() => hasOutput && setIsExpanded(!isExpanded)}
        >
          <div className="flex items-center gap-2 flex-1 min-w-0">
            <Terminal className="w-3 h-3 text-muted-foreground flex-shrink-0" />
            {getIcon()}
            <span className="text-[11px] font-medium text-foreground/90 truncate">
              {formatCommand(runOutput.command)}
            </span>
            <span
              className={cn(
                "text-[11px] flex-shrink-0",
                runOutput.interrupted
                  ? "text-warning"
                  : isSuccess
                  ? "text-success"
                  : "text-destructive"
              )}
            >
              {getStatusText()}
            </span>
          </div>
          <div className="flex items-center gap-1 flex-shrink-0">
            {runOutput.working_dir && (
              <span className="text-[10px] text-muted-foreground flex items-center gap-0.5" title={runOutput.working_dir}>
                <Folder className="w-2.5 h-2.5" />
                {formatWorkingDir(runOutput.working_dir)}
              </span>
            )}
            {hasOutput && (
              <>
                <span className="text-[11px] font-mono text-muted-foreground">
                  {runOutput.output.length.toLocaleString()} chars
                </span>
                <button
                  onClick={(e) => {
                    e.stopPropagation();
                    setShowModal(true);
                  }}
                  className="p-0.5 hover:bg-muted rounded transition-colors"
                  title="Expand in modal"
                >
                  <Maximize2 className="w-3 h-3 text-muted-foreground" />
                </button>
                {isExpanded ? (
                  <ChevronDown className="w-3 h-3 text-muted-foreground" />
                ) : (
                  <ChevronRight className="w-3 h-3 text-muted-foreground" />
                )}
              </>
            )}
          </div>
        </div>

        {/* Expanded Content */}
        {isExpanded && hasOutput && (
          <div className="p-1">
            <CollapsibleContent maxHeight={200}>
              <LightweightCodeViewer
                content={runOutput.output}
                language="bash"
                maxHeight={400}
                minHeight={0}
              />
            </CollapsibleContent>

            {/* Show stderr separately if different from combined output */}
            {runOutput.stderr && runOutput.stderr !== runOutput.output && runOutput.stderr !== runOutput.stdout && (
              <div className="mt-2">
                <div className="px-2 py-1 bg-destructive/10 rounded-t-md border border-b-0 border-destructive/30">
                  <span className="text-[11px] font-medium text-destructive">
                    stderr
                  </span>
                </div>
                <div className="border border-t-0 border-destructive/30 rounded-b-md overflow-hidden">
                  <LightweightCodeViewer
                    content={runOutput.stderr}
                    language="bash"
                    maxHeight={200}
                    minHeight={0}
                  />
                </div>
              </div>
            )}
          </div>
        )}
      </div>

      {/* Modal for expanded view */}
      <Modal
        isOpen={showModal}
        onClose={() => setShowModal(false)}
        title={`Run: ${formatCommand(runOutput.command)} - ${getStatusText()}`}
      >
        <div className="space-y-4">
          {/* Command */}
          <div>
            <div className="px-3 py-2 bg-muted/30 rounded-t-md border border-b-0 border-border">
              <span className="text-[11px] font-medium text-muted-foreground">
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
              <div className="px-3 py-2 bg-muted/30 rounded-t-md border border-b-0 border-border">
                <span className="text-[11px] font-medium text-muted-foreground">
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
                isSuccess ? "text-success" : "text-destructive"
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
