/**
 * Process Logs Viewer
 *
 * The live log of one background process — a Commands-panel command or a
 * command an agent ran in the background — streamed from the machine it runs
 * on (useProcessOutputStream), with Stop while it runs.
 */

import { useMemo } from "react";
import { ArrowLeft, Loader2, SearchX } from "lucide-react";
import { useKillPackageProcess } from "../../hooks/package-queries";
import { BackgroundProcessStatus, useStopBackgroundProcess } from "../../hooks/background-queries";
import { useUnifiedProcesses } from "../../hooks/useUnifiedProcesses";
import { useProcessOutputStream } from "../../hooks/useProcessOutputStream";
import { TerminalOutput, type ProcessInfo } from "../shared/TerminalOutput";
import { cn } from "../../lib/utils";

interface ProcessLogsViewerProps {
  processId: string;
  /** Ports to show when the process record names none (a package process). */
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
  const { processes, isLoading: isLoadingProcesses } = useUnifiedProcesses();
  const process = processes.find((p) => p.id === processId);
  const killPackageProcess = useKillPackageProcess();
  const { stop: stopBackgroundProcess } = useStopBackgroundProcess();

  // The output streams from the machine the process runs on. Wait for the
  // record when the list is still loading, so the stream is not opened on
  // the default machine and then again on the right one.
  const stream = useProcessOutputStream(processId, {
    daemonId: process?.daemon_id,
    enabled: !!process || !isLoadingProcesses,
  });
  const output = useMemo(
    () => ({
      stdout: "",
      stderr: stream.error && stream.lines.length === 0 ? `Could not stream the output: ${stream.error}` : "",
      combined: stream.lines,
    }),
    [stream.lines, stream.error],
  );

  if (!process) {
    return (
      <div className={cn("flex h-full flex-col bg-background", className)}>
        {onBack && (
          <div className="flex items-center gap-2 border-b border-border px-4 py-3">
            <button
              type="button"
              onClick={onBack}
              className="rounded p-1.5 transition-colors hover:bg-muted"
              aria-label="Back"
            >
              <ArrowLeft className="h-4 w-4" />
            </button>
          </div>
        )}
        <div className="flex flex-1 flex-col items-center justify-center gap-2 p-8 text-center text-sm text-muted-foreground">
          {isLoadingProcesses ? (
            <>
              <Loader2 className="h-5 w-5 animate-spin" />
              Finding the process…
            </>
          ) : (
            <>
              <SearchX className="h-6 w-6 opacity-60" />
              <p className="font-medium text-foreground">This process is no longer tracked</p>
              <p className="max-w-xs text-xs">
                Its machine restarted or cleared it from its list, so its output is gone. The command&apos;s tool call
                in the chat still shows what the agent read.
              </p>
            </>
          )}
        </div>
      </div>
    );
  }

  const processInfo: ProcessInfo = {
    id: process.id,
    command: process.command,
    status: process.status,
    start_time: process.start_time,
    end_time: process.end_time,
    exit_code: process.exit_code,
    working_dir: process.working_dir,
    ports: process.ports ?? ports,
  };

  const handleKill =
    process.status === BackgroundProcessStatus.RUNNING
      ? async () => {
          if (process.source === "package") {
            await killPackageProcess.mutateAsync({ processId, daemonId: process.daemon_id });
          } else {
            await stopBackgroundProcess(process);
          }
        }
      : undefined;

  return (
    <TerminalOutput
      process={processInfo}
      output={output}
      isLoading={stream.isLoading}
      onBack={onBack}
      onClose={onClose}
      onRefresh={stream.restart}
      onKill={handleKill}
      onOpenPort={onOpenPort}
      className={className}
    />
  );
}
