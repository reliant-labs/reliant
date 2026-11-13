import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { UserUpdateType, EntityType } from "../../gen/reliant/v1/streaming_pb";
import type { UserUpdate } from "../../types/streaming";

// A project_org_moved refetch (the project moved to another org with its
// chats, runs and triggers) makes every org-following list read again — in
// the org it left as well as the one it went to, though the update names only
// the target.

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
import { bindActiveOrgUser, chooseActiveOrg, useActiveOrgStore } from "../activeOrgStore";
import { resetStreamInvalidation, useGlobalUpdatesStore } from "../globalUpdatesStore";

// Stamped as the server stamps it: with the org the project moved TO.
const moved: UserUpdate = {
  id: "u-moved",
  user_id: "user-1",
  sequence_number: 1,
  chat_id: "",
  project_id: "p-1",
  org_id: "globex",
  update_type: UserUpdateType.REFETCH,
  entity_type: EntityType.PROJECT,
  entity_id: "p-1",
  data: { type: "project_org_moved" },
  created_at: "2026-01-01T00:00:00Z",
};

const keys = [projectServiceKeys.all, triggerServiceKeys.all, runServiceKeys.all];

beforeEach(() => {
  queryClient.clear();
  resetStreamInvalidation();
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null });
  bindActiveOrgUser("user-1");
  for (const key of keys) queryClient.setQueryData([...key, "probe"], { probe: true });
});

afterEach(() => {
  queryClient.clear();
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null });
});

describe("globalUpdatesStore → a project moved to another org", () => {
  it.each([
    ["the org it left", "acme"],
    ["the org it went to", "globex"],
  ])("invalidates the projects, triggers and runs lists while %s is active", async (_, active) => {
    chooseActiveOrg(active);
    useGlobalUpdatesStore.getState().handleUpdate([moved]);
    await vi.waitFor(() => {
      for (const key of keys) expect(queryClient.getQueryState([...key, "probe"])?.isInvalidated).toBe(true);
    });
  });
});
