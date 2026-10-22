/**
 * Regression: a spawned thread must still look like a spawn after a reload.
 *
 * Two activities emit update_type="thread" for the same spawned thread: the
 * spawn announcement (workflow_status.go) and the lifecycle update
 * (thread_status.go). mergeActiveThreads carries identity fields forward, so
 * on a LIVE stream a lifecycle update that omitted origin was harmless — the
 * announcement had already arrived and origin survived the merge.
 *
 * The reconnect snapshot has no such history. GetLatestNonMessageUpdatesPerEntity
 * keeps only the NEWEST row per entity, and a fresh page load starts with an
 * empty store, so the client sees the lifecycle update ALONE. When that row
 * omitted origin the thread came back with origin=undefined, isSpawnOrigin()
 * went false, and BackgroundWorkPill dropped every running spawn — observed on
 * chat 84321199 with six live sub-agents and no pill.
 *
 * The fix is server-side: both emitters now fall back to the persisted
 * threads.origin column, so every thread update carries origin on its own.
 * These tests pin the client behavior that makes that guarantee load-bearing —
 * a single snapshot row is enough to classify the thread.
 *
 * Payloads are verbatim from chat_updates for that chat, except that the
 * lifecycle row gains the origin the fix now emits.
 */

import { describe, expect, it } from "vitest";
import { mergeActiveThreads } from "../chatStreamReducers";
import { isSpawnOrigin } from "../../components/Chat/thread-views/threadUtils";
import type { ActiveThreadUpdate } from "../../types/streaming";

// seq 531 — the spawn announcement.
const spawnAnnouncement = {
  update_type: "thread",
  id: "71a41fe5-a53d-5c72-a8e9-efd7b81200ed",
  chat_id: "84321199-2788-4289-a71f-b84d03beb1d6",
  thread: "71a41fe5-a53d-5c72-a8e9-efd7b81200ed",
  workflow_id: "71a41fe5-a53d-5c72-a8e9-efd7b81200ed",
  workflow_name: "builtin://agent",
  origin: "spawn",
  spawned_by_tool_call_id: "toolu_01NwJUt1E1vQs4RFmC1xUJ5V",
  status: "running",
  thread_title: "Fix blank pause screen",
  title: "general",
  created_at: "2026-08-12T16:49:00Z",
} as unknown as ActiveThreadUpdate;

// seq 534 — the lifecycle update for the SAME thread, as emitted after the fix.
const lifecycleUpdate = {
  update_type: "thread",
  id: "71a41fe5-a53d-5c72-a8e9-efd7b81200ed",
  chat_id: "84321199-2788-4289-a71f-b84d03beb1d6",
  thread: "71a41fe5-a53d-5c72-a8e9-efd7b81200ed",
  workflow_id: "71a41fe5-a53d-5c72-a8e9-efd7b81200ed",
  origin: "spawn",
  origin_node_id: "spawn-toolu_01NwJUt1E1vQs4RFmC1xUJ5V",
  status: "running",
  thread_title: "Fix blank pause screen",
} as unknown as ActiveThreadUpdate;

describe("spawn origin survives the lifecycle update", () => {
  it("keeps origin when both updates arrive live", () => {
    const merged = mergeActiveThreads([], [spawnAnnouncement, lifecycleUpdate]);
    expect(merged).toHaveLength(1);
    expect(isSpawnOrigin(merged[0].origin)).toBe(true);
  });

  it("classifies the thread from a snapshot's single newest row", () => {
    // What the reconnect snapshot delivers: one row per entity, onto an empty
    // store. This is the case that regressed.
    const snapshot = mergeActiveThreads([], [lifecycleUpdate]);
    expect(snapshot).toHaveLength(1);
    expect(isSpawnOrigin(snapshot[0].origin)).toBe(true);
  });

  it("never lets a later update downgrade a known origin to undefined", () => {
    // Defense in depth for replayed history written before the server fix:
    // once a thread is known to be a spawn, an update lacking origin must not
    // un-spawn it.
    const legacyRow = { ...lifecycleUpdate, origin: undefined };
    const merged = mergeActiveThreads([spawnAnnouncement], [legacyRow]);
    expect(isSpawnOrigin(merged[0].origin)).toBe(true);
  });
});
