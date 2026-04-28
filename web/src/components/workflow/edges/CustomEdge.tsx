import { BaseEdge, EdgeLabelRenderer, getBezierPath } from '@xyflow/react'
import type { EdgeProps } from '@xyflow/react'
import type { NodeExecutionStatus } from '../../../lib/workflow-flow'

export interface CustomEdgeData {
  label?: string
  executionStatus?: NodeExecutionStatus
  layoutDirection?: 'horizontal' | 'vertical'
}

export function CustomEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  style,
  data,
  selected,
}: EdgeProps) {
  const edgeData = data as CustomEdgeData | undefined

  const [edgePath, labelX, labelY] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  })

  const label = edgeData?.label || ''
  const executionStatus = edgeData?.executionStatus
  const isTaken = executionStatus === 'completed' || executionStatus === 'running'
  const isFailed = executionStatus === 'failed'

  const strokeColor = isFailed
    ? 'hsl(var(--destructive))'
    : isTaken
      ? 'hsl(var(--success))'
      : 'hsl(var(--muted-foreground) / 0.42)'

  const strokeWidth = selected ? 3 : isTaken || isFailed ? 2.5 : 2

  const edgeStyle = {
    ...(style || {}),
    stroke: selected ? 'hsl(var(--primary))' : strokeColor,
    strokeWidth,
  }

  const interactionPath = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  })[0]

  const markerId = `arrow-${id}`
  const markerColor = selected ? 'hsl(var(--primary))' : strokeColor

  return (
    <>
      <svg style={{ position: 'absolute', top: 0, left: 0, zIndex: 25 }}>
        <defs>
          <marker
            id={markerId}
            markerWidth="18"
            markerHeight="18"
            viewBox="-10 -10 20 20"
            orient="auto"
            refX="0"
            refY="0"
          >
            <polyline
              stroke={markerColor}
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth="1.5"
              fill={markerColor}
              points="-6,-5 0,0 -6,5 -6,-5"
            />
          </marker>
        </defs>
      </svg>
      <path
        d={interactionPath}
        fill="none"
        stroke="transparent"
        strokeWidth={20}
        className="react-flow__edge-interaction"
      />
      <BaseEdge path={edgePath} markerEnd={`url(#${markerId})`} style={edgeStyle} />
      {label && (
        <EdgeLabelRenderer>
          <div
            data-edge-id={id}
            style={{
              position: 'absolute',
              transform: `translate(-50%, -50%) translate(${labelX}px,${labelY}px)`,
              fontSize: 10,
              pointerEvents: 'all',
              zIndex: 30,
            }}
            className="nodrag nopan"
          >
            <div
              className={`rounded-full border px-2 py-1 text-xs font-medium shadow-sm backdrop-blur-sm transition-colors ${
                selected
                  ? 'border-primary bg-primary text-primary-foreground shadow-primary/20'
                  : isTaken
                    ? 'border-success/50 bg-success/10 text-success'
                    : isFailed
                      ? 'border-destructive/50 bg-destructive/10 text-destructive'
                      : 'border-border bg-card/95 text-muted-foreground hover:text-foreground'
              }`}
              title={label}
              onClick={(event) => {
                event.stopPropagation()
                const edgeElement = document.querySelector(`[data-id="${id}"]`)
                if (edgeElement) {
                  edgeElement.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
                }
              }}
            >
              {label}
            </div>
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  )
}
