import { memo } from 'react'
import { Handle, Position, useNodeConnections } from '@xyflow/react'
import { RefreshCw, CheckCircle2, XCircle, Circle, Loader2, Maximize2 } from 'lucide-react'
import type { LoopStep } from '../../../types/workflow'
import { getStepRef, getStepInline, getStepWhile } from '../../../types/workflow'
import type { NodeExecutionStatus } from '../../../lib/workflow-flow'
import { NodeStatusWrapper, buildHandleClassName } from './NodeStatusWrapper'

interface LoopNodeProps {
  id: string
  data: {
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
    /** Whether the loop can be expanded (has a workflow) */
    canExpand?: boolean
  }
  selected?: boolean
}

/** Status indicator dot for an iteration */
function IterationDot({ status, index }: { status: NodeExecutionStatus; index: number }) {
  const baseClass = "w-3 h-3"
  
  switch (status) {
    case 'completed':
      return <span title={`Iteration ${index + 1}: completed`}><CheckCircle2 className={`${baseClass} text-emerald-500`} /></span>
    case 'running':
      return <span title={`Iteration ${index + 1}: running`}><Loader2 className={`${baseClass} text-sky-500 animate-spin`} /></span>
    case 'failed':
      return <span title={`Iteration ${index + 1}: failed`}><XCircle className={`${baseClass} text-red-500`} /></span>
    default:
      return <span title={`Iteration ${index + 1}: pending`}><Circle className={`${baseClass} text-gray-300`} /></span>
  }
}

export const LoopNode = memo(({ id, data, selected }: LoopNodeProps) => {
  const { 
    step, 
    label, 
    executionStatus, 
    currentIteration,
    completedIterations = 0,
    maxIterations,
    iterationStatuses = [],
  } = data
  // Loop properties are accessed via helpers (they live inside the proto args oneof)
  // Can expand if there's a workflow to show (either external workflow reference OR inline definition)
  const loopRef = getStepRef(step)
  const loopInline = getStepInline(step)
  const loopWhile = getStepWhile(step)
  const canExpand = !!(loopRef || loopInline)
  
  // Runtime max iterations (may come from custom params at runtime)
  const effectiveMax = maxIterations

  const targetConnections = useNodeConnections({ handleType: 'target' })
  const sourceConnections = useNodeConnections({ handleType: 'source' })
  
  const isTargetConnected = targetConnections.length > 0
  const isSourceConnected = sourceConnections.length > 0
  
  // Determine if we have execution info to show
  const hasExecutionInfo = iterationStatuses.length > 0 || completedIterations > 0 || currentIteration !== undefined
  
  // Limit displayed iteration dots to avoid overflow
  const MAX_VISIBLE_DOTS = 6
  const showDots = iterationStatuses.length > 0 && iterationStatuses.length <= MAX_VISIBLE_DOTS
  const showProgress = iterationStatuses.length > MAX_VISIBLE_DOTS || (!showDots && hasExecutionInfo)

  // Handle expand click
  const handleExpandClick = (e: React.MouseEvent) => {
    e.stopPropagation()
    if (!canExpand) return
    
    // Dispatch custom event for expansion
    const event = new CustomEvent('loop-expand', {
      detail: { loopNodeId: id, step },
      bubbles: true,
    })
    document.dispatchEvent(event)
  }

  return (
    <NodeStatusWrapper
      status={executionStatus}
      selected={selected}
      theme="violet"
      maxWidth={300}
    >
      <div>
        {/* Connection handles - left (input) and right (output) only */}
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

        <div className="flex flex-col gap-2">
          {/* Header with icon and type */}
          <div className="flex items-center gap-2">
            <div className="w-8 h-8 rounded-lg bg-violet-500 flex items-center justify-center flex-shrink-0">
              <RefreshCw className={`w-4 h-4 text-white ${executionStatus === 'running' ? 'animate-spin' : ''}`} />
            </div>
            <div className="flex-1 min-w-0">
              <div className="text-[10px] font-bold uppercase tracking-wide text-violet-600">LOOP</div>
              <div className="font-semibold text-foreground text-sm truncate">{label}</div>
            </div>
            {/* Expand button - always visible when expandable */}
            {canExpand && (
              <button
                onClick={handleExpandClick}
                className="p-1.5 rounded bg-violet-200 hover:bg-violet-300 dark:bg-violet-700 dark:hover:bg-violet-600 transition-colors flex-shrink-0 z-10 relative"
                title="Expand to see sub-workflow"
                type="button"
              >
                <Maximize2 className="w-4 h-4 text-violet-700 dark:text-violet-200" />
              </button>
            )}
          </div>

        {/* Loop config summary */}
        {loopInline ? (
          <div className="flex items-center gap-2 flex-wrap">
            <div className="text-xs px-2 py-1 rounded-full bg-muted text-muted-foreground">
              Inline{loopInline.nodes?.length ? `: ${loopInline.nodes.length} node${loopInline.nodes.length !== 1 ? 's' : ''}` : ''}
            </div>
          </div>
        ) : loopRef ? (
          <div className="flex items-center gap-2 flex-wrap">
            <div className="text-xs px-2 py-1 rounded-full bg-muted text-muted-foreground truncate max-w-[200px]" title={loopRef}>
              {loopRef}
            </div>
          </div>
        ) : null}
        
        {/* Iteration progress - show dots for small number, progress bar for many */}
        {showDots && (
          <div className="flex items-center gap-1 flex-wrap">
            {iterationStatuses.map((status, idx) => (
              <IterationDot key={idx} status={status} index={idx} />
            ))}
          </div>
        )}
        
        {showProgress && (
          <div className="space-y-1">
            {/* Progress text */}
            <div className="flex items-center justify-between text-xs">
              <span className="text-muted-foreground">Progress</span>
              <span className="font-medium text-foreground">
                {currentIteration !== undefined && executionStatus === 'running' 
                  ? `${currentIteration + 1}${effectiveMax ? `/${effectiveMax}` : ''} running`
                  : `${completedIterations}${effectiveMax ? `/${effectiveMax}` : ''} done`
                }
              </span>
            </div>
            {/* Progress bar */}
            {effectiveMax && (
              <div className="h-1.5 rounded-full overflow-hidden bg-muted">
                <div 
                  className={`h-full transition-all duration-300 ${
                    executionStatus === 'running' ? 'bg-sky-500' :
                    executionStatus === 'failed' ? 'bg-red-500' :
                    executionStatus === 'completed' ? 'bg-emerald-500' :
                    'bg-violet-400'
                  }`}
                  style={{ 
                    width: `${Math.min(100, ((currentIteration ?? completedIterations) / effectiveMax) * 100)}%` 
                  }}
                />
              </div>
            )}
          </div>
        )}

        {/* While condition (if set) */}
        {loopWhile && (
          <div className="text-xs text-muted-foreground font-mono bg-muted px-2 py-1 rounded truncate" title={loopWhile}>
            while: {loopWhile}
          </div>
        )}

        {/* Brief description - only show when not running to save space */}
        {!hasExecutionInfo && (
          <div className="text-xs text-muted-foreground">
            Runs {loopRef ? loopRef : (loopInline ? 'inline workflow' : 'workflow')}
            {loopWhile ? ' while condition holds' : ' in a loop'}
          </div>
        )}
        

      </div>
      </div>
    </NodeStatusWrapper>
  )
})

LoopNode.displayName = 'LoopNode'
