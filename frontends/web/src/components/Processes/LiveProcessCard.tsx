/**
 * LiveProcessCard — a background command an agent started, live, where the
 * agent started it.
 *
 * A shell call with run_in_background returns a process id and nothing else,
 * so the tool card used to print that id and tell the reader to use
 * shell_output / shell_kill — tools only the agent has. This card is the
 * process itself: its state, how long it has run, the ports it listens on,
 * the newest lines of its output streamed from its machine, and the two
 * things a person wants to do with it — read the whole log, or stop it.
 */

import { useEffect, useState } from "react";
import { Loader2, ScrollText, Square } from "lucide-react";
import Status from "../forge-ui/status";
import Tooltip from "../forge-ui/tooltip";
import { PortsDisplay } from "../shared/PortsDisplay";
import {
  refreshBackgroundProcesses,
  useBackgroundProcess,
  useStopBackgroundProcess,
} from "../../hooks/background-queries";
import { useProcessOutputStream } from "../../hooks/useProcessOutputStream";
import { useActiveDaemonPreviewTemplate } from "../../hooks/useActiveDaemonPreviewTemplate";
import { useProjectStore } from "../../store/projectStore";
import { useViewerStore } from "../../store/viewerStore";
import { devServerUrl } from "../../lib/dev-preview";
import { openExternalLink } from "../../lib/open-link";
import { processStatusView, parseProcessTime } from "../../lib/processStatus";
import { toast } from "../../lib/toast-manager";
import { cn } from "../../lib/utils";
import { ElapsedTime } from "./ElapsedTime";
import { reaperNote } from "./reaperNote";

// CSI and OSC escape sequences: the tail is plain text (the log viewer renders colour).
// eslint-disable-next-line no-control-regex
const ANSI_PATTERN = /\u001b\[[0-9;?]*[ -/]*[@-~]|\u001b\][^\u0007]*(\u0007|\u001b\\)/g;

function stripAnsi(text: string): string {
  return text.replace(ANSI_PATTERN, "");
}

/** Output lines the card shows; the log viewer has the rest. */
export const LIVE_CARD_TAIL_LINES = 8;

interface LiveProcessCardProps {
  processId: string;
  className?: string;
}

export function LiveProcessCard({ processId, className }: LiveProcessCardProps) {
  const { process, isFetched } = useBackgroundProcess(processId);
  const { stop } = useStopBackgroundProcess();
  const [isStopping, setIsStopping] = useState(false);
  const projectId = useProjectStore((s) => s.currentProject?.id);
  const openCommandsViewer = useViewerStore((s) => s.openCommandsViewer);
  const previewTemplate = useActiveDaemonPreviewTemplate();

  // A process started after the list was read is not in it yet: read it
  // again, once, rather than calling a live process lost.
  const [refresh, setRefresh] = useState<"idle" | "pending" | "done">("idle");
  useEffect(() => {
    if (isFetched && !process && refresh === "idle") {
      setRefresh("pending");
      void refreshBackgroundProcesses().finally(() => setRefresh("done"));
    }
  }, [isFetched, process, refresh]);

  const stream = useProcessOutputStream(processId, {
    daemonId: process?.daemon_id,
    maxLines: LIVE_CARD_TAIL_LINES,
    enabled: !!process,
  });

  const status = process ? processStatusView(process.status, process.exit_code) : null;
  useEffect(() => {
    if (!status?.running) setIsStopping(false);
  }, [status?.running]);

  if (!process || !status) {
    return (
      <div
        className={cn("flex items-center gap-2 px-2 py-1.5 text-xs text-muted-foreground", className)}
        data-testid="live-process-card-missing"
      >
        {refresh !== "done" ? (
          <>
            <Loader2 className="h-3 w-3 animate-spin" />
            Looking up background process…
          </>
        ) : (
          <>
            <Status tone="neutral" size="sm" />
            Background process <span className="font-mono text-foreground">{processId.slice(0, 8)}</span> is no
            longer tracked by its machine.
          </>
        )}
      </div>
    );
  }

  const handleStop = async () => {
    if (isStopping) return;
    setIsStopping(true);
    try {
      await stop(process);
    } catch (error) {
      setIsStopping(false);
      toast.error(error);
    }
  };

  const handleOpenLogs = () => {
    if (projectId) openCommandsViewer(projectId, undefined, process.id);
  };

  const reaper = status.running ? reaperNote(process) : null;

  return (
    <div className={cn("flex flex-col", className)} data-testid="live-process-card">
      <div className="flex items-center gap-2 px-2 py-1.5 text-xs">
        <Status tone={status.tone} label={status.label} pulse={status.running} size="sm" className="font-medium" />
        <ElapsedTime
          startMs={parseProcessTime(process.start_time)}
          endMs={parseProcessTime(process.end_time)}
          className="text-muted-foreground"
        />
        {process.ports && process.ports.length > 0 && (
          <PortsDisplay
            ports={process.ports}
            compact
            onOpenPort={(port) => void openExternalLink(devServerUrl(port, previewTemplate))}
          />
        )}
        <span className="ml-auto flex items-center gap-1">
          {projectId && (
            <Tooltip content="Open the full log" side="bottom" delay={300} triggerClassName="inline-flex">
              <button
                type="button"
                onClick={handleOpenLogs}
                className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground"
              >
                <ScrollText className="h-3.5 w-3.5" />
                Logs
              </button>
            </Tooltip>
          )}
          {status.running && (
            <Tooltip content={isStopping ? "Stopping…" : "Stop this process"} side="bottom" delay={300} triggerClassName="inline-flex">
              <button
                type="button"
                onClick={handleStop}
                disabled={isStopping}
                aria-label="Stop process"
                className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-destructive-ink transition-colors hover:bg-destructive/10 disabled:cursor-wait disabled:opacity-60"
              >
                {isStopping ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Square className="h-3.5 w-3.5" />}
                Stop
              </button>
            </Tooltip>
          )}
        </span>
      </div>

      <div className="border-t border-border/50 bg-background px-2 py-1.5 font-mono text-2xs leading-relaxed">
        {stream.lines.length === 0 ? (
          <span className="text-muted-foreground">
            {stream.isLoading ? "Connecting to output…" : status.running ? "No output yet" : "No output"}
          </span>
        ) : (
          stream.lines.map((line, index) => (
            <div
              key={`${line.sequence}-${index}`}
              className={cn(
                "whitespace-pre-wrap break-all",
                line.type === "stderr" ? "text-destructive-ink" : "text-foreground/90",
              )}
            >
              {stripAnsi(line.text) || " "}
            </div>
          ))
        )}
      </div>

      {reaper && <div className="px-2 py-1 text-2xs text-muted-foreground">{reaper}</div>}
    </div>
  );
}
