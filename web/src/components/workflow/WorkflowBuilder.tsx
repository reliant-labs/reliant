import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  ReactFlow,
  Background,
  useNodesState,
  useEdgesState,
  ReactFlowProvider,
  ConnectionLineType,
  Panel,
  useReactFlow,
} from "@xyflow/react";
import type { Connection, Edge, Node } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import "./workflow-theme.css";
import { nodeTypes } from "./nodes";
import { edgeTypes } from "./edges";
import { ConfigPanel } from "./config";
import { EdgeConfigPanel } from "./EdgeConfigPanel";
import { SwitchConfigPanel } from "./SwitchConfigPanel";
import { WorkflowSettingsEditor } from "./WorkflowSettingsEditor";
import type {
  Workflow,
  Step,
  WorkflowStep,
  LoopStep,
} from "../../types/workflow";
import {
  getStepInline,
  initStepArgs,
  withRunArgs,
  withWorkflowArgs,
  withLoopArgs,
} from "../../types/workflow";
import { autoLayoutWorkflow } from "../../lib/workflow-layout";
import {
  workflowToFlowElements,
  resolveNodeOverlaps,
  type FlowNodeData,
} from "../../lib/workflow-flow";
import { nodesEdgesToWorkflow } from "../../lib/nodes-edges-to-workflow";
import { useFitViewWithPanels } from "./hooks/useFitViewWithPanels";
import { useWorkflowKeyboardShortcuts } from "./hooks/useWorkflowKeyboardShortcuts";
import {
  useInlineEditStack,
  type InlineEditContext,
} from "./hooks/useInlineEditStack";
import { useLoadWorkflow } from "./hooks/useLoadWorkflow";
import { toast } from "sonner";
import { FloatingWorkflowSidebar } from "./FloatingWorkflowSidebar";
import { FloatingToolbar, type InteractionMode } from "./FloatingToolbar";
import { useUndoRedo } from "../../hooks/useUndoRedo";
import {
  Pencil,
  ArrowLeft,
  Copy,
  Info,
  Code,
  TestTube2,
  Settings2,
  Lock,
  ExternalLink,
} from "lucide-react";
import { WorkflowInfoPopover } from "./WorkflowInfoPopover";
// Auto-save removed - using explicit save only
import {
  ValidationStatusBadge,
  type ValidationStatus,
} from "./ValidationStatusBadge";
import { YamlEditorModal } from "./YamlEditorModal";
import type { ValidationError } from "../../api/workflow-grpc";
import { Modal } from "../ui/Modal";
import { Button } from "../ui/Button";

import type { BackgroundVariant, SelectionMode } from "@xyflow/react";
import { WorkflowBuilderChat, type PanelSize } from "./WorkflowBuilderChat";
import { ScenarioPanel } from "./ScenarioPanel";
import { useProjectStore } from "../../store/projectStore";
import { useIsChatRunning } from "../../store/activityStore";
import { useGlobalUpdatesStore } from "../../store/globalUpdatesStore";
import { normalizeWorkflowRef } from "./useWorkflowInputs";
import { celString, directCel } from "../../lib/celAdapter";
import { getInputDescription, type InputDef } from "../../lib/inputHelpers";
import {
  CELCompletionProvider,
  type CELCompletionContextValue,
} from "./CELCompletionContext";
import { WorkflowMutationProvider } from "./WorkflowMutationContext";
import { WorkflowNodeCallbacksProvider } from "./WorkflowNodeCallbacksContext";

/** Result of a save operation */
export interface SaveResult {
  success: boolean;
  validationErrors: Array<{ message: string }>;
}

interface WorkflowBuilderProps {
  onSave?: (workflow: Workflow) => void | Promise<void | SaveResult>;
  initialWorkflow?: Workflow;
  /** Initial name for new workflows (from random generation) */
  initialName?: string;
  onBack?: () => void;
  /** Whether this workflow is a builtin template (cannot be saved directly) */
  isBuiltin?: boolean;
  /** Whether this is a new workflow (to clear stale chat state) */
  isNewWorkflow?: boolean;
  /** Workflow source type - determines if editable */
  source?: "builtin" | "user" | "project";
  /** Current version number for OCC (0 for new/builtin workflows) */
  version?: number;
  /** When the workflow was created */
  createdAt?: string;
  /** Chat ID associated with this workflow (loaded from database) */
  builderChatId?: string;
  /** Draft ID for this workflow (used for LLM tool calls) */
  draftId?: string;
  /** Session ID for new/unsaved workflows (for localStorage persistence) */
  workflowSessionId?: string;
  /** Callback when a new chat is created */
  onChatIdChange?: (chatId: string) => void;
  /** Callback when draft ID changes (when backend creates a new draft) */
  onDraftIdChange?: (draftId: string) => void;
  /** Callback when workflow version changes (for OCC) */
  onVersionChange?: (version: number) => void;
  /** Canonical YAML definition from backend (for YAML modal display) */
  yamlDefinition?: string;
  /** Callback when YAML definition changes (e.g., after apply from modal) */
  onYamlDefinitionChange?: (yaml: string | undefined) => void;
  /**
   * One-shot drill target from the route's `?drill=` search param. After the
   * workflow loads, the builder enters inline-edit on this node (used by the
   * onboarding tour to land the user inside a workflow's body). Consumed once
   * per workflow load via a ref guard.
   */
  drillIntoNodeId?: string;
  /**
   * Navigate to a different workflow by name. Used after "Create a Copy"
   * succeeds so the user lands in the new copy instead of staring at the
   * source (now stale) URL.
   */
  onNavigateToWorkflow?: (workflowName: string) => void;
}

function WorkflowBuilderInner({
  onSave,
  initialWorkflow,
  initialName,
  onBack,
  isBuiltin = false,
  isNewWorkflow = false,
  source = "user",
  version: _version,
  createdAt,
  builderChatId,
  draftId,
  workflowSessionId,
  onChatIdChange,
  onDraftIdChange,
  onVersionChange,
  yamlDefinition,
  onYamlDefinitionChange,
  drillIntoNodeId,
  onNavigateToWorkflow,
}: WorkflowBuilderProps) {
  const reactFlowWrapper = useRef<HTMLDivElement>(null);
  const [nodes, setNodes, onNodesChange] = useNodesState<Node>([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([]);

  // SINGLE workflow-state source of truth. Every piece of top-level workflow
  // metadata lives here. The ReactFlow `nodes` / `edges` arrays stay separate
  // (React Flow owns its array refs for drag/connect efficiency).
  // `currentWorkflow` (the persistence representation) is derived from
  // `workflow` + `nodes` + `edges` via `nodesEdgesToWorkflow`.
  const [workflow, setWorkflow] = useState<Workflow>(() => ({
    name: initialWorkflow?.name || initialName || "New Workflow",
    description: initialWorkflow?.description,
    inputs: initialWorkflow?.inputs,
    outputs: initialWorkflow?.outputs,
    entry: initialWorkflow?.entry,
    presets: initialWorkflow?.presets,
    apiVersion: initialWorkflow?.apiVersion,
    ui: initialWorkflow?.ui,
  }));

  // Selection by id (not by node-object). The selectedNode / selectedEdge
  // objects are derived during render below.
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
  const [selectedEdgeId, setSelectedEdgeId] = useState<string | null>(null);

  const [isEditingName, setIsEditingName] = useState(false);
  const [showInfoPopover, setShowInfoPopover] = useState(false);
  const [interactionMode, setInteractionMode] =
    useState<InteractionMode>("pan");
  const [showSettingsEditor, setShowSettingsEditor] = useState(false);

  // Chat panel state (controlled) - used for dynamic fit view padding
  const [chatPanelOpen, setChatPanelOpen] = useState(true);
  const [chatPanelSize, setChatPanelSize] = useState<PanelSize>("normal");

  // Track if initial fit view has been applied (to prevent flash of default viewport)
  const [isViewReady, setIsViewReady] = useState(false);

  // Navigation state for editing inline loops
  // When editing an inline loop, this holds the parent context
  const [loopEditStack, setLoopEditStack] = useState<InlineEditContext[]>([]);
  const isEditingLoop = loopEditStack.length > 0;

  // Tracks which workflow has been loaded into state. Sentinel `null`
  // means "no load yet" — the first `useLoadWorkflow` effect run sees this
  // mismatch and triggers an initial load (even when `initialWorkflow` is
  // undefined, which would otherwise collide with a `undefined` initial).
  // Replaces the old `hasLoadedRef` boolean.
  const [loadedWorkflowName, setLoadedWorkflowName] = useState<
    string | null | undefined
  >(null);

  // Track if the original workflow was a builtin (passed from parent)
  const isBuiltinWorkflow = isBuiltin;

  // For builtins, we use a "Create a Copy" flow instead of direct editing
  // The user must explicitly copy the workflow with a new name before saving
  const [showTemplateModal, setShowTemplateModal] = useState(false);
  const [templateName, setTemplateName] = useState("");

  // Determine if nodes should be draggable
  const isLocked = workflow.ui?.locked ?? false;
  const canDragNodes = !isBuiltinWorkflow && !isLocked;

  // Exit confirmation modal for unsaved changes
  const [showExitConfirmModal, setShowExitConfirmModal] = useState(false);
  const [isSavingBeforeExit, setIsSavingBeforeExit] = useState(false);

  // Active chat exit modal
  const [showActiveChatModal, setShowActiveChatModal] = useState(false);
  const isChatBusy = useIsChatRunning(builderChatId ?? "");
  const unsubscribeFromChatDetails = useGlobalUpdatesStore(
    (state) => state.unsubscribeFromChatDetails,
  );

  // Track if workflow has been modified (set by mutating handlers; cleared
  // on load / save / agent-update). No ref-dance: every handler that mutates
  // state inlines `setHasModifications(true)`, and the load/save paths flip
  // it back to false. The previous `markDirty` + `hasLoadedRef` +
  // `isApplyingAgentUpdateRef` + `setTimeout(0)` scaffolding existed only
  // because programmatic load/agent-update paths were structurally
  // indistinguishable from user edits — now they aren't.
  const [hasModifications, setHasModifications] = useState(false);

  // Validation state - tracks backend validation results
  const [validationStatus, setValidationStatus] =
    useState<ValidationStatus>("unknown");
  const [validationErrors, setValidationErrors] = useState<ValidationError[]>(
    [],
  );

  // Selection objects derived from ids — keeps ConfigPanel showing fresh
  // data after rename / agent-edit without a separate setSelectedNode sync.
  const selectedNode = useMemo(
    () => (selectedNodeId ? nodes.find((n) => n.id === selectedNodeId) ?? null : null),
    [nodes, selectedNodeId],
  );
  const selectedEdge = useMemo(
    () => (selectedEdgeId ? edges.find((e) => e.id === selectedEdgeId) ?? null : null),
    [edges, selectedEdgeId],
  );

  // YAML editor modal state
  const [showYamlEditor, setShowYamlEditor] = useState(false);

  // Scenario panel modal state
  const [showScenarioPanel, setShowScenarioPanel] = useState(false);

  // Get current project for the chat assistant
  const currentProject = useProjectStore((state) => state.currentProject);

  // Helper function to normalize workflow names (replace spaces with hyphens, trim trailing spaces)
  const normalizeName = (value: string): string => {
    return value.trim().replace(/\s+/g, "-");
  };

  // Undo/Redo functionality
  const {
    takeSnapshot,
    undo,
    redo,
    canUndo,
    canRedo,
    clear: clearHistory,
  } = useUndoRedo({ maxHistorySize: 50 });

  // Get ReactFlow instance helpers used outside of fit-view (zoom buttons + add-step positioning)
  const { zoomIn, zoomOut, screenToFlowPosition } = useReactFlow();

  // Fit view with dynamic padding that accounts for visible panels
  // (extracted to ./hooks/useFitViewWithPanels)
  const fitViewWithPanels = useFitViewWithPanels({
    wrapperRef: reactFlowWrapper,
    chatPanelOpen,
    chatPanelSize,
    hasSelectedNode: !!selectedNodeId,
    hasSelectedEdge: !!selectedEdgeId,
    showSettingsEditor,
    isEditingLoop,
  });

  // Handle node changes and close config panel if selected node is deleted
  const handleNodesChange = useCallback(
    (changes: any[]) => {
      onNodesChange(changes);

      // Find deleted nodes
      const deletedNodeIds = changes
        .filter((change: any) => change.type === "remove")
        .map((change: any) => change.id);

      if (deletedNodeIds.length > 0) {
        // Close config panel if selected node was deleted
        if (selectedNodeId && deletedNodeIds.includes(selectedNodeId)) {
          setSelectedNodeId(null);
        }

        // Remove orphaned edges connected to deleted nodes
        setEdges((eds) =>
          eds.filter(
            (edge) =>
              !deletedNodeIds.includes(edge.source) &&
              !deletedNodeIds.includes(edge.target),
          ),
        );
      }
    },
    [onNodesChange, selectedNodeId, setEdges],
  );

  // Handle edge changes and close config panel if selected edge is deleted
  const handleEdgesChange = useCallback(
    (changes: any[]) => {
      onEdgesChange(changes);

      // Check if selected edge was deleted
      if (selectedEdgeId) {
        const wasDeleted = changes.some(
          (change: any) =>
            change.type === "remove" && change.id === selectedEdgeId,
        );
        if (wasDeleted) {
          setSelectedEdgeId(null);
        }
      }
    },
    [onEdgesChange, selectedEdgeId],
  );

  // Initial workflow load (effect-shaped, extracted to ./hooks/useLoadWorkflow).
  // Same behavior as before: load on mount, or when initialWorkflow.name
  // changes. Hides canvas, swaps nodes/edges, resets metadata, runs the
  // validation RPC, clears history, and resets the inline-edit stack.
  useLoadWorkflow({
    initialWorkflow,
    initialName,
    loadedWorkflowName,
    setLoadedWorkflowName,
    setNodes,
    setEdges,
    setWorkflow,
    setIsViewReady,
    setHasModifications,
    setValidationStatus,
    setValidationErrors,
    setLoopEditStack,
    clearHistory,
    canDragNodes,
    currentProjectId: currentProject?.id,
  });

  // Update workflow name when initialName changes for new workflows
  // This handles the case where the random name arrives after initial render
  useEffect(() => {
    if (!initialWorkflow && initialName && workflow.name === "New Workflow") {
      setWorkflow((w) => ({ ...w, name: initialName }));
    }
  }, [initialName, initialWorkflow, workflow.name]);

  // Trigger fit view after nodes are loaded/changed for a new workflow
  // This is separate from onInit because onInit only fires once on mount
  useEffect(() => {
    // Only run when we have nodes and the view isn't ready yet
    if (nodes.length > 0 && !isViewReady) {
      // Short delay to let ReactFlow measure the nodes
      const timer = setTimeout(() => {
        fitViewWithPanels(false);
        setIsViewReady(true);
      }, 50);
      return () => clearTimeout(timer);
    }
  }, [nodes.length, isViewReady, fitViewWithPanels]);
  const buildWorkflow = useCallback(
    (): Workflow =>
      nodesEdgesToWorkflow(nodes, edges, {
        name: workflow.name ?? "",
        description: workflow.description ?? "",
        inputs: workflow.inputs ?? {},
        outputs: workflow.outputs ?? {},
        entry: workflow.entry,
        tag: workflow.presets?.tag,
        presetDefault: workflow.presets?.default,
        apiVersion: workflow.apiVersion,
        isLocked,
      }),
    [nodes, edges, workflow, isLocked],
  );

  // Inline-edit navigation stack (enter/exit a loop or inline-workflow body).
  // Owns the enter/exit handlers and the loop-expand / workflow-expand
  // CustomEvent listeners. Stack state itself stays here because it's read
  // earlier in the component (fitViewWithPanels, the load effect, the chat
  // handler) — see hook docstring for rationale.
  const { enterInlineEdit, enterLoopEdit, enterWorkflowEdit, exitLoopEdit } =
    useInlineEditStack({
      loopEditStack,
      setLoopEditStack,
      nodes,
      edges,
      buildWorkflow,
      setNodes,
      setEdges,
      setWorkflow,
      setSelectedNodeId,
      setSelectedEdgeId,
      setIsViewReady,
      clearHistory,
      canDragNodes,
    });

  // Auto-drill into a named loop/workflow node after load. Used by the
  // onboarding tour to land users in the multi-node body of a workflow whose
  // top level is a single loop. The signal comes in via the `drillIntoNodeId`
  // prop (sourced from the URL's `?drill=` search param). We consume it once
  // per workflow load via a ref guard — the URL doesn't need clearing because
  // the next navigation drops the search param naturally.
  const consumedDrillRef = useRef<string | null>(null);
  useEffect(() => {
    if (!initialWorkflow || loopEditStack.length > 0) return;
    if (!drillIntoNodeId) return;
    // Skip if we've already consumed this exact drill target for this load.
    const consumeKey = `${initialWorkflow.name ?? ""}::${drillIntoNodeId}`;
    if (consumedDrillRef.current === consumeKey) return;
    consumedDrillRef.current = consumeKey;

    const step = (initialWorkflow.nodes ?? []).find(
      (n) => n.id === drillIntoNodeId,
    );
    if (!step) return;
    if (step.type === "loop") {
      enterInlineEdit(step as LoopStep, "loop");
    } else if (step.type === "workflow" && getStepInline(step as WorkflowStep)) {
      enterInlineEdit(step as WorkflowStep, "workflow");
    }
  }, [initialWorkflow, loopEditStack.length, enterInlineEdit, drillIntoNodeId]);

  // Track drag state only
  const isDraggingRef = useRef(false);
  const dragStartNodesRef = useRef<Node[]>([]);

  // Undo handler
  const handleUndo = useCallback(() => {
    const previousState = undo(nodes, edges);
    if (previousState) {
      setNodes(previousState.nodes);
      setEdges(previousState.edges);
      // Visual feedback is enough - no toast needed
    }
  }, [nodes, edges, undo, setNodes, setEdges]);

  // Redo handler
  const handleRedo = useCallback(() => {
    const nextState = redo();
    if (nextState) {
      setNodes(nextState.nodes);
      setEdges(nextState.edges);
      // Visual feedback is enough - no toast needed
    }
  }, [redo, setNodes, setEdges]);

  // Reorganize all nodes into a clean, readable layout
  const handleOrganizeNodes = useCallback(() => {
    if (isBuiltinWorkflow || nodes.length === 0) {
      return;
    }

    takeSnapshot(nodes, edges);
    setHasModifications(true);

    const workflowToLayout = buildWorkflow();
    const layoutedWorkflow = autoLayoutWorkflow(workflowToLayout);
    const layoutPositions = layoutedWorkflow.ui?.positions || {};

    setNodes((currentNodes) => {
      const relaidOutNodes = currentNodes.map((node) => {
        const layoutPos = layoutPositions[node.id] as
          | { x?: number; y?: number }
          | undefined;

        if (layoutPos?.x === undefined || layoutPos?.y === undefined) {
          return node;
        }

        return {
          ...node,
          position: { x: layoutPos.x, y: layoutPos.y },
        };
      });

      return resolveNodeOverlaps(relaidOutNodes);
    });

    // Re-center the viewport after ReactFlow applies new positions
    setTimeout(() => fitViewWithPanels(true), 0);
    toast.success("Nodes organized");
  }, [
    isBuiltinWorkflow,
    nodes,
    edges,
    takeSnapshot,
    buildWorkflow,
    setNodes,
    fitViewWithPanels,
  ]);

  // Browser-level unsaved-changes guard. `handleBackClick` covers in-app
  // navigation, but Cmd-R / Cmd-W / closing the Electron window bypass that
  // handler and would silently drop unsaved edits. The browser/Electron will
  // show its native confirm dialog when `returnValue` is set on
  // `beforeunload`. Builtin workflows can't be saved, so we don't bother.
  useEffect(() => {
    if (isBuiltinWorkflow || !hasModifications) return;
    const handler = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = "";
    };
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [hasModifications, isBuiltinWorkflow]);

  // Back button handler - check for active chat first, then unsaved changes
  const handleBackClick = useCallback(() => {
    // If chat is actively running, warn about that first
    if (isChatBusy) {
      setShowActiveChatModal(true);
      return;
    }
    // Show warning if there are any modifications
    // Skip for builtin workflows since they can't be saved anyway
    if (!isBuiltinWorkflow && hasModifications) {
      setShowExitConfirmModal(true);
    } else {
      onBack?.();
    }
  }, [isChatBusy, hasModifications, onBack, isBuiltinWorkflow]);

  // Handle "Cancel Chat & Exit" from active chat modal
  const handleCancelChatAndExit = useCallback(() => {
    if (builderChatId) {
      unsubscribeFromChatDetails();
    }
    setShowActiveChatModal(false);
    // Fall through to modifications check
    if (!isBuiltinWorkflow && hasModifications) {
      setShowExitConfirmModal(true);
    } else {
      onBack?.();
    }
  }, [
    builderChatId,
    unsubscribeFromChatDetails,
    isBuiltinWorkflow,
    hasModifications,
    onBack,
  ]);

  // Handle "Run in Background" from active chat modal
  const handleRunInBackground = useCallback(() => {
    setShowActiveChatModal(false);
    onBack?.();
  }, [onBack]);

  // Discard and exit handler
  const handleDiscardAndExit = useCallback(() => {
    setShowExitConfirmModal(false);
    onBack?.();
  }, [onBack]);

  // Keyboard shortcuts (Ctrl/Cmd+Z undo, +Shift+Z / +Y redo, and Escape
  // deselect/exit-inline/back) — see ./hooks/useWorkflowKeyboardShortcuts.
  useWorkflowKeyboardShortcuts({
    onUndo: handleUndo,
    onRedo: handleRedo,
    onEscape: handleBackClick,
    isEditingLoop,
    exitLoopEdit,
    isBuiltinWorkflow,
    hasSelectedNode: !!selectedNodeId,
    hasSelectedEdge: !!selectedEdgeId,
    showSettingsEditor,
    setSelectedNodeId,
    setSelectedEdgeId,
    setShowSettingsEditor,
    showTemplateModal,
    showExitConfirmModal,
    showActiveChatModal,
  });

  // Recompute sibling layout info when the edge topology changes. Keying on
  // `edges.length` misses the case where an edge is rerouted (same count,
  // different source/target) — labels then render in the wrong order. We
  // depend on a topology fingerprint and only call setEdges when at least one
  // edge's siblingIndex/totalSiblings actually changes (preserving array
  // identity is what keeps this from looping).
  const edgeTopologyKey = useMemo(
    () => edges.map((e) => `${e.id}|${e.source}>${e.target}`).join(","),
    [edges],
  );
  useEffect(() => {
    setEdges((currentEdges) => {
      const groups = new Map<string, Edge[]>();
      currentEdges.forEach((edge) => {
        const key = `${edge.source}-${edge.target}`;
        if (!groups.has(key)) groups.set(key, []);
        groups.get(key)!.push(edge);
      });

      let changed = false;
      const next = currentEdges.map((edge) => {
        const key = `${edge.source}-${edge.target}`;
        const siblings = groups.get(key) || [];
        const siblingIndex = siblings.findIndex((e) => e.id === edge.id);
        const totalSiblings = siblings.length;
        if (
          edge.data?.siblingIndex !== siblingIndex ||
          edge.data?.totalSiblings !== totalSiblings
        ) {
          changed = true;
          return {
            ...edge,
            data: { ...edge.data, siblingIndex, totalSiblings },
          };
        }
        return edge;
      });
      return changed ? next : currentEdges;
    });
  }, [edgeTopologyKey, setEdges]);

  // Handle edge selection
  const onEdgeClick = useCallback((_event: React.MouseEvent, edge: Edge) => {
    setShowSettingsEditor(false);
    setSelectedEdgeId(edge.id);
    setSelectedNodeId(null);
    setChatPanelOpen(false); // Close chat when config panel opens
  }, []);

  // Handle clicking on canvas (deselect)
  const onPaneClick = useCallback(() => {
    setSelectedNodeId(null);
    setSelectedEdgeId(null);
    setShowSettingsEditor(false);
  }, []);

  // Mutation handlers (updateStep, removeNode, renameNode, updateSwitchNode,
  // updateEdge, removeEdge) now live in <WorkflowMutationProvider>. Config
  // panels call `useWorkflowMutations()` directly instead of receiving
  // prop-drilled callbacks. See ./WorkflowMutationContext.tsx.

  // Create a new edge, optionally as part of an existing switch
  const createEdge = useCallback(
    (sourceId: string, targetId: string, sourceHandle?: string) => {
      const sourceNode = nodes.find((n) => n.id === sourceId);
      const targetNode = nodes.find((n) => n.id === targetId);

      if (!sourceNode || !targetNode) return;

      takeSnapshot(nodes, edges);
      setHasModifications(true);

      const newEdgeId = `edge-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;

      // Determine sourceEvent for edge data
      // For event nodes (like workflow start), preserve the eventType
      // This is needed for buildWorkflow to correctly convert edges back to workflow format
      const sourceNodeData = sourceNode.data as { eventType?: string };
      const sourceEvent =
        sourceNode.type === "eventNode" && sourceNodeData.eventType
          ? sourceNodeData.eventType
          : undefined;

      const newEdge: Edge = {
        id: newEdgeId,
        source: sourceId,
        target: targetId,
        sourceHandle: sourceHandle || undefined,
        type: "custom",
        data: {
          sourceEvent,
        },
      };

      setEdges((eds) => [...eds, newEdge]);

      return newEdge;
    },
    [nodes, edges, setEdges, takeSnapshot],
  );

  const onConnect = useCallback(
    (params: Connection) => {
      if (!params.source || !params.target) return;
      createEdge(
        params.source,
        params.target,
        params.sourceHandle || undefined,
      );
    },
    [createEdge],
  );

  // Handle node selection
  const onNodeClick = useCallback((_event: React.MouseEvent, node: Node) => {
    setShowSettingsEditor(false);
    setSelectedNodeId(node.id);
    setSelectedEdgeId(null);
    setChatPanelOpen(false); // Close chat when config panel opens
  }, []);

  // Navigate to a node by ID (used by validation error clicks)
  const navigateToNode = useCallback(
    (nodeId: string) => {
      if (nodes.some((n) => n.id === nodeId)) {
        setShowSettingsEditor(false);
        setSelectedNodeId(nodeId);
        setSelectedEdgeId(null);
        setChatPanelOpen(false);
      }
    },
    [nodes],
  );

  const handleSave = useCallback(async () => {
    if (isBuiltinWorkflow) {
      toast.error(
        'Use "Create a Copy" to create your own copy of this workflow',
        { duration: 3000 },
      );
      return;
    }

    if (!workflow.name || (workflow.name ?? "").trim() === "") {
      toast.error("Please give your workflow a name before saving", {
        duration: 3000,
      });
      return;
    }

    const builtWorkflow = buildWorkflow();

    try {
      const result = await onSave?.(builtWorkflow);

      setLoadedWorkflowName(builtWorkflow.name);
      setHasModifications(false);

      // Update validation status from save result
      if (
        result &&
        typeof result === "object" &&
        "validationErrors" in result
      ) {
        const errors = result.validationErrors || [];
        setValidationErrors(errors as ValidationError[]);
        setValidationStatus(errors.length === 0 ? "valid" : "invalid");
      }

      toast.success("Workflow saved", { duration: 2000 });
    } catch (error) {
      console.error("Save failed:", error);
    }
  }, [buildWorkflow, onSave, workflow.name, isBuiltinWorkflow, nodes, edges]);

  // Save and exit handler
  const handleSaveAndExit = useCallback(async () => {
    setIsSavingBeforeExit(true);
    try {
      await handleSave();
      setShowExitConfirmModal(false);
      onBack?.();
    } catch (error) {
      console.error("Save before exit failed:", error);
      // Still allow exit on error - user can choose to discard
    } finally {
      setIsSavingBeforeExit(false);
    }
  }, [handleSave, onBack]);

  // Handle "Create a Copy" - copy a builtin workflow with a new name
  const handleUseAsTemplate = useCallback(() => {
    // For editable workflows, prompt to save pending edits to the source first
    // so the copy starts from the persisted state. Builtins can't be saved
    // (the copy IS the save), so skip this check for them.
    if (!isBuiltinWorkflow && hasModifications) {
      const proceed = window.confirm(
        "You have unsaved changes. Save them to this workflow before creating a copy?\n\n" +
          "OK: Save current workflow, then create the copy.\n" +
          "Cancel: Abort — make a copy with no source changes.",
      );
      if (!proceed) return;
      void handleSave().then(() => {
        const builtinName = initialWorkflow?.name
          ? normalizeWorkflowRef(initialWorkflow.name)
          : "workflow";
        const randomSuffix = Math.random().toString(36).substring(2, 8);
        setTemplateName(`my-${builtinName}-${randomSuffix}`);
        setShowTemplateModal(true);
      });
      return;
    }

    // Suggest a default name based on the source name with a random suffix for uniqueness
    const builtinName = initialWorkflow?.name
      ? normalizeWorkflowRef(initialWorkflow.name)
      : "workflow";
    const randomSuffix = Math.random().toString(36).substring(2, 8);
    setTemplateName(`my-${builtinName}-${randomSuffix}`);
    setShowTemplateModal(true);
  }, [initialWorkflow?.name, isBuiltinWorkflow, hasModifications, handleSave]);

  const handleTemplateConfirm = useCallback(async () => {
    const normalizedName = normalizeName(templateName);

    if (!normalizedName) {
      toast.error("Please enter a valid workflow name", { duration: 3000 });
      return;
    }

    // Update the workflow name - this will make it no longer a "builtin"
    setWorkflow((w) => ({ ...w, name: normalizedName }));
    setShowTemplateModal(false);

    // Build and save the workflow with the new name
    const builtWorkflow = buildWorkflow();
    builtWorkflow.name = normalizedName;

    try {
      await onSave?.(builtWorkflow);
      setLoadedWorkflowName(normalizedName);
      setHasModifications(false);
      toast.success(`Created "${normalizedName}" from template`, {
        duration: 3000,
      });
      // Navigate to the new copy so the URL matches the workflow now in view.
      // Without this, the URL still points at the source workflow and a
      // refresh would reload the original instead of the user's copy.
      onNavigateToWorkflow?.(normalizedName);
    } catch (error) {
      console.error("Failed to save template:", error);
    }
  }, [templateName, buildWorkflow, onSave, onNavigateToWorkflow, nodes, edges]);

  // Derived persistence representation. Re-computed when nodes/edges/workflow
  // change — but consumers should depend on stable structural keys (see
  // CEL context below) rather than this object on every keystroke.
  const currentWorkflow = useMemo(() => buildWorkflow(), [buildWorkflow]);

  // Get list of existing node IDs for validation (used by ConfigPanel to prevent duplicates)
  const existingNodeIds = useMemo(() => nodes.map((node) => node.id), [nodes]);

  // Stable structural key for the CEL context — only invalidates when the
  // shape (ids, types, declared outputs, edge wiring, input keys) changes.
  // Keystrokes that only edit labels / CEL strings inside a node don't
  // trip this, so Monaco completion doesn't re-init on every keypress.
  const celContextKey = useMemo(() => {
    const nodeBits = nodes.map((node) => {
      if (node.type === "eventNode" || node.type === "switchNode") return "";
      const step = (node.data as FlowNodeData).step as Step | undefined;
      let routerOutputKeys = "";
      if (step?.type === "router" && step.args?.case === "router") {
        const routerOutputs = (step.args.value as Record<string, unknown>)?.outputs as
          | Record<string, string>
          | undefined;
        if (routerOutputs) {
          routerOutputKeys = Object.keys(routerOutputs).sort().join(",");
        }
      }
      return `${node.id}:${step?.type ?? ""}:${routerOutputKeys}`;
    });
    const edgeBits = edges.map((e) => `${e.source}>${e.target}`);
    const inputBits = workflow.inputs
      ? Object.entries(workflow.inputs)
          .map(([k, p]) => `${k}:${p.type ?? "string"}`)
          .sort()
          .join(",")
      : "";
    return `${nodeBits.join("|")}__${edgeBits.join("|")}__${inputBits}`;
  }, [nodes, edges, workflow.inputs]);

  // Build CEL completion context for Monaco editors in config panels.
  // Keyed on the structural fingerprint above so it's stable across edits
  // that don't change shape.
  const celCompletionContext = useMemo<CELCompletionContextValue>(() => {
    const nodeIds: string[] = [];
    const nodeTypeMap: Record<string, string> = {};
    const nodeDeclaredOutputs: Record<string, string[]> = {};
    for (const node of nodes) {
      if (node.type === "eventNode" || node.type === "switchNode") continue;
      const step = (node.data as FlowNodeData).step as Step | undefined;
      nodeIds.push(node.id);
      if (step?.type) {
        nodeTypeMap[node.id] = step.type;
      }
      // Collect declared outputs from router nodes
      if (step?.type === "router" && step.args?.case === "router") {
        const routerOutputs = (step.args.value as Record<string, unknown>)?.outputs as Record<string, string> | undefined;
        if (routerOutputs && Object.keys(routerOutputs).length > 0) {
          nodeDeclaredOutputs[node.id] = Object.keys(routerOutputs);
        }
      }
    }
    const inputParams: Record<string, { type: string; description?: string }> =
      {};
    if (workflow.inputs) {
      for (const [key, param] of Object.entries(workflow.inputs)) {
        inputParams[key] = {
          type: param.type ?? "string",
          description: getInputDescription(param as InputDef),
        };
      }
    }
    const edgeList = edges.map((e) => ({ source: e.source, target: e.target }));
    return { nodeIds, nodeTypeMap, inputParams, edges: edgeList, nodeDeclaredOutputs };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- intentionally
    // keyed on the structural fingerprint to avoid re-init on label/CEL keystrokes
  }, [celContextKey]);

  // Handle workflow updates from the chat assistant.
  //
  // Post-Wave-4 this collapsed from 300+ lines (with ref dancing + 8
  // mirrored setters) to a small handler: replace the workflow state in
  // one call, replace the ReactFlow arrays, clear dirty (agent already
  // saved via edit_workflow). Selection objects are derived during render
  // so no manual sync is needed.
  const handleChatWorkflowUpdate = useCallback(
    (updatedWorkflow: Workflow) => {
      if (!updatedWorkflow || !updatedWorkflow.nodes) {
        return;
      }

      // Loop-edit mode: chat receives the parent workflow, not the loop body.
      // Update the stack so exiting the loop preserves the agent's changes;
      // don't touch the canvas (it shows the loop body).
      if (isEditingLoop && loopEditStack.length > 0) {
        const { nodes: allParentNodes, edges: parentFlowEdges } =
          workflowToFlowElements(updatedWorkflow, { draggable: canDragNodes });
        setLoopEditStack((prev) => {
          const next = [...prev];
          next[next.length - 1] = {
            ...next[next.length - 1],
            parentWorkflow: updatedWorkflow,
            parentNodes: allParentNodes as Node[],
            parentEdges: parentFlowEdges as Edge[],
          };
          return next;
        });
        toast.success("Workflow updated by assistant", { duration: 2000 });
        return;
      }

      // Detect "actually changed" so we don't toast on initial sync.
      const currentNodeIds = nodes
        .filter((n) => n.id !== "workflow" && !n.id.startsWith("switch-"))
        .map((n) => n.id)
        .sort();
      const incomingNodeIds = updatedWorkflow.nodes.map((n) => n.id).sort();
      const nodesChanged =
        JSON.stringify(currentNodeIds) !== JSON.stringify(incomingNodeIds);

      if (nodesChanged) {
        takeSnapshot(nodes, edges);
      }

      const { nodes: allNodes, edges: flowEdges } = workflowToFlowElements(
        updatedWorkflow,
        { draggable: canDragNodes },
      );
      setWorkflow(updatedWorkflow);
      setNodes(allNodes as Node[]);
      setEdges(flowEdges as Edge[]);
      // Agent already persisted via edit_workflow — this is a clean load.
      setHasModifications(false);

      if (nodesChanged) {
        toast.success("Workflow updated by assistant", { duration: 2000 });
      }
    },
    [
      nodes,
      edges,
      takeSnapshot,
      setNodes,
      setEdges,
      setLoopEditStack,
      isEditingLoop,
      loopEditStack,
      canDragNodes,
    ],
  );

  // Check if two nodes overlap
  const nodesOverlap = (node1: Node, node2: Node): boolean => {
    const padding = 20;
    const node1Width = 200;
    const node1Height = 100;

    return (
      node1.position.x < node2.position.x + node1Width + padding &&
      node1.position.x + node1Width + padding > node2.position.x &&
      node1.position.y < node2.position.y + node1Height + padding &&
      node1.position.y + node1Height + padding > node2.position.y
    );
  };

  // Find non-overlapping position
  const findNonOverlappingPosition = useCallback((
    node: Node,
    allNodes: Node[],
  ): { x: number; y: number } => {
    const originalPos = { ...node.position };
    const offset = 150;
    const directions = [
      { x: offset, y: 0 },
      { x: 0, y: offset },
      { x: -offset, y: 0 },
      { x: 0, y: -offset },
      { x: offset, y: offset },
    ];

    for (const dir of directions) {
      const testPos = { x: originalPos.x + dir.x, y: originalPos.y + dir.y };
      const testNode = { ...node, position: testPos };
      const hasOverlap = allNodes.some(
        (n) => n.id !== node.id && nodesOverlap(testNode, n),
      );

      if (!hasOverlap) return testPos;
    }

    return { x: originalPos.x + offset, y: originalPos.y + offset };
  }, []);

  // Handle node drag start - take snapshot BEFORE drag
  const handleNodeDragStart = useCallback(() => {
    isDraggingRef.current = true;
    // Save the state before drag starts
    dragStartNodesRef.current = JSON.parse(JSON.stringify(nodes));
    // Take snapshot BEFORE drag (captures pre-drag state)
    takeSnapshot(nodes, edges);
    setHasModifications(true);
  }, [nodes, edges, takeSnapshot]);

  // Handle node drag stop - just mark drag as complete
  const handleNodeDragStop = useCallback(
    (_: any, node: Node) => {
      // Mark dragging as complete
      isDraggingRef.current = false;

      const overlappingNodes = nodes.filter(
        (n) => n.id !== node.id && nodesOverlap(node, n),
      );

      if (overlappingNodes.length > 0) {
        // Add animation class to moved nodes
        const movedNodeIds = overlappingNodes.map((n) => n.id);
        movedNodeIds.forEach((id) => {
          const element = document.querySelector(`[data-id="${id}"]`);
          if (element) {
            element.classList.add("auto-repositioning");
            setTimeout(
              () => element.classList.remove("auto-repositioning"),
              400,
            );
          }
        });

        setNodes((nds) =>
          nds.map((n) => {
            if (movedNodeIds.includes(n.id)) {
              return { ...n, position: findNonOverlappingPosition(n, nds) };
            }
            return n;
          }),
        );
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps -- findNonOverlappingPosition is stable
    [nodes, setNodes],
  );

  // Structural types that have their own node components
  // Defined as a constant outside useCallback to avoid recreation
  const STRUCTURAL_TYPES = useMemo(
    () => new Set(["run", "workflow", "agent", "join", "loop", "router"]),
    [],
  );

  const addStep = useCallback(
    (stepType: string) => {
      // Take snapshot BEFORE adding node
      takeSnapshot(nodes, edges);
      setHasModifications(true);

      // Use a CEL-safe ID prefix (e.g., call_llm_123, run_456)
      const id = `${stepType}_${Date.now()}`;
      // Place new node at the center of the current viewport
      const wrapper = reactFlowWrapper.current;
      const vpCenterX = (wrapper?.clientWidth ?? 1200) / 2;
      const vpCenterY = (wrapper?.clientHeight ?? 800) / 2;
      const flowCenter = screenToFlowPosition({ x: vpCenterX, y: vpCenterY });
      const candidateNode: Node = {
        id: `_candidate_${id}`,
        type: 'actionNode',
        position: flowCenter,
        data: { label: '' },
      };
      const position = findNonOverlappingPosition(candidateNode, nodes);

      // Build step with args oneof initialized
      // Note: position is stored in workflow.ui.positions, not on step
      let step: Step = {
        id,
        type: stepType,
        args: initStepArgs(stepType),
      };

      // Add type-specific default fields
      if (stepType === "run") {
        step = withRunArgs(step, { command: celString("") });
      } else if (stepType === "workflow") {
        step = withWorkflowArgs(step, { ref: celString("builtin://agent") });
      } else if (stepType === "agent") {
        // agent is not a structural type, no args oneof needed
      } else if (stepType === "join") {
        step.condition = directCel("all");
      } else if (stepType === "loop") {
        step = withLoopArgs(step, { while: directCel(""), ref: celString("") });
      } else if (stepType === "router") {
        // Router args are initialized by initStepArgs — no additional defaults needed
      }

      // Determine React Flow node type
      const flowNodeType = STRUCTURAL_TYPES.has(stepType)
        ? `${stepType}Node`
        : "actionNode";

      const newNode: Node = {
        id,
        type: flowNodeType,
        position,
        data: {
          step,
          label: id,
        },
      };

      setNodes((nds) => nds.concat(newNode));
      // Auto-select the newly created node to open config panel
      setSelectedNodeId(newNode.id);
      setSelectedEdgeId(null);
      setShowSettingsEditor(false);
      // Close chat panel when config panel opens
      setChatPanelOpen(false);
    },
    [setNodes, nodes, edges, takeSnapshot, STRUCTURAL_TYPES, screenToFlowPosition, findNonOverlappingPosition],
  );

  const addSwitch = useCallback(() => {
    takeSnapshot(nodes, edges);
    setHasModifications(true);

    const id = `switch_${Date.now()}`;
    // Place new switch at the center of the current viewport
    const wrapperEl = reactFlowWrapper.current;
    const switchVpCenterX = (wrapperEl?.clientWidth ?? 1200) / 2;
    const switchVpCenterY = (wrapperEl?.clientHeight ?? 800) / 2;
    const switchFlowCenter = screenToFlowPosition({ x: switchVpCenterX, y: switchVpCenterY });
    const switchCandidateNode: Node = {
      id: `_candidate_${id}`,
      type: 'switchNode',
      position: switchFlowCenter,
      data: { label: '' },
    };
    const position = findNonOverlappingPosition(switchCandidateNode, nodes);

    const newNode: Node = {
      id,
      type: "switchNode",
      position,
      data: {
        label: "Switch",
        cases: [
          { id: `case_${Date.now()}_1`, condition: "", label: "" }, // First case (will need condition)
          { id: `case_${Date.now()}_2`, condition: "", label: "" }, // Default case (last, no condition)
        ],
      },
      draggable: canDragNodes,
    };

    setNodes((nds) => nds.concat(newNode));
    // Auto-select the newly created switch to open config panel
    setSelectedNodeId(newNode.id);
    setSelectedEdgeId(null);
    setShowSettingsEditor(false);
    // Close chat panel when config panel opens
    setChatPanelOpen(false);
  }, [setNodes, nodes, edges, takeSnapshot, canDragNodes, screenToFlowPosition, findNonOverlappingPosition]);

  const headerButtonClass = "inline-flex items-center gap-2 rounded-lg border border-border/70 bg-card/90 px-3 py-2 text-sm font-medium text-muted-foreground shadow-sm shadow-black/5 transition-colors hover:bg-muted hover:text-foreground";
  const secondaryHeaderButtonClass = "inline-flex items-center gap-2 rounded-lg border border-border/70 bg-secondary px-3 py-2 text-sm font-semibold text-secondary-foreground shadow-sm shadow-black/5 transition-colors hover:bg-secondary/90";
  const primaryHeaderButtonClass = "inline-flex items-center gap-2 rounded-lg bg-primary px-3 py-2 text-sm font-semibold text-primary-foreground shadow-sm shadow-primary/20 transition-colors hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-50";
  const compactIconButtonClass = "inline-flex h-9 w-9 items-center justify-center rounded-lg text-muted-foreground transition-colors hover:bg-muted hover:text-foreground";

  return (
    <WorkflowMutationProvider
      nodes={nodes}
      edges={edges}
      setNodes={setNodes}
      setEdges={setEdges}
      setHasModifications={setHasModifications}
      takeSnapshot={takeSnapshot}
      setSelectedNodeId={setSelectedNodeId}
      setSelectedEdgeId={setSelectedEdgeId}
    >
    <WorkflowNodeCallbacksProvider
      onExpandLoop={(_loopNodeId, step) => enterLoopEdit(step)}
      onExpandWorkflow={(_workflowNodeId, step) => enterWorkflowEdit(step)}
    >
    <div className="relative h-full bg-background">
      {/* Floating Left Sidebar Stack - position based on visible headers (builtin banner + breadcrumb) */}
      <div
        className={`absolute left-6 z-50 flex flex-col gap-3 ${
          isBuiltinWorkflow && isEditingLoop
            ? "top-36"
            : isBuiltinWorkflow || isEditingLoop
              ? "top-24"
              : "top-16"
        }`}
      >
        {/* Hide add-nodes sidebar for builtin workflows (view-only) */}
        {!isBuiltinWorkflow && (
          <FloatingWorkflowSidebar
            onAddStep={addStep}
            onAddSwitch={addSwitch}
          />
        )}

        {/* Stats Counter */}
        <div className="rounded-2xl border border-border/80 bg-card/95 p-3 shadow-xl shadow-black/10 backdrop-blur-sm">
          <div className="grid grid-cols-2 gap-2 text-center">
            <div className="rounded-lg bg-muted/50 px-3 py-2">
              <div className="text-[10px] font-semibold uppercase tracking-[0.08em] text-muted-foreground">Steps</div>
              <div className="text-lg font-semibold leading-none text-foreground">{nodes.length}</div>
            </div>
            <div className="rounded-lg bg-muted/50 px-3 py-2">
              <div className="text-[10px] font-semibold uppercase tracking-[0.08em] text-muted-foreground">Edges</div>
              <div className="text-lg font-semibold leading-none text-foreground">{edges.length}</div>
            </div>
          </div>
        </div>
      </div>

      {/* Main Canvas Area - Full Width */}
      <div className="flex flex-col h-full bg-background">
        {/* Builtin workflow banner */}
        {isBuiltinWorkflow && (
          <div className="bg-primary/10 border-b border-primary/30 px-4 py-2.5 flex items-center justify-between">
            <div className="flex items-center gap-2.5">
              <Lock className="w-4 h-4 text-primary shrink-0" />
              <span className="text-primary text-sm font-semibold">
                View Only
              </span>
              <span className="text-primary/60 text-sm">—</span>
              <span className="text-primary/90 text-sm">
                This is a {source === "project" ? "project" : "built-in"} template. Click <strong className="text-primary">"Create a Copy"</strong> to create an
                editable copy.
              </span>
            </div>
            <a
              href="https://docs.reliantlabs.io/workflows"
              target="_blank"
              rel="noopener noreferrer"
              className="flex items-center gap-1.5 text-primary/80 hover:text-primary text-xs transition-colors shrink-0"
            >
              Learn more
              <ExternalLink className="w-3 h-3" />
            </a>
          </div>
        )}

        {/* Header */}
        <div className="flex items-center justify-between gap-4 border-b border-border/70 bg-card/80 px-4 py-3 shadow-sm shadow-black/5 backdrop-blur-sm">
          <div className="flex items-center gap-3">
            {/* Back button - either exit loop edit or go back to workflows */}
            {isEditingLoop ? (
              <button
                onClick={() => exitLoopEdit(!isBuiltinWorkflow)}
                className={compactIconButtonClass}
                title={
                  isBuiltinWorkflow
                    ? "Exit loop viewer"
                    : "Save and exit loop editor"
                }
              >
                <ArrowLeft className="w-5 h-5 text-muted-foreground" />
              </button>
            ) : (
              onBack && (
                <button
                  onClick={handleBackClick}
                  className={compactIconButtonClass}
                  title="Back to workflows"
                >
                  <ArrowLeft className="w-5 h-5 text-muted-foreground" />
                </button>
              )
            )}
            <div className="flex items-center gap-2">
              {isEditingName && !isEditingLoop ? (
                <input
                  type="text"
                  value={workflow.name ?? ""}
                  onChange={(e) => {
                    setWorkflow((w) => ({ ...w, name: e.target.value }));
                    setHasModifications(true);
                  }}
                  onBlur={() => {
                    // Apply normalization on blur
                    setWorkflow((w) => ({
                      ...w,
                      name: normalizeName(w.name ?? ""),
                    }));
                    setIsEditingName(false);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      // Apply normalization on Enter
                      setWorkflow((w) => ({
                        ...w,
                        name: normalizeName(w.name ?? ""),
                      }));
                      setIsEditingName(false);
                    }
                  }}
                  autoFocus
                  className="text-2xl font-bold border-none outline-none focus:ring-0 bg-transparent text-foreground flex-shrink-0"
                  placeholder="Workflow Name"
                  style={{
                    width: "auto",
                    minWidth: "200px",
                    maxWidth: "600px",
                  }}
                />
              ) : (
                <>
                  <h1 className="text-2xl font-bold text-foreground">
                    {workflow.name}
                  </h1>
                  {/* Hide edit button for builtins and when editing loops */}
                  {!isEditingLoop && !isBuiltinWorkflow && (
                    <button
                      onClick={() => setIsEditingName(true)}
                      className="p-1 hover:bg-muted rounded transition-colors self-center"
                      title="Edit workflow name"
                    >
                      <Pencil className="w-5 h-5 text-muted-foreground" />
                    </button>
                  )}
                  {/* Info button - shows workflow description and metadata */}
                  {!isEditingLoop && (
                    <button
                      onClick={() => setShowInfoPopover(true)}
                      className="p-1 hover:bg-muted rounded transition-colors self-center"
                      title="Workflow info"
                    >
                      <Info className="w-5 h-5 text-muted-foreground" />
                    </button>
                  )}
                </>
              )}
            </div>
            {/* Validation Status Badge - show validation state */}
            {!isEditingLoop && (
              <ValidationStatusBadge
                status={validationStatus}
                errors={validationErrors}
                onNodeClick={navigateToNode}
                className="ml-2"
              />
            )}
          </div>
          <div className="flex gap-2 flex-shrink-0">
            {isEditingLoop && isBuiltinWorkflow ? (
              // Loop viewing mode for builtin workflow - just show Done button
              <button
                onClick={() => exitLoopEdit(false)}
                className={secondaryHeaderButtonClass}
              >
                Done
              </button>
            ) : isEditingLoop ? (
              // Loop editing mode - show Save Loop Body and Discard buttons
              <>
                <button
                  onClick={() => exitLoopEdit(false)}
                  className={headerButtonClass}
                >
                  Discard Changes
                </button>
                <button
                  onClick={() => exitLoopEdit(true)}
                  className={secondaryHeaderButtonClass}
                >
                  Apply Changes
                </button>
              </>
            ) : isBuiltinWorkflow ? (
              // Builtin workflow - show "Create a Copy" button
              <>
                <button
                  onClick={handleUseAsTemplate}
                  className={secondaryHeaderButtonClass}
                >
                  <Copy className="w-4 h-4" />
                  Create a Copy
                </button>
                <button
                  onClick={() => setShowYamlEditor(true)}
                  className={headerButtonClass}
                >
                  <Code className="w-4 h-4" />
                  View YAML
                </button>
              </>
            ) : (
              // Normal mode - show standard workflow buttons
              <>
                <button
                  onClick={() => setShowYamlEditor(true)}
                  className={headerButtonClass}
                >
                  <Code className="w-4 h-4" />
                  YAML
                </button>
                <button
                  onClick={() => setShowScenarioPanel(true)}
                  className={headerButtonClass}
                >
                  <TestTube2 className="w-4 h-4" />
                  Tests
                </button>
                <button
                  onClick={() => {
                    setSelectedNodeId(null);
                    setSelectedEdgeId(null);
                    setShowSettingsEditor(true);
                    setChatPanelOpen(false);
                  }}
                  className={headerButtonClass}
                >
                  <Settings2 className="w-4 h-4" />
                  Parameters
                </button>
                <button
                  onClick={handleUseAsTemplate}
                  className={headerButtonClass}
                >
                  <Copy className="w-4 h-4" />
                  Duplicate
                </button>
                <button
                  onClick={handleSave}
                  disabled={!hasModifications || isChatBusy}
                  title={
                    isChatBusy ? "Wait for assistant to finish" : undefined
                  }
                  className={primaryHeaderButtonClass}
                >
                  {isChatBusy ? "Working..." : "Save"}
                </button>
              </>
            )}
          </div>
        </div>

        {/* Canvas */}
        <div
          ref={reactFlowWrapper}
          className={`flex-1 bg-background ${interactionMode === "select" ? "selection-mode" : "pan-mode"} ${isViewReady ? "opacity-100" : "opacity-0"}`}
          data-onboarding="workflow-canvas"
        >
          <ReactFlow
            nodes={nodes}
            edges={edges}
            onNodesChange={handleNodesChange}
            onEdgesChange={handleEdgesChange}
            onConnect={onConnect}
            onNodeClick={onNodeClick}
            onEdgeClick={onEdgeClick}
            onPaneClick={onPaneClick}
            nodeTypes={nodeTypes}
            edgeTypes={edgeTypes}
            connectionLineType={ConnectionLineType.Bezier}
            connectionLineStyle={{
              stroke: "hsl(var(--border))",
              strokeWidth: 2,
            }}
            defaultEdgeOptions={{
              type: "custom",
              markerEnd: { type: "arrowclosed", color: "hsl(var(--border))" },
            }}
            edgesReconnectable={!isBuiltinWorkflow}
            nodesConnectable={!isBuiltinWorkflow}
            nodesDraggable={canDragNodes}
            deleteKeyCode={isBuiltinWorkflow ? null : "Backspace"}
            onNodeDragStart={handleNodeDragStart}
            onNodeDragStop={handleNodeDragStop}
            onNodesDelete={
              isBuiltinWorkflow
                ? undefined
                : () => {
                    // Take snapshot BEFORE ReactFlow deletes nodes
                    takeSnapshot(nodes, edges);
                    setHasModifications(true);
                  }
            }
            onEdgesDelete={
              isBuiltinWorkflow
                ? undefined
                : () => {
                    // Take snapshot BEFORE ReactFlow deletes edges
                    takeSnapshot(nodes, edges);
                    setHasModifications(true);
                  }
            }
            panOnDrag={interactionMode === "pan"}
            selectionOnDrag={interactionMode === "select"}
            panOnScroll={interactionMode === "pan"}
            selectionMode={"partial" as SelectionMode}
            proOptions={{ hideAttribution: true }}
          >
            <Background
              id="workflow-bg"
              gap={24}
              color="hsl(var(--muted-foreground))"
              size={1.5}
              variant={"dots" as BackgroundVariant}
            />

            {/* Floating Toolbar - Bottom Center */}
            <Panel position="bottom-center" className="mb-4">
              <FloatingToolbar
                mode={interactionMode}
                onModeChange={setInteractionMode}
                onUndo={handleUndo}
                onRedo={handleRedo}
                canUndo={canUndo}
                canRedo={canRedo}
                onZoomIn={() => zoomIn()}
                onZoomOut={() => zoomOut()}
                onFitView={fitViewWithPanels}
                onOrganizeNodes={handleOrganizeNodes}
                isLocked={isLocked}
                onLockToggle={() => {
                  setWorkflow((w) => ({
                    ...w,
                    ui: { ...w.ui, locked: !isLocked },
                  }));
                  setHasModifications(true);
                }}
                isReadOnly={isBuiltinWorkflow}
              />
            </Panel>
          </ReactFlow>
        </div>
      </div>

      {/* Config Panel - Floating on Right */}
      {(() => {
        // Calculate top offset to align with left sidebar (based on visible headers)
        const configPanelTopOffset =
          isBuiltinWorkflow && isEditingLoop
            ? 144 // top-36
            : isBuiltinWorkflow || isEditingLoop
              ? 96 // top-24
              : 64; // top-16

        // Calculate bottom offset to avoid chat panel overlap
        const configPanelBottomOffset = chatPanelOpen
          ? chatPanelSize === "maximized"
            ? 600
            : 530
          : 0;

        return (
          <CELCompletionProvider value={celCompletionContext}>
            {/* Config Panel - view-only for builtin workflows. Mutation
                callbacks (update/delete/rename) come from
                <WorkflowMutationProvider> via useWorkflowMutations(). */}
            {selectedNode &&
              selectedNode.type !== "eventNode" &&
              selectedNode.type !== "switchNode" &&
              (selectedNode.data as FlowNodeData).step && (
                <ConfigPanel
                  key={selectedNode.id}
                  step={(selectedNode.data as FlowNodeData).step as Step}
                  onClose={() => setSelectedNodeId(null)}
                  onEditLoopBody={enterLoopEdit}
                  onEditInlineWorkflowBody={enterWorkflowEdit}
                  existingNodeIds={existingNodeIds}
                  bottomOffset={configPanelBottomOffset}
                  topOffset={configPanelTopOffset}
                  currentWorkflowName={workflow.name}
                  isInLoop={isEditingLoop}
                  isReadOnly={isBuiltinWorkflow}
                />
              )}

            {/* Switch Config Panel - view-only for builtin workflows */}
            {selectedNode && selectedNode.type === "switchNode" && (
              <SwitchConfigPanel
                key={selectedNode.id}
                node={selectedNode}
                nodes={nodes}
                edges={edges}
                onClose={() => setSelectedNodeId(null)}
                bottomOffset={configPanelBottomOffset}
                topOffset={configPanelTopOffset}
                isReadOnly={isBuiltinWorkflow}
              />
            )}

            {/* Edge Config Panel - view-only for builtin workflows */}
            {selectedEdge && (
              <EdgeConfigPanel
                key={selectedEdge.id}
                edge={selectedEdge}
                nodes={nodes}
                onClose={() => setSelectedEdgeId(null)}
                bottomOffset={configPanelBottomOffset}
                topOffset={configPanelTopOffset}
                isReadOnly={isBuiltinWorkflow}
              />
            )}

            {/* Workflow Settings Editor - hidden for builtin workflows */}
            {!isBuiltinWorkflow && showSettingsEditor && (
              <WorkflowSettingsEditor
                params={workflow.inputs ?? {}}
                entry={workflow.entry}
                outputs={workflow.outputs}
                tag={workflow.presets?.tag}
                nodeIds={nodes.map((n) => n.id)}
                onUpdateParams={(p) => {
                  setWorkflow((w) => ({ ...w, inputs: p }));
                  setHasModifications(true);
                }}
                onUpdateEntry={(e) => {
                  // Cast: proto's `entry` is string[] but the builder
                  // historically tolerated `string | string[] | undefined`
                  // (nodesEdgesToWorkflow normalizes downstream).
                  setWorkflow((w) => ({ ...w, entry: e as Workflow["entry"] }));
                  setHasModifications(true);
                }}
                onUpdateOutputs={(o) => {
                  setWorkflow((w) => ({ ...w, outputs: o }));
                  setHasModifications(true);
                }}
                onUpdateTag={(t) => {
                  setWorkflow((w) => ({
                    ...w,
                    presets: { ...w.presets, tag: t } as Workflow["presets"],
                  }));
                  setHasModifications(true);
                }}
                onClose={() => setShowSettingsEditor(false)}
                bottomOffset={configPanelBottomOffset}
                topOffset={configPanelTopOffset}
              />
            )}
          </CELCompletionProvider>
        );
      })()}

      {/* AI Chat Assistant - disabled for builtin workflows */}
      {currentProject?.id && !isBuiltinWorkflow && (
        <WorkflowBuilderChat
          workflow={
            isEditingLoop
              ? loopEditStack[loopEditStack.length - 1].parentWorkflow
              : currentWorkflow
          }
          onWorkflowChange={handleChatWorkflowUpdate}
          projectId={currentProject.id}
          isNewWorkflow={isNewWorkflow}
          isOpen={chatPanelOpen}
          onOpenChange={(open) => {
            setChatPanelOpen(open);
            if (open) {
              // Close config panels when chat opens
              setSelectedNodeId(null);
              setSelectedEdgeId(null);
              setShowSettingsEditor(false);
            }
          }}
          panelSize={chatPanelSize}
          onPanelSizeChange={setChatPanelSize}
          builderChatId={builderChatId}
          draftId={draftId}
          workflowSessionId={workflowSessionId}
          isConfigPanelOpen={
            !!(selectedNodeId || selectedEdgeId || showSettingsEditor)
          }
          onChatIdChange={onChatIdChange}
          onDraftIdChange={onDraftIdChange}
          onVersionChange={onVersionChange}
        />
      )}

      {/* Create a Copy Modal */}
      {showTemplateModal && (
        <Modal
          isOpen={true}
          onClose={() => setShowTemplateModal(false)}
          title="Create a Copy"
          size="md"
        >
          <div className="space-y-4">
            <p className="text-sm text-muted-foreground">
              Create your own workflow based on this built-in template.
            </p>

            <div>
              <label className="block text-sm font-medium text-foreground mb-1.5">
                Workflow Name
              </label>
              <input
                type="text"
                value={templateName}
                onChange={(e) => setTemplateName(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    handleTemplateConfirm();
                  }
                }}
                placeholder="my-workflow"
                autoFocus
                className="w-full px-3 py-2 bg-background border border-border rounded-lg text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring/20 focus:border-ring"
              />
              <p className="text-xs text-muted-foreground mt-1">
                Use lowercase letters, numbers, and underscores for node IDs
              </p>
            </div>

            <div className="flex justify-end gap-2 pt-4 border-t border-border">
              <Button
                variant="outline"
                onClick={() => setShowTemplateModal(false)}
              >
                Cancel
              </Button>
              <Button variant="primary" onClick={handleTemplateConfirm}>
                Create Workflow
              </Button>
            </div>
          </div>
        </Modal>
      )}

      {/* Active Chat Modal */}
      {showActiveChatModal && (
        <Modal
          isOpen={true}
          onClose={() => setShowActiveChatModal(false)}
          title="Chat in Progress"
          size="sm"
        >
          <div className="space-y-4">
            <p className="text-sm text-muted-foreground">
              The AI assistant is still working. What would you like to do?
            </p>
            <div className="flex flex-col gap-2 pt-4 border-t border-border">
              <Button variant="primary" onClick={handleRunInBackground}>
                Run in Background
              </Button>
              <Button variant="outline" onClick={handleCancelChatAndExit}>
                Cancel Chat & Exit
              </Button>
              <Button
                variant="ghost"
                onClick={() => setShowActiveChatModal(false)}
              >
                Go Back
              </Button>
            </div>
          </div>
        </Modal>
      )}

      {/* Exit Confirmation Modal */}
      {showExitConfirmModal && (
        <Modal
          isOpen={true}
          onClose={() => setShowExitConfirmModal(false)}
          title="Unsaved Changes"
          size="sm"
        >
          <div className="space-y-4">
            <p className="text-sm text-muted-foreground">
              {(workflow.name ?? "").trim()
                ? "You have unsaved changes. What would you like to do?"
                : "You have unsaved changes. Name your workflow to save it, or discard your changes."}
            </p>

            <div className="flex flex-col gap-2 pt-4 border-t border-border">
              {(workflow.name ?? "").trim() && (
                <Button
                  variant="primary"
                  onClick={handleSaveAndExit}
                  disabled={isSavingBeforeExit}
                >
                  {isSavingBeforeExit ? "Saving..." : "Save & Exit"}
                </Button>
              )}
              <Button
                variant={(workflow.name ?? "").trim() ? "outline" : "destructive"}
                onClick={handleDiscardAndExit}
                disabled={isSavingBeforeExit}
              >
                Discard Changes
              </Button>
              <Button
                variant="ghost"
                onClick={() => setShowExitConfirmModal(false)}
                disabled={isSavingBeforeExit}
              >
                {(workflow.name ?? "").trim() ? "Cancel" : "Keep Editing"}
              </Button>
            </div>
          </div>
        </Modal>
      )}

      {/* Workflow Info Popover */}
      <WorkflowInfoPopover
        isOpen={showInfoPopover}
        onClose={() => setShowInfoPopover(false)}
        description={workflow.description ?? ""}
        onDescriptionChange={
          source === "user"
            ? (desc: string) => {
                setWorkflow((w) => ({ ...w, description: desc }));
                setHasModifications(true);
              }
            : undefined
        }
        createdAt={createdAt}
        isEditable={source === "user"}
      />

      {/* YAML Editor Modal */}
      <YamlEditorModal
        isOpen={showYamlEditor}
        onClose={() => setShowYamlEditor(false)}
        workflow={currentWorkflow}
        onApply={(w) => {
          handleChatWorkflowUpdate(w);
          // Clear cached YAML since the user edited it manually;
          // the next save will provide a fresh backend-canonical version.
          onYamlDefinitionChange?.(undefined);
        }}
        isReadOnly={isBuiltinWorkflow}
        yamlDefinition={yamlDefinition}
        projectId={currentProject?.id ?? ""}
      />

      {/* Scenario Panel Modal */}
      {currentProject?.id && (
        <Modal
          isOpen={showScenarioPanel}
          onClose={() => setShowScenarioPanel(false)}
          title="Test Scenarios"
          size="lg"
        >
          <div className="h-[500px]">
            <ScenarioPanel
              projectId={currentProject.id}
              workflowSlug={workflow.name ?? ""}
              isReadOnly={isBuiltinWorkflow}
            />
          </div>
        </Modal>
      )}
    </div>
    </WorkflowNodeCallbacksProvider>
    </WorkflowMutationProvider>
  );
}

export function WorkflowBuilder(props: WorkflowBuilderProps) {
  return (
    <ReactFlowProvider>
      <WorkflowBuilderInner {...props} />
    </ReactFlowProvider>
  );
}