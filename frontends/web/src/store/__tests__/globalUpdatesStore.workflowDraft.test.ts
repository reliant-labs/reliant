import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../../api/streaming-grpc", () => ({
  UserStreamingService: vi.fn(),
}));

import { useGlobalUpdatesStore } from "../globalUpdatesStore";
import { subscribeToDraftUpdates } from "../workflowDraftUpdates";
import { EntityType, UserUpdateType } from "../../gen/reliant/v1/streaming_pb";
import type { UserUpdate } from "../../types/streaming";

function draftUpdate(draftId: string, version: number, seq: number): UserUpdate {
  return {
    id: `u-${seq}`,
    user_id: "user-1",
    sequence_number: seq,
    update_type: UserUpdateType.WORKFLOW_DRAFT_UPDATED,
    entity_type: EntityType.WORKFLOW_DRAFT,
    entity_id: draftId,
    data: { draft_id: draftId, slug: `slug-${draftId}`, version },
    created_at: "",
  };
}

describe("globalUpdatesStore WORKFLOW_DRAFT_UPDATED routing", () => {
  const unsubs: Array<() => void> = [];
  afterEach(() => {
    unsubs.splice(0).forEach((u) => u());
  });

  it("delivers to the subscriber for that draft id only", () => {
    const forA = vi.fn();
    const forB = vi.fn();
    unsubs.push(subscribeToDraftUpdates("draft-a", forA));
    unsubs.push(subscribeToDraftUpdates("draft-b", forB));

    useGlobalUpdatesStore.getState().handleUpdate([draftUpdate("draft-a", 7, 1)]);

    expect(forA).toHaveBeenCalledWith({ draftId: "draft-a", slug: "slug-draft-a", version: 7 });
    expect(forB).not.toHaveBeenCalled();
  });

  it("carries the deleted flag so an open editor learns its draft is gone", () => {
    const listener = vi.fn();
    unsubs.push(subscribeToDraftUpdates("draft-a", listener));
    const update = draftUpdate("draft-a", 7, 3);
    update.data = { draft_id: "draft-a", slug: "slug-draft-a", version: 7, deleted: true };

    useGlobalUpdatesStore.getState().handleUpdate([update]);

    expect(listener).toHaveBeenCalledWith(expect.objectContaining({ draftId: "draft-a", deleted: true }));
  });

  it("stops delivering after unsubscribe", () => {
    const listener = vi.fn();
    const unsub = subscribeToDraftUpdates("draft-a", listener);
    unsub();
    useGlobalUpdatesStore.getState().handleUpdate([draftUpdate("draft-a", 8, 2)]);
    expect(listener).not.toHaveBeenCalled();
  });
});
