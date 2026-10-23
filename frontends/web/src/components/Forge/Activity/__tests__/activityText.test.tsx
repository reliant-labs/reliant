// Copyright (c) 2025 Reliant Labs

/**
 * How the activity verbs read: the issue triage verbs control-plane records
 * (resolve, ignore, unresolve), the issues worker's observations
 * (issue_opened, issue_regressed), and query_database — "Ada queried prod DB:
 * <reason>" with rows, duration and the statement's hash, never the SQL.
 */

import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";

import type { ActivityEvent } from "@/services/controlPlane/observability/activityApi";

import { ActivityRow } from "../ActivityFeed";
import { activitySentence, activityText, shortSha } from "../activityText";

const T = Date.UTC(2026, 9, 22, 12);

function event(over: Partial<ActivityEvent>): ActivityEvent {
  return {
    id: "a1",
    atMs: T,
    actor: { id: "u1", name: "Ada", kind: "user" },
    verb: "promote",
    target: { kind: "issue", id: "iss-1", label: "TypeError: x is undefined" },
    env: { id: "env-prod", name: "prod" },
    project: "shop",
    via: "console",
    detail: {},
    ...over,
  };
}

const SHA = "3f9a1c2b7d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8";

describe("query_database", () => {
  const q = event({
    verb: "query_database",
    via: "cli",
    target: { kind: "database", id: "shop", label: "shop" },
    detail: { reason: "checking the CreateOrder timeouts", row_count: "42", duration_ms: "31", sql_sha256: SHA },
  });

  it("says who queried which environment's database and why, with rows, duration and the short hash", () => {
    expect(activitySentence(q)).toBe("Ada queried prod DB: checking the CreateOrder timeouts via CLI");
    expect(activityText(q).facts).toEqual(["shop", "42 rows", "31 ms", "sql 3f9a1c2b"]);
    expect(shortSha(SHA)).toBe("3f9a1c2b");
  });

  it("never shows SQL: a detail it does not know is not rendered", () => {
    const withSql = event({ ...q, detail: { ...q.detail, sql: "select * from secrets" } });
    render(
      <ol>
        <ActivityRow event={withSql} now={T + 60_000} />
      </ol>
    );
    const row = screen.getByTestId("activity-a1");
    expect(row).toHaveAttribute("data-verb", "query_database");
    expect(row).toHaveTextContent("Ada queried prod DB: checking the CreateOrder timeouts");
    expect(within(row).getByTestId("activity-facts")).toHaveTextContent("shop42 rows31 mssql 3f9a1c2bvia CLI");
    expect(row).not.toHaveTextContent("select");
  });

  it("says one row, and a database with no environment by name", () => {
    const one = event({ ...q, env: null, detail: { row_count: "1" } });
    expect(activitySentence(one)).toBe("Ada queried the shop database via CLI");
    expect(activityText(one).facts).toEqual(["1 row"]);
  });
});

describe("issue triage", () => {
  it("phrases a resolve with when it takes effect", () => {
    expect(activitySentence(event({ verb: "resolve", detail: { resolve_in: "next_release", from: "unresolved" } }))).toBe(
      "Ada resolved TypeError: x is undefined in the next release in prod"
    );
    expect(activitySentence(event({ verb: "resolve", detail: { resolve_in: "version", version: "v1.8.4" } }))).toBe(
      "Ada resolved TypeError: x is undefined in v1.8.4 in prod"
    );
    expect(activityText(event({ verb: "resolve", detail: { from: "regressed", resolve_in: "now" } })).action).toBe("resolved the regression");
  });

  it("phrases an ignore with its end, and a reopen with what it was", () => {
    expect(activityText(event({ verb: "ignore", detail: {} })).tail).toBe("until unignored");
    expect(activityText(event({ verb: "ignore", detail: { ignored_until: new Date(T).toISOString() } })).tail).toMatch(/^until /);
    expect(activitySentence(event({ verb: "unresolve", detail: { from: "resolved" } }))).toBe("Ada reopened TypeError: x is undefined (was resolved) in prod");
  });

  it("names the platform's own observations as Reliant", () => {
    const system = { id: "reliant", name: "reliant", kind: "system" as const };
    expect(activitySentence(event({ verb: "issue_opened", actor: system, via: "reliant", detail: { service: "api", platform: "server", version: "v1.8.3" } }))).toBe(
      "Reliant saw a new issue: TypeError: x is undefined in prod"
    );
    const regressed = activityText(event({ verb: "issue_regressed", actor: system, via: "reliant", detail: { service: "web", version: "v1.8.3" } }));
    expect(`${regressed.action} ${regressed.tail}`).toBe("reopened as regressed");
    expect(regressed.facts).toEqual(["web", "seen again in v1.8.3"]);
  });
});

describe("other verbs", () => {
  it("phrases the known ones, and shows an unknown token as words rather than guessing a tense", () => {
    expect(activityText(event({ verb: "promote" })).action).toBe("promoted");
    expect(activityText(event({ verb: "set_run_state" })).action).toBe("set run state");
  });
});
