import { memo } from 'react'
import { Handle, Position, useNodeConnections } from '@xyflow/react'
import { Terminal } from 'lucide-react'
import type { RunStep } from '../../../types/workflow'
import { getStepCommand } from '../../../types/workflow'
import type { NodeExecutionStatus } from '../../../lib/workflow-flow'
import { NodeStatusWrapper, buildHandleClassName } from './NodeStatusWrapper'

interface RunNodeProps {
  data: {
    step: RunStep
    label: string
    executionStatus?: NodeExecutionStatus
    layoutDirection?: 'horizontal' | 'vertical'
  }
  selected?: boolean
}

export const RunNode = memo(({ data, selected }: RunNodeProps) => {
  const { step, label, executionStatus } = data
  // Proto uses 'command' for run steps
  const command = getStepCommand(step)
  
  const targetConnections = useNodeConnections({ handleType: 'target' })
  const sourceConnections = useNodeConnections({ handleType: 'source' })
  
  const isTargetConnected = targetConnections.length > 0
  const isSourceConnected = sourceConnections.length > 0

  return (
    <NodeStatusWrapper
      status={executionStatus}
      selected={selected}
      theme="indigo"
    >
      {/* Connection handles - left (input) and right (output) only */}
      <Handle 
        type="target" 
        position={Position.Left} 
        className={buildHandleClassName('indigo', isTargetConnected, executionStatus)}
      />
      <Handle 
        type="source" 
        position={Position.Right} 
        className={buildHandleClassName('indigo', isSourceConnected, executionStatus)}
      />

      <div className="flex flex-col gap-2">
        {/* Header with icon and type */}
        <div className="flex items-center gap-2">
          <div className="w-8 h-8 rounded-lg bg-indigo-500 flex items-center justify-center flex-shrink-0">
            <Terminal className="w-4 h-4 text-white" />
          </div>
          <div className="flex-1 min-w-0">
            <div className="text-[10px] font-bold uppercase tracking-wide text-indigo-600">RUN</div>
            <div className="font-semibold text-foreground text-sm leading-tight truncate">{label}</div>
          </div>
        </div>

        {/* Command preview */}
        <div className="text-xs font-mono text-muted-foreground bg-muted px-2 py-1 rounded break-words line-clamp-3">
          {command || <span className="text-muted-foreground italic">Run</span>}
        </div>
      </div>
    </NodeStatusWrapper>
  )
})

RunNode.displayName = 'RunNode'
