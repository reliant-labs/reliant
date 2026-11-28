/**
 * `/m/new` — start a chat.
 *
 * Shares ChatInput's upload, draft and workflow configuration mechanisms.
 * Machine/workspace choices stay on this route; branching stays unavailable.
 *
 * Where it runs (research/NO_MACHINE_CHATS.md §2.5): one of the user's
 * machines, or No machine — preselected when none of their machines is awake,
 * because waking one from a phone should be a choice. Picking an asleep
 * machine wakes it.
 *
 * Creation itself goes through `chatStore.startChat` — the same call the
 * desktop composer makes. That is not laziness: `startChat` also seeds the
 * React Query detail and list caches, initializes per-chat store state, plants
 * the optimistic first user message, and marks the chat RUNNING so the
 * thinking indicator shows before the first stream frame. A "simpler" direct
 * `startChat` (hooks/chat-actions) here would land on `/m/chats/$chatId` with an empty
 * transcript and no spinner until the stream caught up.
 *
 * ## The canvas is where the chat runs (dev-docs/design/mobile-chat-redesign.md §8)
 *
 * Where a chat runs is the decision that matters before the first send, and it
 * cannot change afterward, so that is what fills the screen above the
 * composer — no suggested prompts, and no model (it lives in the composer's ⊕
 * sheet on every touch layout).
 *
 * Workspace: defaults to the project's main worktree, the same default the
 * desktop composer resolves to when the user hasn't switched workspaces. The
 * first few workspaces (main, then most recent) are radio rows on the canvas;
 * "Show all" opens the full, paged list, and "New workspace"
 * (`worktreeCreate`) opens the same sheet the chat list does. A `worktreeId`
 * search param preselects one; that is how a workspace just created from the
 * chat list arrives here. The group is hidden for No machine, because a branch
 * workspace is a checkout on one machine and a machineless chat always runs in
 * main.
 *
 * With the keyboard up the options scroll away under the composer, so a
 * one-line summary under the header keeps the choice in view; tapping it drops
 * the keyboard and brings the options back.
 *
 * ## Project (owner, 2026-11-24)
 *
 * The Project row picks the project the chat runs in (one primary project;
 * attaching more is the desktop's). It lists the org's projects on a
 * connected machine, naming the machine, with the chat's project first and
 * selected (newChatProjectChoices.ts). The project is the URL's
 * (`?project=`, as desktop `/new?project=`; components/Mobile/mobileScope.ts),
 * so a pick navigates (a push: Back returns to the one before) and the
 * context project follows it (MobileScopeSync). The pick re-keys the draft:
 * the workspace resets to its main one and the machine to its placement (or
 * `?machine=`, the one it was just set up on). A repository on no connected machine is offered as "Set up
 * on <machine>": it is cloned there first (reliant#3420's set-up, in the
 * folder `setUpPathOn` picks), and the chat waits on that machine for it.
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

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ChatInput } from "../Chat/ChatInput";
import { NewChatDraftBoundary, newChatAttachmentDraftId } from "../Chat/NewChatDraftBoundary";
import { useChatParamsStore } from "../../store/chatParamsStore";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { ChevronDown, ChevronLeft, ChevronRight, GitBranchPlus, Loader2 } from "lucide-react";
import { useChatStore } from "../../store/chatStore";
import { useProjectStore } from "../../store/projectStore";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import { sortActiveWorkspaces } from "../Worktrees/workspaceStatus";
import { useWorkflows } from "../../hooks/workflow-queries";
import { isOfferedInChatPicker } from "../../hooks/workflow-queries";
import { useDefaultWorkflow, useHiddenItems } from "../../hooks/settings-queries";
import {
  normalizeWorkflowRef,
} from "../workflow/useWorkflowInputs";
import { workflowDisplayName } from "../../lib/workflowDisplayName";
import { trackEvent } from "../../lib/analytics";
import { MobileCardGroup, MobileScreenHeader } from "./MobileChrome";
import { MobileOptionSheet, MobileSelectRow, type MobileSettingOption } from "./MobileSettingsRow";
import { MobileCreateWorkspaceSheet } from "./MobileCreateWorkspaceSheet";
import { relativeTime } from "./relativeTime";
import { useComposeMode } from "@/lib/composeMode";
import { cn } from "@/lib/utils";
import { useDaemonList } from "@/hooks/useOnboardingQueries";
import { createProjectFromRepo, useProjectDaemonInstalls, useProjects, type Project } from "@/hooks/project-queries";
import { useCapability } from "@/lib/surfaceContext";
import {
  DEFAULT_MACHINE,
  NO_MACHINE,
  NO_MACHINE_COMPOSER_HINT,
  chatMachineOptions,
  defaultChatMachine,
  defaultMachineDaemon,
  isUsableMachineForChat,
  startOptionsForMachine,
} from "@/lib/chatMachine";
import {
  canSetUpElsewhere,
  isSettingUpAnywhere,
  moveSourceDaemonIds,
  preferredDaemonIds,
  projectPlacement,
  sectionMachines,
  setUpPathOn,
  startBlockedReason,
  type PlacementState,
} from "@/lib/projectPlacement";
import { describeCloneError } from "../Projects/cloneTargets";
import { NestedRepoStatus } from "../Projects/NestedRepoStatus";
import { StaysOnMachineList } from "../Projects/StaysOnMachineList";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { type Worktree, useWorktreeList } from "../../hooks/worktree-queries";
import {
  choiceLeavesWorkspace,
  resolveChatMachine,
  startChoice,
  workspaceFilterMachine,
  workspaceMachine,
  workspacesOnMachine,
} from "@/lib/workspaceMachine";
import { daemonLabel } from "../Automations/daemonChoices";
import { mobileChatPath, mobileChatsPath, mobileNewChatPath } from "@/lib/paths";
import { scopeOfSearch } from "@/lib/urlState/shellScope";
import type { SearchRecord } from "@/lib/urlState/searchParams";
import { newChatProjectChoices } from "./newChatProjectChoices";
import { newChatProjectOf, newChatProjectParam } from "./mobileScope";

// Workflow refs arrive in two shapes — bare names from ListWorkflows ("agent")
// and URIs from preferences ("builtin://agent"). Every comparison has to
// normalize or the user's default silently fails to match its list entry.
const sameWorkflow = (a: string, b: string) =>
  normalizeWorkflowRef(a) === normalizeWorkflowRef(b);

/**
 * Workspaces a chat can start in: main first, then most recently active.
 * FAILED ones are left out because they never got a checkout. CREATING ones
 * stay in: a chat can start while its checkout finishes, which is what the
 * desktop composer allows straight after create too.
 *
 * `sortActiveWorkspaces` is the ranking, not the chat list's `buildGroups`:
 * that one ranks groups by their chats, which this screen has not loaded, and
 * a workspace's `last_active` is the same recency the server orders by.
 */
export function startableWorkspaces(worktrees: Worktree[]): Worktree[] {
  return sortActiveWorkspaces(
    worktrees.filter((w) => !w.deleted_at && w.status !== WorktreeStatus.FAILED),
  );
}

/** `startableWorkspaces` as options for the "Show all" sheet. */
export function workspaceOptions(worktrees: Worktree[]): MobileSettingOption[] {
  return startableWorkspaces(worktrees).map((w) => ({
    value: w.id,
    label: w.name,
    description:
      w.status === WorktreeStatus.CREATING
        ? "Setting up…"
        : w.is_main
          ? `${w.branch} · main checkout`
          : w.branch,
  }));
}

/** How many workspaces the canvas lists before "Show all". */
export const INLINE_WORKSPACE_LIMIT = 4;

/**
 * The workspaces listed on the canvas: the first few of `ranked`, and always
 * the selected one — a workspace picked from "Show all" takes the last slot,
 * so the choice that decides where the chat runs never sits off-screen.
 */
export function inlineWorkspaces(
  ranked: Worktree[],
  selectedId: string | undefined,
  limit = INLINE_WORKSPACE_LIMIT,
): Worktree[] {
  const head = ranked.slice(0, limit);
  if (!selectedId || head.some((w) => w.id === selectedId)) return head;
  const selected = ranked.find((w) => w.id === selectedId);
  return selected ? [...head.slice(0, limit - 1), selected] : head;
}

/** Trailing meta on an inline workspace row ("default", "2h ago"). */
function workspaceMeta(worktree: Worktree): string {
  if (worktree.status === WorktreeStatus.CREATING) return "Setting up…";
  if (worktree.is_main) return "default";
  const age = relativeTime(worktree.last_active);
  return !age || age === "now" ? age : `${age} ago`;
}

export function MobileNewChat() {
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as SearchRecord;
  const contextProject = useProjectStore((s) => s.currentProject);
  const { projects, isLoading } = useProjects();
  const listedKey = isLoading ? null : projects.map((p) => p.id).join(",");
  const listed = useMemo(() => (listedKey === null ? undefined : new Set(listedKey.split(",").filter(Boolean))), [listedKey]);
  // The URL names the project (`?project=`, else the list's one project),
  // else it is the context project; the draft below is keyed by it.
  const projectId = newChatProjectOf({
    project: newChatProjectParam(search),
    scope: scopeOfSearch(search),
    contextProjectId: contextProject?.id,
    listed,
  });
  const project = projects.find((p) => p.id === projectId) ?? (contextProject?.id === projectId ? contextProject : undefined);
  if (!projectId) return <MobileScreenHeader title="New chat" subtitle="No project" />;
  if (!project) {
    return (
      <div className="flex h-full min-h-0 flex-col">
        <MobileScreenHeader title="New chat" />
        <div className="flex flex-1 items-center justify-center">
          <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" aria-label="Loading project" />
        </div>
      </div>
    );
  }
  // A pick is the URL's: a push, so Back returns to the project before it.
  // The machine it was set up on rides along; the workspace was the old
  // project's. MobileScopeSync moves the context project after it.
  const chooseProject = (picked: Project, daemonId?: string) => {
    void navigate({
      to: mobileNewChatPath(),
      search: ((prev: SearchRecord) => ({ ...prev, project: picked.id, machine: daemonId, worktreeId: undefined })) as never,
    });
  };
  const machineParam = typeof search.machine === "number" ? String(search.machine) : search.machine;
  return (
    <NewChatDraftBoundary key={project.id} projectId={project.id}>
      <MobileNewChatDraft
        project={project}
        initialMachine={typeof machineParam === "string" && machineParam ? machineParam : null}
        onChooseProject={chooseProject}
      />
    </NewChatDraftBoundary>
  );
}

function MobileNewChatDraft({
  project: currentProject,
  initialMachine,
  onChooseProject,
}: {
  /** The project the chat starts in (the URL's). */
  project: Project;
  /** The selected project's placement or the machine it was just set up on. */
  initialMachine: string | null;
  /** Makes `project` the new chat's project and carries its target machine across the draft reset. */
  onChooseProject: (project: Project, daemonId?: string) => void;
}) {
  const navigate = useNavigate();
  // `strict: false` — nested under `_authenticated` → `_mobile`, same as
  // every other param/search read on this surface.
  const { worktreeId: requestedWorktreeId } = useSearch({ strict: false }) as {
    worktreeId?: string;
  };
  const creatingRef = useRef(false);
  const [isCreating, setIsCreating] = useState(false);
  const [error, setError] = useState("");
  const [createdChatId, setCreatedChatId] = useState<string>();

  const { worktrees, hasMore, isLoadingMore, loadMore } = useWorktreeList(currentProject.id);

  const { workflows, loading: workflowsLoading } = useWorkflows();
  const { isWorkflowHidden } = useHiddenItems();

  const userDefaultWorkflow = useDefaultWorkflow();

  // null means "whatever the user's default is" — resolved at send time so a
  // preferences load that lands after mount is still respected.
  const selectedWorkflow = useChatParamsStore((s) => s.tempNewChatWorkflow);
  const setSelectedWorkflow = (workflow: string) => {
    const store = useChatParamsStore.getState();
    store.setTempNewChatParams({});
    store.setTempNewChatPresets({});
    store.setTempNewChatWorkflow(workflow);
  };

  // The user's pick on this screen wins over the one the URL arrived with.
  const [pickedWorktreeId, setPickedWorktreeId] = useState<string | null>(null);
  const [creatingWorkspace, setCreatingWorkspace] = useState(false);
  const canCreateWorkspace = useCapability("worktreeCreate");

  const mainWorktree = worktrees.find((w) => w.is_main && !w.deleted_at);
  const wantedWorktreeId = pickedWorktreeId ?? requestedWorktreeId;
  const requestedWorktree = wantedWorktreeId
    ? worktrees.find((w) => w.id === wantedWorktreeId && !w.deleted_at)
    : undefined;
  const targetWorktree = requestedWorktree ?? mainWorktree;

  // Same filtering the desktop selector applies: drop hidden workflows and
  // ones a chat cannot start (their Chat trigger is off), and
  // de-duplicate, since project and user scopes can both supply a name.
  const visibleWorkflows = useMemo(() => {
    const seen = new Set<string>();
    const unique = workflows.filter((w) => {
      if (isWorkflowHidden(w.name)) return false;
      if (!isOfferedInChatPicker(w)) return false;
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

  const canPickMachine = useCapability("chatDaemonSelection");
  const { data: daemons = [], isLoading: daemonsLoading } = useDaemonList();
  // Re-read while a clone onto one of the machines is landing (as desktop).
  const [installsPollMs, setInstallsPollMs] = useState<number | false>(false);
  const installs = useProjectDaemonInstalls(true, installsPollMs);
  const installRows = installs.data ?? [];
  const placement = projectPlacement(installRows, currentProject?.id);
  const settingUpAnywhere = isSettingUpAnywhere(installRows, currentProject?.id);
  useEffect(() => {
    setInstallsPollMs(settingUpAnywhere ? 3_000 : false);
  }, [settingUpAnywhere]);
  const [settingUp, setSettingUp] = useState(false);
  const [selectedMachine, setSelectedMachine] = useState<string | null>(initialMachine);
  // A branch workspace is a checkout on one machine: picking it picks its
  // machine, and a machine offers only its own workspaces (lib/workspaceMachine.ts).
  const effectiveChoice = canPickMachine ? selectedMachine : null;
  const resolvedMachine = resolveChatMachine({
    choice: effectiveChoice,
    fallback: defaultChatMachine({ daemons, loading: daemonsLoading, surface: "mobile" }) ?? DEFAULT_MACHINE,
    workspaceDaemonId: workspaceMachine(targetWorktree, new Set(daemons.map((d) => d.daemonId))),
  });
  const machine = resolvedMachine.value;
  const noMachine = machine === NO_MACHINE;
  const workspaceMachineId = workspaceFilterMachine(effectiveChoice, resolvedMachine);
  const rankedWorkspaces = useMemo(
    () => startableWorkspaces(workspacesOnMachine(worktrees, workspaceMachineId)),
    [worktrees, workspaceMachineId],
  );
  const workspaceSheetOptions = useMemo(() => workspaceOptions(rankedWorkspaces), [rankedWorkspaces]);
  const [showingAllWorkspaces, setShowingAllWorkspaces] = useState(false);
  // Choosing a machine that does not hold the picked workspace falls back to
  // the main checkout, which every machine has.
  const handleMachineChange = (next: string) => {
    setSelectedMachine(next);
    if (choiceLeavesWorkspace(next, resolvedMachine.lockedBy)) setPickedWorktreeId(mainWorktree?.id ?? null);
  };
  // DEFAULT_MACHINE sends no daemon (the server picks); show the machine it
  // will land on — an awake one, or one still starting.
  const machineRowValue = machine === DEFAULT_MACHINE ? (defaultMachineDaemon(daemons, preferredDaemonIds(installRows, currentProject?.id))?.daemonId ?? "") : machine;
  // The project's machines first, each saying whether it has the project
  // (lib/projectPlacement.ts); the rest say they are not set up, and picking
  // one offers the set-up below instead of a send that fails.
  const machineOptions = chatMachineOptions(daemons);
  const sections = placement.known
    ? sectionMachines(machineOptions, (o) => o.value, placement)
    : { ready: machineOptions, notSetUp: [] };
  const placementWords: Record<PlacementState, string> = {
    ready: "Ready",
    setting_up: "Setting up…",
    failed: `Set-up failed`,
    absent: `Not set up for ${currentProject?.name ?? "this project"}`,
  };
  const machineRowOptions = [
    ...[...sections.ready, ...sections.notSetUp].map((option) => {
      const status = option.statusLabel === "suspended" ? "suspended · wakes when you send" : option.statusLabel;
      return {
        value: option.value,
        label: option.label,
        description: placement.known ? `${placementWords[placement.stateOn(option.value)]} · ${status}` : status,
      };
    }),
    { value: NO_MACHINE, label: "No machine", description: "Web & integrations · can't read or change files" },
  ];
  const runsOnDaemonId = noMachine || resolvedMachine.lockedBy ? undefined : machineRowValue || undefined;
  const placementBlock =
    canPickMachine && currentProject
      ? startBlockedReason({
          placement,
          daemonId: runsOnDaemonId,
          projectName: currentProject.name,
          machineName: daemonLabel(daemons.find((d) => d.daemonId === runsOnDaemonId), runsOnDaemonId ?? ""),
          canClone: canSetUpElsewhere(currentProject),
        })
      : null;
  const canSetUpHere =
    Boolean(runsOnDaemonId) &&
    canSetUpElsewhere(currentProject) &&
    ["absent", "failed"].includes(placement.stateOn(runsOnDaemonId ?? ""));
  // What a set-up onto runsOnDaemonId leaves on the machine the project is
  // on now (an online one first). Source and target are one pair: the source
  // is chosen for this target, never another, and pinned when the set-up
  // starts (setUpHere), so while the clone runs the list stays on the machine
  // the clone leaves, not on whichever machine is online first by then.
  const isOnlineDaemon = (id: string) => {
    const status = daemons.find((d) => d.daemonId === id)?.status;
    return status === DaemonStatus.ACTIVE || status === DaemonStatus.IDLE;
  };
  const [pinnedMove, setPinnedMove] = useState<{ projectId: string; targetId: string; sourceId: string } | null>(null);
  const settingUpHere =
    Boolean(runsOnDaemonId) && canSetUpElsewhere(currentProject) && placement.stateOn(runsOnDaemonId ?? "") === "setting_up";
  const moveSourceId =
    currentProject && runsOnDaemonId && (canSetUpHere || settingUpHere)
      ? pinnedMove?.projectId === currentProject.id && pinnedMove.targetId === runsOnDaemonId
        ? pinnedMove.sourceId
        : canSetUpHere
          ? moveSourceDaemonIds(installRows, currentProject.id, runsOnDaemonId, isOnlineDaemon)[0]
          : undefined
      : undefined;
  const runsOnNestedRepos = runsOnDaemonId
    ? installRows.find((row) => row.project_id === currentProject?.id && row.daemon_id === runsOnDaemonId)?.nested_repos
    : undefined;
  // A machine's OS, so a set-up clone lands in a folder that machine can create (setUpPathOn).
  const platformOf = (daemonId: string) => daemons.find((d) => d.daemonId === daemonId)?.platform;

  // The Project row. A set-up clones onto the machine this chat would run on,
  // else the machine a chat would default to.
  const { projects } = useProjects();
  const chatDaemonId = noMachine ? undefined : (resolvedMachine.lockedBy ?? (machineRowValue || undefined));
  const setUpDaemonId = chatDaemonId ?? defaultMachineDaemon(daemons.filter(isUsableMachineForChat))?.daemonId;
  const setUpTarget = setUpDaemonId
    ? { daemonId: setUpDaemonId, name: daemonLabel(daemons.find((d) => d.daemonId === setUpDaemonId), setUpDaemonId) }
    : undefined;
  const projectChoices = newChatProjectChoices({
    projects: projects ?? [],
    installs: installRows,
    daemons,
    currentProjectId: currentProject?.id,
    setUpTarget,
  });
  const currentChoice = projectChoices.find((choice) => choice.project.id === currentProject?.id);
  const [settingUpProject, setSettingUpProject] = useState<{ name: string; machine: string } | null>(null);
  const pickProject = async (projectId: string) => {
    const choice = projectChoices.find((c) => c.project.id === projectId);
    if (!choice || projectId === currentProject?.id || choice.kind === "unavailable" || settingUpProject) return;
    if (choice.kind !== "set-up" || !setUpTarget) {
      // The old draft's machine need not hold this project. Carry the
      // selected project's placement into the newly keyed draft instead of
      // falling back to an unrelated default machine.
      onChooseProject(choice.project, choice.machineId);
      return;
    }
    const { project } = choice;
    const sourceId = moveSourceDaemonIds(installRows, project.id, setUpTarget.daemonId, isOnlineDaemon)[0];
    setPinnedMove(sourceId ? { projectId: project.id, targetId: setUpTarget.daemonId, sourceId } : null);
    setSettingUpProject({ name: project.name, machine: setUpTarget.name });
    setError("");
    try {
      const path = setUpPathOn({ installs: installRows, projectId: project.id, path: project.path, daemonId: setUpTarget.daemonId, platformOf });
      await createProjectFromRepo({
        cloneUrl: project.remote_url ?? "",
        daemonId: setUpTarget.daemonId,
        name: project.name,
        ...(path ? { path } : {}),
      });
      // The clone is queued; the chat waits on that machine for it.
      onChooseProject(project, setUpTarget.daemonId);
    } catch (err) {
      setError(describeCloneError(err, setUpTarget.name));
    } finally {
      setSettingUpProject(null);
    }
  };

  const setUpHere = async () => {
    if (!currentProject || !runsOnDaemonId || settingUp) return;
    // The list keeps showing what this set-up leaves, on this source.
    setPinnedMove(moveSourceId ? { projectId: currentProject.id, targetId: runsOnDaemonId, sourceId: moveSourceId } : null);
    setSettingUp(true);
    setError("");
    try {
      const path = setUpPathOn({ installs: installRows, projectId: currentProject.id, path: currentProject.path, daemonId: runsOnDaemonId, platformOf });
      await createProjectFromRepo({
        cloneUrl: currentProject.remote_url ?? "",
        daemonId: runsOnDaemonId,
        name: currentProject.name,
        ...(path ? { path } : {}),
      });
    } catch (err) {
      setError(describeCloneError(err, daemonLabel(daemons.find((d) => d.daemonId === runsOnDaemonId), runsOnDaemonId)));
    } finally {
      setSettingUp(false);
    }
  };
  const handleSend = async (content: string, attachmentIds?: string[], workflow?: string | null, params?: Record<string, unknown>, _thread?: string | null, presets?: Record<string, string | null>) => {
    // ChatInput keeps the draft when this throws, so a blocked or duplicate
    // send never loses the prompt.
    if (creatingRef.current || !currentProject) throw new Error("Chat creation is unavailable");
    if (placementBlock) throw new Error(placementBlock);
    creatingRef.current = true;
    setIsCreating(true);
    setError("");
    try {
      const { daemon_id: daemonId, no_machine: startNoMachine } = startOptionsForMachine(startChoice(resolvedMachine));
      // A chat with no machine runs in the main checkout: a branch workspace
      // is a checkout on one machine.
      const worktreeId = noMachine ? mainWorktree?.id : targetWorktree?.id;
      const chat = await useChatStore
        .getState()
        .startChat(
          worktreeId,
          content,
          attachmentIds,
          params,
          workflow ?? effectiveWorkflow,
          presets ? Object.fromEntries(Object.entries(presets).filter((entry): entry is [string, string] => entry[1] !== null)) : undefined,
          daemonId || startNoMachine ? { daemonId, noMachine: startNoMachine } : undefined,
          // The URL's project, not the store's, which follows it a beat later.
          { projectId: currentProject.id },
        );

      trackEvent("chat_created", {
        has_attachments: !!attachmentIds?.length,
        workflow: effectiveWorkflow,
        surface: "mobile",
        no_machine: noMachine,
      });

      // Mirror the desktop path: the chat screen renders ChatContainer, which
      // reads activeChatId for its subscriptions.
      useChatStore.getState().selectChat(chat);

      setCreatedChatId(chat.id);
      try {
        await navigate({ to: mobileChatPath(chat.id) });
      } catch {
        setError("Chat created. Open it to continue.");
      }
    } catch (err) {
      // Stay on the screen with the text intact — retyping a prompt on a
      // phone is the worst possible recovery.
      setError(
        err instanceof Error ? err.message : "Could not start the chat",
      );
      setIsCreating(false);
      creatingRef.current = false;
      throw err;
    }
  };

  // No workspace group without a machine: a branch workspace is a checkout on
  // one machine and a machineless chat always runs in main. With main as the
  // only choice and nothing to create, the group is one read-only line.
  const showWorkspaces = !noMachine && rankedWorkspaces.length > 0;
  const workspacesReadOnly = rankedWorkspaces.length === 1 && !canCreateWorkspace;
  const listedWorkspaces = inlineWorkspaces(rankedWorkspaces, targetWorktree?.id);
  const canShowAllWorkspaces = hasMore || rankedWorkspaces.length > listedWorkspaces.length;
  const pickWorkspace = (id: string) => {
    setPickedWorktreeId(id);
    setShowingAllWorkspaces(false);
  };

  const workflowEntry = visibleWorkflows.find((workflow) => sameWorkflow(workflow.name, effectiveWorkflow));
  // Keyboard up: the viewport is ~400px and the options scroll away under the
  // composer, so where the chat will run stays visible as one line.
  const composeMode = useComposeMode();
  const canvasRef = useRef<HTMLDivElement>(null);
  const summary = [
    (noMachine ? mainWorktree : targetWorktree)?.name,
    noMachine
      ? "No machine"
      : canPickMachine && !daemonsLoading
        ? machineRowOptions.find((option) => option.value === machineRowValue)?.label
        : undefined,
    workflowDisplayName(workflowEntry ?? { name: effectiveWorkflow }),
  ]
    .filter(Boolean)
    .join(" · ");
  const revealOptions = () => {
    // Blurring the composer drops the keyboard, which ends compose mode.
    if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
    if (canvasRef.current) canvasRef.current.scrollTop = 0;
  };

  return (
    <div className="flex h-full min-h-0 flex-col">
      <MobileScreenHeader
        title="New chat"
        subtitle={currentProject?.name ?? "No project"}
        leading={
          <Link
            to={mobileChatsPath()}
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
      {composeMode && summary && (
        <button
          type="button"
          onClick={revealOptions}
          aria-label={`Chat options: ${summary}. Show options`}
          data-testid="mobile-new-chat-summary"
          className="flex min-h-[44px] w-full shrink-0 items-center gap-1 border-b border-border bg-background px-4 text-left text-meta text-muted-foreground active:bg-foreground/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
        >
          <span className="min-w-0 flex-1 truncate">{summary}</span>
          <ChevronDown className="h-4 w-4 shrink-0" aria-hidden="true" />
        </button>
      )}

      <div ref={canvasRef} className="mx-auto min-h-0 w-full max-w-3xl flex-1 space-y-4 overflow-y-auto px-4 py-4">
        {currentProject && (
          <div data-testid="mobile-new-chat-project">
          <MobileCardGroup>
            <MobileSelectRow
              label="Project"
              description={currentChoice?.description}
              value={currentProject.id}
              placeholder={currentProject.name}
              sheetTitle="Which project"
              options={projectChoices.map((choice) => ({
                value: choice.project.id,
                label: choice.project.name,
                description: choice.description,
                disabled: choice.kind === "unavailable" && choice.project.id !== currentProject.id,
              }))}
              onChange={(id) => void pickProject(id)}
            />
          </MobileCardGroup>
          </div>
        )}
        {showWorkspaces &&
          (workspacesReadOnly ? (
            <MobileCardGroup label="Workspace">
              <div className="flex min-h-[44px] items-center justify-between gap-3 px-4 py-3 text-ui text-foreground">
                <span className="min-w-0 truncate">{rankedWorkspaces[0].name}</span>
                <span className="shrink-0 text-meta text-muted-foreground">{workspaceMeta(rankedWorkspaces[0])}</span>
              </div>
            </MobileCardGroup>
          ) : (
            <MobileCardGroup label="Workspace" className="divide-y divide-border">
              <div role="radiogroup" aria-label="Workspace" className="divide-y divide-border">
                {listedWorkspaces.map((worktree) => (
                  <WorkspaceRadioRow
                    key={worktree.id}
                    worktree={worktree}
                    checked={worktree.id === targetWorktree?.id}
                    onSelect={() => pickWorkspace(worktree.id)}
                  />
                ))}
              </div>
              {canCreateWorkspace && (
                <button
                  type="button"
                  onClick={() => setCreatingWorkspace(true)}
                  className={cn(CARD_ROW, "font-medium text-primary-ink")}
                >
                  <GitBranchPlus className="h-4 w-4 shrink-0" aria-hidden="true" />
                  New workspace
                </button>
              )}
              {canShowAllWorkspaces && (
                <button
                  type="button"
                  onClick={() => setShowingAllWorkspaces(true)}
                  className={cn(CARD_ROW, "justify-between text-foreground")}
                >
                  <span>
                    Show all ({rankedWorkspaces.length}
                    {hasMore ? "+" : ""})
                  </span>
                  <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                </button>
              )}
            </MobileCardGroup>
          ))}

        <MobileCardGroup>
          {canPickMachine && !daemonsLoading && (
            <MobileSelectRow
              label="Runs on"
              value={machineRowValue}
              sheetTitle="Where this chat runs"
              options={machineRowOptions}
              onChange={handleMachineChange}
            />
          )}
          {workflowsLoading && visibleWorkflows.length === 0 ? (
            <div className="flex min-h-[44px] items-center justify-center py-3">
              <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" aria-label="Loading workflows" />
            </div>
          ) : (
            <MobileSelectRow
              label="Workflow"
              value={workflowEntry?.name ?? effectiveWorkflow}
              sheetTitle="Choose a workflow"
              options={visibleWorkflows.map((workflow) => ({
                value: workflow.name,
                label: workflowDisplayName(workflow),
                description: workflow.description || undefined,
              }))}
              onChange={setSelectedWorkflow}
            />
          )}
        </MobileCardGroup>

        {canPickMachine && resolvedMachine.lockedBy && daemons.length > 1 && (
          <p role="note" className="px-1 text-meta text-muted-foreground">
            Runs on{" "}
            {daemonLabel(daemons.find((d) => d.daemonId === resolvedMachine.lockedBy), resolvedMachine.lockedBy)}, where
            this workspace lives.
          </p>
        )}
        {noMachine && (
          <p role="note" className="px-1 text-meta text-muted-foreground">
            {NO_MACHINE_COMPOSER_HINT}
          </p>
        )}
      </div>

      {/* Composer pins to the bottom with what stands between the user and a
          send as status lines right above it. No bordered footer: the
          composer card is the only frame. This screen has no tab bar, so it
          absorbs the home-indicator inset itself — MobileLayout leaves the
          bottom inset to whichever element actually sits at the edge. */}
      <div
        className="shrink-0 px-3 pt-2"
        style={{ paddingBottom: "calc(0.5rem + var(--mobile-safe-bottom, env(safe-area-inset-bottom)))" }}
      >
        <div className="mx-auto w-full max-w-3xl min-w-0">
          {placementBlock && (
            <StatusLine data-testid="mobile-new-chat-placement">
              <p role="status">{placementBlock}</p>
              <NestedRepoStatus repos={runsOnNestedRepos} className="mt-1" />
              {currentProject && moveSourceId && (
                <StaysOnMachineList
                  className="mt-2"
                  projectId={currentProject.id}
                  daemonId={moveSourceId}
                  machineName={
                    installRows.find((row) => row.daemon_id === moveSourceId)?.daemon_name ||
                    daemonLabel(daemons.find((d) => d.daemonId === moveSourceId), moveSourceId)
                  }
                />
              )}
              {canSetUpHere && (
                <button
                  type="button"
                  onClick={() => void setUpHere()}
                  disabled={settingUp}
                  className="mt-2 flex min-h-[44px] w-full items-center justify-center gap-2 rounded-md border border-border font-medium text-primary-ink active:bg-foreground/5 disabled:opacity-60"
                >
                  {settingUp && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
                  Add to {daemonLabel(daemons.find((d) => d.daemonId === runsOnDaemonId), runsOnDaemonId ?? "")}
                </button>
              )}
            </StatusLine>
          )}
          {settingUpProject && (
            <StatusLine data-testid="mobile-new-chat-project-setup">
              <p role="status" className="flex items-center gap-2">
                <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
                Setting up {settingUpProject.name} on {settingUpProject.machine}…
              </p>
            </StatusLine>
          )}
          {error && (
            <StatusLine tone="destructive">
              <p role="alert">{error}</p>
            </StatusLine>
          )}
          {createdChatId && (
            <StatusLine>
              <Link
                to={mobileChatPath(createdChatId)}
                className="flex min-h-[44px] items-center font-medium text-primary-ink"
              >
                Open created chat
              </Link>
            </StatusLine>
          )}
          <ChatInput attachmentDraftId={newChatAttachmentDraftId(currentProject!.id)} onSend={handleSend} disabled={isCreating || !currentProject || !!placementBlock || !!settingUpProject} placeholder="What do you want to do?" />
        </div>
      </div>

      {showingAllWorkspaces && (
        <MobileOptionSheet
          title="Which workspace"
          options={workspaceSheetOptions}
          value={targetWorktree?.id ?? ""}
          onSelect={pickWorkspace}
          onClose={() => setShowingAllWorkspaces(false)}
          paging={{ hasMore, isLoadingMore, onLoadMore: () => void loadMore() }}
          action={
            canCreateWorkspace
              ? {
                  label: "New workspace",
                  icon: <GitBranchPlus className="h-4 w-4" />,
                  // Close first: two stacked bottom sheets read as one stuck
                  // on top of the other.
                  onSelect: () => {
                    setShowingAllWorkspaces(false);
                    setCreatingWorkspace(true);
                  },
                }
              : undefined
          }
        />
      )}

      {creatingWorkspace && currentProject && (
        <MobileCreateWorkspaceSheet
          projectId={currentProject.id}
          daemonId={workspaceMachineId}
          onClose={() => setCreatingWorkspace(false)}
          onCreated={(worktree) => {
            // The store has already appended it, so selecting it by id
            // resolves on the next render.
            setPickedWorktreeId(worktree.id);
            setCreatingWorkspace(false);
          }}
        />
      )}
    </div>
  );
}

/** A 44px action row inside a `MobileCardGroup`, matching `MobileSelectRow`. */
const CARD_ROW =
  "flex min-h-[44px] w-full items-center gap-3 px-4 py-3 text-left text-ui active:bg-foreground/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring";

/**
 * One workspace on the canvas. Radio semantics because exactly one is
 * selected and one tap selects it; a sheet per choice is what this replaced.
 */
function WorkspaceRadioRow({
  worktree,
  checked,
  onSelect,
}: {
  worktree: Worktree;
  checked: boolean;
  onSelect: () => void;
}) {
  const meta = workspaceMeta(worktree);
  return (
    <button type="button" role="radio" aria-checked={checked} onClick={onSelect} className={cn(CARD_ROW, "text-foreground")}>
      <span
        aria-hidden="true"
        className={cn(
          "flex h-4 w-4 shrink-0 items-center justify-center rounded-full border-2",
          checked ? "border-primary" : "border-muted-foreground/50",
        )}
      >
        {checked && <span className="h-1.5 w-1.5 rounded-full bg-primary" />}
      </span>
      <span className={cn("min-w-0 flex-1 truncate", checked && "font-medium")}>{worktree.name}</span>
      {meta && <span className="shrink-0 text-meta text-muted-foreground">{meta}</span>}
    </button>
  );
}

/**
 * A line above the composer for what stands between the user and the chat:
 * a placement block, a failed send, a created chat to open. Kept local — the
 * chat screen's status slot is its own component with its own lifecycle.
 */
function StatusLine({
  tone = "muted",
  children,
  "data-testid": testId,
}: {
  tone?: "muted" | "destructive";
  children: ReactNode;
  "data-testid"?: string;
}) {
  return (
    <div
      data-testid={testId}
      className={cn(
        "px-1 pb-2 text-meta",
        tone === "destructive" ? "text-destructive-ink" : "text-muted-foreground",
      )}
    >
      {children}
    </div>
  );
}
