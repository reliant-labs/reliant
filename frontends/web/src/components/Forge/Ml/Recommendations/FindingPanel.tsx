// Copyright (c) 2026 Reliant Labs

/**
 * One finding, in the inbox's side panel (02-views §9): state and when it was
 * seen, why it matters, the impact, the evidence (chart with its events,
 * table with subject links, facts, the pod-start breakdown), the rule, the
 * adopt-first fix (the KCL diff coloured by line, or the command), the
 * actions (writes refused in the demo; a proposed fix links its PR) and how
 * the fix is verified.
 */

import { ExternalLink, GitPullRequest } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { MlFixKind, type MlEvidenceRow, type MlFinding } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { cn } from "@/lib/utils";

import {
  Chip,
  fmtAgo,
  fmtDuration,
  fmtGpuHours,
  fmtTime,
  fmtUsd,
  FactList,
  findingCategoryOf,
  findingStateOf,
  impactUnitOf,
  MlChart,
  MlTable,
  severityOf,
  SidePanel,
  StackedBar,
  StatusDot,
  SubjectLink,
  subjectKindOf,
  tsMs,
} from "../kit";
import { DEMO_READ_ONLY } from "../Fleet/parts";
import { CopyButton, CopyCommand } from "./CopyCommand";
import { fixKindLabel, parseDiff, prNumber } from "./recsModel";

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="space-y-1.5">
      <h3 className="text-meta font-medium text-ink-muted">{title}</h3>
      {children}
    </section>
  );
}

export function KclDiff({ diff, path }: { diff: string; path: string }) {
  const lines = parseDiff(diff);
  return (
    <figure className="min-w-0 space-y-1" data-testid="ml-finding-diff">
      <figcaption className="font-mono text-code text-ink-muted">{path}</figcaption>
      <pre className="overflow-x-auto rounded-md border border-border/60 bg-surface-sunken py-2 font-mono text-code">
        {lines
          .filter((l) => l.kind !== "file")
          .map((l, i) => (
            <div
              key={i}
              className={cn("whitespace-pre px-3", l.kind === "add" ? "text-success-ink" : l.kind === "del" ? "text-danger-ink" : l.kind === "hunk" ? "text-ink-muted" : "text-ink")}
              data-kind={l.kind}
            >
              {l.text || " "}
            </div>
          ))}
      </pre>
    </figure>
  );
}

export function FindingPanel({ finding: f, onClose }: { finding: MlFinding; onClose: () => void }) {
  const sev = severityOf(f.severity);
  const state = findingStateOf(f.state);
  const nowMs = Date.now();
  const fix = f.fix;
  return (
    <SidePanel
      title={f.title}
      onClose={onClose}
      testId="ml-finding-panel"
      width="min(720px, 100vw)"
      meta={
        <span className="inline-flex flex-wrap items-center gap-x-3 gap-y-1">
          <StatusDot tone={sev.tone} label={sev.label} />
          <StatusDot tone={state.tone} label={`${state.label}${f.assignee ? ` · ${f.assignee}` : ""}`} />
          <span>{findingCategoryOf(f.category).label}</span>
          <span>
            first seen {fmtAgo(tsMs(f.firstSeenAt), nowMs)} · last {fmtAgo(tsMs(f.lastSeenAt), nowMs)}
          </span>
        </span>
      }
    >
      {f.summary && <p className="text-ui text-ink">{f.summary}</p>}
      <Section title="Why it matters">
        <p className="text-ui text-ink">{f.why}</p>
      </Section>
      <Section title="Impact">
        <FactList
          columns={2}
          facts={[
            { label: "Impact", value: f.impact?.text || "—" },
            { label: "Unit", value: impactUnitOf(f.impact?.unit ?? 0).label },
            { label: "Per day", value: f.impact?.usdPerDay ? `${fmtUsd(f.impact.usdPerDay)}/day` : "—" },
            { label: "GPU time", value: f.impact?.gpuHoursPerDay ? `${fmtGpuHours(f.impact.gpuHoursPerDay)}/day` : "—" },
          ]}
        />
      </Section>
      {(f.evidenceSeries.length > 0 || f.evidenceTable?.rows.length || f.evidenceFacts.length > 0 || f.startBreakdown) && (
        <Section title="Evidence">
          <div className="space-y-3">
            {f.evidenceSeries.length > 0 && <MlChart series={f.evidenceSeries} events={f.evidenceEvents} label={f.evidenceSeries[0].label || "Evidence"} height={150} legend testId="ml-finding-chart" />}
            {f.startBreakdown && (
              <div className="space-y-1">
                <p className="text-meta text-ink-muted">
                  Pod start p50 by stage · {f.startBreakdown.sampleCount} starts of {f.startBreakdown.workload}
                </p>
                <StackedBar
                  segments={f.startBreakdown.stages.map((s) => ({ label: s.stage, value: s.p50Seconds, tone: s.findingId === f.id ? "warning" : undefined, title: `${s.stage}: p50 ${fmtDuration(s.p50Seconds)}, p95 ${fmtDuration(s.p95Seconds)}${s.topCause ? ` · ${s.topCause}` : ""}` }))}
                  format={(v) => fmtDuration(v)}
                />
              </div>
            )}
            {f.evidenceTable && f.evidenceTable.rows.length > 0 && (
              <div className="-mx-4 border-y border-border-subtle">
                <MlTable<MlEvidenceRow>
                  rows={f.evidenceTable.rows}
                  rowKey={(r) => r.cells.join("|")}
                  columns={f.evidenceTable.columns.map((c, i) => ({
                    key: String(i),
                    header: c,
                    render: (r) => (i === 0 && r.subject ? <SubjectLink subject={r.subject}>{r.cells[i]}</SubjectLink> : <span className={cn(i > 0 && "text-ink-muted")}>{r.cells[i]}</span>),
                  }))}
                  testId="ml-finding-table"
                />
              </div>
            )}
            {f.evidenceFacts.length > 0 && <FactList columns={1} facts={f.evidenceFacts.map((x) => ({ label: x.label, value: x.value }))} />}
            {f.evidenceEvents.length > 0 && (
              <ul className="space-y-1 text-ui">
                {f.evidenceEvents.slice(0, 6).map((e) => (
                  <li key={e.id} className="flex items-baseline gap-2">
                    <span className="w-24 shrink-0 text-meta tabular-nums text-ink-muted">{fmtTime(tsMs(e.at))}</span>
                    <span className="min-w-0 flex-1 truncate" title={e.detail}>
                      {e.subject ? <SubjectLink subject={e.subject}>{e.title}</SubjectLink> : e.title}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </Section>
      )}
      <Section title="Rule">
        <p className="text-ui text-ink">{f.rule}</p>
        <p className="font-mono text-code text-ink-muted">{f.ruleId}</p>
      </Section>
      {fix && (
        <Section title="Fix">
          <div className="space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <Chip>{fixKindLabel(fix.kind)}</Chip>
              {fix.approach === "adopt" && <Chip title="The fix uses what the cluster already runs">adopt what exists</Chip>}
            </div>
            <p className="text-ui text-ink">{fix.summary}</p>
            {fix.kind === MlFixKind.KCL_PR && fix.kclDiff && <KclDiff diff={fix.kclDiff} path={fix.kclPath} />}
            {fix.command && <CopyCommand command={fix.command} label="Command" testId="ml-finding-command" />}
            {fix.docsUrl && (
              <a href={fix.docsUrl} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-meta text-primary-ink hover:underline">
                Reference <ExternalLink className="h-3 w-3" aria-hidden="true" />
              </a>
            )}
          </div>
        </Section>
      )}
      <div className="flex flex-wrap items-center gap-2 border-t border-border pt-3" data-testid="ml-finding-actions">
        {f.prUrl ? (
          <Button variant="primary" size="sm" href={f.prUrl} target="_blank" rel="noreferrer" leftIcon={<GitPullRequest />}>
            View PR {prNumber(f.prUrl)}
          </Button>
        ) : fix?.kind === MlFixKind.KCL_PR ? (
          <Button variant="primary" size="sm" disabled title={DEMO_READ_ONLY} leftIcon={<GitPullRequest />}>
            Open PR
          </Button>
        ) : null}
        {fix?.command && <CopyButton text={fix.command} />}
        {["Assign", "Snooze", "Dismiss with reason"].map((a) => (
          <Button key={a} variant="outline" size="sm" disabled title={DEMO_READ_ONLY}>
            {a}
          </Button>
        ))}
      </div>
      {f.verify && (
        <p className="text-ui text-ink-muted">
          <span className="font-medium text-ink">After fix: </span>
          {f.verify}
        </p>
      )}
      {f.dismissReason && <p className="text-ui text-ink-muted">Dismissed: {f.dismissReason}</p>}
      {f.snoozedUntil && <p className="text-ui text-ink-muted">Snoozed until {fmtTime(tsMs(f.snoozedUntil))}</p>}
      <Section title={`Subjects · ${f.subjects.length}`}>
        <ul className="space-y-1 text-ui" data-testid="ml-finding-subjects">
          {f.subjects.map((s) => (
            <li key={`${s.kind}|${s.id}`} className="flex items-baseline gap-2">
              <span className="w-24 shrink-0 text-meta text-ink-muted">{subjectKindOf(s.kind).label}</span>
              <SubjectLink subject={s} className="min-w-0 truncate" />
            </li>
          ))}
        </ul>
      </Section>
    </SidePanel>
  );
}
