/**
 * `/m/new` — start a chat.
 *
 * Deliberately the narrow slice of `NewChatView`: a project header, a
 * workflow picker, one message box. Attachments, workflow params, daemon
 * selection, presets, branching and worktree selection are all `false` for
 * this surface, and the point of that list is that none of them appear here.
 *
 * Creation itself goes through `chatStore.createChat` — the same call the
 * desktop composer makes. That is not laziness: `createChat` also seeds the
 * React Query detail and list caches, initializes per-chat store state, plants
 * the optimistic first user message, and marks the chat RUNNING so the
 * thinking indicator shows before the first stream frame. A "simpler" direct
 * `api.chatsV2.create` here would land on `/m/chats/$chatId` with an empty
 * transcript and no spinner until the stream caught up.
 *
 * Worktree: defaults to the project's main worktree, the same default the
 * desktop composer resolves to when the user hasn't switched workspaces. The
 * chat list's per-group "new chat in this workspace" action overrides that
 * via the `worktreeId` search param, so a chat started from a branch group
 * lands in that branch rather than always falling back to main.
 *
 * ## Why the workflow picker is a single row, not 23 full-bleed ones
 *
 * The previous shape rendered every workflow as its own row — on this
 * project that's ~23 rows, which pushed the message composer (the actual
 * point of the screen) below the fold on a 390px phone and made the picker
 * read as the primary content instead of a secondary choice most people
 * leave on its default. Reusing `MobileSelectRow` (the same tap-to-open-sheet
 * primitive the settings panels use for their pickers) collapses that to one
 * row showing the resolved default, with the full list one tap away in
 * `MobileOptionSheet` — the same disclosure a user already learned from
 * Settings, rather than a bespoke "show 5, tap More" list that would be a
 * new pattern for this one screen.
 */

import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { ArrowUp, ChevronLeft, Loader2 } from "lucide-react";
import { useChatStore } from "../../store/chatStore";
import { useProjectStore } from "../../store/projectStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useWorkflows } from "../../store/globalDataStore";
import {
  DEFAULT_WORKFLOW,
  usePreferencesStore,
} from "../../store/preferencesStore";
import {
  getWorkflowDisplayName,
  normalizeWorkflowRef,
} from "../workflow/useWorkflowInputs";
import { trackEvent } from "../../lib/analytics";
import { cn } from "../../lib/utils";
import { MobileCardGroup, MobileScreenHeader } from "./MobileChrome";
import { MobileSelectRow } from "./MobileSettingsRow";

// Workflow refs arrive in two shapes — bare names from ListWorkflows ("agent")
// and URIs from preferences ("builtin://agent"). Every comparison has to
// normalize or the user's default silently fails to match its list entry.
const sameWorkflow = (a: string, b: string) =>
  normalizeWorkflowRef(a) === normalizeWorkflowRef(b);

export function MobileNewChat() {
  const navigate = useNavigate();
  // `strict: false` — nested under `_authenticated` → `_mobile`, same as
  // every other param/search read on this surface.
  const { worktreeId: requestedWorktreeId } = useSearch({ strict: false }) as {
    worktreeId?: string;
  };
  const [message, setMessage] = useState("");
  const [isCreating, setIsCreating] = useState(false);
  const [error, setError] = useState("");

  const currentProject = useProjectStore((s) => s.currentProject);
  const worktrees = useWorktreeStore((s) => s.worktrees);
  const loadWorktrees = useWorktreeStore((s) => s.loadWorktrees);

  const { workflows, loading: workflowsLoading } = useWorkflows();
  const preferences = usePreferencesStore((s) => s.preferences);
  const preferencesLoading = usePreferencesStore((s) => s.isLoading);
  const loadPreferences = usePreferencesStore((s) => s.loadPreferences);
  const isWorkflowHidden = usePreferencesStore((s) => s.isWorkflowHidden);

  const userDefaultWorkflow = preferences?.defaultWorkflow || DEFAULT_WORKFLOW;

  // null means "whatever the user's default is" — resolved at send time so a
  // preferences load that lands after mount is still respected.
  const [selectedWorkflow, setSelectedWorkflow] = useState<string | null>(null);

  useEffect(() => {
    if (!preferences && !preferencesLoading) void loadPreferences();
  }, [preferences, preferencesLoading, loadPreferences]);

  // MobileShell guarantees a project but not its worktrees; without this the
  // main worktree is missing and every send would fail on a phone that landed
  // here directly instead of via the chat list.
  useEffect(() => {
    if (!currentProject) return;
    if (worktrees.length === 0) void loadWorktrees(currentProject.id);
  }, [currentProject, worktrees.length, loadWorktrees]);

  const mainWorktree = worktrees.find((w) => w.is_main && !w.deleted_at);
  const requestedWorktree = requestedWorktreeId
    ? worktrees.find((w) => w.id === requestedWorktreeId && !w.deleted_at)
    : undefined;
  const targetWorktree = requestedWorktree ?? mainWorktree;

  // Same filtering the desktop selector applies: drop hidden workflows and
  // de-duplicate, since project and user scopes can both supply a name.
  const visibleWorkflows = useMemo(() => {
    const seen = new Set<string>();
    const unique = workflows.filter((w) => {
      if (isWorkflowHidden(w.name)) return false;
      const key = normalizeWorkflowRef(w.name).toLowerCase().trim();
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });
    // Default first, then alphabetical — the default is the one tap most
    // users want, so it should never be below the fold.
    return unique.sort((a, b) => {
      if (sameWorkflow(a.name, userDefaultWorkflow)) return -1;
      if (sameWorkflow(b.name, userDefaultWorkflow)) return 1;
      return a.name.localeCompare(b.name);
    });
  }, [workflows, userDefaultWorkflow, isWorkflowHidden]);

  const effectiveWorkflow = selectedWorkflow || userDefaultWorkflow;
  const canSend = message.trim().length > 0 && !isCreating && !!currentProject;

  const handleSend = async () => {
    const content = message.trim();
    if (!content || isCreating || !currentProject) return;

    setIsCreating(true);
    setError("");
    try {
      const chat = await useChatStore
        .getState()
        .createChat(targetWorktree?.id, content, undefined, undefined, effectiveWorkflow);

      trackEvent("chat_created", {
        has_attachments: false,
        workflow: effectiveWorkflow,
        surface: "mobile",
      });

      // Mirror the desktop path: the chat screen renders ChatContainer, which
      // reads activeChatId for its subscriptions.
      useChatStore.getState().selectChat(chat);

      await navigate({ to: "/m/chats/$chatId", params: { chatId: chat.id } });
    } catch (err) {
      // Stay on the screen with the text intact — retyping a prompt on a
      // phone is the worst possible recovery.
      setError(
        err instanceof Error ? err.message : "Could not start the chat",
      );
      setIsCreating(false);
    }
  };

  return (
    <div className="flex h-full min-h-0 flex-col">
      <MobileScreenHeader
        title="New chat"
        subtitle={currentProject?.name ?? "No project"}
        leading={
          <Link
            to="/m/chats"
            // Explicit px, not `h-10 w-10`: rem sizing resolves against the
            // root font-size, and at the smallest Appearance step `h-10`
            // measures under 44px — on the only way out of this screen.
            className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted"
            aria-label="Back to chats"
          >
            <ChevronLeft className="h-5 w-5" />
          </Link>
        }
      />

      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-4">
        {workflowsLoading && visibleWorkflows.length === 0 ? (
          <div className="flex items-center justify-center py-8">
            <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
          </div>
        ) : (
          <MobileCardGroup label="Start with">
            <MobileSelectRow
              label="Workflow"
              value={effectiveWorkflow}
              sheetTitle="Choose a workflow"
              options={visibleWorkflows.map((workflow) => ({
                value: workflow.name,
                label: getWorkflowDisplayName(workflow.name, true),
                description: workflow.description || undefined,
              }))}
              onChange={setSelectedWorkflow}
            />
          </MobileCardGroup>
        )}
      </div>

      {error && (
        <p className="shrink-0 px-4 py-2 text-xs text-destructive">{error}</p>
      )}

      {/* Composer pins to the bottom. This screen has no tab bar, so it
          absorbs the home-indicator inset itself — MobileLayout leaves the
          bottom inset to whichever element actually sits at the edge. */}
      <div
        className="flex shrink-0 items-end gap-2 border-t border-border px-3 py-3"
        style={{ paddingBottom: "calc(0.75rem + env(safe-area-inset-bottom))" }}
      >
        <textarea
          value={message}
          onChange={(e) => setMessage(e.target.value)}
          placeholder="What do you want to do?"
          rows={2}
          aria-label="Message"
          disabled={isCreating}
          className={cn(
            "max-h-40 min-h-14 flex-1 resize-none rounded-lg border border-border bg-background px-3 py-2.5 text-sm text-foreground",
            "placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring/20",
            "disabled:opacity-60",
          )}
        />
        <button
          type="button"
          onClick={() => void handleSend()}
          disabled={!canSend}
          aria-label="Send"
          className="flex h-14 w-14 shrink-0 items-center justify-center rounded-lg bg-primary text-primary-foreground active:opacity-80 disabled:opacity-40"
        >
          {isCreating ? (
            <Loader2 className="h-5 w-5 animate-spin" />
          ) : (
            <ArrowUp className="h-5 w-5" />
          )}
        </button>
      </div>
    </div>
  );
}
