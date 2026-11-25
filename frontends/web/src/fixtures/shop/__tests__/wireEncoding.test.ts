// Copyright (c) 2025 Reliant Labs

/**
 * Every answer the shop pack's observability handlers give survives the
 * wire: each is encoded to protobuf binary (`toBinary`), as a real Connect
 * transport encodes it. A value the message cannot carry (a negative count
 * in a uint64, a fraction in an int64) throws there, and would break the
 * page that reads it on any transport that serializes.
 *
 * Each read is asked as a page asks it: with no window (the server's
 * default), over the last hour, and over an hour that began after the pack's
 * clock (a tab left open past the window it loaded with). A read that names
 * one thing asks about the story's own.
 */

import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { create, toBinary, type DescMessage, type DescMethod, type DescService } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { ConnectError } from "@connectrpc/connect";

import { AlertingService } from "@/gen/controlplane/services/alerting/v1/alerting_pb";
import { AppDatabaseService } from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import { DashboardService, DashboardSource } from "@/gen/controlplane/services/dashboard/v1/dashboard_pb";
import { DeliveryInsightsService } from "@/gen/controlplane/services/delivery_insights/v1/delivery_insights_pb";
import { IssuesService } from "@/gen/controlplane/services/issues/v1/issues_pb";
import { ObservabilityService } from "@/gen/controlplane/services/observability/v1/observability_pb";
import { RumService } from "@/gen/controlplane/services/rum/v1/rum_pb";
import { UserService } from "@/gen/controlplane/services/user/v1/user_pb";

import { shopScenario } from "..";
import { createShopApi } from "../model/shopApi";
import { DEMO_INCIDENT, DEMO_ORG, demoNow, resetDemoNowForTests } from "../shopStory";
import { isDemoAnswered } from "./reads";

const SERVICES: readonly DescService[] = [ObservabilityService, AlertingService, DeliveryInsightsService, IssuesService, UserService, RumService, DashboardService, AppDatabaseService];
const HOUR = 3_600_000;
const AT = Date.UTC(2026, 10, 18, 15, 30, 0);

/** The story's own ids, for the reads that name one thing. */
async function named(now: number): Promise<Record<string, object>> {
  const story = createShopApi(now);
  const scope = { orgId: DEMO_ORG, projectId: "shop", envIds: ["env-prod"], filters: [] };
  const hour = { startMs: now - HOUR, endMs: now };
  const [session] = (await story.listSessions({ ...scope, window: { startMs: now - 24 * HOUR, endMs: now }, hasError: false, hasReplay: true, userId: "", pathContains: "", pageSize: 1 })).sessions;
  const [metric] = (await story.listMetricCatalog({ ...scope, window: hour })).metrics;
  const ref = { source: DashboardSource.GENERATED, projectId: "shop", key: "overview" };
  const rum = { orgId: DEMO_ORG, projectId: "shop" };
  return {
    GetDashboard: { orgId: DEMO_ORG, ref },
    ListDashboardVersions: { orgId: DEMO_ORG, ref },
    GetIssue: { issueId: DEMO_INCIDENT.issueId },
    ListIssueEvents: { issueId: DEMO_INCIDENT.issueId },
    ListIssueTimeline: { issueId: DEMO_INCIDENT.issueId },
    GetMetric: { metricName: metric.name },
    ListMetricExemplars: { metricName: metric.name },
    GetWorkload: { workload: "api" },
    GetSession: { scope: rum, sessionId: session.sessionId },
    GetSessionTimeline: { scope: rum, sessionId: session.sessionId },
    GetSessionReplay: { scope: rum, sessionId: session.sessionId },
    QueryDatabasePanel: { environmentId: "env-prod", database: "shop", sql: "select state, count(*) from orders group by 1" },
    ListDashboardDatabases: { environmentId: "env-prod" },
  };
}

/** `init` with the request's scope (when it has one) narrowed to prod and to `window`. */
function asked(input: DescMessage, init: Record<string, unknown>, window: { startMs: number; endMs: number } | null) {
  const scope = input.fields.find((f) => f.localName === "scope");
  if (!scope) return create(input, init as never);
  const timeRange = window ? { start: timestampFromMs(window.startMs), end: timestampFromMs(window.endMs) } : undefined;
  const given = (init.scope ?? {}) as Record<string, unknown>;
  return create(input, { ...init, scope: { orgId: DEMO_ORG, projectId: "shop", envIds: ["env-prod"], ...given, timeRange } } as never);
}

describe("the shop pack's observability answers, encoded for the wire", () => {
  beforeAll(() => resetDemoNowForTests(AT));
  afterAll(() => resetDemoNowForTests());

  it("encodes every read's answer, whatever window it asks", async () => {
    const now = demoNow();
    const handlers = shopScenario().handlers as Record<string, (req: unknown) => unknown>;
    const names = await named(now);
    const windows = { "no window": null, "the last hour": { startMs: now - HOUR, endMs: now }, "an hour after the clock": { startMs: now + HOUR, endMs: now + 2 * HOUR } };
    const failures: string[] = [];
    let encoded = 0;
    for (const service of SERVICES) {
      for (const method of service.methods.filter((m: DescMethod) => m.methodKind === "unary" && isDemoAnswered(m))) {
        const key = `${service.typeName}/${method.name}`;
        const handler = handlers[key];
        if (!handler) continue;
        for (const [label, window] of Object.entries(windows)) {
          let answer: unknown;
          try {
            answer = await handler(asked(method.input, (names[method.name] ?? {}) as Record<string, unknown>, window));
          } catch (err) {
            // A read refused as a server would (NotFound, InvalidArgument, Unimplemented) has answered.
            if (err instanceof ConnectError) continue;
            failures.push(`${key} (${label}): ${String(err)}`);
            continue;
          }
          try {
            toBinary(method.output, create(method.output, answer as never));
            encoded++;
          } catch (err) {
            failures.push(`${key} (${label}): ${String(err)}`);
          }
        }
      }
    }
    expect(failures).toEqual([]);
    expect(encoded).toBeGreaterThan(80);
  });
});
