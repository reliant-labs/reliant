// Copyright (c) 2025 Reliant Labs

/**
 * The control-plane client for services whose reads spend the org's ClickHouse
 * query quota (ObservabilityService, IssuesService, RumService). It is
 * getControlPlaneClient with two additions:
 *
 *  - a call made as a background poll (quotaStore.runAsPoll) carries
 *    `Forge-Poll: 1`, so the server can refuse polls early to keep a reserve
 *    for a person's own query;
 *  - every read that takes one of the server's per-user concurrent-read slots
 *    (control-plane obsread, SERVER_READ_CAP) waits its turn in the page's ONE
 *    read scheduler first, so no view can fan out past the cap and starve the
 *    others. A view that fires twenty reads at once queues them.
 *
 * The scheduler is here, below every hook and component, rather than in any one
 * view, because the cap is per user: the dashboard, the metrics explorer, logs,
 * traces and the service graph share the same six slots.
 */

import type { DescService } from "@bufbuild/protobuf";
import type { Client } from "@connectrpc/connect";

import { getControlPlaneClient } from "../client";
import { obsReadScheduler } from "./readScheduler";
import { callOptions, isPollRead } from "./quotaStore";

/**
 * The RPCs that take a server read slot (obsread.Open on the console surface).
 * Mutations, the usage read and the Postgres-only issue reads do not: they
 * must never wait behind a queue of ClickHouse reads. obsClient.test.ts fails
 * when a method of these services is in neither this set nor NO_READ_SLOT_RPCS,
 * so a new RPC must be classified.
 */
export const READ_SLOT_RPCS: ReadonlySet<string> = new Set([
  // ObservabilityService
  "searchLogs",
  "listTraces",
  "getTrace",
  "listMetrics",
  "queryMetric",
  "getMetric",
  "listMetricExemplars",
  "getServiceGraph",
  "getServiceGraphCatalog",
  "getServiceOverview",
  "getFlamegraph",
  "queryPanel",
  "listAttributeValues",
  "getServiceSignals",
  "listWorkloads",
  "getWorkload",
  "getDashboards",
  "getReleaseHealth",
  // IssuesService
  "listIssues",
  "getIssue",
  "listIssueEvents",
  // RumService
  "listSessions",
  "getSession",
  "getSessionTimeline",
  "getSessionReplay",
]);

/** The methods of the services above that take no read slot. */
export const NO_READ_SLOT_RPCS: ReadonlySet<string> = new Set([
  "getObservabilityUsage",
  "updateIssueStatus",
  "addIssueComment",
  "updateIssueComment",
  "deleteIssueComment",
  "assignIssue",
  "linkIssueChat",
  "listAssignableUsers",
  "listIssueTimeline",
]);

type Options = { headers?: Record<string, string>; signal?: AbortSignal };

export function obsClient<S extends DescService>(service: S): Client<S> {
  const base = getControlPlaneClient(service);
  return new Proxy(base, {
    get(target, prop, receiver) {
      const member = Reflect.get(target, prop, receiver) as unknown;
      if (typeof member !== "function") return member;
      const call = member as (...a: unknown[]) => Promise<unknown>;
      return (request: unknown, options?: Options) => {
        // Read now, synchronously: the scheduler may start the call after the poll that asked for it has ended.
        const poll = callOptions({ poll: isPollRead() });
        const send = (signal: AbortSignal | undefined) => {
          const merged: Options | undefined =
            poll || signal ? { ...options, ...(signal ? { signal } : {}), ...(poll ? { headers: { ...options?.headers, ...poll.headers } } : {}) } : options;
          return call.call(target, request, merged);
        };
        if (typeof prop !== "string" || !READ_SLOT_RPCS.has(prop)) return send(options?.signal);
        return obsReadScheduler().run((signal) => send(signal), options?.signal);
      };
    },
  });
}
