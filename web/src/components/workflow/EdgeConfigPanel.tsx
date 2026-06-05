import { useState, useEffect } from 'react'
import type { Edge, Node } from '@xyflow/react'
import { ConfigurationPanel } from './ConfigurationPanel'
import { useWorkflowMutations } from './WorkflowMutationContext'

interface EdgeConfigPanelProps {
  edge: Edge
  nodes: Node[]
  onClose: () => void
  bottomOffset?: number
  topOffset?: number
  isReadOnly?: boolean
}

export function EdgeConfigPanel({
  edge,
  nodes,
  onClose,
  bottomOffset,
  topOffset,
  isReadOnly = false,
}: EdgeConfigPanelProps) {
  // Mutation primitives from <WorkflowMutationProvider>.
  const { updateEdge, removeEdge } = useWorkflowMutations()

  const sourceNode = nodes.find((n) => n.id === edge.source)
  const targetNode = nodes.find((n) => n.id === edge.target)

  // Get labels
  const getNodeLabel = (node: Node | undefined, fallback: string) => {
    const step = node?.data?.step as { id?: string } | undefined
    return step?.id || (node?.data?.label as string) || fallback
  }

  const sourceLabel = getNodeLabel(sourceNode, edge.source)
  const targetLabel = getNodeLabel(targetNode, edge.target)

  const [label, setLabel] = useState<string>((edge.data?.label as string) || '')
  // Track whether the user has actually typed; gates the debounced save so
  // just selecting an edge doesn't mark the workflow dirty.
  const [didEdit, setDidEdit] = useState(false)

  // Sync with edge data when switching between edges
  useEffect(() => {
    setLabel((edge.data?.label as string) || '')
    setDidEdit(false)
  }, [edge.id, edge.data?.label])

  // Auto-save with debounce — only when the user has actually edited and the
  // local label differs from the persisted value.
  useEffect(() => {
    if (!didEdit) return
    const persisted = (edge.data?.label as string) || ''
    const next = label.trim()
    if (next === persisted) return
    const timer = setTimeout(() => {
      updateEdge(edge.id, {
        label: next || undefined,
      })
    }, 400)
    return () => clearTimeout(timer)
  }, [edge.id, edge.data?.label, label, didEdit, updateEdge])

  const handleDelete = () => {
    removeEdge(edge.id)
  }

  return (
    <ConfigurationPanel
      title={isReadOnly ? "Edge (View Only)" : "Edge"}
      subtitle={`${sourceLabel} → ${targetLabel}`}
      onClose={onClose}
      onDelete={isReadOnly ? undefined : handleDelete}
      deleteLabel="Delete Edge"
      bottomOffset={bottomOffset}
      topOffset={topOffset}
    >
      <div className="space-y-3">
        {/* Label input */}
        <div>
          <label className="text-xs font-medium text-muted-foreground mb-1 block">
            Label (optional)
          </label>
          <input
            type="text"
            value={label}
            onChange={(e) => {
              setLabel(e.target.value)
              setDidEdit(true)
            }}
            placeholder="e.g. on success, completed"
            className="w-full px-2 py-1.5 text-sm border border-input rounded bg-background text-foreground focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-60 disabled:cursor-not-allowed"
            disabled={isReadOnly}
          />
        </div>

        {/* Info */}
        <div className="text-xs text-muted-foreground">
          Use a Switch node for conditional routing.
        </div>
      </div>
    </ConfigurationPanel>
  )
}
