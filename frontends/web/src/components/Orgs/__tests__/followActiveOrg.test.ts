/**
 * A switch resets the product reads the server answers per org (the chat
 * list, archived chats, chat search, the Inbox, projects, saved workflows,
 * triggers, runs) so every view shows only the new org's, at once. A
 * chat's own reads are kept (the open pane is not blanked) but marked stale,
 * and the project store moves off another org's project.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";

const follow = vi.hoisted(() => vi.fn(async (_org: string | null) => {}));
vi.mock("@/store/projectStore", () => ({
  useProjectStore: { getState: () => ({ followActiveOrg: follow }) },
}));

import { chatKeys } from "@/hooks/chat-queries";
import { projectServiceKeys } from "@/hooks/project-service-hooks_gen";
import { runServiceKeys } from "@/hooks/run-service-hooks_gen";
import { triggerServiceKeys } from "@/hooks/trigger-service-hooks_gen";
import { workflowServiceKeys } from "@/hooks/workflow-service-hooks_gen";
import { inboxServiceKeys } from "@/hooks/inbox-service-hooks_gen";
import { chatServiceKeys } from "@/hooks/chat-service-hooks_gen";
import { followActiveOrg } from "../followActiveOrg";

let client: QueryClient;

beforeEach(() => {
  follow.mockClear();
  client = new QueryClient({ defaultOptions: { queries: { gcTime: Infinity, retry: false } } });
});

describe("followActiveOrg", () => {
  it("resets the org's product lists, keeps a chat's own reads stale, and moves the project store", () => {
    const lists = [
      chatKeys.list(null),
      chatKeys.list("p1"),
      chatServiceKeys.listArchivedChats({}),
      chatServiceKeys.searchChats({ query: "needle" }),
      inboxServiceKeys.listInbox({ projectId: "p1" }),
      projectServiceKeys.listProjects({ limit: 100, offset: 0 }),
      workflowServiceKeys.listWorkflows({ projectId: "p1" }),
      triggerServiceKeys.listTriggers({}),
      runServiceKeys.listRuns({}),
    ];
    for (const key of lists) client.setQueryData(key, "acme's");
    client.setQueryData(chatKeys.detail("c1"), "open chat");

    followActiveOrg("globex", client);

    for (const key of lists) expect(client.getQueryData(key), JSON.stringify(key)).toBeUndefined();
    expect(client.getQueryData(chatKeys.detail("c1"))).toBe("open chat");
    expect(client.getQueryState(chatKeys.detail("c1"))?.isInvalidated).toBe(true);
    expect(follow).toHaveBeenCalledWith("globex");
  });
});
