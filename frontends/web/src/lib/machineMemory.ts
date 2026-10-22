// Copyright (c) 2025 Reliant Labs

/**
 * What a machine's memory did that the user should hear about.
 *
 * Three things can end work on a cloud machine for lack of memory, and each
 * reaches the app by a different road:
 *
 *  - A process killed at the machine's memory limit. Inside a kata VM the
 *    kernel kills one process and the machine stays up, so nothing restarts
 *    and the operator sees nothing. The daemon reads the kill from its cgroup
 *    and carries it on every heartbeat (`oom_kills`, `last_oom_kill_at_ms`).
 *  - The daemon's whole container OOM-killed (DaemonInfo.lastOomKilledAt, via
 *    the operator and the registry; OomKillBanner).
 *  - The machine's VM dying under it (control-plane Daemon.lastVmCrashAt): a
 *    restart of everything, which the kubelet reports only as a re-created
 *    sandbox.
 */

import type { Timestamp } from "@bufbuild/protobuf/wkt";

/** The process OOM kills a machine's heartbeat reported. */
export interface ProcessOomKills {
  /** Kills since the machine's daemon started. */
  count: number;
  /** When the daemon saw the newest one (unix ms). */
  lastAtMs: number;
  /** The machine's memory limit in bytes; 0 when unknown. */
  limitBytes: number;
}

/**
 * The OOM kills on a DAEMON_HEARTBEAT payload, or undefined when it carries
 * none (the gateway sends the keys only for a machine that has had a kill).
 */
export function heartbeatOomKills(data: Record<string, unknown> | null | undefined): ProcessOomKills | undefined {
  const count = Number(data?.oom_kills ?? 0);
  const lastAtMs = Number(data?.last_oom_kill_at_ms ?? 0);
  if (!Number.isFinite(count) || count <= 0 || !Number.isFinite(lastAtMs) || lastAtMs <= 0) return undefined;
  const limit = Number(data?.memory_limit_bytes ?? 0);
  return { count, lastAtMs, limitBytes: Number.isFinite(limit) && limit > 0 ? limit : 0 };
}

/** "64 GiB": memory limits are binary sizes, so they are shown as one. */
export function formatMemoryLimit(bytes: number): string {
  const gib = bytes / 2 ** 30;
  return gib >= 10 ? `${Math.round(gib)} GiB` : `${Number(gib.toFixed(1))} GiB`;
}

/** The sentence for a process killed at the limit. */
export function processOomKillNotice(k: ProcessOomKills): string {
  const limit = k.limitBytes > 0 ? `its ${formatMemoryLimit(k.limitBytes)} memory limit` : "its memory limit";
  const times = k.count === 1 ? "" : ` (${k.count} times since the machine started)`;
  return `A process on your machine was killed: the machine hit ${limit}${times}.`;
}

interface CrashFields {
  lastVmCrashAt?: Timestamp;
  vmCrashCount: number;
  lastOomKilledAt?: Timestamp;
  oomKillCount: number;
}

/**
 * One line for a machine's detail page: when it last restarted for lack of
 * memory or because its VM died, and how often. Undefined when it never has.
 */
export function machineCrashSummary(d: CrashFields | undefined, fmt: (ts?: Timestamp) => string): string | undefined {
  if (!d) return undefined;
  const parts: string[] = [];
  const times = (n: number) => (n === 1 ? "once" : `${n} times`);
  if (d.vmCrashCount > 0 && d.lastVmCrashAt) {
    parts.push(`VM crashed ${fmt(d.lastVmCrashAt)} (${times(d.vmCrashCount)})`);
  }
  if (d.oomKillCount > 0 && d.lastOomKilledAt) {
    parts.push(`Ran out of memory ${fmt(d.lastOomKilledAt)} (${times(d.oomKillCount)})`);
  }
  return parts.length > 0 ? parts.join("; ") : undefined;
}
