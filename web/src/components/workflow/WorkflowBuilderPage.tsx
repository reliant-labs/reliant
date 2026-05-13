import { useState, useEffect, useMemo, useRef, useCallback } from "react";
import { WorkflowBuilder } from "./WorkflowBuilder";
import { WorkflowHub } from "./WorkflowHub";
import { WorkflowParseErrorView } from "./WorkflowParseErrorView";
import type { Workflow } from "../../types/workflow";
import type {
  Step as ResponseStep,
  Edge as ResponseEdge,
} from "../../api/workflow-grpc";
import {
  workflowGrpc,
  getWorkflowWithDraftId,
  getWorkflowByDraftId,
  type WorkflowResponse,
  type InvalidWorkflow,
} from "../../api/workflow-grpc";
// Preset imports removed - handlers moved to WorkflowHub
import { toast } from "sonner";
import {
  useGlobalDataStore,
  useWorkflows,
  type WorkflowDef,
} from "../../store/globalDataStore";
import { useProjectStore } from "../../store/projectStore";
import { useViewerStore } from "../../store/viewerStore";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { usePreferencesStore } from "../../store/preferencesStore";
import { useTourStore } from "../../store/tourStore";
import { trackEvent } from "../../lib/analytics";

interface WorkflowBuilderPageProps {
  selectedWorkflow?: Workflow | null;
  onWorkflowChange?: (workflow: Workflow) => void;
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
  };
}

export function WorkflowBuilderPage({
  selectedWorkflow,
  onWorkflowChange,
}: WorkflowBuilderPageProps) {
  const [currentView, setCurrentView] = useState<"hub" | "builder" | "error">(
    "hub",
  );
  const [editingWorkflow, setEditingWorkflow] = useState<Workflow | undefined>(
    selectedWorkflow || undefined,
  );
  // isReadOnly is true for builtin and project workflows (cannot be saved directly)
  const [isReadOnly, setIsReadOnly] = useState(false);
  // isNewWorkflow is true when creating a new workflow (to clear stale chat state)
  const [isNewWorkflow, setIsNewWorkflow] = useState(false);
  // Track workflow source and metadata for info popover
  const [workflowSource, setWorkflowSource] = useState<
    "builtin" | "user" | "project"
  >("user");
  const [workflowVersion, setWorkflowVersion] = useState<number>(0);
  const [_workflowSlug, setWorkflowSlug] = useState<string | undefined>(
    undefined,
  );

  // Track draft ID - loaded from existing workflow (for LLM tool calls)
  const [draftId, setDraftId] = useState<string | undefined>(undefined);

  // Track initial name for new workflows (from random generation)
  const [initialWorkflowName, setInitialWorkflowName] = useState<
    string | undefined
  >(undefined);

  // Track builder chat ID - for chat resumption (separate from draft ID)
  const [builderChatId, setBuilderChatId] = useState<string | undefined>(
    undefined,
  );

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

  // Session ID for new/unsaved workflows - used as a stable key for localStorage before workflow is saved
  // This is a ref to avoid unnecessary re-renders
  const workflowSessionIdRef = useRef<string | undefined>(undefined);
  // Track last workflow that failed to save due to OCC conflict (for force save retry)
  // (Variable removed - was unused)
  // Local workflow state for detailed data (updated_at, full steps, etc.)
  const [detailedWorkflows, setDetailedWorkflows] = useState<
    Map<string, WorkflowResponse>
  >(new Map());
  // Track invalid workflows that failed to load
  const [invalidWorkflows, setInvalidWorkflows] = useState<InvalidWorkflow[]>(
    [],
  );
  const currentProject = useProjectStore((state) => state.currentProject);
  const projectId = currentProject?.id;

  // Use cached workflows from global store for immediate display
  const { workflows: cachedWorkflows, loading: workflowsLoading } =
    useWorkflows();

  // Get presets from global store
  const presets = useGlobalDataStore((state) => state.presets);
  const refetchPresets = useGlobalDataStore((state) => state.refetchPresets);

  // Get default workflow preference
  const {
    preferences,
    updatePreferences,
    loadPreferences,
    isLoading: preferencesLoading,
  } = usePreferencesStore();

  // Load preferences on mount
  useEffect(() => {
    if (!preferences && !preferencesLoading) {
      loadPreferences();
    }
  }, [preferences, preferencesLoading, loadPreferences]);

  // Ensure presets are loaded when component mounts with a projectId
  useEffect(() => {
    if (projectId && presets.length === 0) {
      refetchPresets(projectId);
    }
  }, [projectId, presets.length, refetchPresets]);

  // Check if we should open a specific workflow from the viewer store
  const workflowToOpen = useViewerStore((state) => state.workflowToOpen);
  const clearWorkflowToOpen = useViewerStore(
    (state) => state.clearWorkflowToOpen,
  );
  const setWorkflowMode = useViewerStore((state) => state.setWorkflowMode);

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
      ); // includeHidden for hub
      const detailed = new Map<string, WorkflowResponse>();
      result.workflows.forEach((w) => detailed.set(w.name, w));
      setDetailedWorkflows(detailed);
      setInvalidWorkflows(result.invalidWorkflows);
    } catch (err) {
      console.error("Failed to load detailed workflows:", err);
    }
  }, [projectId]);

  // Background fetch for detailed workflow data (updated_at, etc.)
  useEffect(() => {
    if (!projectId) return;
    loadDetailedWorkflows();

    // Listen for workflow saves to refresh the detailed data
    const handleWorkflowSaved = () => {
      loadDetailedWorkflows();
    };
    window.addEventListener("workflow-saved", handleWorkflowSaved);
    return () =>
      window.removeEventListener("workflow-saved", handleWorkflowSaved);
  }, [projectId, loadDetailedWorkflows]);

  // If a workflow is pre-selected from props, show the builder
  useEffect(() => {
    if (selectedWorkflow) {
      setEditingWorkflow(selectedWorkflow);
      setCurrentView("builder");
    }
  }, [selectedWorkflow]);

  // If workflowToOpen is set from the viewer store, load that workflow
  // Special value "__hub__" means show hub view
  // Special value "__new__" means create a new editable workflow (for onboarding)
  useEffect(() => {
    if (workflowToOpen === "__hub__") {
      setCurrentView("hub");
      setEditingWorkflow(undefined);
      setYamlDefinition(undefined);
      clearWorkflowToOpen();
      return;
    }
    if (workflowToOpen === "__new__") {
      // Create a new editable workflow for onboarding
      setEditingWorkflow(undefined);
      setYamlDefinition(undefined);
      setIsReadOnly(false);
      setIsNewWorkflow(true);
      setWorkflowSource("user");
      setWorkflowVersion(0);
      setBuilderChatId(undefined);
      setDraftId(undefined);
      workflowSessionIdRef.current = crypto.randomUUID();
      setCurrentView("builder");
      clearWorkflowToOpen();
      return;
    }
    if (!workflowToOpen || !projectId) return;

    const loadWorkflow = async () => {
      try {
        const {
          workflow,
          draftId: loadedDraftId,
          builderChatId: loadedChatId,
          parseError: loadedParseError,
          rawDefinition: loadedRawDefinition,
          yamlDefinition: loadedYamlDef,
        } = await getWorkflowWithDraftId(projectId, workflowToOpen);

        // Handle parse error - show error view with chat available
        if (loadedParseError) {
          setParseError(loadedParseError);
          setRawDefinition(loadedRawDefinition);
          setErrorWorkflowName(workflowToOpen);
          setDraftId(loadedDraftId);
          setBuilderChatId(loadedChatId);
          setCurrentView("error");
          return;
        }

        if (workflow) {
          // Clear any previous parse error state
          setParseError(undefined);
          setRawDefinition(undefined);
          setErrorWorkflowName(undefined);

          // Detect if this is a built-in workflow from the workflowToOpen name
          const isBuiltinWorkflow = workflowToOpen.startsWith("builtin://");
          // Check if it's a project workflow by looking it up in existing workflows
          const existingWorkflow = existingWorkflows.find(
            (w) => w.name === workflowToOpen,
          );
          const isProjectWorkflow = existingWorkflow?.source === "project";

          setEditingWorkflow(workflow);
          setIsReadOnly(isBuiltinWorkflow || isProjectWorkflow);
          setIsNewWorkflow(false);
          setWorkflowSource(
            isBuiltinWorkflow
              ? "builtin"
              : isProjectWorkflow
                ? "project"
                : "user",
          );
          setDraftId(loadedDraftId);
          setBuilderChatId(loadedChatId);
          setYamlDefinition(loadedYamlDef);
          workflowSessionIdRef.current = undefined;
          setCurrentView("builder");
        } else {
          toast.error(`Workflow "${workflowToOpen}" not found`);
          // Workflow was deleted or doesn't exist - fall back to hub
          setCurrentView("hub");
          // Clear the saved workflow state
          const worktreeId =
            useWorktreeStore.getState().currentWorktree?.id ?? null;
          useWorkspaceStateStore.getState().setWorkflowState(
            projectId,
            worktreeId,
            true, // Stay in workflow mode
            null, // But no specific workflow
          );
        }
      } catch (err) {
        console.error("Failed to load workflow:", err);
        toast.error(`Failed to load workflow "${workflowToOpen}"`);
        // Same fallback logic for errors
        setCurrentView("hub");
        const worktreeId =
          useWorktreeStore.getState().currentWorktree?.id ?? null;
        useWorkspaceStateStore
          .getState()
          .setWorkflowState(projectId, worktreeId, true, null);
      } finally {
        // Clear the flag so we don't reload on re-render
        clearWorkflowToOpen();
      }
    };

    loadWorkflow();
  }, [workflowToOpen, projectId, clearWorkflowToOpen, existingWorkflows]);

  // Escape key handling for hub view - exit workflow mode
  // When in builder view, WorkflowBuilder handles escape itself
  useEffect(() => {
    const handleEscape = (e: KeyboardEvent) => {
      // Only handle escape when in hub view
      if (e.key !== "Escape" || currentView !== "hub") return;

      // Skip if we're in an input field
      const target = e.target as HTMLElement;
      if (
        target.tagName === "INPUT" ||
        target.tagName === "TEXTAREA" ||
        target.contentEditable === "true"
      ) {
        return;
      }

      // Defer to onboarding tour if it's active (it handles its own Escape)
      if (useTourStore.getState().isWizardActive) return;

      // Prevent ModernApp from handling this ESC
      e.preventDefault();
      e.stopPropagation();
      e.stopImmediatePropagation();

      // Exit workflow mode (go back to main chat)
      setWorkflowMode(false);
    };

    // Use window with capture phase to run BEFORE document-level handlers (like ModernApp's global shortcuts)
    // This ensures the workflow navigation stack is respected (panels → hub → exit)
    window.addEventListener("keydown", handleEscape, true);
    return () => window.removeEventListener("keydown", handleEscape, true);
  }, [currentView, setWorkflowMode]);

  const handleSave = async (workflow: Workflow) => {
    if (!projectId) {
      toast.error("No project selected", { duration: 5000 });
      return { success: false, validationErrors: [] };
    }

    try {
      // Pass the builder chat ID, expected version for OCC, and draft ID for ID-based updates (allows renames)
      const response = await workflowGrpc.saveWorkflow(
        projectId,
        workflow,
        builderChatId,
        workflowVersion || undefined,
        undefined,
        draftId,
      );

      if (!response.success) {
        throw new Error(response.message || "Failed to save workflow");
      }

      // Update builderChatId from response (may be new or updated)
      if (response.builderChatId) {
        setBuilderChatId(response.builderChatId);
      }

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

      trackEvent("workflow_draft_saved", {
        workflowSlug: workflow.name,
        workflowName: workflow.name,
        isNew: !draftId,
      });

      if (!draftId) {
        trackEvent("workflow_created");
      }

      // After first save, clear the session ID since workflow now has proper identification
      workflowSessionIdRef.current = undefined;

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

      // After saving, this is no longer a "new" workflow
      setIsNewWorkflow(false);

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
        });
        return updated;
      });

      // Refetch global workflow store to update cached list and all subscribers (AgentSelector, etc.)
      if (projectId) {
        try {
          await useGlobalDataStore.getState().refetchWorkflows(projectId);
        } catch (error) {
          // Silently ignore - global store update is best-effort
          console.warn("Failed to refetch workflows after save:", error);
        }
      }

      // Return save result for WorkflowBuilder to show appropriate toasts
      return {
        success: true,
        validationErrors: response.validationErrors,
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

  const handleCreateNew = async () => {
    setIsReadOnly(false);
    setIsNewWorkflow(true);
    setWorkflowSource("user");
    setWorkflowVersion(0);

    // Generate a new session ID for this new workflow (for localStorage persistence before save)
    workflowSessionIdRef.current = crypto.randomUUID();

    // Persist state - new workflow, so no name yet
    const worktreeId = useWorktreeStore.getState().currentWorktree?.id ?? null;

    // Create draft BEFORE showing builder so we have the random name ready
    if (projectId) {
      useWorkspaceStateStore.getState().setWorkflowState(
        projectId,
        worktreeId,
        true,
        null, // New workflow has no name yet
      );

      // Create draft immediately so we have a draft ID and random name before showing builder
      try {
        const { draftId: newDraftId } =
          await workflowGrpc.createWorkflowDraft(projectId);
        setDraftId(newDraftId);

        // Fetch the full workflow content from the draft to display immediately
        const {
          workflow,
          version,
          yamlDefinition: newYaml,
        } = await getWorkflowByDraftId(projectId, newDraftId);
        if (workflow) {
          setEditingWorkflow(workflow);
          setWorkflowVersion(version);
          setYamlDefinition(newYaml);
        } else {
          setEditingWorkflow(undefined);
        }
      } catch (error) {
        console.error("Failed to create workflow draft:", error);
        // Continue without draft ID - tools will create one on first write
        setDraftId(undefined);
        setEditingWorkflow(undefined);
      }
    } else {
      setDraftId(undefined);
      setEditingWorkflow(undefined);
    }

    // Show builder AFTER we have the workflow content
    setCurrentView("builder");
  };

  const handleSelectWorkflow = async (
    workflowName: string,
    isReadOnlyWorkflow: boolean = false,
    source?: "builtin" | "user" | "project",
    _updatedAt?: string,
    slug?: string,
    draftIdFromList?: string,
  ) => {
    if (!projectId) {
      toast.error("No project selected", { duration: 5000 });
      return;
    }

    try {
      // For user workflows with a draft_id, use ID-based lookup (stable, doesn't depend on slug generation)
      // For builtin/project workflows, use name-based lookup
      const {
        workflow,
        draftId: loadedDraftId,
        builderChatId: loadedChatId,
        version: loadedVersion,
        yamlDefinition: loadedYaml,
      } = source === "user" && draftIdFromList
        ? await getWorkflowByDraftId(projectId, draftIdFromList)
        : await getWorkflowWithDraftId(projectId, workflowName);
      setEditingWorkflow(workflow);
      setIsReadOnly(isReadOnlyWorkflow);
      setIsNewWorkflow(false);
      setWorkflowSource(source ?? "user");
      // Use the version from getWorkflow for OCC
      setWorkflowVersion(loadedVersion);
      setWorkflowSlug(slug);
      setDraftId(loadedDraftId);
      setBuilderChatId(loadedChatId);
      setYamlDefinition(loadedYaml);

      // For existing workflows, clear the session ID and initial name as we use the workflow name for persistence
      workflowSessionIdRef.current = undefined;
      setInitialWorkflowName(undefined);

      setCurrentView("builder");

      // Persist the active workflow
      const worktreeId =
        useWorktreeStore.getState().currentWorktree?.id ?? null;
      useWorkspaceStateStore
        .getState()
        .setWorkflowState(projectId, worktreeId, true, workflowName);
    } catch (err) {
      console.error("Failed to load workflow:", err);
      toast.error(
        err instanceof Error ? err.message : "Failed to load workflow",
        { duration: 5000 },
      );
    }
  };

  const handleDeleteWorkflow = async (name: string) => {
    if (!projectId) {
      toast.error("No project selected", { duration: 5000 });
      return;
    }

    try {
      await workflowGrpc.deleteWorkflow(projectId, name);

      // Refetch global store (primary source) and detailed data
      // Await both to ensure UI updates immediately before returning
      await Promise.all([
        useGlobalDataStore.getState().refetchWorkflows(projectId),
        loadDetailedWorkflows(),
      ]);
    } catch (err) {
      console.error("Delete workflow failed:", err);
      toast.error(
        err instanceof Error ? err.message : "Failed to delete workflow",
        { duration: 5000 },
      );
    }
  };

  const handleImportWorkflow = async (
    yamlContent: string,
    overwrite: boolean = false,
  ) => {
    if (!projectId) {
      toast.error("No project selected", { duration: 5000 });
      return { success: false, message: "No project selected" };
    }

    try {
      const response = await workflowGrpc.importWorkflow(
        projectId,
        yamlContent,
        overwrite,
      );

      if (response.success) {
        // Refetch global store (primary source) and detailed data
        useGlobalDataStore
          .getState()
          .refetchWorkflows(projectId)
          .catch(() => {});
        await loadDetailedWorkflows();
      }

      // Return the response for WorkflowHub to handle toasts and conflict modal
      return {
        success: response.success,
        conflict: response.conflict,
        existingId: response.existingId,
        slug: response.slug,
        message: response.message,
      };
    } catch (err) {
      console.error("Import workflow failed:", err);
      return {
        success: false,
        message:
          err instanceof Error ? err.message : "Failed to import workflow",
      };
    }
  };

  const handleExportWorkflow = async (slug: string) => {
    if (!projectId) {
      toast.error("No project selected", { duration: 5000 });
      return;
    }

    try {
      await workflowGrpc.downloadWorkflow(projectId, slug);
      toast.success("Workflow exported", { duration: 3000 });
    } catch (err) {
      console.error("Export workflow failed:", err);
      toast.error(
        err instanceof Error ? err.message : "Failed to export workflow",
        { duration: 5000 },
      );
    }
  };

  const handleSetDefaultWorkflow = async (workflowName: string) => {
    try {
      await updatePreferences({ defaultWorkflow: workflowName });
    } catch (err) {
      console.error("Failed to set default workflow:", err);
      throw err;
    }
  };

  const handleToggleVisibility = async (
    workflowName: string,
    isHidden: boolean,
  ) => {
    if (!projectId) return;
    // Find the workflow to get its slug/filename
    const workflow = existingWorkflows.find((w) => w.name === workflowName);
    if (!workflow) return;

    try {
      const result = await workflowGrpc.setWorkflowVisibility(
        projectId,
        workflow.filename,
        isHidden,
      );
      if (result.success) {
        // Refresh both the global store and local detailed workflows
        await Promise.all([
          useGlobalDataStore.getState().refetchWorkflows(projectId),
          loadDetailedWorkflows(),
        ]);
      }
    } catch (err) {
      console.error("Failed to toggle workflow visibility:", err);
      throw err;
    }
  };

  if (currentView === "hub") {
    return (
      <WorkflowHub
        onCreateNew={handleCreateNew}
        onSelectWorkflow={(name) => {
          // Find the workflow to check if it's read-only (builtin or project)
          const workflow = existingWorkflows.find((w) => w.name === name);
          const isReadOnlySource =
            workflow?.source === "builtin" || workflow?.source === "project";
          // Pass draftId for user workflows to enable stable ID-based lookups
          handleSelectWorkflow(
            name,
            isReadOnlySource,
            workflow?.source,
            workflow?.updatedAt,
            workflow?.filename,
            workflow?.draftId,
          );
        }}
        onDeleteWorkflow={(name) => {
          // Find the filename for this workflow name
          const workflow = existingWorkflows.find((w) => w.name === name);
          if (workflow) {
            handleDeleteWorkflow(workflow.filename);
          }
        }}
        onImportWorkflow={handleImportWorkflow}
        onExportWorkflow={(slug) => handleExportWorkflow(slug)}
        onToggleVisibility={handleToggleVisibility}
        existingWorkflows={existingWorkflows}
        invalidWorkflows={invalidWorkflows}
        isLoading={workflowsLoading && existingWorkflows.length === 0}
        presets={presets}
        defaultWorkflow={preferences?.defaultWorkflow}
        onSetDefaultWorkflow={handleSetDefaultWorkflow}
        projectId={projectId}
      />
    );
  }

  const handleBack = async () => {
    setCurrentView("hub");
    setEditingWorkflow(undefined);
    setYamlDefinition(undefined);

    // Clear active workflow but stay in workflow mode
    const worktreeId = useWorktreeStore.getState().currentWorktree?.id ?? null;
    if (projectId) {
      useWorkspaceStateStore.getState().setWorkflowState(
        projectId,
        worktreeId,
        true, // Still in workflow mode
        null, // Hub view - no specific workflow
      );

      // Refresh workflow list to show any changes made
      await Promise.all([
        useGlobalDataStore.getState().refetchWorkflows(projectId),
        loadDetailedWorkflows(),
      ]);
    }
  };

  // Callback to update chat ID when chat is created in WorkflowBuilderChat
  const handleChatIdChange = (newChatId: string) => {
    setBuilderChatId(newChatId);
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

      // Clear error state and go back to hub
      setParseError(undefined);
      setRawDefinition(undefined);
      setErrorWorkflowName(undefined);
      setDraftId(undefined);
      setBuilderChatId(undefined);
      setCurrentView("hub");

      // Refetch workflows
      useGlobalDataStore
        .getState()
        .refetchWorkflows(projectId)
        .catch(() => {});
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
        builderChatId: loadedChatId,
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

        // Set up builder view
        setEditingWorkflow(workflow);
        setIsReadOnly(false);
        setIsNewWorkflow(false);
        setWorkflowSource("user");
        setDraftId(loadedDraftId);
        setBuilderChatId(loadedChatId);
        setYamlDefinition(fixedYaml);
        setCurrentView("builder");

        toast.success("Workflow loaded successfully");
      }
    } catch (err) {
      console.error("Failed to reload workflow:", err);
      toast.error("Failed to reload workflow");
    }
  };

  // Render error view
  if (currentView === "error") {
    return (
      <WorkflowParseErrorView
        workflowName={errorWorkflowName || "Unknown"}
        parseError={parseError || "Unknown error"}
        rawDefinition={rawDefinition}
        draftId={draftId}
        builderChatId={builderChatId}
        onBack={handleBack}
        onClear={handleClearWorkflow}
        onFixed={handleWorkflowFixed}
        onChatIdChange={handleChatIdChange}
        onDraftIdChange={setDraftId}
      />
    );
  }

  return (
    <div className="h-full w-full bg-background">
      <WorkflowBuilder
        onSave={handleSave}
        initialWorkflow={editingWorkflow}
        initialName={initialWorkflowName}
        onBack={handleBack}
        isBuiltin={isReadOnly}
        isNewWorkflow={isNewWorkflow}
        source={workflowSource}
        version={workflowVersion}
        builderChatId={builderChatId}
        draftId={draftId}
        workflowSessionId={workflowSessionIdRef.current}
        onChatIdChange={handleChatIdChange}
        onDraftIdChange={setDraftId}
        onVersionChange={setWorkflowVersion}
        yamlDefinition={yamlDefinition}
        onYamlDefinitionChange={setYamlDefinition}
      />
    </div>
  );
}