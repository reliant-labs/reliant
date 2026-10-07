import { Check, Server } from "lucide-react";
import { cn } from "@/lib/utils";
import { machineDisplayName } from "@/lib/machineName";
import type { DaemonInfo as CloudDaemon } from "@/gen/reliant/v1/daemon_registry_pb";
import { cloneTargetOptions } from "./cloneTargets";

/**
 * Which machine a clone lands on — always stated, and choosable when there is
 * a choice.
 *
 * With several machines it is a radio group, the most recently used one
 * preselected (see `cloneTargets.pickCloneTarget`, which applies the same
 * ordering the list renders in). With ONE it is a plain statement of the
 * target. That case used to render nothing at all, on the theory that a choice
 * of one is not a choice — but the target is still a fact the user needs: the
 * owner on 2026-10-07 had exactly one machine in the list, did not know which
 * machine the clone would use, and it was a deleted one.
 *
 * Callers pass only machines that may be offered: `daemons` is already
 * stripped of machines the control plane said are gone (lib/goneMachines).
 * FAILED machines are dropped here; nothing will ever drain their queue, and
 * the picker's "Clone repo" button owns the all-machines-failed case.
 */
export function CloneTargetPicker({
  daemons,
  selectedDaemonId,
  onSelect,
}: {
  daemons: CloudDaemon[];
  selectedDaemonId: string | null;
  onSelect: (daemonId: string) => void;
}) {
  const options = cloneTargetOptions(daemons);
  if (options.length === 0) return null;

  if (options.length === 1) {
    const { daemon, immediate } = options[0];
    return (
      <div
        className="mb-4 flex items-center gap-3 rounded-md border border-border/60 bg-background px-3 py-2.5"
        data-testid="clone-target-summary"
      >
        <Server className="h-4 w-4 flex-shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="min-w-0 flex-1">
          <span className="block text-xs text-muted-foreground">Clone onto</span>
          <span className="block truncate text-sm font-medium text-foreground">{machineDisplayName(daemon)}</span>
        </span>
        <span className="text-xs text-muted-foreground">
          {immediate ? "Ready — clones now" : "Not running — clones when it's ready"}
        </span>
      </div>
    );
  }

  return (
    <fieldset className="mb-4" data-testid="clone-target-picker">
      <legend className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">Clone onto</legend>
      <div className="space-y-1.5" role="radiogroup" aria-label="Machine to clone onto">
        {options.map(({ daemon, immediate }) => {
          const isSelected = daemon.daemonId === selectedDaemonId;
          return (
            <button
              key={daemon.daemonId}
              type="button"
              role="radio"
              aria-checked={isSelected}
              onClick={() => onSelect(daemon.daemonId)}
              data-testid={`clone-target-${daemon.daemonId}`}
              className={cn(
                "flex w-full items-center gap-3 rounded-md border px-3 py-2.5 text-left transition-colors",
                isSelected
                  ? "border-primary bg-primary/10"
                  : "border-border/60 bg-background hover:border-border",
              )}
            >
              <Server className="h-4 w-4 flex-shrink-0 text-muted-foreground" aria-hidden="true" />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-sm font-medium text-foreground">{machineDisplayName(daemon)}</span>
                {/* States the honest outcome per machine: a machine that is
                    not running yet QUEUES the clone, and saying "cloned"
                    there is the false success this flow exists to avoid. */}
                <span className="block text-xs text-muted-foreground">
                  {immediate ? "Ready — clones now" : "Not running — clones when it's ready"}
                </span>
              </span>
              {isSelected && <Check className="h-4 w-4 flex-shrink-0 text-primary" aria-hidden="true" />}
            </button>
          );
        })}
      </div>
    </fieldset>
  );
}
