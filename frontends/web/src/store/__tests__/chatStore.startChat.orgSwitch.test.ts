/**
 * A StartChat answer lands in the lists of the org the start began in, not
 * the org the tab shows when it resolves. The chat-list keys name their org
 * (lib/orgScope); a write that took the tab's org at answer time put a chat
 * of org A into org B's sidebar after a switch mid-request.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { chatKeys, type ChatListEnvelope } from "@/hooks/chat-queries";
import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { bindActiveOrgUser, setEffectiveOrg, useActiveOrgStore } from "../activeOrgStore";
import { useChatStore } from "../chatStore";
import { useProjectStore } from "../projectStore";

const envelope = (): ChatListEnvelope => ({ chats: [], lastUserUpdateSequence: 0, mainWorktrees: [] });
const ids = (key: readonly unknown[]) => queryClient.getQueryData<ChatListEnvelope>(key)?.chats.map((c) => c.id) ?? [];

let answer: ((chat: unknown) => void) | undefined;

beforeEach(() => {
  answer = undefined;
  queryClient.clear();
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null });
  bindActiveOrgUser("alice");
  setEffectiveOrg("org-a");
  useProjectStore.setState({ currentProject: { id: "p1", name: "p", path: "/p" } } as never);
  setTransport(
    mockTransport({
      "ChatService.StartChat": () => new Promise((resolve) => (answer = (chat) => resolve({ chat }))),
    }),
  );
  // Both orgs' lists are loaded and kept (nothing observes them here).
  queryClient.setQueryDefaults(chatKeys.lists(), { gcTime: Infinity });
  for (const org of ["org-a", "org-b"]) {
    queryClient.setQueryData(chatKeys.list(null, org), envelope());
    queryClient.setQueryData(chatKeys.list("p1", org), envelope());
  }
});

afterEach(() => {
  setTransport(null);
  queryClient.clear();
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null });
});

describe("startChat across an org switch", () => {
  it("puts the chat in the org it was started in, and none of it in the org switched to", async () => {
    const started = useChatStore.getState().startChat(undefined, "hello");
    // The person switches this tab to org B while StartChat is in flight.
    await vi.waitFor(() => expect(answer).toBeTypeOf("function"));
    setEffectiveOrg("org-b");
    answer!({ id: "chat-a", projectId: "p1", orgId: "org-a", title: "hello" });
    await started;

    expect(ids(chatKeys.list(null, "org-b"))).toEqual([]);
    expect(ids(chatKeys.list("p1", "org-b"))).toEqual([]);
    expect(ids(chatKeys.list("p1", "org-a"))).toEqual(["chat-a"]);
    expect(ids(chatKeys.list(null, "org-a"))).toEqual(["chat-a"]);
  });

  it("never lists a chat of another org, even in the org it was started from", async () => {
    const started = useChatStore.getState().startChat(undefined, "hello");
    await vi.waitFor(() => expect(answer).toBeTypeOf("function"));
    answer!({ id: "chat-x", projectId: "p1", orgId: "org-x", title: "hello" });
    await started;

    expect(ids(chatKeys.list("p1", "org-a"))).toEqual([]);
    expect(ids(chatKeys.list(null, "org-a"))).toEqual([]);
  });
});
