// Copyright (c) 2025 Reliant Labs

/**
 * "Edit as code" for alert rules: the KCL that declares a rule, for the user to
 * change in their repo.
 *
 * Rules are declarative (decision: no server-side rule editor). An environment's
 * rules are its `forge.Observability.alerts` in `deploy/kcl/<env>/main.k`:
 * the project's own `forge.AlertRule` entries, plus forge's generated defaults,
 * which are tuned or turned off by id through `forge.Alerts.defaults`. They
 * reach the control plane in the env's release shape on the next deploy. So:
 *
 *   a `user` rule     → its forge.AlertRule block, to edit in place;
 *   a `default` rule  → the forge.AlertDefault entry that turns it off, and the
 *                       one that tunes it (labels, hold time).
 *
 * The output is valid KCL: strings are escaped (KCL interpolates `${…}`, so a
 * literal one is written `\${…}`), and a dict key that is not an identifier is
 * quoted.
 */

import type { DeclaredAlertRule } from "@/services/controlPlane/observability/alertingTypes";

/** A KCL string literal. */
export function kclString(value: string): string {
  return `"${value.replace(/\\/g, "\\\\").replace(/"/g, '\\"').replace(/\n/g, "\\n").replace(/\$\{/g, "\\${")}"`;
}

const IDENT = /^[A-Za-z_][A-Za-z0-9_]*$/;

function kclKey(key: string): string {
  return IDENT.test(key) ? key : kclString(key);
}

/** `{severity = "critical", "team.name" = "payments"}`, keys sorted so the snippet is stable. */
export function kclDict(map: Record<string, string>): string {
  const entries = Object.keys(map)
    .sort()
    .map((k) => `${kclKey(k)} = ${kclString(map[k])}`);
  return `{${entries.join(", ")}}`;
}

/** Where an environment's rules live. */
export function kclPath(envName: string): string {
  return `deploy/kcl/${envName}/main.k`;
}

function indent(text: string, by: string): string {
  return text
    .split("\n")
    .map((line) => (line ? by + line : line))
    .join("\n");
}

/** The forge.AlertRule block that declares a rule. Fields left at their defaults (interval 1m) are left out. */
export function alertRuleKcl(rule: DeclaredAlertRule): string {
  const lines = [`alert = ${kclString(rule.alert)}`, `expr = ${kclString(rule.expr)}`];
  if (rule.for && rule.for !== "5m") lines.push(`"for" = ${kclString(rule.for)}`);
  if (rule.interval && rule.interval !== "1m") lines.push(`interval = ${kclString(rule.interval)}`);
  if (rule.keepFiringFor) lines.push(`keep_firing_for = ${kclString(rule.keepFiringFor)}`);
  if (Object.keys(rule.labels).length) lines.push(`labels = ${kclDict(rule.labels)}`);
  if (Object.keys(rule.annotations).length) lines.push(`annotations = ${kclDict(rule.annotations)}`);
  return `forge.AlertRule {\n${indent(lines.join("\n"), "    ")}\n}`;
}

/** Wraps an `alerts = forge.Alerts {…}` body in the env's observability block. */
function inObservability(alertsBody: string): string {
  return [
    "observability = forge.Observability {",
    "    alerts = forge.Alerts {",
    indent(alertsBody, "        "),
    "    }",
    "}",
  ].join("\n");
}

export interface RuleAsCode {
  /** The file to edit. */
  path: string;
  /** What the snippet does, one line. */
  title: string;
  kcl: string;
}

/**
 * The KCL a reader edits for one declared rule, in `envName`. A user rule has
 * one snippet; a default has two (turn it off, tune it).
 */
export function ruleAsCode(rule: DeclaredAlertRule, envName: string): RuleAsCode[] {
  const path = kclPath(envName);
  if (rule.source === "user") {
    return [
      {
        path,
        title: "Edit the rule where it is declared",
        kcl: inObservability(`rules = [\n${indent(alertRuleKcl(rule), "    ")}\n]`),
      },
    ];
  }
  const id = kclString(rule.defaultId || rule.alert);
  const tuned = [
    Object.keys(rule.labels).length ? `labels = ${kclDict(rule.labels)}` : `labels = {severity = "warning"}`,
    `"for" = ${kclString(rule.for || "5m")}`,
  ];
  return [
    {
      path,
      title: "Turn this default off for the environment",
      kcl: inObservability(`defaults = {\n    ${id}: forge.AlertDefault {enabled = False}\n}`),
    },
    {
      path,
      title: "Tune it: labels (severity included) are merged over the default's, `for` replaces its hold time",
      kcl: inObservability(`defaults = {\n    ${id}: forge.AlertDefault {\n${indent(tuned.join("\n"), "        ")}\n    }\n}`),
    },
  ];
}

/** A first rule, for an environment that declares none. */
export function starterRuleKcl(envName: string): RuleAsCode {
  const example: DeclaredAlertRule = {
    alert: "CheckoutErrorsHigh",
    expr: "SELECT ServiceName AS service, countIf(StatusCode = 'Error') / count() AS value FROM otel_traces WHERE SpanKind = 'Server' AND Timestamp > now() - INTERVAL 5 MINUTE GROUP BY service HAVING value > 0.05",
    for: "10m",
    interval: "1m",
    keepFiringFor: "",
    labels: { severity: "critical" },
    annotations: { summary: "{{ $labels.service }} fails {{ $value }} of requests" },
    source: "user",
    defaultId: "",
  };
  return { path: kclPath(envName), title: "Declare a rule", kcl: inObservability(`rules = [\n${indent(alertRuleKcl(example), "    ")}\n]`) };
}
