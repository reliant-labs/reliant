/**
 * A new chat's attached projects go with its first send:
 * StartChatRequest.attached_project_ids, created with the chat before its
 * first turn. A start without any sends none, as before.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import type { StartChatRequest } from "@/gen/reliant/v1/chat_pb";
import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { bindActiveOrgUser, setEffectiveOrg, useActiveOrgStore } from "../activeOrgStore";
import { useChatStore } from "../chatStore";
import { useProjectStore } from "../projectStore";

let requests: StartChatRequest[] = [];

beforeEach(() => {
  requests = [];
  queryClient.clear();
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null });
  bindActiveOrgUser("alice");
  setEffectiveOrg("org-a");
  useProjectStore.setState({ currentProject: { id: "p1", name: "p", path: "/p" } } as never);
  setTransport(
    mockTransport({
      "ChatService.StartChat": (req) => {
        requests.push(req as StartChatRequest);
        return { chat: { id: `chat-${requests.length}`, projectId: "p1", orgId: "org-a", title: "hello" } };
      },
    }),
  );
});

afterEach(() => {
  setTransport(null);
  queryClient.clear();
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null });
});

describe("startChat with attached projects", () => {
  it("sends the projects attached beside the chat's own", async () => {
    await useChatStore
      .getState()
      .startChat("wt-1", "hello", undefined, undefined, undefined, undefined, undefined, { attachedProjectIds: ["p2", "p3"] });
    expect(requests).toHaveLength(1);
    expect(requests[0].projectId).toBe("p1");
    expect(requests[0].attachedProjectIds).toEqual(["p2", "p3"]);
  });

  it("sends none without any", async () => {
    await useChatStore.getState().startChat("wt-1", "hello again");
    expect(requests[0].attachedProjectIds).toEqual([]);
  });
});
