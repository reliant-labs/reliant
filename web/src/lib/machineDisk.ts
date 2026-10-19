// Copyright (c) 2025 Reliant Labs

/**
 * A machine's disk, as its daemon reports it on the heartbeat
 * (internal/diskwatch → DaemonInfo.disk / InboxStorage.volumes).
 *
 * A machine has several volumes, and on a cloud machine the one that fills is
 * NOT the home directory: the Go/npm caches, docker's images and volumes and
 * the persisted system packages share one small volume ("build caches &
 * docker"). Everything here therefore talks about a specific volume, by the
 * label the daemon gave it, and never about "the disk".
 */

import { DiskGuardState, type DiskConsumer, type DiskReport, type DiskVolume } from "@/gen/reliant/v1/tools_daemon_pb";
import { formatBytes } from "@/lib/formatBytes";

/** A command failing with "no space left on device" this recently turns the
 *  notice from a warning into an error: work is failing now, not soon. */
export const RECENT_NO_SPACE_MS = 10 * 60 * 1000;

const CATEGORY_LABEL: Record<string, string> = {
  caches: "Build caches",
  docker: "Docker",
  system: "System packages",
  workspaces: "Workspaces",
  other: "Other",
};

export function usedBytes(v: DiskVolume): bigint {
  return v.totalBytes > v.freeBytes ? v.totalBytes - v.freeBytes : 0n;
}

/** 0..1, the share of the volume in use. */
export function usedFraction(v: DiskVolume): number {
  if (v.totalBytes <= 0n) return 0;
  return Number(usedBytes(v)) / Number(v.totalBytes);
}

/** Anything carrying volumes: a DaemonInfo's DiskReport, an InboxStorage. */
type HasVolumes = Pick<DiskReport, "volumes">;

/** The report's volumes, fullest first. */
export function volumesFullestFirst(report: HasVolumes | undefined): DiskVolume[] {
  return [...(report?.volumes ?? [])].sort((a, b) => usedFraction(b) - usedFraction(a));
}

/** The machine refuses new heavy commands (builds, test suites, codegen)
 *  because this volume is nearly full (the daemon's disk guard). */
export function heavyRefused(v: DiskVolume): boolean {
  return v.guard === DiskGuardState.REFUSING_HEAVY;
}

/** The volume to warn about: the fullest one under pressure or refusing
 *  heavy commands, if any. */
export function pressuredVolume(report: HasVolumes | undefined): DiskVolume | undefined {
  return volumesFullestFirst(report).find((v) => v.pressure || heavyRefused(v));
}

/** "Freed 3.2 GB automatically (Go build cache 3.1 GB)", or "" when the
 *  daemon's disk guard has not reclaimed anything for this volume. */
export function reclaimSummary(v: DiskVolume): string {
  if (v.reclaimedAtMs <= 0n || v.reclaimedBytes <= 0n) return "";
  const what = v.reclaimed.length > 0 ? ` (${v.reclaimed.join(", ")})` : "";
  return `Freed ${formatBytes(v.reclaimedBytes)} automatically${what}`;
}

export interface ConsumerGroup {
  category: string;
  label: string;
  bytes: bigint;
}

/** What is using a volume, summed by category, biggest first. */
export function consumerGroups(v: DiskVolume): ConsumerGroup[] {
  const sums = new Map<string, bigint>();
  for (const c of v.consumers) {
    sums.set(c.category, (sums.get(c.category) ?? 0n) + c.bytes);
  }
  return [...sums.entries()]
    .map(([category, bytes]) => ({ category, label: CATEGORY_LABEL[category] ?? category, bytes }))
    .sort((a, b) => (b.bytes > a.bytes ? 1 : b.bytes < a.bytes ? -1 : 0));
}

/** "Docker 18 GB · Build caches 15 GB" — empty until the machine measured it. */
export function consumerSummary(v: DiskVolume, max = 3): string {
  return consumerGroups(v)
    .slice(0, max)
    .map((g) => `${g.label} ${formatBytes(g.bytes)}`)
    .join(" · ");
}

/** "build caches & docker: 38 of 40 GB used". */
export function volumeUsage(v: DiskVolume): string {
  return `${v.label}: ${formatBytes(usedBytes(v))} of ${formatBytes(v.totalBytes)} used`;
}

/** A command on this machine failed for want of space on this volume, recently. */
export function failingRecently(v: DiskVolume, now: number): boolean {
  const at = Number(v.lastNoSpaceAtMs);
  return at > 0 && now - at < RECENT_NO_SPACE_MS;
}

export interface DiskNotice {
  tone: "warning" | "error";
  title: string;
  /** "build caches & docker: 38 of 40 GB used" */
  usage: string;
  /** "Docker 18 GB · Build caches 15 GB", or "" before it was measured. */
  using: string;
  /** Why new builds are not starting, or "" while they are. */
  paused: string;
  /** What the machine already freed by itself (reclaimSummary), or "". */
  reclaimed: string;
}

/** What a refused heavy command is told, in the user's words. */
export const HEAVY_PAUSED =
  "New builds, test runs and codegen are paused until space is freed, so they do not fail half-way; everything else keeps running.";

/** What to tell the user about a volume under pressure. */
export function diskNotice(v: DiskVolume, now: number): DiskNotice {
  const failing = failingRecently(v, now);
  const paused = heavyRefused(v);
  const full = failing || v.freeBytes * 100n < v.totalBytes; // under 1% left
  return {
    tone: failing || paused ? "error" : "warning",
    title: failing
      ? "Commands are failing: your machine's disk is full"
      : paused
        ? "Builds are paused: your machine's disk is almost full"
        : full
          ? "Your machine's disk is full"
          : "Your machine's disk is almost full",
    usage: volumeUsage(v),
    using: consumerSummary(v),
    paused: paused ? HEAVY_PAUSED : "",
    reclaimed: reclaimSummary(v),
  };
}

/** Consumers a clean-up can act on right now (not already being cleared). */
export function clearableConsumers(v: DiskVolume): DiskConsumer[] {
  return v.consumers.filter((c) => c.clearable && !c.clearing);
}

export function clearingConsumers(v: DiskVolume): DiskConsumer[] {
  return v.consumers.filter((c) => c.clearing);
}

export function totalReclaimable(consumers: DiskConsumer[]): bigint {
  return consumers.reduce((sum, c) => sum + c.reclaimableBytes, 0n);
}

/** How a clearable consumer is cleared, in the confirmation dialog's words. */
export function clearNote(c: DiskConsumer): string {
  switch (c.kind) {
    case "go_build_cache":
    case "golangci_lint_cache":
      return "Removes entries no build has used for 2 hours. Running builds keep what they use.";
    case "docker_build_cache":
      return "Removes docker build cache unused for 2 hours. Images, containers and volumes are not touched.";
    default:
      return "";
  }
}

/** Big consumers that are never cleared from here, and why — so the user
 *  knows what is left to decide and does not assume we missed it. */
export function notClearedNote(c: DiskConsumer): string | null {
  switch (c.kind) {
    case "docker_images":
      return "Re-pullable, but locally built images are lost. Remove the ones you don't need yourself.";
    case "docker_volumes":
      return "Volumes can hold databases and other data. Remove only ones you know are unused.";
    case "go_mod_cache":
      return "Clearing it forces every build to re-download its modules.";
    case "npm_cache":
    case "pnpm_store":
    case "yarn_cache":
      return "Clearing it while an install runs can break that install.";
    default:
      return null;
  }
}
