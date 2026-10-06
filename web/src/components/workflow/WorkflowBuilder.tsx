import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
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
  isEntryFlowNodeType,
  ENTRY_NODE_ID,
  type FlowNodeData,
} from "../../lib/workflow-flow";
import { AutomationFormDialog } from "../Automations/AutomationFormDialog";
import type { Trigger } from "../../api/trigger-grpc";
import { workflowRefForTrigger } from "../../lib/triggerRail";
import { TriggerRailProvider, type TriggerRailContextValue } from "./TriggerRailContext";
import { TriggerPayloadPanel } from "./config/TriggerPayloadPanel";
import { nodesEdgesToWorkflow } from "../../lib/nodes-edges-to-workflow";
import { useFitViewWithPanels } from "./hooks/useFitViewWithPanels";
import { useWorkflowKeyboardShortcuts } from "./hooks/useWorkflowKeyboardShortcuts";
import { useStepPaletteShortcutLabel, useWorkflowBuilderShortcuts } from "./hooks/useWorkflowBuilderShortcuts";
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
  Play,
  Settings2,
  Lock,
  ExternalLink,
  CheckCircle2,
  PencilRuler,
} from "lucide-react";
import { Tooltip } from "../ui/Tooltip";
import { DraftStatusBadge } from "./DraftStatusBadge";
import {
  markCompleteBlockers,
  splitFindings,
  type DraftStatus,
} from "./workflowDraftStatus";
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
import { WorkflowEditorChatPanel, type PanelSize } from "./WorkflowEditorChatPanel";
import { useWorkflowDraftSync, type RemoteWorkflowState } from "./hooks/useWorkflowDraftSync";
import { ScenarioPanel } from "./ScenarioPanel";
import { BuilderTestRunPanel } from "./run/BuilderTestRunPanel";
import { useBuilderTestRun, withTestRunStatus } from "./hooks/useBuilderTestRun";
import { useProjectStore } from "../../store/projectStore";
import { normalizeWorkflowRef } from "./useWorkflowInputs";
import { celString, directCel } from "../../lib/celAdapter";
import { getInputDescription, type InputDef } from "../../lib/inputHelpers";
import {
  CELCompletionProvider,
  type CELCompletionContextValue,
} from "./CELCompletionContext";
import { WorkflowMutationProvider } from "./WorkflowMutationContext";
import { WorkflowNodeCallbacksProvider } from "./WorkflowNodeCallbacksContext";
import { StepPalette, type PaletteFocus } from "./palette/StepPalette";
import { toolCallsDefaultForEdge } from "./executeToolsDefaults";
import { CanvasInsertProvider, useCanvasInsertion } from "./canvas/CanvasInsertContext";
import { NodeOutputAddButtons } from "./canvas/NodeOutputAddButtons";
import { SelectionActions } from "./canvas/SelectionActions";
import { buildFlowEdge, readableStepId, withNodeAriaLabels } from "./canvas/insertPlacement";
import { getNodeDisplayName } from "../../lib/node-metadata";
import { DeclaredTriggerPanel } from "./config/DeclaredTriggerPanel";
import { ActivateTriggerDialog } from "../Automations/ActivateTriggerDialog";
import { useTriggers } from "../../hooks/trigger-queries";
import { declaredRailLines } from "../../lib/triggerRail";
import { findingsForTrigger, type DeclaredTrigger } from "../../lib/declaredTriggers";
import { useAddTrigger } from "./hooks/useAddTrigger";
import { ConnectIntegrationDialog, type ConnectIntegrationTarget } from "./connections/ConnectIntegrationDialog";
import {
  catalogSearchGrpc,
  type CatalogEntry,
  type CatalogEntrySummary,
} from "../../api/catalog-search-grpc";
import { connectionKeys, useActionOutputSchemas, useCatalogEntry, useDeclaredTriggerRef } from "../../hooks/connection-queries";
import {
  actionNodeIdBase,
  getActionParams,
  getActionUses,
  isIntegrationActionStep,
  newActionStep,
  uniqueNodeId,
  withActionParam,
} from "../../lib/actionNodeArgs";
import { actionParamDefaults } from "../../lib/jsonSchemaFields";

/** Result of a save operation */
export interface SaveResult {
  success: boolean;
  /** Findings for the saved definition (errors, then "warning:*"). */
  validationErrors: ValidationError[];
  /** Status after the save (absent in tour mode). */
  status?: DraftStatus;
  /** True when validation blocked a save of a complete workflow (nothing stored). */
  rejected?: boolean;
  /** Runtime slug of the stored workflow (absent in tour mode). */
  slug?: string;
}

/** Result of marking a workflow complete / moving it to draft. */
export interface StatusChangeResult {
  success: boolean;
  validationErrors: ValidationError[];
}

interface WorkflowBuilderProps {
  /**
   * Saves the canvas for a test run and resolves to the stored workflow's slug,
   * or null when nothing was saved. A save that fails must not start a run.
   */
  onSaveForTestRun?: (workflow: Workflow) => Promise<string | null>;
  /** Saves the canvas. `intent` overrides the stored status (e.g. "draft"
   * to take a complete workflow back to work in progress). */
  onSave?: (workflow: Workflow, intent?: DraftStatus) => void | Promise<void | SaveResult>;
  /** Lifecycle of the stored workflow; builtin/project are "complete". */
  draftStatus?: DraftStatus;
  /** Marks the stored workflow complete (validated server-side) or moves it to draft. */
  onSetStatus?: (status: DraftStatus) => Promise<StatusChangeResult>;
  /** Filled in by the builder with a "save the canvas as a draft" action. */
  saveAsDraftRef?: React.MutableRefObject<(() => Promise<void>) | null>;
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
  version: number;
  /** When the workflow was created */
  createdAt?: string;
  /** Chat shown in the editor's chat panel (route search param). Not bound to the workflow server-side. */
  chatId?: string;
  /** Called when the panel starts a chat or the user clears it. */
  onChatIdChange?: (chatId: string | undefined) => void;
  /** Called when a pushed update changes the draft's lifecycle status. */
  onDraftStatusChange?: (status: DraftStatus) => void;
  /** Draft ID for this workflow (used for LLM tool calls) */
  draftId?: string;
  /** Opens the workflow Library (offered when the open draft is deleted elsewhere). */
  onWorkflowDeleted?: () => void;
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
  onSaveForTestRun,
  draftStatus = "complete",
  onSetStatus,
  saveAsDraftRef,
  initialWorkflow,
  initialName,
  onBack,
  isBuiltin = false,
  isNewWorkflow = false,
  source = "user",
  version,
  createdAt,
  chatId,
  onChatIdChange,
  onDraftStatusChange,
  draftId,
  onWorkflowDeleted,
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
  // Seeded from the whole definition (minus the graph, which lives in
  // `nodes`/`edges`), so fields the canvas does not draw — declared
  // `triggers`, `title`, `daemon` — survive a save.
  const [workflow, setWorkflow] = useState<Workflow>(() => ({
    ...initialWorkflow,
    nodes: undefined,
    edges: undefined,
    name: initialWorkflow?.name || initialName || "New Workflow",
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
  // The start node's panel (Trigger payload). Not a selection: it can stay
  // open beside a step's panel.
  const [showStartPanel, setShowStartPanel] = useState(false);

  // The step palette ("Add step"): one searchable list of built-in steps
  // and integration actions. `connectTarget` is the integration a palette
  // row's Connect affordance is connecting.
  const [paletteOpen, setPaletteOpen] = useState(false);
  // "+ Add trigger" opens the same palette on its Triggers kind.
  const [paletteKind, setPaletteKind] = useState<"action" | "trigger">("action");
  // Where the palette opens: set by the sidebar's integration shortcuts.
  const [paletteFocus, setPaletteFocus] = useState<PaletteFocus | undefined>(undefined);
  // The declared trigger whose editor is open (by index), and the one being
  // activated. A catalog ref remembered per trigger name gives the editor the
  // payload schema and event list of the type it was added from.
  const [selectedDeclared, setSelectedDeclared] = useState<number | null>(null);
  const [activatingDeclared, setActivatingDeclared] = useState<number | null>(null);
  const [declaredCatalogRefs, setDeclaredCatalogRefs] = useState<Record<string, string>>({});
  const [connectTarget, setConnectTarget] = useState<ConnectIntegrationTarget | null>(null);
  const queryClient = useQueryClient();

  // The automation dialog opened from the trigger rail: `null` is closed,
  // `{}` is create (prefilled with this workflow), `{ trigger }` is edit.
  const [automationDialog, setAutomationDialog] = useState<{ trigger?: Trigger } | null>(null);

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

  // Test run: a docked panel (not a modal) so the canvas, which shows each
  // node's status as the run goes, stays visible.
  const [showTestRunPanel, setShowTestRunPanel] = useState(false);
  const [testRunChatId, setTestRunChatId] = useState<string | null>(null);

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
        definition: workflow,
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

  // Back button handler - warn about unsaved changes
  const handleBackClick = useCallback(() => {
    // Skip for builtin workflows since they can't be saved anyway
    if (!isBuiltinWorkflow && hasModifications) {
      setShowExitConfirmModal(true);
    } else {
      onBack?.();
    }
  }, [hasModifications, onBack, isBuiltinWorkflow]);

  // Discard and exit handler
  const handleDiscardAndExit = useCallback(() => {
    setShowExitConfirmModal(false);
    onBack?.();
  }, [onBack]);

  const deselectNodeAndStartPanel = useCallback((nodeId: string | null) => {
    setSelectedNodeId(nodeId);
    if (nodeId === null) {
      setShowStartPanel(false);
      setSelectedDeclared(null);
    }
  }, []);

  // Keyboard shortcuts (Ctrl/Cmd+Z undo, +Shift+Z / +Y redo, and Escape
  // deselect/exit-inline/back) — see ./hooks/useWorkflowKeyboardShortcuts.
  useWorkflowKeyboardShortcuts({
    onUndo: handleUndo,
    onRedo: handleRedo,
    onEscape: handleBackClick,
    isEditingLoop,
    exitLoopEdit,
    isBuiltinWorkflow,
    // The start panel counts as a selection, so Escape closes it before it
    // falls back to leaving the builder.
    hasSelectedNode: !!selectedNodeId || showStartPanel || selectedDeclared !== null,
    hasSelectedEdge: !!selectedEdgeId,
    showSettingsEditor,
    setSelectedNodeId: deselectNodeAndStartPanel,
    setSelectedEdgeId,
    setShowSettingsEditor,
    showTemplateModal,
    showExitConfirmModal,
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
    setSelectedDeclared(null);
    setSelectedEdgeId(edge.id);
    setSelectedNodeId(null);
    setChatPanelOpen(false); // Close chat when config panel opens
  }, []);

  // Handle clicking on canvas (deselect)
  const onPaneClick = useCallback(() => {
    setSelectedDeclared(null);
    setSelectedNodeId(null);
    setSelectedEdgeId(null);
    setShowSettingsEditor(false);
    setShowStartPanel(false);
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

      // The start node's event rides on the edge as sourceEvent, which is how
      // buildWorkflow converts it back (shared with canvas inserts).
      const newEdge = buildFlowEdge(sourceNode, targetId, sourceHandle);

      setEdges((eds) => [...eds, newEdge]);

      // Wiring Run LLM Tool Calls after a Call LLM fills in which calls it
      // runs — the one value that step nearly always takes.
      const prefilled = toolCallsDefaultForEdge(sourceId, targetId, nodes, edges);
      if (prefilled) {
        setNodes((nds) =>
          nds.map((node) => (node.id === targetId ? { ...node, data: { ...node.data, step: prefilled } } : node)),
        );
      }

      return newEdge;
    },
    [nodes, edges, setEdges, setNodes, takeSnapshot],
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

  // Handle node selection. The start node opens its own panel (Trigger
  // payload), which stays open beside a step panel while the user picks a
  // step to write an expression in.
  const onNodeClick = useCallback((_event: React.MouseEvent, node: Node) => {
    setShowSettingsEditor(false);
    setSelectedDeclared(null);
    setSelectedEdgeId(null);
    setChatPanelOpen(false); // Close chat when config panel opens
    if (isEntryFlowNodeType(node.type)) {
      setShowStartPanel(true);
      setSelectedNodeId(null);
      return;
    }
    setSelectedNodeId(node.id);
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

  const handleSave = useCallback(async (intent?: DraftStatus) => {
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

    setIsSaving(true);
    try {
      const result = await onSave?.(builtWorkflow, intent);

      // Show the findings for what was just saved (or refused) inline. The
      // badge counts errors only; warnings ride along in the popover.
      if (result && typeof result === "object") {
        const findings = result.validationErrors || [];
        setValidationErrors(findings);
        setValidationStatus(
          splitFindings(findings).errors.length === 0 ? "valid" : "invalid",
        );
        if (result.rejected) {
          // Nothing was stored: keep the edits dirty so they aren't lost.
          return;
        }
      }

      setLoadedWorkflowName(builtWorkflow.name);
      setHasModifications(false);
      setSavedTriggersJson(JSON.stringify(builtWorkflow.triggers ?? []));

      const savedAsDraft =
        result && typeof result === "object" && result.status === "draft";
      toast.success(savedAsDraft ? "Saved as draft" : "Workflow saved", {
        duration: 2000,
      });
    } catch (error) {
      console.error("Save failed:", error);
    } finally {
      setIsSaving(false);
    }
  }, [buildWorkflow, onSave, workflow.name, isBuiltinWorkflow, nodes, edges]);

  // Save-then-run for the Test run panel. Nothing runs unless the draft was
  // stored: an unnamed canvas, a rejected save and a failed save all end here.
  const saveForTestRun = useCallback(async (): Promise<string | null> => {
    if (!onSaveForTestRun) return null;
    if (!workflow.name || workflow.name.trim() === "") {
      toast.error("Please give your workflow a name before running it", { duration: 3000 });
      return null;
    }
    setIsSaving(true);
    try {
      const slug = await onSaveForTestRun(buildWorkflow());
      if (!slug) return null;
      setLoadedWorkflowName(workflow.name);
      setHasModifications(false);
      return slug;
    } catch (error) {
      // The page has already told the user why the save failed.
      console.error("Save before test run failed:", error);
      return null;
    } finally {
      setIsSaving(false);
    }
  }, [onSaveForTestRun, buildWorkflow, workflow.name]);

  // A running test paints its node statuses onto the canvas.
  const nodeIds = useMemo(() => nodes.map((n) => n.id), [nodes]);
  const testRunStatuses = useBuilderTestRun(testRunChatId, nodeIds);
  const displayedNodes = useMemo(
    () => withNodeAriaLabels(withTestRunStatus(nodes, testRunStatuses), edges, getNodeDisplayName),
    [nodes, edges, testRunStatuses],
  );

  // Offered by a rejected save of a complete workflow: store the canvas as a
  // draft instead (it stops being runnable until marked complete again).
  useEffect(() => {
    if (!saveAsDraftRef) return;
    saveAsDraftRef.current = () => handleSave("draft");
    return () => {
      saveAsDraftRef.current = null;
    };
  }, [saveAsDraftRef, handleSave]);

  const [isChangingStatus, setIsChangingStatus] = useState(false);
  const markCompleteReasons = useMemo(
    () =>
      markCompleteBlockers({
        errors: validationStatus === "invalid" ? validationErrors : [],
        hasUnsavedChanges: hasModifications,
        isSaving: isChangingStatus,
      }),
    [validationStatus, validationErrors, hasModifications, isChangingStatus],
  );

  const handleSetStatus = useCallback(
    async (status: DraftStatus) => {
      if (!onSetStatus) return;
      setIsChangingStatus(true);
      try {
        const result = await onSetStatus(status);
        const findings = result.validationErrors || [];
        setValidationErrors(findings);
        setValidationStatus(
          splitFindings(findings).errors.length === 0 ? "valid" : "invalid",
        );
      } finally {
        setIsChangingStatus(false);
      }
    },
    [onSetStatus],
  );

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
      if (isEntryFlowNodeType(node.type) || node.type === "switchNode") return "";
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
      const actionRef = step && isIntegrationActionStep(step) ? getActionUses(step) : "";
      return `${node.id}:${step?.type ?? ""}:${routerOutputKeys}:${actionRef}`;
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

  // The output schema of every integration action on the canvas, so
  // `nodes.<id>.data.*` autocompletes downstream (one cached GetCatalogEntry
  // per distinct ref, shared with the config panel and the palette).
  const actionRefsByNode = useMemo(() => {
    const out: Array<[string, string]> = [];
    for (const node of nodes) {
      const step = (node.data as FlowNodeData).step as Step | undefined;
      if (step && isIntegrationActionStep(step)) {
        const uses = getActionUses(step);
        if (uses && !uses.includes("{{")) out.push([node.id, uses]);
      }
    }
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps -- keyed on the structural fingerprint
  }, [celContextKey]);
  const actionOutputSchemas = useActionOutputSchemas(actionRefsByNode);
  // `trigger.payload.*` for node expressions, from the first declared
  // integration trigger (a run is started by one trigger; the first is the
  // usual one, and the trigger editor narrows to its own).
  const firstIntegrationTrigger = (workflow.triggers ?? []).find((t) => t.source?.case === "integration");
  const firstIntegration = firstIntegrationTrigger?.source?.case === "integration" ? firstIntegrationTrigger.source.value : undefined;
  const firstTriggerRef = useDeclaredTriggerRef(
    firstIntegration?.integration,
    (firstIntegration?.events ?? []) as string[],
    firstIntegrationTrigger ? declaredCatalogRefs[firstIntegrationTrigger.name ?? ""] : undefined,
  );
  const triggerPayloadSchema = useCatalogEntry(firstTriggerRef).data?.payloadSchema;

  // Build CEL completion context for Monaco editors in config panels.
  // Keyed on the structural fingerprint above so it's stable across edits
  // that don't change shape.
  const celCompletionContext = useMemo<CELCompletionContextValue>(() => {
    const nodeIds: string[] = [];
    const nodeTypeMap: Record<string, string> = {};
    const nodeDeclaredOutputs: Record<string, string[]> = {};
    for (const node of nodes) {
      if (isEntryFlowNodeType(node.type) || node.type === "switchNode") continue;
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
    return { nodeIds, nodeTypeMap, inputParams, edges: edgeList, nodeDeclaredOutputs, nodeOutputSchemas: actionOutputSchemas, triggerPayloadSchema };
    // Intentionally keyed on the structural fingerprint to avoid re-init on label/CEL keystrokes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [celContextKey, actionOutputSchemas, triggerPayloadSchema]);

  // The trigger rail's data (research/WORKFLOW_UI.md §3.2). Keyed on the
  // STORED name: an automation names a saved workflow, so a rename in progress
  // must not re-filter the rail. A new, never-saved workflow cannot have one.
  const savedWorkflowName = initialWorkflow?.name ?? "";
  // The declared triggers as last stored, to tell which edits are unsaved: an
  // activation resolves the STORED declaration, so activating an unsaved one
  // would activate something other than what is on screen.
  const [savedTriggersJson, setSavedTriggersJson] = useState(() => JSON.stringify(initialWorkflow?.triggers ?? []));
  useEffect(() => {
    setSavedTriggersJson(JSON.stringify(initialWorkflow?.triggers ?? []));
  }, [initialWorkflow]);
  const declared = useMemo(() => (isEditingLoop ? [] : [...(workflow.triggers ?? [])]) as DeclaredTrigger[], [workflow.triggers, isEditingLoop]);
  const unsavedDeclared = useMemo(() => {
    const saved = JSON.parse(savedTriggersJson) as DeclaredTrigger[];
    const byName = new Map(saved.map((t) => [t.name, JSON.stringify(t)]));
    return new Set(declared.filter((t) => byName.get(t.name) !== JSON.stringify(t)).map((t) => t.name ?? ""));
  }, [declared, savedTriggersJson]);
  const handleEditTrigger = useCallback((trigger: Trigger) => {
    setAutomationDialog({ trigger });
  }, []);
  const canEditDefinition = !isBuiltinWorkflow && !isEditingLoop;
  // One "Add trigger" everywhere: the palette's Triggers kind. What a pick
  // becomes depends on the workflow (useAddTrigger): a declaration on one
  // you can edit, a personal trigger on a built-in.
  const handleAddTrigger = useCallback(() => {
    setPaletteKind("trigger");
    setPaletteFocus(undefined);
    setPaletteOpen(true);
  }, []);
  // The Chat trigger: on unless the definition is automation-only.
  const handleSetChatEnabled = useCallback((enabled: boolean) => {
    setHasModifications(true);
    setWorkflow((w) => ({ ...w, automationOnly: !enabled }));
  }, []);
  const handleEditDeclared = useCallback((index: number) => {
    setSelectedDeclared(index);
    setSelectedNodeId(null);
    setSelectedEdgeId(null);
    setShowSettingsEditor(false);
    setChatPanelOpen(false);
  }, []);
  const findingsFor = useCallback(
    (index: number, name: string) => findingsForTrigger(validationErrors, index, name),
    [validationErrors],
  );
  const triggerRail = useMemo<TriggerRailContextValue>(
    () => ({
      workflowRef: savedWorkflowName,
      projectId: currentProject?.id ?? "",
      canAddTrigger: !isNewWorkflow && savedWorkflowName !== "" && !!currentProject?.id,
      canEditDefinition,
      declared,
      findingsFor,
      unsavedDeclared,
      selectedDeclared,
      chatEnabled: !workflow.automationOnly,
      onSetChatEnabled: handleSetChatEnabled,
      onEditTrigger: handleEditTrigger,
      onAddTrigger: handleAddTrigger,
      onEditDeclared: handleEditDeclared,
      onActivateDeclared: setActivatingDeclared,
    }),
    [savedWorkflowName, currentProject?.id, isNewWorkflow, canEditDefinition, declared, findingsFor, unsavedDeclared, selectedDeclared, workflow.automationOnly, handleSetChatEnabled, handleEditTrigger, handleAddTrigger, handleEditDeclared],
  );
  // The caller's activations, for the editor's Activations section (the
  // rail reads the same cached list).
  const allTriggersQuery = useTriggers(undefined, { enabled: !!savedWorkflowName });
  const declaredActivations = useMemo(
    () => declaredRailLines(declared, allTriggersQuery.data ?? [], savedWorkflowName).lines,
    [declared, allTriggersQuery.data, savedWorkflowName],
  );

  // Apply a workflow that changed outside this canvas (an agent edited the
  // draft): replace the workflow state in one call, replace the ReactFlow
  // arrays, clear dirty (the writer already persisted it). Selection objects
  // are derived during render so no manual sync is needed.
  const applyRemoteWorkflowUpdate = useCallback(
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
        toast.success("Workflow updated", { duration: 2000 });
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
        { draggable: canDragNodes, entryNode: "triggerRail" },
      );
      setWorkflow(updatedWorkflow);
      setNodes(allNodes as Node[]);
      setEdges(flowEdges as Edge[]);
      // The writer already persisted this — it is a clean load.
      setHasModifications(false);

      if (nodesChanged) {
        toast.success("Workflow updated", { duration: 2000 });
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

  const [isSaving, setIsSaving] = useState(false);
  useWorkflowDraftSync({
    projectId: currentProject?.id,
    draftId,
    version,
    hasModifications,
    isSaving: isSaving || isChangingStatus,
    onDeleted: useCallback(() => {
      toast("This workflow was deleted", {
        id: "workflow-deleted-elsewhere",
        duration: Infinity,
        description: "Your unsaved changes are still on the canvas.",
        action: { label: "Back to Library", onClick: () => onWorkflowDeleted?.() },
      });
    }, [onWorkflowDeleted]),
    onRemoteUpdate: useCallback(
      (remote: RemoteWorkflowState) => {
        applyRemoteWorkflowUpdate(remote.workflow);
        onVersionChange?.(remote.version);
        onYamlDefinitionChange?.(remote.yamlDefinition);
        onDraftStatusChange?.(remote.status);
      },
      [applyRemoteWorkflowUpdate, onVersionChange, onYamlDefinitionChange, onDraftStatusChange],
    ),
    onModifiedElsewhere: useCallback((reload: () => Promise<void>) => {
      toast("This workflow was updated elsewhere.", {
        id: "workflow-updated-elsewhere",
        duration: 15000,
        description: "Reloading discards your unsaved changes.",
        action: {
          label: "Reload",
          onClick: () => {
            reload().catch(() => toast.error("Failed to reload workflow"));
          },
        },
      });
    }, []),
  });

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

  // Every added step lands connected: at the "+" that asked for it, after the
  // selected node, or after the end of the main path (./canvas).
  const canvasInsertion = useCanvasInsertion({
    enabled: !isBuiltinWorkflow,
    nodes,
    edges,
    setNodes,
    setEdges,
    takeSnapshot,
    markDirty: () => setHasModifications(true),
    selectedNodeId,
    paletteOpen,
    openPalette: () => {
      setPaletteKind("action");
      setPaletteFocus(undefined);
      setPaletteOpen(true);
    },
    createEdge,
    // Only a canvas with nothing to attach to places a node freely.
    fallbackPosition: () => {
      const wrapper = reactFlowWrapper.current;
      return screenToFlowPosition({ x: (wrapper?.clientWidth ?? 1200) / 2, y: (wrapper?.clientHeight ?? 800) / 2 });
    },
  });

  /** Add a prepared node, connected; select it and open its panel. */
  const placeNode = useCallback(
    (node: Node) => {
      const inserted = canvasInsertion.insertNode(node);
      // Auto-select the newly created node to open config panel
      setSelectedNodeId(inserted.id);
      setSelectedEdgeId(null);
      setShowSettingsEditor(false);
      // Close chat panel when config panel opens
      setChatPanelOpen(false);
    },
    [canvasInsertion],
  );

  /** Add a prepared step, connected; select it and open its panel. */
  const insertStep = useCallback(
    (step: Step) => {
      const stepType = step.type ?? "";
      const flowNodeType = STRUCTURAL_TYPES.has(stepType) ? `${stepType}Node` : "actionNode";
      placeNode({ id: step.id!, type: flowNodeType, position: { x: 0, y: 0 }, data: { step, label: step.id! } });
    },
    [placeNode, STRUCTURAL_TYPES],
  );

  const addStep = useCallback(
    (stepType: string) => {
      // A readable, unique, CEL-safe id: call_llm, then call_llm_2.
      const id = readableStepId(stepType, nodes.map((n) => n.id));

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
      } else if (stepType === "join") {
        step.condition = directCel("all");
      } else if (stepType === "loop") {
        step = withLoopArgs(step, { while: directCel(""), ref: celString("") });
      }

      insertStep(step);
    },
    [insertStep, nodes],
  );

  /**
   * Add an integration action from the palette: `type: action`,
   * `uses: <ref>`, with its parameters' declared defaults. The entry is
   * usually already cached (the palette prefetches the highlighted row); if
   * not, the node goes in immediately and the defaults follow.
   */
  const addActionStep = useCallback(
    (entry: CatalogEntrySummary) => {
      const id = uniqueNodeId(actionNodeIdBase(entry.ref), nodes.map((n) => n.id));
      const cached = queryClient.getQueryData<CatalogEntry>(connectionKeys.catalogEntry(entry.ref));
      insertStep(newActionStep(id, entry.ref, actionParamDefaults(cached?.paramsSchema)));
      if (cached) return;
      void queryClient
        .fetchQuery({
          queryKey: connectionKeys.catalogEntry(entry.ref),
          queryFn: () => catalogSearchGrpc.get(entry.ref),
          staleTime: 5 * 60_000,
        })
        .then((full) => {
          const defaults = actionParamDefaults(full.paramsSchema);
          if (Object.keys(defaults).length === 0) return;
          setNodes((nds) =>
            nds.map((node) => {
              if (node.id !== id) return node;
              const current = (node.data as FlowNodeData).step as Step;
              let next = current;
              const existing = getActionParams(current);
              for (const [key, value] of Object.entries(defaults)) {
                if (existing[key] === undefined) next = withActionParam(next, key, value);
              }
              return { ...node, data: { ...node.data, step: next } };
            }),
          );
        })
        .catch(() => undefined); // the config panel reports a missing entry
    },
    [insertStep, nodes, queryClient, setNodes],
  );

  const addSwitch = useCallback(() => {
    // A Switch is canvas-only (it compiles into edge conditions), so its id
    // never reaches YAML; the case ids are handle ids. Both only need to be
    // unique.
    const id = readableStepId("switch", nodes.map((n) => n.id));
    placeNode({
      id,
      type: "switchNode",
      position: { x: 0, y: 0 },
      data: {
        label: "Switch",
        cases: [
          { id: `case_${Date.now()}_1`, condition: "", label: "" }, // First case (will need condition)
          { id: `case_${Date.now()}_2`, condition: "", label: "" }, // Default case (last, no condition)
        ],
      },
      draggable: canDragNodes,
    });
  }, [placeNode, nodes, canDragNodes]);

  const openStepPalette = useCallback(() => {
    if (isBuiltinWorkflow) return;
    setPaletteKind("action");
    setPaletteFocus(undefined);
    setPaletteOpen(true);
  }, [isBuiltinWorkflow]);

  /** The sidebar's integration shortcuts: one integration expanded, or the list. */
  const openStepPaletteOnIntegration = useCallback(
    (integrationId?: string) => {
      if (isBuiltinWorkflow) return;
      setPaletteKind("action");
      setPaletteFocus(integrationId ? { integration: integrationId } : "integrations");
      setPaletteOpen(true);
    },
    [isBuiltinWorkflow],
  );

  /** Declare a trigger and open its editor. */
  const declareTrigger = useCallback(
    (trigger: DeclaredTrigger, catalogRef?: string) => {
      const index = (workflow.triggers ?? []).length;
      setHasModifications(true);
      setWorkflow((w) => ({ ...w, triggers: [...(w.triggers ?? []), trigger] }));
      if (catalogRef) setDeclaredCatalogRefs((prev) => ({ ...prev, [trigger.name ?? ""]: catalogRef }));
      setPaletteOpen(false);
      handleEditDeclared(index);
    },
    [workflow.triggers, handleEditDeclared],
  );

  // One "Add trigger" picker: a declaration on a workflow you can edit, a
  // personal trigger on a built-in.
  const addTrigger = useAddTrigger({
    canEditDefinition,
    declared,
    declare: declareTrigger,
    closePalette: useCallback(() => setPaletteOpen(false), []),
  });

  useWorkflowBuilderShortcuts({ onOpenStepPalette: openStepPalette });
  const paletteShortcutLabel = useStepPaletteShortcutLabel();

  const choosePaletteBuiltin = useCallback(
    (type: string) => {
      setPaletteOpen(false);
      if (type === "switch") addSwitch();
      else addStep(type);
    },
    [addStep, addSwitch],
  );

  const choosePaletteAction = useCallback(
    (entry: CatalogEntrySummary) => {
      setPaletteOpen(false);
      addActionStep(entry);
    },
    [addActionStep],
  );

  const connectFromPalette = useCallback((entry: CatalogEntrySummary) => {
    setConnectTarget({
      ref: entry.ref,
      integrationId: entry.integration.id,
      displayName: entry.integration.displayName,
      icon: entry.integration.icon,
    });
  }, []);

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
      setWorkflow={setWorkflow}
    >
    <WorkflowNodeCallbacksProvider
      onExpandLoop={(_loopNodeId, step) => enterLoopEdit(step)}
      onExpandWorkflow={(_workflowNodeId, step) => enterWorkflowEdit(step)}
    >
    <TriggerRailProvider value={triggerRail}>
    <div className="relative h-full bg-background" data-context="workflow-canvas">
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
            onOpenPalette={openStepPalette}
            onOpenIntegration={openStepPaletteOnIntegration}
            paletteShortcutLabel={paletteShortcutLabel}
          />
        )}

        {/* Stats Counter */}
        <div className="rounded-2xl border border-border/80 bg-card/95 p-3 shadow-xl shadow-black/10 backdrop-blur-sm">
          <div className="grid grid-cols-2 gap-2 text-center">
            <div className="rounded-lg bg-muted/50 px-3 py-2">
              <div className="text-2xs font-semibold uppercase tracking-[0.08em] text-muted-foreground">Steps</div>
              <div className="text-lg font-semibold leading-none text-foreground">{nodes.length}</div>
            </div>
            <div className="rounded-lg bg-muted/50 px-3 py-2">
              <div className="text-2xs font-semibold uppercase tracking-[0.08em] text-muted-foreground">Edges</div>
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
              {source === "project" ? (
                // A project workflow is a file in the repo, versioned and shared
                // with the team. It is edited where it lives, not copied.
                <>
                  <span className="text-primary text-sm font-semibold">
                    In your repo
                  </span>
                  <span className="text-primary/60 text-sm">—</span>
                  <span className="text-primary/90 text-sm">
                    Defined in this project's{" "}
                    <code className="font-mono text-xs text-primary">.reliant/workflows/</code>.
                    Edit the YAML file there to change it for everyone on the repo.
                  </span>
                </>
              ) : (
                <>
                  <span className="text-primary text-sm font-semibold">
                    View Only
                  </span>
                  <span className="text-primary/60 text-sm">—</span>
                  <span className="text-primary/90 text-sm">
                    This is a built-in workflow. Click <strong className="text-primary">"Create a Copy"</strong> to create an
                    editable copy.
                  </span>
                </>
              )}
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
            {!isEditingLoop && !isBuiltinWorkflow && draftStatus === "draft" && (
              <DraftStatusBadge
                errorCount={
                  validationStatus === "invalid"
                    ? splitFindings(validationErrors).errors.length
                    : 0
                }
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
                {onSaveForTestRun && !isBuiltinWorkflow && (
                  <button
                    onClick={() => setShowTestRunPanel((open) => !open)}
                    className={headerButtonClass}
                    aria-pressed={showTestRunPanel}
                  >
                    <Play className="w-4 h-4" />
                    Run
                  </button>
                )}
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
                {onSetStatus && draftStatus === "complete" && (
                  <Tooltip content="Take this workflow out of service to make edits that may be invalid along the way. It won't run until you mark it complete again.">
                    <button
                      onClick={() => void handleSetStatus("draft")}
                      disabled={isChangingStatus}
                      className={headerButtonClass}
                      data-testid="workflow-move-to-draft"
                    >
                      <PencilRuler className="w-4 h-4" />
                      Move to draft
                    </button>
                  </Tooltip>
                )}
                <button
                  onClick={() => void handleSave()}
                  disabled={!hasModifications}
                  className={
                    draftStatus === "draft" ? secondaryHeaderButtonClass : primaryHeaderButtonClass
                  }
                >
                  {draftStatus === "draft" ? "Save draft" : "Save"}
                </button>
                {onSetStatus && draftStatus === "draft" && (
                  <Tooltip
                    content={
                      markCompleteReasons.length > 0
                        ? `Can't mark complete yet: ${markCompleteReasons.join(" ")}`
                        : "Validate and make this workflow runnable"
                    }
                  >
                    {/* The wrapper keeps the tooltip working while the button is disabled. */}
                    <span className="inline-flex">
                      <button
                        onClick={() => void handleSetStatus("complete")}
                        disabled={markCompleteReasons.length > 0}
                        className={primaryHeaderButtonClass}
                        data-testid="workflow-mark-complete"
                      >
                        <CheckCircle2 className="w-4 h-4" />
                        Mark complete
                      </button>
                    </span>
                  </Tooltip>
                )}
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
          <CanvasInsertProvider value={canvasInsertion.api}>
          <ReactFlow
            nodes={displayedNodes}
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

            {/* "+" on unconnected outputs, and the selection's Add / Connect bar. */}
            <NodeOutputAddButtons />
            <SelectionActions />

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
          </CanvasInsertProvider>
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

            {/* Start node panel: what `trigger.*` exposes, inserted into the
                focused expression. Docks beside a step/edge panel so both
                are visible. Not offered inside a loop body, whose start is
                the loop, not a trigger. */}
            {showStartPanel && !isEditingLoop && (
              <TriggerPayloadPanel
                onClose={() => setShowStartPanel(false)}
                bottomOffset={configPanelBottomOffset}
                topOffset={configPanelTopOffset}
                docked={
                  !!(selectedNode && selectedNode.id !== ENTRY_NODE_ID) ||
                  !!selectedEdge ||
                  (!isBuiltinWorkflow && showSettingsEditor)
                }
              />
            )}

            {/* A declared trigger's editor: source, filter, inputs, activations. */}
            {selectedDeclared !== null && declared[selectedDeclared] && !selectedNodeId && !selectedEdgeId && (
              <DeclaredTriggerPanel
                key={`${selectedDeclared}-${declared[selectedDeclared]!.name}`}
                index={selectedDeclared}
                trigger={declared[selectedDeclared]!}
                allTriggers={declared}
                inputs={workflow.inputs}
                catalogRef={declaredCatalogRefs[declared[selectedDeclared]!.name ?? ""]}
                findings={findingsFor(selectedDeclared, declared[selectedDeclared]!.name ?? "")}
                activations={declaredActivations[selectedDeclared]?.activations ?? []}
                isReadOnly={!canEditDefinition}
                canActivate={triggerRail.canAddTrigger}
                unsaved={unsavedDeclared.has(declared[selectedDeclared]!.name ?? "")}
                onClose={() => setSelectedDeclared(null)}
                onActivate={() => setActivatingDeclared(selectedDeclared)}
                onEditActivation={handleEditTrigger}
                bottomOffset={configPanelBottomOffset}
                topOffset={configPanelTopOffset}
              />
            )}

            {/* Test run: saves the draft, runs it, and the canvas shows the run. */}
            {showTestRunPanel && onSaveForTestRun && currentProject?.id && !isBuiltinWorkflow && !isEditingLoop && (
              <BuilderTestRunPanel
                projectId={currentProject.id}
                workflowRef={savedWorkflowName}
                saveDraft={saveForTestRun}
                testChatId={testRunChatId}
                onStarted={setTestRunChatId}
                onClose={() => setShowTestRunPanel(false)}
                bottomOffset={configPanelBottomOffset}
                topOffset={configPanelTopOffset}
                docked={
                  !!(selectedNode && selectedNode.id !== ENTRY_NODE_ID) ||
                  !!selectedEdge ||
                  showStartPanel ||
                  showSettingsEditor
                }
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

      {/* Chat panel - a normal chat; disabled for builtin workflows */}
      {currentProject?.id && !isBuiltinWorkflow && (
        <WorkflowEditorChatPanel
          workflowSlug={initialWorkflow?.name}
          chatId={chatId}
          onChatIdChange={(id) => onChatIdChange?.(id)}
          isStreamYielded={!!testRunChatId && showTestRunPanel}
          isOpen={chatPanelOpen}
          onOpenChange={(open) => {
            setChatPanelOpen(open);
            if (open) {
              // Close config panels when chat opens
              setSelectedNodeId(null);
              setSelectedEdgeId(null);
              setShowSettingsEditor(false);
              setShowStartPanel(false);
            }
          }}
          panelSize={chatPanelSize}
          onPanelSizeChange={setChatPanelSize}
          isConfigPanelOpen={
            !!(selectedNodeId || selectedEdgeId || showSettingsEditor || showStartPanel || showTestRunPanel)
          }
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
          applyRemoteWorkflowUpdate(w);
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

      {/* Step palette: built-in steps and integration actions in one search. */}
      <StepPalette
        key={paletteKind}
        open={paletteOpen}
        initialKind={paletteKind}
        initialFocus={paletteFocus}
        allowKindSwitch={canEditDefinition}
        onClose={() => setPaletteOpen(false)}
        onChooseBuiltin={choosePaletteBuiltin}
        onChooseAction={choosePaletteAction}
        onChooseTrigger={(entry) => void addTrigger.chooseCatalog(entry)}
        onChooseBuiltinTrigger={addTrigger.chooseBuiltin}
        onConnect={connectFromPalette}
      />
      {activatingDeclared !== null && declared[activatingDeclared] && (
        <ActivateTriggerDialog
          open
          onClose={() => setActivatingDeclared(null)}
          workflowRef={workflowRefForTrigger(savedWorkflowName, source)}
          workflowTitle={workflow.title || savedWorkflowName}
          declared={declared[activatingDeclared]!}
          catalogRef={declaredCatalogRefs[declared[activatingDeclared]!.name ?? ""]}
          defaultProjectId={currentProject?.id}
        />
      )}
      <ConnectIntegrationDialog target={connectTarget} onClose={() => setConnectTarget(null)} />

      {addTrigger.personal && (
        <ActivateTriggerDialog
          open
          mode="personal"
          onClose={addTrigger.closePersonal}
          workflowRef={workflowRefForTrigger(savedWorkflowName, source)}
          workflowTitle={workflow.title || savedWorkflowName}
          declared={addTrigger.personal.trigger}
          catalogRef={addTrigger.personal.catalogRef}
          defaultProjectId={currentProject?.id}
        />
      )}

      {/* Edit one of the caller's trigger rows (a personal trigger or an
          activation), from its card. Allowed on read-only and built-in
          workflows too: a row does not edit the definition. */}
      <AutomationFormDialog
        open={automationDialog !== null}
        onClose={() => setAutomationDialog(null)}
        trigger={automationDialog?.trigger}
      />
    </div>
    </TriggerRailProvider>
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