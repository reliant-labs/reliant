/**
 * The product's org-scoped lists never go out naming no org while the tab's
 * org is unknown, and their cache keys name the org they were read in.
 *
 * A list request with no org_id is answered with the caller's rows in EVERY
 * org (the server's documented back-compat meaning). The sidebar used to fire
 * ListProjects and ListChats before the membership list answered, under a key
 * that named no org, and kept serving that every-org answer as the active
 * org's: projects and chats of other orgs showed up in the sidebar.
 */
import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const listMyOrganizations = vi.fn();
vi.mock("@/services/controlPlane/orgs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/orgs")>()),
  listMyOrganizations: () => listMyOrganizations(),
}));
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
vi.mock("@/api/streaming-grpc", () => ({
  UserStreamingService: vi.fn().mockImplementation(() => ({
    isConnected: () => false,
    disconnect: vi.fn(),
    subscribeToChatDetails: vi.fn(),
    unsubscribeFromChatDetails: vi.fn(),
  })),
}));

import { ActiveOrgBinding } from "@/components/Orgs/ActiveOrgBinding";
import { followActiveOrg } from "@/components/Orgs/followActiveOrg";
import { chatKeys, useChatListWithWorkspaces } from "@/hooks/chat-queries";
import { fetchProjects, projectsKey, useProjects } from "@/hooks/project-queries";
import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { projectRow } from "@/test/projectList";
import { bindActiveOrgUser, setEffectiveOrg, useActiveOrgStore } from "@/store/activeOrgStore";
import { useAuthStore } from "@/store/authStore";
import { getOrgScope, keyNamesOrg } from "../orgScope";

const ACME = { id: "org-a", name: "Acme", slug: "acme", role: "owner", isOwner: true, isDefault: true };
const SIDE = { id: "org-b", name: "Side", slug: "side", role: "member", isOwner: false, isDefault: false };

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

/** A ProjectService + ChatService that records the org each list named. */
function server() {
  const projectOrgs: (string | undefined)[] = [];
  const chatOrgs: (string | undefined)[] = [];
  setTransport(
    mockTransport({
      "ProjectService.ListProjects": (req) => {
        const orgId = (req as { orgId?: string }).orgId;
        projectOrgs.push(orgId);
        return { projects: [projectRow({ id: `p-${orgId ?? "every-org"}`, name: "p", path: "/p" })], total: 1 };
      },
      "ChatService.ListChats": (req) => {
        chatOrgs.push((req as { orgId?: string }).orgId);
        return { chats: [], lastUserUpdateSequence: 0n, mainWorktrees: [] };
      },
    }),
  );
  return { projectOrgs, chatOrgs };
}

/** Resolve the membership list on demand, as a slow control plane would. */
function deferredOrgs() {
  let answer!: (value: unknown) => void;
  listMyOrganizations.mockReturnValue(new Promise((resolve) => (answer = resolve)));
  return (value: unknown) => act(async () => answer(value));
}

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  queryClient.clear();
  listMyOrganizations.mockReset();
  useAuthStore.setState({ user: { id: "alice" } as never, session: { access_token: "t" } as never });
  // Unbound, so each test's binding loads what it saved first.
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false });
});

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
  useAuthStore.setState({ user: null, session: null });
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false });
});

describe("org-scoped product lists", () => {
  it("do not fetch with no org while the org list loads, then fetch in the resolved org", async () => {
    const { projectOrgs, chatOrgs } = server();
    const answer = deferredOrgs();
    render(<ActiveOrgBinding onSwitch={followActiveOrg} />, { wrapper });
    const projects = renderHook(() => useProjects(), { wrapper });
    const chats = renderHook(() => useChatListWithWorkspaces(null), { wrapper });

    // The org is not known yet: the lists wait (loading), and nothing went out.
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(projects.result.current.isLoading).toBe(true);
    expect(projectOrgs).toEqual([]);
    expect(chatOrgs).toEqual([]);
    expect(getOrgScope()).toEqual({ ready: false, orgId: null });

    await answer({ orgs: [ACME, SIDE], defaultOrgId: "org-a" });

    await waitFor(() => expect(projects.result.current.projects.map((p) => p.id)).toEqual(["p-org-a"]));
    await waitFor(() => expect(chats.result.current.isSuccess).toBe(true));
    // Every request named the org; none went out naming none.
    expect(projectOrgs).toEqual(["org-a"]);
    expect(chatOrgs).toEqual(["org-a"]);
  });

  it("name the org in their cache keys", async () => {
    server();
    bindActiveOrgUser("alice");
    setEffectiveOrg("org-a");
    const projects = renderHook(() => useProjects(), { wrapper });
    const chats = renderHook(() => useChatListWithWorkspaces(null), { wrapper });
    await waitFor(() => expect(projects.result.current.projects).toHaveLength(1));
    await waitFor(() => expect(chats.result.current.isSuccess).toBe(true));

    expect(keyNamesOrg(projectsKey(), "org-a")).toBe(true);
    expect(queryClient.getQueryData(projectsKey("org-a"))).toBeDefined();
    expect(queryClient.getQueryData(projectsKey(null))).toBeUndefined();
    expect(queryClient.getQueryData(chatKeys.list(null, "org-a"))).toBeDefined();
    expect(queryClient.getQueryData(chatKeys.list(null, null))).toBeUndefined();
    // Another org's list is another entry: it is never served as this one's.
    expect(projectsKey("org-b")).not.toEqual(projectsKey("org-a"));
    expect(chatKeys.list(null, "org-b")).not.toEqual(chatKeys.list(null, "org-a"));
  });

  it("start at once in a tab that saved its org, naming it", async () => {
    const { projectOrgs } = server();
    sessionStorage.setItem("reliant.activeOrg.alice", "org-b");
    bindActiveOrgUser("alice");
    const projects = renderHook(() => useProjects(), { wrapper });
    await waitFor(() => expect(projects.result.current.projects.map((p) => p.id)).toEqual(["p-org-b"]));
    expect(projectOrgs).toEqual(["org-b"]);
  });

  it("an imperative read waits for the org too", async () => {
    const { projectOrgs } = server();
    bindActiveOrgUser("alice");
    let settled = false;
    const read = fetchProjects().then((projects) => {
      settled = true;
      return projects;
    });
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(settled).toBe(false);
    expect(projectOrgs).toEqual([]);

    act(() => setEffectiveOrg("org-a"));
    expect((await read).map((p) => p.id)).toEqual(["p-org-a"]);
    expect(projectOrgs).toEqual(["org-a"]);
  });

  it("a person with no org yet lists with none once the org list says so", async () => {
    const { projectOrgs } = server();
    const answer = deferredOrgs();
    render(<ActiveOrgBinding onSwitch={followActiveOrg} />, { wrapper });
    const projects = renderHook(() => useProjects(), { wrapper });
    await answer({ orgs: [], defaultOrgId: "" });
    await waitFor(() => expect(projects.result.current.projects).toHaveLength(1));
    expect(projectOrgs).toEqual([undefined]);
  });

  it("wait from the moment someone signs in, before any binding renders", () => {
    useAuthStore.setState({ user: null, session: null });
    expect(getOrgScope()).toEqual({ ready: true, orgId: null }); // signed out: nothing to list
    useAuthStore.setState({ user: { id: "bob" } as never, session: { access_token: "t" } as never });
    expect(useActiveOrgStore.getState().userId).toBe("bob");
    expect(getOrgScope()).toEqual({ ready: false, orgId: null });
  });

  it("the first org of a tab drops a list read with no org, and keeps the org's", () => {
    queryClient.setQueryDefaults(["projectService"], { gcTime: Infinity });
    queryClient.setQueryDefaults(["chats"], { gcTime: Infinity });
    queryClient.setQueryData(projectsKey(null), { projects: [], total: 9 });
    queryClient.setQueryData(projectsKey("org-a"), { projects: [], total: 1 });
    queryClient.setQueryData(chatKeys.list(null, null), { chats: [], lastUserUpdateSequence: 0, mainWorktrees: [] });

    followActiveOrg("org-a", queryClient, { firstLoad: true });

    expect(queryClient.getQueryData(projectsKey(null))).toBeUndefined();
    expect(queryClient.getQueryData(chatKeys.list(null, null))).toBeUndefined();
    expect(queryClient.getQueryData(projectsKey("org-a"))).toEqual({ projects: [], total: 1 });
  });
});
