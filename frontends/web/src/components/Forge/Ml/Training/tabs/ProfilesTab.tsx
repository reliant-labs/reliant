// Copyright (c) 2026 Reliant Labs

/**
 * A run's Profiles tab (02-views §3.8): every capture of the scope — PyTorch
 * profiler (Kineto) windows, Nsight Systems, memory snapshots, the NCCL
 * flight recorder's hang dump, and the always-on CPU (eBPF) and GPU (CUPTI)
 * profiles. Selecting one (`?sel=`, the first by default) shows what we
 * computed from it: the HTA time breakdown (compute, comm, memcpy, idle),
 * overlap and launch-boundness, the top kernels, its notes, and its
 * flamegraph in speedscope with the heaviest functions beside it; a flight
 * recorder dump shows each rank's last collective with the rank that never
 * joined marked. Capturing a new window is a recorded action the demo does
 * not take.
 */

import { useMemo } from "react";
import { ExternalLink } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { MlProfileKind, MlSubjectKind, type MlCollectiveState, type MlKernel, type MlProfile } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { formatProfileValue, SpeedscopeFrame, topFunctions } from "../../../Observability/ProfilesView";
import { MlRead } from "../../MlParts";
import { EmptyNote, FactList, fmtBytes, fmtStep, fmtTime, Grid, Meter, MlTable, num, Panel, StackedBar, StatusDot, SubjectLink, tsMs, useMl, useMlSearch, type MlColumn } from "../../kit";
import { collectiveRows, flightVerdict, PROFILE_KIND_LABELS } from "../obs/obsModel";
import type { RunScope } from "../runScope";

const has = (v: number | undefined) => typeof v === "number" && Number.isFinite(v) && v > 0;

function ranksText(ranks: readonly number[]): string {
  if (ranks.length === 0) return "—";
  if (ranks.length > 4) return `all ${ranks.length}`;
  return ranks.join(", ");
}

function stepsText(p: MlProfile): string {
  const from = num(p.fromStep);
  const to = num(p.toStep);
  if (!from && !to) return "—";
  return from === to ? fmtStep(from) : `${fmtStep(from)}–${fmtStep(to)}`;
}

export function ProfilesTab({ scope }: { scope: RunScope }) {
  const [search, setSearch] = useMlSearch();
  const list = useMl("listMlProfiles", { subject: scope.subject });
  return (
    <div className="space-y-4" data-testid="ml-run-profiles">
      <MlRead query={list} what="profiles">
        {(data) => {
          const profiles = data.profiles;
          const selected = profiles.find((p) => p.id === search.sel) ?? profiles[0];
          const columns: MlColumn<MlProfile>[] = [
            { key: "kind", header: "Kind", render: (p) => <span className="text-ink">{PROFILE_KIND_LABELS[p.kind] ?? "Profile"}</span> },
            { key: "trigger", header: "Trigger", render: (p) => <span className="text-ink-muted">{p.trigger}</span> },
            { key: "attempt", header: "Attempt", render: (p) => (p.subject ? <SubjectLink subject={p.subject}>{p.subject.label}</SubjectLink> : "—") },
            { key: "steps", header: "Steps", numeric: true, render: stepsText, sort: (p) => num(p.fromStep) },
            { key: "ranks", header: "Ranks", numeric: true, render: (p) => ranksText(p.ranks) },
            { key: "size", header: "Size", numeric: true, render: (p) => fmtBytes(p.sizeBytes), sort: (p) => num(p.sizeBytes) },
            { key: "captured", header: "Captured", render: (p) => <span className="text-ink-muted">{fmtTime(tsMs(p.capturedAt))}</span>, sort: (p) => tsMs(p.capturedAt) ?? 0 },
            { key: "by", header: "By", render: (p) => <span className="truncate text-ink-muted">{p.capturedBy}</span> },
            {
              key: "open",
              header: "",
              render: (p) =>
                p.openUrl ? (
                  <a href={p.openUrl} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-primary-ink hover:underline">
                    {p.openLabel || "Open"} <ExternalLink className="h-3 w-3" aria-hidden="true" />
                  </a>
                ) : null,
            },
          ];
          return (
            <>
              <Panel
                title="Captures"
                note={`${profiles.length} for this scope · on demand: a declared window on N ranks; continuous: always on, ≤1% overhead`}
                flush
                testId="ml-profiles-list"
                actions={
                  <Button variant="outline" size="xs" disabled title="Demo shell: read-only. A capture declares a profiling window (steps N–M, ranks) on the live run.">
                    Capture profile
                  </Button>
                }
              >
                <MlTable
                  columns={columns}
                  rows={profiles}
                  rowKey={(p) => p.id}
                  onRowClick={(p) => setSearch({ sel: p.id })}
                  selected={selected?.id}
                  testId="ml-profiles-table"
                  empty={<EmptyNote>No profiles for this scope. Declare a profiling window in the run's KCL, or turn on continuous profiling.</EmptyNote>}
                />
              </Panel>
              {selected && <ProfileDetail id={selected.id} />}
            </>
          );
        }}
      </MlRead>
    </div>
  );
}

function ProfileDetail({ id }: { id: string }) {
  const profile = useMl("getMlProfile", { profileId: id });
  return (
    <MlRead query={profile} what="this profile">
      {(data) => (data.profile ? <ProfileBody profile={data.profile} /> : <EmptyNote>The profile is gone.</EmptyNote>)}
    </MlRead>
  );
}

function ProfileBody({ profile }: { profile: MlProfile }) {
  const s = profile.summary;
  const timed = !!s && (has(s.computeShare) || has(s.commShare));
  return (
    <div className="space-y-4" data-testid="ml-profile-detail">
      {profile.collectives.length > 0 && <FlightRecorder profile={profile} />}
      {(timed || (s && s.notes.length > 0)) && (
        <Grid cols={2}>
          {timed && s ? (
            <Panel title="Where the GPU time went" note="Holistic Trace Analysis of this capture" testId="ml-profile-hta">
              <div className="space-y-4">
                <StackedBar
                  segments={[
                    { label: "compute", value: s.computeShare, color: 0 },
                    { label: "communication", value: s.commShare, color: 2 },
                    { label: "memcpy", value: s.memcpyShare, color: 3 },
                    { label: "idle", value: s.idleShare, tone: "neutral" },
                  ]}
                  format={(v) => `${(v * 100).toFixed(0)}%`}
                />
                <FactList
                  columns={3}
                  facts={[
                    { label: "Comm overlapped", value: <span className="tabular-nums">{(s.commOverlap * 100).toFixed(0)}%</span> },
                    { label: "Launch-bound", value: s.launchBound ? <StatusDot tone="warning" label="yes" /> : "no" },
                    { label: "Peak memory", value: has(s.peakMemoryGb) ? `${s.peakMemoryGb.toFixed(1)} GB` : "—" },
                  ]}
                />
              </div>
            </Panel>
          ) : null}
          {s && s.notes.length > 0 && (
            <Panel title={PROFILE_KIND_LABELS[profile.kind] ?? "Profile"} note={`${profile.trigger} · ${profile.capturedBy}`} testId="ml-profile-notes">
              <FactList columns={1} facts={s.notes.map((n) => ({ label: n.label, value: <span className="whitespace-normal">{n.value}</span> }))} />
            </Panel>
          )}
        </Grid>
      )}
      {s && s.topKernels.length > 0 && <KernelTable kernels={s.topKernels} />}
      {profile.flamegraphJson && <Flamegraph profile={profile} />}
      {!profile.flamegraphJson && profile.openUrl && profile.kind === MlProfileKind.NSIGHT_SYSTEMS && (
        <Panel title="Nsight Systems report">
          <p className="text-ui text-ink-muted">
            The timeline lives in the .nsys-rep:{" "}
            <a href={profile.openUrl} className="text-primary-ink hover:underline" target="_blank" rel="noopener noreferrer">
              {profile.openLabel || "Download"}
            </a>{" "}
            ({fmtBytes(profile.sizeBytes)}) and open it in Nsight Systems.
          </p>
        </Panel>
      )}
    </div>
  );
}

function KernelTable({ kernels }: { kernels: readonly MlKernel[] }) {
  const columns: MlColumn<MlKernel>[] = [
    { key: "name", header: "Kernel", render: (k) => <span className="font-mono text-code text-ink">{k.name}</span> },
    { key: "category", header: "Category", render: (k) => <span className="text-ink-muted">{k.category}</span> },
    { key: "share", header: "Share of GPU time", numeric: true, render: (k) => <Meter ratio={k.share} label={`${(k.share * 100).toFixed(1)}%`} />, sort: (k) => k.share },
    { key: "calls", header: "Calls", numeric: true, render: (k) => num(k.calls).toLocaleString("en-US"), sort: (k) => num(k.calls) },
    { key: "mean", header: "Mean", numeric: true, render: (k) => (k.meanUs >= 1000 ? `${(k.meanUs / 1000).toFixed(2)} ms` : `${Math.round(k.meanUs)} µs`), sort: (k) => k.meanUs },
  ];
  return (
    <Panel title="Top kernels" note="by GPU time" flush testId="ml-profile-kernels">
      <MlTable columns={columns} rows={kernels} rowKey={(k) => k.name} initialSort={{ key: "share", desc: true }} />
    </Panel>
  );
}

function Flamegraph({ profile }: { profile: MlProfile }) {
  const { rows, unit } = useMemo(() => topFunctions(profile.flamegraphJson, 12), [profile.flamegraphJson]);
  const title = `${PROFILE_KIND_LABELS[profile.kind] ?? "Profile"} · ${stepsText(profile)}`;
  return (
    <div className="grid min-w-0 gap-4 xl:grid-cols-3" data-testid="ml-profile-flamegraph">
      <Panel title="Flamegraph" note={profile.kind === MlProfileKind.MEMORY_SNAPSHOT ? "bytes at peak, by allocation stack" : profile.kind === MlProfileKind.GPU_CONTINUOUS ? "GPU kernel time by category" : "Python and native frames"} flush className="xl:col-span-2">
        <div className="border-t border-border-subtle">
          <SpeedscopeFrame profile={profile.flamegraphJson} title={title} />
        </div>
      </Panel>
      <Panel title="Heaviest frames" note={`by self ${unit === "bytes" ? "bytes" : "time"}`} flush>
        <MlTable
          columns={[
            { key: "name", header: "Frame", render: (r) => <span className="block max-w-xs truncate font-mono text-code text-ink" title={r.name}>{r.name}</span> },
            { key: "self", header: "Self", numeric: true, render: (r) => formatProfileValue(r.self, unit) },
            { key: "share", header: "%", numeric: true, render: (r) => (r.share * 100).toFixed(1) },
          ]}
          rows={rows}
          rowKey={(r) => r.name}
          testId="ml-profile-top"
        />
      </Panel>
    </div>
  );
}

function FlightRecorder({ profile }: { profile: MlProfile }) {
  const rows = useMemo(() => collectiveRows(profile.collectives), [profile.collectives]);
  const verdict = flightVerdict(profile.collectives);
  const attemptId = profile.subject?.id ?? "";
  const columns: MlColumn<MlCollectiveState>[] = [
    {
      key: "rank",
      header: "Rank",
      render: (c) => (
        <span className="inline-flex items-center gap-2">
          {c.mismatched && <StatusDot tone="danger" hideLabel label="mismatched" />}
          <SubjectLink subject={{ kind: MlSubjectKind.RANK, id: `${attemptId}/${c.rank}` }}>rank {c.rank}</SubjectLink>
        </span>
      ),
      sort: (c) => c.rank,
    },
    { key: "pod", header: "Pod", render: (c) => <span className="font-mono text-code text-ink-muted">{c.pod.replace(/^.*-(w-\d+)$/, "$1")}</span> },
    { key: "seq", header: "Last seq", numeric: true, render: (c) => num(c.lastSeq).toLocaleString("en-US"), sort: (c) => num(c.lastSeq) },
    { key: "op", header: "Op", render: (c) => <span className="font-mono text-code">{c.op}</span> },
    { key: "size", header: "Size", numeric: true, render: (c) => fmtBytes(c.sizeBytes) },
    { key: "state", header: "State", render: (c) => (c.mismatched ? <span className="text-danger-ink">{c.state}</span> : <span className="text-ink-muted">{c.state}</span>) },
  ];
  return (
    <Panel title="NCCL flight recorder" note={`dumped by the watchdog at step ${fmtStep(profile.fromStep)} · ${profile.ranks.length} ranks`} flush testId="ml-profile-flight">
      <p className="flex items-center gap-2 border-t border-border-subtle px-4 py-2 text-ui text-ink" data-testid="ml-profile-flight-verdict">
        <StatusDot tone="danger" /> {verdict}
      </p>
      <MlTable columns={columns} rows={rows} rowKey={(c) => String(c.rank)} maxRows={10} testId="ml-profile-collectives" />
    </Panel>
  );
}
