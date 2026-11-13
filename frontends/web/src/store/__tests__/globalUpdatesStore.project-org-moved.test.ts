import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { UserUpdateType, EntityType } from "../../gen/reliant/v1/streaming_pb";
import type { UserUpdate } from "../../types/streaming";

// A project_org_moved refetch (the project moved to another org with its
// chats, runs and triggers) makes every org-following list read again.

vi.mock("../../api/streaming-grpc", () => ({
  UserStreamingService: vi.fn().mockImplementation(() => ({
    isConnected: () => false,
    disconnect: vi.fn(),
  })),
}));

import { queryClient } from "../../lib/query-client";
import { projectServiceKeys } from "../../hooks/project-service-hooks_gen";
import { triggerServiceKeys } from "../../hooks/trigger-service-hooks_gen";
import { runServiceKeys } from "../../hooks/run-service-hooks_gen";
import { useGlobalUpdatesStore } from "../globalUpdatesStore";

const moved: UserUpdate = {
  id: "u-moved",
  user_id: "user-1",
  sequence_number: 1,
  chat_id: "",
  project_id: "p-1",
  update_type: UserUpdateType.REFETCH,
  entity_type: EntityType.PROJECT,
  entity_id: "p-1",
  data: { type: "project_org_moved" },
  created_at: new Date().toISOString(),
};

const keys = [projectServiceKeys.all, triggerServiceKeys.all, runServiceKeys.all];

beforeEach(() => {
  queryClient.clear();
  for (const key of keys) queryClient.setQueryData([...key, "probe"], { probe: true });
});

afterEach(() => queryClient.clear());

describe("globalUpdatesStore → a project moved to another org", () => {
  it("invalidates the projects, triggers and runs lists", async () => {
    useGlobalUpdatesStore.getState().handleUpdate([moved]);
    await vi.waitFor(() => {
      for (const key of keys) expect(queryClient.getQueryState([...key, "probe"])?.isInvalidated).toBe(true);
    });
  });
});
