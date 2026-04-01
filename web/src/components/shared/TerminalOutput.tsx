/**
 * Unified Terminal Output Component
 * 
 * A shared component for displaying process output in a terminal-like view.
 * Used by both Package Commands and Background Processes views.
 */

import { useEffect, useRef, useState, useMemo } from "react";
import {
  RefreshCw,
  Copy,
  Check,
  Loader2,
  Square,
  ArrowLeft,
  Clock,
  Folder,
  Terminal,
} from "lucide-react";
import { AnsiUp } from "ansi_up";
import DOMPurify from "dompurify";
import { cn } from "../../lib/utils";
import { PortsDisplay } from "./PortsDisplay";
import { BackgroundProcessStatus } from "../../api/background-grpc";

// ============================================
// Types
// ============================================

export interface ProcessInfo {
  id: string;
  command: string;
  status: BackgroundProcessStatus;
  start_time: string;
  end_time?: string;
  exit_code?: number;
  working_dir?: string;
  ports?: { port: number; protocol?: string }[];
}

export interface OutputLine {
  type: "stdout" | "stderr";
  text: string;
}

export interface ProcessOutput {
  stdout: string;
  stderr: string;
  combined?: OutputLine[]; // Interleaved output from backend
}

export interface TerminalOutputProps {
  process: ProcessInfo;
  output: ProcessOutput | null;
  isLoading?: boolean;
  onBack?: () => void;
  onClose?: () => void;
  onRefresh?: () => void;
  onKill?: () => Promise<void> | void;
  onOpenPort?: (port: number) => void;
  className?: string;
}

// ============================================
// Helper Functions
// ============================================

function formatDuration(startTime: string, endTime?: string): string {
  const start = new Date(startTime).getTime();
  const end = endTime ? new Date(endTime).getTime() : Date.now();
  const durationMs = Math.max(0, end - start);

  const totalSeconds = Math.floor(durationMs / 1000);
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;

  if (hours > 0) return `${hours}h ${minutes}m ${seconds}s`;
  if (minutes > 0) return `${minutes}m ${seconds}s`;
  return `${seconds}s`;
}

function useLiveDuration(startTime: string, endTime?: string, isRunning?: boolean): string {
  const [duration, setDuration] = useState(() => formatDuration(startTime, endTime));

  useEffect(() => {
    if (!isRunning || endTime) {
      setDuration(formatDuration(startTime, endTime));
      return;
    }

    const interval = setInterval(() => {
      setDuration(formatDuration(startTime, endTime));
    }, 1000);

    return () => clearInterval(interval);
  }, [startTime, endTime, isRunning]);

  return duration;
}

function getStatusConfig(status: ProcessInfo["status"], exitCode?: number) {
  switch (status) {
    case BackgroundProcessStatus.RUNNING:
      return {
        label: "Running",
        bgColor: "bg-green-500/10",
        textColor: "text-green-500",
        borderColor: "border-green-500/30",
        icon: <Loader2 className="w-3 h-3 animate-spin" />,
      };
    case BackgroundProcessStatus.COMPLETED:
      return {
        label: exitCode === 0 ? "Completed" : `Exit ${exitCode}`,
        bgColor: exitCode === 0 ? "bg-green-500/10" : "bg-yellow-500/10",
        textColor: exitCode === 0 ? "text-green-500" : "text-yellow-500",
        borderColor: exitCode === 0 ? "border-green-500/30" : "border-yellow-500/30",
        icon: null,
      };
    case BackgroundProcessStatus.FAILED:
      return {
        label: `Failed (exit ${exitCode})`,
        bgColor: "bg-red-500/10",
        textColor: "text-red-500",
        borderColor: "border-red-500/30",
        icon: null,
      };
    case BackgroundProcessStatus.KILLED:
    case BackgroundProcessStatus.KILLED_EXTERNALLY:
      return {
        label: "Killed",
        bgColor: "bg-yellow-500/10",
        textColor: "text-yellow-500",
        borderColor: "border-yellow-500/30",
        icon: null,
      };
    default:
      return {
        label: "Unknown",
        bgColor: "bg-muted",
        textColor: "text-muted-foreground",
        borderColor: "border-border",
        icon: null,
      };
  }
}

// ============================================
// Main Component
// ============================================

export function TerminalOutput({
  process,
  output,
  isLoading,
  onBack,
  onClose: _onClose,
  onRefresh,
  onKill,
  onOpenPort,
  className,
}: TerminalOutputProps) {
  const [copied, setCopied] = useState(false);
  const [autoScroll, setAutoScroll] = useState(true);
  const [isCanceling, setIsCanceling] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);
  const outputEndRef = useRef<HTMLDivElement>(null);

  const isRunning = process.status === BackgroundProcessStatus.RUNNING;

  // Reset canceling state when process stops running
  useEffect(() => {
    if (!isRunning) {
      setIsCanceling(false);
    }
  }, [isRunning]);

  const handleKill = async () => {
    if (isCanceling || !onKill) return;
    setIsCanceling(true);
    try {
      await onKill();
    } catch {
      setIsCanceling(false);
    }
  };
  const duration = useLiveDuration(process.start_time, process.end_time, isRunning);
  const statusConfig = getStatusConfig(process.status, process.exit_code);

  // Use combined output from backend if available (properly interleaved)
  // Fall back to frontend combination for backwards compatibility
  const combinedOutput = output
    ? (output.combined && output.combined.length > 0 
        ? output.combined 
        : combineOutputFallback(output.stdout, output.stderr))
    : null;

  // Auto-scroll to bottom when output updates
  useEffect(() => {
    if (autoScroll && outputEndRef.current) {
      outputEndRef.current.scrollIntoView({ behavior: "smooth" });
    }
  }, [combinedOutput, autoScroll]);

  const handleCopy = async () => {
    if (!output) return;
    const text = (output.stdout + "\n" + output.stderr).trim();
    await navigator.clipboard.writeText(text);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const handleScroll = (e: React.UIEvent<HTMLDivElement>) => {
    const element = e.currentTarget;
    const isAtBottom = element.scrollHeight - element.scrollTop - element.clientHeight < 50;
    setAutoScroll(isAtBottom);
  };

  return (
    <div className={cn("flex flex-col h-full bg-background", className)}>
      {/* Header */}
      <div className="flex items-center gap-3 px-4 py-3 border-b border-border bg-card/50">
        {onBack && (
          <button
            onClick={onBack}
            className="p-1.5 hover:bg-muted rounded transition-colors"
            title="Back"
          >
            <ArrowLeft className="w-4 h-4" />
          </button>
        )}

        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 flex-wrap">
            <code className="text-sm font-mono truncate">{process.command}</code>
            <span
              className={cn(
                "inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-xs font-medium border",
                statusConfig.bgColor,
                statusConfig.textColor,
                statusConfig.borderColor
              )}
            >
              {statusConfig.icon}
              {statusConfig.label}
            </span>
          </div>
        </div>

        <div className="flex items-center gap-1">
          {isRunning && onKill && (
            <button
              onClick={handleKill}
              disabled={isCanceling}
              className={cn(
                "p-1.5 rounded transition-colors",
                isCanceling 
                  ? "text-yellow-500 cursor-wait" 
                  : "hover:bg-destructive/10 text-destructive"
              )}
              title={isCanceling ? "Stopping..." : "Stop process"}
            >
  {isCanceling ? (
                <Loader2 className="w-4 h-4 animate-spin" />
              ) : (
                <Square className="w-4 h-4" />
              )}
            </button>
          )}
          {onRefresh && (
            <button
              onClick={onRefresh}
              className="p-1.5 hover:bg-muted rounded transition-colors"
              title="Refresh"
            >
              <RefreshCw className={cn("w-4 h-4", isLoading && "animate-spin")} />
            </button>
          )}
          <button
            onClick={handleCopy}
            className="p-1.5 hover:bg-muted rounded transition-colors"
            title="Copy output"
            disabled={!output}
          >
            {copied ? (
              <Check className="w-4 h-4 text-green-500" />
            ) : (
              <Copy className="w-4 h-4" />
            )}
          </button>
        </div>
      </div>

      {/* Stats bar */}
      <div className="flex items-center gap-4 px-4 py-2 border-b border-border bg-muted/20 text-xs flex-wrap">
        <div className="flex items-center gap-1.5">
          <Clock className="w-3.5 h-3.5 text-muted-foreground" />
          <span className="font-mono">{duration}</span>
        </div>

        {process.working_dir && (
          <div className="flex items-center gap-1.5 min-w-0">
            <Folder className="w-3.5 h-3.5 text-muted-foreground flex-shrink-0" />
            <span className="font-mono truncate" title={process.working_dir}>
              {process.working_dir.split("/").slice(-2).join("/")}
            </span>
          </div>
        )}

        {process.ports && process.ports.length > 0 && (
          <PortsDisplay 
            ports={process.ports} 
            onOpenPort={onOpenPort} 
          />
        )}
      </div>

      {/* Terminal output area */}
      <div
        ref={scrollRef}
        onScroll={handleScroll}
        className="flex-1 overflow-auto bg-[#1e1e1e] min-h-0 relative"
      >
        {isLoading && !combinedOutput ? (
          <div className="flex items-center justify-center h-full text-muted-foreground">
            <Loader2 className="w-5 h-5 animate-spin mr-2" />
            Loading output...
          </div>
        ) : !combinedOutput || combinedOutput.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-full text-muted-foreground">
            <Terminal className="w-12 h-12 mb-3 opacity-40" />
            <p className="text-sm">No output yet</p>
            {isRunning && (
              <p className="text-xs mt-1 opacity-70">
                Output will appear here as the process runs
              </p>
            )}
          </div>
        ) : (
          <AnsiOutputRenderer lines={combinedOutput} outputEndRef={outputEndRef} />
        )}

        {/* Scroll to bottom button */}
        {combinedOutput && combinedOutput.length > 0 && !autoScroll && (
          <button
            onClick={() => {
              setAutoScroll(true);
              outputEndRef.current?.scrollIntoView({ behavior: "smooth" });
            }}
            className="absolute bottom-4 right-4 px-3 py-1.5 rounded-full bg-primary text-primary-foreground text-xs font-medium shadow-lg hover:bg-primary/90 transition-colors"
          >
            ↓ Scroll to bottom
          </button>
        )}
      </div>
    </div>
  );
}

// ============================================
// ANSI Output Renderer Component
// ============================================

interface AnsiOutputRendererProps {
  lines: OutputLine[];
  outputEndRef: React.RefObject<HTMLDivElement | null>;
}

function AnsiOutputRenderer({ lines, outputEndRef }: AnsiOutputRendererProps) {
  // Create AnsiUp instance once, memoized
  const ansiUp = useMemo(() => {
    const instance = new AnsiUp();
    // Use classes instead of inline styles for better theme support
    instance.use_classes = true;
    return instance;
  }, []);

  return (
    <div className="p-4 font-mono text-sm leading-relaxed">
      {lines.map((line, index) => (
        <div
          key={index}
          className={cn(
            "whitespace-pre-wrap break-all",
            line.type === "stderr" ? "text-red-400" : "text-gray-200"
          )}
          dangerouslySetInnerHTML={{ __html: DOMPurify.sanitize(ansiUp.ansi_to_html(line.text)) }}
        />
      ))}
      <div ref={outputEndRef} />
    </div>
  );
}

// ============================================
// Helper: Combine stdout/stderr (fallback for old API responses)
// ============================================

function combineOutputFallback(stdout: string, stderr: string): OutputLine[] {
  const lines: OutputLine[] = [];

  // Add stdout lines
  if (stdout) {
    const stdoutLines = stdout.split("\n");
    for (const line of stdoutLines) {
      // Keep empty lines to preserve formatting
      lines.push({ type: "stdout", text: line });
    }
  }

  // Add stderr lines (they appear after stdout since we don't have timestamps)
  if (stderr) {
    const stderrLines = stderr.split("\n");
    for (const line of stderrLines) {
      lines.push({ type: "stderr", text: line });
    }
  }

  // Remove trailing empty lines
  while (lines.length > 0 && lines[lines.length - 1].text === "") {
    lines.pop();
  }

  return lines;
}
