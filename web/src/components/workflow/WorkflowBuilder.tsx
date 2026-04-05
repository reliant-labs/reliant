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
import { ConfigPanel } from "./ConfigPanel";
import { EdgeConfigPanel } from "./EdgeConfigPanel";
import { SwitchConfigPanel } from "./SwitchConfigPanel";
import { WorkflowSettingsEditor } from "./WorkflowSettingsEditor";
import type {
  Workflow,
  Step,
  WorkflowStep,
  LoopStep,
  Edge as WorkflowEdge,
  Param,
  ThreadConfig,
  SwitchMetadata,
} from "../../types/workflow";
import {
  getStepType,
  isLoopStep,
  isWorkflowStep,
  getSwitchNodeId,
  getStepInline,
  initStepArgs,
  withRunArgs,
  withWorkflowArgs,
  withLoopArgs,
} from "../../types/workflow";
import {
  deriveWorkflowEntryFromEdges,
  sanitizeWorkflowReferences,
} from "./workflowRef";
import { autoLayoutWorkflow } from "../../lib/workflow-layout";
import {
  convertEdgesToFlowElements,
  resolveNodeOverlaps,
  getFlowNodeType,
  type FlowNodeData,
} from "../../lib/workflow-flow";
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
import { workflowGrpc, type ValidationError } from "../../api/workflow-grpc";
import { Modal } from "../ui/Modal";
import { Button } from "../ui/Button";

import type { BackgroundVariant, SelectionMode } from "@xyflow/react";
import { WorkflowBuilderChat, type PanelSize } from "./WorkflowBuilderChat";
import { ScenarioPanel } from "./ScenarioPanel";
import { useProjectStore } from "../../store/projectStore";
import { useOnboardingChecklistStore } from "../../store/onboardingChecklistStore";
import { useIsChatRunning } from "../../store/activityStore";
import { useGlobalUpdatesStore } from "../../store/globalUpdatesStore";
import { normalizeWorkflowRef } from "./useWorkflowInputs";
import { celString, directCel } from "../../lib/celAdapter";
import { getInputDescription, type InputDef } from "../../lib/inputHelpers";
import { getSubWorkflowRef } from "../../lib/workflow-step-accessors";
import {
  CELCompletionProvider,
  type CELCompletionContextValue,
} from "./CELCompletionContext";

// Navigation context for editing inline workflows (loops or workflow nodes with inline definitions)
interface InlineEditContext {
  // The parent workflow definition (with the step containing inline workflow)
  parentWorkflow: Workflow;
  // The ID of the step being edited (either a loop or workflow node)
  stepId: string;
  // The type of step being edited ('loop' or 'workflow')
  stepType: "loop" | "workflow";
  // The parent nodes/edges state (for restoring when navigating back)
  parentNodes: Node[];
  parentEdges: Edge[];
}

// Convert a step with inline workflow to a pseudo Workflow for editing
// Works for both loop and workflow nodes that have inline definitions
function inlineStepToWorkflow(
  step: LoopStep | WorkflowStep,
  label: string,
): Workflow {
  const inlineWorkflow = getStepInline(step) || {
    nodes: [],
    edges: [],
    entry: [],
    description: undefined,
    inputs: undefined,
    outputs: undefined,
    ui: undefined,
  };
  // Normalize entry to array format
  const entryArray = inlineWorkflow.entry
    ? Array.isArray(inlineWorkflow.entry)
      ? inlineWorkflow.entry
      : [inlineWorkflow.entry]
    : [];
  return {
    name: `${step.id} (${label})`,
    description:
      inlineWorkflow.description ||
      `Inline ${label.toLowerCase()} for ${step.id}`,
    nodes: inlineWorkflow.nodes || [],
    edges: inlineWorkflow.edges || [],
    inputs: inlineWorkflow.inputs,
    outputs: inlineWorkflow.outputs,
    entry: entryArray,
    ui: inlineWorkflow.ui,
  };
}

// Convert edited workflow back to inline definition
// Returns the updated inline workflow definition to set on step.inline
function workflowToInlineDefinition(workflow: Workflow): Workflow {
  return {
    name: workflow.name,
    description: workflow.description,
    nodes: workflow.nodes,
    edges: workflow.edges,
    outputs: workflow.outputs,
    inputs: workflow.inputs,
    entry: workflow.entry || [],
    ui: workflow.ui,
  };
}

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
}: WorkflowBuilderProps) {
  const reactFlowWrapper = useRef<HTMLDivElement>(null);
  const [nodes, setNodes, onNodesChange] = useNodesState<Node>([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([]);
  const [_reactFlowInstance, setReactFlowInstance] = useState<any>(null);
  const [workflowName, setWorkflowName] = useState(
    initialWorkflow?.name || initialName || "New Workflow",
  );
  const [workflowDescription, setWorkflowDescription] = useState(
    initialWorkflow?.description || "",
  );
  const [selectedNode, setSelectedNode] = useState<Node | null>(null);
  const [selectedEdge, setSelectedEdge] = useState<Edge | null>(null);

  const [isEditingName, setIsEditingName] = useState(false);
  const [showInfoPopover, setShowInfoPopover] = useState(false);
  const [interactionMode, setInteractionMode] =
    useState<InteractionMode>("pan");
  const [showSettingsEditor, setShowSettingsEditor] = useState(false);
  const [workflowInputs, setWorkflowInputs] = useState<Record<string, Param>>(
    initialWorkflow?.inputs || {},
  );

  // Track the workflow entry field (starting node(s)) - use entry field instead of deprecated "from: started" edges
  const [workflowEntry, setWorkflowEntry] = useState<
    string | string[] | undefined
  >(initialWorkflow?.entry);
  // Track workflow outputs (CEL expressions)
  const [workflowOutputs, setWorkflowOutputs] = useState<
    Record<string, string>
  >(initialWorkflow?.outputs || {});
  // Track workflow tag (for preset matching)
  const [workflowTag, setWorkflowTag] = useState<string | undefined>(
    initialWorkflow?.presets?.tag,
  );
  // Track workflow preset default
  const [workflowPresetDefault, setWorkflowPresetDefault] = useState<
    string | undefined
  >(initialWorkflow?.presets?.default);
  // Track workflow-level thread configuration (legacy - not in proto)
  const [workflowThread, setWorkflowThread] = useState<
    ThreadConfig | undefined
  >(undefined);
  // Track api version
  const [workflowApiVersion, setWorkflowApiVersion] = useState<
    string | undefined
  >(initialWorkflow?.apiVersion);

  // Lock state - prevents node dragging when true
  const [isLocked, setIsLocked] = useState(
    initialWorkflow?.ui?.locked ?? false,
  );

  // Chat panel state (controlled) - used for dynamic fit view padding
  const [chatPanelOpen, setChatPanelOpen] = useState(true);
  const [chatPanelSize, setChatPanelSize] = useState<PanelSize>("normal");

  // Track if initial fit view has been applied (to prevent flash of default viewport)
  const [isViewReady, setIsViewReady] = useState(false);

  // Navigation state for editing inline loops
  // When editing an inline loop, this holds the parent context
  const [loopEditStack, setLoopEditStack] = useState<InlineEditContext[]>([]);
  const isEditingLoop = loopEditStack.length > 0;

  // Track which workflow is currently loaded to prevent unwanted resets
  const [loadedWorkflowName, setLoadedWorkflowName] = useState<
    string | undefined
  >(initialWorkflow?.name);
  const hasLoadedRef = useRef(false);
  // Track when we're applying agent updates to avoid false modification detection
  const isApplyingAgentUpdateRef = useRef(false);

  // Track if the original workflow was a builtin (passed from parent)
  const isBuiltinWorkflow = isBuiltin;

  // For builtins, we use a "Create a Copy" flow instead of direct editing
  // The user must explicitly copy the workflow with a new name before saving
  const [showTemplateModal, setShowTemplateModal] = useState(false);
  const [templateName, setTemplateName] = useState("");

  // Determine if nodes should be draggable
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

  // Track if workflow has been modified (independent of auto-save for new workflows)
  const [hasModifications, setHasModifications] = useState(false);

  // Validation state - tracks backend validation results
  const [validationStatus, setValidationStatus] =
    useState<ValidationStatus>("unknown");
  const [validationErrors, setValidationErrors] = useState<ValidationError[]>(
    [],
  );

  // YAML editor modal state
  const [showYamlEditor, setShowYamlEditor] = useState(false);

  // Scenario panel modal state
  const [showScenarioPanel, setShowScenarioPanel] = useState(false);

  // Get current project for the chat assistant
  const currentProject = useProjectStore((state) => state.currentProject);

  // Helper function to normalize names (replace spaces with hyphens, trim trailing spaces)
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

  // Get ReactFlow instance for programmatic fit view
  const { zoomIn, zoomOut, setViewport, getNodes, screenToFlowPosition } = useReactFlow();

  // Fit view with dynamic padding that accounts for visible panels
  // This function calculates the optimal viewport to show all nodes without overlap with UI panels
  // animate: true for smooth transition (user clicks button), false for instant (initial load)
  const fitViewWithPanels = useCallback(
    (animate = true) => {
      // Get the canvas wrapper dimensions
      const wrapper = reactFlowWrapper.current;
      const viewportWidth = wrapper?.clientWidth || 1200;
      const viewportHeight = wrapper?.clientHeight || 800;

      // Overlay dimensions (in pixels from viewport edges)
      const SIDEBAR_WIDTH = 220; // Left sidebar
      const CHAT_PANEL_NORMAL = 430; // Chat panel normal
      const CHAT_PANEL_MAXIMIZED = 630; // Chat panel maximized
      const CONFIG_PANEL_WIDTH = 410; // Config panel
      const HEADER_HEIGHT = 70; // Top header
      const TOOLBAR_HEIGHT = 100; // Bottom toolbar

      // Calculate right-side overlay
      let rightOverlay = 40;
      if (chatPanelOpen) {
        rightOverlay =
          chatPanelSize === "maximized"
            ? CHAT_PANEL_MAXIMIZED
            : CHAT_PANEL_NORMAL;
      }
      if (selectedNode || selectedEdge || showSettingsEditor) {
        rightOverlay = Math.max(rightOverlay, CONFIG_PANEL_WIDTH);
      }

      // Calculate the safe zone (area not covered by overlays)
      const safeLeft = SIDEBAR_WIDTH;
      const safeRight = viewportWidth - rightOverlay;
      const safeTop = isEditingLoop ? HEADER_HEIGHT + 40 : HEADER_HEIGHT;
      const safeBottom = viewportHeight - TOOLBAR_HEIGHT;

      const safeWidth = safeRight - safeLeft;
      const safeHeight = safeBottom - safeTop;

      // Get current nodes to calculate bounds
      const currentNodes = getNodes();
      if (currentNodes.length === 0) {
        // No nodes, just use default view
        setViewport(
          { x: 0, y: 0, zoom: 1 },
          animate ? { duration: 200 } : undefined,
        );
        return;
      }

      // Calculate node bounds
      let minX = Infinity,
        minY = Infinity,
        maxX = -Infinity,
        maxY = -Infinity;
      for (const node of currentNodes) {
        const x = node.position.x;
        const y = node.position.y;
        const width = node.measured?.width ?? node.width ?? 200;
        const height = node.measured?.height ?? node.height ?? 100;
        minX = Math.min(minX, x);
        minY = Math.min(minY, y);
        maxX = Math.max(maxX, x + width);
        maxY = Math.max(maxY, y + height);
      }

      const nodesWidth = maxX - minX;
      const nodesHeight = maxY - minY;

      // Add small padding around nodes (in world coordinates)
      const nodePadding = 40;
      const paddedWidth = nodesWidth + nodePadding * 2;
      const paddedHeight = nodesHeight + nodePadding * 2;

      // Calculate zoom to fit nodes in safe zone
      const zoomX = safeWidth / paddedWidth;
      const zoomY = safeHeight / paddedHeight;
      let zoom = Math.min(zoomX, zoomY);

      // Apply zoom constraints
      zoom = Math.max(0.3, Math.min(1.0, zoom)); // Keep between 30% and 100%

      // Calculate viewport position to center nodes in the safe zone
      // The safe zone center in viewport coords
      const safeCenterX = safeLeft + safeWidth / 2;
      const safeCenterY = safeTop + safeHeight / 2;

      // The nodes center in world coords
      const nodesCenterX = minX + nodesWidth / 2;
      const nodesCenterY = minY + nodesHeight / 2;

      // Viewport x,y represents the world coordinate at viewport (0,0)
      // To center nodes in safe zone: safeCenterX = -x * zoom + nodesCenterX * zoom
      // Solving for x: x = nodesCenterX - safeCenterX / zoom
      const x = -nodesCenterX * zoom + safeCenterX;
      const y = -nodesCenterY * zoom + safeCenterY;

      setViewport({ x, y, zoom }, animate ? { duration: 200 } : undefined);
    },
    [
      getNodes,
      setViewport,
      chatPanelOpen,
      chatPanelSize,
      selectedNode,
      selectedEdge,
      showSettingsEditor,
      isEditingLoop,
    ],
  );

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
        if (selectedNode && deletedNodeIds.includes(selectedNode.id)) {
          setSelectedNode(null);
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
    [onNodesChange, selectedNode, setEdges],
  );

  // Handle edge changes and close config panel if selected edge is deleted
  const handleEdgesChange = useCallback(
    (changes: any[]) => {
      onEdgesChange(changes);

      // Check if selected edge was deleted
      if (selectedEdge) {
        const wasDeleted = changes.some(
          (change: any) =>
            change.type === "remove" && change.id === selectedEdge.id,
        );
        if (wasDeleted) {
          setSelectedEdge(null);
        }
      }
    },
    [onEdgesChange, selectedEdge],
  );

  // Load initial workflow into nodes and edges
  // Only load when:
  // 1. Component first mounts and hasn't loaded yet
  // 2. A different workflow is being loaded (name changed)
  // This prevents unwanted resets when API calls fail or other re-renders occur
  useEffect(() => {
    const isNewWorkflow = initialWorkflow?.name !== loadedWorkflowName;
    const shouldLoad = !hasLoadedRef.current || isNewWorkflow;

    if (!shouldLoad) {
      return;
    }

    // Hide canvas while loading (will show after fit view is applied)
    setIsViewReady(false);

    if (initialWorkflow?.nodes) {
      // Get saved positions from ui.positions or use auto-layout
      const savedPositions = initialWorkflow.ui?.positions || {};
      const savedSwitches = initialWorkflow.ui?.switches || {};
      const workflowWithLayout = autoLayoutWorkflow(initialWorkflow);

      // Filter out any nodes without a type field (defensive check for malformed data)
      const validNodes = (workflowWithLayout.nodes || []).filter((step) => {
        if (!step.type || !step.id) {
          console.warn(
            "[WorkflowBuilder] Skipping node without type/id field:",
            step.id || "unknown",
          );
          return false;
        }
        return true;
      });
      // Layout positions computed by autoLayoutWorkflow — used when no saved positions exist
      const layoutPositions = workflowWithLayout.ui?.positions || {};
      const loadedNodes: Node[] = validNodes.map((step) => {
        const stepType = getStepType(step);
        const stepId = step.id!; // We already filtered out undefined ids
        // 3-tier chain: savedPositions → layoutPositions → random
        const savedPos = savedPositions[stepId];
        const layoutPos = layoutPositions[stepId] as { x?: number; y?: number } | undefined;
        const position = (savedPos?.x !== undefined && savedPos?.y !== undefined)
          ? { x: savedPos.x, y: savedPos.y }
          : (layoutPos?.x !== undefined && layoutPos?.y !== undefined)
            ? { x: layoutPos.x!, y: layoutPos.y! }
            : {
                x: Math.random() * 400 + 100,
                y: Math.random() * 400 + 100,
              };
        return {
          id: stepId,
          type: getFlowNodeType(stepType),
          position,
          data: {
            step,
            label: stepId,
          },
        };
      });

      // Saved switch metadata is applied only to switches regenerated from actual edges.

      // ALWAYS add the workflow entry point node for existing workflows
      // 3-tier chain: savedPositions → layoutPositions → default
      const savedWorkflowPos = savedPositions["workflow"];
      const layoutWorkflowPos = layoutPositions["workflow"] as { x?: number; y?: number } | undefined;
      const workflowNodePosition = (savedWorkflowPos?.x !== undefined && savedWorkflowPos?.y !== undefined)
        ? { x: savedWorkflowPos.x, y: savedWorkflowPos.y }
        : (layoutWorkflowPos?.x !== undefined && layoutWorkflowPos?.y !== undefined)
          ? { x: layoutWorkflowPos.x!, y: layoutWorkflowPos.y! }
          : { x: 50, y: 200 };
      const workflowNode: Node = {
        id: "workflow",
        type: "eventNode",
        position: workflowNodePosition,
        data: {
          eventType: "started",
          label: "Workflow Start",
        },
        draggable: canDragNodes,
        deletable: false, // Cannot delete the workflow entry point
      };

      // Create entry edges from the entry field if no explicit workflow edges exist
      const workflowEdgesArr = workflowWithLayout.edges || [];
      const hasWorkflowStartEdge = workflowEdgesArr.some(
        (e) => e.from === "workflow" || e.from === "started",
      );

      const edgesWithEntry = [...workflowEdgesArr];
      if (!hasWorkflowStartEdge && initialWorkflow.entry) {
        // Create synthetic edges from entry field
        const entryNodes = Array.isArray(initialWorkflow.entry)
          ? initialWorkflow.entry
          : [initialWorkflow.entry];
        for (const entryNode of entryNodes) {
          edgesWithEntry.push({
            from: "workflow",
            cases: [{ to: [entryNode] }],
          });
        }
      }

      // Convert workflow edges to flow edges and switch nodes
      // Multi-case edges become switch nodes automatically
      const { edges: loadedEdges, switchNodes: generatedSwitchNodes } =
        convertEdgesToFlowElements(
          edgesWithEntry,
          [workflowNode, ...loadedNodes],
          savedSwitches,
          undefined,
          canDragNodes,
        );

      // Use only switch nodes regenerated from current edges.
      // This prevents stale/orphan switch metadata from reappearing on the canvas.
      const allSwitchNodes: Node[] = [...generatedSwitchNodes];

      // Resolve any overlapping nodes before setting state
      const allNodes = [
        workflowNode,
        ...loadedNodes,
        ...allSwitchNodes,
      ] as Node[];
      const resolvedNodes = resolveNodeOverlaps(allNodes);
      setNodes(resolvedNodes);
      setEdges(loadedEdges as Edge[]);

      // Update workflow name, description, and other top-level fields
      setWorkflowName(initialWorkflow.name || initialName || "New Workflow");
      setWorkflowDescription(initialWorkflow.description || "");
      setWorkflowTag(initialWorkflow.presets?.tag);
      setWorkflowPresetDefault(initialWorkflow.presets?.default);
      // setWorkflowThread removed - not in proto
      setWorkflowApiVersion(initialWorkflow.apiVersion || "");
      // Also update inputs, outputs, entry - must be synced when loading a different workflow
      setWorkflowInputs(initialWorkflow.inputs || {});
      setWorkflowOutputs(initialWorkflow.outputs || {});
      setWorkflowEntry(initialWorkflow.entry);
    } else {
      // New workflow - only create workflow entry point node
      const workflowNode: Node = {
        id: "workflow",
        type: "eventNode",
        position: { x: 50, y: 200 },
        data: {
          eventType: "started",
          label: "Workflow Start",
        },
        draggable: canDragNodes,
        deletable: false, // Cannot delete the workflow entry point
      };

      setNodes([workflowNode] as Node[]);
      setEdges([] as Edge[]);

      // Reset to defaults for new workflow (use random name if provided)
      setWorkflowName(initialName || "New Workflow");
      setWorkflowDescription("");
      setWorkflowTag(undefined);
      setWorkflowPresetDefault(undefined);
      setWorkflowThread(undefined);
      setWorkflowApiVersion(undefined);
      // Also reset inputs, outputs, entry for new workflows
      setWorkflowInputs({});
      setWorkflowOutputs({});
      setWorkflowEntry(undefined);
    }

    // Mark as loaded and track the workflow name and status
    hasLoadedRef.current = true;
    setLoadedWorkflowName(initialWorkflow?.name);
    setIsLocked(initialWorkflow?.ui?.locked ?? false);

    // Reset dirty flag on load - only user edits should mark dirty
    setHasModifications(false);

    // Reset validation state when loading a new workflow
    setValidationStatus("validating");
    setValidationErrors([]);

    // Validate on open
    if (currentProject?.id && initialWorkflow) {
      workflowGrpc
        .validateWorkflow(currentProject.id, initialWorkflow)
        .then((result) => {
          setValidationErrors(result.errors);
          setValidationStatus(result.valid ? "valid" : "invalid");
        })
        .catch((error) => {
          console.error("Failed to validate workflow on load:", error);
          setValidationStatus("unknown");
        });
    }

    // Clear history when loading a new workflow
    clearHistory();

    // Reset loop edit stack when loading a new workflow
    setLoopEditStack([]);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- canDragNodes is derived from isBuiltinWorkflow and isLocked which are stable during workflow load
  }, [
    initialWorkflow,
    initialName,
    loadedWorkflowName,
    setNodes,
    setEdges,
    clearHistory,
  ]);

  // Update workflow name when initialName changes for new workflows
  // This handles the case where the random name arrives after initial render
  useEffect(() => {
    if (!initialWorkflow && initialName && workflowName === "New Workflow") {
      setWorkflowName(initialName);
    }
  }, [initialName, initialWorkflow, workflowName]);

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
  const buildWorkflow = useCallback((): Workflow => {
    // Extract steps from nodes (excluding event nodes and switch nodes - they're UI-only)
    const steps = nodes
      .filter((node) => node.type !== "eventNode" && node.type !== "switchNode")
      .map((node) => {
        const step = node.data.step as WorkflowStep;
        // Remove triggers and position from steps (deprecated, now using edges and ui.positions)
        const {
          triggers: _triggers,
          position: _position,
          ...cleanStep
        } = step as any;
        return cleanStep;
      });

    // Build UI metadata - include positions for all nodes (excluding switch nodes, handled separately)
    const positions: Record<string, { x: number; y: number }> = {};
    nodes.forEach((node) => {
      if (node.position && node.type !== "switchNode") {
        positions[node.id] = node.position;
      }
    });

    // Build set of valid node IDs (including event node IDs like 'workflow')
    const validNodeIds = new Set(nodes.map((n) => n.id));

    // Build a map of switch nodes for resolving edges through switches
    const switchNodes = new Map<
      string,
      {
        cases: Array<{ id: string; condition: string; label?: string }>;
        position: { x: number; y: number };
      }
    >();
    for (const node of nodes) {
      if (node.type === "switchNode") {
        const nodeData = node.data as FlowNodeData;
        switchNodes.set(node.id, {
          cases: (nodeData.cases || []) as Array<{
            id: string;
            condition: string;
            label?: string;
          }>,
          position: node.position,
        });
      }
    }

    // Find edges going INTO switch nodes (to know the source of the switch)
    const switchInputs = new Map<string, string>(); // switchNodeId -> sourceNodeId
    for (const edge of edges) {
      if (switchNodes.has(edge.target)) {
        switchInputs.set(edge.target, edge.source);
      }
    }

    const sourceBySwitchId = new Map<string, string>();
    for (const edge of edges) {
      if (!switchNodes.has(edge.target)) {
        continue;
      }
      const sourceEvent = (edge.data as { sourceEvent?: string } | undefined)
        ?.sourceEvent;
      const source =
        edge.source === "workflow"
          ? "started"
          : sourceEvent && sourceEvent !== "completed"
            ? `${edge.source}.${sourceEvent}`
            : edge.source;
      sourceBySwitchId.set(edge.target, source);
    }

    // Build switch metadata for UI persistence
    const switches: Record<string, SwitchMetadata> = {};
    for (const [switchId, switchData] of switchNodes) {
      const sourceNode = switchInputs.get(switchId);
      const sourceFrom = sourceBySwitchId.get(switchId);
      if (sourceNode && sourceFrom) {
        const persistedSwitchId = getSwitchNodeId(sourceFrom);
        switches[persistedSwitchId] = {
          sourceNode,
          position: switchData.position,
          cases: switchData.cases.map((c) => ({
            id: c.id,
            condition: c.condition ? directCel(c.condition) : undefined,
            label: c.label ?? "",
          })),
        };
      }
    }

    // Convert React Flow edges to workflow edges
    // Group by source, handling switch nodes specially
    const edgesBySource = new Map<
      string,
      Array<{
        target: string;
        condition?: string;
        label?: string;
        sourceEvent?: string;
      }>
    >();

    for (const edge of edges) {
      const edgeData = (edge.data || {}) as {
        sourceEvent?: string;
        label?: string;
      };
      const { sourceEvent, label } = edgeData;

      // Skip edges going TO switch nodes (handled below)
      if (switchNodes.has(edge.target)) {
        continue;
      }

      // Filter out orphaned edges
      if (!validNodeIds.has(edge.source) || !validNodeIds.has(edge.target)) {
        continue;
      }

      let from: string;
      let condition: string | undefined;
      let edgeLabel: string | undefined = label;

      // Check if this edge is coming FROM a switch node
      if (switchNodes.has(edge.source)) {
        const switchNode = switchNodes.get(edge.source)!;
        const switchSource = sourceBySwitchId.get(edge.source);

        if (!switchSource) {
          console.warn(`Switch node ${edge.source} has no input edge`);
          continue;
        }

        // Find which case this edge is for (by sourceHandle)
        const caseId = edge.sourceHandle;
        const caseIndex = switchNode.cases.findIndex((c) => c.id === caseId);
        const caseData = caseIndex >= 0 ? switchNode.cases[caseIndex] : null;

        // Use the switch's input source as the "from"
        // Preserves event-scoped sources like "node.failed".
        from = switchSource;

        // Get condition from the case
        if (caseData) {
          condition = caseData.condition || undefined;
          edgeLabel = caseData.label || edgeLabel;
        }
      } else {
        // Regular edge (not from a switch)
        // Format: "source-id" or "source-id.event-name"
        if (edge.source === "workflow") {
          // Workflow start node always maps to "started" in the definition
          from = "started";
        } else if (sourceEvent && sourceEvent !== "completed") {
          from = `${edge.source}.${sourceEvent}`;
        } else {
          from = edge.source;
        }
      }

      if (!edgesBySource.has(from)) {
        edgesBySource.set(from, []);
      }
      edgesBySource.get(from)!.push({
        target: edge.target,
        condition,
        label: edgeLabel,
        sourceEvent,
      });
    }

    // Convert grouped edges to Edge format with cases
    // Separate out "started" edges to use entry field instead
    const workflowEdges: WorkflowEdge[] = [];
    const entryTargets: string[] = [];

    for (const [from, cases] of edgesBySource) {
      if (from === "started") {
        // Extract entry targets instead of creating "from: started" edges
        for (const c of cases) {
          entryTargets.push(c.target);
        }
      } else {
        // Separate conditional cases from default targets
        const conditionalCases = cases.filter((c) => c.condition);
        const defaultCases = cases.filter((c) => !c.condition);

        // Create separate edges for each default target (don't merge into arrays)
        for (const c of defaultCases) {
          workflowEdges.push({ from, default: [c.target] });
        }

        // Create edge for conditional cases if any
        if (conditionalCases.length > 0) {
          workflowEdges.push({
            from,
            cases: conditionalCases.map((c) => ({
              to: [c.target],
              condition: c.condition!,
              label: c.label,
            })),
          });
        }
      }
    }

    // Compute entry field from visual edges. Connections from the workflow start node
    // should take precedence over any previously edited workflowEntry state.
    let computedEntry = deriveWorkflowEntryFromEdges(workflowEntry, edges);
    if (!computedEntry && entryTargets.length > 0) {
      computedEntry = entryTargets;
    }

    const { entry: sanitizedEntry, outputs: sanitizedOutputs } =
      sanitizeWorkflowReferences(computedEntry, workflowOutputs, steps.map((step) => step.id));

    return {
      name: workflowName,
      description: workflowDescription || undefined,
      presets:
        workflowTag || workflowPresetDefault
          ? {
              tag: workflowTag,
              default: workflowPresetDefault,
            }
          : undefined,
      // Note: workflow-level thread config removed (not in proto)
      apiVersion: workflowApiVersion || undefined,
      nodes: steps,
      edges: workflowEdges.length > 0 ? workflowEdges : undefined, // Only include if non-empty
      entry: sanitizedEntry, // Use entry field instead of "from: started" edges
      inputs:
        Object.keys(workflowInputs).length > 0
          ? (workflowInputs as Workflow["inputs"])
          : undefined,

      outputs: sanitizedOutputs,
      ui: {
        positions,
        ...(Object.keys(switches).length > 0 && { switches }),
        ...(isLocked && { locked: true }),
      },
    };
  }, [
    nodes,
    edges,
    workflowName,
    workflowDescription,
    workflowInputs,
    workflowOutputs,
    workflowEntry,
    workflowTag,
    isLocked,
    workflowApiVersion,
    workflowPresetDefault,
  ]);

  // Mark workflow as dirty (has unsaved changes)
  // Called from user edit handlers - NOT from load/restore operations
  const markDirty = useCallback(() => {
    if (hasLoadedRef.current && !isApplyingAgentUpdateRef.current) {
      setHasModifications(true);
    }
  }, []);

  // Enter inline editing mode - navigate into an inline workflow (loop or workflow node)
  const enterInlineEdit = useCallback(
    (step: LoopStep | WorkflowStep, stepType: "loop" | "workflow") => {
      // Steps must have an ID to be editable
      if (!step.id) {
        toast.error("Cannot edit step without an ID");
        return;
      }
      // If the step uses a workflow reference (not inline), we can't edit it here
      if (getSubWorkflowRef(step) && !getStepInline(step)) {
        toast.info(
          "This step references an external workflow. Open that workflow to edit it.",
        );
        return;
      }

      // Save current state to the stack
      const currentWorkflow = buildWorkflow();
      const context: InlineEditContext = {
        parentWorkflow: currentWorkflow,
        stepId: step.id,
        stepType,
        parentNodes: [...nodes],
        parentEdges: [...edges],
      };

      setLoopEditStack((prev) => [...prev, context]);

      // Hide canvas while loading inline body (will show after fit view is applied)
      setIsViewReady(false);

      // Convert step to editable workflow (inline is directly on the step)
      const label = stepType === "loop" ? "Loop Body" : "Inline Workflow";
      const inlineWorkflow = inlineStepToWorkflow(step, label);
      const workflowWithLayout = autoLayoutWorkflow(inlineWorkflow);

      // Load the inline body into the editor
      const savedPositions = inlineWorkflow.ui?.positions || {};
      // Filter out any nodes without a type or id field (defensive check for malformed data)
      const validInlineNodes = (workflowWithLayout.nodes || []).filter((s) => {
        if (!s.type || !s.id) {
          console.warn(
            "[WorkflowBuilder] Skipping inline node without type/id field:",
            s.id || "unknown",
          );
          return false;
        }
        return true;
      });
      // Layout positions from autoLayoutWorkflow — used when no saved positions exist
      const layoutPositions = workflowWithLayout.ui?.positions || {};
      const loadedNodes: Node[] = validInlineNodes.map((s) => {
        const sType = getStepType(s);
        const stepId = s.id!; // Already filtered out undefined ids
        // 3-tier chain: savedPositions → layoutPositions → random
        const savedPos = savedPositions[stepId];
        const layoutPos = layoutPositions[stepId] as { x?: number; y?: number } | undefined;
        const position = (savedPos?.x !== undefined && savedPos?.y !== undefined)
          ? { x: savedPos.x, y: savedPos.y }
          : (layoutPos?.x !== undefined && layoutPos?.y !== undefined)
            ? { x: layoutPos.x!, y: layoutPos.y! }
            : { x: Math.random() * 400 + 100, y: Math.random() * 400 + 100 };
        return {
          id: stepId,
          type: getFlowNodeType(sType), // Use getFlowNodeType to get correct node type (actionNode, switchNode, etc.)
          position,
          data: {
            step: s,
            label: stepId,
          },
        };
      });

      // Add workflow start node
      const startLabel = stepType === "loop" ? "Loop Start" : "Workflow Start";
      // 3-tier chain: savedPositions → layoutPositions → default
      const savedWfPos = savedPositions["workflow"] || savedPositions["started"];
      const layoutWfPos = layoutPositions["workflow"] as { x?: number; y?: number } | undefined;
      const workflowNode: Node = {
        id: "workflow",
        type: "eventNode",
        position: (savedWfPos?.x !== undefined && savedWfPos?.y !== undefined)
          ? { x: savedWfPos.x, y: savedWfPos.y }
          : (layoutWfPos?.x !== undefined && layoutWfPos?.y !== undefined)
            ? { x: layoutWfPos.x!, y: layoutWfPos.y! }
            : { x: 50, y: 200 },
        data: {
          eventType: "started",
          label: startLabel,
        },
        draggable: canDragNodes,
        deletable: false,
      };

      // Create entry edges from the entry field if no explicit workflow edges exist
      const workflowEdgesArr = workflowWithLayout.edges || [];
      const hasWorkflowStartEdge = workflowEdgesArr.some(
        (e) => e.from === "workflow" || e.from === "started",
      );

      const edgesWithEntry = [...workflowEdgesArr];
      if (!hasWorkflowStartEdge && inlineWorkflow.entry) {
        // Create synthetic edges from entry field
        const entryNodes = inlineWorkflow.entry;
        for (const entryNode of entryNodes) {
          edgesWithEntry.push({
            from: "workflow",
            cases: [{ to: [entryNode] }],
          });
        }
      }

      // Convert edges
      const { edges: loadedEdges, switchNodes } = convertEdgesToFlowElements(
        edgesWithEntry,
        [workflowNode, ...loadedNodes],
        {},
        undefined,
        canDragNodes,
      );

      setNodes([workflowNode, ...loadedNodes, ...switchNodes] as Node[]);
      setEdges(loadedEdges as Edge[]);
      setWorkflowName(inlineWorkflow.name || "");
      setWorkflowDescription(inlineWorkflow.description || "");
      setWorkflowInputs(inlineWorkflow.inputs || {});
      // Clear workflowEntry so that buildWorkflow derives the entry point from the visual edges
      setWorkflowEntry(undefined);
      setSelectedNode(null);
      setSelectedEdge(null);
      clearHistory();

      toast.success(
        `${isBuiltinWorkflow ? "Viewing" : "Editing"} ${label.toLowerCase()}: ${step.id}`,
      );
    },
    [
      nodes,
      edges,
      buildWorkflow,
      setNodes,
      setEdges,
      clearHistory,
      isBuiltinWorkflow,
      canDragNodes,
    ],
  );

  // Convenience wrappers for enterInlineEdit
  const enterLoopEdit = useCallback(
    (loopStep: LoopStep) => {
      enterInlineEdit(loopStep, "loop");
    },
    [enterInlineEdit],
  );

  const enterWorkflowEdit = useCallback(
    (workflowStep: WorkflowStep) => {
      enterInlineEdit(workflowStep, "workflow");
    },
    [enterInlineEdit],
  );

  // Exit inline editing mode - save changes back to parent and navigate up
  const exitLoopEdit = useCallback(
    (saveChanges: boolean = true) => {
      if (loopEditStack.length === 0) return;

      // Hide canvas while restoring parent workflow (will show after fit view is applied)
      setIsViewReady(false);

      const context = loopEditStack[loopEditStack.length - 1];
      const isStepMatch =
        context.stepType === "loop" ? isLoopStep : isWorkflowStep;

      if (saveChanges) {
        // Build the current workflow (inline body)
        const inlineBodyWorkflow = buildWorkflow();

        // Find the original step in the parent workflow
        const originalStep = (context.parentWorkflow.nodes || []).find(
          (s) => s.id === context.stepId && isStepMatch(s),
        ) as (LoopStep | WorkflowStep) | undefined;

        if (originalStep) {
          // Convert edited workflow back to inline definition
          const updatedInline = workflowToInlineDefinition(inlineBodyWorkflow);

          // Update the parent nodes with the new inline workflow
          // With flattened schema, inline is directly on the step
          const updatedParentNodes = context.parentNodes.map((node) => {
            if (node.id === context.stepId) {
              const step = (node.data as { step: Step }).step;
              if (isStepMatch(step)) {
                const updatedStep =
                  step.type === "loop"
                    ? withLoopArgs(step, { inline: updatedInline } as any)
                    : withWorkflowArgs(step, { inline: updatedInline } as any);
                return {
                  ...node,
                  data: {
                    ...node.data,
                    step: updatedStep,
                  },
                };
              }
            }
            return node;
          });

          // Restore parent state with updated step
          setNodes(updatedParentNodes as Node[]);
          setEdges(context.parentEdges as Edge[]);
          setWorkflowName(context.parentWorkflow.name || "");
          setWorkflowDescription(context.parentWorkflow.description || "");
          setWorkflowInputs(context.parentWorkflow.inputs || {});
          setWorkflowEntry(context.parentWorkflow.entry);

          toast.success("Changes applied to workflow");
        } else {
          // Couldn't find original step, just restore without changes
          setNodes(context.parentNodes as Node[]);
          setEdges(context.parentEdges as Edge[]);
          setWorkflowName(context.parentWorkflow.name || "");
          setWorkflowDescription(context.parentWorkflow.description || "");
          setWorkflowInputs(context.parentWorkflow.inputs || {});
          setWorkflowEntry(context.parentWorkflow.entry);
        }
      } else {
        // Discard changes, just restore parent state
        setNodes(context.parentNodes as Node[]);
        setEdges(context.parentEdges as Edge[]);
        setWorkflowName(context.parentWorkflow.name || "");
        setWorkflowDescription(context.parentWorkflow.description || "");
        setWorkflowInputs(context.parentWorkflow.inputs || {});
        setWorkflowEntry(context.parentWorkflow.entry);
      }

      // Pop the stack
      setLoopEditStack((prev) => prev.slice(0, -1));
      setSelectedNode(null);
      setSelectedEdge(null);
      clearHistory();
    },
    [loopEditStack, buildWorkflow, setNodes, setEdges, clearHistory],
  );

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
    markDirty();

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
    markDirty,
    buildWorkflow,
    setNodes,
    fitViewWithPanels,
  ]);

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

  // Keyboard shortcuts for undo/redo
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      // Ctrl/Cmd + Z for undo
      if ((e.ctrlKey || e.metaKey) && e.key === "z" && !e.shiftKey) {
        e.preventDefault();
        handleUndo();
      }
      // Ctrl/Cmd + Shift + Z for redo
      else if ((e.ctrlKey || e.metaKey) && e.key === "z" && e.shiftKey) {
        e.preventDefault();
        handleRedo();
      }
      // Ctrl/Cmd + Y for redo (alternative)
      else if ((e.ctrlKey || e.metaKey) && e.key === "y") {
        e.preventDefault();
        handleRedo();
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [handleUndo, handleRedo]);

  // Escape key handling - deselect first, then navigate back to hub
  // Uses capture phase to intercept before ModernApp's global handler
  useEffect(() => {
    const handleEscape = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;

      // Skip if we're in an input field
      const target = e.target as HTMLElement;
      if (
        target.tagName === "INPUT" ||
        target.tagName === "TEXTAREA" ||
        target.contentEditable === "true"
      ) {
        return;
      }

      // Skip if modals are open - let them handle ESC
      if (showTemplateModal || showExitConfirmModal || showActiveChatModal) {
        return;
      }

      // Defer to onboarding tour if it's active (it handles its own Escape)
      if (useOnboardingChecklistStore.getState().isWizardActive) return;

      // Prevent ModernApp from handling this ESC
      e.preventDefault();
      e.stopPropagation();
      e.stopImmediatePropagation();

      // Priority 1: If editing a loop, exit loop edit (save changes only for non-builtins)
      if (isEditingLoop) {
        exitLoopEdit(!isBuiltinWorkflow);
        return;
      }

      // Priority 2: If something is selected or settings panel is open, deselect/close
      if (selectedNode || selectedEdge || showSettingsEditor) {
        setSelectedNode(null);
        setSelectedEdge(null);
        setShowSettingsEditor(false);
        return;
      }

      // Priority 3: Nothing selected - go back to hub
      handleBackClick();
    };

    // Use window with capture phase to run BEFORE document-level handlers (like ModernApp's global shortcuts)
    // This ensures the workflow navigation stack is respected (panels → hub → exit)
    window.addEventListener("keydown", handleEscape, true);
    return () => window.removeEventListener("keydown", handleEscape, true);
  }, [
    isEditingLoop,
    exitLoopEdit,
    selectedNode,
    selectedEdge,
    showSettingsEditor,
    showTemplateModal,
    showExitConfirmModal,
    showActiveChatModal,
    handleBackClick,
    isBuiltinWorkflow,
  ]);

  // Listen for loop-expand events from LoopNode expand button clicks
  useEffect(() => {
    const handleLoopExpand = (
      e: CustomEvent<{ loopNodeId: string; step: LoopStep }>,
    ) => {
      enterLoopEdit(e.detail.step);
    };

    document.addEventListener("loop-expand", handleLoopExpand as EventListener);
    return () => {
      document.removeEventListener(
        "loop-expand",
        handleLoopExpand as EventListener,
      );
    };
  }, [enterLoopEdit]);

  // Listen for workflow-expand events from WorkflowNode expand button clicks
  useEffect(() => {
    const handleWorkflowExpand = (
      e: CustomEvent<{ workflowNodeId: string; step: WorkflowStep }>,
    ) => {
      enterWorkflowEdit(e.detail.step);
    };

    document.addEventListener(
      "workflow-expand",
      handleWorkflowExpand as EventListener,
    );
    return () => {
      document.removeEventListener(
        "workflow-expand",
        handleWorkflowExpand as EventListener,
      );
    };
  }, [enterWorkflowEdit]);

  // Update sibling info whenever edges change
  useEffect(() => {
    setEdges((currentEdges) => {
      // Group edges by source-target pair
      const groups = new Map<string, Edge[]>();
      currentEdges.forEach((edge) => {
        const key = `${edge.source}-${edge.target}`;
        if (!groups.has(key)) groups.set(key, []);
        groups.get(key)!.push(edge);
      });

      // Update sibling info
      return currentEdges.map((edge) => {
        const key = `${edge.source}-${edge.target}`;
        const siblings = groups.get(key) || [];
        const siblingIndex = siblings.findIndex((e) => e.id === edge.id);
        const totalSiblings = siblings.length;

        if (
          edge.data?.siblingIndex !== siblingIndex ||
          edge.data?.totalSiblings !== totalSiblings
        ) {
          return {
            ...edge,
            data: {
              ...edge.data,
              siblingIndex,
              totalSiblings,
            },
          };
        }
        return edge;
      });
    });
  }, [edges.length, setEdges]);

  // Handle edge selection
  const onEdgeClick = useCallback((_event: React.MouseEvent, edge: Edge) => {
    setShowSettingsEditor(false);
    setSelectedEdge(edge);
    setSelectedNode(null);
    setChatPanelOpen(false); // Close chat when config panel opens
  }, []);

  // Handle clicking on canvas (deselect)
  const onPaneClick = useCallback(() => {
    setSelectedNode(null);
    setSelectedEdge(null);
    setShowSettingsEditor(false);
  }, []);

  // Update step configuration (no snapshot - config changes not tracked for undo, but do mark dirty)
  const handleStepUpdate = useCallback(
    (updatedStep: Step) => {
      // Defensive check for malformed step data
      if (!updatedStep.type) {
        console.error(
          "[WorkflowBuilder] Cannot update step without type field:",
          updatedStep.id,
        );
        return;
      }
      markDirty();
      setNodes((nds) =>
        nds.map((node) => {
          if (node.id === updatedStep.id) {
            const nodeType = getStepType(updatedStep);
            return {
              ...node,
              id: updatedStep.id,
              type: getFlowNodeType(nodeType),
              data: {
                step: updatedStep,
                label: updatedStep.id,
              },
            };
          }
          return node;
        }),
      );

      // Update selected node
      setSelectedNode((current) => {
        if (current?.id === updatedStep.id) {
          return {
            ...current,
            id: updatedStep.id,
            data: {
              step: updatedStep,
              label: updatedStep.id,
            },
          } as Node;
        }
        return current;
      });
    },
    [setNodes, markDirty],
  );

  const handleStepDelete = useCallback(
    (stepId: string) => {
      // Take snapshot BEFORE deleting
      takeSnapshot(nodes, edges);
      markDirty();
      setNodes((nds) => nds.filter((node) => node.id !== stepId));
      setSelectedNode(null);
      // Edges will be automatically cleaned up by handleNodesChange
    },
    [setNodes, nodes, edges, takeSnapshot, markDirty],
  );

  // Handle node rename - updates node, edges, and related references
  const handleNodeRename = useCallback(
    (oldId: string, newId: string) => {
      if (oldId === newId) return;

      // Take snapshot for undo
      takeSnapshot(nodes, edges);
      markDirty();

      // Update the node's id and data
      setNodes((nds) =>
        nds.map((node) => {
          if (node.id === oldId) {
            const step = (node.data as { step: Step }).step;
            const updatedStep = { ...step, id: newId };
            return {
              ...node,
              id: newId,
              data: {
                ...node.data,
                step: updatedStep,
                label: newId,
              },
            };
          }
          return node;
        }),
      );

      // Update all edges that reference the old ID
      setEdges((eds) =>
        eds.map((edge) => {
          let updated = false;
          const newEdge = { ...edge };

          // Update source if it matches
          if (edge.source === oldId) {
            newEdge.source = newId;
            // Also update edge ID if it contains the old source ID
            newEdge.id = newEdge.id.replace(
              new RegExp(`^${oldId}-`),
              `${newId}-`,
            );
            updated = true;
          }

          // Update target if it matches
          if (edge.target === oldId) {
            newEdge.target = newId;
            // Also update edge ID if it contains the old target ID
            newEdge.id = newEdge.id.replace(
              new RegExp(`-${oldId}$`),
              `-${newId}`,
            );
            updated = true;
          }

          return updated ? newEdge : edge;
        }),
      );

      // Update selected node if it was the renamed node
      setSelectedNode((current) => {
        if (current?.id === oldId) {
          const step = (current.data as { step: Step }).step;
          const updatedStep = { ...step, id: newId };
          return {
            ...current,
            id: newId,
            data: {
              ...current.data,
              step: updatedStep,
              label: newId,
            },
          };
        }
        return current;
      });
    },
    [nodes, edges, takeSnapshot, setNodes, setEdges, markDirty],
  );

  const handleSwitchUpdate = useCallback(
    (nodeId: string, data: any) => {
      markDirty();
      setNodes((nds) => {
        const updated = nds.map((node) => {
          if (node.id === nodeId) {
            const newNode = {
              ...node,
              data: {
                ...node.data,
                ...data,
              },
            };
            // Also update selectedNode so the panel sees the changes
            setSelectedNode(newNode);
            return newNode;
          }
          return node;
        });

        // Reconcile switch edges when case IDs change (e.g. re-ordering/deleting cases).
        // Any edge leaving this switch whose sourceHandle no longer exists should be removed,
        // otherwise buildWorkflow may serialize malformed routing.
        const updatedSwitch = updated.find((n) => n.id === nodeId);
        const nextCases = ((updatedSwitch?.data as FlowNodeData | undefined)
          ?.cases || []) as Array<{ id: string }>;
        const validHandles = new Set(nextCases.map((c) => c.id));

        setEdges((eds) =>
          eds.filter((edge) => {
            if (edge.source !== nodeId) {
              return true;
            }
            if (!edge.sourceHandle) {
              return true;
            }
            return validHandles.has(edge.sourceHandle);
          }),
        );

        return updated;
      });
    },
    [setNodes, setEdges, markDirty],
  );

  const handleEdgeUpdate = useCallback(
    (edgeId: string, data: any) => {
      // No snapshot for edge config updates (labels, conditions, etc.) but do mark dirty
      markDirty();
      setEdges((eds) =>
        eds.map((edge) => {
          if (edge.id === edgeId) {
            return {
              ...edge,
              data: {
                ...edge.data,
                ...data,
              },
            };
          }
          return edge;
        }),
      );

      // Update selected edge
      setSelectedEdge((current) => {
        if (current?.id === edgeId) {
          return {
            ...current,
            data: {
              ...current.data,
              ...data,
            },
          };
        }
        return current;
      });
    },
    [setEdges, markDirty],
  );

  // Delete an edge
  const handleDeleteCase = useCallback(
    (edgeId: string) => {
      takeSnapshot(nodes, edges);
      markDirty();
      setEdges((eds) => eds.filter((e) => e.id !== edgeId));
      setSelectedEdge(null);
    },
    [edges, nodes, setEdges, takeSnapshot, markDirty],
  );

  // Create a new edge, optionally as part of an existing switch
  const createEdge = useCallback(
    (sourceId: string, targetId: string, sourceHandle?: string) => {
      const sourceNode = nodes.find((n) => n.id === sourceId);
      const targetNode = nodes.find((n) => n.id === targetId);

      if (!sourceNode || !targetNode) return;

      takeSnapshot(nodes, edges);
      markDirty();

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
    [nodes, edges, setEdges, takeSnapshot, markDirty],
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
    setSelectedNode(node);
    setSelectedEdge(null);
    setChatPanelOpen(false); // Close chat when config panel opens
  }, []);

  // Navigate to a node by ID (used by validation error clicks)
  const navigateToNode = useCallback(
    (nodeId: string) => {
      const node = nodes.find((n) => n.id === nodeId);
      if (node) {
        setShowSettingsEditor(false);
        setSelectedNode(node);
        setSelectedEdge(null);
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

    if (!workflowName || workflowName.trim() === "") {
      toast.error("Please give your workflow a name before saving", {
        duration: 3000,
      });
      return;
    }

    const workflow = buildWorkflow();

    try {
      const result = await onSave?.(workflow);

      setLoadedWorkflowName(workflow.name);
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
  }, [buildWorkflow, onSave, workflowName, isBuiltinWorkflow]);

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
    // Suggest a default name based on the builtin name with a random suffix for uniqueness
    const builtinName = initialWorkflow?.name
      ? normalizeWorkflowRef(initialWorkflow.name)
      : "workflow";
    const randomSuffix = Math.random().toString(36).substring(2, 8);
    setTemplateName(`my-${builtinName}-${randomSuffix}`);
    setShowTemplateModal(true);
  }, [initialWorkflow?.name]);

  const handleTemplateConfirm = useCallback(async () => {
    const normalizedName = normalizeName(templateName);

    if (!normalizedName) {
      toast.error("Please enter a valid workflow name", { duration: 3000 });
      return;
    }

    // Update the workflow name - this will make it no longer a "builtin"
    setWorkflowName(normalizedName);
    setShowTemplateModal(false);

    // Build and save the workflow with the new name
    const workflow = buildWorkflow();
    workflow.name = normalizedName;

    try {
      await onSave?.(workflow);
      setLoadedWorkflowName(normalizedName);
      setHasModifications(false);
      toast.success(`Created "${normalizedName}" from template`, {
        duration: 3000,
      });
    } catch (error) {
      console.error("Failed to save template:", error);
    }
  }, [templateName, buildWorkflow, onSave]);

  // Get current workflow for the chat assistant
  const currentWorkflow = useMemo(() => buildWorkflow(), [buildWorkflow]);

  // Get list of existing node IDs for validation (used by ConfigPanel to prevent duplicates)
  const existingNodeIds = useMemo(() => nodes.map((node) => node.id), [nodes]);

  // Build CEL completion context for Monaco editors in config panels
  const celCompletionContext = useMemo<CELCompletionContextValue>(() => {
    const nodeIds: string[] = [];
    const nodeTypeMap: Record<string, string> = {};
    for (const node of nodes) {
      if (node.type === "eventNode" || node.type === "switchNode") continue;
      const step = (node.data as FlowNodeData).step as Step | undefined;
      nodeIds.push(node.id);
      if (step?.type) {
        nodeTypeMap[node.id] = step.type;
      }
    }
    const inputParams: Record<string, { type: string; description?: string }> =
      {};
    if (workflowInputs) {
      for (const [key, param] of Object.entries(workflowInputs)) {
        inputParams[key] = {
          type: param.type ?? "string",
          description: getInputDescription(param as InputDef),
        };
      }
    }
    const edgeList = edges.map((e) => ({ source: e.source, target: e.target }));
    return { nodeIds, nodeTypeMap, inputParams, edges: edgeList };
  }, [nodes, edges, workflowInputs]);

  // Handle workflow updates from the chat assistant
  const handleChatWorkflowUpdate = useCallback(
    (updatedWorkflow: Workflow) => {
      // Validate workflow structure
      if (!updatedWorkflow || !updatedWorkflow.nodes) {
        return;
      }

      // When editing a loop, the chat receives the parent workflow (not the loop body).
      // We need to update the loopEditStack with the new parent workflow from the agent
      // so that when the user exits the loop, they don't lose the agent's changes.
      if (isEditingLoop && loopEditStack.length > 0) {
        // Convert the updated workflow to ReactFlow nodes/edges for the parent context
        const parentWorkflowWithLayout = autoLayoutWorkflow(updatedWorkflow);
        const layoutPositions = parentWorkflowWithLayout.ui?.positions || {};
        const validParentNodes = (parentWorkflowWithLayout.nodes || []).filter(
          (step) => step.type && step.id,
        );
        const parentStepNodes: Node[] = validParentNodes.map((step) => {
          const stepType = getStepType(step);
          const savedPosition = updatedWorkflow.ui?.positions?.[step.id!] as
            | { x: number; y: number }
            | undefined;
          const layoutPosition = layoutPositions[step.id!] as
            | { x: number; y: number }
            | undefined;
          const position =
            savedPosition?.x !== undefined && savedPosition?.y !== undefined
              ? savedPosition
              : layoutPosition?.x !== undefined &&
                  layoutPosition?.y !== undefined
                ? layoutPosition
                : { x: 100, y: 100 };
          return {
            id: step.id!,
            type: getFlowNodeType(stepType),
            position,
            data: {
              step,
              label: step.id || "",
            },
          };
        });

        // Add the workflow start event node (use layout position when no saved position)
        const workflowNodePosition =
          (updatedWorkflow.ui?.positions?.["workflow"] as
            | { x: number; y: number }
            | undefined) ??
          (layoutPositions["workflow"] as { x: number; y: number } | undefined);
        const workflowNode: Node = {
          id: "workflow",
          type: "eventNode",
          position: workflowNodePosition
            ? {
                x: workflowNodePosition.x ?? 50,
                y: workflowNodePosition.y ?? 200,
              }
            : { x: 50, y: 200 },
          data: {
            eventType: "started",
            label: "Workflow Start",
          },
          draggable: canDragNodes,
          deletable: false,
        };
        const parentNodes = [workflowNode, ...parentStepNodes];

        // Convert edges
        const workflowEdges = updatedWorkflow.edges || [];
        const hasWorkflowStartEdge = workflowEdges.some(
          (e) => e.from === "workflow" || e.from === "started",
        );
        const edgesWithEntry = [...workflowEdges];
        if (!hasWorkflowStartEdge && updatedWorkflow.entry) {
          const entryNodes = Array.isArray(updatedWorkflow.entry)
            ? updatedWorkflow.entry
            : [updatedWorkflow.entry];
          for (const entryNode of entryNodes) {
            edgesWithEntry.push({
              from: "workflow",
              cases: [{ to: [entryNode] }],
            });
          }
        }
        const { edges: parentFlowEdges, switchNodes: parentSwitchNodes } =
          convertEdgesToFlowElements(
            edgesWithEntry,
            parentNodes,
            updatedWorkflow.ui?.switches || {},
            undefined,
            canDragNodes,
          );
        const allParentNodes = [...parentNodes, ...parentSwitchNodes];

        // Update the stack entry with the new parent workflow state
        setLoopEditStack((prev) => {
          const updated = [...prev];
          const lastIndex = updated.length - 1;
          updated[lastIndex] = {
            ...updated[lastIndex],
            parentWorkflow: updatedWorkflow,
            parentNodes: allParentNodes,
            parentEdges: parentFlowEdges,
          };
          return updated;
        });

        // When editing a loop, we don't update the canvas display (it shows the loop body)
        // Only update the stack so exiting the loop doesn't lose agent changes
        toast.success("Workflow updated by assistant", { duration: 2000 });
        return;
      }

      // Mark that we're applying agent updates - this prevents false modification detection
      // since the agent has already saved the workflow via edit_workflow
      isApplyingAgentUpdateRef.current = true;

      // Check if the workflow actually changed (compare node IDs)
      // This prevents showing toast on initial sync when restoring a chat
      const currentNodeIds = nodes
        .filter((n) => n.id !== "workflow" && !n.id.startsWith("switch-"))
        .map((n) => n.id)
        .sort();
      const incomingNodeIds = updatedWorkflow.nodes.map((n) => n.id).sort();
      const nodesChanged =
        JSON.stringify(currentNodeIds) !== JSON.stringify(incomingNodeIds);

      // Take snapshot for undo (only if there are actual changes)
      if (nodesChanged) {
        takeSnapshot(nodes, edges);
      }

      // Update workflow name and description
      if (updatedWorkflow.name !== workflowName) {
        setWorkflowName(updatedWorkflow.name || "");
      }
      if (updatedWorkflow.description !== workflowDescription) {
        setWorkflowDescription(updatedWorkflow.description || "");
      }

      // Update inputs
      if (updatedWorkflow.inputs) {
        setWorkflowInputs(
          updatedWorkflow.inputs || {},
        );
      }

      // Update entry and outputs so buildWorkflow() emits correct YAML (fixes wrong entry / missing outputs after assistant update)
      if (updatedWorkflow.entry !== undefined) {
        setWorkflowEntry(updatedWorkflow.entry);
      }
      if (updatedWorkflow.outputs !== undefined) {
        setWorkflowOutputs(updatedWorkflow.outputs);
      }

      // Update presets, thread, and apiVersion
      if (updatedWorkflow.presets) {
        setWorkflowTag(updatedWorkflow.presets.tag);
        setWorkflowPresetDefault(updatedWorkflow.presets.default);
      }
      // thread removed from Workflow proto
      if (updatedWorkflow.apiVersion) {
        setWorkflowApiVersion(updatedWorkflow.apiVersion);
      }

      // Convert workflow nodes to React Flow nodes with layout (use layout positions when assistant sends no positions)
      const workflowWithLayout = autoLayoutWorkflow(updatedWorkflow);
      const layoutPositions = workflowWithLayout.ui?.positions || {};
      // Filter out any nodes without a type field (defensive check for malformed data)
      const validChatNodes = (workflowWithLayout.nodes || []).filter((step) => {
        if (!step.type) {
          console.warn(
            "[WorkflowBuilder] Skipping chat-updated node without type field:",
            step.id || "unknown",
          );
          return false;
        }
        return true;
      });
      const stepNodes: Node[] = validChatNodes
        .filter((step) => step.id)
        .map((step) => {
          const stepType = getStepType(step);
          const savedPosition = updatedWorkflow.ui?.positions?.[step.id!] as
            | { x: number; y: number }
            | undefined;
          const layoutPosition = layoutPositions[step.id!] as
            | { x: number; y: number }
            | undefined;
          const position =
            savedPosition?.x !== undefined && savedPosition?.y !== undefined
              ? savedPosition
              : layoutPosition?.x !== undefined &&
                  layoutPosition?.y !== undefined
                ? layoutPosition
                : { x: 100, y: 100 };
          return {
            id: step.id!,
            type: getFlowNodeType(stepType), // Use proper mapping (activity types -> actionNode)
            position,
            data: {
              step,
              label: step.id || "",
            },
          };
        });

      // Add the workflow start event node (use layout position when no saved position)
      const workflowNodePos =
        (updatedWorkflow.ui?.positions?.["workflow"] as
          | { x: number; y: number }
          | undefined) ??
        (layoutPositions["workflow"] as { x: number; y: number } | undefined);
      const workflowNode: Node = {
        id: "workflow",
        type: "eventNode",
        position: workflowNodePos
          ? { x: workflowNodePos.x ?? 50, y: workflowNodePos.y ?? 200 }
          : { x: 50, y: 200 },
        data: {
          eventType: "started",
          label: "Workflow Start",
        },
        draggable: canDragNodes,
        deletable: false,
      };

      const newNodes = [workflowNode, ...stepNodes];

      // Convert edges using shared utility
      // Need to pass the new nodes as second argument for switch node generation

      // Create synthetic edges from entry field if no explicit workflow edges exist
      const workflowEdges = updatedWorkflow.edges || [];
      const hasWorkflowStartEdge = workflowEdges.some(
        (e) => e.from === "workflow" || e.from === "started",
      );

      const edgesWithEntry = [...workflowEdges];
      if (!hasWorkflowStartEdge && updatedWorkflow.entry) {
        const entryNodes = Array.isArray(updatedWorkflow.entry)
          ? updatedWorkflow.entry
          : [updatedWorkflow.entry];

        for (const entryNode of entryNodes) {
          edgesWithEntry.push({
            from: "workflow",
            cases: [{ to: [entryNode] }],
          });
        }
      }

      const { edges: flowEdges, switchNodes } = convertEdgesToFlowElements(
        edgesWithEntry,
        newNodes,
        updatedWorkflow.ui?.switches || {},
        undefined,
        canDragNodes,
      );

      // Include switch nodes for fan-out edges (one source -> multiple targets)
      const allNodes = [...newNodes, ...switchNodes];

      setNodes(allNodes);
      setEdges(flowEdges);

      // Update selected node/edge so the open ConfigPanel reflects the new data
      setSelectedNode((current) => {
        if (!current) return null;
        const updatedNode = allNodes.find((n) => n.id === current.id);
        return updatedNode ?? null;
      });
      setSelectedEdge((current) => {
        if (!current) return null;
        const updatedEdge = flowEdges.find((e) => e.id === current.id);
        return updatedEdge ?? null;
      });

      // Agent already saved the workflow, so mark as not modified
      setHasModifications(false);

      // Reset the agent update flag after React processes the state changes
      setTimeout(() => {
        isApplyingAgentUpdateRef.current = false;
      }, 0);

      // Only show toast if nodes actually changed (not on initial sync)
      if (nodesChanged) {
        toast.success("Workflow updated by assistant", { duration: 2000 });
      }
    },
    [
      nodes,
      edges,
      workflowName,
      workflowDescription,
      takeSnapshot,
      setNodes,
      setEdges,
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
    markDirty();
  }, [nodes, edges, takeSnapshot, markDirty]);

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
      markDirty();

      // Use the actual step type as the ID prefix (e.g., call_llm-123, run-456)
      const id = `${stepType}-${Date.now()}`;
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
        step = withRunArgs(step, { command: celString("") } as any);
      } else if (stepType === "workflow") {
        step = withWorkflowArgs(step, { ref: celString("builtin://agent") } as any);
      } else if (stepType === "agent") {
        // agent is not a structural type, no args oneof needed
      } else if (stepType === "join") {
        step.condition = directCel("all") as any;
      } else if (stepType === "loop") {
        step = withLoopArgs(step, { while: directCel(""), ref: celString("") } as any);
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
      setSelectedNode(newNode);
      setSelectedEdge(null);
      setShowSettingsEditor(false);
      // Close chat panel when config panel opens
      setChatPanelOpen(false);
    },
    [setNodes, nodes, edges, takeSnapshot, STRUCTURAL_TYPES, screenToFlowPosition, findNonOverlappingPosition, markDirty],
  );

  const addSwitch = useCallback(() => {
    takeSnapshot(nodes, edges);
    markDirty();

    const id = `switch-${Date.now()}`;
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
          { id: `case-${Date.now()}-1`, condition: "", label: "" }, // First case (will need condition)
          { id: `case-${Date.now()}-2`, condition: "", label: "" }, // Default case (last, no condition)
        ],
      },
      draggable: canDragNodes,
    };

    setNodes((nds) => nds.concat(newNode));
    // Auto-select the newly created switch to open config panel
    setSelectedNode(newNode);
    setSelectedEdge(null);
    setShowSettingsEditor(false);
    // Close chat panel when config panel opens
    setChatPanelOpen(false);
  }, [setNodes, nodes, edges, takeSnapshot, canDragNodes, screenToFlowPosition, findNonOverlappingPosition, markDirty]);

  return (
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
        <div className="bg-card border border-border rounded-xl shadow-lg p-3">
          <div className="text-sm space-y-1">
            <div>
              <span className="text-foreground font-medium">Steps:</span>{" "}
              <span className="text-muted-foreground">{nodes.length}</span>
            </div>
            <div>
              <span className="text-foreground font-medium">Connections:</span>{" "}
              <span className="text-muted-foreground">{edges.length}</span>
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
        <div className="bg-background p-4 flex items-center justify-between gap-4">
          <div className="flex items-center gap-3">
            {/* Back button - either exit loop edit or go back to workflows */}
            {isEditingLoop ? (
              <button
                onClick={() => exitLoopEdit(!isBuiltinWorkflow)}
                className="p-2 hover:bg-muted rounded-lg transition-colors"
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
                  className="p-2 hover:bg-muted rounded-lg transition-colors"
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
                  value={workflowName}
                  onChange={(e) => {
                    setWorkflowName(e.target.value);
                    markDirty();
                  }}
                  onBlur={() => {
                    // Apply normalization on blur
                    const normalized = normalizeName(workflowName);
                    setWorkflowName(normalized);
                    setIsEditingName(false);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      // Apply normalization on Enter
                      const normalized = normalizeName(workflowName);
                      setWorkflowName(normalized);
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
                    {workflowName}
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
                className="flex items-center gap-2 px-4 py-2 bg-secondary text-secondary-foreground rounded-lg hover:bg-secondary/90 transition-colors font-semibold"
              >
                Done
              </button>
            ) : isEditingLoop ? (
              // Loop editing mode - show Save Loop Body and Discard buttons
              <>
                <button
                  onClick={() => exitLoopEdit(false)}
                  className="px-4 py-2 bg-muted text-muted-foreground rounded-lg hover:bg-muted/80 hover:text-foreground transition-colors"
                >
                  Discard Changes
                </button>
                <button
                  onClick={() => exitLoopEdit(true)}
                  className="flex items-center gap-2 px-4 py-2 bg-secondary text-secondary-foreground rounded-lg hover:bg-secondary/90 transition-colors font-semibold"
                >
                  Apply Changes
                </button>
              </>
            ) : isBuiltinWorkflow ? (
              // Builtin workflow - show "Create a Copy" button
              <>
                <button
                  onClick={handleUseAsTemplate}
                  className="flex items-center gap-2 px-4 py-2 bg-secondary text-secondary-foreground rounded-lg hover:bg-secondary/90 transition-colors font-semibold"
                >
                  <Copy className="w-4 h-4" />
                  Create a Copy
                </button>
                <button
                  onClick={() => setShowYamlEditor(true)}
                  className="flex items-center gap-2 px-4 py-2 bg-muted text-muted-foreground rounded-lg hover:bg-muted/80 hover:text-foreground transition-colors"
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
                  className="flex items-center gap-2 px-4 py-2 bg-muted text-muted-foreground rounded-lg hover:bg-muted/80 hover:text-foreground transition-colors"
                >
                  <Code className="w-4 h-4" />
                  YAML
                </button>
                <button
                  onClick={() => setShowScenarioPanel(true)}
                  className="flex items-center gap-2 px-4 py-2 bg-muted text-muted-foreground rounded-lg hover:bg-muted/80 hover:text-foreground transition-colors"
                >
                  <TestTube2 className="w-4 h-4" />
                  Tests
                </button>
                <button
                  onClick={() => {
                    setSelectedNode(null);
                    setSelectedEdge(null);
                    setShowSettingsEditor(true);
                    setChatPanelOpen(false);
                  }}
                  className="flex items-center gap-2 px-4 py-2 bg-muted text-muted-foreground rounded-lg hover:bg-muted/80 hover:text-foreground transition-colors"
                >
                  <Settings2 className="w-4 h-4" />
                  Parameters
                </button>
                <button
                  onClick={handleUseAsTemplate}
                  className="flex items-center gap-2 px-4 py-2 bg-muted text-muted-foreground rounded-lg hover:bg-muted/80 hover:text-foreground transition-colors"
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
                  className="flex items-center gap-2 px-4 py-2 bg-primary text-primary-foreground rounded-lg hover:bg-primary/90 transition-colors font-semibold disabled:opacity-50 disabled:cursor-not-allowed"
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
            onInit={setReactFlowInstance}
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
                    markDirty();
                  }
            }
            onEdgesDelete={
              isBuiltinWorkflow
                ? undefined
                : () => {
                    // Take snapshot BEFORE ReactFlow deletes edges
                    takeSnapshot(nodes, edges);
                    markDirty();
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
              color="#a855f7"
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
                onLockToggle={() => setIsLocked((prev) => !prev)}
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
            {/* Config Panel - view-only for builtin workflows */}
            {selectedNode &&
              selectedNode.type !== "eventNode" &&
              selectedNode.type !== "switchNode" &&
              (selectedNode.data as FlowNodeData).step && (
                <ConfigPanel
                  key={selectedNode.id}
                  step={(selectedNode.data as FlowNodeData).step as Step}
                  onUpdate={handleStepUpdate}
                  onClose={() => setSelectedNode(null)}
                  onDelete={() => handleStepDelete(selectedNode.id)}
                  onEditLoopBody={enterLoopEdit}
                  onEditInlineWorkflowBody={enterWorkflowEdit}
                  onRename={handleNodeRename}
                  existingNodeIds={existingNodeIds}
                  bottomOffset={configPanelBottomOffset}
                  topOffset={configPanelTopOffset}
                  currentWorkflowName={workflowName}
                  isInLoop={isEditingLoop}
                  isReadOnly={isBuiltinWorkflow}
                />
              )}

            {/* Switch Config Panel - view-only for builtin workflows */}
            {selectedNode && selectedNode.type === "switchNode" && (
              <SwitchConfigPanel
                key={selectedNode.id}
                node={selectedNode}
                onUpdate={handleSwitchUpdate}
                onClose={() => setSelectedNode(null)}
                onDelete={() => handleStepDelete(selectedNode.id)}
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
                onUpdate={handleEdgeUpdate}
                onClose={() => setSelectedEdge(null)}
                onDelete={handleDeleteCase}
                bottomOffset={configPanelBottomOffset}
                topOffset={configPanelTopOffset}
                isReadOnly={isBuiltinWorkflow}
              />
            )}

            {/* Workflow Settings Editor - hidden for builtin workflows */}
            {!isBuiltinWorkflow && showSettingsEditor && (
              <WorkflowSettingsEditor
                params={workflowInputs}
                entry={workflowEntry}
                outputs={workflowOutputs}
                tag={workflowTag}
                thread={workflowThread}
                nodeIds={nodes.map((n) => n.id)}
                onUpdateParams={(p) => {
                  setWorkflowInputs(p);
                  markDirty();
                }}
                onUpdateEntry={(e) => {
                  setWorkflowEntry(e);
                  markDirty();
                }}
                onUpdateOutputs={(o) => {
                  setWorkflowOutputs(o);
                  markDirty();
                }}
                onUpdateTag={(t) => {
                  setWorkflowTag(t);
                  markDirty();
                }}
                onUpdateThread={(t) => {
                  setWorkflowThread(t);
                  markDirty();
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
              setSelectedNode(null);
              setSelectedEdge(null);
              setShowSettingsEditor(false);
            }
          }}
          panelSize={chatPanelSize}
          onPanelSizeChange={setChatPanelSize}
          builderChatId={builderChatId}
          draftId={draftId}
          workflowSessionId={workflowSessionId}
          isConfigPanelOpen={
            !!(selectedNode || selectedEdge || showSettingsEditor)
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
                Use lowercase letters, numbers, and hyphens
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
              {workflowName.trim()
                ? "You have unsaved changes. What would you like to do?"
                : "You have unsaved changes. Name your workflow to save it, or discard your changes."}
            </p>

            <div className="flex flex-col gap-2 pt-4 border-t border-border">
              {workflowName.trim() && (
                <Button
                  variant="primary"
                  onClick={handleSaveAndExit}
                  disabled={isSavingBeforeExit}
                >
                  {isSavingBeforeExit ? "Saving..." : "Save & Exit"}
                </Button>
              )}
              <Button
                variant={workflowName.trim() ? "outline" : "destructive"}
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
                {workflowName.trim() ? "Cancel" : "Keep Editing"}
              </Button>
            </div>
          </div>
        </Modal>
      )}

      {/* Workflow Info Popover */}
      <WorkflowInfoPopover
        isOpen={showInfoPopover}
        onClose={() => setShowInfoPopover(false)}
        description={workflowDescription}
        onDescriptionChange={
          source === "user"
            ? (desc: string) => {
                setWorkflowDescription(desc);
                markDirty();
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
              workflowSlug={workflowName}
              isReadOnly={isBuiltinWorkflow}
            />
          </div>
        </Modal>
      )}
    </div>
  );
}

export function WorkflowBuilder(props: WorkflowBuilderProps) {
  return (
    <ReactFlowProvider>
      <WorkflowBuilderInner {...props} />
    </ReactFlowProvider>
  );
}