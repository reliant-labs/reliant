/**
 * Read-only Packages drill-in — command list + process status only: the
 * workspace's command processes, and the ones the open chat's agents started.
 *
 * Deliberately NOT `CommandsViewerTab`: that component is desktop's fully
 * interactive run/kill/favorites/search surface (~1900 lines), with no
 * read-only mode to opt into. Rather than gating run/kill controls out of an
 * interactive component built for a mouse, this reads the same
 * `usePackageCommands`/`usePackageProcesses` hooks directly and renders a
 * plain list — matching the "command list and process status; read-only"
 * mobile scope exactly.
 */

import { Hammer, Package as PackageIcon, Zap, Loader2 } from "lucide-react";
import {
  usePackageCommands,
  usePackageProcesses,
  type PackageCommand,
  type PackageType,
} from "../../hooks/package-queries";
import { BackgroundProcessStatus, useChatBackgroundProcesses } from "../../hooks/background-queries";
import { useActiveChatId } from "../../store/chatStoreHooks";
import Status from "../forge-ui/status";
import { processStatusView } from "../../lib/processStatus";
import { cn } from "../../lib/utils";

const PACKAGE_TYPE_ICON: Record<PackageType, React.ReactNode> = {
  makefile: <Hammer className="h-3.5 w-3.5" />,
  npm: <PackageIcon className="h-3.5 w-3.5" />,
  taskfile: <Zap className="h-3.5 w-3.5" />,
};

function ProcessStatusLine({ status, exitCode }: { status: BackgroundProcessStatus; exitCode?: number }) {
  const view = processStatusView(status, exitCode);
  return <Status tone={view.tone} label={view.label} pulse={view.running} size="sm" className="text-xs text-muted-foreground" />;
}

function ProcessList({ title, processes }: { title: string; processes: { id: string; command: string; status: BackgroundProcessStatus; exit_code?: number }[] }) {
  return (
    <div className="border-b border-border">
      <div className="px-3 pb-1 pt-3 text-xs font-medium text-muted-foreground">{title}</div>
      <div className="space-y-1 px-2 pb-2">
        {processes.map((process) => (
          <div key={process.id} className="flex items-center gap-2 rounded-md border border-border bg-background px-2 py-2">
            <div className="min-w-0 flex-1">
              <div className="truncate font-mono text-xs text-foreground">{process.command}</div>
              <ProcessStatusLine status={process.status} exitCode={process.exit_code} />
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

interface MobilePackagesPanelProps {
  worktreeId?: string;
  projectPath?: string;
}

export function MobilePackagesPanel({ worktreeId, projectPath }: MobilePackagesPanelProps) {
  const commandsQuery = usePackageCommands(worktreeId, projectPath);
  const processesQuery = usePackageProcesses(worktreeId);
  // What the open chat's agents started in the background: no workspace
  // claims those, so the workspace list above never shows them.
  const chatProcesses = useChatBackgroundProcesses(useActiveChatId());
  const agentProcesses = [...chatProcesses.running, ...chatProcesses.recent];

  const commands = commandsQuery.data?.commands ?? ({} as Record<PackageType, PackageCommand[]>);
  const processes = processesQuery.data ?? [];

  const isLoading = commandsQuery.isLoading || processesQuery.isLoading;
  const allCommands = Object.entries(commands).flatMap(([type, cmds]) =>
    cmds.map((cmd) => ({ type: type as PackageType, cmd })),
  );

  if (isLoading) {
    return (
      <div className="flex flex-1 items-center justify-center py-10 text-muted-foreground">
        <Loader2 className="h-5 w-5 animate-spin" />
      </div>
    );
  }

  return (
    <div className="flex-1 overflow-y-auto">
      {agentProcesses.length > 0 && <ProcessList title="This chat" processes={agentProcesses} />}
      {processes.length > 0 && <ProcessList title="Processes" processes={processes} />}

      <div className="px-3 pb-1 pt-3 text-xs font-medium text-muted-foreground">
        Commands
      </div>
      {allCommands.length === 0 ? (
        <div className="px-4 py-6 text-center text-sm text-muted-foreground">
          No package commands detected in this workspace.
        </div>
      ) : (
        <div className="space-y-1 px-2 pb-4">
          {allCommands.map(({ type, cmd }) => (
            <div
              key={`${type}:${cmd.relative_path ?? ""}:${cmd.name}`}
              className={cn(
                "flex items-center gap-2 rounded-md border border-border bg-background px-2 py-2",
              )}
            >
              <span className="flex h-5 w-5 items-center justify-center text-muted-foreground">
                {PACKAGE_TYPE_ICON[type]}
              </span>
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium text-foreground">{cmd.name}</div>
                <div className="truncate font-mono text-xs text-muted-foreground">
                  {cmd.command}
                </div>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
