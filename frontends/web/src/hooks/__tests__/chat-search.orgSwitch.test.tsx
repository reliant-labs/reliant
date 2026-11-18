/**
 * Chat search keeps the previous term's hits on screen while the next loads,
 * but never another org's. With an unconditional keepPreviousData, switching
 * the tab from org A to org B (B's answer not cached yet) rendered A's chat
 * titles under B until B answered: ActiveOrgBinding's reset runs in an effect,
 * after the render that already uses B's key.
 */
import type { ReactNode } from "react";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { bindActiveOrgUser, setEffectiveOrg, useActiveOrgStore } from "@/store/activeOrgStore";
import { useChatSearch, type ChatSearchHit } from "../chat-pull-queries";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

const hit = (id: string, orgId: string) => ({ id, title: `${orgId} ${id}`, updatedAt: "2026-01-01T00:00:00Z", orgId });

/** SearchChats per org: org-a answers at once, org-b only when released. */
let releaseB: (() => void) | undefined;

beforeEach(() => {
  releaseB = undefined;
  queryClient.clear();
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null });
  bindActiveOrgUser("alice");
  setEffectiveOrg("org-a");
  setTransport(
    mockTransport({
      "ChatService.ListChats": () => ({ chats: [], lastUserUpdateSequence: 0n, mainWorktrees: [] }),
      "ChatService.SearchChats": (req) => {
        const orgId = (req as { orgId?: string }).orgId;
        if (orgId === "org-a") return { chats: [hit("a1", "org-a"), hit("a2", "org-a")] };
        return new Promise((resolve) => (releaseB = () => resolve({ chats: [hit("b1", "org-b")] })));
      },
    }),
  );
});

afterEach(() => {
  cleanup();
  setTransport(null);
  queryClient.clear();
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null });
});

describe("useChatSearch across an org switch", () => {
  it("never renders org A's hits once the tab is in org B", async () => {
    const renders: { org: string | null; ids: string[] }[] = [];
    const { result } = renderHook(
      () => {
        const search = useChatSearch(null, "deploy");
        const org = useActiveOrgStore((s) => s.effectiveId);
        renders.push({ org, ids: search.results.map((h: ChatSearchHit) => h.chatId) });
        return search;
      },
      { wrapper },
    );
    await waitFor(() => expect(result.current.results.map((h) => h.chatId)).toEqual(["a1", "a2"]));

    act(() => setEffectiveOrg("org-b"));
    await waitFor(() => expect(releaseB).toBeTypeOf("function"));
    // While B's answer is in flight: nothing of A's, in any render under B.
    const underB = () => renders.filter((r) => r.org === "org-b");
    expect(underB().length).toBeGreaterThan(0);
    for (const render of underB()) expect(render.ids).not.toContain("a1");
    expect(result.current.results).toEqual([]);

    act(() => releaseB!());
    await waitFor(() => expect(result.current.results.map((h) => h.chatId)).toEqual(["b1"]));
    for (const render of underB()) expect(render.ids.some((id) => id.startsWith("a"))).toBe(false);
  });
});
