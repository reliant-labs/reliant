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
  applyNodeChanges,
  type BackgroundVariant,
} from '@xyflow/react'
import type { Node } from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import './workflow-theme.css'
import { nodeTypes } from './nodes'
import { edgeTypes } from './edges'
import { workflowToFlowElements, mergeExpandedLoops, type FlowNodeData, type ExpandedLoopConfig } from '../../lib/workflow-flow'
import type { Workflow, LoopStep, Step } from '../../types/workflow'
import { getStepRef, getStepInline } from '../../types/workflow'
import type { WorkflowExecution, StepExecution } from '../Chat/ExecutionSidebar/types'
import { X, ArrowRightToLine, ArrowLeftToLine, ArrowDownToLine, ArrowUpToLine, Pencil, PanelBottom } from 'lucide-react'
import { NodeDetailsPanel } from './NodeDetailsPanel'
import { ActivityLog, type ActivityEvent } from './ActivityLog'
import { useExtendedExecutionStatus, findStepExecutionsForNode, findChildWorkflow, findLoopIterations, findLoopIterationSteps, type LoopIterationInfo } from './hooks/useExecutionStatus'
import { useExpandedLoops } from './hooks/useExpandedLoops'
import { useNavigate } from '@tanstack/react-router'
import { useProjectStore } from '../../store/projectStore'
import { useWorktreeStore } from '../../store/worktreeStore'
import { workflowGrpc } from '../../api/workflow-grpc'

interface WorkflowViewerProps {
  /** The workflow definition to visualize */
  workflow: Workflow
  /** Current execution state (optional - for highlighting) */
  execution?: WorkflowExecution
  /** Project ID for fetching sub-workflows when expanding loops */
  projectId?: string
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
  workflowName,
  onNodeClick,
  onClose,
  onViewSubWorkflow,
  title,
  showMiniMap: _showMiniMap = false,
  compact = false,
  hideFullscreen = false,
  viewerMode,
  onToggleViewerMode,
  onExpandedChange,
}: WorkflowViewerProps) {
  const { fitView, getNodes } = useReactFlow()
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

  // Build execution status map using the extended hook (includes loop info)
  const { statusMap: executionStatus, loopInfo } = useExtendedExecutionStatus(execution, workflowNodeIds)
  
  
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
  
  
  // Build expanded loop configs for merging
  const expandedLoopConfigs = useMemo((): ExpandedLoopConfig[] => {
    const configs: ExpandedLoopConfig[] = []
    
    for (const [nodeId, loopState] of expandedLoopsHook.expandedLoops) {
      if (!loopState.subWorkflow) continue
      
      // Get iteration info for this loop
      // iterationSteps is an array of LoopIterationInfo, each with an .iteration property
      const iterationSteps = allLoopIterationSteps[nodeId] || []
      const selectedIter = loopState.selectedIteration
      
      // If loop is running, prefer the currently running iteration for highlighting
      const currentLoopInfo = loopInfo[nodeId]
      const currentRunningIter = currentLoopInfo?.currentIteration
      const isLoopRunning = executionStatus[nodeId] === 'running'
      
      // Use running iteration if loop is running and we have one, otherwise use selected
      const iterToUse = (isLoopRunning && currentRunningIter !== undefined) ? currentRunningIter : selectedIter
      
      // Find the iteration data by iteration number (not array index)
      // The iterToUse is 0-indexed in our UI
      const selectedIterData = iterationSteps.find(iter => iter.iteration === iterToUse)
      const selectedIterSteps = selectedIterData?.steps || []
      
      // Build child execution status from selected iteration's steps
      // Get the sub-workflow node IDs for matching (filter out undefined)
      const subWorkflowStepIds = new Set(
        (loopState.subWorkflow.nodes?.map(s => s.id).filter((id): id is string => !!id)) || []
      )
      
      const childExecutionStatus: Record<string, import('../../lib/workflow-flow').NodeExecutionStatus> = {}

      for (const step of selectedIterSteps) {
        // The stepId in step execution should match sub-workflow step IDs directly
        // But it might have suffixes like -save, _0, etc.
        let matchedNodeId: string | null = null
        
        // Try exact match first
        if (subWorkflowStepIds.has(step.stepId)) {
          matchedNodeId = step.stepId
        } else {
          // Try to find a matching prefix (more aggressive matching)
          for (const subStepId of subWorkflowStepIds) {
            // Check various patterns: prefix with -, _, or exact match
            if (step.stepId.startsWith(subStepId + '-') || 
                step.stepId.startsWith(subStepId + '_') ||
                step.stepId === subStepId ||
                // Also try reverse: subStepId might be a prefix of stepId
                subStepId.startsWith(step.stepId + '-') ||
                subStepId.startsWith(step.stepId + '_')) {
              matchedNodeId = subStepId
              break
            }
          }
          
          // If still no match, try base name extraction
          if (!matchedNodeId) {
            const baseName = step.stepId.split('-')[0].split('_')[0]
            if (subWorkflowStepIds.has(baseName)) {
              matchedNodeId = baseName
            }
          }
          
          // If still no match, try removing common suffixes
          if (!matchedNodeId) {
            // Try removing common suffixes like -save, -result, etc.
            const withoutSuffix = step.stepId.replace(/-(save|result|output|input)$/i, '')
            if (subWorkflowStepIds.has(withoutSuffix)) {
              matchedNodeId = withoutSuffix
            }
          }
          
          // If still no match, try fuzzy matching - check if any part of stepId matches
          if (!matchedNodeId) {
            const stepIdParts = step.stepId.split(/[-_]/)
            for (const part of stepIdParts) {
              if (subWorkflowStepIds.has(part)) {
                matchedNodeId = part
                break
              }
            }
          }
          
          // Last resort: try substring matching (stepId contains nodeId or vice versa)
          if (!matchedNodeId) {
            for (const subStepId of subWorkflowStepIds) {
              if (step.stepId.includes(subStepId) || subStepId.includes(step.stepId)) {
                matchedNodeId = subStepId
                break
              }
            }
          }
        }
        
        if (matchedNodeId) {
          const currentStatus = childExecutionStatus[matchedNodeId]
          const newStatus = step.status as import('../../lib/workflow-flow').NodeExecutionStatus
          
          // Priority: running > failed > completed > pending
          if (!currentStatus) {
            childExecutionStatus[matchedNodeId] = newStatus
          } else if (newStatus === 'running') {
            childExecutionStatus[matchedNodeId] = 'running'
          } else if (newStatus === 'failed' && currentStatus !== 'running') {
            childExecutionStatus[matchedNodeId] = 'failed'
          }
          // Keep completed if nothing higher priority
        }
      }

      // Also mark 'workflow' as completed for the sub-workflow start node
      childExecutionStatus['workflow'] = 'completed'
      
      // Get iteration statuses for display
      // Build an array where index i corresponds to iteration number i
      // Find the max iteration number to size the array
      const maxIterNum = iterationSteps.reduce((max, iter) => Math.max(max, iter.iteration), -1)
      const iterationStatuses: import('../../lib/workflow-flow').NodeExecutionStatus[] = []
      
      for (let i = 0; i <= maxIterNum; i++) {
        const iterData = iterationSteps.find(iter => iter.iteration === i)
        if (iterData) {
          if (iterData.steps.some(s => s.status === 'failed')) {
            iterationStatuses.push('failed')
          } else if (iterData.steps.some(s => s.status === 'running')) {
            iterationStatuses.push('running')
          } else if (iterData.steps.every(s => s.status === 'completed')) {
            iterationStatuses.push('completed')
          } else {
            iterationStatuses.push('pending')
          }
        } else {
          iterationStatuses.push('pending')
        }
      }
      
      // Calculate total iterations - use max of:
      // 1. Size of iterationStatuses array (maxIterNum + 1)
      // 2. Completed iterations from loopInfo
      // 3. Current iteration + 1 if loop is running
      const totalIterLoopInfo = loopInfo[nodeId]
      const reportedIterations = totalIterLoopInfo?.completedIterations || 0
      const currentIteration = totalIterLoopInfo?.currentIteration
      
      // If loop is running and we have a current iteration, that's our count
      const runningCount = currentIteration !== undefined ? currentIteration + 1 : 0
      const totalIters = Math.max(
        iterationStatuses.length,  // Based on actual iteration data
        reportedIterations, 
        runningCount, 
        executionStatus[nodeId] === 'running' ? 1 : 0
      )
      
      configs.push({
        loopNodeId: nodeId,
        subWorkflow: loopState.subWorkflow,
        childExecutionStatus,
        loopIsRunning: executionStatus[nodeId] === 'running',
        selectedIteration: selectedIter,
        totalIterations: totalIters,
        iterationStatuses: iterationStatuses.length > 0 ? iterationStatuses : 
          // If no iteration data yet but loop is running, show one pending tab
          (executionStatus[nodeId] === 'running' ? ['running' as const] : []),
      })
    }
    
    return configs
  }, [
    expandedLoopsHook.expandedLoops,
    allLoopIterationSteps, 
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
        return (
          oldNode.data?.executionStatus !== baseNode.data?.executionStatus ||
          JSON.stringify(oldNode.data) !== JSON.stringify(baseNode.data)
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
  
  // Listen for loop expand/collapse events from nodes
  useEffect(() => {
    const handleLoopExpand = (e: Event) => {
      const customEvent = e as CustomEvent<{ loopNodeId: string; step: LoopStep }>
      console.log('[WorkflowViewer] Received loop-expand event', { 
        loopNodeId: customEvent.detail.loopNodeId, 
        hasStep: !!customEvent.detail.step,
        hasProjectId: !!projectId,
        projectId 
      })
      if (!projectId) {
        console.warn('[WorkflowViewer] Cannot expand loop: projectId is missing')
        return
      }
      // Call expandLoop and handle any errors
      console.log('[WorkflowViewer] Calling expandLoop', { loopNodeId: customEvent.detail.loopNodeId })
      expandedLoopsHook.expandLoop(customEvent.detail.loopNodeId, customEvent.detail.step)
        .then(() => {
          console.log('[WorkflowViewer] Loop expanded successfully', { loopNodeId: customEvent.detail.loopNodeId })
        })
        .catch((err: unknown) => {
          console.error('[WorkflowViewer] Error expanding loop:', err)
        })
    }
    
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
    
    document.addEventListener('loop-expand', handleLoopExpand as EventListener)
    document.addEventListener('click', handleLoopCollapse)
    document.addEventListener('loop-iteration-change', handleIterationChange as EventListener)
    
    return () => {
      document.removeEventListener('loop-expand', handleLoopExpand as EventListener)
      document.removeEventListener('click', handleLoopCollapse)
      document.removeEventListener('loop-iteration-change', handleIterationChange as EventListener)
    }
  }, [projectId, expandedLoopsHook])
  
  // Refit view when loops are expanded/collapsed (but NOT when viewer is expanded)
  useEffect(() => {
    if (expandedLoopConfigs.length > 0 && !isExpanded) {
      setTimeout(() => fitView({ padding: 0.2 }), 100)
    }
  }, [expandedLoopConfigs.length, fitView, isExpanded])

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

  // Fit view on mount and when nodes change (but NOT when expanded - let user control zoom/pan)
  const handleInit = useCallback(() => {
    if (!isExpanded) {
      setTimeout(() => fitView({ padding: 0.2 }), 100)
    }
  }, [fitView, isExpanded])

  const displayTitle = title || workflow.name || 'Workflow'

  // Height class: compact mode uses fixed height, otherwise fills parent
  // When expanded, we'll render in a portal with fixed positioning
  const heightClass = compact 
    ? 'h-64' 
    : 'h-full'

  // Content to render - same whether expanded or not
  const content = (
    <div className={`flex bg-background overflow-hidden ${heightClass}`}>
      {/* Main content area */}
      <div className="flex-1 flex flex-col min-w-0 min-h-0">
        {/* Header - aligned with chat header (inline) or right sidebar (side) */}
        <div className={`flex items-center justify-between border-b border-border bg-muted/50 ${viewerMode === 'side' ? 'h-10 px-3' : 'px-4 sm:px-6 lg:px-8 py-2 border-t border-border'}`}>
          <div className={`w-full flex items-center justify-between ${viewerMode === 'side' ? '' : 'max-w-[1200px] mx-auto'}`}>
            <div className="flex items-center gap-2">
              <h3 className="text-sm font-medium text-foreground">{displayTitle}</h3>
              {execution && (
                <span className={`text-xs px-2 py-0.5 rounded-full font-medium ${
                  execution.status === 'running'
                    ? 'bg-blue-100 text-blue-700'
                    : execution.status === 'completed'
                      ? 'bg-emerald-100 text-emerald-700'
                      : execution.status === 'failed'
                        ? 'bg-red-100 text-red-700'
                        : 'bg-gray-100 text-gray-700'
                }`}>
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

        {/* ReactFlow Canvas - full width */}
        <div className="flex-1 min-h-0 h-full w-full relative">
          <ReactFlow
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            edgeTypes={edgeTypes}
            onInit={handleInit}
            onNodeClick={handleNodeClick}
            onPaneClick={handleCloseDetails}
            onNodesChange={handleNodesChange}
            fitView={!isExpanded}
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
            {!compact && <Controls showInteractive={false} />}
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
        <div className="flex items-center gap-4 px-3 py-2 border-t border-border bg-muted/30 text-xs">
          <div className="flex items-center gap-1.5">
            <div className="w-3 h-3 rounded border-2 border-gray-300 bg-white" />
            <span className="text-muted-foreground">Pending</span>
          </div>
          <div className="flex items-center gap-1.5">
            <div className="w-3 h-3 rounded border-2 border-blue-500 bg-blue-50 animate-pulse" />
            <span className="text-muted-foreground">Running</span>
          </div>
          <div className="flex items-center gap-1.5">
            <div className="w-3 h-3 rounded border-2 border-emerald-500 bg-emerald-50" />
            <span className="text-muted-foreground">Completed</span>
          </div>
          <div className="flex items-center gap-1.5">
            <div className="w-3 h-3 rounded border-2 border-red-500 bg-red-50" />
            <span className="text-muted-foreground">Failed</span>
          </div>
        </div>
        
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
