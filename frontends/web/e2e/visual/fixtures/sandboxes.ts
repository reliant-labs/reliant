/**
 * The platform's Sandboxes dashboard (control-plane
 * deploy/observability/dashboards/sandboxes.json, copied verbatim into
 * sandboxes.dashboard.json: re-copy it when that file changes), drawn by the
 * web app's own DashboardView against QueryPanel answers shaped like what each
 * panel's SQL returns through control-plane's panelfold: a first column that
 * is a time makes one series per (numeric column, group of the text columns);
 * anything else is table rows, the numeric columns their values and the text
 * columns their group; one row of figures is the panel's numbers.
 *
 * The fleet: five managed sandboxes (workspace pods `ws-<id>` on two GKE
 * nodes) and two self-hosted machines, `cicd` (a current daemon, every key)
 * and `ovh-build-1` (a daemon from before MachineStats: identity, disk and
 * processes, no load or memory), so the panels show the gaps an older daemon
 * leaves. Opened by URL under shopfront's release dashboards; never listed.
 */
import type { Fixture, Fixtures } from '../rpc'
import { DAY, FIXED_NOW, HOUR, iso } from './base'
import spec from './sandboxes.dashboard.json' with { type: 'json' }

const SUMMARY = {
  ref: { source: 'DASHBOARD_SOURCE_REPO', projectId: 'shopfront', key: 'sandboxes' },
  source: 'DASHBOARD_SOURCE_REPO',
  title: spec.title,
  description: spec.description,
  readOnly: true,
  starred: false,
  panelCount: spec.panels.length,
  previewPanels: [],
  tags: [], group: '', category: '',
  release: { releaseVersion: '20261118.121200-a1b2c3d4e5f6', shippedAt: iso(3 * HOUR), filePath: 'deploy/observability/dashboards/sandboxes.json', envIds: [] },
}

const PODS = ['ws-2aab1465', 'ws-e164d47d', 'ws-1e76f570', 'ws-929888f7', 'ws-b1b21231']
const NODES = ['gke-prod-daemon-v2-pool-a-7c1f', 'gke-prod-daemon-v2-pool-a-9d42']
const MACHINES = ['cicd', 'ovh-build-1']

/** A deterministic wave in [lo, hi], one cycle about every `period` points, shifted by `phase`. */
const wave = (i: number, lo: number, hi: number, phase = 0, period = 24) =>
  Math.round((lo + ((hi - lo) * (1 + Math.sin(((i + phase) / period) * 2 * Math.PI))) / 2) * 1000) / 1000

interface Window {
  times: string[]
  days: string[]
}

/** The buckets of the asked window (the request's step, else hourly), ending at the frozen clock. */
function windowOf(req: Record<string, unknown>): Window {
  const scope = (req.scope ?? {}) as { window?: { start?: string; end?: string } }
  const end = scope.window?.end ? Date.parse(scope.window.end) : FIXED_NOW
  const start = scope.window?.start ? Date.parse(scope.window.start) : end - 7 * DAY
  const asked = Number(req.stepSeconds ?? 0) * 1000
  const step = Math.max(asked || HOUR, (end - start) / 240)
  const times: string[] = []
  for (let t = Math.ceil(start / step) * step; t < end; t += step) times.push(new Date(t).toISOString())
  const days: string[] = []
  for (let d = Math.floor(start / DAY) * DAY; d < end; d += DAY) days.push(new Date(d).toISOString())
  return { times, days }
}

type Point = { time: string; value: number; null?: boolean }
type Series = { label: string; group: Record<string, string>; value: number; points: Point[] }

const series = (label: string, group: Record<string, string>, times: string[], value: (i: number) => number | null): Series => ({
  label,
  group,
  value: 0,
  points: times.map((time, i) => {
    const v = value(i)
    return v === null ? { time, value: 0, null: true } : { time, value: v }
  }),
})

const row = (group: Record<string, string>, values: (number | null)[]) => ({
  group,
  values: values.map((v) => v ?? 0),
  missingValueIndexes: values.flatMap((v, i) => (v === null ? [i] : [])),
})

/** One panel's answer: its rows or series, in the shape control-plane's sql fold returns. */
function answer(id: string, w: Window): { columns: string[]; rows?: ReturnType<typeof row>[]; series?: Series[]; daily?: boolean } | undefined {
  const T = w.times
  const perPod = (label: string, f: (i: number, p: number) => number | null) => PODS.map((pod, p) => series(label, { pod }, T, (i) => f(i, p)))
  const perMachine = (key: string, label: string, names: string[], f: (i: number, m: number) => number | null) => names.map((name, m) => series(label, { [key]: name }, T, (i) => f(i, m)))
  const daily = (cols: string[], f: (d: number, c: number) => number) => ({ columns: cols, series: cols.map((c, k) => series(c, {}, w.days, (d) => f(d, k))), daily: true })
  switch (id) {
    case 'fleet-now':
      return { columns: ['sandboxes', 'managed_awake', 'self_hosted_reporting', 'users_with_a_managed_sandbox', 'managed_cpu_cores_in_use', 'managed_memory_gib_in_use'], rows: [row({}, [7, 5, 2, 4, 6.42, 22.8])] }
    case 'health-now':
      return { columns: ['sandboxes_sampled', 'over_1_5_load_per_cpu', 'worst_load_per_cpu', 'min_disk_free', 'heavy_waiting', 'forge_installs'], rows: [row({}, [5, 1, 1.82, 0.124, 2, 3])] }
    case 'awake-over-time':
      return { columns: ['awake_sandboxes'], series: [series('awake_sandboxes', {}, T, (i) => Math.round(wave(i, 2, 6, 6)))] }
    case 'fleet-cpu-memory':
      return { columns: ['cpu_cores', 'memory_gib'], series: [series('cpu_cores', {}, T, (i) => wave(i, 1.2, 7.4, 6)), series('memory_gib', {}, T, (i) => wave(i, 9, 24, 4))] }
    case 'cpu-distribution':
      return { columns: ['sandboxes'], rows: [['1 · under 0.5', 2], ['2 · 0.5 to 1', 1], ['3 · 1 to 2', 1], ['4 · 2 to 4', 1]].map(([band, n]) => row({ cpu_cores_band: band as string }, [n as number])) }
    case 'memory-distribution':
      return { columns: ['sandboxes'], rows: [['2 · 1 to 2', 1], ['3 · 2 to 4', 2], ['4 · 4 to 8', 1], ['5 · 8 to 16', 1]].map(([band, n]) => row({ memory_gib_band: band as string }, [n as number])) }
    case 'cpu-per-box':
      return { columns: ['cpu_cores'], series: perPod('cpu_cores', (i, p) => wave(i, 0.05, 0.4 + p * 0.6, p * 5)) }
    case 'memory-per-box':
      return { columns: ['memory'], series: perPod('memory', (i, p) => Math.round(wave(i, 0.8, 2 + p * 2.4, p * 3) * 1073741824)) }
    case 'cpu-vs-request':
      return { columns: ['of_request'], series: perPod('of_request', (i, p) => wave(i, 0.05, 0.5 + p * 0.25, p * 4)) }
    case 'cpu-vs-limit':
      return { columns: ['of_limit'], series: perPod('of_limit', (i, p) => wave(i, 0.02, 0.2 + p * 0.15, p * 4)) }
    case 'memory-vs-limit':
      return { columns: ['of_limit'], series: perPod('of_limit', (i, p) => wave(i, 0.1, 0.35 + p * 0.12, p * 3)) }
    case 'daily-load':
      return daily(['p50_load_per_cpu', 'p95_load_per_cpu', 'share_over_1_5'], (d, c) => [wave(d, 0.3, 0.6, 1, 7), wave(d, 0.9, 1.9, 1, 7), wave(d, 0.01, 0.12, 1, 7)][c]!)
    case 'daily-pressure-slots':
      return daily(['cpu_psi', 'memory_psi', 'io_psi', 'mean_slot_wait_s', 'p95_slot_wait_s'], (d, c) => [wave(d, 2, 9, 2, 7), wave(d, 0, 3, 3, 7), wave(d, 1, 6, 4, 7), Math.round(wave(d, 4, 40, 2, 7)), Math.round(wave(d, 30, 210, 2, 7))][c]!)
    case 'daily-timeouts-disk':
      return daily(['tool_timeouts', 'peak_forge_installs', 'min_disk_free'], (d, c) => [Math.round(wave(d, 0, 6, 5, 7)), Math.round(wave(d, 1, 4, 2, 7)), wave(d, 0.12, 0.4, 3, 7)][c]!)
    case 'load-per-cpu':
      return { columns: ['load_per_cpu'], series: perMachine('machine', 'load_per_cpu', PODS, (i, m) => wave(i, 0.1, 0.5 + m * 0.35, m * 5)) }
    case 'pressure':
      return { columns: ['cpu_some', 'memory_some', 'memory_full', 'io_some', 'io_full'], series: ['cpu_some', 'memory_some', 'memory_full', 'io_some', 'io_full'].map((c, k) => series(c, {}, T, (i) => wave(i, 0, [14, 6, 2, 9, 3][k]!, k * 4))) }
    case 'heavy-slots':
      return { columns: ['held', 'waiting', 'slots'], series: [series('held', {}, T, (i) => Math.round(wave(i, 0, 6, 6))), series('waiting', {}, T, (i) => Math.max(0, Math.round(wave(i, -3, 3, 6)))), series('slots', {}, T, () => 8)] }
    case 'heavy-wait':
      return { columns: ['oldest_wait'], series: perMachine('machine', 'oldest_wait', PODS.slice(0, 3), (i, m) => Math.max(0, Math.round(wave(i, -60, 120 + m * 60, m * 7)))) }
    case 'forge-installs':
      return { columns: ['forge_installs'], series: perMachine('machine', 'forge_installs', PODS.slice(0, 3), (i, m) => Math.max(0, Math.round(wave(i, -1, 3, m * 6)))) }
    case 'tool-timeouts':
      return { columns: ['timeouts'], series: perMachine('machine', 'timeouts', PODS.slice(0, 2), (i, m) => ((i + m * 11) % 17 === 0 ? 1 + m : null)) }
    case 'disk-free':
      return { columns: ['free'], series: PODS.slice(0, 3).map((pod, p) => series('free', { volume: `${pod} · /home/workspace` }, T, (i) => Math.round((0.62 - p * 0.18 - i * 0.0008) * 1000) / 1000)) }
    case 'restart-summary':
      return { columns: ['restarts', 'oom_kills', 'sandboxes_restarted'], rows: [row({}, [3, 1, 2])] }
    case 'restarts-by-reason':
      return { columns: ['restarts'], series: [series('restarts', { exit_reason: 'Error' }, T, (i) => (i % 53 === 7 ? 1 : null)), series('restarts', { exit_reason: 'OOMKilled' }, T, (i) => (i === T.length - 30 ? 1 : null))] }
    case 'oom-kills':
      return { columns: ['oom_kills'], series: [series('oom_kills', { pod: PODS[3]! }, T, (i) => (i === T.length - 30 ? 1 : null))] }
    case 'pod-recreations':
      return { columns: ['recreated_pods'], series: [series('recreated_pods', {}, T, (i) => (i % 24 === 9 ? 2 : i % 24 === 14 ? 1 : null))] }
    case 'pods-not-running':
      return { columns: ['pods'], series: [series('pods', { phase: 'Pending' }, T, (i) => (i % 24 === 9 ? 1 : null))] }
    case 'boxes-by-node':
      return { columns: ['sandboxes', 'cpu_cores', 'memory_gib'], rows: [row({ node: NODES[0]! }, [3, 4.18, 14.2]), row({ node: NODES[1]! }, [2, 2.24, 8.6])] }
    case 'noisiest-boxes':
      return {
        columns: ['cpu_avg_cores', 'cpu_peak_cores', 'memory_peak_gib', 'restarts_total'],
        rows: PODS.map((pod, p) =>
          row({ workspace_id: pod.slice(3), user_id: `u-${(4096 + p * 977).toString(16)}`, owner_org_id: p % 2 ? 'personal' : '55949942-3ee9-4cfc-bb7c-817bdba75186', node: NODES[p % 2]! }, [
            Math.round((2.6 - p * 0.5) * 100) / 100,
            Math.round((3.9 - p * 0.6) * 100) / 100,
            Math.round((11.8 - p * 2.1) * 10) / 10,
            p === 3 ? 2 : p === 0 ? 1 : 0,
          ])
        ),
      }
    case 'platform-machines-latest':
      return {
        columns: ['minutes_silent', 'cpus', 'load5', 'load5_per_cpu', 'mem_available_pct', 'processes', 'process_limit', 'fullest_used_pct'],
        rows: [
          row({ machine: 'cicd', last_seen: '2026-11-18 15:29:41', fullest_volume: '/home', fullest_paths: '/home/workspace/.cache/go-build' }, [0, 32, 11.4, 0.36, 61.2, 1840, 65536, 71.5]),
          // A daemon from before MachineStats: no load or memory keys.
          row({ machine: 'ovh-build-1', last_seen: '2026-11-18 15:28:57', fullest_volume: '/', fullest_paths: '/var/lib/docker' }, [1, 16, null, null, null, 912, 32768, 48.3]),
        ],
      }
    case 'platform-disk-used':
      return {
        columns: ['used'],
        series: [
          ['cicd · /', 0.42],
          ['cicd · /home', 0.66],
          ['ovh-build-1 · /', 0.45],
        ].map(([volume, base], v) => series('used', { volume: volume as string }, T, (i) => Math.round(((base as number) + wave(i, 0, 0.06, v * 9)) * 1000) / 1000)),
      }
    case 'platform-load5-per-cpu':
      // Only cicd's daemon reports load.
      return { columns: ['load5_per_cpu'], series: perMachine('machine', 'load5_per_cpu', MACHINES.slice(0, 1), (i) => wave(i, 0.08, 0.72, 5)) }
    case 'platform-mem-available':
      return { columns: ['mem_available'], series: perMachine('machine', 'mem_available', MACHINES.slice(0, 1), (i) => wave(i, 0.38, 0.81, 2)) }
    default:
      return undefined
  }
}

const PANELS = new Map(spec.panels.map((p) => [p.id, p as { id: string; sql?: string; unit?: string }]))

/** A sandboxes panel's answer, or undefined for any other panel (matched by id and statement). */
export function sandboxesQueryPanel(req: Record<string, unknown>): Record<string, unknown> | undefined {
  const panel = (req.panel ?? {}) as { id?: string; sql?: string }
  const declared = PANELS.get(String(panel.id ?? ''))
  if (!declared || declared.sql !== panel.sql) return undefined
  const a = answer(declared.id, windowOf(req))
  if (!a) return undefined
  return {
    panelId: declared.id,
    kind: 'sql',
    unit: declared.unit ?? '',
    stepSeconds: Number(req.stepSeconds ?? 3600),
    columns: a.columns,
    rows: a.rows ?? [],
    series: a.series ?? [],
    ...(a.daily ? { bucketSeconds: 86400, bucketTimezone: 'UTC' } : a.series ? { bucketSeconds: Number(req.stepSeconds ?? 3600) } : {}),
  }
}

/** The table with the Sandboxes dashboard laid over it: its GetDashboard and QueryPanel answers first. */
export function withSandboxesFixtures(base: Fixtures): Fixtures {
  const previousQuery = base['controlplane.v1.ObservabilityService/QueryPanel'] as Fixture | undefined
  const previousGet = base['controlplane.v1.DashboardService/GetDashboard'] as Fixture | undefined
  const call = (fixture: Fixture | undefined, req: Record<string, unknown>) => (typeof fixture === 'function' ? fixture(req) : fixture ?? {})
  return {
    ...base,
    'controlplane.v1.ObservabilityService/QueryPanel': (req) => sandboxesQueryPanel(req) ?? call(previousQuery, req),
    'controlplane.v1.DashboardService/GetDashboard': (req) => {
      const key = String(((req.ref ?? {}) as { key?: string }).key ?? '')
      if (key !== SUMMARY.ref.key) return call(previousGet, req)
      return { summary: SUMMARY, spec, version: 1, canWrite: false }
    },
  }
}
