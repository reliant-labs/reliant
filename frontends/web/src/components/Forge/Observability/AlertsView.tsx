// Copyright (c) 2025 Reliant Labs

/**
 * Alerts: what is firing, what changed state, silences, the rules each
 * environment declares, and where notifications go (control-plane
 * AlertingService; alerting plan A7).
 *
 * ── RULES ARE CODE ───────────────────────────────────────────────────────────
 *
 * A rule is declared in the project's KCL (`forge.Observability.alerts`) and
 * reaches the control plane in the environment's release shape. Nothing here
 * edits one: the Rules tab shows each rule as its environment declares it, who
 * wrote it (the project, or forge's defaults by id), and the KCL to change it —
 * "edit as code", as dashboards have "save as code". What IS managed here is
 * org state the code cannot hold: receivers (with their secrets), silences, and
 * the read state of in-app notifications.
 *
 * ── WHO MAY CHANGE WHAT ──────────────────────────────────────────────────────
 *
 * Any member can read all of it and mark notifications read; creating or
 * changing a receiver or a silence needs the org admin role. The console cannot
 * ask which role the caller has, so it offers the actions and, on the first
 * PermissionDenied, turns the area read-only and says why.
 */

import { useMemo, useState, type ReactNode } from "react";
import { AlertTriangle, ArrowUpRight, BellOff, CheckCircle2, ChevronDown, ChevronRight, Code2, Copy, ExternalLink, LineChart, Lock, ShieldCheck } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Button from "@/components/forge-ui/button";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import { CommandLine, DOCS } from "@/components/Forge/GettingStarted";
import { useAlertHistory, useAlerts } from "@/hooks/observability-queries";
import type { AlertEvent, AlertInstance, DeclaredAlertRule } from "@/services/controlPlane/observability/alertingTypes";
import { alertingErrorKind } from "@/services/controlPlane/observability/errors";
import { RANGE_LABELS, type RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { AlertReceivers } from "./AlertReceivers";
import { AskReliantActions, AskReliantMenu } from "./AskReliant/AskReliant";
import type { AskSubject } from "./AskReliant/askReliantPrompt";
import { AlertSilences, SilenceDialog } from "./AlertSilences";
import { ruleAsCode, starterRuleKcl, type RuleAsCode } from "./alertsAsCode";
import { formatAgo, formatTimestamp } from "./format";
import { alertPivot, alertRange, useExploreHref } from "./Metrics/pivots";
import { placeEnvironments, type ObsEnvironment, type ObsPlace } from "./obsPlace";
import { ALERTS_TABS, type AlertsTab, type ObsNavigate } from "./obsSearch";
import { ExploreLink } from "./Metrics/ExploreLink";
import { ObsError, ObsLoadingRows } from "./ObsStates";

export const ALERTS_TAB_LABELS: Record<AlertsTab, string> = {
  firing: "Firing",
  history: "History",
  silences: "Silences",
  rules: "Rules",
  receivers: "Receivers",
};

/** A rule as one environment declares it, with that environment. */
export interface EnvRule {
  env: ObsEnvironment;
  rule: DeclaredAlertRule;
  /** `<env name>/<rule>`: the URL's `rule`. */
  key: string;
}

export function declaredRules(environments: ObsEnvironment[]): EnvRule[] {
  return environments.flatMap((env) =>
    (env.declared?.alerts ?? []).map((rule) => ({ env, rule, key: `${env.name}/${rule.alert}` }))
  );
}

/** Whether an environment opted out of all telemetry, so nothing is evaluated there. */
export function observabilityOff(env: ObsEnvironment): boolean {
  return env.declared?.observability?.enabled === false;
}

export interface AlertsViewProps {
  orgId: string;
  place: ObsPlace;
  /** The environments in view (the project area's picker); empty is all of them. */
  envIds: string[];
  range: RangePreset;
  tab: AlertsTab;
  rule: string | undefined;
  navigate: ObsNavigate;
}

export function AlertsView({ orgId, place, envIds, range, tab, rule, navigate }: AlertsViewProps) {
  const environments = useMemo(() => {
    const all = placeEnvironments(place);
    return envIds.length ? all.filter((env) => envIds.includes(env.id)) : all;
  }, [place, envIds]);
  // The server narrows by one env id; several are filtered here.
  const serverEnv = environments.length === 1 ? environments[0].id : undefined;
  const inView = (envId: string) => !envIds.length || envIds.includes(envId) || place.kind === "env";
  const alerts = useAlerts(orgId, serverEnv);
  const firing = useMemo(() => (alerts.data ?? []).filter((a) => inView(a.envId)), [alerts.data, envIds]); // eslint-disable-line react-hooks/exhaustive-deps
  const rules = useMemo(() => declaredRules(environments), [environments]);
  const [readOnly, setReadOnly] = useState(false);
  const [silencing, setSilencing] = useState<AlertInstance | null>(null);
  const envName = (id: string) => placeEnvironments(place).find((env) => env.id === id)?.name ?? id;

  const kind = alerts.error ? alertingErrorKind(alerts.error) : null;
  const notConfigured = kind === "not-configured";
  const onDenied = () => setReadOnly(true);
  const select = (next: AlertsTab) => navigate({ alerts: next === "firing" ? undefined : next, rule: undefined }, { replace: true });
  const count: Partial<Record<AlertsTab, number>> = {
    firing: firing.filter((a) => a.state === "firing").length,
    rules: rules.length,
  };

  return (
    <div className="space-y-4" data-testid="obs-alerts" data-tab={tab}>
      <div className="flex flex-wrap items-center gap-3">
        <div role="tablist" aria-label="Alerts" className="inline-flex flex-wrap rounded-lg border border-border bg-surface-sunken p-0.5">
          {ALERTS_TABS.map((id) => (
            <button
              key={id}
              type="button"
              role="tab"
              aria-selected={id === tab}
              onClick={() => select(id)}
              data-testid={`obs-alerts-tab-${id}`}
              className={cn(
                "inline-flex items-center gap-1.5 rounded-md px-3 py-1 text-sm transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                id === tab ? "bg-surface font-medium text-ink shadow-sm" : "text-ink-muted hover:text-ink"
              )}
            >
              {ALERTS_TAB_LABELS[id]}
              {count[id] ? (
                <span
                  className={cn(
                    "rounded-full px-1.5 text-2xs tabular-nums",
                    id === "firing" ? "bg-danger-surface text-danger-ink" : "bg-border/60 text-ink-muted"
                  )}
                >
                  {count[id]}
                </span>
              ) : null}
            </button>
          ))}
        </div>
        {readOnly && (
          <p className="inline-flex items-center gap-1.5 text-xs text-ink-muted" role="status" data-testid="obs-alerts-read-only">
            <Lock className="h-3.5 w-3.5" aria-hidden="true" />
            You can view alerting. Changing receivers and silences needs an organization admin.
          </p>
        )}
      </div>

      {tab === "rules" ? (
        <RulesTab rules={rules} environments={environments} firing={firing} selected={rule} navigate={navigate} />
      ) : notConfigured ? (
        <AlertingNotConfigured ruleCount={rules.length} onRules={() => select("rules")} />
      ) : tab === "firing" ? (
        alerts.isLoading ? (
          <ObsLoadingRows testId="obs-alerts-loading" />
        ) : alerts.error ? (
          <ObsError error={alerts.error} subject="this organization" onRetry={() => void alerts.refetch()} retrying={alerts.isFetching} />
        ) : (
          <FiringTab
            alerts={firing}
            rules={rules}
            environments={environments}
            envName={envName}
            readOnly={readOnly}
            onSilence={setSilencing}
            onOpenRule={(key) => navigate({ alerts: "rules", rule: key })}
            onRules={() => select("rules")}
          />
        )
      ) : tab === "history" ? (
        <HistoryTab orgId={orgId} envId={serverEnv} inView={inView} range={range} envName={envName} navigate={navigate} />
      ) : tab === "silences" ? (
        <AlertSilences orgId={orgId} readOnly={readOnly} onDenied={onDenied} />
      ) : (
        <AlertReceivers orgId={orgId} environments={placeEnvironments(place)} readOnly={readOnly} onDenied={onDenied} />
      )}

      {silencing && (
        <SilenceDialog
          orgId={orgId}
          initial={silenceMatchersFor(silencing)}
          title={`Silence ${silencing.rule}`}
          onClose={() => setSilencing(null)}
          onDenied={onDenied}
          onCreated={() => {
            setSilencing(null);
            navigate({ alerts: "silences" }, { replace: true });
          }}
        />
      )}
    </div>
  );
}

/** The matchers that silence exactly this alert instance: its rule and its own labels (never a reliant_* one). */
export function silenceMatchersFor(alert: Pick<AlertInstance, "rule" | "labels">) {
  const labels: Record<string, string> = { ...alert.labels, alertname: alert.rule };
  return Object.keys(labels)
    .filter((name) => !name.startsWith("reliant_") && /^[a-zA-Z_][a-zA-Z0-9_]*$/.test(name))
    .sort((a, b) => (a === "alertname" ? -1 : b === "alertname" ? 1 : a.localeCompare(b)))
    .map((name) => ({ name, value: labels[name], isRegex: false, isEqual: true }));
}

/**
 * What a chat about an alert instance is handed: references only
 * (askReliantPrompt.ts) — its rule, fingerprint, environment, state and since
 * when; never its labels or annotations, which `obs status` reads.
 */
export function alertAskSubject(alert: Pick<AlertInstance, "rule" | "fingerprint" | "state" | "firingAtMs" | "activeAtMs">, env: string | null): Extract<AskSubject, { kind: "alert" }> {
  return {
    kind: "alert",
    rule: alert.rule,
    fingerprint: alert.fingerprint,
    env,
    state: alert.state,
    sinceMs: (alert.state === "firing" ? alert.firingAtMs : alert.activeAtMs) ?? null,
  };
}

/** The instance a rule's actions are about: the longest-firing one, else the longest-pending. */
function leadInstance(active: AlertInstance[]): AlertInstance | undefined {
  const since = (a: AlertInstance) => (a.state === "firing" ? a.firingAtMs : a.activeAtMs) ?? Number.MAX_SAFE_INTEGER;
  return [...active].sort((a, b) => (a.state === b.state ? since(a) - since(b) : a.state === "firing" ? -1 : 1))[0];
}

// ── States ───────────────────────────────────────────────────────────────────

function Frame({ icon, title, children, testId, tone = "neutral" }: { icon: ReactNode; title: string; children?: ReactNode; testId: string; tone?: "neutral" | "good" }) {
  return (
    <div data-testid={testId} role="status" className="flex items-start gap-3 rounded-lg border border-dashed border-border bg-surface-sunken px-4 py-5">
      <span className={cn("mt-0.5 shrink-0", tone === "good" ? "text-success-ink" : "text-ink-muted")} aria-hidden="true">
        {icon}
      </span>
      <div className="min-w-0 space-y-1.5">
        <p className="text-sm font-medium text-ink">{title}</p>
        {children && <div className="space-y-1.5 text-xs text-ink-muted">{children}</div>}
      </div>
    </div>
  );
}

function AlertingNotConfigured({ ruleCount, onRules }: { ruleCount: number; onRules: () => void }) {
  return (
    <Frame icon={<BellOff className="h-4 w-4" />} title="Alerting isn't running for this organization yet" testId="obs-alerting-not-configured">
      <p>
        Alerts are evaluated and routed by Reliant&apos;s hosted observability, which this control plane doesn&apos;t run yet. Until it
        does, nothing fires, nothing is delivered, and receivers and silences can&apos;t be set up.
      </p>
      <p>
        {ruleCount > 0 ? (
          <>
            Your environments already declare {ruleCount} rule{ruleCount === 1 ? "" : "s"}; they start being evaluated as soon as it
            runs.{" "}
            <button type="button" onClick={onRules} className="rounded-sm text-primary underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring">
              See the rules
            </button>
          </>
        ) : (
          <>Rules are declared in your code, in each environment&apos;s <code className="font-mono">forge.Observability.alerts</code>.</>
        )}
      </p>
    </Frame>
  );
}

// ── Firing ───────────────────────────────────────────────────────────────────

const SEVERITY_TONE: Record<string, string> = {
  critical: "bg-danger-surface text-danger-ink ring-danger/30",
  warning: "bg-warning-surface text-warning-ink ring-warning/30",
  info: "text-ink-muted ring-border",
};

export function SeverityChip({ severity }: { severity: string }) {
  if (!severity) return null;
  return (
    <span className={cn("rounded px-1.5 py-px text-2xs font-medium uppercase tracking-wide ring-1 ring-inset", SEVERITY_TONE[severity] ?? SEVERITY_TONE.info)}>
      {severity}
    </span>
  );
}

function SourceChip({ rule }: { rule: DeclaredAlertRule | undefined }) {
  if (!rule) return null;
  return (
    <span
      className="rounded border border-border px-1.5 py-px text-2xs text-ink-muted"
      title={rule.source === "user" ? "Declared by your project (forge.AlertRule)" : `forge default "${rule.defaultId}", tuned in forge.Alerts.defaults`}
      data-testid="obs-rule-source"
    >
      {rule.source === "user" ? "Your rule" : `forge default · ${rule.defaultId}`}
    </span>
  );
}

function LabelChips({ labels, skip = [] }: { labels: Record<string, string>; skip?: string[] }) {
  const keys = Object.keys(labels).filter((k) => !skip.includes(k)).sort();
  if (!keys.length) return null;
  return (
    <span className="flex flex-wrap gap-1">
      {keys.map((k) => (
        <span key={k} className="rounded bg-surface-sunken px-1.5 py-px font-mono text-2xs text-ink-muted ring-1 ring-inset ring-border/60">
          {k}=<span className="text-ink">{labels[k]}</span>
        </span>
      ))}
    </span>
  );
}

function FiringTab({
  alerts,
  rules,
  environments,
  envName,
  readOnly,
  onSilence,
  onOpenRule,
  onRules,
}: {
  alerts: AlertInstance[];
  rules: EnvRule[];
  environments: ObsEnvironment[];
  envName: (id: string) => string;
  readOnly: boolean;
  onSilence: (alert: AlertInstance) => void;
  onOpenRule: (key: string) => void;
  onRules: () => void;
}) {
  const [open, setOpen] = useState<string | null>(null);
  const exploreHref = useExploreHref();
  const exploreAlert = (a: AlertInstance, rule: DeclaredAlertRule | undefined) =>
    rule ? (exploreHref?.(alertPivot(rule.expr, a.labels), { env: envName(a.envId), range: alertRange(a.firingAtMs ?? a.activeAtMs, Date.now()) }) ?? null) : null;
  const ruleOf = (a: AlertInstance) => rules.find((r) => r.env.id === a.envId && r.rule.alert === a.rule);
  if (alerts.length === 0) {
    const evaluated = rules.filter((r) => !observabilityOff(r.env)).length;
    return (
      <Frame icon={<CheckCircle2 className="h-4 w-4" />} tone="good" title="Nothing is firing" testId="obs-alerts-none">
        {evaluated > 0 ? (
          <p>
            {evaluated} rule{evaluated === 1 ? " is" : "s are"} evaluated across {environments.length} environment
            {environments.length === 1 ? "" : "s"} every minute.{" "}
            <button type="button" onClick={onRules} className="rounded-sm text-primary underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring">
              See the rules
            </button>
          </p>
        ) : (
          <p>
            No environment in view declares a rule yet, so nothing can fire.{" "}
            <button type="button" onClick={onRules} className="rounded-sm text-primary underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring">
              Add one in code
            </button>
          </p>
        )}
      </Frame>
    );
  }
  return (
    <Card padding="none" data-testid="obs-alerts-firing">
      <ul className="divide-y divide-border/60">
        {alerts.map((a) => {
          const declared = ruleOf(a);
          const key = `${a.envId}/${a.fingerprint}`;
          const expanded = open === key;
          const summary = a.annotations.summary || a.annotations.description || "";
          return (
            <li key={key} data-testid={`obs-alert-${a.rule}`} data-state={a.state}>
              <div className="flex flex-wrap items-start gap-3 px-4 py-3">
                <button
                  type="button"
                  onClick={() => setOpen(expanded ? null : key)}
                  aria-expanded={expanded}
                  aria-label={`${expanded ? "Hide" : "Show"} details of ${a.rule}`}
                  className="mt-0.5 rounded p-0.5 text-ink-muted hover:text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  {expanded ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
                </button>
                <div className="min-w-0 flex-1 space-y-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span
                      className={cn("inline-flex items-center gap-1 text-xs font-medium", a.state === "firing" ? "text-danger-ink" : "text-warning-ink")}
                    >
                      <span className={cn("h-2 w-2 rounded-full", a.state === "firing" ? "bg-danger" : "bg-warning")} aria-hidden="true" />
                      {a.state === "firing" ? "Firing" : "Pending"}
                    </span>
                    <span className="font-mono text-sm font-medium text-ink">{a.rule}</span>
                    <SeverityChip severity={a.labels.severity ?? ""} />
                    <span className="font-mono text-xs text-ink-muted">{envName(a.envId)}</span>
                    <SourceChip rule={declared?.rule} />
                  </div>
                  {summary && <p className="text-sm text-ink">{summary}</p>}
                  <p className="text-xs text-ink-muted">
                    {a.state === "firing" && a.firingAtMs
                      ? `Firing since ${formatAgo(a.firingAtMs)}`
                      : a.activeAtMs
                        ? `Pending since ${formatAgo(a.activeAtMs)}, fires if it holds for the rule's "for"`
                        : ""}
                    {` · last value ${Number.isInteger(a.lastValue) ? a.lastValue : a.lastValue.toPrecision(3)}`}
                  </p>
                  <LabelChips labels={a.labels} skip={["alertname", "severity"]} />
                </div>
                <div className="flex shrink-0 gap-2">
                  <AskReliantMenu
                    subject={alertAskSubject(a, environments.find((env) => env.id === a.envId)?.name ?? null)}
                    appearance="button"
                    testId={`obs-alert-ask-${a.rule}`}
                  />
                  {declared && (
                    <Button variant="ghost" size="sm" leftIcon={<Code2 className="h-3.5 w-3.5" />} onClick={() => onOpenRule(declared.key)}>
                      Rule
                    </Button>
                  )}
                  {exploreAlert(a, declared?.rule) && (
                    <ExploreLink href={exploreAlert(a, declared?.rule)!} testId={`obs-alert-explore-${a.rule}`}>
                      <LineChart className="h-3.5 w-3.5" aria-hidden="true" />
                      Explore
                    </ExploreLink>
                  )}
                  <Button
                    variant="outline"
                    size="sm"
                    leftIcon={<BellOff className="h-3.5 w-3.5" />}
                    disabled={readOnly}
                    onClick={() => onSilence(a)}
                    data-testid={`obs-alert-silence-${a.rule}`}
                  >
                    Silence
                  </Button>
                </div>
              </div>
              {expanded && (
                <dl className="grid gap-x-6 gap-y-1 border-t border-border/60 bg-surface-sunken px-12 py-3 text-xs sm:grid-cols-[auto_1fr]" data-testid="obs-alert-details">
                  {Object.keys(a.annotations)
                    .sort()
                    .map((k) => (
                      <AnnotationRow key={`a-${k}`} name={k} value={a.annotations[k]} />
                    ))}
                  {Object.keys(a.labels)
                    .sort()
                    .map((k) => (
                      <AnnotationRow key={`l-${k}`} name={`label ${k}`} value={a.labels[k]} />
                    ))}
                  {a.lastEvalAtMs && <AnnotationRow name="last evaluated" value={formatTimestamp(a.lastEvalAtMs)} />}
                </dl>
              )}
            </li>
          );
        })}
      </ul>
    </Card>
  );
}

function AnnotationRow({ name, value }: { name: string; value: string }) {
  const link = /^https?:\/\//.test(value);
  return (
    <>
      <dt className="text-ink-muted">{name}</dt>
      <dd className="min-w-0 break-words text-ink">
        {link ? (
          <a href={value} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-primary underline-offset-2 hover:underline">
            {value}
            <ExternalLink className="h-3 w-3" aria-hidden="true" />
          </a>
        ) : (
          value
        )}
      </dd>
    </>
  );
}

// ── History ──────────────────────────────────────────────────────────────────

const TRANSITION_TONE: Record<AlertEvent["transition"], string> = {
  firing: "text-danger-ink",
  pending: "text-warning-ink",
  resolved: "text-success-ink",
  inactive: "text-ink-muted",
  removed: "text-ink-muted",
};

function HistoryTab({
  orgId,
  envId,
  inView,
  range,
  envName,
  navigate,
}: {
  orgId: string;
  envId: string | undefined;
  inView: (envId: string) => boolean;
  range: RangePreset;
  envName: (id: string) => string;
  navigate: ObsNavigate;
}) {
  const history = useAlertHistory(orgId, range, envId);
  if (history.isLoading) return <ObsLoadingRows testId="obs-alert-history-loading" />;
  if (history.error) {
    return <ObsError error={history.error} subject="this organization" onRetry={() => void history.refetch()} retrying={history.isFetching} />;
  }
  const events = (history.data ?? []).filter((e) => inView(e.envId));
  if (events.length === 0) {
    return (
      <Frame icon={<ShieldCheck className="h-4 w-4" />} title={`No alert changed state in the ${RANGE_LABELS[range].toLowerCase()}`} testId="obs-alert-history-empty">
        <p>
          Every time an alert starts pending, fires, or resolves it is recorded here.{" "}
          {range !== "7d" && (
            <button type="button" onClick={() => navigate({ range: "7d" })} className="rounded-sm text-primary underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring">
              Look at the last 7 days
            </button>
          )}
        </p>
      </Frame>
    );
  }
  const columns: DataTableColumn<AlertEventRow>[] = [
    {
      key: "when",
      header: "When",
      render: (_, { event }) => <span className="text-xs tabular-nums text-ink-muted" title={formatTimestamp(event.atMs)}>{formatAgo(event.atMs)}</span>,
    },
    {
      key: "change",
      header: "Change",
      render: (_, { event }) => <span className={cn("text-xs font-medium capitalize", TRANSITION_TONE[event.transition])}>{event.transition}</span>,
    },
    { key: "rule", header: "Rule", stackTitle: true, render: (_, { event }) => <span className="font-mono text-xs">{event.rule}</span> },
    { key: "env", header: "Environment", render: (_, { event }) => <span className="font-mono text-xs text-ink-muted">{envName(event.envId)}</span> },
    { key: "labels", header: "Labels", render: (_, { event }) => <LabelChips labels={event.labels} /> },
    {
      key: "value",
      header: "Value",
      numeric: true,
      render: (_, { event }) => <span className="text-xs">{Number.isInteger(event.value) ? event.value : event.value.toPrecision(3)}</span>,
    },
  ];
  return (
    <Card padding="none" className="overflow-hidden" data-testid="obs-alert-history">
      <div className="max-h-[32rem] overflow-auto">
        <DataTable<AlertEventRow>
          flush
          stacked
          compact
          ariaLabel="Alert state changes, newest first"
          columns={columns}
          data={events.map((event, i) => ({ event, key: `${event.fingerprint}-${event.atMs}-${i}` }))}
          getRowKey={({ key }) => key}
          getRowProps={({ event }) => ({ className: "align-top", "data-testid": "obs-alert-event", "data-transition": event.transition })}
        />
      </div>
    </Card>
  );
}

type AlertEventRow = { event: AlertEvent; key: string };

// ── Rules ────────────────────────────────────────────────────────────────────

function RulesTab({
  rules,
  environments,
  firing,
  selected,
  navigate,
}: {
  rules: EnvRule[];
  environments: ObsEnvironment[];
  firing: AlertInstance[];
  selected: string | undefined;
  navigate: ObsNavigate;
}) {
  const off = environments.filter(observabilityOff);
  const open = rules.find((r) => r.key === selected);
  if (rules.length === 0) {
    const env = environments[0]?.name ?? "<env>";
    return (
      <div className="space-y-3">
        <Frame icon={<AlertTriangle className="h-4 w-4" />} title="No alert rules are declared" testId="obs-rules-none">
          <p>
            Rules live in your code, per environment, and arrive with its next deploy. forge adds best-practice defaults (error rate, a
            service going silent, containers killed for memory) to every environment that has services; add your own beside them.
          </p>
          <p>Start from the rule below, then see what your rules would fire right now against the local stack that forge env up runs:</p>
          <CommandLine command="forge obs alerts check" />
          <p>
            <a
              href={DOCS.alerts}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 rounded-sm font-medium text-primary underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              data-testid="obs-rules-none-docs"
            >
              How alerts work
              <ArrowUpRight className="h-3 w-3" aria-hidden="true" />
            </a>
          </p>
        </Frame>
        <CodeSnippet snippet={starterRuleKcl(env)} />
      </div>
    );
  }
  return (
    <div className="grid grid-cols-[minmax(0,1fr)] gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)]">
      <div className="space-y-2">
        {off.length > 0 && (
          <p className="rounded-lg border border-border/60 bg-surface-sunken px-3 py-2 text-xs text-ink-muted" data-testid="obs-rules-observability-off">
            Observability is off for {off.map((e) => e.name).join(", ")}, so nothing is collected there and no rule is evaluated.
          </p>
        )}
        <Card padding="none" data-testid="obs-rules">
          <ul className="divide-y divide-border/60">
            {rules.map(({ env, rule, key }) => {
              const active = firing.filter((a) => a.envId === env.id && a.rule === rule.alert);
              return (
                <li key={key}>
                  <button
                    type="button"
                    onClick={() => navigate({ rule: key }, { replace: true })}
                    aria-current={key === selected ? "true" : undefined}
                    data-testid={`obs-rule-${key}`}
                    className={cn(
                      "flex w-full flex-wrap items-center gap-2 px-4 py-2.5 text-left transition-colors hover:bg-ink/5 focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
                      key === selected && "bg-surface-sunken"
                    )}
                  >
                    <span className="font-mono text-sm text-ink">{rule.alert}</span>
                    <SeverityChip severity={rule.labels.severity ?? "warning"} />
                    <SourceChip rule={rule} />
                    <span className="ml-auto flex items-center gap-2 text-xs text-ink-muted">
                      {active.length > 0 && <span className="font-medium text-danger-ink">{active.length} firing</span>}
                      <span className="font-mono">{env.name}</span>
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>
        </Card>
      </div>
      {open ? (
        <RuleDetail entry={open} active={firing.filter((a) => a.envId === open.env.id && a.rule === open.rule.alert)} />
      ) : (
        <p className="rounded-lg border border-dashed border-border bg-surface-sunken px-4 py-5 text-sm text-ink-muted" data-testid="obs-rule-pick">
          Choose a rule to see its query and the code that declares it.
        </p>
      )}
    </div>
  );
}

function RuleDetail({ entry, active }: { entry: EnvRule; active: AlertInstance[] }) {
  const { rule, env } = entry;
  const exploreHref = useExploreHref();
  const explore = exploreHref?.(alertPivot(rule.expr), { env: env.name, range: "1h" }) ?? null;
  const lead = leadInstance(active);
  return (
    <Card padding="md" data-testid="obs-rule-detail">
      <div className="space-y-4">
        <div className="space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="font-mono text-sm font-semibold text-ink">{rule.alert}</h3>
            <SeverityChip severity={rule.labels.severity ?? "warning"} />
            <SourceChip rule={rule} />
            {explore && (
              <ExploreLink href={explore} testId="obs-rule-explore" className="ml-auto">
                <LineChart className="h-3.5 w-3.5" aria-hidden="true" />
                Explore
              </ExploreLink>
            )}
          </div>
          <p className="text-xs text-ink-muted">
            Declared for <span className="font-mono">{env.name}</span>
            {rule.source === "default" ? ` by forge's default "${rule.defaultId}"` : " by your project"} · holds {rule.for || "5m"} before firing ·
            evaluated every {rule.interval || "1m"}
            {rule.keepFiringFor ? ` · keeps firing ${rule.keepFiringFor} after it clears` : ""}
          </p>
          {lead && (
            <div className="flex flex-wrap items-center gap-2 pt-1" data-testid="obs-rule-ask">
              <span className={cn("text-xs font-medium", lead.state === "firing" ? "text-danger-ink" : "text-warning-ink")}>
                {active.length > 1 ? `${active.length} instances ${lead.state === "firing" ? "firing" : "pending"}` : lead.state === "firing" ? "Firing now" : "Pending now"}
              </span>
              <AskReliantActions subject={alertAskSubject(lead, env.name)} />
            </div>
          )}
        </div>
        <div>
          <h4 className="mb-1 text-xs font-medium uppercase tracking-wide text-ink-muted">Query</h4>
          <pre className="max-h-40 overflow-auto whitespace-pre-wrap break-words rounded-md border border-border/60 bg-surface-sunken p-2 font-mono text-2xs text-ink">
            {rule.expr}
          </pre>
          <p className="mt-1 text-2xs text-ink-muted">Every row it returns is a firing instance; its non-value columns are the labels.</p>
        </div>
        {Object.keys(rule.annotations).length > 0 && (
          <dl className="grid gap-x-4 gap-y-1 text-xs sm:grid-cols-[auto_1fr]">
            {Object.keys(rule.annotations)
              .sort()
              .map((k) => (
                <AnnotationRow key={k} name={k} value={rule.annotations[k]} />
              ))}
          </dl>
        )}
        <div className="space-y-3 border-t border-border/60 pt-3" data-testid="obs-rule-as-code">
          <h4 className="flex items-center gap-1.5 text-sm font-medium text-ink">
            <Code2 className="h-4 w-4" aria-hidden="true" />
            Edit as code
          </h4>
          <p className="text-xs text-ink-muted">
            Rules are declared in your repository and take effect with the environment&apos;s next deploy. There is nothing to change here.
          </p>
          {ruleAsCode(rule, env.name).map((snippet) => (
            <CodeSnippet key={snippet.title} snippet={snippet} />
          ))}
        </div>
      </div>
    </Card>
  );
}

function CodeSnippet({ snippet }: { snippet: RuleAsCode }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="space-y-1" data-testid="obs-code-snippet">
      <div className="flex items-center justify-between gap-2">
        <p className="text-xs text-ink">
          {snippet.title} — in <code className="font-mono text-ink-muted">{snippet.path}</code>
        </p>
        <Button
          variant="ghost"
          size="xs"
          leftIcon={copied ? <CheckCircle2 className="h-3 w-3" /> : <Copy className="h-3 w-3" />}
          onClick={() => {
            void navigator.clipboard?.writeText(snippet.kcl).then(
              () => setCopied(true),
              () => setCopied(false)
            );
          }}
        >
          {copied ? "Copied" : "Copy"}
        </Button>
      </div>
      <pre className="max-h-72 overflow-auto rounded-md border border-border/60 bg-surface-sunken p-3 font-mono text-2xs leading-relaxed text-ink" tabIndex={0}>
        {snippet.kcl}
      </pre>
    </div>
  );
}
