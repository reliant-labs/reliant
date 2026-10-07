/**
 * WorkflowViewer - Read-only workflow visualization with execution status
 * 
 * A lightweight ReactFlow visualization that shows workflow structure
 * and highlights nodes based on execution state.
 * 
 * Uses the same node/edge components as WorkflowBuilder but in view-only mode.
 * 
 * Supports expandable loop nodes that show sub-workflow content inline.
 */

import { useMemo, useCallback, useState, useEffect, useRef } from 'react'
import {
  ReactFlow,
  Background,
  Controls,
  ReactFlowProvider,
  useReactFlow,
  useNodesInitialized,
  useStore,
  applyNodeChanges,
  type BackgroundVariant,
} from '@xyflow/react'
import type { Node } from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import './workflow-theme.css'
import { nodeTypes } from './nodes'
import { edgeTypes } from './edges'
import { workflowToFlowElements, mergeExpandedLoops, type FlowNodeData, type ExpandedLoopConfig } from '../../lib/workflow-flow'
import { structurallyEqual } from '../../lib/structuralEqual'
import type { Workflow, LoopStep, Step } from '../../types/workflow'
import { getStepRef, getStepInline, getStepParallel } from '../../types/workflow'
import type { WorkflowExecution, StepExecution } from '../Chat/ExecutionSidebar/types'
import { X, ArrowRightToLine, ArrowLeftToLine, ArrowDownToLine, ArrowUpToLine, Pencil, PanelBottom } from 'lucide-react'
import { NodeDetailsPanel } from './NodeDetailsPanel'
import { ActivityLog, type ActivityEvent } from './ActivityLog'
import {
  useExtendedExecutionStatus,
  findStepExecutionsForNode,
  findChildWorkflow,
  findLoopIterations,
  findLoopIterationSteps,
  findLoopChildStepExecutions,
  buildLoopChildStatus,
  type LoopChildStatus,
  type LoopIterationInfo,
} from './hooks/useExecutionStatus'
import {
  FOCUS_MIN_ZOOM,
  OVERVIEW_MIN_ZOOM,
  frameBounds,
  isInView,
  runningFocusNodeIds,
} from './viewerViewport'
import { useExpandedLoops } from './hooks/useExpandedLoops'
import { WorkflowNodeCallbacksProvider } from './WorkflowNodeCallbacksContext'
import { useNavigate } from '@tanstack/react-router'
import { useProjectStore } from '../../store/projectStore'
import { useWorktreeStore } from '../../store/worktreeStore'
import { workflowGrpc } from '../../api/workflow-grpc'
import { cn } from '../../lib/utils'

interface WorkflowViewerProps {
  /** The workflow definition to visualize */
  workflow: Workflow
  /** Current execution state (optional - for highlighting) */
  execution?: WorkflowExecution
  /** Project ID for fetching sub-workflows when expanding loops */
  projectId?: string
  /** Chat ID — connects the diagram to the authoritative node_execution stream */
  chatId?: string | null
  /** Workflow name (for editing - should match the identifier used to fetch the workflow) */
  workflowName?: string
  /** Callback when a node is clicked (for viewing details) */
  onNodeClick?: (nodeId: string, step?: StepExecution) => void
  /** Callback to close the viewer */
  onClose?: () => void
  /** Callback to drill into a sub-workflow */
  onViewSubWorkflow?: (childWorkflow: WorkflowExecution) => void
  /** Optional title override */
  title?: string
  /** Show mini-map (default: false) */
  showMiniMap?: boolean
  /** Compact mode - smaller size for embedding */
  compact?: boolean
  /** Hide the fullscreen/expand button */
  hideFullscreen?: boolean
  /**
   * Hide the title bar (name, status, edit pencil). For a host that already
   * names the workflow and offers Edit itself — the workflow detail page —
   * where a second header is a second way to do the same thing.
   */
  hideHeader?: boolean
  /** Hide the run-status legend: meaningless for a definition that is not running. */
  hideLegend?: boolean
  /** Current viewer mode (for inline/side toggle) */
  viewerMode?: 'inline' | 'side'
  /** Callback to toggle between inline and side panel modes */
  onToggleViewerMode?: () => void
  /** Callback when expanded state changes */
  onExpandedChange?: (expanded: boolean) => void
}

/** Selected node state for details panel */
interface SelectedNodeState {
  nodeId: string
  nodeData: FlowNodeData
  stepExecutions: StepExecution[]
  childWorkflow?: WorkflowExecution
  loopIterations?: WorkflowExecution[]       // From child workflows (legacy)
  loopIterationSteps?: LoopIterationInfo[]   // From step executions (inline loops)
}

function WorkflowViewerInner({
  workflow,
  execution,
  projectId,
  chatId,
  workflowName,
  onNodeClick,
  onClose,
  onViewSubWorkflow,
  title,
  showMiniMap: _showMiniMap = false,
  compact = false,
  hideFullscreen = false,
  hideHeader = false,
  hideLegend = false,
  viewerMode,
  onToggleViewerMode,
  onExpandedChange,
}: WorkflowViewerProps) {
  const { getNodes, getNodesBounds, getViewport, setViewport } = useReactFlow()
  const navigate = useNavigate()
  const currentProject = useProjectStore((state) => state.currentProject)
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree)
  
  // Track if we're currently saving positions (debounce)
  const savingPositionsRef = useRef(false)
  const saveTimeoutRef = useRef<NodeJS.Timeout | null>(null)
  const [isExpanded, setIsExpanded] = useState(false)
  const [selectedNode, setSelectedNode] = useState<SelectedNodeState | null>(null)
  const [isActivityLogExpanded, setIsActivityLogExpanded] = useState(false)

  // Create a unique key for this workflow (for workspace state) - must be defined before using it
  const workflowKey = useMemo(() => {
    // Use workflow name, or if we have execution, use execution ID for uniqueness
    const name = workflow.name || 'unnamed'
    return execution?.id ? `${execution.id}:${name}` : name
  }, [workflow.name, execution?.id])
  

  // Reset selection when workflow or execution changes
  // Also reset the restore flag when workflow changes
  useEffect(() => {
    setSelectedNode(null)
    hasRestoredRef.current = false // Reset restore flag when workflow changes
  }, [workflow.name, workflow.nodes, execution?.id])

  // Extract node IDs from workflow for status mapping
  const workflowNodeIds = useMemo(
    () => (workflow.nodes?.map(s => s.id).filter((id): id is string => !!id)) || [],
    [workflow]
  )

  // Build execution status map using the extended hook (includes loop info).
  // Node STATUS is authoritative from the node_execution stream (via chatId);
  // loop STRUCTURE stays tree-derived from `execution`.
  const {
    statusMap: executionStatus,
    loopInfo,
    loopScoped,
    latestSequence,
  } = useExtendedExecutionStatus(execution, workflowNodeIds, chatId)
  
  
  // Get loop iteration steps for expanded loops
  const allLoopIterationSteps = useMemo(() => {
    const result: Record<string, LoopIterationInfo[]> = {}
    for (const nodeId of workflowNodeIds) {
      const iterations = findLoopIterationSteps(execution, nodeId)
      if (iterations.length > 0) {
        result[nodeId] = iterations
      }
    }
    return result
  }, [execution, workflowNodeIds])

  // Expanded loops state management (with workspace persistence)
  const expandedLoopsHook = useExpandedLoops(
    projectId || currentProject?.id || '',
    currentWorktree?.id ?? null,
    workflowKey,
    execution,
    allLoopIterationSteps
  )

  // Restore expanded loops from workspace state when workflow loads
  // Only run once when workflow first loads, not on every render
  const hasRestoredRef = useRef(false)
  const lastWorkflowKeyRef = useRef<string>('')
  const expandLoopRef = useRef(expandedLoopsHook.expandLoop)
  
  // Keep ref updated with latest expandLoop function
  expandLoopRef.current = expandedLoopsHook.expandLoop
  
  useEffect(() => {
    // Reset restore flag if workflow key changed
    if (lastWorkflowKeyRef.current !== workflowKey) {
      hasRestoredRef.current = false
      lastWorkflowKeyRef.current = workflowKey
    }
    
    // Only restore once per workflow
    if (hasRestoredRef.current) return
    if (!projectId && !currentProject?.id) return
    
    const loopNodes = (workflow.nodes?.filter(node => node.type === 'loop') || []) as LoopStep[]
    if (loopNodes.length === 0) {
      hasRestoredRef.current = true
      return
    }

    // Auto-expand all loops by default
    // Loops are always open by default when viewing a workflow
    // Check if already expanded before expanding to prevent infinite loops
    const loopsToExpand = loopNodes.filter(loopNode => {
      if (!loopNode.id) return false
      if (!getStepRef(loopNode) && !getStepInline(loopNode)) return false
      // Check if already expanded
      return !expandedLoopsHook.expandedLoops.has(loopNode.id)
    })
    
    if (loopsToExpand.length === 0) {
      hasRestoredRef.current = true
      return
    }

    const expandPromises = loopsToExpand.map(loopNode => {
      const nodeId = loopNode.id!
      return expandLoopRef.current(nodeId, loopNode).catch((err) => {
        console.error('[WorkflowViewer] Failed to auto-expand loop:', nodeId, err)
      })
    })

    // Wait for all expansions to complete before marking as restored
    Promise.all(expandPromises).finally(() => {
      hasRestoredRef.current = true
    })
  }, [workflowKey, projectId, currentProject?.id, currentWorktree?.id, workflow.nodes, expandedLoopsHook.expandedLoops])

  // Convert workflow to ReactFlow elements with execution status and loop info
  const baseElements = useMemo(
    () => {
      const elements = workflowToFlowElements(workflow, {
        executionStatus,
        loopInfo,
        draggable: true, // Allow users to drag nodes to fix layout
      })
      
      return elements
    },
    [workflow, executionStatus, loopInfo]
  )
  
  
  // Live state of every expanded loop's body, per iteration: step rows for
  // what finished, the loop-scoped stream for what is running now.
  const loopChildStatusById = useMemo(() => {
    const result = new Map<string, LoopChildStatus>()
    for (const [nodeId, loopState] of expandedLoopsHook.expandedLoops) {
      if (!loopState.subWorkflow) continue
      const loopStep = workflow.nodes?.find((node) => node.id === nodeId) as LoopStep | undefined
      const parallel = loopStep ? getStepParallel(loopStep) : undefined
      result.set(
        nodeId,
        buildLoopChildStatus({
          workflowId: execution?.id,
          loopNodeId: nodeId,
          childNodeIds:
            loopState.subWorkflow.nodes?.map((s) => s.id).filter((id): id is string => !!id) ?? [],
          iterations: allLoopIterationSteps[nodeId] || [],
          loopScoped,
          loopIsRunning: executionStatus[nodeId] === 'running',
          latestSequence,
          parallel: parallel === true || typeof parallel === 'string',
        }),
      )
    }
    return result
  }, [
    expandedLoopsHook.expandedLoops,
    workflow.nodes,
    execution?.id,
    allLoopIterationSteps,
    loopScoped,
    executionStatus,
    latestSequence,
  ])

  // Follow the run into each new iteration as it starts — the stream knows a
  // new iteration began long before its first step row is written.
  const { followLatestIterations } = expandedLoopsHook
  const latestIterationByLoop = useMemo(() => {
    const latest: Record<string, number> = {}
    for (const [nodeId, status] of loopChildStatusById) {
      if (status.latestIteration !== undefined) latest[nodeId] = status.latestIteration
    }
    return latest
  }, [loopChildStatusById])
  useEffect(() => {
    followLatestIterations(latestIterationByLoop)
  }, [followLatestIterations, latestIterationByLoop])

  // Build expanded loop configs for merging
  const expandedLoopConfigs = useMemo((): ExpandedLoopConfig[] => {
    const configs: ExpandedLoopConfig[] = []

    for (const [nodeId, loopState] of expandedLoopsHook.expandedLoops) {
      if (!loopState.subWorkflow) continue

      const live = loopChildStatusById.get(nodeId)
      const selectedIter = loopState.selectedIteration
      const loopIsRunning = executionStatus[nodeId] === 'running'

      const childExecutionStatus: Record<string, import('../../lib/workflow-flow').NodeExecutionStatus> = {
        ...(live?.byIteration.get(selectedIter) ?? {}),
        // The sub-workflow's start node has always "run" once an iteration exists.
        workflow: 'completed',
      }

      const iterationStatuses = live?.iterationStatuses ?? []
      const reportedIterations = loopInfo[nodeId]?.completedIterations || 0
      const totalIters = Math.max(iterationStatuses.length, reportedIterations, loopIsRunning ? 1 : 0)

      configs.push({
        loopNodeId: nodeId,
        subWorkflow: loopState.subWorkflow,
        childExecutionStatus,
        loopIsRunning,
        selectedIteration: selectedIter,
        totalIterations: totalIters,
        iterationStatuses: iterationStatuses.length > 0 ? iterationStatuses :
          // If no iteration data yet but loop is running, show one running tab
          (loopIsRunning ? ['running' as const] : []),
      })
    }

    return configs
  }, [
    expandedLoopsHook.expandedLoops,
    loopChildStatusById,
    executionStatus,
    loopInfo
  ])
  
  // Merge expanded loops into base elements
  const { nodes: baseNodes, edges } = useMemo(
    () => mergeExpandedLoops(baseElements, expandedLoopConfigs),
    [baseElements, expandedLoopConfigs]
  )
  
  // Use state for nodes so we can update positions when dragged
  // Initialize from baseNodes, but only update when layout direction changes
  const [nodes, setNodesState] = useState<Node<FlowNodeData>[]>(baseNodes)
  
  // Track if user is currently dragging - prevents merge effect from running
  const isDraggingRef = useRef(false)
  
  // Ref to track current nodes during drags (to avoid stale closures)
  const nodesRef = useRef(nodes)
  
  // Keep ref in sync with nodes state
  useEffect(() => {
    nodesRef.current = nodes
  }, [nodes])
  
  // Track previous baseNodes to detect changes
  const prevBaseNodesRef = useRef(baseNodes)
  
  // Update nodes when baseNodes changes (e.g., loops expanded/collapsed)
  // But preserve user-dragged positions and don't interfere with dragging
  useEffect(() => {
    // CRITICAL: Don't run ANY update if user is dragging
    if (isDraggingRef.current) {
      prevBaseNodesRef.current = baseNodes
      return
    }
    
    const prevBaseNodes = prevBaseNodesRef.current
    const currentNodes = nodesRef.current
    
    // Check if structure changed (nodes added/removed or types changed)
    const structureChanged = 
      baseNodes.length !== prevBaseNodes.length || 
      baseNodes.some((n) => {
        const oldNode = prevBaseNodes.find(p => p.id === n.id)
        return !oldNode || oldNode.type !== n.type
      })
    
    if (structureChanged) {
      // Merge baseNodes with current positions to preserve user-dragged positions
      const positionMap = new Map(currentNodes.map(n => [n.id, n.position]))
      const mergedNodes = baseNodes.map(baseNode => ({
        ...baseNode,
        position: positionMap.get(baseNode.id) || baseNode.position,
      }))
      
      setNodesState(mergedNodes)
      nodesRef.current = mergedNodes
      prevBaseNodesRef.current = baseNodes
    } else {
      // Only data changed (execution status, etc.) - update data but preserve positions
      const dataChanged = baseNodes.some((baseNode) => {
        const oldNode = prevBaseNodes.find(p => p.id === baseNode.id)
        if (!oldNode) return false
        // Not JSON.stringify: data carries the step proto, whose int64
        // fields are bigints.
        return (
          oldNode.data?.executionStatus !== baseNode.data?.executionStatus ||
          !structurallyEqual(oldNode.data, baseNode.data)
        )
      })
      
      if (dataChanged) {
        const baseNodesMap = new Map(baseNodes.map(n => [n.id, n]))
        const updatedNodes = currentNodes.map(node => {
          const baseNode = baseNodesMap.get(node.id)
          if (!baseNode) return node
          return {
            ...baseNode,
            position: node.position, // Always preserve current position
          }
        })
        
        setNodesState(updatedNodes)
        nodesRef.current = updatedNodes
        prevBaseNodesRef.current = baseNodes
      }
    }
  }, [baseNodes])
  
  // Handle node position changes (when user drags)
  const handleNodesChange = useCallback((changes: any[]) => {
    // Process changes and track drag state
    let hasDragging = false
    let hasDragEnd = false
    
    // Check for drag state changes
    changes.forEach((change) => {
      if (change.type === 'position') {
        if (change.dragging === true) {
          hasDragging = true
          isDraggingRef.current = true
        } else if (change.dragging === false) {
          hasDragEnd = true
        }
      }
    })
    
    // Update state using functional update to always get latest nodes
    // In controlled mode, we only update the nodes prop, not ReactFlow's internal state
    setNodesState((currentNodes) => {
      // Apply changes using ReactFlow's applyNodeChanges utility
      const updatedNodes = applyNodeChanges(changes, currentNodes)
      
      // Update ref for next change
      nodesRef.current = updatedNodes
      
      return updatedNodes
    })
    
    // When drag ends, mark dragging as complete after a short delay
    if (hasDragEnd && !hasDragging) {
      setTimeout(() => {
        isDraggingRef.current = false
      }, 300)
    }
    
    // Debounce saving positions to backend
    if (saveTimeoutRef.current) {
      clearTimeout(saveTimeoutRef.current)
    }
    
    saveTimeoutRef.current = setTimeout(() => {
      // Only save if we have a project and workflow name
      if (!projectId || !workflow.name || savingPositionsRef.current) return
      
      // Check if any position actually changed
      const positionChanges = changes.filter(
        (c) => c.type === 'position' && c.dragging === false
      )
      
      if (positionChanges.length === 0) return
      
      savingPositionsRef.current = true
      
      // Get current node positions
      const currentNodes = getNodes()
      // Convert proto positions to plain objects
      const existingPositions = workflow.ui?.positions || {}
      const updatedPositions: Record<string, { x: number; y: number }> = {}
      for (const [key, pos] of Object.entries(existingPositions)) {
        if (pos && typeof pos.x === 'number' && typeof pos.y === 'number') {
          updatedPositions[key] = { x: pos.x, y: pos.y }
        }
      }
      
      // Update positions for nodes that were moved
      positionChanges.forEach((change) => {
        const node = currentNodes.find((n) => n.id === change.id)
        if (node && change.position) {
          // Save positions for all nodes (including child nodes in expanded loops)
          // Use the node ID directly (child nodes have prefixed IDs like "loopId:nodeId")
          updatedPositions[node.id] = change.position
        }
      })
      
      // Update workflow with new positions
      // Cast to unknown first to avoid proto type conflicts
      const updatedWorkflow = {
        ...workflow,
        ui: {
          ...workflow.ui,
          positions: updatedPositions,
        },
      } as Workflow
      
      // Save to backend
      workflowGrpc.saveWorkflow(projectId, updatedWorkflow)
        .catch(() => {})
        .finally(() => {
          savingPositionsRef.current = false
        })
    }, 1000) // Debounce for 1 second
  }, [projectId, workflow, getNodes, setNodesState])
  
  // Cleanup timeout on unmount
  useEffect(() => {
    return () => {
      if (saveTimeoutRef.current) {
        clearTimeout(saveTimeoutRef.current)
      }
    }
  }, [])
  
  // Typed callback for LoopNode expand button (Wave-6a: was a document
  // CustomEvent listener). Wired up via WorkflowNodeCallbacksProvider below.
  const handleLoopExpand = useCallback(
    (loopNodeId: string, step: LoopStep) => {
      if (!projectId) {
        console.warn('[WorkflowViewer] Cannot expand loop: projectId is missing')
        return
      }
      expandedLoopsHook.expandLoop(loopNodeId, step).catch((err: unknown) => {
        console.error('[WorkflowViewer] Error expanding loop:', err)
      })
    },
    [projectId, expandedLoopsHook],
  )

  // Remaining DOM listeners (loop collapse via data-attr, iteration change)
  // are out of scope for the CustomEvent purge — they're not `loop-expand`.
  useEffect(() => {
    const handleLoopCollapse = (e: MouseEvent) => {
      const target = e.target as HTMLElement
      const collapseButton = target.closest('[data-collapse-loop]')
      if (collapseButton) {
        const loopNodeId = collapseButton.getAttribute('data-collapse-loop')
        if (loopNodeId) {
          expandedLoopsHook.collapseLoop(loopNodeId)
        }
      }
    }

    const handleIterationChange = (e: CustomEvent<{ loopNodeId: string; iteration: number }>) => {
      expandedLoopsHook.setSelectedIteration(e.detail.loopNodeId, e.detail.iteration)
    }

    document.addEventListener('click', handleLoopCollapse)
    document.addEventListener('loop-iteration-change', handleIterationChange as EventListener)

    return () => {
      document.removeEventListener('click', handleLoopCollapse)
      document.removeEventListener('loop-iteration-change', handleIterationChange as EventListener)
    }
  }, [expandedLoopsHook])
  
  // Handle node click - open details panel
  const handleNodeClick = useCallback(
    (_event: React.MouseEvent, node: Node<FlowNodeData>) => {
      // Don't open sidebar if clicking on an expanded loop node (they have their own controls)
      if (node.type === 'expandedLoopNode') {
        return
      }
      
      // Handle loop node clicks - expand them
      if (node.type === 'loopNode') {
        const step = node.data.step as LoopStep
        const canExpand = !!(getStepRef(step) || getStepInline(step))
        if (canExpand && projectId) {
          expandedLoopsHook.expandLoop(node.id, step).catch(() => {})
        }
        return
      }
      
      // A node inside an expanded loop is drawn as "<loop>:<node>" and its
      // steps are recorded against that loop and an iteration — show the
      // iteration the loop is displaying.
      if (node.parentId && node.id.startsWith(`${node.parentId}:`)) {
        const loopState = expandedLoopsHook.expandedLoops.get(node.parentId)
        const childNodeIds =
          loopState?.subWorkflow?.nodes?.map((s) => s.id).filter((id): id is string => !!id) ?? []
        const stepExecutions = findLoopChildStepExecutions(
          execution,
          node.parentId.split(':').pop()!,
          node.id.slice(node.parentId.length + 1),
          childNodeIds,
          loopState?.selectedIteration,
        )
        setSelectedNode({ nodeId: node.id, nodeData: node.data, stepExecutions })
        onNodeClick?.(node.id, stepExecutions[0])
        return
      }

      const stepExecutions = findStepExecutionsForNode(execution, node.id, workflowNodeIds)
      const childWorkflow = findChildWorkflow(execution, node.id)
      const loopIterations = findLoopIterations(execution, node.id)
      // For inline loops: get iterations from step executions
      const loopIterationSteps = findLoopIterationSteps(execution, node.id)
      
      setSelectedNode({
        nodeId: node.id,
        nodeData: node.data,
        stepExecutions,
        childWorkflow,
        loopIterations: loopIterations.length > 0 ? loopIterations : undefined,
        loopIterationSteps: loopIterationSteps.length > 0 ? loopIterationSteps : undefined,
      })
      
      // Also call external callback if provided
      if (onNodeClick) {
        onNodeClick(node.id, stepExecutions[0])
      }
    },
    [onNodeClick, execution, workflowNodeIds, projectId, expandedLoopsHook]
  )
  
  // Close details panel
  const handleCloseDetails = useCallback(() => {
    setSelectedNode(null)
  }, [])

  // Handle activity log event click - select the node with the specific event data
  const handleActivityEventClick = useCallback(
    (event: ActivityEvent) => {
      const { nodeId, stepExecution, childWorkflow: eventChildWorkflow } = event

      // Find the node in the flow - try exact match first, then try to find by stepId
      let node = nodes.find(n => n.id === nodeId)

      // If node not found and we have a stepExecution, try to find by stepId
      if (!node && stepExecution) {
        const stepId = stepExecution.stepId
        // Try direct match
        node = nodes.find(n => n.id === stepId)
        // Try prefix match (e.g., stepId might be "nodeId-save" but nodeId is "nodeId")
        if (!node) {
          node = nodes.find(n => stepId.startsWith(n.id + '-') || stepId.startsWith(n.id + '_'))
        }
      }

      // If still no node found, try to create a minimal node data from the step execution
      let nodeData: FlowNodeData
      if (node) {
        nodeData = node.data
      } else {
        // Create minimal node data from step execution
        nodeData = {
          step: stepExecution ? { id: stepExecution.stepId } as Step : { id: nodeId } as Step,
          label: stepExecution?.stepId || nodeId,
        }
      }

      // Use the step execution from the event if available, otherwise find all step executions for the node
      const stepExecutions = stepExecution 
        ? [stepExecution]
        : findStepExecutionsForNode(execution, nodeId, workflowNodeIds)

      // Use the child workflow from the event if available, otherwise find it
      const childWorkflow = eventChildWorkflow || findChildWorkflow(execution, nodeId)

      const loopIterations = findLoopIterations(execution, nodeId)
      const loopIterationSteps = findLoopIterationSteps(execution, nodeId)

      setSelectedNode({
        nodeId: node?.id || nodeId,
        nodeData,
        stepExecutions,
        childWorkflow,
        loopIterations: loopIterations.length > 0 ? loopIterations : undefined,
        loopIterationSteps: loopIterationSteps.length > 0 ? loopIterationSteps : undefined,
      })
    },
    [nodes, execution, workflowNodeIds]
  )

  // --- Camera -------------------------------------------------------------
  // Frame the graph so it can be READ (see viewerViewport.ts): the running
  // step while a run is live, otherwise the graph from its start. Re-framed
  // when the layout or pane size changes, and scrolled to a newly running step
  // only if it is off screen. Once the user pans or zooms, the camera is
  // theirs until a different workflow or run is shown.
  const nodesInitialized = useNodesInitialized()
  const paneWidth = useStore((state) => state.width)
  const paneHeight = useStore((state) => state.height)
  const userMovedViewportRef = useRef(false)
  const lastLayoutKeyRef = useRef<string | null>(null)
  const focusKey = useMemo(() => runningFocusNodeIds(nodes).join('|'), [nodes])
  const layoutKey = `${workflowKey}|${nodes.length}|${paneWidth}x${paneHeight}`

  useEffect(() => {
    userMovedViewportRef.current = false
    lastLayoutKeyRef.current = null
  }, [workflowKey])

  useEffect(() => {
    if (!nodesInitialized || paneWidth === 0 || paneHeight === 0) return
    if (userMovedViewportRef.current) return
    const pane = { width: paneWidth, height: paneHeight }
    const focusIds = focusKey ? focusKey.split('|') : []
    const isFirstFrame = lastLayoutKeyRef.current === null

    if (lastLayoutKeyRef.current !== layoutKey) {
      lastLayoutKeyRef.current = layoutKey
      const viewport = focusIds.length > 0
        ? frameBounds(getNodesBounds(focusIds), pane, { minZoom: FOCUS_MIN_ZOOM })
        : frameBounds(getNodesBounds(getNodes()), pane, { minZoom: OVERVIEW_MIN_ZOOM })
      void setViewport(viewport, { duration: isFirstFrame ? 0 : 200 })
      return
    }

    if (focusIds.length === 0) return
    const target = getNodesBounds(focusIds)
    const current = getViewport()
    if (current.zoom >= FOCUS_MIN_ZOOM && isInView(target, current, pane)) return
    void setViewport(frameBounds(target, pane, { minZoom: FOCUS_MIN_ZOOM }), { duration: 400 })
  }, [nodesInitialized, paneWidth, paneHeight, focusKey, layoutKey, getNodes, getNodesBounds, getViewport, setViewport])

  const handleUserMove = useCallback((event: MouseEvent | TouchEvent | null) => {
    // Programmatic moves (setViewport above) carry no event.
    if (event) userMovedViewportRef.current = true
  }, [])
  const handleUserZoomControl = useCallback(() => {
    userMovedViewportRef.current = true
  }, [])

  const displayTitle = title || workflow.name || 'Workflow'

  // Height class: compact mode uses fixed height, otherwise fills parent
  // When expanded, we'll render in a portal with fixed positioning
  const heightClass = compact 
    ? 'h-64' 
    : 'h-full'

  // Content to render - same whether expanded or not
  const content = (
    <WorkflowNodeCallbacksProvider onExpandLoop={handleLoopExpand}>
    <div className={`flex bg-background overflow-hidden ${heightClass}`}>
      {/* Main content area */}
      <div className="flex-1 flex flex-col min-w-0 min-h-0">
        {/* Header - aligned with chat header (inline) or right sidebar (side) */}
        {!hideHeader && (
        <div className={`flex items-center justify-between border-b border-border bg-muted/50 ${viewerMode === 'side' ? 'h-10 px-3' : 'px-4 sm:px-6 lg:px-8 py-2 border-t border-border'}`}>
          <div className={`w-full flex items-center justify-between ${viewerMode === 'side' ? '' : 'max-w-[1200px] mx-auto'}`}>
            <div className="flex items-center gap-2">
              <h3 className="text-sm font-medium text-foreground">{displayTitle}</h3>
              {execution && (
                <span className={cn(
                  'text-xs px-2 py-0.5 rounded-full font-medium',
                  execution.status === 'running'
                    ? 'bg-info/15 text-info'
                    : execution.status === 'completed'
                      ? 'bg-success/15 text-success-ink'
                      : execution.status === 'failed'
                        ? 'bg-destructive/15 text-destructive-ink'
                        : 'bg-muted text-muted-foreground',
                )}>
                  {execution.status}
                </span>
              )}
            </div>
            <div className="flex items-center gap-1">
            {(workflowName || workflow.name) && (
              <button
                onClick={() => {
                  const name = workflowName || workflow.name;
                  if (!name) return;
                  navigate({
                    to: '/workflow/$workflowName',
                    params: { workflowName: name },
                  });
                }}
                className="p-1 hover:bg-muted rounded"
                title="Edit in Workflow Builder"
              >
                <Pencil className="w-4 h-4 text-muted-foreground" />
              </button>
            )}
            {!hideFullscreen && !compact && (
              <button
                onClick={() => {
                  const newExpanded = !isExpanded
                  setIsExpanded(newExpanded)
                  onExpandedChange?.(newExpanded)
                }}
                className="p-1 hover:bg-muted rounded"
                title={isExpanded ? 'Minimize' : 'Maximize'}
              >
                {viewerMode === 'side' ? (
                  isExpanded ? (
                    <ArrowLeftToLine className="w-4 h-4 text-muted-foreground" />
                  ) : (
                    <ArrowRightToLine className="w-4 h-4 text-muted-foreground" />
                  )
                ) : (
                  isExpanded ? (
                    <ArrowUpToLine className="w-4 h-4 text-muted-foreground" />
                  ) : (
                    <ArrowDownToLine className="w-4 h-4 text-muted-foreground" />
                  )
                )}
              </button>
            )}
            {/* Toggle between inline and side panel modes */}
            {onToggleViewerMode && viewerMode && (
              <button
                onClick={onToggleViewerMode}
                className="p-1 hover:bg-muted rounded"
                title={
                  viewerMode === 'side' 
                    ? 'Switch to inline view (above chat)' 
                    : 'Switch to side panel view (beside chat)'
                }
              >
                {viewerMode === 'side' ? (
                  <PanelBottom className="w-4 h-4 text-muted-foreground" style={{ transform: 'rotate(180deg)' }} />
                ) : (
                  <PanelBottom className="w-4 h-4 text-muted-foreground" style={{ transform: 'rotate(-90deg)' }} />
                )}
              </button>
            )}
            {onClose && (
              <button
                onClick={onClose}
                className="p-1 hover:bg-muted rounded"
                title="Close"
              >
                <X className="w-4 h-4 text-muted-foreground" />
              </button>
            )}
            </div>
          </div>
        </div>
        )}

        {/* ReactFlow Canvas - full width */}
        <div className="flex-1 min-h-0 h-full w-full relative">
          <ReactFlow
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            edgeTypes={edgeTypes}
            onNodeClick={handleNodeClick}
            onPaneClick={handleCloseDetails}
            onNodesChange={handleNodesChange}
            onMoveStart={handleUserMove}
            fitViewOptions={{ padding: 0.2 }}
            nodesDraggable={true}
            nodesConnectable={false}
            elementsSelectable={true}
            panOnDrag={true} // Pan with left mouse on empty space, drag nodes when clicking on them
            panOnScroll={true} // Pan with scroll wheel when holding spacebar or shift
            zoomOnScroll={true}
            zoomOnPinch={true}
            minZoom={0.1}
            maxZoom={2}
            proOptions={{ hideAttribution: true }}
          >
            {/* Background grid pattern - subtle dots for visual reference */}
            <Background 
              color="hsl(var(--muted-foreground) / 0.4)" 
              gap={24}
              size={2.5}
              variant={"dots" as BackgroundVariant}
            />
            {!compact && (
              <Controls
                showInteractive={false}
                onZoomIn={handleUserZoomControl}
                onZoomOut={handleUserZoomControl}
                onFitView={handleUserZoomControl}
              />
            )}
          </ReactFlow>
          
          {/* Node Details Panel - positioned absolutely over the canvas */}
          {selectedNode && (
            <div className="absolute top-0 right-0 h-full z-50">
              <NodeDetailsPanel
                nodeId={selectedNode.nodeId}
                nodeData={selectedNode.nodeData}
                stepExecutions={selectedNode.stepExecutions}
                childWorkflow={selectedNode.childWorkflow}
                loopIterations={selectedNode.loopIterations}
                loopIterationSteps={selectedNode.loopIterationSteps}
                onClose={handleCloseDetails}
                onViewSubWorkflow={onViewSubWorkflow}
                viewerMode={viewerMode}
              />
            </div>
          )}
        </div>

        {/* Status Legend */}
        {!hideLegend && (
        <div className="flex items-center gap-4 px-3 py-2 border-t border-border bg-muted/30 text-xs">
          <div className="flex items-center gap-1.5">
            <div className="w-3 h-3 rounded border-2 border-border bg-background" />
            <span className="text-muted-foreground">Pending</span>
          </div>
          <div className="flex items-center gap-1.5">
            <div className="w-3 h-3 rounded border-2 border-info bg-info/15 animate-pulse" />
            <span className="text-muted-foreground">Running</span>
          </div>
          <div className="flex items-center gap-1.5">
            <div className="w-3 h-3 rounded border-2 border-success bg-success/15" />
            <span className="text-muted-foreground">Completed</span>
          </div>
          <div className="flex items-center gap-1.5">
            <div className="w-3 h-3 rounded border-2 border-destructive bg-destructive/15" />
            <span className="text-muted-foreground">Failed</span>
          </div>
        </div>
        )}
        
        {/* Activity Log (collapsible) */}
        {execution && (
          <ActivityLog
            execution={execution}
            highlightedNodeId={selectedNode?.nodeId}
            onEventClick={handleActivityEventClick}
            isExpanded={isActivityLogExpanded}
            onToggleExpand={() => setIsActivityLogExpanded(!isActivityLogExpanded)}
            workflowNodeIds={workflowNodeIds}
          />
        )}
      </div>
    </div>
    </WorkflowNodeCallbacksProvider>
  )

  // Normal rendering - parent container controls dimensions when expanded
  return content
}

/**
 * WorkflowViewer with ReactFlowProvider wrapper
 */
export function WorkflowViewer(props: WorkflowViewerProps) {
  return (
    <ReactFlowProvider>
      <WorkflowViewerInner {...props} />
    </ReactFlowProvider>
  )
}