import { Check, Server } from "lucide-react";
import { cn } from "@/lib/utils";
import type { DaemonInfo as CloudDaemon } from "@/gen/reliant/v1/daemon_registry_pb";
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
      <legend className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">Clone onto</legend>
      <div className="space-y-1.5" role="radiogroup" aria-label="Machine to clone onto">
        {options.map(({ daemon, immediate }) => {
          const isSelected = daemon.daemonId === selectedDaemonId;
          const label = daemon.hostname || `machine ${daemon.daemonId.slice(0, 8)}`;
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
                <span className="block truncate text-sm font-medium text-foreground">{label}</span>
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
