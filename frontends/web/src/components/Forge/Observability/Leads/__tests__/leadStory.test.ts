// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";

import type { Issue } from "@/services/controlPlane/observability/issuesTypes";
import type { SessionSummary } from "@/services/controlPlane/observability/rumTypes";
import type { Span, TraceSummary } from "@/services/controlPlane/observability/types";

import {
  errorsSummary,
  issueStory,
  leadIssue,
  median,
  overviewSummary,
  sessionStory,
  sessionsByInterest,
  sessionsSummary,
  slowStepOf,
  stepsOf,
  traceStory,
  tracesSummary,
} from "../leadStory";

const NOW = Date.UTC(2026, 10, 13, 12);
const HOUR = 3_600_000;

const issue = (over: Partial<Issue>): Issue =>
  ({
    id: "i1",
    platform: "server",
    envName: "prod",
    service: "api",
    title: "boom",
    culprit: "",
    exceptionType: "",
    status: "unresolved",
    firstSeenMs: NOW - HOUR,
    firstVersion: "",
    lastSeenMs: NOW,
    events: 1,
    affected: 1,
    resolvedVersion: "",
    assignee: null,
    ...over,
  }) as Issue;

const trace = (over: Partial<TraceSummary>): TraceSummary => ({ traceId: "t", rootOperation: "GET /", rootService: "api", durationMs: 100, errorCount: 0, spanCount: 1, startMs: NOW, ...over }) as TraceSummary;

const span = (over: Partial<Span>): Span => ({ spanId: "s", parentSpanId: "", name: "root", service: "api", startMs: 0, durationMs: 100, status: "ok", statusMessage: "", ...over }) as Span;

const session = (over: Partial<SessionSummary>): SessionSummary =>
  ({ sessionId: "s", userId: "", entryPath: "/", startedMs: NOW - 60_000, endedMs: NOW, pageViews: 1, errors: 0, hasReplay: true, envName: "prod", ...over }) as SessionSummary;

describe("errorsSummary", () => {
  it("says nothing is open, in the scope, and what was resolved", () => {
    expect(errorsSummary({ open: [], resolved: [], env: "prod", more: false })).toEqual({ tone: "good", text: "No open issues in prod." });
    expect(errorsSummary({ open: [], resolved: [issue({})], env: null, more: false }).text).toBe("No open issues across every environment. 1 issue resolved in the last 7 days.");
  });

  it("counts open issues, the new and the regressed, and says when the count is a lower bound", () => {
    const open = [issue({ status: "new" }), issue({ status: "regressed" }), issue({})];
    expect(errorsSummary({ open, resolved: [], env: "prod", more: false })).toEqual({ tone: "bad", text: "3 open issues in prod, 1 new and 1 back after a fix." });
    expect(errorsSummary({ open: [issue({})], resolved: [], env: "prod", more: true }).text).toBe("At least 1 open issue in prod.");
  });
});

describe("leadIssue", () => {
  it("is the newest to appear, null for none", () => {
    expect(leadIssue([])).toBeNull();
    expect(leadIssue([issue({ id: "old", firstSeenMs: 1 }), issue({ id: "new", firstSeenMs: 2 })])?.id).toBe("new");
  });
});

describe("issueStory", () => {
  it("says what was raised, where and since when", () => {
    expect(issueStory(issue({ exceptionType: "TimeoutError", culprit: "charge in pay.go", firstVersion: "v2" }), NOW)).toBe(
      "TimeoutError raised in charge in pay.go. It first happened 1h ago, in v2."
    );
  });

  it("falls back to the service, notes a regression, and has no sentence without a type or a place", () => {
    expect(issueStory(issue({ culprit: "f in x.ts", status: "regressed", resolvedVersion: "v1", firstSeenMs: 0 }), NOW)).toBe(
      "An error raised in f in x.ts. It came back after it was marked resolved in v1."
    );
    expect(issueStory(issue({ exceptionType: "E", firstSeenMs: 0 }), NOW)).toBe("E raised in api.");
    expect(issueStory(issue({}), NOW)).toBeNull();
  });
});

describe("tracesSummary", () => {
  it("has a sentence for no requests and for one", () => {
    expect(tracesSummary({ traces: [], window: "last hour", more: false })).toEqual({ tone: "good", text: "No requests recorded in the last hour." });
    expect(tracesSummary({ traces: [trace({ durationMs: 1500 })], window: "last hour", more: false })).toEqual({ tone: "warn", text: "1 request in the last hour. It took 1.5s." });
  });

  it("gives the median, names the slowest, counts failures, and says when the page is the newest only", () => {
    const traces = [trace({ durationMs: 10 }), trace({ durationMs: 30 }), trace({ durationMs: 900, rootOperation: "orders.v1.OrdersService/CreateOrder", errorCount: 1 })];
    expect(tracesSummary({ traces, window: "last hour", more: true })).toEqual({
      tone: "bad",
      text: "The newest 3 requests in the last hour. Half answered in under 30ms; the slowest, orders.v1.OrdersService/CreateOrder, took 900ms. 1 request ended in an error.",
    });
  });

  it("names an unnamed request plainly", () => {
    expect(tracesSummary({ traces: [trace({ rootOperation: "" }), trace({ rootOperation: "", durationMs: 200 })], window: "last hour", more: false }).text).toContain("the slowest, one with no name, took 200ms");
  });
});

describe("median", () => {
  it("is the middle value, the mean of the middle two, and 0 for none", () => {
    expect(median([])).toBe(0);
    expect(median([3, 1, 2])).toBe(2);
    expect(median([1, 2, 3, 4])).toBe(2.5);
  });
});

describe("trace steps", () => {
  const spans = [
    span({ spanId: "r", name: "CreateOrder", durationMs: 1000 }),
    span({ spanId: "a", parentSpanId: "r", name: "charge", service: "payments", startMs: 10, durationMs: 800, status: "error", statusMessage: "timeout" }),
    span({ spanId: "b", parentSpanId: "r", name: "SELECT", startMs: 5, durationMs: 50 }),
  ];

  it("orders steps as a timeline, a span whose parent is missing a root", () => {
    expect(stepsOf(spans).map((step) => [step.span.spanId, step.depth])).toEqual([["r", 0], ["b", 1], ["a", 1]]);
    expect(stepsOf([span({ spanId: "x", parentSpanId: "gone" })])[0].depth).toBe(0);
  });

  it("survives a span that names itself its parent", () => {
    expect(stepsOf([span({ spanId: "x", parentSpanId: "x" })])).toHaveLength(1);
  });

  it("finds the slow step only when it holds a fifth of the request", () => {
    expect(slowStepOf(stepsOf(spans))?.span.spanId).toBe("a");
    expect(slowStepOf(stepsOf([span({ spanId: "r", durationMs: 1000 }), span({ spanId: "c", parentSpanId: "r", durationMs: 100 })]))).toBeNull();
  });

  it("tells the trace in a sentence, and has none for no spans", () => {
    expect(traceStory(spans)).toBe("CreateOrder took 1s. The longest step, 80% of it, was charge in payments. It failed in payments: “timeout”.");
    expect(traceStory([span({ name: "ping", durationMs: 2 })])).toBe("ping took 2ms.");
    expect(traceStory([])).toBeNull();
  });
});

describe("sessions", () => {
  it("puts visits with errors first, then the newest", () => {
    const rows = sessionsByInterest([session({ sessionId: "a", startedMs: 3 }), session({ sessionId: "b", errors: 1, startedMs: 1 }), session({ sessionId: "c", startedMs: 2 })]);
    expect(rows.map((row) => row.sessionId)).toEqual(["b", "a", "c"]);
  });

  it("summarises none, some and all with errors", () => {
    expect(sessionsSummary({ sessions: [], window: "last 24 hours", more: false }).text).toBe("No visits recorded in the last 24 hours.");
    expect(sessionsSummary({ sessions: [session({}), session({ errors: 2 })], window: "last 24 hours", more: true })).toEqual({
      tone: "warn",
      text: "The newest 2 visits recorded in the last 24 hours. 1 visit hit an error; its replay is marked.",
    });
    expect(sessionsSummary({ sessions: [session({ errors: 1 }), session({ errors: 1 })], window: "last 24 hours", more: false }).text).toContain("Every one hit an error; their replays are marked.");
    expect(sessionsSummary({ sessions: [session({})], window: "last 24 hours", more: false }).text).toBe("1 visit recorded in the last 24 hours. None hit an error.");
  });

  it("tells one visit, signed in or not", () => {
    expect(sessionStory(session({ userId: "u1", entryPath: "/cart", pageViews: 3, errors: 1 }))).toBe("User u1 started on /cart, viewed 3 pages over 1m and hit 1 error.");
    expect(sessionStory(session({ entryPath: "", pageViews: 0, startedMs: NOW, endedMs: NOW }))).toBe("A signed-out visitor started on /, with no errors.");
  });
});

describe("overviewSummary", () => {
  it("is unknown while the items are, and says nothing needs attention", () => {
    expect(overviewSummary(null, "prod")).toBeNull();
    expect(overviewSummary([], "prod")).toEqual({ tone: "good", text: "Nothing needs attention in prod." });
  });

  it("names the one thing, or counts them and names the first, with its environment across all", () => {
    expect(overviewSummary([{ kind: "alert", subject: "HighErrors", what: "firing", envName: "prod" }], "prod")).toEqual({ tone: "bad", text: "One thing needs attention in prod: HighErrors firing." });
    expect(
      overviewSummary(
        [
          { kind: "workload", subject: "api", what: "crash-looping", envName: "staging" },
          { kind: "job", subject: "migrate", what: "failed", envName: "prod" },
        ],
        null
      )
    ).toEqual({ tone: "warn", text: "2 things need attention across every environment, first api crash-looping in staging." });
  });
});
