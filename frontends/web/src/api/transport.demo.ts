// Copyright (c) 2025 Reliant Labs

/**
 * api/transport.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts). The real module is the app's interceptor
 * chain: bearer tokens, 401 recovery and sign-out, the write-owner check,
 * deadlines, tracing. The demo has none of that to run: every RPC goes to the
 * demo's one transport (lib/connect.demo.ts), answered in the page from
 * sample data, with no credential and no URL. So this twin keeps only the
 * per-call context helpers the chat and attachment code attach to their
 * calls, and the daemon heartbeat setter the updates stream calls; none of
 * them reaches the auth store or a sign-in module, which the demo bundle must
 * not contain.
 */

import { createContextKey, createContextValues } from "@connectrpc/connect";

import type * as Real from "./transport";

/** Nothing reads it: the demo transport sends no x-daemon-last-seen header. */
export const setDaemonLastSeen: typeof Real.setDaemonLastSeen = () => {};

export const rpcBudgetKey: typeof Real.rpcBudgetKey = createContextKey<number | undefined>(undefined, {
  description: "this call's client-side budget in ms, over the per-method table",
});

/** Recorded for parity; the demo transport answers in the page and needs no budget. */
export const withRpcBudget: typeof Real.withRpcBudget = (values, budgetMs) => {
  const next = values ?? createContextValues();
  next.set(rpcBudgetKey, budgetMs);
  return next;
};

export const requestSentKey: typeof Real.requestSentKey = createContextKey<(() => void) | undefined>(undefined, {
  description: "called when the request is handed to fetch",
});

/**
 * The app calls `onSent` when the request is handed to fetch, after its
 * interceptors and the token lookup. The demo has neither: the request is
 * handed to the demo transport as soon as it is made, so `onSent` runs now.
 */
export const withRequestSent: typeof Real.withRequestSent = (values, onSent) => {
  const next = values ?? createContextValues();
  next.set(requestSentKey, onSent);
  try {
    onSent();
  } catch {
    // An observer never fails the request it observes.
  }
  return next;
};
