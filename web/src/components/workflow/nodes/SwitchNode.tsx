import { memo, useLayoutEffect, useRef, useState } from 'react'
import { Handle, Position, useNodeConnections } from '@xyflow/react'
import { GitBranch } from 'lucide-react'
import { NodeStatusWrapper, buildHandleClassName } from './NodeStatusWrapper'

export interface SwitchCase {
  id: string
  condition: string  // CEL expression, empty = default
  label?: string     // Optional display label
}

export interface SwitchNodeData {
  cases: SwitchCase[]
  label?: string
  condition?: { expr?: string }
  saveMessage?: import('../../../types/workflow').SaveMessageConfig
}

interface SwitchNodeProps {
  id: string
  data: SwitchNodeData
  selected?: boolean
}

export const SwitchNode = memo(({ id: _id, data, selected }: SwitchNodeProps) => {
  const cases = data.cases || []
  const executionStatus = (data as { executionStatus?: import('../../../lib/workflow-flow').NodeExecutionStatus }).executionStatus

  // Measure actual case-row heights so handles stay aligned when labels wrap.
  // Falls back to the historical 40px estimate before layout.
  const headerRef = useRef<HTMLDivElement | null>(null)
  const caseRowsRef = useRef<Array<HTMLDivElement | null>>([])
  const [handleTops, setHandleTops] = useState<number[]>([])

  useLayoutEffect(() => {
    const header = headerRef.current
    if (!header) return
    const rows = caseRowsRef.current.slice(0, cases.length)
    const nodeRect = header.parentElement?.getBoundingClientRect()
    if (!nodeRect) return
    const tops = rows.map((row) => {
      if (!row) return 0
      const r = row.getBoundingClientRect()
      // Distance from top of the node to the vertical center of this row.
      return r.top - nodeRect.top + r.height / 2
    })
    setHandleTops((prev) => {
      if (prev.length === tops.length && prev.every((v, i) => v === tops[i])) {
        return prev
      }
      return tops
    })
  })

  // Fallback positioning matches the previous hard-coded layout (header ~44,
  // row ~40) so handles render in roughly the right spot pre-measurement.
  const headerHeight = 44
  const caseHeight = 40
  const getHandleTop = (index: number) =>
    handleTops[index] ?? headerHeight + index * caseHeight + caseHeight / 2

  const targetConnections = useNodeConnections({ handleType: 'target' })
  const sourceConnections = useNodeConnections({ handleType: 'source' })
  const isTargetConnected = targetConnections.length > 0

  return (
    <NodeStatusWrapper
      status={executionStatus}
      selected={selected}
      theme="sky"
      minWidth={180}
      className="!p-0 !rounded-xl"
    >
      {/* Input handle (left center) */}
      <Handle
        type="target"
        position={Position.Left}
        className={buildHandleClassName('sky', isTargetConnected, executionStatus)}
      />

      {/* Header */}
      <div ref={headerRef} className="px-3 py-2 flex items-center gap-2 border-b border-sky-200">
        <div className="w-8 h-8 rounded-lg bg-sky-500 flex items-center justify-center flex-shrink-0">
          <GitBranch className="w-4 h-4 text-white" />
        </div>
        <div className="flex-1 min-w-0">
          <div className="text-2xs font-bold uppercase tracking-wide text-sky-600">SWITCH</div>
          <div className="font-semibold text-foreground text-sm">
            {data.label || 'Switch'}
          </div>
        </div>
      </div>

      {/* Cases */}
      <div className="py-1">
        {cases.map((caseItem, index) => {
          // A case is "default" only if it has no condition (empty string)
          const isDefault = !caseItem.condition
          const displayText = isDefault
            ? 'default'
            : (caseItem.label || caseItem.condition || `case ${index + 1}`)

          return (
            <div
              key={caseItem.id}
              ref={(el) => {
                caseRowsRef.current[index] = el
              }}
              className="flex items-center px-3 py-2"
            >
              {/* Case content */}
              <div
                className={`
                  flex-1 px-3 py-1.5 rounded-lg text-sm bg-muted
                  ${isDefault
                    ? 'text-muted-foreground italic'
                    : 'text-foreground font-mono text-xs'
                  }
                `}
              >
                {displayText}
              </div>
            </div>
          )
        })}
      </div>

      {/* Output handles - one per case, positioned on right side */}
      {cases.map((caseItem, index) => {
        // Check if this specific case handle is connected
        const caseConnected = sourceConnections.some(conn => conn.sourceHandle === caseItem.id)
        return (
          <Handle
            key={caseItem.id}
            type="source"
            position={Position.Right}
            id={caseItem.id}
            className={buildHandleClassName('sky', caseConnected, executionStatus)}
            style={{ top: getHandleTop(index) }}
          />
        )
      })}
    </NodeStatusWrapper>
  )
})

SwitchNode.displayName = 'SwitchNode'
