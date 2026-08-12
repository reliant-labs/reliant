/**
 * Read-only Packages drill-in — command list + process status only.
 *
 * Deliberately NOT `CommandsViewerTab`: that component is desktop's fully
 * interactive run/kill/favorites/search surface (~1900 lines), with no
 * read-only mode to opt into. Rather than gating run/kill controls out of an
 * interactive component built for a mouse, this reads the same
 * `usePackageCommands`/`usePackageProcesses` hooks directly and renders a
 * plain list — matching the "command list and process status; read-only"
 * mobile scope exactly.
 */

import { Hammer, Package as PackageIcon, Zap, CheckCircle2, XCircle, Loader2, Ban } from "lucide-react";
import {
  usePackageCommands,
  usePackageProcesses,
  type PackageCommand,
  type PackageType,
} from "../../hooks/package-queries";
import { BackgroundProcessStatus } from "../../api/background-grpc";
import { cn } from "../../lib/utils";

const PACKAGE_TYPE_ICON: Record<PackageType, React.ReactNode> = {
  makefile: <Hammer className="h-3.5 w-3.5" />,
  npm: <PackageIcon className="h-3.5 w-3.5" />,
  taskfile: <Zap className="h-3.5 w-3.5" />,
};

function statusIcon(status: BackgroundProcessStatus) {
  switch (status) {
    case BackgroundProcessStatus.RUNNING:
      return <Loader2 className="h-3.5 w-3.5 animate-spin text-primary" />;
    case BackgroundProcessStatus.COMPLETED:
      return <CheckCircle2 className="h-3.5 w-3.5 text-success" />;
    case BackgroundProcessStatus.FAILED:
      return <XCircle className="h-3.5 w-3.5 text-destructive" />;
    case BackgroundProcessStatus.KILLED:
      return <Ban className="h-3.5 w-3.5 text-muted-foreground" />;
    default:
      return null;
  }
}

function statusLabel(status: BackgroundProcessStatus): string {
  switch (status) {
    case BackgroundProcessStatus.RUNNING:
      return "Running";
    case BackgroundProcessStatus.COMPLETED:
      return "Completed";
    case BackgroundProcessStatus.FAILED:
      return "Failed";
    case BackgroundProcessStatus.KILLED:
      return "Killed";
    default:
      return "Unknown";
  }
}

interface MobilePackagesPanelProps {
  worktreeId?: string;
  projectPath?: string;
}

export function MobilePackagesPanel({ worktreeId, projectPath }: MobilePackagesPanelProps) {
  const commandsQuery = usePackageCommands(worktreeId, projectPath);
  const processesQuery = usePackageProcesses(worktreeId);

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
      {processes.length > 0 && (
        <div className="border-b border-border">
          <div className="px-3 pb-1 pt-3 text-xs font-bold uppercase tracking-[0.06em] text-muted-foreground/80">
            Processes
          </div>
          <div className="space-y-1 px-2 pb-2">
            {processes.map((process) => (
              <div
                key={process.id}
                className="flex items-center gap-2 rounded-md border border-border bg-background/50 px-2 py-2"
              >
                {statusIcon(process.status)}
                <div className="min-w-0 flex-1">
                  <div className="truncate font-mono text-xs text-foreground">
                    {process.command}
                  </div>
                  <div className="text-xs text-muted-foreground">
                    {statusLabel(process.status)}
                  </div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      <div className="px-3 pb-1 pt-3 text-xs font-bold uppercase tracking-[0.06em] text-muted-foreground/80">
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
                "flex items-center gap-2 rounded-md border border-border bg-background/50 px-2 py-2",
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
