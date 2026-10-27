// Copyright (c) 2025 Reliant Labs

/**
 * The console's ONE background-poll scheduler for observability reads.
 *
 * React Query's own `refetchInterval` gives every observer its own timer, so
 * four panels on one query key poll four times per interval, and nothing ties
 * a poll to the org's quota. This registry keeps one timer per query key, however
 * many components watch it, and answers to the quota store: no timer runs while
 * the quota is gone, timers run at a quarter of their rate while it is tight,
 * and a tick in a hidden tab does nothing. When the quota state changes the
 * timers are re-armed, so polling pauses and resumes on its own.
 */

import { focusManager, hashKey, type QueryClient, type QueryKey } from "@tanstack/react-query";

import { runAsPoll, type ObsQuotaStore } from "./quotaStore";

interface Entry {
  client: QueryClient;
  key: QueryKey;
  subs: Set<{ ms: number }>;
  timer: ReturnType<typeof setInterval> | null;
  armedMs: number | false;
}

export class ObsPollRegistry {
  private readonly entries = new Map<string, Entry>();

  private readonly store: ObsQuotaStore;

  constructor(store: ObsQuotaStore) {
    this.store = store;
    store.subscribe(() => this.entries.forEach((entry) => this.arm(entry)));
  }

  /** Poll `key` every `ms` for as long as the returned function is not called. */
  subscribe(client: QueryClient, key: QueryKey, ms: number): () => void {
    const id = hashKey(key);
    let entry = this.entries.get(id);
    if (!entry) {
      entry = { client, key, subs: new Set(), timer: null, armedMs: false };
      this.entries.set(id, entry);
    }
    const sub = { ms };
    entry.subs.add(sub);
    this.arm(entry);
    return () => {
      const current = this.entries.get(id);
      if (!current) return;
      current.subs.delete(sub);
      if (current.subs.size === 0) {
        this.disarm(current);
        this.entries.delete(id);
      } else {
        this.arm(current);
      }
    };
  }

  private arm(entry: Entry): void {
    const base = Math.min(...[...entry.subs].map((sub) => sub.ms));
    const ms = this.store.pollMs(Number.isFinite(base) ? base : false);
    if (ms === entry.armedMs && (ms === false) === (entry.timer === null)) return;
    this.disarm(entry);
    entry.armedMs = ms;
    if (ms === false) return;
    entry.timer = setInterval(() => this.tick(entry), ms);
  }

  private disarm(entry: Entry): void {
    if (entry.timer !== null) clearInterval(entry.timer);
    entry.timer = null;
    entry.armedMs = false;
  }

  private tick(entry: Entry): void {
    if (!focusManager.isFocused() || this.store.getState().kind === "exhausted") return;
    // exact + active: the query the components watch, and only while one is mounted.
    // cancelRefetch false: a read still in flight is the answer; never restart it.
    runAsPoll(() => {
      void entry.client.refetchQueries({ queryKey: entry.key, exact: true, type: "active" }, { cancelRefetch: false });
    });
  }
}
