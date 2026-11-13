/**
 * The user stream carries every org the person is in; the app shows one. An
 * update about another org (named by the stream's org_id, or known from the
 * chat or project the app holds) reaches nothing: no cache patch, no list
 * refetch, no OS notification. The open chat's updates and those about no org
 * (daemon signals) always pass, and the resume cursor moves past every one.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ChatState } from "../../gen/reliant/v1/chat_pb";
import { UserUpdateType, EntityType } from "../../gen/reliant/v1/streaming_pb";
import type { Chat } from "../../api/client";
import type { UserUpdate } from "../../types/streaming";

vi.mock("../../api/streaming-grpc", () => ({
  UserStreamingService: vi.fn().mockImplementation(() => ({
    isConnected: () => false,
    disconnect: vi.fn(),
  })),
}));

const notifications = vi.hoisted(() => ({ completed: vi.fn() }));
vi.mock("../../lib/notifications", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/notifications")>();
  return {
    ...actual,
    showWorkflowCompletionNotification: notifications.completed,
    showApprovalRequiredNotification: vi.fn(),
    getNotificationPermission: () => "granted" as const,
  };
});

import { chatKeys } from "../../hooks/chat-queries";
import { projectsKey } from "../../hooks/project-queries";
import { queryClient } from "../../lib/query-client";
import { bindActiveOrgUser, chooseActiveOrg, useActiveOrgStore } from "../activeOrgStore";
import { useChatStore } from "../chatStore";
import { STREAM_REFETCH_INTERVAL_MS, resetStreamInvalidation, useGlobalUpdatesStore } from "../globalUpdatesStore";
import { isOtherOrgUpdate } from "../orgUpdateFilter";
import { create } from "@bufbuild/protobuf";
import { ListProjectsResponseSchema, ProjectSchema } from "../../gen/reliant/v1/project_pb";

const later = new Date(Date.now() + 60_000).toISOString();

function chat(id: string, orgId: string | undefined): Chat {
  return {
    id, userId: "user-1", title: `Title ${id}`, projectId: "p-acme", worktreeId: "wt",
    state: ChatState.IDLE, createdAt: later, updatedAt: later, lastActive: later,
    selectedPresets: {}, needsRecovery: false, activity: 0, unread: false, orgId,
  } as Chat;
}

function update(overrides: Partial<UserUpdate>): UserUpdate {
  return {
    id: "u", user_id: "user-1", sequence_number: 1, chat_id: "c-acme", project_id: "",
    update_type: UserUpdateType.CHAT_TITLE_CHANGED, entity_type: EntityType.CHAT, entity_id: "c-acme",
    data: { title: "Renamed" }, created_at: later, ...overrides,
  };
}

const noLookup = { chatOrg: () => undefined, projectOrg: () => undefined };

describe("isOtherOrgUpdate", () => {
  it("drops an update the stream names another org for, and keeps the active org's", () => {
    expect(isOtherOrgUpdate(update({ org_id: "globex" }), "acme", noLookup)).toBe(true);
    expect(isOtherOrgUpdate(update({ org_id: "acme" }), "acme", noLookup)).toBe(false);
  });

  it("falls back to the org of the chat, then the project, the app holds", () => {
    const lookup = { chatOrg: (id: string) => ({ "c-globex": "globex" })[id], projectOrg: (id: string) => ({ "p-globex": "globex" })[id] };
    expect(isOtherOrgUpdate(update({ chat_id: "c-globex" }), "acme", lookup)).toBe(true);
    expect(isOtherOrgUpdate(update({ chat_id: "c-unknown", project_id: "p-globex" }), "acme", lookup)).toBe(true);
    expect(isOtherOrgUpdate(update({ chat_id: "c-unknown", project_id: "p-unknown" }), "acme", lookup)).toBe(false);
  });

  it("keeps updates about no org, with no active org, and about the open chat", () => {
    expect(isOtherOrgUpdate(update({ chat_id: undefined, update_type: UserUpdateType.DAEMON_HEARTBEAT }), "acme", noLookup)).toBe(false);
    expect(isOtherOrgUpdate(update({ org_id: "globex" }), null, noLookup)).toBe(false);
    expect(isOtherOrgUpdate(update({ org_id: "globex", chat_id: "c-open" }), "acme", noLookup, "c-open")).toBe(false);
  });
});

describe("globalUpdatesStore: another org's updates", () => {
  beforeEach(() => {
    queryClient.clear();
    useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null });
    bindActiveOrgUser("user-1");
    chooseActiveOrg("acme");
    useChatStore.setState({ activeChatId: null });
    useGlobalUpdatesStore.setState({ lastSequence: 0 });
    queryClient.setQueryData(chatKeys.list(null), { chats: [chat("c-acme", "acme"), chat("c-globex", "globex")], lastUserUpdateSequence: 0, mainWorktrees: [] });
    queryClient.setQueryData(chatKeys.detail("c-acme"), chat("c-acme", "acme"));
    queryClient.setQueryData(chatKeys.detail("c-globex"), chat("c-globex", "globex"));
    notifications.completed.mockClear();
  });

  afterEach(() => {
    queryClient.clear();
    localStorage.clear();
    useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null });
  });

  const title = (id: string) => queryClient.getQueryData<Chat>(chatKeys.detail(id))?.title;

  it("patches the active org's chat and leaves another org's alone, moving the cursor past both", () => {
    useGlobalUpdatesStore.getState().handleUpdate([
      update({ sequence_number: 1, chat_id: "c-acme", org_id: "acme" }),
      update({ sequence_number: 2, chat_id: "c-globex", org_id: "globex" }),
    ]);
    expect(title("c-acme")).toBe("Renamed");
    expect(title("c-globex")).toBe("Title c-globex");
    expect(useGlobalUpdatesStore.getState().lastSequence).toBe(2);
  });

  it("judges an update without org_id by the chat it names", () => {
    useGlobalUpdatesStore.getState().handleUpdate([update({ chat_id: "c-globex" })]);
    expect(title("c-globex")).toBe("Title c-globex");
  });

  it("judges an unknown chat by its project's org: another org's new chat refetches no list", async () => {
    vi.useFakeTimers();
    try {
      resetStreamInvalidation();
      queryClient.setQueryData(projectsKey(), create(ListProjectsResponseSchema, {
        projects: [
          create(ProjectSchema, { id: "p-globex", name: "G", orgId: "globex" }),
          create(ProjectSchema, { id: "p-acme", name: "A", orgId: "acme" }),
        ],
      }));
      const created = (projectId: string, seq: number) => update({
        sequence_number: seq, chat_id: `c-new-${seq}`, project_id: projectId, update_type: UserUpdateType.CHAT_CREATED,
        data: { chat_id: `c-new-${seq}`, title: "x" },
      });
      const listInvalidated = () => queryClient.getQueryState(chatKeys.list(null))?.isInvalidated;

      useGlobalUpdatesStore.getState().handleUpdate([created("p-globex", 1)]);
      await vi.advanceTimersByTimeAsync(STREAM_REFETCH_INTERVAL_MS * 3);
      expect(listInvalidated()).toBe(false);

      useGlobalUpdatesStore.getState().handleUpdate([created("p-acme", 2)]);
      await vi.advanceTimersByTimeAsync(STREAM_REFETCH_INTERVAL_MS * 3);
      expect(listInvalidated()).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("raises no notification for another org's finished run", () => {
    const finished = (chatId: string, orgId: string, seq: number) => update({
      sequence_number: seq, chat_id: chatId, org_id: orgId, update_type: UserUpdateType.CHAT_STATE_CHANGE,
      data: { state: "idle", previous_state: "idle", reason: "workflow_completed", unread: true },
    });
    useGlobalUpdatesStore.getState().handleUpdate([finished("c-globex", "globex", 1)]);
    expect(notifications.completed).not.toHaveBeenCalled();
    useGlobalUpdatesStore.getState().handleUpdate([finished("c-acme", "acme", 2)]);
    expect(notifications.completed).toHaveBeenCalledTimes(1);
  });

  it("keeps the open chat's updates whatever its org", () => {
    useChatStore.setState({ activeChatId: "c-globex" });
    useGlobalUpdatesStore.getState().handleUpdate([update({ chat_id: "c-globex", org_id: "globex" })]);
    expect(title("c-globex")).toBe("Renamed");
  });
});
