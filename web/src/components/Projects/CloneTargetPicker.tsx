import { Check, Server } from "lucide-react";
import { cn } from "@/lib/utils";
import type { Daemon as CloudDaemon } from "@/services/controlPlane/daemon";
import { cloneTargetOptions } from "./cloneTargets";

/**
 * Which machine a clone lands on.
 *
 * The picker used to choose silently — the first ACTIVE daemon the server
 * happened to list — which is fine with one machine and wrong with several: a
 * checkout appears on a machine the user was not working on, and nothing in
 * the UI ever said which one it would be. So the choice is shown, with the
 * most recently used machine preselected (see `cloneTargets.pickCloneTarget`,
 * which applies the same ordering the list renders in).
 *
 * It renders nothing at all when there is only one candidate. A radio group of
 * one is not a choice, and asking the user to confirm the only possible answer
 * is noise on the common path.
 *
 * FAILED machines are absent rather than disabled: nothing will ever drain
 * their queue, so they are not choices. The picker's "Clone repo" button owns
 * the all-machines-failed case and explains it there.
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
  if (options.length < 2) return null;

  return (
    <fieldset className="mb-4" data-testid="clone-target-picker">
      <legend className="mb-2 text-sm font-medium text-foreground">Clone onto</legend>
      <div className="space-y-1.5">
        {options.map(({ daemon, immediate }) => {
          const isSelected = daemon.id === selectedDaemonId;
          const label = daemon.name || daemon.hostname || `machine ${daemon.id.slice(0, 8)}`;
          return (
            <button
              key={daemon.id}
              type="button"
              role="radio"
              aria-checked={isSelected}
              onClick={() => onSelect(daemon.id)}
              data-testid={`clone-target-${daemon.id}`}
              className={cn(
                "flex w-full items-center gap-3 rounded-lg border px-3 py-2.5 text-left transition-colors",
                isSelected
                  ? "border-primary bg-primary/10"
                  : "border-border/60 bg-background hover:bg-muted/60",
              )}
            >
              <Server className="h-4 w-4 flex-shrink-0 text-muted-foreground" />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-sm font-medium text-foreground">{label}</span>
                {/* States the honest outcome per machine: a machine that is
                    not running yet QUEUES the clone, and saying "cloned"
                    there is the false success this flow exists to avoid. */}
                <span className="block text-xs text-muted-foreground">
                  {immediate ? "Ready — clones now" : "Not running — clones when it's ready"}
                </span>
              </span>
              {isSelected && <Check className="h-4 w-4 flex-shrink-0 text-primary" />}
            </button>
          );
        })}
      </div>
    </fieldset>
  );
}
