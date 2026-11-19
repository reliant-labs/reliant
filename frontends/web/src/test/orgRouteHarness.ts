// Copyright (c) 2025 Reliant Labs

/**
 * A PRODUCTION ROUTE TREE ON A ROUTER OF ITS OWN, for tests of the org routes
 * (control-plane docs/design/unified-shell-and-projects.md §3, §12). Built
 * for U3 and meant to be reused by U4–U6: it answers "where does this URL
 * land, in which org, and what does a request from that page name" against
 * the trees the app really mounts (routes.tsx `routeTree`, forgeShellRoutes.tsx
 * `forgeRouteTree`), not a substitute tree.
 *
 * Headless: the router loads, runs every `beforeLoad`, follows redirects and
 * commits matches (and so the root's commit hooks, lib/routeOrg.ts) without
 * React, so no page component, guard or overlay renders and nothing needs
 * mocking beyond the network. What a URL renders is read from the committed
 * matches: the leaf route, and the org layout's resolution.
 *
 * The network is one in-memory Connect server (createRouterTransport) behind
 * the app's REAL orgInterceptor, so a request sent from a page is the wire
 * message the control plane would receive:
 *
 *   - OrganizationService.ListMyOrganizations answers `world.orgs` (or
 *     fails), through the app's own listMyOrganizations and query cache. Hold
 *     it (holdMemberships) to keep a navigation loading.
 *   - OrganizationService.ListMembers is the probe for an org-scoped RPC:
 *     every request's `org_id`, as received, is recorded in `wireOrgs`.
 *
 * A test file using it mocks two modules (vi.mock is per file, so the
 * harness cannot): the control-plane config, so the app has orgs at all (it
 * is read at import), and lib/lazyRoute, so the router does not load page
 * chunks it will never render:
 *
 *   vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
 *   vi.mock("@/lib/lazyRoute", async (importOriginal) => ({
 *     ...(await importOriginal<typeof import("@/lib/lazyRoute")>()),
 *     lazyRoute: () => () => null,
 *   }));
 *
 * __tests__/orgRoutes.production.test.ts is the worked example.
 */

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { createMemoryHistory, createRouter, type AnyRoute, type AnyRouter } from "@tanstack/react-router";

import { orgInterceptor } from "@/api/orgInterceptor";
import {
  ListMembersResponseSchema,
  ListMyOrganizationsResponseSchema,
  MyOrganizationSchema,
  OrganizationService,
} from "@/gen/controlplane/services/organization/v1/organization_pb";
import { ChatSchema, ChatService, GetChatResponseSchema } from "@/gen/reliant/v1/chat_pb";
import { setTransport } from "@/lib/connect";
import { orgOfPath } from "@/lib/paths/org";
import { queryClient } from "@/lib/query-client";
import { ORG_ROUTE_ID, useRouteOrgScope, type OrgRouteState } from "@/lib/routeOrg";
import { resetShellForTests } from "@/lib/shell";
import { getControlPlaneClient } from "@/services/controlPlane/client";
import { orgKeys } from "@/services/controlPlane/orgs";
import { bindActiveOrgUser, useActiveOrgStore } from "@/store/activeOrgStore";
import { useAuthStore } from "@/store/authStore";

import { setRuntimeConfig } from "./runtimeConfig";

export interface WorldOrg {
  id: string;
  slug: string;
  name?: string;
}

/** A chat the person may read (ChatService.GetChat answers it); any other id is NotFound. */
export interface WorldChat {
  id: string;
  /** `Chat.org_id`; "" for a chat from before orgs. */
  orgId: string;
  projectId?: string;
}

export interface OrgRouteWorld {
  /** Which host's tree is mounted: the app host (routes.tsx) or the forge host (forgeShellRoutes.tsx). */
  host: "reliant" | "forge";
  routeTree: AnyRoute;
  /** web.org_routes. */
  flag: boolean;
  /** The signed-in user's id; null: nobody signed in. Default "alice". */
  user?: string | null;
  /** The person's memberships, oldest first; the first is their default unless `defaultOrgId` says otherwise. */
  orgs: WorldOrg[];
  defaultOrgId?: string;
  /** ListMyOrganizations fails with this instead of answering. */
  membershipError?: Error;
  /** The org this tab was last in (sessionStorage), as a page load finds it. */
  tabOrg?: string;
  /** The chats GetChat answers (U5's chat URLs); any other id is NotFound. */
  chats?: WorldChat[];
  /** GetChat fails with this instead of answering. */
  chatError?: Error;
}

export interface OrgRouteHarness {
  router: AnyRouter;
  /** Every `org_id` the probe RPC (ListMembers) received, in order. */
  wireOrgs: string[];
  /** Times ListMyOrganizations was answered. */
  membershipCalls: () => number;
  /** Every chat id GetChat was asked for, in order. */
  chatReads: string[];
  /** Load `url` as a fresh page load would, following every redirect. */
  open: (url: string) => Promise<void>;
  /** Reload the page on screen: a fresh page load of the committed URL. */
  refresh: () => Promise<void>;
  /** An in-app navigation; resolves when it has committed or been superseded. */
  navigate: (href: string) => Promise<void>;
  /** The browser's Back and Forward; resolve once the page they land on has loaded. */
  back: () => Promise<void>;
  forward: () => Promise<void>;
  /** Hold ListMyOrganizations until the returned release runs, and drop its cache so the next org load asks. */
  holdMemberships: () => () => void;
  /** Send the probe RPC as a page does with no org of its own: the interceptor fills the org. */
  sendImplicit: () => Promise<string>;
  /** Send the probe RPC naming `orgId` explicitly, as an org-scoped read does (lib/orgScope orgRequest). */
  sendExplicit: (orgId: string | null) => Promise<string>;
  /** The committed URL's pathname. */
  pathname: () => string;
  /** The committed leaf route's id. */
  leafRouteId: () => string;
  /** The committed org layout's resolution, or undefined off the org tree. */
  orgRoute: () => OrgRouteState | undefined;
  /** Whether anything committed as not found. */
  notFound: () => boolean;
  /** The committed leaf's loader data (the chat routes': lib/chatRouteResolve.ts ChatRouteData). */
  leafData: () => unknown;
  /** The committed URL's search, as the routes validated it. */
  search: () => Record<string, unknown>;
}

const deferred = () => {
  let release!: () => void;
  const promise = new Promise<void>((resolve) => {
    release = resolve;
  });
  return { promise, release };
};

export function createOrgRouteHarness(world: OrgRouteWorld): OrgRouteHarness {
  const user = world.user === undefined ? "alice" : world.user;
  const wireOrgs: string[] = [];
  const chatReads: string[] = [];
  let calls = 0;
  let hold: Promise<void> | null = null;

  setRuntimeConfig(world.flag ? { ORG_ROUTES: "true" } : {});
  resetShellForTests(world.host);
  sessionStorage.clear();
  localStorage.clear();
  if (user && world.tabOrg) sessionStorage.setItem(`reliant.activeOrg.${user}`, world.tabOrg);
  queryClient.clear();
  // A failed membership list is an answer, not something to retry for seconds.
  queryClient.setDefaultOptions({ ...queryClient.getDefaultOptions(), queries: { ...queryClient.getDefaultOptions().queries, retry: false } });
  useAuthStore.setState({
    user: user ? ({ id: user, email: `${user}@example.com` } as never) : null,
    session: user ? ({ access_token: "token" } as never) : null,
    initialized: true,
    loading: false,
  });
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null, segments: {} });
  // The auth store binds the signed-in person on every sign-in; a page load finds this tab's org.
  bindActiveOrgUser(user ?? null);

  setTransport(
    createRouterTransport(
      ({ service }) => {
        service(OrganizationService, {
          listMyOrganizations: async () => {
            calls++;
            if (hold) await hold;
            if (world.membershipError) throw world.membershipError;
            return create(ListMyOrganizationsResponseSchema, {
              organizations: world.orgs.map((org) =>
                create(MyOrganizationSchema, {
                  id: org.id,
                  slug: org.slug,
                  name: org.name ?? org.slug,
                  role: "member",
                  isDefault: (world.defaultOrgId ?? world.orgs[0]?.id) === org.id,
                }),
              ),
              defaultOrgId: world.defaultOrgId ?? world.orgs[0]?.id ?? "",
            });
          },
          listMembers: (req) => {
            wireOrgs.push(req.orgId);
            return create(ListMembersResponseSchema, {});
          },
        });
        service(ChatService, {
          getChat: (req) => {
            chatReads.push(req.chatId);
            if (world.chatError) throw world.chatError;
            const chat = world.chats?.find((c) => c.id === req.chatId);
            if (!chat) throw new ConnectError("chat not found", Code.NotFound);
            return create(GetChatResponseSchema, {
              chat: create(ChatSchema, { id: chat.id, projectId: chat.projectId ?? "p-1", title: chat.id, ...(chat.orgId ? { orgId: chat.orgId } : {}) }),
            });
          },
        });
      },
      { transport: { interceptors: [orgInterceptor] } },
    ),
  );

  let router!: AnyRouter;
  const register = (r: AnyRouter) => {
    router = r;
    (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER = r;
  };

  const load = async (url: string) => {
    // A fresh page load: nothing committed yet, so an org URL is pending.
    useRouteOrgScope.setState({ scope: orgOfPath(url.split(/[?#]/, 1)[0]) === null ? { kind: "legacy" } : { kind: "pending" } });
    register(createRouter({ routeTree: world.routeTree, history: createMemoryHistory({ initialEntries: [url] }) }));
    await router.load();
  };

  /**
   * Run a history move and load the page it lands on. Headless, nothing
   * subscribes the router to its history (React's Transitioner does in the
   * app), so the load is started here, as the subscription would.
   */
  const settle = async (move: () => void) => {
    move();
    await router.load();
  };

  const committedOrgLayout = () => router.state.matches.find((match) => match.routeId === ORG_ROUTE_ID);
  const client = () => getControlPlaneClient(OrganizationService);

  return {
    get router() {
      return router;
    },
    wireOrgs,
    chatReads,
    membershipCalls: () => calls,
    open: async (url) => {
      await load(url);
    },
    refresh: async () => {
      // A reload keeps the URL and loses everything held in memory but the query cache's network answers.
      queryClient.clear();
      await load(router.state.location.href);
    },
    navigate: async (href) => {
      await router.navigate({ href });
    },
    back: () => settle(() => router.history.back()),
    forward: () => settle(() => router.history.forward()),
    holdMemberships: () => {
      const gate = deferred();
      hold = gate.promise;
      queryClient.removeQueries({ queryKey: orgKeys.mine });
      return () => {
        hold = null;
        gate.release();
      };
    },
    sendImplicit: async () => {
      await client().listMembers({});
      return wireOrgs[wireOrgs.length - 1]!;
    },
    sendExplicit: async (orgId) => {
      await client().listMembers(orgId ? { orgId } : {});
      return wireOrgs[wireOrgs.length - 1]!;
    },
    pathname: () => router.state.location.pathname,
    leafRouteId: () => router.state.matches[router.state.matches.length - 1]?.routeId ?? "",
    orgRoute: () => (committedOrgLayout()?.context as { orgRoute?: OrgRouteState } | undefined)?.orgRoute,
    notFound: () => router.state.matches.some((match) => match.status === "notFound" || match.globalNotFound),
    leafData: () => router.state.matches[router.state.matches.length - 1]?.loaderData,
    search: () => (router.state.matches[router.state.matches.length - 1]?.search ?? {}) as Record<string, unknown>,
  };
}
