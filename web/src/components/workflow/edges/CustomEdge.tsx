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
  
  // Use bezier path for both horizontal and vertical layouts for smooth, curved connections
  const [edgePath, labelX, labelY] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  })

  // Cast data to our custom type (already done above)
  const label = edgeData?.label || ''
  const executionStatus = edgeData?.executionStatus

  // Edge color based on execution status
  // "taken" edges (source completed/running) show in green
  const isTaken = executionStatus === 'completed' || executionStatus === 'running'
  const isFailed = executionStatus === 'failed'
  
  const strokeColor = isFailed 
    ? '#ef4444' // red-500
    : isTaken 
      ? '#10b981' // emerald-500
      : '#b1b1b7' // default gray
  
  const strokeWidth = isTaken || isFailed ? 2.5 : 2

  const edgeStyle = {
    ...(style || {}),
    stroke: strokeColor,
    strokeWidth,
  }

  // Invisible wider path for easier clicking/selection
  const interactionPath = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  })[0]

  // Use SVG marker with unique ID for this edge
  const markerId = `arrow-${id}`

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
              stroke={strokeColor}
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth="1.5"
              fill={strokeColor}
              points="-6,-5 0,0 -6,5 -6,-5"
            />
          </marker>
        </defs>
      </svg>
      {/* Invisible wider path for easier selection */}
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
              zIndex: 30, // Ensure label appears above edge lines
            }}
            className="nodrag nopan"
          >
            <div
              className={`px-2 py-1 rounded border text-xs font-medium cursor-pointer transition-colors ${
                selected
                  ? 'bg-blue-500 border-blue-600 text-white shadow-lg'
                  : isTaken
                    ? 'bg-emerald-50 border-emerald-500 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300'
                    : isFailed
                      ? 'bg-red-50 border-red-500 text-red-700 dark:bg-red-950 dark:text-red-300'
                      : 'bg-background border-border text-foreground'
              }`}
              title={label}
              onClick={(e) => {
                e.stopPropagation()
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
