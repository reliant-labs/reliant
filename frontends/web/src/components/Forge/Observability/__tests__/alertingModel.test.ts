// Copyright (c) 2025 Reliant Labs

/**
 * The Alerts area's pure rules: the KCL it hands a reader ("edit as code"),
 * which environments route to which receiver, what a receiver or a silence
 * needs before it is sent, and how matchers read.
 */

import { describe, expect, it } from "vitest";

import type { DeclaredAlertRule } from "@/services/controlPlane/observability/alertingTypes";

import { DEFAULT_ROUTES, effectiveRoutes, formSettings, receiverProblems, receiverRouting, settingsSummary, type ReceiverForm } from "../AlertReceivers";
import { matcherOp, matcherText, silenceProblems } from "../AlertSilences";
import { alertRuleKcl, kclDict, kclString, ruleAsCode, starterRuleKcl } from "../alertsAsCode";
import { declaredRules, silenceMatchersFor } from "../AlertsView";
import type { ObsEnvironment } from "../obsPlace";

const rule = (patch: Partial<DeclaredAlertRule> = {}): DeclaredAlertRule => ({
  alert: "CheckoutSlow",
  expr: "SELECT 1 AS value",
  for: "5m",
  interval: "1m",
  keepFiringFor: "",
  labels: {},
  annotations: {},
  source: "user",
  defaultId: "",
  ...patch,
});

describe("edit as code", () => {
  it("writes KCL string literals: quotes, backslashes, newlines and a literal ${ escaped", () => {
    expect(kclString('say "hi"')).toBe('"say \\"hi\\""');
    expect(kclString("a\\b")).toBe('"a\\\\b"');
    expect(kclString("one\ntwo")).toBe('"one\\ntwo"');
    // KCL interpolates ${…}; a template's own ${ must stay literal.
    expect(kclString("cost ${value}")).toBe('"cost \\${value}"');
    // Prometheus-style templates have no ${ and pass through.
    expect(kclString("{{ $labels.service }}")).toBe('"{{ $labels.service }}"');
  });

  it("quotes a dict key that is not an identifier, and sorts keys", () => {
    expect(kclDict({ "team.name": "payments", severity: "critical" })).toBe('{severity = "critical", "team.name" = "payments"}');
  });

  it("leaves out fields at their defaults, and quotes the `for` keyword", () => {
    expect(alertRuleKcl(rule())).toBe('forge.AlertRule {\n    alert = "CheckoutSlow"\n    expr = "SELECT 1 AS value"\n}');
    const tuned = alertRuleKcl(rule({ for: "10m", interval: "30s", keepFiringFor: "5m", labels: { severity: "critical" } }));
    expect(tuned).toContain('"for" = "10m"');
    expect(tuned).toContain('interval = "30s"');
    expect(tuned).toContain('keep_firing_for = "5m"');
    expect(tuned).toContain('labels = {severity = "critical"}');
  });

  it("gives a user rule one snippet and a forge default two, all in the env's main.k", () => {
    const own = ruleAsCode(rule(), "prod");
    expect(own).toHaveLength(1);
    expect(own[0].path).toBe("deploy/kcl/prod/main.k");
    expect(own[0].kcl).toMatch(/^observability = forge\.Observability \{\n {4}alerts = forge\.Alerts \{\n {8}rules = \[/);
    const fromForge = ruleAsCode(rule({ source: "default", defaultId: "service-silent", alert: "ServiceSilent", for: "15m" }), "staging");
    expect(fromForge.map((s) => s.kcl)).toEqual([
      expect.stringContaining('"service-silent": forge.AlertDefault {enabled = False}'),
      expect.stringContaining('"for" = "15m"'),
    ]);
  });

  it("offers a first rule for an environment that has none", () => {
    expect(starterRuleKcl("dev")).toMatchObject({ path: "deploy/kcl/dev/main.k", kcl: expect.stringContaining('alert = "CheckoutErrorsHigh"') });
  });
});

const env = (name: string, declared: ObsEnvironment["declared"]): ObsEnvironment => ({ id: `env-${name}`, name, declared });

describe("routing", () => {
  const prod = env("prod", { alerts: [rule()], alertRoutes: [{ match: { severity: "critical" }, receiver: "pager", continue: true }, { match: {}, receiver: "reliant-inapp", continue: false }], observability: null });
  const staging = env("staging", { alerts: [rule()], alertRoutes: [], observability: null });
  const off = env("dev", { alerts: [rule()], alertRoutes: [], observability: { enabled: false, disabled: [] } });
  const unrecorded = env("qa", null);
  const ruleless = env("demo", { alerts: [], alertRoutes: [], observability: null });

  it("uses an env's declared routes, forge's pair when it declares rules but no routes, and nothing otherwise", () => {
    expect(effectiveRoutes(prod).map((r) => r.receiver)).toEqual(["pager", "reliant-inapp"]);
    expect(effectiveRoutes(staging)).toBe(DEFAULT_ROUTES);
    expect(DEFAULT_ROUTES.map((r) => r.receiver)).toEqual(["reliant-inapp", "slack"]);
    expect(effectiveRoutes(off)).toEqual([]);
    expect(effectiveRoutes(unrecorded)).toEqual([]);
    expect(effectiveRoutes(ruleless)).toEqual([]);
  });

  it("maps each receiver name to the environments that route to it, once each", () => {
    const routing = receiverRouting([prod, staging, off, unrecorded]);
    expect(routing.get("reliant-inapp")?.map((e) => e.name)).toEqual(["prod", "staging"]);
    expect(routing.get("pager")?.map((e) => e.name)).toEqual(["prod"]);
    expect(routing.get("slack")?.map((e) => e.name)).toEqual(["staging"]);
  });

  it("lists every env's rules keyed env/rule", () => {
    expect(declaredRules([prod, unrecorded]).map((r) => r.key)).toEqual(["prod/CheckoutSlow"]);
  });
});

describe("receiver forms", () => {
  const form = (patch: Partial<ReceiverForm>): ReceiverForm => ({ name: "oncall", type: "slack", target: "", secret: "https://hooks.slack.com/services/x", ...patch });

  it("checks the server's name pattern and refuses a taken name", () => {
    expect(receiverProblems(form({}), "create")).toEqual([]);
    expect(receiverProblems(form({ name: "-bad" }), "create").join(" ")).toContain("up to 63 letters");
    expect(receiverProblems(form({ name: "reliant-inapp" }), "create").join(" ")).toContain("already exists");
    expect(receiverProblems(form({ name: "team" }), "create", ["team"]).join(" ")).toContain("already exists");
    // A name is fixed once made.
    expect(receiverProblems(form({ name: "-bad" }), "edit")).toEqual([]);
  });

  it.each<[string, Partial<ReceiverForm>, string]>([
    ["slack without its webhook", { secret: "" }, "Paste the Slack incoming-webhook URL"],
    ["slack over http", { secret: "http://hooks.slack.com/x" }, "https URL"],
    ["pagerduty without its key", { type: "pagerduty", secret: "" }, "routing key"],
    ["a webhook over http", { type: "webhook", target: "http://example.com/hook", secret: "" }, "public host"],
    ["a webhook to a cluster host", { type: "webhook", target: "https://alerts.default.svc.cluster.local/x", secret: "" }, "public host"],
    ["a webhook to localhost", { type: "webhook", target: "https://localhost/x", secret: "" }, "public host"],
    ["email with no address", { type: "email", target: "", secret: "" }, "at least one address"],
    ["email with a bad address", { type: "email", target: "ops@example.com, nope", secret: "" }, "Separate addresses"],
  ])("refuses %s", (_name, patch, message) => {
    expect(receiverProblems(form(patch), "create").join(" ")).toContain(message);
  });

  it("lets an edit keep the stored secret by leaving it empty", () => {
    expect(receiverProblems(form({ secret: "" }), "edit")).toEqual([]);
    expect(receiverProblems(form({ type: "pagerduty", secret: "" }), "edit")).toEqual([]);
  });

  it("writes the target under its type's key and keeps settings it does not own", () => {
    expect(formSettings(form({ target: " #alerts " }))).toEqual({ channel: "#alerts" });
    expect(formSettings(form({ type: "email", target: "a@example.com" }), { to: "old@example.com", extra: "kept" })).toEqual({ to: "a@example.com", extra: "kept" });
    expect(formSettings(form({ target: "" }), { channel: "#old" })).toEqual({});
    expect(formSettings(form({ type: "pagerduty" }), { other: "x" })).toEqual({ other: "x" });
  });

  it("summarizes each type's destination", () => {
    expect(settingsSummary({ type: "slack", settings: {} })).toBe("the webhook's default channel");
    expect(settingsSummary({ type: "webhook", settings: { url: "https://x.example.com" } })).toBe("https://x.example.com");
    expect(settingsSummary({ type: "inapp", settings: {} })).toContain("Reliant console");
  });
});

describe("silences", () => {
  it("spells each matcher operator as Alertmanager does", () => {
    expect(matcherOp({ isEqual: true, isRegex: false })).toBe("=");
    expect(matcherOp({ isEqual: false, isRegex: false })).toBe("!=");
    expect(matcherOp({ isEqual: true, isRegex: true })).toBe("=~");
    expect(matcherOp({ isEqual: false, isRegex: true })).toBe("!~");
    expect(matcherText({ name: "service", value: "api|web", isEqual: true, isRegex: true })).toBe('service=~"api|web"');
  });

  it("silences exactly one instance: its rule and own labels, never a reliant_* one", () => {
    expect(silenceMatchersFor({ rule: "R", labels: { service: "api", reliant_env_id: "e", "bad-name": "x" } })).toEqual([
      { name: "alertname", value: "R", isRegex: false, isEqual: true },
      { name: "service", value: "api", isRegex: false, isEqual: true },
    ]);
  });

  it("refuses a silence that could mute everything, names a reliant_* label, says nothing, or is already over", () => {
    const now = Date.UTC(2026, 0, 1);
    expect(silenceProblems({ matchers: [], endsAtMs: now + 1, comment: "x" }, now).join(" ")).toContain("at least one matcher");
    expect(silenceProblems({ matchers: [{ name: "reliant_org_id", value: "o", isEqual: true, isRegex: false }], endsAtMs: now + 1, comment: "x" }, now).join(" ")).toContain("set by Reliant");
    expect(silenceProblems({ matchers: [{ name: "a", value: "b", isEqual: true, isRegex: false }], endsAtMs: now + 1, comment: " " }, now).join(" ")).toContain("Say why");
    expect(silenceProblems({ matchers: [{ name: "a", value: "b", isEqual: true, isRegex: false }], endsAtMs: now, comment: "x" }, now).join(" ")).toContain("future");
  });
});
