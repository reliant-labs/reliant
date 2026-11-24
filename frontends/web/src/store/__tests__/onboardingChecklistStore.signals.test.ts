/**
 * The Setup Guide's items complete on real signals: what the app already
 * caches, written as any other reader's refetch would write it. Nothing here
 * clicks the guide, and a click is never what completes an item.
 *
 * A REAL QueryClient (the app's singleton), so the cache subscriptions in
 * subscribeToStoreChanges are exercised end to end.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";

vi.mock("../../lib/settingsPersistence", () => ({
  readSetting: vi.fn(async () => ({ status: "missing" })),
  upsertStringSetting: vi.fn(async () => undefined),
  deleteSettingIfExists: vi.fn(async () => undefined),
  safeGetSetting: vi.fn(async () => null),
}));
vi.mock("../../lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}));
vi.mock("../projectStore", () => ({
  useProjectStore: {
    getState: () => ({ currentProject: { id: "project-1" } }),
    subscribe: vi.fn(() => () => undefined),
  },
}));
vi.mock("../../lib/events", () => {
  const bus = { on: vi.fn(() => () => undefined), emit: vi.fn() };
  return { getEventBus: () => bus, initEventBus: () => bus };
});
vi.mock("../../hooks/chat-queries", () => ({
  chatKeys: { all: ["chats"] },
  getCachedChatList: () => [],
}));
vi.mock("../authStore", () => {
  const state = { session: null };
  const useAuthStore = <T,>(selector: (s: typeof state) => T): T => selector(state);
  useAuthStore.getState = () => state;
  return { useAuthStore };
});
vi.mock("../../lib/analytics", () => ({ trackEvent: vi.fn() }));

import { queryClient } from "../../lib/query-client";
import { DAEMON_LIST_QUERY_KEY } from "../../hooks/daemon-registry-queries";
import { projectServiceKeys } from "../../hooks/project-service-hooks_gen";
import {
  DaemonInfoSchema,
  DaemonStatus,
  ListDaemonsResponseSchema,
} from "../../gen/reliant/v1/daemon_registry_pb";
import {
  HostedAppSchema,
  HostedAppsState,
  ListProjectsWithAppsResponseSchema,
  ProjectSchema,
  ProjectWithAppsSchema,
  ProjectRepoSummarySchema,
} from "../../gen/reliant/v1/project_pb";
import { memberKeys } from "../../services/controlPlane/members";
import { trackEvent } from "../../lib/analytics";
import { useOnboardingChecklistStore } from "../onboardingChecklistStore";

let unsubscribe: () => void = () => undefined;

beforeEach(() => {
  queryClient.clear();
  vi.mocked(trackEvent).mockClear();
  useOnboardingChecklistStore.setState({ completedItems: new Set(), panelState: "collapsed", isInitialized: true } as never);
  unsubscribe = useOnboardingChecklistStore.getState().subscribeToStoreChanges();
});
afterEach(() => unsubscribe());

const done = () => useOnboardingChecklistStore.getState().completedItems;

function catalog(init: Parameters<typeof create<typeof ListProjectsWithAppsResponseSchema>>[1]) {
  queryClient.setQueryData(
    projectServiceKeys.listProjectsWithApps({}),
    create(ListProjectsWithAppsResponseSchema, init),
  );
}

describe("Setup Guide signals", () => {
  it("merges pending detection with achievements earned while it awaited reads", async () => {
    unsubscribe();
    catalog({
      hostedAppsState: HostedAppsState.OK,
      unlinkedApps: [create(HostedAppSchema, { name: "site", lastDeployedAt: "2026-11-20T10:00:00Z" })],
    });
    // Detection finds the app and then yields at its tour import.
    const pending = useOnboardingChecklistStore.getState().detectCompletedItems();
    await useOnboardingChecklistStore.getState().markComplete("start-chat");
    await pending;
    expect(done()).toEqual(new Set(["deploy-app", "start-chat"]));
  });

  it("connect-machine: the daemon list holds a usable machine, not a disconnected one", () => {
    queryClient.setQueryData(DAEMON_LIST_QUERY_KEY, create(ListDaemonsResponseSchema, {
      daemons: [create(DaemonInfoSchema, { status: DaemonStatus.DISCONNECTED })],
    }));
    expect(done().has("connect-machine")).toBe(false);

    queryClient.setQueryData(DAEMON_LIST_QUERY_KEY, create(ListDaemonsResponseSchema, {
      daemons: [create(DaemonInfoSchema, { status: DaemonStatus.ACTIVE })],
    }));
    expect(done().has("connect-machine")).toBe(true);
    expect(trackEvent).toHaveBeenCalledWith("checklist_step_completed", expect.objectContaining({ stepId: "connect-machine" }));
  });

  it("add-repo: a project of your own with a git remote; a local folder or a shared project is not one", () => {
    catalog({
      hostedAppsState: HostedAppsState.OK,
      projects: [
        create(ProjectWithAppsSchema, { project: create(ProjectSchema, { id: "p1", name: "my-project" }) }),
        create(ProjectWithAppsSchema, {
          project: create(ProjectSchema, { id: "p2", name: "theirs", viewerAccess: true }),
          repos: [create(ProjectRepoSummarySchema, { id: "r", name: "theirs", remote: "github.com/acme/theirs" })],
        }),
      ],
    });
    expect(done().has("add-repo")).toBe(false);

    catalog({
      hostedAppsState: HostedAppsState.OK,
      projects: [
        create(ProjectWithAppsSchema, {
          project: create(ProjectSchema, { id: "p3", name: "shop" }),
          repos: [create(ProjectRepoSummarySchema, { id: "r3", name: "shop", remote: "github.com/me/shop" })],
        }),
      ],
    });
    expect(done().has("add-repo")).toBe(true);
  });

  it("deploy-app: an app in the catalog has been deployed", () => {
    catalog({ hostedAppsState: HostedAppsState.OK, unlinkedApps: [create(HostedAppSchema, { name: "site" })] });
    expect(done().has("deploy-app")).toBe(false);

    catalog({
      hostedAppsState: HostedAppsState.OK,
      unlinkedApps: [create(HostedAppSchema, { name: "site", lastDeployedAt: "2026-11-20T10:00:00Z" })],
    });
    expect(done().has("deploy-app")).toBe(true);
  });

  it("invite-teammate: a second member or a pending invitation, as the Members page caches them", () => {
    queryClient.setQueryData(memberKeys.members("org-1"), {
      pages: [{ members: [], nextPageToken: "", seats: { members: 1, pendingInvitations: 0, limit: -1 } }],
      pageParams: [""],
    });
    expect(done().has("invite-teammate")).toBe(false);

    queryClient.setQueryData(memberKeys.invitations("org-1"), [{ id: "i1", email: "sam@acme.dev", status: "pending" }]);
    expect(done().has("invite-teammate")).toBe(true);
  });
});
