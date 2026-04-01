import { useState, useEffect } from 'react'
import type { Edge, Node } from '@xyflow/react'
import { ConfigurationPanel } from './ConfigurationPanel'

interface EdgeConfigPanelProps {
  edge: Edge
  nodes: Node[]
  onUpdate: (edgeId: string, data: any) => void
  onClose: () => void
  onDelete: (edgeId: string) => void
  bottomOffset?: number
  topOffset?: number
  isReadOnly?: boolean
}

export function EdgeConfigPanel({
  edge,
  nodes,
  onUpdate,
  onClose,
  onDelete,
  bottomOffset,
  topOffset,
  isReadOnly = false,
}: EdgeConfigPanelProps) {
  
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

  // Sync with edge data
  useEffect(() => {
    setLabel((edge.data?.label as string) || '')
  }, [edge.id, edge.data?.label])

  // Auto-save with debounce
  useEffect(() => {
    const timer = setTimeout(() => {
      onUpdate(edge.id, {
        label: label.trim() || undefined,
      })
    }, 400)
    return () => clearTimeout(timer)
  }, [edge.id, label, onUpdate])

  const handleDelete = () => {
    onDelete(edge.id)
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
            onChange={(e) => setLabel(e.target.value)}
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
