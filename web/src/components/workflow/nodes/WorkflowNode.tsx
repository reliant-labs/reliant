import { memo } from 'react'
import { Handle, Position, useNodeConnections } from '@xyflow/react'
import { Bot, Maximize2 } from 'lucide-react'
import type { WorkflowStep } from '../../../types/workflow'
import { getStepRef, getStepInline, getStepInputs } from '../../../types/workflow'
import { unwrapProtoValue } from '../../../lib/protoValueUtils'
import type { NodeExecutionStatus } from '../../../lib/workflow-flow'
import { NodeStatusWrapper, buildHandleClassName } from './NodeStatusWrapper'
import { normalizeWorkflowRef } from '../useWorkflowInputs'

interface WorkflowNodeProps {
  id: string
  data: {
    step: WorkflowStep
    label: string
    executionStatus?: NodeExecutionStatus
    layoutDirection?: 'horizontal' | 'vertical'
  }
  selected?: boolean
}

export const WorkflowNode = memo(({ id, data, selected }: WorkflowNodeProps) => {
  const { step, label, executionStatus, layoutDirection = 'horizontal' } = data
  // Proto uses 'ref' for workflow reference and 'inputs' for arguments
  const workflowRef = getStepRef(step)
  const inlineWorkflow = getStepInline(step)
  const inputs = getStepInputs(step)

  const targetConnections = useNodeConnections({ handleType: 'target' })
  const sourceConnections = useNodeConnections({ handleType: 'source' })

  // Display clean workflow name without prefix
  const workflowName = workflowRef ? normalizeWorkflowRef(workflowRef) : ''

  // Determine workflow type
  const isBuiltin = workflowRef?.startsWith("builtin://") ?? false
  const isUserWorkflow = workflowRef?.startsWith("workflow://") ?? false

  // Count inline workflow nodes
  const inlineNodeCount = inlineWorkflow?.nodes?.length ?? 0

  // Can expand if there's an inline workflow to edit
  const canExpand = !!inlineWorkflow



  const isTargetConnected = targetConnections.length > 0
  const isSourceConnected = sourceConnections.length > 0

  // Handle expand click - dispatch custom event like LoopNode does
  const handleExpandClick = (e: React.MouseEvent) => {
    e.stopPropagation()
    if (!canExpand) return

    const event = new CustomEvent('workflow-expand', {
      detail: { workflowNodeId: id, step },
      bubbles: true,
    })
    document.dispatchEvent(event)
  }

  return (
    <NodeStatusWrapper
      status={executionStatus}
      selected={selected}
      theme="purple"
    >
      {/* Connection handles - dynamic based on layout direction */}
      <Handle
        type="target"
        position={layoutDirection === 'vertical' ? Position.Top : Position.Left}
        className={buildHandleClassName('purple', isTargetConnected, executionStatus)}
      />
      <Handle
        type="source"
        position={layoutDirection === 'vertical' ? Position.Bottom : Position.Right}
        className={buildHandleClassName('purple', isSourceConnected, executionStatus)}
      />

      <div className="flex flex-col gap-2">
        {/* Header with icon and type */}
        <div className="flex items-center gap-2">
          <div className="w-8 h-8 rounded-lg bg-purple-500 flex items-center justify-center flex-shrink-0">
            <Bot className="w-4 h-4 text-white" />
          </div>
          <div className="flex-1 min-w-0">
            <div className="text-[10px] font-bold uppercase tracking-wide text-purple-600">AGENT</div>
            <div className="font-semibold text-foreground text-sm leading-tight truncate">{label}</div>
          </div>
          {/* Expand button for inline workflows */}
          {canExpand && (
            <button
              onClick={handleExpandClick}
              className="p-1.5 rounded bg-purple-200 hover:bg-purple-300 dark:bg-purple-700 dark:hover:bg-purple-600 transition-colors flex-shrink-0 z-10 relative"
              title="Expand to edit inline workflow"
              type="button"
            >
              <Maximize2 className="w-4 h-4 text-purple-700 dark:text-purple-200" />
            </button>
          )}
        </div>

        {/* Workflow info */}
        {inlineWorkflow ? (
          <div className="text-xs break-words">
            <span className="text-purple-600 font-medium">Inline</span>
            {inlineNodeCount > 0 && (
              <span className="text-muted-foreground">: {inlineNodeCount} node{inlineNodeCount !== 1 ? 's' : ''}</span>
            )}
          </div>
        ) : workflowRef ? (
          <>
            <div className="text-xs break-words">
              <span className="text-purple-600 font-medium">Reference:</span>{' '}
              <span className="text-foreground font-semibold">{workflowName}</span>
            </div>
            {(isBuiltin || isUserWorkflow) && (
              <div className="text-xs">
                <span className="px-1.5 py-0.5 rounded text-[10px] font-medium bg-muted text-muted-foreground">
                  {isBuiltin ? 'builtin' : 'saved workflow'}
                </span>
              </div>
            )}
          </>
        ) : (
          <div className="text-xs text-muted-foreground italic">Not configured</div>
        )}

        {/* Inputs preview */}
        {inputs && Object.keys(inputs).length > 0 && (
          <div className="text-xs text-muted-foreground bg-muted px-2 py-1 rounded">
            <div className="font-medium text-foreground mb-1">Inputs:</div>
            {Object.entries(inputs).slice(0, 2).map(([key, value]) => (
              <div key={key} className="truncate max-w-[180px]">
                <span className="text-muted-foreground">{key}:</span>{' '}
                {String(unwrapProtoValue(value as import('@bufbuild/protobuf/wkt').Value | undefined) ?? '')}
              </div>
            ))}
            {Object.keys(inputs).length > 2 && (
              <div className="text-muted-foreground italic">
                +{Object.keys(inputs).length - 2} more
              </div>
            )}
          </div>
        )}
      </div>
    </NodeStatusWrapper>
  )
})

WorkflowNode.displayName = 'WorkflowNode'