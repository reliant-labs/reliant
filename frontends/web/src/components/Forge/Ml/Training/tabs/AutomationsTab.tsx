// Copyright (c) 2026 Reliant Labs

/**
 * A run's Automations tab (owner, 2026-11-26 02:20Z: hooks over any agent
 * loop): the events this run emits that workflows and webhooks subscribe
 * to, what is subscribed and how it last went, an example payload; then the
 * alerts pending or firing on the run (AlertingService), with the auto
 * action each rule opts into.
 */

import { MlSubjectKind, type MlHook } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { cn } from "@/lib/utils";

import { CodeBlock, MlRead } from "../../MlParts";
import { Chip, EmptyNote, fmtAgo, MlAlertsTable, Panel, StatusDot, tsMs, useMlAlerts } from "../../kit";
import { Disclosure } from "../parts";
import type { RunScope } from "../runScope";

export function AutomationsTab({ scope, now }: { scope: RunScope; now: number }) {
  const alerts = useMlAlerts({ kind: MlSubjectKind.RUN, id: scope.run.id });
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
      <Panel title="Alerts" note="pending and firing on this run · auto actions are opt-in, declared in KCL" flush testId="ml-run-alerts">
        <MlRead query={alerts} what="alerts">
          {(data) => <MlAlertsTable alerts={data.alerts} now={now} testId="ml-run-alerts-table" empty={<EmptyNote>Nothing is pending or firing on this run.</EmptyNote>} />}
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
