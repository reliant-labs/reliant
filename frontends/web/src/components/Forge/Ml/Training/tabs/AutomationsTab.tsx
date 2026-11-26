// Copyright (c) 2026 Reliant Labs

/**
 * A run's Automations tab (owner, 2026-11-26 02:20Z: hooks over any agent
 * loop): the events this run emits that workflows and webhooks subscribe
 * to, what is subscribed and how it last went, an example payload; then the
 * alert rules with their opt-in auto actions.
 */

import { MlAlertState, MlSubjectKind, type MlAlertRule, type MlHook } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { cn } from "@/lib/utils";

import { CodeBlock, MlRead } from "../../MlParts";
import { Chip, EmptyNote, fmtAgo, MlTable, Panel, severityOf, StatusDot, tsMs, useMl, useMlSearch } from "../../kit";
import { Disclosure } from "../parts";
import type { RunScope } from "../runScope";

const ALERT_STATE: Record<number, { label: string; tone: "danger" | "warning" | "success" | "neutral" }> = {
  [MlAlertState.OK]: { label: "OK", tone: "success" },
  [MlAlertState.PENDING]: { label: "Pending", tone: "warning" },
  [MlAlertState.FIRING]: { label: "Firing", tone: "danger" },
  [MlAlertState.SILENCED]: { label: "Silenced", tone: "neutral" },
};

export function AutomationsTab({ scope, now }: { scope: RunScope; now: number }) {
  const alerts = useMl("listMlAlerts", { subject: { kind: MlSubjectKind.RUN, id: scope.run.id, label: scope.run.name } });
  const [search] = useMlSearch();
  const hooks = scope.detail.hooks;
  return (
    <div className="space-y-4" data-testid="ml-run-automations">
      <p className="text-ui text-ink-muted">
        Every event below can trigger a Reliant workflow or webhook. Declare a subscription in <span className="font-mono text-code text-ink">.reliant/workflows/</span>.
      </p>
      <Panel title="Hook events" note={`${hooks.length} event types this run emits`} flush testId="ml-run-hooks">
        {hooks.length === 0 ? (
          <div className="px-4">
            <EmptyNote>This run emits no hook events (it is not tracked, or has not started).</EmptyNote>
          </div>
        ) : (
          <ul className="divide-y divide-border-subtle">
            {hooks.map((h) => (
              <HookRow key={h.eventType} hook={h} now={now} />
            ))}
          </ul>
        )}
      </Panel>
      <Panel title="Alert rules" note="auto actions are opt-in, declared in KCL" flush testId="ml-run-alerts">
        <MlRead query={alerts} what="alert rules">
          {(data) => (
            <MlTable<MlAlertRule>
              rows={data.alerts}
              rowKey={(a) => a.id}
              selected={search.sel}
              testId="ml-run-alerts-table"
              empty={<EmptyNote>No alert rules cover this run.</EmptyNote>}
              columns={[
                {
                  key: "name",
                  header: "Rule",
                  render: (a) => (
                    <div className="flex flex-col py-0.5 leading-tight">
                      <span className="font-medium text-ink">{a.name}</span>
                      <span className="font-mono text-code text-ink-muted">{a.id}</span>
                    </div>
                  ),
                },
                { key: "expr", header: "When", className: "whitespace-normal", render: (a) => <span className="text-meta text-ink-muted">{a.expression}</span> },
                { key: "state", header: "State", render: (a) => <StatusDot tone={ALERT_STATE[a.state]?.tone ?? "neutral"} label={ALERT_STATE[a.state]?.label ?? "—"} /> },
                { key: "sev", header: "Severity", render: (a) => <span className="text-ink-muted">{severityOf(a.severity).label}</span> },
                {
                  key: "auto",
                  header: "Auto action",
                  className: "whitespace-normal",
                  render: (a) =>
                    a.autoAction ? (
                      <div className="space-y-0.5 py-0.5">
                        <Chip tone={a.autoActionEnabled ? "success" : "neutral"}>{a.autoActionEnabled ? "on" : "off · opt-in"}</Chip>
                        <p className="text-meta text-ink">{a.autoAction}</p>
                      </div>
                    ) : (
                      <span className="text-meta text-ink-muted">notify only</span>
                    ),
                },
                { key: "last", header: "Last fired", render: (a) => <span className="text-meta text-ink-muted" title={a.lastAction}>{tsMs(a.lastFiredAt) ? `${fmtAgo(tsMs(a.lastFiredAt), now)} · ${a.fired7d}× 7d` : "never"}</span> },
                { key: "action", header: "Last action", className: "whitespace-normal", render: (a) => <span className="text-meta text-ink-muted">{a.lastAction || "—"}</span> },
                { key: "route", header: "Routed to", render: (a) => <span className="text-meta">{a.routedTo}</span> },
                { key: "decl", header: "Declared in", render: (a) => <span className="font-mono text-code text-ink-muted">{a.declaredIn}</span> },
              ]}
            />
          )}
        </MlRead>
      </Panel>
    </div>
  );
}

const RESULT_TONE: Record<string, "success" | "danger" | "neutral"> = { succeeded: "success", failed: "danger" };

function HookRow({ hook, now }: { hook: MlHook; now: number }) {
  return (
    <li className="space-y-1.5 px-4 py-3" data-testid={`ml-hook-${hook.eventType}`}>
      <div className="flex flex-wrap items-baseline justify-between gap-x-4">
        <span className="font-mono text-code font-medium text-ink">{hook.eventType}</span>
        <span className="text-meta tabular-nums text-ink-muted">
          {hook.emitted7d}× in 7d · last {fmtAgo(tsMs(hook.lastEmittedAt), now)}
        </span>
      </div>
      <p className="text-meta text-ink-muted">{hook.description}</p>
      <ul className="space-y-0.5">
        {hook.targets.length === 0 && <li className="text-meta text-ink-muted">Nothing subscribed.</li>}
        {hook.targets.map((t) => (
          <li key={`${t.kind}-${t.name}`} className="flex flex-wrap items-center gap-x-2 text-meta">
            <Chip>{t.kind}</Chip>
            <span className="font-medium text-ink">{t.name}</span>
            <StatusDot tone={RESULT_TONE[t.lastResult] ?? "neutral"} label={<span className={cn("text-ink-muted")}>{t.lastResult ? `${t.lastResult}${tsMs(t.lastRunAt) ? ` ${fmtAgo(tsMs(t.lastRunAt), now)}` : ""}` : "never ran"}</span>} />
            <span className="font-mono text-code text-ink-muted">{t.declaredIn}</span>
          </li>
        ))}
      </ul>
      {hook.examplePayload && (
        <Disclosure summary="Example payload">
          <CodeBlock code={hook.examplePayload} label={`${hook.eventType} payload`} />
        </Disclosure>
      )}
    </li>
  );
}
