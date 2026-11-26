// Copyright (c) 2026 Reliant Labs

/**
 * A training run (`/forge/ml/runs/<run>[/phases/<phase>][?attempt=n]`),
 * 02-views §3.2: the frame (state, badges, where it came from), the header
 * strip, the hero timeline with its time ↔ step toggle, "Failures &
 * recovery", then the tabs, each scoped to the run, a phase or an attempt.
 */

import { useState } from "react";
import { GitCompare, MessageSquare } from "lucide-react";

import { MlSubjectKind, type GetMlRunResponse } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { FilterChip } from "@/components/forge-ui/filter_bar";

import { MlFrame, MlRead, type MlCrumb } from "../MlParts";
import { fmtAgo, fmtCompact, fmtDuration, fmtGpuHours, fmtPct, fmtStep, fmtUsd, KpiStrip, MlLink, MlTabs, mlHref, num, Panel, SubjectLink, tsMs, useActiveTab, useMl, useMlSearch, type MlTabDef } from "../kit";
import { runScopeOf, type RunScope } from "./runScope";
import { DemoAction, RunState } from "./parts";
import { RunTimeline, type TimelineAxis } from "./RunTimeline";
import { FailuresStrip } from "./FailuresStrip";
import { lostToFailures } from "./trainingModel";
import { MetricsTab } from "./tabs/MetricsTab";
import { SystemTab } from "./tabs/SystemTab";
import { LogsTab } from "./tabs/LogsTab";
import { TracesTab } from "./tabs/TracesTab";
import { ProfilesTab } from "./tabs/ProfilesTab";
import { OverviewTab } from "./tabs/OverviewTab";
import { CheckpointsTab } from "./tabs/CheckpointsTab";
import { EvalsTab } from "./tabs/EvalsTab";
import { ConfigTab } from "./tabs/ConfigTab";
import { EventsTab } from "./tabs/EventsTab";
import { CostTab } from "./tabs/CostTab";
import { AutomationsTab } from "./tabs/AutomationsTab";

export function RunDetail({ runKey, phaseKey }: { runKey: string; phaseKey?: string }) {
  const read = useMl("getMlRun", { run: runKey });
  return (
    <MlRead query={read} what="this run">
      {(detail) => <RunDetailBody detail={detail} phaseKey={phaseKey} />}
    </MlRead>
  );
}

/** The crumbs of a run page: the run, then the phase and attempt in scope. */
export function runCrumbs(scope: RunScope): MlCrumb[] {
  const base = scope.run.name || scope.run.id;
  const crumbs: MlCrumb[] = [{ label: scope.run.name, href: mlHref("runs", [base]) }];
  if (scope.phaseScoped && scope.phase) crumbs.push({ label: scope.phase.key, href: mlHref("runs", [base, "phases", scope.phase.key]) });
  if (scope.attempt && scope.phase) crumbs.push({ label: `attempt ${scope.attempt.n}`, href: mlHref("runs", [base, "phases", scope.phase.key], { attempt: scope.attempt.n }) });
  if (scope.rank !== null && scope.attempt) crumbs.push({ label: `rank ${scope.rank}` });
  return crumbs;
}

function RunDetailBody({ detail, phaseKey }: { detail: GetMlRunResponse; phaseKey?: string }) {
  const [search, setSearch] = useMlSearch();
  const [now] = useState(() => Date.now());
  const scope = runScopeOf(detail, phaseKey, search.attempt, search.rank);
  const { run } = scope;
  const axis: TimelineAxis = search.axis === "step" ? "step" : "time";
  const exp = detail.experiment;
  const par = detail.config?.parallelism;
  const lost = lostToFailures(run);
  const current = run.phases.find((p) => p.key === run.currentPhaseKey);
  const warn = detail.health.some((h) => h.status === "warn" || h.status === "fail");
  const tabs: MlTabDef[] = [
    { id: "overview", label: "Overview", tone: warn ? "warning" : undefined },
    { id: "metrics", label: "Metrics" },
    { id: "system", label: "System" },
    { id: "logs", label: "Logs" },
    { id: "traces", label: "Traces" },
    { id: "profiles", label: "Profiles" },
    { id: "checkpoints", label: "Checkpoints", count: detail.checkpoints.length || undefined },
    { id: "evals", label: "Evals" },
    { id: "config", label: "Config & lineage" },
    { id: "events", label: "Events" },
    { id: "cost", label: "Cost" },
    { id: "automations", label: "Automations", count: detail.hooks.length || undefined },
  ];
  const tab = useActiveTab(tabs);
  const base = run.name || run.id;
  const scopeNote = scope.attempt ? `${scope.phase?.key} attempt ${scope.attempt.n}` : scope.phaseScoped ? `phase ${scope.phase?.key}` : "the whole run";

  return (
    <MlFrame
      view="runs"
      title={run.name}
      crumbs={runCrumbs(scope)}
      meta={<RunState run={run} />}
      subtitle={
        <span className="flex flex-wrap items-center gap-x-1.5">
          {exp && <SubjectLink subject={{ kind: MlSubjectKind.EXPERIMENT, id: exp.id, label: exp.name }} />}
          {run.template && (
            <>
              <span>·</span>
              <span>
                {run.template}@{run.templateRelease} <span className="font-mono text-code">({run.commit.slice(0, 7)})</span>
              </span>
            </>
          )}
          <span>·</span>
          <span>{run.owner}</span>
          <span>·</span>
          <span>started {fmtAgo(tsMs(run.startedAt) ?? tsMs(run.createdAt), now)}</span>
          {run.sweepId && <span>· {run.sweepId} trial {run.trial}</span>}
          {run.launchedVia === "observed" && <span>· observed from the cluster</span>}
        </span>
      }
      actions={
        <>
          <MlLink href={mlHref("runs", ["compare"], { compare: run.id })} className="inline-flex h-8 items-center gap-1.5 rounded-md border border-border px-2.5 text-ui hover:bg-ink/5 hover:no-underline">
            <GitCompare className="h-3.5 w-3.5" aria-hidden="true" /> Compare
          </MlLink>
          <DemoAction icon={<MessageSquare className="h-3.5 w-3.5" />} reason="Demo shell: Ask about this opens a chat in the product">
            Ask about this
          </DemoAction>
        </>
      }
      testId="ml-page-run"
    >
      <KpiStrip
        testId="ml-run-kpis"
        items={[
          {
            label: `Step · ${run.currentPhaseKey || "—"}`,
            value: num(run.totalSteps) ? (
              <span>
                {fmtStep(run.step)}
                <span className="text-ink-muted">/{fmtStep(run.totalSteps)}</span>
              </span>
            ) : (
              "—"
            ),
            sub: num(run.tokensSeen) ? `${fmtCompact(run.tokensSeen)} of ${fmtCompact(run.tokensPlanned)} tokens` : "no steps reported",
            hero: true,
          },
          { label: "ETA", value: run.etaSeconds !== undefined ? fmtDuration(run.etaSeconds) : "—", sub: current ? `${current.key} to ${fmtStep(current.totalSteps)}` : undefined },
          { label: "GPUs", value: `${run.accelerators}×${run.acceleratorProduct.includes("H100") ? "H100" : run.acceleratorProduct.includes("A100") ? "A100" : run.acceleratorProduct}`, sub: par ? `mesh ${meshLabel(par)}` : undefined },
          { label: "Goodput", value: fmtPct(run.goodput), sub: "productive share of GPU time", href: mlHref("runs", [base], { tab: "cost" }) },
          { label: "GPU-h · $", value: fmtGpuHours(run.cost?.gpuHours), sub: fmtUsd(run.cost?.usd), href: mlHref("runs", [base], { tab: "cost" }) },
          { label: "Lost to failures", value: lost.count ? fmtGpuHours(lost.gpuHours) : "none", sub: lost.count ? `${fmtUsd(lost.usd)} · ${lost.count} failure${lost.count === 1 ? "" : "s"}` : undefined, tone: lost.count ? "warning" : undefined },
        ]}
      />

      <Panel
        title="Timeline"
        note="click a phase to scope the tabs, an attempt to scope them to it"
        actions={
          <div className="flex items-center gap-1" role="group" aria-label="Timeline axis">
            <FilterChip on={axis === "time"} onClick={() => setSearch({ axis: undefined })} testId="ml-run-axis-time">
              Time
            </FilterChip>
            <FilterChip on={axis === "step"} onClick={() => setSearch({ axis: "step" })} testId="ml-run-axis-step">
              Step
            </FilterChip>
          </div>
        }
        testId="ml-run-timeline-panel"
      >
        <RunTimeline run={run} checkpoints={detail.checkpoints} axis={axis} selectedPhase={scope.phaseScoped ? (scope.phase?.key ?? null) : null} selectedAttempt={scope.attempt?.n ?? null} now={now} />
      </Panel>

      <FailuresStrip run={run} />

      <div className="space-y-3">
        <MlTabs tabs={tabs} testId="ml-run-tabs" />
        <p className="text-meta text-ink-muted" data-testid="ml-run-scope">
          Showing {scopeNote}
          {(scope.phaseScoped || scope.attempt) && (
            <>
              {" · "}
              <MlLink href={mlHref("runs", [base], { tab: search.tab })}>whole run</MlLink>
            </>
          )}
          {scope.phase &&
            scope.phase.attempts.length > 1 &&
            scope.phase.attempts.map((a) => (
              <span key={a.n}>
                {" · "}
                {scope.attempt?.n === a.n ? (
                  <span className="text-ink">a{a.n}</span>
                ) : (
                  <MlLink href={mlHref("runs", [base, "phases", scope.phase!.key], { attempt: a.n, tab: search.tab })}>a{a.n}</MlLink>
                )}
              </span>
            ))}
        </p>
        <RunTab tab={tab} scope={scope} now={now} />
      </div>
    </MlFrame>
  );
}

const meshLabel = (p: { data: number; tensor: number; pipeline: number; context: number; expert: number }) =>
  [p.pipeline > 1 && `pp${p.pipeline}`, p.data > 1 && `dp${p.data}`, p.context > 1 && `cp${p.context}`, p.expert > 1 && `ep${p.expert}`, p.tensor > 1 && `tp${p.tensor}`].filter(Boolean).join("×") || "dp1";

function RunTab({ tab, scope, now }: { tab: string; scope: RunScope; now: number }) {
  switch (tab) {
    case "metrics":
      return <MetricsTab scope={scope} />;
    case "system":
      return <SystemTab scope={scope} />;
    case "logs":
      return <LogsTab scope={scope} />;
    case "traces":
      return <TracesTab scope={scope} />;
    case "profiles":
      return <ProfilesTab scope={scope} />;
    case "checkpoints":
      return <CheckpointsTab scope={scope} now={now} />;
    case "evals":
      return <EvalsTab scope={scope} />;
    case "config":
      return <ConfigTab scope={scope} />;
    case "events":
      return <EventsTab scope={scope} now={now} />;
    case "cost":
      return <CostTab scope={scope} />;
    case "automations":
      return <AutomationsTab scope={scope} now={now} />;
    default:
      return <OverviewTab scope={scope} />;
  }
}
