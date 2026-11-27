// Copyright (c) 2025 Reliant Labs

/**
 * The control-plane transport's stage for the services whose reads spend the
 * org's ClickHouse query quota (ObservabilityService, IssuesService,
 * RumService; readSlots.ts). api/transport.ts runs it on every control-plane
 * call, inside error-normalize and the org snapshot and ahead of the deadline
 * (see the order there), so any client of those services on the app's
 * transport gets it: a hand-written one, a generated hook's, the module's.
 *
 * For each call it does, in order:
 *
 *  1. QUOTA GUARD. A read that takes a server read slot (READ_SLOT_RPCS) runs
 *     through the page's quota store (quotaStore.ts `obsQuotaStore().guard`):
 *     while the org's quota is gone it answers with the quota error at once and
 *     sends nothing, and a quota error the server returns is reported there.
 *     Postgres reads and mutations of the same services are never refused.
 *  2. READ QUEUE. The same reads wait their turn in the page's ONE read
 *     scheduler (readScheduler.ts), so no view can fan out past the server's
 *     per-user cap and starve the others. The scheduler sits here, below every
 *     hook and component, because the cap is per user: the dashboard, the
 *     metrics explorer, logs, traces and the service graph share the slots.
 *  3. POLL HEADER. A call its caller marked as a background poll carries
 *     `Forge-Poll: 1`, so the server can refuse polls early to keep a reserve
 *     for a person's own query.
 *
 * A TransportScope's transport (lib/transportScope.tsx: the forge tour's
 * shop fixture, a preview) does not run this stage. Its reads answer in the
 * browser and spend none of the person's server slots or quota, so they never
 * queue behind real reads and are never refused for the real org's quota.
 *
 * What the stage cannot know from the request, whether the call is a poll or a
 * prefetch, the caller says per call with `obsCall()`. It must be captured
 * where the call is MADE: `runAsPoll` and `runAsBackground` mark only the
 * synchronous start of a fetch, and the app's transport resolves its route
 * (lib/connect.ts `routed`) a microtask before any interceptor runs.
 */

import { createContextKey, createContextValues, type CallOptions, type Interceptor } from "@connectrpc/connect";
import { forService } from "@reliantlabs/forge-web-runtime/interceptors";

import { isPollRead, obsQuotaStore, POLL_HEADER } from "./quotaStore";
import { isBackgroundRead, obsReadScheduler, type ReadPriority } from "./readScheduler";
import { isReadSlotProcedure, READ_SLOT_SERVICES } from "./readSlots";

/** What a caller says about one read: set by obsCall, read by the stage. */
export interface ObsReadMark {
  /** A background poll (quotaStore.runAsPoll): it carries Forge-Poll. */
  poll: boolean;
  /** Where it waits in the read queue: a poll or a prefetch waits behind what a person is looking at. */
  priority: ReadPriority;
}

/** A call made with no mark is a person's read: no Forge-Poll, visible priority. */
export const obsReadKey = createContextKey<ObsReadMark | undefined>(undefined, {
  description: "whether an observability read is a poll, and its read-queue priority",
});

/**
 * The call options for one observability read, with the read marked as what
 * it is NOW: a poll (inside quotaStore.runAsPoll), a prefetch (inside
 * readScheduler.runAsBackground) or a person's read. Call it synchronously at
 * the start of the read, in the same expression as the rpc:
 *
 *   const res = await client.searchLogs(request, obsCall(options));
 *
 * `options` (a signal, headers such as Forge-Panel) is kept.
 */
export function obsCall(options?: CallOptions): CallOptions {
  const poll = isPollRead();
  const mark: ObsReadMark = { poll, priority: poll || isBackgroundRead() ? "background" : "visible" };
  const contextValues = options?.contextValues ?? createContextValues();
  contextValues.set(obsReadKey, mark);
  return { ...options, contextValues };
}

/** 1. Refuses a ClickHouse read while the org's quota is gone, and reports the server's quota errors. */
export const quotaGuardInterceptor: Interceptor = (next) => (req) =>
  isReadSlotProcedure(req.service.typeName, req.method.name) ? obsQuotaStore().guard(() => next(req)) : next(req);

/**
 * 2. Starts a ClickHouse read when the page's read queue gives it a slot. The
 * call's own signal cancels it while it waits, and the deadline
 * (timeoutInterceptor, further in) starts only once it runs.
 */
export const readSchedulerInterceptor: Interceptor = (next) => (req) => {
  if (!isReadSlotProcedure(req.service.typeName, req.method.name)) return next(req);
  const priority = req.contextValues.get(obsReadKey)?.priority ?? "visible";
  return obsReadScheduler().run(() => next(req), req.signal, priority);
};

/** 3. Marks a background poll for the server. */
export const pollHeaderInterceptor: Interceptor = (next) => (req) => {
  if (req.contextValues.get(obsReadKey)?.poll) req.header.set(POLL_HEADER, "1");
  return next(req);
};

/** The stage api/transport.ts runs on the control-plane transport: the three above, for the quota-spending services only. */
export function obsReadInterceptor(): Interceptor {
  return forService([...READ_SLOT_SERVICES], quotaGuardInterceptor, readSchedulerInterceptor, pollHeaderInterceptor);
}
