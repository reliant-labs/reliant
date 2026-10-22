import { describe, it, expect } from 'vitest'
import type { Node } from '@xyflow/react'
import type { Workflow } from '../../types/workflow'
import type { FlowNodeData, WorkflowFlowElements, ExpandedLoopConfig } from '../workflow-flow'
import { mergeExpandedLoops } from '../workflow-flow'

function makeLoopWorkflow(name: string, loopId: string): Workflow {
  return {
    name,
    nodes: [
      { id: loopId, type: 'loop', inline: { name: `${loopId}-body`, nodes: [], edges: [], entry: [] } },
    ],
    edges: [],
    entry: [loopId],
  }
}

describe('mergeExpandedLoops', () => {
  it('expands nested loops even when configs are provided child-first', () => {
    const baseElements: WorkflowFlowElements = {
      nodes: [
        {
          id: 'outer',
          type: 'loopNode',
          position: { x: 100, y: 100 },
          data: {
            label: 'outer',
            step: { id: 'outer', type: 'loop', inline: { name: 'outer-body', nodes: [], edges: [], entry: [] } },
          } as FlowNodeData,
        } as Node<FlowNodeData>,
      ],
      edges: [],
    }

    const expandedLoops: ExpandedLoopConfig[] = [
      {
        // Intentionally child-first to verify depth-based ordering
        loopNodeId: 'outer:inner',
        subWorkflow: {
          name: 'inner-body',
          nodes: [{ id: 'inner-run', type: 'run', command: 'echo inner' }],
          edges: [],
          entry: ['inner-run'],
        },
      },
      {
        loopNodeId: 'outer',
        subWorkflow: makeLoopWorkflow('outer-body', 'inner'),
      },
    ]

    const merged = mergeExpandedLoops(baseElements, expandedLoops)

    const outerNode = merged.nodes.find(n => n.id === 'outer')
    const innerNode = merged.nodes.find(n => n.id === 'outer:inner')
    const innerChildNode = merged.nodes.find(n => n.id === 'outer:inner:inner-run')

    expect(outerNode?.type).toBe('expandedLoopNode')
    expect(innerNode?.type).toBe('expandedLoopNode')
    expect(innerChildNode).toBeDefined()
  })
})
