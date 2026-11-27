// Copyright (c) 2025 Reliant Labs

/**
 * The console-wide state of the org's query quota: ONE answer, said once.
 *
 * Any observability query that fails with a quota error reports here
 * (ObsQuotaProvider watches the query cache, and the control-plane transport's
 * quota guard sees every read's answer). Everything that spends quota reads
 * here: a ClickHouse read refuses to leave the browser while the quota is gone
 * (`guard`, run by obsInterceptors.ts on the transport), and every poll asks
 * what its interval is now (`pollMs`). The views read it to draw one banner
 * instead of one error per panel.
 *
 *   ok         reads and polls run as usual.
 *   soft       the server is refusing POLLS to keep a reserve for a person's
 *              own query. Polls slow down; explicit reads still go through.
 *   exhausted  the quota is gone until `resetsAtMs`. Polls stop, and a read
 *              answers at once with the quota error, so a panel mounting
 *              during the pause costs the server nothing.
 *
 * The store is plain TypeScript so the transport (outside React) can read it;
 * React reads it through useSyncExternalStore. There is ONE per page
 * (`obsQuotaStore()`), as there is one read scheduler: the quota is the org's
 * at the server, so every view and every client draws on the same answer.
 */

import { quotaOf, quotaPausedError, type QuotaExhaustion, type QuotaLimit } from "./quota";

export type ObsQuotaState =
  | { kind: "ok" }
  | { kind: "soft" | "exhausted"; resetsAtMs: number; limit: QuotaLimit; lane: string };

/** A soft state slows polls to a quarter of their rate. */
export const SOFT_POLL_FACTOR = 4;

/** The longest a pause may last: the quota interval is an hour, so a later reset is a bad clock or a bad header. */
const MAX_PAUSE_MS = 65 * 60_000;
const OK: ObsQuotaState = { kind: "ok" };

export class ObsQuotaStore {
  private state: ObsQuotaState = OK;
  private readonly listeners = new Set<() => void>();

  private readonly now: () => number;

  // Read through Date at each call, so a fake clock installed after the page's store was made still applies.
  constructor(now: () => number = () => Date.now()) {
    this.now = now;
  }

  getState = (): ObsQuotaState => this.state;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  /** A quota error was seen. The gone-quota state outranks the soft one, and a later reset outranks an earlier. */
  report(q: QuotaExhaustion): void {
    const resetsAtMs = Math.min(q.resetsAtMs, this.now() + MAX_PAUSE_MS);
    const kind = q.soft ? "soft" : "exhausted";
    const cur = this.state;
    if (cur.kind === "exhausted" && kind === "soft") return;
    if (cur.kind === kind && cur.resetsAtMs >= resetsAtMs) return;
    this.set({ kind, resetsAtMs, limit: q.limit, lane: q.lane });
  }

  /** The quota interval ended. */
  clear(): void {
    if (this.state.kind !== "ok") this.set(OK);
  }

  /** The interval a poll runs at now: false while the quota is gone, slower while it is tight. */
  pollMs(baseMs: number | false): number | false {
    if (baseMs === false || this.state.kind === "exhausted") return false;
    return this.state.kind === "soft" ? baseMs * SOFT_POLL_FACTOR : baseMs;
  }

  /**
   * Runs a read, unless the quota is gone: then it answers with the quota
   * error at once, the same one the server would, without a request. Once the
   * reset time has passed (the provider resumes polls a few seconds later, to
   * spread tabs out) a person's read goes through, and its success ends the
   * pause.
   */
  guard<T>(run: () => Promise<T>): Promise<T> {
    const s = this.state;
    if (s.kind !== "exhausted") return this.read(run);
    if (this.now() < s.resetsAtMs) {
      return Promise.reject(quotaPausedError({ limit: s.limit, lane: s.lane, resetsAtMs: s.resetsAtMs, soft: false }));
    }
    return this.read(run).then((value) => {
      this.clear();
      return value;
    });
  }

  private read<T>(run: () => Promise<T>): Promise<T> {
    return run().catch((error: unknown) => {
      const quota = quotaOf(error, this.now());
      if (quota) this.report(quota);
      throw error;
    });
  }

  private set(next: ObsQuotaState): void {
    this.state = next;
    this.listeners.forEach((listener) => listener());
  }
}

let shared: ObsQuotaStore | null = null;

/** The page's quota store, made on first use: what the transport's quota guard and ObsQuotaProvider share. */
export function obsQuotaStore(): ObsQuotaStore {
  shared ??= new ObsQuotaStore();
  return shared;
}

/** Tests only: a fresh page. */
export function resetObsQuotaStore(): void {
  shared = null;
}

// ── Which reads are background polls ─────────────────────────────────────────

let polling = false;

/**
 * Runs `refetch` as a background poll. A query function that starts inside it
 * sees `isPollRead()` — synchronously, at its first line — and its read, made
 * with obsCall (obsInterceptors.ts), carries `Forge-Poll`, so the server can
 * tell a poll from a person's read.
 * The flag is set only for the synchronous start of the fetch, so a read a
 * person triggers can never be mistaken for a poll.
 */
export function runAsPoll(refetch: () => void): void {
  polling = true;
  try {
    refetch();
  } finally {
    polling = false;
  }
}

export function isPollRead(): boolean {
  return polling;
}

/** The request header that marks a background poll (forge/pkg/middleware allows it cross-origin). */
export const POLL_HEADER = "Forge-Poll";
