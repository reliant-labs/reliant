/**
 * useExpandedLoops - Hook for managing expandable loop nodes in workflow viewer
 * 
 * Tracks:
 * - Which loop nodes are expanded
 * - Sub-workflow definitions (lazily fetched)
 * - Selected iteration per expanded loop
 * - Loading/error states
 */

import { useState, useCallback, useMemo, useEffect, useRef } from 'react'
import { getWorkflow } from '../../../api/workflow-grpc'
import type { Workflow, LoopStep } from '../../../types/workflow'
import { getStepRef, getStepInline } from '../../../types/workflow'
import type { WorkflowExecution } from '../../Chat/ExecutionSidebar/types'
import type { LoopIterationInfo } from './useExecutionStatus'
import { useWorkspaceStateStore } from '../../../store/workspaceStateStore'

export function getCollapsedLoopNodeIds(nodeId: string, expandedIds: string[]): string[] {
  const descendantPrefix = `${nodeId}:`
  return expandedIds.filter(id => id !== nodeId && !id.startsWith(descendantPrefix))
}

/**
 * State for a single expanded loop
 */
export interface ExpandedLoopState {
  nodeId: string
  /** The sub-workflow name from loop config */
  subWorkflowName: string
  /** Fetched sub-workflow definition */
  subWorkflow: Workflow | null
  /** Loading state for sub-workflow fetch */
  loading: boolean
  /** Error message if fetch failed */
  error?: string
  /** Currently selected iteration (0-indexed) */
  selectedIteration: number
}

/**
 * Hook result
 */
export interface UseExpandedLoopsResult {
  /** Map of expanded loop states by node ID */
  expandedLoops: Map<string, ExpandedLoopState>
  /** Expand a loop node (triggers sub-workflow fetch) */
  expandLoop: (nodeId: string, loopStep: LoopStep) => Promise<void>
  /** Collapse a loop node */
  collapseLoop: (nodeId: string) => void
  /** Toggle expansion state */
  toggleLoop: (nodeId: string, loopStep: LoopStep) => void
  /** Check if a loop is expanded */
  isExpanded: (nodeId: string) => boolean
  /** Change selected iteration for an expanded loop */
  setSelectedIteration: (nodeId: string, iteration: number) => void
  /** Get selected iteration for a loop */
  getSelectedIteration: (nodeId: string) => number
  /** Collapse all loops */
  collapseAll: () => void
}

/**
 * Hook for managing expanded loop nodes
 * 
 * @param projectId - Project ID for fetching sub-workflows
 * @param worktreeId - Worktree ID for workspace state (null for main branch)
 * @param workflowKey - Unique key for this workflow (e.g., workflow name or chatId:workflowName)
 * @param execution - Current workflow execution (for iteration data)
 * @param loopIterationSteps - Iteration info from useExecutionStatus
 */
export function useExpandedLoops(
  projectId: string,
  worktreeId: string | null,
  workflowKey: string,
  _execution?: WorkflowExecution,
  loopIterationSteps?: Record<string, LoopIterationInfo[]>
): UseExpandedLoopsResult {
  const [expandedLoops, setExpandedLoops] = useState<Map<string, ExpandedLoopState>>(new Map())
  const expandedLoopsRef = useRef(expandedLoops)
  expandedLoopsRef.current = expandedLoops
  const setExpandedLoopsState = useWorkspaceStateStore((state) => state.setExpandedLoops)

  // Track which loops have been manually selected (user clicked a different iteration)
  const [manuallySelected, setManuallySelected] = useState<Set<string>>(new Set())

  // Note: Restoration of expanded loops is handled by the parent component (WorkflowViewer)
  // which calls expandLoop for each saved loop node ID. This hook just manages the state.

  // Save expanded loops to workspace state whenever they change
  // Use a ref to track previous IDs to prevent unnecessary saves
  const prevExpandedIdsRef = useRef<string[]>([])
  useEffect(() => {
    if (!projectId || !workflowKey) return
    
    const expandedIds = Array.from(expandedLoops.keys()).sort()
    const prevIds = prevExpandedIdsRef.current.sort()
    
    // Only save if IDs actually changed
    if (expandedIds.length !== prevIds.length || 
        expandedIds.some((id, i) => id !== prevIds[i])) {
      prevExpandedIdsRef.current = expandedIds
      setExpandedLoopsState(projectId, worktreeId, workflowKey, expandedIds)
    }
  }, [projectId, worktreeId, workflowKey, expandedLoops, setExpandedLoopsState])

  // Expand a loop node
  const expandLoop = useCallback(async (nodeId: string, loopStep: LoopStep) => {
    // Check if already expanded - use ref for synchronous check
    if (expandedLoopsRef.current.has(nodeId)) {
      return
    }
    
    // Loop properties live inside the proto args oneof — use accessors
    const subWorkflowRef = getStepRef(loopStep)
    const inlineWorkflowDef = getStepInline(loopStep)
    
    // Must have either a workflow reference or inline definition
    if (!subWorkflowRef && !inlineWorkflowDef) {
      return
    }

    // Handle inline loop workflow - no fetch needed
    if (inlineWorkflowDef) {
      // Convert inline workflow to a Workflow-like structure
      const inlineWorkflow: Workflow = {
        name: `${nodeId}_inline`,
        nodes: inlineWorkflowDef.nodes || [],
        edges: inlineWorkflowDef.edges || [],
        outputs: inlineWorkflowDef.outputs || {},
        entry: inlineWorkflowDef.entry,
      }
      
      setExpandedLoops(prev => {
        const next = new Map(prev)
        next.set(nodeId, {
          nodeId,
          subWorkflowName: `${nodeId}_inline`,
          subWorkflow: inlineWorkflow,
          loading: false,
          selectedIteration: loopIterationSteps?.[nodeId]?.length 
            ? Math.max(...loopIterationSteps[nodeId].map(i => i.iteration))
            : 0,
        })
        return next
      })
      
      return
    }

    // Handle external workflow reference - fetch from API
    
    // Set loading state
    setExpandedLoops(prev => {
      const next = new Map(prev)
      next.set(nodeId, {
        nodeId,
        subWorkflowName: subWorkflowRef!,
        subWorkflow: null,
        loading: true,
        selectedIteration: loopIterationSteps?.[nodeId]?.length 
          ? Math.max(...loopIterationSteps[nodeId].map(i => i.iteration))
          : 0,
      })
      return next
    })

    // Fetch sub-workflow
    try {
      const subWorkflow = await getWorkflow(projectId, subWorkflowRef!)
      setExpandedLoops(prev => {
        const next = new Map(prev)
        const current = next.get(nodeId)
        if (current) {
          next.set(nodeId, {
            ...current,
            subWorkflow,
            loading: false,
            error: undefined,
          })
        }
        return next
      })
    } catch (err) {
      setExpandedLoops(prev => {
        const next = new Map(prev)
        const current = next.get(nodeId)
        if (current) {
          next.set(nodeId, {
            ...current,
            loading: false,
            error: err instanceof Error ? err.message : 'Failed to load sub-workflow',
          })
        }
        return next
      })
    }
  }, [projectId, loopIterationSteps])

  // Collapse a loop node (and any descendant expanded loops)
  const collapseLoop = useCallback((nodeId: string) => {
    setExpandedLoops(prev => {
      const remainingIds = getCollapsedLoopNodeIds(nodeId, Array.from(prev.keys()))
      if (remainingIds.length === prev.size) {
        return prev
      }

      const next = new Map<string, ExpandedLoopState>()
      for (const id of remainingIds) {
        const state = prev.get(id)
        if (state) {
          next.set(id, state)
        }
      }

      return next
    })
  }, [])

  // Toggle expansion
  const toggleLoop = useCallback((nodeId: string, loopStep: LoopStep) => {
    if (expandedLoops.has(nodeId)) {
      collapseLoop(nodeId)
    } else {
      expandLoop(nodeId, loopStep)
    }
  }, [expandedLoops, expandLoop, collapseLoop])

  // Check if expanded
  const isExpanded = useCallback((nodeId: string) => {
    return expandedLoops.has(nodeId)
  }, [expandedLoops])

  // Set selected iteration
  const setSelectedIteration = useCallback((nodeId: string, iteration: number) => {
    // Mark as manually selected so live updates don't override
    setManuallySelected(prev => new Set(prev).add(nodeId))
    
    setExpandedLoops(prev => {
      const next = new Map(prev)
      const current = next.get(nodeId)
      if (current) {
        next.set(nodeId, {
          ...current,
          selectedIteration: iteration,
        })
      }
      return next
    })
  }, [])

  // Get selected iteration
  const getSelectedIteration = useCallback((nodeId: string) => {
    return expandedLoops.get(nodeId)?.selectedIteration ?? 0
  }, [expandedLoops])

  // Collapse all
  const collapseAll = useCallback(() => {
    setExpandedLoops(new Map())
  }, [])

  // Live tracking: auto-update to current iteration when loop is running
  useEffect(() => {
    if (!loopIterationSteps) return
    
    setExpandedLoops(prev => {
      let changed = false
      const next = new Map(prev)
      
      for (const [nodeId, state] of next) {
        // Skip if user has manually selected an iteration
        if (manuallySelected.has(nodeId)) continue
        
        const iterations = loopIterationSteps[nodeId]
        if (!iterations || iterations.length === 0) continue
        
        // Find the latest iteration number
        const latestIteration = Math.max(...iterations.map(i => i.iteration))
        
        // Update if different
        if (state.selectedIteration !== latestIteration) {
          changed = true
          next.set(nodeId, {
            ...state,
            selectedIteration: latestIteration,
          })
        }
      }
      
      return changed ? next : prev
    })
  }, [loopIterationSteps, manuallySelected])

  // Reset manually selected when loop is collapsed
  useEffect(() => {
    setManuallySelected(prev => {
      const expandedIds = new Set(expandedLoops.keys())
      const next = new Set<string>()
      for (const id of prev) {
        if (expandedIds.has(id)) {
          next.add(id)
        }
      }
      // Only update if something was removed
      return next.size !== prev.size ? next : prev
    })
  }, [expandedLoops])

  return useMemo(() => ({
    expandedLoops,
    expandLoop,
    collapseLoop,
    toggleLoop,
    isExpanded,
    setSelectedIteration,
    getSelectedIteration,
    collapseAll,
  }), [
    expandedLoops,
    expandLoop,
    collapseLoop,
    toggleLoop,
    isExpanded,
    setSelectedIteration,
    getSelectedIteration,
    collapseAll,
  ])
}
