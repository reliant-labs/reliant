import { useState, useEffect, useMemo, useRef, useCallback } from "react";
import { useLocation, useNavigate } from "@tanstack/react-router";
import { getParentRouteNavigateOptions } from "../../lib/routeParent";
import {
  WorkflowBuilder,
  type SaveResult,
  type StatusChangeResult,
} from "./WorkflowBuilder";
import {
  isCompleteSaveRejection,
  splitFindings,
  type DraftStatus,
} from "./workflowDraftStatus";
import { WorkflowParseErrorView } from "./WorkflowParseErrorView";
import { WorkflowHeader } from "./WorkflowHeader";
import { LoadingSpinner } from "../Layout/LoadingSpinner";
import type { Workflow } from "../../types/workflow";
import type {
  Step as ResponseStep,
  Edge as ResponseEdge,
} from "../../api/workflow-grpc";
import {
  workflowGrpc,
  getWorkflowWithDraftId,
  type WorkflowResponse,
} from "../../api/workflow-grpc";
import { NewWorkflowDialog } from "./NewWorkflowDialog";
import { toast } from "sonner";
import {
  useGlobalDataStore,
  useWorkflows,
  type WorkflowDef,
} from "../../store/globalDataStore";
import { useProjectStore } from "../../store/projectStore";
import { trackEvent } from "../../lib/analytics";

interface WorkflowBuilderPageProps {
  selectedWorkflow?: Workflow | null;
  onWorkflowChange?: (workflow: Workflow) => void;
  /** Workflow name from the URL route (`/workflow/$workflowName`). */
  routeWorkflowName?: string;
  /** When true, the route is `/workflow/new`: the New workflow dialog. */
  routeIsNew?: boolean;
  /**
   * Whether the route has finished resolving a project (?project=, then the
   * last-used one). Until then a builder link with no project shows the
   * loading screen; after, it goes to the Library to pick one.
   */
  projectResolved?: boolean;
  /** One-shot drill target from `?drill=`. Forwarded to WorkflowBuilder. */
  routeDrillIntoNodeId?: string;
  /** Chat shown in the editor's chat panel, from `?chat=`. */
  routeChatId?: string;
  /** Update `?chat=` (undefined clears it). */
  onChatIdChange?: (chatId: string | undefined) => void;
  /**
   * When true, the onboarding tour is active on a builder step. The page treats
   * the loaded workflow (typically a builtin like `get-it-right`) as editable
   * in-memory: the "View Only / Create a Copy" banner is suppressed and saves
   * no-op with a toast. Nothing actually persists.
   */
  tourMode?: boolean;
  /** Close handler (router back / navigate to /). */
  onClose?: () => void;
  /** Navigate to /settings. */
  onNavigateToSettings?: () => void;
}

// Helper to convert WorkflowDef (from global store) to WorkflowResponse format
function workflowDefToResponse(def: WorkflowDef): WorkflowResponse {
  return {
    name: def.name,
    filename: def.filename,
    description: def.description,
    stepCount: def.step_count,
    source: def.source,
    nodes: [], // Hub only needs metadata, not full nodes
    edges: [],
    isHidden: def.is_hidden || false,
    // The cached (chat-safe) list only holds runnable workflows; drafts and
    // their findings arrive with the detailed (include_hidden) listing.
    status: def.status ?? "complete",
    validationErrors: [],
  };
}

export function WorkflowBuilderPage({
  selectedWorkflow,
  onWorkflowChange,
  routeWorkflowName,
  routeIsNew = false,
  projectResolved = true,
  routeDrillIntoNodeId,
  routeChatId,
  onChatIdChange,
  tourMode = false,
  onClose,
  onNavigateToSettings,
}: WorkflowBuilderPageProps) {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const [editingWorkflow, setEditingWorkflow] = useState<Workflow | undefined>(
    selectedWorkflow || undefined,
  );
  // isReadOnly is true for builtin and project workflows (cannot be saved directly)
  const [isReadOnly, setIsReadOnly] = useState(false);
  // Track workflow source and metadata for info popover
  const [workflowSource, setWorkflowSource] = useState<
    "builtin" | "user" | "project"
  >("user");
  const [workflowVersion, setWorkflowVersion] = useState<number>(0);
  // Lifecycle of the stored workflow. New workflows start as drafts; builtin
  // and project workflows are always complete (and read-only here).
  const [draftStatus, setDraftStatus] = useState<DraftStatus>("draft");
  // Track draft ID - loaded from existing workflow (for LLM tool calls)
  const [draftId, setDraftId] = useState<string | undefined>(undefined);

  // Track initial name for new workflows (kept for forward-compat; the route
  // adapter no longer assigns to this, but the builder still receives it as a
  // prop).
  const [initialWorkflowName] = useState<string | undefined>(undefined);

  // Canonical YAML definition from the backend (used by YAML modal instead of frontend serializer)
  const [yamlDefinition, setYamlDefinition] = useState<string | undefined>(
    undefined,
  );

  // Parse error state - when the stored YAML couldn't be parsed
  const [parseError, setParseError] = useState<string | undefined>(undefined);
  const [rawDefinition, setRawDefinition] = useState<string | undefined>(
    undefined,
  );
  const [errorWorkflowName, setErrorWorkflowName] = useState<
    string | undefined
  >(undefined);

  // View is derived: a parse error from the last load shows the error view
  // (so the user can recover), anything else the builder. There is no hub
  // view any more — the Library (/workflows/library) replaced it.
  const currentView: "builder" | "error" = parseError ? "error" : "builder";

  // Set by WorkflowBuilder to "save the current canvas as a draft" — the
  // action a rejected save of a complete workflow offers from its toast.
  const saveAsDraftRef = useRef<(() => Promise<void>) | null>(null);
  // Track last workflow that failed to save due to OCC conflict (for force save retry)
  // (Variable removed - was unused)
  // Local workflow state for detailed data (updated_at, full steps, etc.)
  const [detailedWorkflows, setDetailedWorkflows] = useState<
    Map<string, WorkflowResponse>
  >(new Map());
  // Track invalid workflows that failed to load
  const currentProject = useProjectStore((state) => state.currentProject);
  const projectId = currentProject?.id;

  // Use cached workflows from global store for immediate display
  const { workflows: cachedWorkflows } = useWorkflows();

  // Get presets from global store
  const presets = useGlobalDataStore((state) => state.presets);
  const refetchPresets = useGlobalDataStore((state) => state.refetchPresets);

  // Ensure presets are loaded when component mounts with a projectId
  useEffect(() => {
    if (projectId && presets.length === 0) {
      refetchPresets(projectId);
    }
  }, [projectId, presets.length, refetchPresets]);

  // Merge cached workflows with any detailed data we've fetched
  const existingWorkflows = useMemo(() => {
    const workflowMap = new Map<string, WorkflowResponse>();

    // First, add all cached workflows
    cachedWorkflows.forEach((def) => {
      const detailed = detailedWorkflows.get(def.name);
      if (detailed) {
        workflowMap.set(def.name, { ...detailed, source: def.source });
      } else {
        workflowMap.set(def.name, workflowDefToResponse(def));
      }
    });

    // Then, add any detailed workflows that aren't in cache yet (newly saved workflows)
    detailedWorkflows.forEach((detailed, name) => {
      if (!workflowMap.has(name)) {
        workflowMap.set(name, detailed);
      }
    });

    return Array.from(workflowMap.values());
  }, [cachedWorkflows, detailedWorkflows]);

  // Helper to load detailed workflow data (includeHidden for management UI)
  const loadDetailedWorkflows = useCallback(async () => {
    if (!projectId) return;
    try {
      const result = await workflowGrpc.listWorkflowsWithErrors(
        projectId,
        true,
      ); // includeHidden: the builder resolves hidden workflows too
      const detailed = new Map<string, WorkflowResponse>();
      result.workflows.forEach((w) => detailed.set(w.name, w));
      setDetailedWorkflows(detailed);
    } catch (err) {
      console.error("Failed to load detailed workflows:", err);
    }
  }, [projectId]);

  // Centralized refresh — always fetch global store + detailed data together so
  // sites can't accidentally update only one. Best-effort: errors are logged
  // rather than thrown to preserve callsite behavior.
  const refreshWorkflowList = useCallback(async () => {
    if (!projectId) return;
    await Promise.all([
      useGlobalDataStore
        .getState()
        .refetchWorkflows(projectId)
        .catch((error) => {
          console.warn("Failed to refetch workflows:", error);
        }),
      loadDetailedWorkflows(),
    ]);
  }, [projectId, loadDetailedWorkflows]);

  // Background fetch for detailed workflow data (updated_at, etc.)
  useEffect(() => {
    if (!projectId) return;
    loadDetailedWorkflows();

    // Listen for workflow saves to refresh the detailed data
    const handleWorkflowSaved = () => {
      refreshWorkflowList();
    };
    window.addEventListener("workflow-saved", handleWorkflowSaved);
    return () =>
      window.removeEventListener("workflow-saved", handleWorkflowSaved);
  }, [projectId, loadDetailedWorkflows, refreshWorkflowList]);

  // Keep editingWorkflow in sync if a parent passes a different selectedWorkflow
  // after mount. (Embedded usage path; the route-driven path sets editingWorkflow
  // in the loadWorkflow effect below.)
  useEffect(() => {
    if (selectedWorkflow) {
      setEditingWorkflow(selectedWorkflow);
    }
  }, [selectedWorkflow]);

  // What this page has already loaded (or just saved), as loadKey(). A save
  // that renames the workflow navigates to its new slug; the canvas already
  // holds that workflow, so the route change must not reload it.
  const loadedRouteNameRef = useRef<string | undefined>(undefined);
  const loadKey = (name: string) => `${projectId ?? ""}::${tourMode}::${name}`;

  // Drive view state from the URL route:
  //   routeIsNew === true                       → the New workflow dialog
  //   routeWorkflowName === undefined && !isNew → nothing to load
  //   routeWorkflowName set                     → load that workflow
  // The route is the source of truth; no flag to clear afterwards. Nothing
  // here CREATES anything: an effect can run twice (StrictMode, or any
  // dependency change), so a create belongs to a click — see NewWorkflowDialog.
  useEffect(() => {
    if ((routeIsNew || routeWorkflowName) && !projectId) {
      // A link to the builder with no project. Wait while the route resolves
      // one (useRouteProjectResolution: ?project=, then the last project);
      // if none resolves, the Library is where a project is chosen. Rendering
      // builder chrome meanwhile would lie about what the user is looking at.
      if (projectResolved) {
        navigate({ to: "/workflows/library", replace: true });
      }
      return;
    }

    if (routeIsNew) {
      loadedRouteNameRef.current = undefined;
      return;
    }

    if (!routeWorkflowName) {
      // No workflow name and not "new": nothing to load.
      // Clear any lingering editor data so the next time the user opens a
      // workflow they don't see a flash of the previous one.
      setEditingWorkflow(undefined);
      setYamlDefinition(undefined);
      setParseError(undefined);
      return;
    }

    if (!projectId) return;

    const workflowName = routeWorkflowName;
    if (loadedRouteNameRef.current === loadKey(workflowName)) return;
    let cancelled = false;
    const loadWorkflow = async () => {
      try {
        const {
          workflow,
          draftId: loadedDraftId,
          version: loadedVersion,
          parseError: loadedParseError,
          rawDefinition: loadedRawDefinition,
          yamlDefinition: loadedYamlDef,
          status: loadedStatus,
          source: loadedSource,
        } = await getWorkflowWithDraftId(projectId, workflowName);
        // A response for a route we already left (a rename navigated away)
        // must not touch the page — least of all with a 404 for the old name.
        if (cancelled) return;
        loadedRouteNameRef.current = loadKey(workflowName);
        setDraftStatus(loadedStatus);

        // Handle parse error - show error view with chat available
        if (loadedParseError) {
          setParseError(loadedParseError);
          setRawDefinition(loadedRawDefinition);
          setErrorWorkflowName(workflowName);
          setDraftId(loadedDraftId);
          return;
        }

        if (workflow) {
          // Clear any previous parse error state
          setParseError(undefined);
          setRawDefinition(undefined);
          setErrorWorkflowName(undefined);

          // GetWorkflow says where it came from. (This used to look the name
          // up in the workflow LIST, which made the list an input of this
          // effect: every save refreshed the list and re-ran the load — by the
          // old name, after a rename, which 404s.)
          const isBuiltinWorkflow = loadedSource === "builtin" || workflowName.startsWith("builtin://");
          const isProjectWorkflow = loadedSource === "project";

          setEditingWorkflow(workflow);
          setWorkflowVersion(loadedVersion);
          // In tour mode, force editable so the user sees what editing feels
          // like on a builtin demo (e.g. get-it-right). Saves are blocked in
          // handleSave below — nothing actually persists.
          setIsReadOnly((isBuiltinWorkflow || isProjectWorkflow) && !tourMode);
          setWorkflowSource(
            isBuiltinWorkflow
              ? "builtin"
              : isProjectWorkflow
                ? "project"
                : "user",
          );
          setDraftId(loadedDraftId);
          setYamlDefinition(loadedYamlDef);
        } else {
          toast.error(`Workflow "${workflowName}" not found`);
          // Workflow was deleted or does not exist - back to the Library
          navigate({ to: "/workflows/library" });
        }
      } catch (err) {
        if (cancelled) return;
        console.error("Failed to load workflow:", err);
        toast.error(`Failed to load workflow "${workflowName}"`);
        // Same fallback logic for errors
        navigate({ to: "/workflows/library" });
      }
    };

    void loadWorkflow();
    return () => {
      cancelled = true;
    };
    // loadKey reads only projectId and tourMode, both listed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [routeWorkflowName, routeIsNew, projectId, projectResolved, navigate, tourMode]);

  const handleSave = async (
    workflow: Workflow,
    intent?: DraftStatus,
    opts?: { asCopy?: boolean },
  ): Promise<SaveResult> => {
    if (tourMode) {
      toast.info("Tour mode — changes are not saved", { duration: 4000 });
      return { success: true, validationErrors: [] };
    }
    if (!projectId) {
      toast.error("No project selected", { duration: 5000 });
      return { success: false, validationErrors: [] };
    }

    try {
      // Pass the expected version for OCC, and draft ID for ID-based updates (allows renames)
      // No intent keeps the stored status: drafts stay drafts, and a published
      // workflow stays published — so the backend rejects a save that would
      // break it rather than silently taking it out of service.
      // A copy (Duplicate) is a NEW workflow: no draft id, no version, or the
      // save would rename this one instead.
      const response = await workflowGrpc.saveWorkflow(
        projectId,
        workflow,
        opts?.asCopy ? undefined : workflowVersion || undefined,
        undefined,
        opts?.asCopy ? undefined : draftId,
        intent,
      );

      if (!response.success) {
        if (isCompleteSaveRejection(response)) {
          // Nothing was stored; the problems show on the canvas. Say which
          // gate refused it and offer the way forward.
          const publishing = intent === "complete" && draftStatus === "draft";
          const problems = splitFindings(response.validationErrors).errors.length;
          toast.error(
            publishing
              ? `Not published — fix ${problems} problem${problems === 1 ? "" : "s"} first.`
              : "Not saved — a published workflow has to work, and these changes have problems.",
            {
              duration: 12000,
              description: publishing
                ? "Your changes are not saved yet. Save keeps them as a draft."
                : "Fix them, or unpublish and save (it won't run until you publish it again).",
              action: {
                label: publishing ? "Save draft" : "Unpublish and save",
                onClick: () => {
                  void saveAsDraftRef.current?.();
                },
              },
            },
          );
          return { success: false, rejected: true, validationErrors: response.validationErrors };
        }
        throw new Error(response.message || "Failed to save workflow");
      }

      setDraftStatus(response.status);

      // Update draftId if returned (this is the workflow draft UUID)
      if (response.id) {
        setDraftId(response.id);
      }

      // Update version for next save (OCC)
      if (response.version) {
        setWorkflowVersion(response.version);
      }

      // Update YAML definition from backend (canonical YAML for modal display)
      if (response.yamlDefinition) {
        setYamlDefinition(response.yamlDefinition);
      }

      if (!workflow.name) {
        throw new Error("workflow.name required to save draft");
      }
      trackEvent("workflow_draft_saved", {
        workflowSlug: workflow.name,
        workflowName: workflow.name,
        isNew: !draftId,
      });

      if (!draftId) {
        trackEvent("workflow_created");
      }

      // Note: No toasts here - WorkflowBuilder handles toasts for explicit button clicks
      // Auto-save uses the SaveStatusIndicator instead

      // Update the selected workflow in parent component
      if (onWorkflowChange) {
        onWorkflowChange(workflow);
      }

      // Update editing workflow state with canonical input aliasing
      const savedWorkflow: Workflow = {
        ...workflow,
        inputs: workflow.inputs,
      };
      setEditingWorkflow(savedWorkflow);

      // Once saved, this is no longer a read-only workflow (even if it started as one)
      // This handles the "Create a Copy" flow where a builtin/project gets forked
      setIsReadOnly(false);
      // Saved workflows are always user-owned
      setWorkflowSource("user");

      // Update detailed workflows map immediately for optimistic UI
      setDetailedWorkflows((prev) => {
        const updated = new Map(prev);
        const name = workflow.name || "Unnamed";
        updated.set(name, {
          name,
          filename: response.slug,
          description: workflow.description,
          stepCount: (workflow.nodes || []).length,
          source: "user" as const,
          nodes: (workflow.nodes || []) as unknown as ResponseStep[],
          edges: (workflow.edges || []) as unknown as ResponseEdge[],
          status: response.status,
          validationErrors: response.validationErrors,
        });
        return updated;
      });

      // A save that renamed the workflow (or stored a copy of a built-in)
      // lives at a new slug. Move the URL there BEFORE anything refreshes, in
      // place (replace, so Back doesn't return to a name that no longer
      // exists). The canvas already shows this workflow, unsaved edits and
      // all, so the route change must not reload it.
      if (response.slug && response.slug !== routeWorkflowName) {
        loadedRouteNameRef.current = loadKey(response.slug);
        navigate({
          to: "/workflow/$workflowName",
          params: { workflowName: response.slug },
          search: (prev: Record<string, unknown>) => prev,
          replace: true,
        } as never);
      }

      // Refresh global store + detailed data so cached list and all
      // subscribers (AgentSelector, the Library, etc.) see the save.
      await refreshWorkflowList();

      // Return save result for WorkflowBuilder to show appropriate toasts
      return {
        success: true,
        validationErrors: response.validationErrors,
        status: response.status,
        slug: response.slug,
      };
    } catch (err) {
      console.error("Workflow save failed:", err);

      // Extract error message from gRPC/Connect error
      let errorMessage = "Failed to save workflow";
      let isConflict = false;
      if (err instanceof Error) {
        errorMessage = err.message || errorMessage;
        // Check for OCC conflict (CodeAborted from backend)
        isConflict = errorMessage.includes("workflow was modified since");
      }

      if (isConflict) {
        // Show conflict message - user must reload to get LLM's changes before saving
        toast.error("Workflow was modified by the AI assistant.", {
          duration: 15000,
          description:
            "Reload to see the latest changes, then re-apply your edits.",
          action: {
            label: "Reload",
            onClick: async () => {
              if (projectId && editingWorkflow?.name) {
                try {
                  const {
                    workflow: fresh,
                    version: freshVersion,
                    parseError: reloadParseError,
                    yamlDefinition: freshYaml,
                  } = await getWorkflowWithDraftId(
                    projectId,
                    editingWorkflow.name,
                  );
                  if (reloadParseError) {
                    toast.error("Workflow has a parse error after reload");
                    return;
                  }
                  if (fresh) {
                    setEditingWorkflow(fresh);
                    setWorkflowVersion(freshVersion);
                    setYamlDefinition(freshYaml);
                    toast.success("Workflow reloaded with latest changes");
                  }
                } catch {
                  toast.error("Failed to reload workflow");
                }
              }
            },
          },
        });
      } else {
        // Show generic error toast
        toast.error(errorMessage, { duration: 8000 });
      }

      // CRITICAL: Re-throw the error to signal failure to WorkflowBuilder
      throw err;
    }
  };

  // Test run: save the canvas, then run it by slug. Only a stored workflow
  // returns a slug; a rejected or failed save returns null and nothing starts.
  const handleSaveForTestRun = async (workflow: Workflow): Promise<string | null> => {
    const result = await handleSave(workflow);
    return result.success && result.slug ? result.slug : null;
  };

  // Publish / Unpublish of the STORED workflow (wire: complete / draft).
  // Publishing validates it server-side; the builder enables Publish only
  // when the canvas has no problems, but the server is the gate. (Publishing
  // unsaved edits goes through handleSave with intent "complete" instead.)
  const handleSetStatus = async (status: DraftStatus): Promise<StatusChangeResult> => {
    if (!projectId || !draftId) {
      toast.error("Save the workflow first", { duration: 4000 });
      return { success: false, validationErrors: [] };
    }
    try {
      const response = await workflowGrpc.setWorkflowStatus(
        projectId,
        draftId,
        status,
        workflowVersion || undefined,
      );
      setDraftStatus(response.status);
      if (response.version) setWorkflowVersion(response.version);
      if (response.success) {
        toast.success(
          status === "complete"
            ? "Published — it can run now"
            : "Unpublished — it won't run until you publish it again",
          { duration: 4000 },
        );
        await refreshWorkflowList();
      } else {
        const problems = splitFindings(response.validationErrors).errors.length;
        toast.error(
          problems > 0
            ? `Not published — fix ${problems} problem${problems === 1 ? "" : "s"} first.`
            : response.message || "Could not change status",
          { duration: 8000 },
        );
      }
      return { success: response.success, validationErrors: response.validationErrors };
    } catch (err) {
      console.error("Failed to change workflow status:", err);
      toast.error(err instanceof Error ? err.message : "Failed to change workflow status", {
        duration: 8000,
      });
      return { success: false, validationErrors: [] };
    }
  };

  // Shared full-screen layout: header (with close + settings) + content below.
  // The header is only rendered when route adapters provide handlers (route
  // mode); when WorkflowBuilderPage is composed directly with no onClose
  // (e.g. embedded usage), we render just the content.
  const renderWithChrome = (content: React.ReactNode) => {
    if (!onClose && !onNavigateToSettings) {
      return content;
    }
    return (
      <div className="flex flex-col h-screen bg-background font-sans dense-ui">
        <WorkflowHeader
          onClose={onClose ?? (() => {})}
          onNavigateToSettings={onNavigateToSettings ?? (() => {})}
        />
        <div className="flex-1 overflow-hidden">{content}</div>
      </div>
    );
  };

  // Deep-link to /workflow/$name or /workflow/new with no current project: the
  // route effect either redirects (projects already loaded, no selection) or is
  // waiting for projectStore to finish loading. Either way, do NOT render the
  // builder — it would show stale/blank workflow chrome until the redirect
  // lands or the effect re-runs. Show the loading spinner; if the redirect is
  // imminent, it'll unmount this component before the spinner is even visible.
  if ((routeWorkflowName || routeIsNew) && !projectId) {
    return <LoadingSpinner />;
  }

  // /workflow/new is the New workflow dialog. Creating is the dialog's
  // Create click; on success the URL moves to the new workflow (replace, so a
  // reload or Back never lands on /workflow/new and offers to create again).
  if (routeIsNew && projectId) {
    return renderWithChrome(
      <div className="h-full w-full bg-background" data-testid="new-workflow-page">
        <NewWorkflowDialog
          open
          projectId={projectId}
          onClose={() => navigate({ to: "/workflows/library", replace: true })}
          onCreated={(created) => {
            trackEvent("workflow_created");
            navigate({
              to: "/workflow/$workflowName",
              params: { workflowName: created.slug },
              search: (prev: Record<string, unknown>) => {
                // ?chat= named whatever was open before; it is not this workflow's.
                const { chat: _chat, ...rest } = prev;
                return rest;
              },
              replace: true,
            } as never);
          }}
        />
      </div>,
    );
  }

  const handleBack = async () => {
    // Navigate to the logical parent of the current route (lib/routeParent).
    // From /workflow/$name that's the Library (/workflows/library).
    navigate(getParentRouteNavigateOptions(pathname));

    // Refresh workflow list to show any changes made.
    await refreshWorkflowList();
  };

  // Handle clearing a corrupted workflow
  const handleClearWorkflow = async () => {
    if (!projectId || !errorWorkflowName) return;

    try {
      // Find the workflow to get its slug/filename
      const workflow = existingWorkflows.find(
        (w) => w.name === errorWorkflowName,
      );
      if (workflow) {
        await workflowGrpc.deleteWorkflow(projectId, workflow.filename);
      }

      toast.success("Workflow cleared");

      // Clear error state and go back to the Library.
      setParseError(undefined);
      setRawDefinition(undefined);
      setErrorWorkflowName(undefined);
      setDraftId(undefined);
      navigate({ to: "/workflows/library" });

      // Refresh workflows.
      refreshWorkflowList();
    } catch (err) {
      console.error("Failed to clear workflow:", err);
      toast.error("Failed to clear workflow");
    }
  };

  // Handle successful fix from the error view
  const handleWorkflowFixed = async () => {
    if (!projectId || !errorWorkflowName) return;

    // Try to reload the workflow
    try {
      const {
        workflow,
        draftId: loadedDraftId,
        parseError: loadedParseError,
        yamlDefinition: fixedYaml,
      } = await getWorkflowWithDraftId(projectId, errorWorkflowName);

      if (loadedParseError) {
        // Still has error
        setParseError(loadedParseError);
        return;
      }

      if (workflow) {
        // Clear error state
        setParseError(undefined);
        setRawDefinition(undefined);
        setErrorWorkflowName(undefined);

        // Set up builder data — clearing parseError above is what flips the
        // derived view back to "builder".
        setEditingWorkflow(workflow);
        setIsReadOnly(false);
          setWorkflowSource("user");
        setDraftId(loadedDraftId);
        setYamlDefinition(fixedYaml);

        toast.success("Workflow loaded successfully");
      }
    } catch (err) {
      console.error("Failed to reload workflow:", err);
      toast.error("Failed to reload workflow");
    }
  };

  // Render error view
  if (currentView === "error") {
    return renderWithChrome(
      <WorkflowParseErrorView
        workflowName={errorWorkflowName || "Unknown"}
        parseError={parseError || "Unknown error"}
        rawDefinition={rawDefinition}
        draftId={draftId}
        chatId={routeChatId}
        onChatIdChange={onChatIdChange}
        onBack={handleBack}
        onClear={handleClearWorkflow}
        onFixed={handleWorkflowFixed}
      />,
    );
  }

  return renderWithChrome(
    <div className="h-full w-full bg-background">
      <WorkflowBuilder
        onSave={handleSave}
        onSaveForTestRun={handleSaveForTestRun}
        draftStatus={workflowSource === "user" ? draftStatus : "complete"}
        onSetStatus={handleSetStatus}
        saveAsDraftRef={saveAsDraftRef}
        initialWorkflow={editingWorkflow}
        initialName={initialWorkflowName}
        onBack={handleBack}
        isBuiltin={isReadOnly}
        source={workflowSource}
        version={workflowVersion}
        chatId={routeChatId}
        onChatIdChange={onChatIdChange}
        onDraftStatusChange={setDraftStatus}
        draftId={draftId}
        onWorkflowDeleted={() => navigate({ to: "/workflows/library" })}
        onVersionChange={setWorkflowVersion}
        yamlDefinition={yamlDefinition}
        onYamlDefinitionChange={setYamlDefinition}
        drillIntoNodeId={routeDrillIntoNodeId}
      />
    </div>,
  );
}