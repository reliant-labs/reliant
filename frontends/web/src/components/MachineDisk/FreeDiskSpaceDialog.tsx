// Copyright (c) 2025 Reliant Labs

/**
 * "Free up space" on one machine volume: what is using it, what Reliant can
 * clear safely, and what it deliberately will not.
 *
 * Only what the machine reports as clearable is offered — a trim of Go-format
 * cache entries no build has used for 2 hours, and docker's unused build
 * cache — and only after the user ticks it and confirms. Docker images and
 * volumes, the Go module cache and package-manager caches are listed with the
 * reason they are left alone: a docker volume can be someone's database, and
 * the user (or the agent, with their go-ahead) has to make that call.
 *
 * The clean-up runs on the machine in the background; the volume's numbers and
 * a "Clearing…" state arrive on the next disk report.
 */

import { useState } from "react";
import { toast } from "sonner";

import type { DiskConsumer, DiskVolume } from "@/gen/reliant/v1/tools_daemon_pb";
import { useClearMachineCaches } from "@/hooks/useMachineDisk";
import { formatBytes } from "@/lib/formatBytes";
import {
  clearableConsumers,
  clearingConsumers,
  clearNote,
  notClearedNote,
  totalReclaimable,
  usedFraction,
  volumeUsage,
} from "@/lib/machineDisk";
import { Button } from "../ui/Button";
import Modal from "../forge-ui/modal";
import ProgressBar from "../forge-ui/progress_bar";
import { CardInset } from "../forge-ui/card";

export interface FreeDiskSpaceDialogProps {
  isOpen: boolean;
  onClose: () => void;
  daemonId: string;
  machineLabel: string;
  volume: DiskVolume;
  online: boolean;
}

export function FreeDiskSpaceDialog({ isOpen, onClose, daemonId, machineLabel, volume, online }: FreeDiskSpaceDialogProps) {
  const clear = useClearMachineCaches();
  const clearable = clearableConsumers(volume);
  const clearing = clearingConsumers(volume);
  // Everything clearable starts ticked: it is all rebuildable, and the dialog
  // itself is the confirmation.
  const [unticked, setUnticked] = useState<Set<string>>(new Set());
  const chosen = clearable.filter((c) => !unticked.has(c.kind));
  const leftAlone = volume.consumers.filter((c) => !c.clearable && notClearedNote(c) !== null);

  const toggle = (kind: string) =>
    setUnticked((prev) => {
      const next = new Set(prev);
      if (next.has(kind)) next.delete(kind);
      else next.add(kind);
      return next;
    });

  const confirm = () => {
    clear.mutate(
      { daemonId, kinds: chosen.map((c) => c.kind) },
      {
        onSuccess: (resp) => {
          const skipped = resp.skipped.map((s) => `${s.kind.replace(/_/g, " ")}: ${s.reason}`).join("; ");
          if (resp.started.length > 0) {
            toast.success(`Clearing ${resp.started.length === 1 ? "1 cache" : `${resp.started.length} caches`} on ${machineLabel}`, {
              description: ["This runs on the machine; the numbers update when it finishes.", skipped].filter(Boolean).join(" "),
            });
          } else {
            toast.error("Nothing was cleared", { description: skipped || undefined });
          }
          onClose();
        },
        onError: (error) =>
          toast.error("Could not start clearing", { description: error instanceof Error ? error.message : String(error) }),
      },
    );
  };

  const pct = Math.round(usedFraction(volume) * 100);
  return (
    <Modal open={isOpen} onClose={() => !clear.isPending && onClose()} title={`Free up space on ${machineLabel}`} size="md">
      <div className="flex flex-col gap-4" data-testid="free-disk-space-dialog">
        <div className="flex flex-col gap-1.5">
          <p className="text-sm text-foreground">{volumeUsage(volume)}</p>
          {/* .forge-ui: the fill is forge's `accent` (index.css). */}
          <ProgressBar
            value={pct}
            variant={volume.pressure ? "warning" : "default"}
            size="sm"
            ariaLabel="Disk usage"
            className="forge-ui"
          />
          {volume.paths.length > 0 && (
            <p className="truncate font-mono text-xs text-muted-foreground" title={volume.paths.join("\n")}>
              {volume.paths.slice(0, 3).join(", ")}
              {volume.paths.length > 3 ? ", …" : ""}
            </p>
          )}
        </div>

        {volume.consumers.length === 0 ? (
          <p className="text-pretty text-sm text-muted-foreground">
            The machine has not measured what is using this volume yet. It does so within a few minutes of the volume
            filling up.
          </p>
        ) : (
          <>
            {clearing.length > 0 && (
              <p className="text-sm text-muted-foreground" role="status">
                Clearing {clearing.map((c) => c.label).join(", ")}…
              </p>
            )}
            {clearable.length > 0 ? (
              <fieldset className="flex flex-col gap-2">
                <legend className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                  Safe to clear
                </legend>
                {clearable.map((c) => (
                  <ClearableRow key={c.kind} c={c} checked={!unticked.has(c.kind)} onToggle={() => toggle(c.kind)} />
                ))}
              </fieldset>
            ) : (
              clearing.length === 0 && (
                <p className="text-pretty text-sm text-muted-foreground">
                  Nothing here can be cleared safely right now: the caches Reliant clears only drop entries no build has
                  used for 2 hours, and these are all in recent use.
                </p>
              )
            )}
            {leftAlone.length > 0 && (
              <div className="flex flex-col gap-2">
                <h4 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                  Not cleared from here
                </h4>
                <ul className="flex max-h-48 flex-col gap-2 overflow-y-auto" aria-label="Not cleared from here">
                  {leftAlone.map((c) => (
                    <li key={c.kind}>
                      <CardInset padding="sm">
                        <ConsumerLine c={c} />
                        <p className="mt-0.5 text-xs text-muted-foreground">{notClearedNote(c)}</p>
                      </CardInset>
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </>
        )}

        <div className="flex items-center justify-end gap-2">
          {!online && <span className="mr-auto text-xs text-muted-foreground">{machineLabel} is offline.</span>}
          <Button variant="outline" size="sm" onClick={onClose} disabled={clear.isPending}>
            Close
          </Button>
          {clearable.length > 0 && (
            <Button size="sm" onClick={confirm} disabled={!online || clear.isPending || chosen.length === 0}>
              {clear.isPending
                ? "Starting…"
                : `Clear ${chosen.length === 1 ? "1 cache" : `${chosen.length} caches`}${
                    totalReclaimable(chosen) > 0n ? ` (about ${formatBytes(totalReclaimable(chosen))})` : ""
                  }`}
            </Button>
          )}
        </div>
      </div>
    </Modal>
  );
}

function ConsumerLine({ c }: { c: DiskConsumer }) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <span className="min-w-0 truncate text-sm font-medium text-foreground" title={c.path || undefined}>
        {c.label}
      </span>
      <span className="shrink-0 text-xs tabular-nums text-muted-foreground">
        {c.approximate ? "at least " : ""}
        {formatBytes(c.bytes)}
        {c.reclaimableBytes > 0n && c.reclaimableBytes < c.bytes ? ` · ${formatBytes(c.reclaimableBytes)} unused` : ""}
      </span>
    </div>
  );
}

function ClearableRow({ c, checked, onToggle }: { c: DiskConsumer; checked: boolean; onToggle: () => void }) {
  return (
    <label className="flex cursor-pointer items-start gap-2">
      <input type="checkbox" checked={checked} onChange={onToggle} className="mt-1 h-3.5 w-3.5 rounded border-border" />
      <CardInset padding="sm" className="min-w-0 flex-1">
        <div className="flex items-baseline justify-between gap-3">
          <span className="min-w-0 truncate text-sm font-medium text-foreground" title={c.path || undefined}>
            {c.label}
          </span>
          <span className="shrink-0 text-xs tabular-nums text-muted-foreground">
            frees about {formatBytes(c.reclaimableBytes)} of {formatBytes(c.bytes)}
          </span>
        </div>
        <p className="mt-0.5 text-xs text-muted-foreground">{clearNote(c)}</p>
      </CardInset>
    </label>
  );
}
