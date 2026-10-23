// Copyright (c) 2025 Reliant Labs

/**
 * Grow disk: pick a larger size, see what it costs, and acknowledge that it
 * cannot be undone. Also DiskGrowNotice, the detail page's line about a grow
 * in progress, a failed one, or space waiting on a restart.
 *
 * What confirming does depends on the machine (control-plane
 * docs/design/workspace-disk-resize.md §5):
 *   - suspended: the disk grows now, and the machine uses it when it starts;
 *   - running: the disk grows now, but the running guest keeps the size it
 *     booted with. So the default is to restart it straight away, the same
 *     disruption Restart warns about, and the alternative is to grow now and
 *     use the space after the next restart.
 *
 * The policy (steps, limit, price, status) is ./diskGrow; this file only
 * renders it.
 */

import { useEffect, useId, useState } from "react";
import { Minus, Plus } from "lucide-react";

import Modal from "@/components/forge-ui/modal";
import { CardInset } from "@/components/forge-ui/card";
import { type DaemonPricingLike } from "@/components/Billing/daemonUsage";
import { cn } from "@/lib/utils";
import { Button } from "./ui";
import { DISK_CEILING_GIB, DISK_GROW_STEP_GIB, diskGrowPrice, diskGrowRange, type DiskGrowStatus } from "./diskGrow";
import type { RestartStage } from "./machineLifecycle";

/** Where a grow is: the RPC itself, then (with a restart) the restart's stages. */
export type GrowStage = "growing" | RestartStage;

const STAGE_COPY: Record<GrowStage, string> = {
  growing: "Growing the disk…",
  stopping: "Stopping the machine…",
  resizing: "Growing the disk…",
  starting: "Starting the machine…",
};

export interface GrowDiskModalProps {
  open: boolean;
  machineName: string;
  /** The disk's current size: DaemonDisk.requested_gib. */
  currentGib: number;
  provisionedGib: number | null;
  /** The plan's limit (eligibility max_daemon_storage_gib); 0 while loading. */
  maxGib: number;
  limitLoading: boolean;
  /** Whether the machine has a pod, so the grow only lands after a restart. */
  running: boolean;
  /** Whether a restart can be offered now (see lifecyclePlan). */
  restartable: boolean;
  pricing?: DaemonPricingLike;
  isPending: boolean;
  stage: GrowStage | null;
  error: string;
  /** The error was the plan's limit, so "See plans" is the way forward. */
  storageDenied: boolean;
  onClose: () => void;
  onConfirm: (targetGib: number, restart: boolean) => void;
  onSeePlans: () => void;
}

export function GrowDiskModal({
  open,
  machineName,
  currentGib,
  provisionedGib,
  maxGib,
  limitLoading,
  running,
  restartable,
  pricing,
  isPending,
  stage,
  error,
  storageDenied,
  onClose,
  onConfirm,
  onSeePlans,
}: GrowDiskModalProps) {
  const ids = useId();
  const range = diskGrowRange(currentGib, maxGib);
  const [target, setTarget] = useState<number | null>(null);
  const [restart, setRestart] = useState(true);
  const [acknowledged, setAcknowledged] = useState(false);

  // A fresh choice every time the modal opens. Carrying the last size, or
  // the last tick of the one-way warning, into a later grow would confirm
  // something nobody chose this time.
  useEffect(() => {
    if (!open) {
      setTarget(null);
      setRestart(true);
      setAcknowledged(false);
    }
  }, [open]);

  const chosen = range ? Math.min(Math.max(target ?? range.min, range.min), range.max) : null;
  const whole = (gib: number) => `${gib} GB`;
  const price = chosen !== null && provisionedGib !== null ? diskGrowPrice(pricing, { currentGib: provisionedGib, targetGib: chosen }) : null;
  const restartNow = running && restartable && restart;
  const atCeiling = maxGib >= DISK_CEILING_GIB;
  const step = (delta: number) => range && chosen !== null && setTarget(chosen + delta);

  return (
    <Modal open={open} onClose={() => !isPending && onClose()} title="Grow disk" size="md">
      <div className="flex flex-col gap-4" data-testid="grow-disk-dialog">
        <p className="text-sm text-muted-foreground">
          <span className="font-semibold text-foreground">{machineName}</span>&apos;s disk is {provisionedGib === null ? "not yet measured" : `${provisionedGib} GB`}. Requested size: {whole(currentGib)}.
        </p>

        {limitLoading ? (
          <p className="text-sm text-muted-foreground">Checking how large your plan lets this disk grow…</p>
        ) : range && chosen !== null ? (
          <div className="flex flex-col gap-2">
            <label htmlFor={`${ids}-size`} className="text-sm font-medium text-foreground">
              New size
            </label>
            <div className="flex items-center gap-3">
              <Button
                size="sm"
                variant="outline"
                aria-label={`${DISK_GROW_STEP_GIB} GB smaller`}
                disabled={isPending || chosen <= range.min}
                onClick={() => step(-DISK_GROW_STEP_GIB)}
              >
                <Minus className="h-4 w-4" />
              </Button>
              <input
                id={`${ids}-size`}
                type="range"
                min={range.min}
                max={range.max}
                step={DISK_GROW_STEP_GIB}
                value={chosen}
                disabled={isPending || range.min === range.max}
                onChange={(e) => setTarget(Number(e.target.value))}
                aria-valuetext={`${whole(chosen)}`}
                className="min-w-0 flex-1 accent-primary"
              />
              <Button
                size="sm"
                variant="outline"
                aria-label={`${DISK_GROW_STEP_GIB} GB larger`}
                disabled={isPending || chosen >= range.max}
                onClick={() => step(DISK_GROW_STEP_GIB)}
              >
                <Plus className="h-4 w-4" />
              </Button>
              <span className="w-20 shrink-0 text-right text-sm font-semibold tabular-nums text-foreground" data-testid="grow-disk-target">
                {whole(chosen)}
              </span>
            </div>
            <p className="text-xs text-muted-foreground">
              +{chosen - currentGib} GB · your plan allows up to {whole(range.max)}
            </p>
          </div>
        ) : (
          <p className="text-sm text-muted-foreground" data-testid="grow-disk-at-limit">
            This disk is already {whole(currentGib)}, the largest your plan allows.
            {!atCeiling && (
              <>
                {" "}Larger disks need a bigger compute plan.{" "}
                <button type="button" onClick={onSeePlans} className="font-medium text-primary hover:underline">
                  See plans
                </button>
              </>
            )}
          </p>
        )}

        {price && chosen !== null && (
          <CardInset className="text-sm" data-testid="grow-disk-price">
            <p className="font-medium text-foreground">Up to {price.added}</p>
            <p className="mt-1 text-xs text-muted-foreground">
              Only storage beyond your plan&apos;s included storage is billed.
            </p>
          </CardInset>
        )}

        {range && running && restartable && (
          <fieldset className="flex flex-col gap-2" disabled={isPending}>
            <legend className="sr-only">When to use the space</legend>
            <GrowChoice
              name={`${ids}-when`}
              checked={restart}
              onSelect={() => setRestart(true)}
              title="Grow and restart now"
              detail={`${machineName} restarts so it can use the space. Open sessions on it — running agents, terminals and dev servers — disconnect. Files on its disk are kept.`}
            />
            <GrowChoice
              name={`${ids}-when`}
              checked={!restart}
              onSelect={() => setRestart(false)}
              title="Grow now, use the space after the next restart"
              detail="Nothing is interrupted. The disk is billed at its new size from now on."
            />
          </fieldset>
        )}
        {range && running && !restartable && (
          <p className="text-sm text-muted-foreground">
            The disk grows now. {machineName} can use the space after its next restart.
          </p>
        )}
        {range && !running && (
          <p className="text-sm text-muted-foreground">
            The disk grows now. {machineName} uses the space when it next starts.
          </p>
        )}

        {range && (
          <CardInset className="text-sm" data-testid="grow-disk-warning">
            <p className="text-foreground">
              Disks can only grow. You won&apos;t be able to make this disk smaller later, and it&apos;s billed at
              its new size for as long as the machine exists. To go back to a smaller disk you&apos;d create a new
              machine and move your files.
            </p>
            <label className="mt-3 flex cursor-pointer items-start gap-2">
              <input
                type="checkbox"
                checked={acknowledged}
                disabled={isPending}
                onChange={(e) => setAcknowledged(e.target.checked)}
                className="mt-0.5 h-4 w-4 rounded border-border"
              />
              <span className="font-medium text-foreground">I understand this disk can&apos;t be made smaller</span>
            </label>
          </CardInset>
        )}

        {stage && (
          <p className="text-sm font-medium text-foreground" role="status" data-testid="grow-disk-progress">
            {STAGE_COPY[stage]}
          </p>
        )}

        {error && (
          <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive-ink">
            {error}
            {storageDenied && !atCeiling && (
              <>
                {" "}
                <button type="button" onClick={onSeePlans} className="font-medium text-primary hover:underline">
                  See plans
                </button>
              </>
            )}
          </div>
        )}

        <div className="flex justify-end gap-3">
          <Button variant="outline" disabled={isPending} onClick={onClose}>
            {range ? "Cancel" : "Close"}
          </Button>
          {range && chosen !== null && (
            <Button
              isLoading={isPending}
              disabled={!acknowledged}
              onClick={() => onConfirm(chosen, restartNow)}
            >
              {isPending ? "Growing…" : restartNow ? "Grow and restart" : "Grow disk"}
            </Button>
          )}
        </div>
      </div>
    </Modal>
  );
}

function GrowChoice({
  name,
  checked,
  onSelect,
  title,
  detail,
}: {
  name: string;
  checked: boolean;
  onSelect: () => void;
  title: string;
  detail: string;
}) {
  return (
    <label
      className={cn(
        "flex cursor-pointer items-start gap-2 rounded-lg border-2 p-3 transition-colors",
        checked ? "border-primary bg-primary/5" : "border-border bg-card hover:border-muted-foreground/40",
      )}
    >
      <input type="radio" name={name} checked={checked} onChange={onSelect} className="mt-1 h-4 w-4 accent-primary" />
      <span className="min-w-0">
        <span className="block text-sm font-semibold text-foreground">{title}</span>
        <span className="mt-0.5 block text-xs text-muted-foreground">{detail}</span>
      </span>
    </label>
  );
}

/**
 * The detail page's line about the disk: a grow in progress, a grow that
 * failed, or grown space the running machine can use only after a restart.
 */
export function DiskGrowNotice({
  status,
  machineName,
  onRestart,
  restartable,
}: {
  status: DiskGrowStatus;
  machineName: string;
  onRestart: () => void;
  restartable: boolean;
}) {
  if (status.kind === "failed") {
    return (
      <div
        className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive-ink"
        data-testid="disk-grow-notice"
      >
        Couldn&apos;t grow the disk to {status.targetGib} GB.{" "}
        {status.message ? `${status.message} ` : ""}Nothing extra is billed until it succeeds.
      </div>
    );
  }
  return (
    <div
      className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-border bg-background px-3 py-2 text-sm text-muted-foreground"
      role="status"
      data-testid="disk-grow-notice"
    >
      {status.kind === "expanding" ? (
        <span>Growing the disk to {status.targetGib} GB…</span>
      ) : (
        <>
          <span>
            Restart {machineName} to use the extra {status.extraGib} GB on its disk.
          </span>
          {restartable && (
            <Button size="sm" variant="outline" onClick={onRestart}>
              Restart
            </Button>
          )}
        </>
      )}
    </div>
  );
}
