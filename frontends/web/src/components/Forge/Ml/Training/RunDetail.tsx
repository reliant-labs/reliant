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
import { endReasonOf, useCompareTray, fmtAgo, fmtCompact, fmtDuration, fmtGpuHours, fmtPct, fmtStep, fmtUsd, KpiStrip, MlLink, MlTabs, mlHref, num, Panel, SubjectLink, tsMs, useActiveTab, useMl, useMlSearch, type MlTabDef } from "../kit";
import { runScopeOf, type RunScope } from "./runScope";
import { DemoAction, RunState } from "./parts";
import { RunTimeline, type TimelineAxis } from "./RunTimeline";
import { FailuresStrip } from "./FailuresStrip";
import { scopedNumbers } from "./trainingModel";
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
  const scoped = scopedNumbers(run, scope.phase, scope.attempt, scope.phaseScoped, now);
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
          <CompareToggle id={run.id} name={run.name} />
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
            label: `Step · ${scoped.scopeLabel}`,
            value: scoped.totalSteps ? (
              <span>
                {fmtStep(scoped.step)}
                <span className="text-ink-muted">/{fmtStep(scoped.totalSteps)}</span>
              </span>
            ) : (
              "—"
            ),
            sub: !scope.phaseScoped && num(run.tokensSeen) ? `${fmtCompact(run.tokensSeen)} of ${fmtCompact(run.tokensPlanned)} tokens` : scope.attempt ? `from step ${fmtStep(scope.attempt.startStep)}` : scoped.totalSteps ? "steps continue across attempts" : "no steps reported",
            hero: true,
          },
          scoped.etaSeconds !== null
            ? { label: "ETA", value: fmtDuration(scoped.etaSeconds), sub: current ? `${current.key} to ${fmtStep(current.totalSteps)}` : undefined }
            : { label: scope.attempt ? "Ran for" : "Duration", value: scoped.ranSeconds !== null ? fmtDuration(scoped.ranSeconds) : "—", sub: scope.attempt ? endReasonOf(scope.attempt.endReason).label : undefined },
          { label: "GPUs", value: `${scoped.accelerators}×${productShort(run.acceleratorProduct)}`, sub: par ? `mesh ${meshLabel(par)}` : undefined },
          { label: "Goodput", value: fmtPct(scoped.goodput), sub: `productive share · ${scoped.scopeLabel}`, href: mlHref("runs", [base], { tab: "cost" }) },
          { label: "GPU-h · $", value: fmtGpuHours(scoped.gpuHours), sub: fmtUsd(scoped.usd), href: mlHref("runs", [base], { tab: "cost" }) },
          {
            label: "Lost to failures",
            value: scoped.lost.count ? fmtGpuHours(scoped.lost.gpuHours) : "none",
            sub: scoped.lost.count ? `${fmtUsd(scoped.lost.usd)} · ${scoped.lost.count} failure${scoped.lost.count === 1 ? "" : "s"}` : scoped.scopeLabel,
            tone: scoped.lost.count ? "warning" : undefined,
          },
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

function productShort(product: string): string {
  return product.includes("H100") ? "H100" : product.includes("A100") ? "A100" : product.includes("L4") ? "L4" : product;
}

/** Adds the run to the compare tray, or takes it out. */
function CompareToggle({ id, name }: { id: string; name: string }) {
  const tray = useCompareTray();
  const on = tray.has(id);
  return (
    <button
      type="button"
      onClick={() => tray.toggle({ id, name })}
      disabled={!on && tray.full}
      aria-pressed={on}
      className="inline-flex h-8 items-center gap-1.5 rounded-md border border-border px-2.5 text-ui text-ink hover:bg-ink/5 disabled:cursor-not-allowed disabled:text-ink-muted"
      title={!on && tray.full ? "The tray holds 4 runs" : undefined}
      data-testid="ml-run-compare"
    >
      <GitCompare className="h-3.5 w-3.5" aria-hidden="true" /> {on ? "In compare tray" : "Add to compare"}
    </button>
  );
}
