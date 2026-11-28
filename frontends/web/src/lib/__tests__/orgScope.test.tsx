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
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClientProvider, QueryObserver } from "@tanstack/react-query";
import { act, cleanup, render, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const listMyOrganizations = vi.fn();
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
import { myOrgsHandlers } from "@/test/orgsTransport";
import { projectRow } from "@/test/projectList";
import { bindActiveOrgUser, setEffectiveOrg, useActiveOrgStore } from "@/store/activeOrgStore";
import { useAuthStore } from "@/store/authStore";
import { observabilityServiceKeys } from "@/hooks/controlplane/observability-service-hooks_gen";
import { relativeWindow } from "@/services/controlPlane/observability/api";
import { metricCatalogRequest } from "@/services/controlPlane/observability/metricsApi";
import { rumServiceKeys } from "@/hooks/controlplane/rum-service-hooks_gen";
import { listSessionsRequest } from "@/services/controlPlane/observability/rumApi";
import { forgeCloudKeys } from "@/hooks/forge-queries";
import { getOrgScope, keepPreviousInOrg, keepPreviousInOrgSegment, keyNamesOrg, resetAfterLeavingOrg } from "../orgScope";

const ACME = { id: "org-a", name: "Acme", slug: "acme", role: "owner", isOwner: true, isDefault: true };
const SIDE = { id: "org-b", name: "Side", slug: "side", role: "member", isOwner: false, isDefault: false };

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

/** The membership list, answered by `listMyOrganizations`. */
const MY_ORGS = myOrgsHandlers({ listMyOrganizations: () => listMyOrganizations() });

/** A ProjectService + ChatService that records the org each list named. */
function server() {
  const projectOrgs: (string | undefined)[] = [];
  const chatOrgs: (string | undefined)[] = [];
  setTransport(
    mockTransport({
      ...MY_ORGS,
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
  setTransport(mockTransport(MY_ORGS));
  useAuthStore.setState({ user: { id: "alice" } as never, session: { access_token: "t" } as never });
  // Unbound, so each test's binding loads what it saved first.
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null });
});

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
  useAuthStore.setState({ user: null, session: null });
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null });
});

describe("org-scoped product lists", () => {
  it("do not fetch with no org while the org list loads, then fetch in the resolved org", async () => {
    const { projectOrgs, chatOrgs } = server();
    const answer = deferredOrgs();
    render(<ActiveOrgBinding onSwitch={followActiveOrg} />, { wrapper });
    const projects = renderHook(() => useProjects(), { wrapper });
    const chats = renderHook(() => useChatListWithWorkspaces(null), { wrapper });

    // The org is not known yet: the membership list is in flight, the lists
    // wait (loading, their queries disabled), and nothing went out.
    await waitFor(() => expect(listMyOrganizations).toHaveBeenCalledTimes(1));
    expect(projects.result.current.isLoading).toBe(true);
    expect(chats.result.current.fetchStatus).toBe("idle");
    expect(projectOrgs).toEqual([]);
    expect(chatOrgs).toEqual([]);
    expect(getOrgScope()).toMatchObject({ ready: false, orgId: null, error: null });

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
    const read = fetchProjects();
    // Still waiting: it settles only on an org-store change, which has not
    // happened, so it loses a race against an already-resolved promise.
    expect(await Promise.race([read.then(() => "settled"), Promise.resolve("waiting")])).toBe("waiting");
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
    expect(getOrgScope()).toMatchObject({ ready: true, orgId: null }); // signed out: nothing to list
    useAuthStore.setState({ user: { id: "bob" } as never, session: { access_token: "t" } as never });
    expect(useActiveOrgStore.getState().userId).toBe("bob");
    expect(getOrgScope()).toMatchObject({ ready: false, orgId: null });
  });

  it("an API-key session proceeds at once, naming no org, without the person's membership list", async () => {
    const { projectOrgs } = server();
    // The control plane refuses a machine credential's membership list.
    listMyOrganizations.mockRejectedValue(new ConnectError("human sessions only", Code.PermissionDenied));
    useAuthStore.getState().setApiKeySession("rk_test");
    render(<ActiveOrgBinding onSwitch={followActiveOrg} />, { wrapper });
    const projects = renderHook(() => useProjects(), { wrapper });

    await waitFor(() => expect(projects.result.current.projects.map((p) => p.id)).toEqual(["p-every-org"]));
    // No org named: the server acts in the credential's own org.
    expect(projectOrgs).toEqual([undefined]);
    expect(listMyOrganizations).not.toHaveBeenCalled();
    expect(getOrgScope()).toMatchObject({ ready: true, orgId: null, error: null });
    await expect(fetchProjects()).resolves.toHaveLength(1);
  });

  it("an org list that fails is the lists' error with a Retry, never a load forever or every org's rows", async () => {
    const { projectOrgs, chatOrgs } = server();
    const refused = new ConnectError("control plane unavailable", Code.PermissionDenied);
    listMyOrganizations.mockRejectedValue(refused);
    render(<ActiveOrgBinding onSwitch={followActiveOrg} />, { wrapper });
    const projects = renderHook(() => useProjects(), { wrapper });
    const chats = renderHook(() => useChatListWithWorkspaces(null), { wrapper });

    await waitFor(() => expect(projects.result.current.error).toBe(refused));
    expect(projects.result.current.isLoading).toBe(false);
    await waitFor(() => expect(chats.result.current.isError).toBe(true));
    await expect(fetchProjects()).rejects.toBe(refused);
    // Nothing went out naming no org.
    expect(projectOrgs).toEqual([]);
    expect(chatOrgs).toEqual([]);

    // Retry re-reads the membership list; once it answers, the lists load in the org.
    listMyOrganizations.mockResolvedValue({ orgs: [ACME], defaultOrgId: "org-a" });
    act(() => projects.result.current.refetch());
    await waitFor(() => expect(projects.result.current.projects.map((p) => p.id)).toEqual(["p-org-a"]));
    expect(projects.result.current.error).toBeNull();
    await waitFor(() => expect(chatOrgs).toEqual(["org-a"]));
    expect(projectOrgs).toEqual(["org-a"]);
  });

  it("keeps a previous answer as placeholder only when it was read in the same org", () => {
    const kept = { rows: 1 };
    const generated = (orgId?: string) => ({ queryKey: ["chatService", "searchChats", { query: "x", ...(orgId ? { orgId } : {}) }] });
    expect(keepPreviousInOrg("org-a")(kept, generated("org-a"))).toBe(kept);
    expect(keepPreviousInOrg("org-b")(kept, generated("org-a"))).toBeUndefined();
    expect(keepPreviousInOrg("org-b")(kept, generated())).toBeUndefined();
    expect(keepPreviousInOrg(null)(kept, generated())).toBe(kept);
    expect(keepPreviousInOrg(null)(kept, generated("org-a"))).toBeUndefined();
    expect(keepPreviousInOrg("org-a")(kept, undefined)).toBeUndefined();
  });

  it("keeps an observability answer only in its org, and never when the scope becomes null", () => {
    const kept = { rows: 1 };
    // A hand-written observability key: the org is a plain string segment, not a request field.
    const panelA = { queryKey: ["observability", "org-a", "", [], [], false, "panel"] as readonly unknown[] };
    expect(keepPreviousInOrgSegment("org-a")(kept, panelA)).toBe(kept);
    expect(keepPreviousInOrgSegment("org-b")(kept, panelA)).toBeUndefined();
    // An empty app selection (MetricsExplorerPage) makes the scope null: A's rows must go.
    expect(keepPreviousInOrgSegment(null)(kept, panelA)).toBeUndefined();
    // The sessions list's generated key: the org is in its request's RumScope.
    const sessionsA = {
      queryKey: rumServiceKeys.listSessions(
        listSessionsRequest({ orgId: "org-a", envIds: [], window: relativeWindow(3_600_000), hasError: false, hasReplay: true, userId: "", pathContains: "" })
      ),
    };
    expect(keepPreviousInOrg("org-a")(kept, sessionsA)).toBe(kept);
    expect(keepPreviousInOrg("org-b")(kept, sessionsA)).toBeUndefined();
    expect(keyNamesOrg(sessionsA.queryKey, "org-a")).toBe(true);
    // The metric catalog's generated key: the org is in its request's QueryScope.
    const catalogA = { queryKey: observabilityServiceKeys.listMetrics(metricCatalogRequest({ orgId: "org-a", envIds: [], filters: [], window: relativeWindow(3_600_000) })) };
    expect(keepPreviousInOrg("org-a")(kept, catalogA)).toBe(kept);
    expect(keepPreviousInOrg("org-b")(kept, catalogA)).toBeUndefined();
    expect(keepPreviousInOrg(null)(kept, catalogA)).toBeUndefined();
    expect(keyNamesOrg(catalogA.queryKey, "org-a")).toBe(true);
    expect(keyNamesOrg(catalogA.queryKey, "org-b")).toBe(false);
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

  it("leaving an org drops its keyed reads without re-sending them, and re-reads the unkeyed ones", async () => {
    // A switch resets before the screen's observers move to the new org's key
    // (TanStack applies new options in an effect, after the binding's): a
    // refetch of the old org's key would send its request again (main-red
    // control-plane#2954, the Apps list's ListEnvironments after a switch).
    const keyedFetches: string[] = [];
    const unkeyedFetches: string[] = [];
    const keyed = new QueryObserver(queryClient, {
      queryKey: forgeCloudKeys.orgEnvs("org-a"),
      queryFn: () => (keyedFetches.push("org-a"), "a"),
    });
    const unkeyed = new QueryObserver(queryClient, {
      queryKey: forgeCloudKeys.environmentShape(null, "env-prod"),
      queryFn: () => (unkeyedFetches.push("transport org"), "s"),
    });
    const unsubscribe = [keyed.subscribe(() => {}), unkeyed.subscribe(() => {})];
    await waitFor(() => expect([keyedFetches, unkeyedFetches]).toEqual([["org-a"], ["transport org"]]));

    await resetAfterLeavingOrg(queryClient, ["forge"], "org-a");

    expect(keyedFetches).toEqual(["org-a"]);
    expect(queryClient.getQueryCache().find({ queryKey: forgeCloudKeys.orgEnvs("org-a") })).toBeUndefined();
    expect(unkeyedFetches).toEqual(["transport org", "transport org"]);
    unsubscribe.forEach((stop) => stop());
  });
});
