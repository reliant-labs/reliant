/**
 * Route-rendered wrapper for the workflow builder. (The hub it used to also
 * render is the Workflows Library now, at /workflows/library.)
 *
 * Reads its inputs from the URL (route params and search):
 *   /workflow/new              → new blank workflow (isNew={true})
 *   /workflow/$workflowName    → load named workflow (incl. builtin://...)
 *   ?drill=<nodeId>            → one-shot: drill into a loop on load (tour)
 *   ?chat=<chatId>             → the chat shown in the editor's chat panel
 *
 * This component replaces the old isWorkflowMode + workflowToOpen Zustand
 * flags. WorkflowBuilderPage is the underlying implementation; this is just
 * the route adapter.
 */

import {
  useLocation,
  useNavigate,
  useParams,
  useSearch,
} from "@tanstack/react-router";
import { useCallback } from "react";
import { getParentRouteNavigateOptions } from "../../lib/routeParent";
import { WorkflowBuilderPage } from "./WorkflowBuilderPage";

interface WorkflowPageProps {
  isNew?: boolean;
}

export function WorkflowPage({ isNew = false }: WorkflowPageProps) {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const params = useParams({ strict: false }) as { workflowName?: string };
  const search = useSearch({ strict: false }) as { drill?: string; tour?: string; chat?: string };

  // When the onboarding tour is active on a builder step, the user is viewing
  // a builtin workflow as a demo. Treat the workflow as editable in-memory so
  // the UI doesn't show "View Only / Create a Copy" prompts; saves are no-op'd
  // in WorkflowBuilderPage so nothing actually persists.
  const tourMode = search.tour === "workflow-builder";

  // Decode any URL-encoded characters in the workflow name (e.g. builtin://
  // becomes builtin%3A%2F%2F in the URL).
  const workflowName = params.workflowName
    ? decodeURIComponent(params.workflowName)
    : undefined;

  // Close navigates to the logical parent route (see lib/routeParent.ts).
  // router.history.back() is a foot-gun on direct navs: it can leave the SPA
  // entirely when the prior tab entry was an external page.
  const onClose = useCallback(() => {
    navigate(getParentRouteNavigateOptions(pathname));
  }, [navigate, pathname]);

  const onNavigateToSettings = useCallback(() => {
    navigate({ to: "/settings" });
  }, [navigate]);

  const onChatIdChange = useCallback(
    (chat: string | undefined) => {
      navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, chat }),
        replace: true,
      } as never);
    },
    [navigate],
  );

  return (
    <WorkflowBuilderPage
      routeWorkflowName={workflowName}
      routeIsNew={isNew}
      routeDrillIntoNodeId={search.drill}
      routeChatId={search.chat}
      onChatIdChange={onChatIdChange}
      tourMode={tourMode}
      onClose={onClose}
      onNavigateToSettings={onNavigateToSettings}
    />
  );
}
