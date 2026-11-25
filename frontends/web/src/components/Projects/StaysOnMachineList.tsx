// Copyright (c) 2025 Reliant Labs

/**
 * "These stay on <machine>": what a set-up on another machine does not take
 * with it (lib/machineMove.ts), read from the machine the project is on now
 * (PreviewProjectMachineMove). Shown wherever a project is set up on another
 * machine, before the clone is queued, and beside the offer to detach the old
 * machine once it landed.
 *
 * It only reads: the server never wakes a machine for it, and an offline one
 * still shows what Reliant knows (branch workspaces, repositories with no
 * remote). A server without the RPC (Unimplemented) renders nothing, as does
 * a project with no checkout on that machine.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { Loader2 } from "lucide-react";

import { usePreviewProjectMachineMove } from "@/hooks/project-service-hooks_gen";
import { stayBehind } from "@/lib/machineMove";
import { cn } from "@/lib/utils";

export function StaysOnMachineList({
  projectId,
  daemonId,
  machineName,
  className,
}: {
  projectId: string;
  /** The machine the project is on now: the one the move leaves. */
  daemonId: string;
  /** Its name, when the server's preview carries none. */
  machineName: string;
  className?: string;
}) {
  const preview = usePreviewProjectMachineMove(
    { projectId, daemonId },
    {
      enabled: Boolean(projectId && daemonId),
      staleTime: 15_000,
      retry: false,
      meta: { silenceErrorToast: true },
    },
  );

  const box = cn("rounded-md border border-border/60 bg-background px-3 py-2 text-xs", className);

  if (preview.isLoading) {
    return (
      <p role="status" className={cn(box, "flex items-center gap-1.5 text-muted-foreground")} data-testid="stays-on-machine">
        <Loader2 className="h-3 w-3 animate-spin motion-reduce:animate-none" aria-hidden="true" />
        Checking what stays on {machineName}…
      </p>
    );
  }
  if (preview.error) {
    // An older server has no preview: say nothing rather than a failure the
    // user can do nothing about.
    if (ConnectError.from(preview.error).code === Code.Unimplemented) return null;
    return (
      <p className={cn(box, "text-muted-foreground")} data-testid="stays-on-machine">
        Couldn&apos;t check what stays on {machineName}.
      </p>
    );
  }
  if (!preview.data) return null;

  const { summary, items } = stayBehind(preview.data, machineName);
  if (!summary) return null;
  return (
    <div className={box} data-testid="stays-on-machine">
      <p className="text-foreground">{summary}</p>
      {items.length > 0 && (
        <ul className="mt-1 space-y-0.5 text-muted-foreground" aria-label={`Stays on ${preview.data.daemonName || machineName}`}>
          {items.map((item) => (
            <li key={item.key} className="break-words">
              {item.text}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
