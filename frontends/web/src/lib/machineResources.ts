// Copyright (c) 2026 Reliant Labs

/**
 * The machine detail view's resource rows: the machine's and the daemon
 * process's CPU, memory and open files, from the reading its heartbeat last
 * carried (DaemonInfo.resources). A reading the daemon did not send is left
 * out, never shown as zero: a daemon that predates the report, or a platform
 * it cannot read yet, has fewer rows or none.
 */

import type { DaemonResourceStats } from "@/gen/reliant/v1/tools_daemon_pb";
import { formatBytes } from "@/lib/formatBytes";

export interface ResourceRow {
  label: string;
  value: string;
}

export interface ResourceSection {
  title: "Machine" | "Reliant daemon";
  rows: ResourceRow[];
}

const count = (n: number | bigint) => Number(n).toLocaleString("en-US");
const percent = (ratio: number) => `${Math.round(ratio * 100)}%`;

function machineRows(m: DaemonResourceStats["machine"]): ResourceRow[] {
  if (!m) return [];
  const rows: ResourceRow[] = [];
  const cpu: string[] = [];
  if (m.cpuBusyRatio !== undefined) cpu.push(`${percent(m.cpuBusyRatio)} busy`);
  if (m.load1 !== undefined) {
    cpu.push(m.cpus > 0 ? `load ${m.load1.toFixed(1)} on ${count(m.cpus)} CPUs` : `load ${m.load1.toFixed(1)}`);
  }
  if (cpu.length > 0) rows.push({ label: "CPU", value: cpu.join(" · ") });
  if (m.memTotalBytes > 0n) {
    const used = m.memTotalBytes > m.memAvailableBytes ? m.memTotalBytes - m.memAvailableBytes : 0n;
    rows.push({ label: "Memory", value: `${formatBytes(used)} of ${formatBytes(m.memTotalBytes)} in use` });
  }
  if (m.swapTotalBytes !== undefined && m.swapTotalBytes > 0n && m.swapFreeBytes !== undefined) {
    const used = m.swapTotalBytes > m.swapFreeBytes ? m.swapTotalBytes - m.swapFreeBytes : 0n;
    rows.push({ label: "Swap", value: `${formatBytes(used)} of ${formatBytes(m.swapTotalBytes)} in use` });
  }
  if (m.openFiles !== undefined) {
    rows.push({
      label: "Open files",
      value: m.openFilesLimit ? `${count(m.openFiles)} of ${count(m.openFilesLimit)}` : count(m.openFiles),
    });
  }
  return rows;
}

function processRows(p: DaemonResourceStats["process"]): ResourceRow[] {
  if (!p) return [];
  const rows: ResourceRow[] = [];
  if (p.cpuCores !== undefined) rows.push({ label: "CPU", value: `${p.cpuCores.toFixed(2)} cores` });
  if (p.rssBytes !== undefined) rows.push({ label: "Memory", value: formatBytes(p.rssBytes) });
  if (p.openFds !== undefined) {
    rows.push({
      label: "File descriptors",
      value: p.fdLimit ? `${count(p.openFds)} of ${count(p.fdLimit)}` : count(p.openFds),
    });
  }
  if (p.threads !== undefined) rows.push({ label: "Threads", value: count(p.threads) });
  return rows;
}

/** The sections to show, each with at least one row; empty when there is nothing. */
export function resourceSections(resources: DaemonResourceStats | undefined): ResourceSection[] {
  if (!resources) return [];
  const sections: ResourceSection[] = [
    { title: "Machine", rows: machineRows(resources.machine) },
    { title: "Reliant daemon", rows: processRows(resources.process) },
  ];
  return sections.filter((s) => s.rows.length > 0);
}
