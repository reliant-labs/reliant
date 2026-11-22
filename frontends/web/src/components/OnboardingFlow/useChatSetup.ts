/**
 * The daemonless home's hooks (U6): what chats need from the signed-in person
 * (useChatSetup), and the redirect the chat routes run on it
 * (useChatRouteRedirect). The decisions are pure, in ./onboardingGate.ts.
 *
 * Neither ever mounts the wizard or completes onboarding: they only read the
 * user record and the daemon list, and navigate.
 */

import { useEffect } from "react";
import { useNavigate, useRouterState, useSearch } from "@tanstack/react-router";

import { useCurrentUser, useDaemonList } from "@/hooks/useOnboardingQueries";
import { logger } from "@/lib/logger";
import { isForgeShell } from "@/lib/shell";
import type { IndexSearch } from "@/routeSchemas";

import { chatRouteAction, chatSetup, type ChatSetup } from "./onboardingGate";

/** What chats need from the signed-in person right now (ChatSetup). */
export function useChatSetup(): ChatSetup {
  const { data: user, isLoading: userLoading } = useCurrentUser();
  const { data: daemons, isLoading: daemonsLoading } = useDaemonList();
  return chatSetup({ userLoading, user, daemonsLoading, daemons });
}

/**
 * ModernApp's redirect, on its own routes (`/`, `/project/$projectId`, `/c/…`,
 * `/new`, either form): the home sends a person who cannot chat yet to their projects, and a
 * chat sends a person who has not onboarded through the wizard and back
 * (chatRouteAction). Returns whether the page should hold (a spinner) rather
 * than render: a redirect is on its way, or the home is still checking.
 */
export function useChatRouteRedirect(): { holding: boolean; setup: ChatSetup } {
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const href = useRouterState({ select: (s) => s.location.href });
  // Not strict: the chat routes sit under `/_app` and, in the org tree, `/o/$org/_app`.
  const search = useSearch({ strict: false }) as IndexSearch;
  const chatIntent = search.plan !== undefined || search["reset-onboarding"] === true || search.start !== undefined;
  const { data: user, isLoading: userLoading } = useCurrentUser();
  const setup = useChatSetup();
  const action = chatRouteAction({ pathname, href, chatIntent, userLoading, user, setup, forgeBrand: isForgeShell() });

  const to = action.kind === "go" ? action.to : null;
  const searchKey = action.kind === "go" ? JSON.stringify(action.search) : "";
  useEffect(() => {
    if (to === null) return;
    const next = JSON.parse(searchKey) as Record<string, unknown> | "keep";
    logger.info("[Onboarding] chat route: sending away", { pathname, to, setup });
    void navigate({
      to,
      search: next === "keep" ? (prev: Record<string, unknown>) => prev : next,
      replace: true,
    } as never);
    // `pathname` and `setup` are only logged: the target decides.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [to, searchKey, navigate]);

  return { holding: action.kind !== "stay", setup };
}
