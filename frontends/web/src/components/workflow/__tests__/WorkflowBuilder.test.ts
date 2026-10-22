import { describe, it, expect } from 'vitest'
import type { Edge, Node } from '@xyflow/react'
import type { Edge as WorkflowEdge } from '../../../types/workflow'

// Helper to simulate buildWorkflow logic (with new cases model)
function buildEdges(nodes: Node[], edges: Edge[]) {
  // Filter out event nodes (like workflow)
  const steps = nodes
    .filter((node) => node.type !== 'eventNode')
    .map((node) => ({
      id: node.id,
      type: node.data.step?.type || 'run',
      command: node.data.step?.command || '',
      position: node.position,
    }))

  // Build set of valid node IDs
  const validNodeIds = new Set(nodes.map((n) => n.id))

  // Group Flow edges by source to create switch-case edges
  const edgesBySource = new Map<string, Array<{ to: string; condition?: string; label?: string }>>()

  for (const edge of edges) {
    const { sourceEvent, condition, label } = edge.data || {}

    // Filter out orphaned edges
    if (!validNodeIds.has(edge.source) || !validNodeIds.has(edge.target)) {
      continue
    }

    let from: string
    if (edge.source === 'workflow' && sourceEvent) {
      from = sourceEvent
    } else if (sourceEvent && sourceEvent !== 'completed') {
      from = `${edge.source}.${sourceEvent}`
    } else {
      from = edge.source
    }

    if (!edgesBySource.has(from)) {
      edgesBySource.set(from, [])
    }
    edgesBySource.get(from)!.push({
      to: edge.target,
      condition,
      label,
    })
  }

  // Convert to Edge format with cases
  const workflowEdges: WorkflowEdge[] = []
  for (const [from, cases] of edgesBySource) {
    workflowEdges.push({ from, cases })
  }

  // Return a flattened view for test compatibility (old tests expect flat edges)
  const flatEdges = workflowEdges.flatMap(edge => {
    const caseEdges = (edge.cases || []).map(c => ({ from: edge.from, to: c.to, condition: c.condition, label: c.label }))
    const defaultTargets = edge.default
      ? (Array.isArray(edge.default) ? edge.default : [edge.default])
      : []
    const defaultEdges = defaultTargets.map(to => ({ from: edge.from, to, condition: undefined, label: undefined }))
    return [...caseEdges, ...defaultEdges]
  })

  return { nodes: steps, edges: flatEdges }
}

describe('WorkflowBuilder - Edge Building', () => {
  it('should format started edges correctly', () => {
    const nodes: Node[] = [
      {
        id: 'started',
        type: 'eventNode',
        position: { x: 0, y: 0 },
        data: { eventType: 'started' },
      },
      {
        id: 'run-1',
        type: 'runNode',
        position: { x: 100, y: 100 },
        data: { step: { id: 'run-1', type: 'run', command: 'echo hello' } },
      },
    ]

    const edges: Edge[] = [
      {
        id: 'e1',
        source: 'started',
        target: 'run-1',
        data: {},
      },
    ]

    const result = buildEdges(nodes, edges)

    expect(result.edges).toHaveLength(1)
    expect(result.edges[0].from).toBe('started')
    expect(result.edges[0].to).toBe('run-1')
  })

  it('should handle started event node correctly', () => {
    const nodes: Node[] = [
      {
        id: 'started',
        type: 'eventNode',
        position: { x: 0, y: 0 },
        data: { eventType: 'started' },
      },
      {
        id: 'run-1',
        type: 'runNode',
        position: { x: 100, y: 100 },
        data: { step: { id: 'run-1', type: 'run', command: 'echo hello' } },
      },
    ]

    const edges: Edge[] = [
      {
        id: 'e1',
        source: 'started',
        target: 'run-1',
        data: {},
      },
    ]

    const result = buildEdges(nodes, edges)

    // Should be "started"
    expect(result.edges[0].from).toBe('started')
  })

  it('should format regular node edges correctly', () => {
    const nodes: Node[] = [
      {
        id: 'workflow-1',
        type: 'workflowNode',
        position: { x: 0, y: 0 },
        data: { step: { id: 'workflow-1', type: 'workflow', ref: 'builtin://agent' } },
      },
      {
        id: 'run-1',
        type: 'runNode',
        position: { x: 100, y: 100 },
        data: { step: { id: 'run-1', type: 'run', command: 'echo hello' } },
      },
    ]

    const edges: Edge[] = [
      {
        id: 'e1',
        source: 'workflow-1',
        target: 'run-1',
        data: {
          sourceEvent: 'completed',
          label: 'completed',
        },
      },
    ]

    const result = buildEdges(nodes, edges)

    // In V2, step completion is implicit - just use step ID
    expect(result.edges[0].from).toBe('workflow-1')
    expect(result.edges[0].to).toBe('run-1')
  })

  it('should filter out edges pointing to non-existent steps', () => {
    const nodes: Node[] = [
      {
        id: 'workflow',
        type: 'eventNode',
        position: { x: 0, y: 0 },
        data: { eventType: 'started' },
      },
      {
        id: 'run-2',
        type: 'runNode',
        position: { x: 100, y: 100 },
        data: { step: { id: 'run-2', type: 'run', command: 'echo world' } },
      },
    ]

    const edges: Edge[] = [
      {
        id: 'e1',
        source: 'workflow',
        target: 'run-1', // This step doesn't exist!
        data: { sourceEvent: 'started' },
      },
      {
        id: 'e2',
        source: 'workflow',
        target: 'run-2',
        data: { sourceEvent: 'started' },
      },
    ]

    const result = buildEdges(nodes, edges)

    // Should filter out the orphaned edge to run-1
    expect(result.edges).toHaveLength(1)
    expect(result.edges[0].to).toBe('run-2')

    // The nodes array should only have run-2
    expect(result.nodes).toHaveLength(1)
    expect(result.nodes[0].id).toBe('run-2')
  })

  it('should automatically filter out orphaned edges', () => {
    const nodes: Node[] = [
      {
        id: 'workflow',
        type: 'eventNode',
        position: { x: 0, y: 0 },
        data: { eventType: 'started' },
      },
      {
        id: 'run-2',
        type: 'runNode',
        position: { x: 100, y: 100 },
        data: { step: { id: 'run-2', type: 'run', command: 'echo world' } },
      },
    ]

    const edges: Edge[] = [
      {
        id: 'e1',
        source: 'workflow',
        target: 'run-1', // ORPHANED - step deleted
        data: { sourceEvent: 'started' },
      },
      {
        id: 'e2',
        source: 'workflow',
        target: 'run-2',
        data: { sourceEvent: 'started' },
      },
    ]

    const result = buildEdges(nodes, edges)

    // Orphaned edges should be automatically filtered out
    expect(result.edges).toHaveLength(1)
    expect(result.edges[0].to).toBe('run-2')

    // All remaining edges should point to valid nodes
    const nodeIds = new Set(result.nodes.map(s => s.id))
    const invalidEdges = result.edges.filter(edge => {
      return edge.to !== 'workflow' && !nodeIds.has(edge.to)
    })
    expect(invalidEdges).toHaveLength(0)
  })

})
