/**
 * ExpandedLoopNode - Group container for an expanded loop showing sub-workflow
 * 
 * This is a ReactFlow group node that:
 * - Contains child nodes from the sub-workflow
 * - Shows iteration navigator for switching between iterations
 * - Has a collapse button to return to compact LoopNode view
 * - Displays overall loop status and progress
 */

import { memo } from 'react'
import { Handle, Position, useNodeConnections, NodeResizer } from '@xyflow/react'
import { RefreshCw, ChevronLeft, ChevronRight, CheckCircle2, XCircle, Loader2, Circle, Minimize2 } from 'lucide-react'
import type { LoopStep } from '../../../types/workflow'
import { getStepWhile } from '../../../types/workflow'
import type { NodeExecutionStatus } from '../../../lib/workflow-flow'
import { buildHandleClassName } from './NodeStatusWrapper'
import { normalizeWorkflowRef } from '../useWorkflowInputs'

interface ExpandedLoopNodeData {
  step: LoopStep
  label: string
  executionStatus?: NodeExecutionStatus
  /** Current iteration if running (0-indexed) */
  currentIteration?: number
  /** Number of completed iterations */
  completedIterations?: number
  /** Max iterations from config or runtime */
  maxIterations?: number
  /** Status of each iteration */
  iterationStatuses?: NodeExecutionStatus[]
  /** Is this node in expanded state */
  isExpanded: boolean
  /** Currently selected iteration to view */
  selectedIteration: number
  /** Total available iterations */
  totalIterations: number
  /** Sub-workflow name */
  subWorkflowName: string
}

interface ExpandedLoopNodeProps {
  id: string
  data: ExpandedLoopNodeData
  selected?: boolean
}

/** Compact status indicator dot */
function StatusDot({ status }: { status?: NodeExecutionStatus }) {
  switch (status) {
    case 'completed':
      return <CheckCircle2 className="w-3.5 h-3.5 text-emerald-500" />
    case 'running':
      return <Loader2 className="w-3.5 h-3.5 text-sky-500 animate-spin" />
    case 'failed':
      return <XCircle className="w-3.5 h-3.5 text-red-500" />
    default:
      return <Circle className="w-3.5 h-3.5 text-violet-300" />
  }
}

/** Iteration navigator - compact stepper with prev/next and dropdown */
function IterationNavigator({
  loopNodeId,
  selectedIteration,
  totalIterations,
  iterationStatuses,
}: {
  loopNodeId: string
  selectedIteration: number
  totalIterations: number
  iterationStatuses: NodeExecutionStatus[]
}) {
  const dispatchChange = (iteration: number) => {
    const event = new CustomEvent('loop-iteration-change', {
      detail: { loopNodeId, iteration },
      bubbles: true,
    })
    document.dispatchEvent(event)
  }

  const canGoPrev = selectedIteration > 0
  const canGoNext = selectedIteration < totalIterations - 1

  return (
    <div className="flex items-center gap-1">
      {/* Previous button */}
      <button
        onClick={(e) => {
          e.stopPropagation()
          if (canGoPrev) dispatchChange(selectedIteration - 1)
        }}
        disabled={!canGoPrev}
        className={`p-0.5 rounded transition-colors ${
          canGoPrev 
            ? 'hover:bg-violet-200 text-violet-600' 
            : 'text-violet-300 cursor-not-allowed'
        }`}
        title="Previous iteration"
      >
        <ChevronLeft className="w-4 h-4" />
      </button>

      {/* Current iteration indicator with dropdown */}
      <div className="flex items-center gap-1.5 px-2 py-0.5 bg-white rounded border border-violet-200">
        <StatusDot status={iterationStatuses[selectedIteration]} />
        <select
          value={selectedIteration}
          onChange={(e) => {
            e.stopPropagation()
            dispatchChange(Number(e.target.value))
          }}
          onClick={(e) => e.stopPropagation()}
          className="text-xs font-medium text-violet-700 bg-transparent border-none cursor-pointer focus:outline-none pr-1"
          title="Select iteration"
        >
          {Array.from({ length: totalIterations }, (_, i) => (
            <option key={i} value={i}>
              {i + 1} of {totalIterations}
            </option>
          ))}
        </select>
      </div>

      {/* Next button */}
      <button
        onClick={(e) => {
          e.stopPropagation()
          if (canGoNext) dispatchChange(selectedIteration + 1)
        }}
        disabled={!canGoNext}
        className={`p-0.5 rounded transition-colors ${
          canGoNext 
            ? 'hover:bg-violet-200 text-violet-600' 
            : 'text-violet-300 cursor-not-allowed'
        }`}
        title="Next iteration"
      >
        <ChevronRight className="w-4 h-4" />
      </button>
    </div>
  )
}

export const ExpandedLoopNode = memo(({ id, data, selected }: ExpandedLoopNodeProps) => {
  const {
    step,
    label,
    executionStatus,
    iterationStatuses = [],
    selectedIteration,
    totalIterations,
    subWorkflowName,
    maxIterations,
  } = data
  const stepWhile = getStepWhile(step)

  const targetConnections = useNodeConnections({ handleType: 'target' })
  const sourceConnections = useNodeConnections({ handleType: 'source' })

  const isTargetConnected = targetConnections.length > 0
  const isSourceConnected = sourceConnections.length > 0

  // Runtime max iterations (may come from custom params at runtime)
  const effectiveMax = maxIterations

  // Determine border/background based on execution status
  // Uses neutral violet colors for loop container to contrast with green action nodes
  const getStatusStyles = () => {
    switch (executionStatus) {
      case 'running':
        return 'border-sky-500 bg-sky-50/30 ring-2 ring-sky-200'
      case 'completed':
        return 'border-violet-400 bg-violet-50/50'
      case 'failed':
        return 'border-red-500 bg-red-50/30 ring-2 ring-red-200'
      default:
        return selected 
          ? 'border-violet-500 ring-2 ring-violet-200' 
          : 'border-violet-300'
    }
  }

  const completedCount = iterationStatuses.filter(s => s === 'completed').length

  return (
    <>
      {/* Node resizer for group nodes */}
      <NodeResizer
        minWidth={300}
        minHeight={200}
        isVisible={selected}
        lineClassName="!border-violet-400"
        handleClassName="!w-3 !h-3 !bg-violet-500 !border-white"
      />

      {/* Connection handles on group boundary */}
      <Handle
        type="target"
        position={Position.Left}
        className={buildHandleClassName('violet', isTargetConnected, executionStatus)}
      />
      <Handle
        type="source"
        position={Position.Right}
        className={buildHandleClassName('violet', isSourceConnected, executionStatus)}
      />

      {/* Group container */}
      <div
        className={`
          w-full h-full rounded-lg border-2 border-dashed
          ${getStatusStyles()}
          flex flex-col overflow-hidden
        `}
      >
        {/* Header bar - neutral violet colors */}
        <div className="flex items-center justify-between px-3 py-2.5 bg-violet-100/90 dark:bg-violet-800/90 border-b border-violet-200 dark:border-violet-700">
          {/* Left: Icon, label, workflow name */}
          <div className="flex items-center gap-2.5 min-w-0">
            <div className="w-7 h-7 rounded-lg bg-violet-200 dark:bg-violet-700 flex items-center justify-center flex-shrink-0">
              <RefreshCw
                className={`w-4 h-4 text-violet-600 dark:text-violet-300 ${executionStatus === 'running' ? 'animate-spin' : ''}`}
              />
            </div>
            <div className="min-w-0">
              <div className="text-sm font-semibold text-violet-700 dark:text-violet-200 truncate">{label}</div>
              <div className="text-[10px] text-violet-500 dark:text-violet-400 truncate" title={subWorkflowName}>
                {normalizeWorkflowRef(subWorkflowName)}
              </div>
            </div>
          </div>

          {/* Right: Progress + collapse button */}
          <div className="flex items-center gap-2.5 flex-shrink-0">
            {/* Progress indicator */}
            <div className="text-xs font-medium text-violet-700 dark:text-violet-200 bg-violet-200/70 dark:bg-violet-700/70 px-2.5 py-1 rounded-md border border-violet-300/50 dark:border-violet-600/50">
              {effectiveMax 
                ? `${completedCount}/${effectiveMax}`
                : totalIterations > 0 
                  ? `${completedCount} done`
                  : 'starting...'
              }
            </div>

            {/* Collapse button - more prominent */}
            <button
              className="p-1.5 rounded bg-violet-200 hover:bg-violet-300 dark:bg-violet-700 dark:hover:bg-violet-600 transition-colors flex-shrink-0 border border-violet-300/50 dark:border-violet-600/50"
              title="Collapse loop"
              data-collapse-loop={id}
              type="button"
            >
              <Minimize2 className="w-4 h-4 text-violet-700 dark:text-violet-200" />
            </button>
          </div>
        </div>

        {/* Iteration navigator bar - compact stepper instead of tabs */}
        {totalIterations > 0 && (
          <div className="flex items-center justify-center px-2 py-1.5 bg-violet-50/70 dark:bg-violet-900/50 border-b border-violet-200 dark:border-violet-700">
            <IterationNavigator
              loopNodeId={id}
              selectedIteration={selectedIteration}
              totalIterations={totalIterations}
              iterationStatuses={iterationStatuses}
            />
          </div>
        )}

        {/* Child content area - ReactFlow renders child nodes here */}
        <div className="flex-1 relative bg-white/30 dark:bg-violet-950/30 min-h-[200px]">
          {/* Child nodes are rendered by ReactFlow based on parentId */}
          {/* This is just the container */}
        </div>

        {/* Footer with while condition */}
        {stepWhile && (
          <div className="px-3 py-1.5 bg-violet-50/70 dark:bg-violet-900/50 border-t border-violet-200 dark:border-violet-700">
            <div className="text-[10px] text-violet-600 dark:text-violet-400 font-mono truncate" title={stepWhile}>
              while: {stepWhile}
            </div>
          </div>
        )}
      </div>
    </>
  )
})

ExpandedLoopNode.displayName = 'ExpandedLoopNode'
