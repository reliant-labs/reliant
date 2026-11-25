// Copyright (c) 2025 Reliant Labs

/**
 * After a project landed on a new machine: the offer to detach the machine
 * it came from. Moving a project is "add the new machine, then offer
 * detach", so this is only ever an offer: nothing is detached without the
 * user's click, and nothing is offered until the new checkout is INSTALLED.
 *
 * Detach (DetachProjectFromDaemon) makes Reliant forget the old checkout and
 * leaves its files on disk. The server refuses it while branch workspaces of
 * the project live on that machine, or while it would leave no machine; its
 * sentence is shown as it is.
 */

import { useState } from "react";
import { Loader2 } from "lucide-react";

import { useDetachProjectFromDaemon } from "@/hooks/project-service-hooks_gen";
import { describeMachineFolderError } from "./machineFolderErrors";
import { StaysOnMachineList } from "./StaysOnMachineList";

export interface OldMachine {
  daemonId: string;
  name: string;
}

export function DetachOldMachineOffer({
  projectId,
  projectName,
  targetName,
  targetInstalled,
  oldMachines,
}: {
  projectId: string;
  projectName: string;
  /** The machine the project was just set up on. */
  targetName: string;
  /** Its checkout is there: only then is the old machine offered. */
  targetInstalled: boolean;
  /** The machines it was on before, as they were when the set-up started. */
  oldMachines: ReadonlyArray<OldMachine>;
}) {
  const detach = useDetachProjectFromDaemon({ meta: { silenceErrorToast: true } });
  const [busy, setBusy] = useState<string | null>(null);
  const [detached, setDetached] = useState<ReadonlySet<string>>(new Set());
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [dismissed, setDismissed] = useState(false);

  if (!targetInstalled || dismissed || oldMachines.length === 0) return null;

  const run = async (machine: OldMachine) => {
    setBusy(machine.daemonId);
    setErrors(({ [machine.daemonId]: _, ...rest }) => rest);
    try {
      await detach.mutateAsync({ projectId, daemonId: machine.daemonId });
      setDetached((prior) => new Set(prior).add(machine.daemonId));
    } catch (err) {
      setErrors((prior) => ({ ...prior, [machine.daemonId]: describeMachineFolderError(err, machine.name) }));
    } finally {
      setBusy(null);
    }
  };

  const pending = oldMachines.filter((machine) => !detached.has(machine.daemonId));
  return (
    <div
      className="space-y-2 rounded-md border border-border/60 bg-background px-3 py-2 text-xs"
      data-testid="detach-old-machine-offer"
    >
      <p className="text-foreground">
        {projectName} is set up on {targetName}.
        {pending.length > 0 && " You can detach the machine it was on: Reliant forgets that checkout and leaves its files on disk."}
      </p>
      {oldMachines.map((machine) =>
        detached.has(machine.daemonId) ? (
          <p key={machine.daemonId} role="status" className="text-success-ink">
            Detached from {machine.name}. Its files stay on disk.
          </p>
        ) : (
          <div key={machine.daemonId} className="space-y-1.5">
            <StaysOnMachineList projectId={projectId} daemonId={machine.daemonId} machineName={machine.name} />
            <button
              type="button"
              onClick={() => void run(machine)}
              disabled={busy !== null}
              className="inline-flex items-center gap-1.5 rounded-md border border-border px-2 py-1 font-medium text-foreground transition-colors hover:bg-foreground/5 disabled:opacity-60"
            >
              {busy === machine.daemonId && (
                <Loader2 className="h-3 w-3 animate-spin motion-reduce:animate-none" aria-hidden="true" />
              )}
              Detach from {machine.name} (keep files)
            </button>
            {errors[machine.daemonId] && (
              <p role="alert" className="text-destructive-ink">
                {errors[machine.daemonId]}
              </p>
            )}
          </div>
        ),
      )}
      {pending.length > 0 && (
        <button
          type="button"
          onClick={() => setDismissed(true)}
          className="rounded-md px-2 py-1 text-muted-foreground hover:bg-foreground/5 hover:text-foreground"
        >
          Not now
        </button>
      )}
    </div>
  );
}
