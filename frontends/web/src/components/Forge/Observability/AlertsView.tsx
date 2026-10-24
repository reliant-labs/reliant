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
import { AlertTriangle, BellOff, CheckCircle2, ChevronDown, ChevronRight, Code2, Copy, ExternalLink, LineChart, Lock, ShieldCheck } from "lucide-react";

import Card from "@/components/forge-ui/card";
import { Button } from "@/components/ui/Button";
import { useAlertHistory, useAlerts } from "@/hooks/observability-queries";
import type { AlertEvent, AlertInstance, DeclaredAlertRule } from "@/services/controlPlane/observability/alertingTypes";
import { alertingErrorKind } from "@/services/controlPlane/observability/errors";
import { RANGE_LABELS, type RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { AlertReceivers } from "./AlertReceivers";
import { AlertSilences, SilenceDialog } from "./AlertSilences";
import { ruleAsCode, starterRuleKcl, type RuleAsCode } from "./alertsAsCode";
import { formatAgo, formatTimestamp } from "./format";
import { alertPivot, alertRange, useExploreHref } from "./Metrics/pivots";
import { placeEnvironments, type ObsEnvironment, type ObsPlace } from "./obsPlace";
import { ALERTS_TABS, type AlertsTab, type ObsNavigate } from "./obsSearch";
import { ExploreLink } from "./Metrics/ExploreLink";
import { ObsError, ObsLoadingRows } from "./ObsStates";
import { StackedTable } from "@/components/Forge/StackedTable";

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
        <div role="tablist" aria-label="Alerts" className="inline-flex flex-wrap rounded-lg border border-border bg-background p-0.5">
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
                id === tab ? "bg-card font-medium text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground"
              )}
            >
              {ALERTS_TAB_LABELS[id]}
              {count[id] ? (
                <span
                  className={cn(
                    "rounded-full px-1.5 text-2xs tabular-nums",
                    id === "firing" ? "bg-destructive/15 text-destructive" : "bg-border/60 text-muted-foreground"
                  )}
                >
                  {count[id]}
                </span>
              ) : null}
            </button>
          ))}
        </div>
        {readOnly && (
          <p className="inline-flex items-center gap-1.5 text-xs text-muted-foreground" role="status" data-testid="obs-alerts-read-only">
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

// ── States ───────────────────────────────────────────────────────────────────

function Frame({ icon, title, children, testId, tone = "neutral" }: { icon: ReactNode; title: string; children?: ReactNode; testId: string; tone?: "neutral" | "good" }) {
  return (
    <div data-testid={testId} role="status" className="flex items-start gap-3 rounded-lg border border-dashed border-border bg-background px-4 py-5">
      <span className={cn("mt-0.5 shrink-0", tone === "good" ? "text-success" : "text-muted-foreground")} aria-hidden="true">
        {icon}
      </span>
      <div className="min-w-0 space-y-1.5">
        <p className="text-sm font-medium text-foreground">{title}</p>
        {children && <div className="space-y-1.5 text-xs text-muted-foreground">{children}</div>}
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
  critical: "bg-destructive/10 text-destructive ring-destructive/30",
  warning: "bg-warning/10 text-warning ring-warning/30",
  info: "text-muted-foreground ring-border",
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
      className="rounded border border-border px-1.5 py-px text-2xs text-muted-foreground"
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
        <span key={k} className="rounded bg-background px-1.5 py-px font-mono text-2xs text-muted-foreground ring-1 ring-inset ring-border/60">
          {k}=<span className="text-foreground">{labels[k]}</span>
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
                  className="mt-0.5 rounded p-0.5 text-muted-foreground hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  {expanded ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
                </button>
                <div className="min-w-0 flex-1 space-y-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span
                      className={cn("inline-flex items-center gap-1 text-xs font-medium", a.state === "firing" ? "text-destructive" : "text-warning")}
                    >
                      <span className={cn("h-2 w-2 rounded-full", a.state === "firing" ? "bg-destructive" : "bg-warning")} aria-hidden="true" />
                      {a.state === "firing" ? "Firing" : "Pending"}
                    </span>
                    <span className="font-mono text-sm font-medium text-foreground">{a.rule}</span>
                    <SeverityChip severity={a.labels.severity ?? ""} />
                    <span className="font-mono text-xs text-muted-foreground">{envName(a.envId)}</span>
                    <SourceChip rule={declared?.rule} />
                  </div>
                  {summary && <p className="text-sm text-foreground">{summary}</p>}
                  <p className="text-xs text-muted-foreground">
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
                <dl className="grid gap-x-6 gap-y-1 border-t border-border/60 bg-background px-12 py-3 text-xs sm:grid-cols-[auto_1fr]" data-testid="obs-alert-details">
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
      <dt className="text-muted-foreground">{name}</dt>
      <dd className="min-w-0 break-words text-foreground">
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
  firing: "text-destructive",
  pending: "text-warning",
  resolved: "text-success",
  inactive: "text-muted-foreground",
  removed: "text-muted-foreground",
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
  return (
    <Card padding="none" data-testid="obs-alert-history">
      <div className="max-h-[32rem] overflow-auto">
        <StackedTable className="w-full text-sm">
          <caption className="sr-only">Alert state changes, newest first</caption>
          <thead className="sticky top-0 bg-card">
            <tr className="border-b border-border text-left text-xs font-medium uppercase tracking-wide text-muted-foreground">
              <th scope="col" className="px-4 py-2 font-medium">When</th>
              <th scope="col" className="px-4 py-2 font-medium">Change</th>
              <th scope="col" className="px-4 py-2 font-medium">Rule</th>
              <th scope="col" className="px-4 py-2 font-medium">Environment</th>
              <th scope="col" className="px-4 py-2 font-medium">Labels</th>
              <th scope="col" className="px-4 py-2 text-right font-medium">Value</th>
            </tr>
          </thead>
          <tbody>
            {events.map((e, i) => (
              <tr key={`${e.fingerprint}-${e.atMs}-${i}`} className="border-b border-border/50 align-top last:border-b-0" data-testid="obs-alert-event" data-transition={e.transition}>
                <td className="whitespace-nowrap px-4 py-1.5 text-xs tabular-nums text-muted-foreground" title={formatTimestamp(e.atMs)}>
                  {formatAgo(e.atMs)}
                </td>
                <td className={cn("px-4 py-1.5 text-xs font-medium capitalize", TRANSITION_TONE[e.transition])}>{e.transition}</td>
                <td className="px-4 py-1.5 font-mono text-xs text-foreground">{e.rule}</td>
                <td className="px-4 py-1.5 font-mono text-xs text-muted-foreground">{envName(e.envId)}</td>
                <td className="px-4 py-1.5">
                  <LabelChips labels={e.labels} />
                </td>
                <td className="px-4 py-1.5 text-right text-xs tabular-nums">{Number.isInteger(e.value) ? e.value : e.value.toPrecision(3)}</td>
              </tr>
            ))}
          </tbody>
        </StackedTable>
      </div>
    </Card>
  );
}

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
        </Frame>
        <CodeSnippet snippet={starterRuleKcl(env)} />
      </div>
    );
  }
  return (
    <div className="grid grid-cols-[minmax(0,1fr)] gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)]">
      <div className="space-y-2">
        {off.length > 0 && (
          <p className="rounded-lg border border-border/60 bg-background px-3 py-2 text-xs text-muted-foreground" data-testid="obs-rules-observability-off">
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
                      "flex w-full flex-wrap items-center gap-2 px-4 py-2.5 text-left transition-colors hover:bg-muted/50 focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
                      key === selected && "bg-muted/60"
                    )}
                  >
                    <span className="font-mono text-sm text-foreground">{rule.alert}</span>
                    <SeverityChip severity={rule.labels.severity ?? "warning"} />
                    <SourceChip rule={rule} />
                    <span className="ml-auto flex items-center gap-2 text-xs text-muted-foreground">
                      {active.length > 0 && <span className="font-medium text-destructive">{active.length} firing</span>}
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
        <RuleDetail entry={open} />
      ) : (
        <p className="rounded-lg border border-dashed border-border bg-background px-4 py-5 text-sm text-muted-foreground" data-testid="obs-rule-pick">
          Choose a rule to see its query and the code that declares it.
        </p>
      )}
    </div>
  );
}

function RuleDetail({ entry }: { entry: EnvRule }) {
  const { rule, env } = entry;
  const exploreHref = useExploreHref();
  const explore = exploreHref?.(alertPivot(rule.expr), { env: env.name, range: "1h" }) ?? null;
  return (
    <Card padding="md" data-testid="obs-rule-detail">
      <div className="space-y-4">
        <div className="space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="font-mono text-sm font-semibold text-foreground">{rule.alert}</h3>
            <SeverityChip severity={rule.labels.severity ?? "warning"} />
            <SourceChip rule={rule} />
            {explore && (
              <ExploreLink href={explore} testId="obs-rule-explore" className="ml-auto">
                <LineChart className="h-3.5 w-3.5" aria-hidden="true" />
                Explore
              </ExploreLink>
            )}
          </div>
          <p className="text-xs text-muted-foreground">
            Declared for <span className="font-mono">{env.name}</span>
            {rule.source === "default" ? ` by forge's default "${rule.defaultId}"` : " by your project"} · holds {rule.for || "5m"} before firing ·
            evaluated every {rule.interval || "1m"}
            {rule.keepFiringFor ? ` · keeps firing ${rule.keepFiringFor} after it clears` : ""}
          </p>
        </div>
        <div>
          <h4 className="mb-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">Query</h4>
          <pre className="max-h-40 overflow-auto whitespace-pre-wrap break-words rounded-md border border-border/60 bg-background p-2 font-mono text-2xs text-foreground">
            {rule.expr}
          </pre>
          <p className="mt-1 text-2xs text-muted-foreground">Every row it returns is a firing instance; its non-value columns are the labels.</p>
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
          <h4 className="flex items-center gap-1.5 text-sm font-medium text-foreground">
            <Code2 className="h-4 w-4" aria-hidden="true" />
            Edit as code
          </h4>
          <p className="text-xs text-muted-foreground">
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
        <p className="text-xs text-foreground">
          {snippet.title} — in <code className="font-mono text-muted-foreground">{snippet.path}</code>
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
      <pre className="max-h-72 overflow-auto rounded-md border border-border/60 bg-background p-3 font-mono text-2xs leading-relaxed text-foreground" tabIndex={0}>
        {snippet.kcl}
      </pre>
    </div>
  );
}
