// Copyright (c) 2026 Reliant Labs

/**
 * The led list pages on real projects' edges, not the demo's sample shop:
 * nothing recorded, a single item, long names, a failed read. Each must say
 * something true, draw no empty card, and keep its way to the full list.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { Issue } from "@/services/controlPlane/observability/issuesTypes";
import type { SessionSummary } from "@/services/controlPlane/observability/rumTypes";
import type { ObsScope, TraceSummary } from "@/services/controlPlane/observability/types";

import { createFixtureApi } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import { ErrorsLead, SessionsLead, TracesLead, type LeadHref } from "../ListLeads";

const SCOPE: ObsScope = { orgId: "org", projectId: "app", envIds: ["env-prod"], filters: [] };
const hrefOf: LeadHref = (view, search) => `/forge/p/app/observability/${view}?${new URLSearchParams(search as Record<string, string>)}`;
const NOW = Date.now();
const LONG = `orders.v1.OrdersService/${"VeryLongMethodName".repeat(12)}`;

/** The fixture API with the list reads answered by `over`. */
const api = (over: Partial<ObservabilityApi>): ObservabilityApi => ({ ...createFixtureApi(), ...over });

const issue = (over: Partial<Issue>): Issue =>
  ({
    id: "iss-1",
    platform: "server",
    envId: "env-prod",
    envName: "prod",
    service: "api",
    title: "boom",
    culprit: "",
    exceptionType: "",
    status: "unresolved",
    firstSeenMs: NOW - 3_600_000,
    firstVersion: "",
    lastSeenMs: NOW,
    events: 3,
    affected: 2,
    trend: [],
    resolvedAtMs: 0,
    resolvedVersion: "",
    assignee: null,
    ...over,
  }) as Issue;

const trace = (over: Partial<TraceSummary>): TraceSummary =>
  ({ traceId: "t1", rootOperation: "GET /", rootService: "api", startMs: NOW, durationMs: 120, rootDurationMs: 120, spanCount: 3, errorCount: 0, services: ["api"], ...over }) as TraceSummary;

const session = (over: Partial<SessionSummary>): SessionSummary =>
  ({ sessionId: "s1", service: "web", serviceVersion: "", release: "", envId: "env-prod", envName: "prod", userId: "", entryPath: "/", startedMs: NOW - 60_000, endedMs: NOW, pageViews: 1, interactions: 1, errors: 0, hasReplay: false, ...over }) as SessionSummary;

const issues = (open: Issue[], resolved: Issue[] = []): Partial<ObservabilityApi> => ({
  listIssues: async (req) => ({ issues: req.statuses.includes("resolved") ? resolved : open, nextPageToken: "" }),
});

describe("ErrorsLead", () => {
  it("says nothing is open, with no card and no empty table, and keeps All filters", async () => {
    renderWithApi(api(issues([])), <ErrorsLead scope={SCOPE} env="prod" hrefOf={hrefOf} />);
    expect(await screen.findByText("No open issues in prod.")).toBeInTheDocument();
    expect(screen.queryByTestId("lead-featured-issue")).toBeNull();
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.getByTestId("lead-full-list")).toHaveAttribute("href", expect.stringContaining("/observability/errors"));
  });

  it("leads with a single issue as the card, and lists no 'other' issues", async () => {
    renderWithApi(api(issues([issue({ title: LONG, status: "new" })])), <ErrorsLead scope={SCOPE} env="prod" hrefOf={hrefOf} />);
    const card = await screen.findByTestId("lead-featured-issue");
    expect(within(card).getByRole("heading", { name: LONG })).toBeInTheDocument();
    expect(card).toHaveAttribute("data-issue-id", "iss-1");
    expect(screen.getByTestId("lead-summary")).toHaveTextContent("1 open issue in prod, 1 new.");
    expect(screen.queryByRole("table", { name: "Open issues" })).toBeNull();
  });

  it("names each issue's environment only when every environment is in scope", async () => {
    renderWithApi(api(issues([issue({ id: "a" }), issue({ id: "b", envName: "staging", firstSeenMs: 0 })])), <ErrorsLead scope={{ ...SCOPE, envIds: [] }} env={null} hrefOf={hrefOf} />);
    const table = await screen.findByRole("table", { name: "Open issues" });
    expect(table).toHaveTextContent("api · staging");
    expect(screen.getByTestId("lead-summary")).toHaveTextContent("across every environment");
  });

  it("says when the inbox could not be read, and still links to the full list", async () => {
    renderWithApi(api({ listIssues: async () => Promise.reject(new Error("down")) }), <ErrorsLead scope={SCOPE} env="prod" hrefOf={hrefOf} />);
    expect(await screen.findByText(/could not be read/, {}, { timeout: 5000 })).toBeInTheDocument();
    expect(screen.getByTestId("lead-full-list")).toBeInTheDocument();
  });
});

describe("TracesLead", () => {
  it("says no requests were recorded, with no card", async () => {
    renderWithApi(api({ listTraces: async () => ({ traces: [], nextCursor: "", red: null, redGroups: [] }) }), <TracesLead scope={SCOPE} env="prod" hrefOf={hrefOf} />);
    expect(await screen.findByText("No requests recorded in the last hour.")).toBeInTheDocument();
    expect(screen.queryByTestId("lead-slowest-trace")).toBeNull();
  });

  it("leads with a long-named request and lists no other rows for one request", async () => {
    renderWithApi(api({ listTraces: async () => ({ traces: [trace({ rootOperation: LONG, durationMs: 2500 })], nextCursor: "", red: null, redGroups: [] }) }), <TracesLead scope={SCOPE} env="prod" hrefOf={hrefOf} />);
    const card = await screen.findByTestId("lead-slowest-trace");
    expect(within(card).getByRole("heading", { name: LONG })).toBeInTheDocument();
    expect(screen.getByTestId("lead-summary")).toHaveTextContent("1 request in the last hour. It took 2.5s.");
    expect(screen.queryByRole("table")).toBeNull();
  });
});

describe("SessionsLead", () => {
  it("says no visits were recorded (an app with no browser frontend)", async () => {
    renderWithApi(api({ listSessions: async () => ({ sessions: [], nextPageToken: "" }) }), <SessionsLead scope={SCOPE} env="prod" hrefOf={hrefOf} />);
    expect(await screen.findByText("No visits recorded in the last 24 hours.")).toBeInTheDocument();
    expect(screen.queryByTestId("lead-featured-session")).toBeNull();
  });

  it("opens a visit with no replay as a visit, not a replay", async () => {
    renderWithApi(api({ listSessions: async () => ({ sessions: [session({ entryPath: `/${"a".repeat(200)}` })], nextPageToken: "" }) }), <SessionsLead scope={SCOPE} env="prod" hrefOf={hrefOf} />);
    const card = await screen.findByTestId("lead-featured-session");
    expect(card).toHaveTextContent("No replay was recorded.");
    expect(within(card).getByTestId("lead-featured-session-open")).toHaveTextContent("Open the visit");
  });
});
