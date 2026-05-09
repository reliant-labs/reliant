/**
 * Process Logs Viewer for Package Commands
 * 
 * A wrapper around TerminalOutput that connects to the packageCommandsStore.
 * Uses gRPC streaming for real-time output instead of polling.
 */

import { useEffect, useRef, useState, useCallback } from "react";
import { usePackageProcesses, useKillProcess, type ProcessLogsResponse } from "../../hooks/package-queries";
import { TerminalOutput, type ProcessInfo } from "../shared/TerminalOutput";
import { cn } from "../../lib/utils";
import {
  ProcessOutputStreamingService,
  type OutputLine,
} from "../../api/process-output-streaming";
import { logger } from "../../lib/logger";
import { BackgroundProcessStatus } from "../../api/background-grpc";

interface ProcessLogsViewerProps {
  processId: string;
  ports?: { port: number; protocol?: string }[];
  onClose?: () => void;
  onBack?: () => void;
  onOpenPort?: (port: number) => void;
  className?: string;
}

export function ProcessLogsViewer({
  processId,
  ports,
  onClose,
  onBack,
  onOpenPort,
  className,
}: ProcessLogsViewerProps) {
  const { data: processes = [] } = usePackageProcesses();
  const killProcessMutation = useKillProcess();

  // Local state for streamed output
  const [logs, setLogs] = useState<ProcessLogsResponse | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const streamingServiceRef = useRef<ProcessOutputStreamingService | null>(null);

  const process = processes.find((p) => p.id === processId);

  // Build combined text from output lines for stdout/stderr fields
  const buildLogsFromLines = useCallback((lines: OutputLine[]): ProcessLogsResponse => {
    const stdout: string[] = [];
    const stderr: string[] = [];
    const combined: Array<{ type: "stdout" | "stderr"; text: string }> = [];

    for (const line of lines) {
      if (line.type === "stdout") {
        stdout.push(line.text);
      } else {
        stderr.push(line.text);
      }
      combined.push({ type: line.type, text: line.text });
    }

    return {
      stdout: stdout.join("\n"),
      stderr: stderr.join("\n"),
      combined,
    };
  }, []);

  // Set up streaming on mount
  useEffect(() => {
    // Create streaming service with callbacks
    const service = new ProcessOutputStreamingService(processId, {
      onSnapshot: (lines, _latestSequence) => {
        logger.debug("[ProcessLogsViewer] Received snapshot", {
          processId: processId.slice(0, 8),
          lineCount: lines.length,
        });
        setLogs(buildLogsFromLines(lines));
        setIsLoading(false);
      },
      onLine: (line) => {
        setLogs((prev) => {
          if (!prev) {
            // First line, create new logs
            return buildLogsFromLines([line]);
          }
          // Append to existing logs
          const newStdout = line.type === "stdout"
            ? prev.stdout + (prev.stdout ? "\n" : "") + line.text
            : prev.stdout;
          const newStderr = line.type === "stderr"
            ? prev.stderr + (prev.stderr ? "\n" : "") + line.text
            : prev.stderr;
          return {
            stdout: newStdout,
            stderr: newStderr,
            combined: [...prev.combined, { type: line.type, text: line.text }],
          };
        });
      },
      onComplete: (status, exitCode, _endTime) => {
        logger.info("[ProcessLogsViewer] Process complete", {
          processId: processId.slice(0, 8),
          status,
          exitCode,
        });
        // Stream will close automatically, no action needed
      },
      onError: (error) => {
        logger.error("[ProcessLogsViewer] Stream error", {
          processId: processId.slice(0, 8),
          error,
        });
        setIsLoading(false);
      },
      onStatusChange: (status) => {
        logger.debug("[ProcessLogsViewer] Stream status", {
          processId: processId.slice(0, 8),
          status,
        });
        if (status === "connected") {
          setIsLoading(false);
        }
      },
    });

    streamingServiceRef.current = service;
    service.start();

    return () => {
      service.stop();
      streamingServiceRef.current = null;
    };
  }, [processId, buildLogsFromLines]);

  // Manual refresh - restart stream to get fresh data
  const handleRefresh = useCallback(() => {
    if (streamingServiceRef.current) {
      streamingServiceRef.current.stop();
    }
    setLogs(null);
    setIsLoading(true);
    
    // Create new service to restart stream
    const service = new ProcessOutputStreamingService(processId, {
      onSnapshot: (lines) => {
        setLogs(buildLogsFromLines(lines));
        setIsLoading(false);
      },
      onLine: (line) => {
        setLogs((prev) => {
          if (!prev) return buildLogsFromLines([line]);
          const newStdout = line.type === "stdout"
            ? prev.stdout + (prev.stdout ? "\n" : "") + line.text
            : prev.stdout;
          const newStderr = line.type === "stderr"
            ? prev.stderr + (prev.stderr ? "\n" : "") + line.text
            : prev.stderr;
          return {
            stdout: newStdout,
            stderr: newStderr,
            combined: [...prev.combined, { type: line.type, text: line.text }],
          };
        });
      },
      onComplete: () => {},
      onError: () => setIsLoading(false),
      onStatusChange: (status) => {
        if (status === "connected") setIsLoading(false);
      },
    });
    streamingServiceRef.current = service;
    service.start();
  }, [processId, buildLogsFromLines]);

  if (!process) {
    return (
      <div
        className={cn(
          "flex items-center justify-center p-8 text-muted-foreground",
          className
        )}
      >
        Process not found
      </div>
    );
  }

  // Convert to ProcessInfo format
  const processInfo: ProcessInfo = {
    id: process.id,
    command: process.command,
    status: process.status,
    start_time: process.start_time,
    end_time: process.end_time,
    exit_code: process.exit_code,
    working_dir: process.working_dir,
    ports,
  };

  return (
    <TerminalOutput
      process={processInfo}
      output={logs || null}
      isLoading={isLoading}
      onBack={onBack}
      onClose={onClose}
      onRefresh={handleRefresh}
      onKill={
        process.status === BackgroundProcessStatus.RUNNING
          ? () => killProcessMutation.mutate(processId)
          : undefined
      }
      onOpenPort={onOpenPort}
      className={className}
    />
  );
}