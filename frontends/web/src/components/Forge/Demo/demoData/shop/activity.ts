// Copyright (c) 2025 Reliant Labs

/**
 * shop's activity feed, once, for both of its readers: the observability
 * views' ObservabilityApi (issues.ts) and the wire's ActivityService
 * (consoleScenario.ts). Every actor is the cast, CI or the platform itself.
 */

import type { ActivityEvent } from "@/services/controlPlane/observability/activityApi";

import { DAY, DEMO_CI_ACTOR, DEMO_INCIDENT, demoPerson, HOUR, MINUTE, RUNNING } from "../shopStory";

const ENV = { dev: { id: "env-dev", name: "dev" }, staging: { id: "env-staging", name: "staging" }, prod: { id: "env-prod", name: "prod" } };

const user = (handle: string): ActivityEvent["actor"] => ({ id: demoPerson(handle)?.id ?? `user-${handle}`, name: handle, kind: "user" });
const CI: ActivityEvent["actor"] = { id: "token-ci", name: DEMO_CI_ACTOR, kind: "machine" };
const PLATFORM: ActivityEvent["actor"] = { id: "reliant", name: "reliant", kind: "system" };

const ISSUE = { kind: "issue", id: DEMO_INCIDENT.issueId, label: DEMO_INCIDENT.error };

/** Newest first. */
export function shopActivity(now: number): ActivityEvent[] {
  const at = (ago: number) => now - ago;
  const events: Omit<ActivityEvent, "project">[] = [
    { id: "act-promote-dev-v184", atMs: at(RUNNING["env-dev"].age), actor: user("maya"), verb: "promote", target: { kind: "release", id: "v1.8.4", label: "v1.8.4" }, env: ENV.dev, via: "cli", detail: { from: "v1.8.3" } },
    {
      id: "act-query-prod",
      atMs: at(18 * MINUTE),
      actor: user("maya"),
      verb: "query_database",
      target: { kind: "database", id: "shop", label: "shop" },
      env: ENV.prod,
      via: "console",
      detail: { reason: "orders left in payment_failed since v1.8.3", row_count: "41", duration_ms: "27", sql_sha256: "3f9a1c2b7d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8" },
    },
    { id: "act-cut-v184", atMs: at(30 * MINUTE), actor: CI, verb: "cut", target: { kind: "release", id: "v1.8.4", label: "v1.8.4" }, env: ENV.dev, via: "ci", detail: {} },
    { id: "act-assign-incident", atMs: at(35 * MINUTE), actor: user("jon"), verb: "assign", target: ISSUE, env: ENV.prod, via: "console", detail: { assignee: "maya" } },
    { id: "act-opened-incident", atMs: at(38 * MINUTE), actor: PLATFORM, verb: "issue_opened", target: ISSUE, env: ENV.prod, via: "reliant", detail: { platform: "server", service: "api", status: "new", version: DEMO_INCIDENT.release } },
    { id: "act-promote-prod-v183", atMs: at(RUNNING["env-prod"].age), actor: user("jon"), verb: "promote", target: { kind: "release", id: "v1.8.3", label: "v1.8.3" }, env: ENV.prod, via: "cli", detail: { from: "v1.8.2" } },
    { id: "act-promote-staging-v183", atMs: at(RUNNING["env-staging"].age), actor: user("jon"), verb: "promote", target: { kind: "release", id: "v1.8.3", label: "v1.8.3" }, env: ENV.staging, via: "cli", detail: { from: "v1.8.2" } },
    { id: "act-cut-v183", atMs: at(26 * HOUR), actor: CI, verb: "cut", target: { kind: "release", id: "v1.8.3", label: "v1.8.3" }, env: ENV.dev, via: "ci", detail: {} },
    { id: "act-rotate-session", atMs: at(3 * DAY), actor: user("priya"), verb: "rotate", target: { kind: "secret", id: "SESSION_SIGNING_KEY", label: "SESSION_SIGNING_KEY" }, env: ENV.prod, via: "cli", detail: {} },
    { id: "act-rotate-webhook", atMs: at(4.6 * DAY), actor: user("priya"), verb: "rotate", target: { kind: "secret", id: "PAYMENTS_WEBHOOK_SECRET", label: "PAYMENTS_WEBHOOK_SECRET" }, env: ENV.staging, via: "cli", detail: {} },
    {
      id: "act-resolve-lock",
      atMs: at(5 * DAY - 2 * HOUR),
      actor: user("priya"),
      verb: "resolve",
      target: { kind: "issue", id: "iss-inventory-lock", label: 'Reserve: could not obtain lock on row in relation "stock"' },
      env: ENV.prod,
      via: "console",
      detail: { platform: "server", service: "inventory", status: "resolved", from: "unresolved", resolve_in: "version", version: "v1.8.2" },
    },
  ];
  return events.map((e) => ({ ...e, project: "shop" })).sort((a, b) => b.atMs - a.atMs);
}
