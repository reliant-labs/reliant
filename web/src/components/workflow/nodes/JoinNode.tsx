import { memo } from 'react'
import { Handle, Position, useNodeConnections } from '@xyflow/react'
import { GitMerge } from 'lucide-react'
import type { JoinStep } from '../../../types/workflow'
import type { NodeExecutionStatus } from '../../../lib/workflow-flow'
import { NodeStatusWrapper, buildHandleClassName } from './NodeStatusWrapper'

interface JoinNodeProps {
  data: {
    step: JoinStep
    label: string
    executionStatus?: NodeExecutionStatus
    layoutDirection?: 'horizontal' | 'vertical'
  }
  selected?: boolean
}

export const JoinNode = memo(({ data, selected }: JoinNodeProps) => {
  const { step, label, executionStatus, layoutDirection = 'horizontal' } = data
  // step.condition is a DirectCelBool ({ expr: "all" | "any" }), not a raw
  // string — extract the expression before comparing. Default to "all" to
  // match the config panel.
  const joinMode = step.condition?.expr || 'all'

  const targetConnections = useNodeConnections({ handleType: 'target' })
  const sourceConnections = useNodeConnections({ handleType: 'source' })
  
  const isTargetConnected = targetConnections.length > 0
  const isSourceConnected = sourceConnections.length > 0

  return (
    <NodeStatusWrapper
      status={executionStatus}
      selected={selected}
      theme="teal"
      minWidth={180}
      maxWidth={250}
    >
      {/* Connection handles - dynamic based on layout direction */}
      <Handle 
        type="target" 
        position={layoutDirection === 'vertical' ? Position.Top : Position.Left} 
        className={buildHandleClassName('teal', isTargetConnected, executionStatus)}
      />
      <Handle 
        type="source" 
        position={layoutDirection === 'vertical' ? Position.Bottom : Position.Right} 
        className={buildHandleClassName('teal', isSourceConnected, executionStatus)}
      />

      <div className="flex flex-col gap-2">
        {/* Header with icon and type */}
        <div className="flex items-center gap-2">
          <div className="w-8 h-8 rounded-lg bg-teal-500 flex items-center justify-center flex-shrink-0">
            <GitMerge className="w-4 h-4 text-white" />
          </div>
          <div className="flex-1 min-w-0">
            <div className="text-[10px] font-bold uppercase tracking-wide text-teal-600">JOIN</div>
            <div className="font-semibold text-foreground text-sm">{label}</div>
          </div>
        </div>

        {/* Join condition */}
        <div className="flex items-center gap-2">
          <div className="text-xs px-2 py-1 rounded-full bg-muted text-muted-foreground">
            {joinMode === 'all' ? 'Wait for all' : 'First wins'}
          </div>
        </div>

        {/* Description */}
        <div className="text-xs text-muted-foreground">
          {joinMode === 'all' 
            ? 'Continues after all incoming branches complete'
            : 'Continues when first incoming branch completes'}
        </div>
      </div>
    </NodeStatusWrapper>
  )
})

JoinNode.displayName = 'JoinNode'
