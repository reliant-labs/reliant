// Copyright (c) 2025 Reliant Labs

/**
 * The machine detail view's Disk card: every volume the machine's daemon
 * reports (internal/diskwatch, DaemonInfo.disk), how full each is, what it
 * holds, and — for a volume under pressure — what is using it, with "Free up
 * space".
 *
 * Every volume, not one "disk": a cloud machine's home volume can sit at 21%
 * while its build-cache & docker volume is at 100%, and a single number for
 * "the disk" is how the product ended up reporting the wrong one.
 *
 * Where the machine's plan lets its disk grow, the card also offers "Grow
 * disk", in its header and beside "Free up space" on a volume under pressure.
 */

import { useState } from "react";
import { HardDrive } from "lucide-react";

import { DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import type { DiskVolume } from "@/gen/reliant/v1/tools_daemon_pb";
import { formatBytes } from "@/lib/formatBytes";
import { consumerGroups, diskNotice, usedBytes, usedFraction, volumesFullestFirst } from "@/lib/machineDisk";
import { Progress } from "@/components/ui/Progress";
import { FreeDiskSpaceDialog } from "@/components/MachineDisk/FreeDiskSpaceDialog";
import { Badge, Button, Card, CardContent, CardHeader, CardTitle } from "./ui";

function reportedAgo(ms: bigint): string {
  const at = Number(ms);
  if (!at) return "";
  const mins = Math.max(0, Math.round((Date.now() - at) / 60_000));
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins} min ago`;
  const hours = Math.round(mins / 60);
  return hours < 48 ? `${hours} h ago` : `${Math.round(hours / 24)} days ago`;
}

export function MachineDiskCard({
  daemon,
  machineLabel,
  onGrowDisk,
  growDisabled = false,
}: {
  daemon: DaemonInfo;
  machineLabel: string;
  /** Opens Grow disk. Absent where the machine's disk cannot grow. */
  onGrowDisk?: () => void;
  /** True while another lifecycle action runs on the machine. */
  growDisabled?: boolean;
}) {
  const [freeing, setFreeing] = useState<DiskVolume | null>(null);
  const volumes = volumesFullestFirst(daemon.disk);
  if (volumes.length === 0) return null;
  const online = daemon.status === DaemonStatus.ACTIVE;
  const ago = reportedAgo(daemon.disk?.reportedAtMs ?? 0n);

  return (
    <Card className="mt-6" data-testid="machine-disk-card">
      <CardHeader className="flex items-center justify-between gap-3">
        <CardTitle className="inline-flex items-center gap-2">
          <HardDrive className="h-4 w-4 text-muted-foreground" /> Disk
          {ago && <span className="text-xs font-normal text-muted-foreground">· reported {ago}</span>}
        </CardTitle>
        {onGrowDisk && (
          <Button size="sm" variant="outline" disabled={growDisabled} onClick={onGrowDisk}>
            Grow disk
          </Button>
        )}
      </CardHeader>
      <CardContent>
        <ul className="flex flex-col divide-y divide-border">
          {volumes.map((v) => (
            <VolumeRow
              key={v.label + v.paths.join()}
              v={v}
              onFree={() => setFreeing(v)}
              canFree={v.pressure}
              onGrow={v.pressure ? onGrowDisk : undefined}
              growDisabled={growDisabled}
            />
          ))}
        </ul>
      </CardContent>
      {freeing && (
        <FreeDiskSpaceDialog
          isOpen
          onClose={() => setFreeing(null)}
          daemonId={daemon.daemonId}
          machineLabel={machineLabel}
          volume={volumes.find((v) => v.label === freeing.label) ?? freeing}
          online={online}
        />
      )}
    </Card>
  );
}

function VolumeRow({
  v,
  onFree,
  canFree,
  onGrow,
  growDisabled,
}: {
  v: DiskVolume;
  onFree: () => void;
  canFree: boolean;
  /** Offered beside "Free up space" on a volume under pressure. */
  onGrow?: () => void;
  growDisabled: boolean;
}) {
  const pct = Math.round(usedFraction(v) * 100);
  const notice = v.pressure ? diskNotice(v, Date.now()) : null;
  const groups = consumerGroups(v);
  return (
    <li className="flex flex-col gap-1.5 py-3 first:pt-0 last:pb-0" data-testid="machine-disk-volume">
      <div className="flex items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2">
          <span className="text-sm font-medium capitalize text-foreground">{v.label}</span>
          {notice && <Badge label={notice.tone === "error" ? "Full — commands failing" : "Low on space"} variant={notice.tone === "error" ? "error" : "warning"} />}
        </div>
        <span className="shrink-0 text-sm tabular-nums text-muted-foreground">
          {formatBytes(usedBytes(v))} of {formatBytes(v.totalBytes)} used ·{" "}
          {formatBytes(v.freeBytes)} free
        </span>
      </div>
      <Progress value={pct} size="sm" variant={v.pressure ? "warning" : "primary"} />
      <p className="truncate font-mono text-xs text-muted-foreground" title={v.paths.join("\n")}>
        {v.paths.slice(0, 4).join(", ")}
        {v.paths.length > 4 ? `, +${v.paths.length - 4} more` : ""}
      </p>
      {groups.length > 0 && (
        <p className="text-xs text-muted-foreground">
          Using it: {groups.map((g) => `${g.label} ${formatBytes(g.bytes)}`).join(" · ")}
        </p>
      )}
      {(canFree || onGrow) && (
        <div className="flex flex-wrap gap-2">
          {canFree && (
            <Button size="sm" variant="outline" onClick={onFree}>
              Free up space
            </Button>
          )}
          {onGrow && (
            <Button size="sm" variant="outline" disabled={growDisabled} onClick={onGrow}>
              Grow disk
            </Button>
          )}
        </div>
      )}
    </li>
  );
}
