/**
 * The trigger rail is a PROJECTION (research/WORKFLOW_UI.md §3.2): it draws
 * how a workflow gets started, but triggers are separate stored rows and must
 * never reach the definition. These tests pin that the builder's rail entry
 * produces exactly the same saved graph and YAML as the plain event entry.
 */

import { describe, expect, it } from 'vitest'
import type { Node } from '@xyflow/react'
import type { Workflow } from '../../types/workflow'
import {
  ENTRY_NODE_ID,
  isEntryFlowNodeType,
  workflowToFlowElements,
} from '../workflow-flow'
import { nodesEdgesToWorkflow } from '../nodes-edges-to-workflow'
import { serializeWorkflowToYAML } from '../workflow-serializer'

const workflow: Workflow = {
  name: 'nightly-triage',
  description: 'Triage new issues',
  entry: ['fetch'],
  nodes: [
    { id: 'fetch', type: 'run', command: 'gh issue list' },
    { id: 'summarise', type: 'run', command: 'echo done' },
  ],
  edges: [{ from: 'fetch', default: ['summarise'] }],
  ui: {
    positions: {
      workflow: { x: 40, y: 200 },
      fetch: { x: 400, y: 200 },
      summarise: { x: 700, y: 200 },
    },
  },
}

function save(nodes: Node[], edges: ReturnType<typeof workflowToFlowElements>['edges']): Workflow {
  return nodesEdgesToWorkflow(nodes, edges, {
    name: workflow.name ?? '',
    description: workflow.description ?? '',
    inputs: {},
    outputs: {},
    entry: workflow.entry,
    tag: undefined,
    presetDefault: undefined,
    apiVersion: undefined,
    isLocked: false,
  })
}

describe('trigger rail projection', () => {
  it('draws the entry as the rail only when asked, with the same id and data', () => {
    const plain = workflowToFlowElements(workflow)
    const rail = workflowToFlowElements(workflow, { entryNode: 'triggerRail' })

    const plainEntry = plain.nodes.find((n) => n.id === ENTRY_NODE_ID)!
    const railEntry = rail.nodes.find((n) => n.id === ENTRY_NODE_ID)!

    expect(plainEntry.type).toBe('eventNode')
    expect(railEntry.type).toBe('triggerRailNode')
    // The rail carries no trigger data in the graph: triggers are read at
    // render time, so nothing about them can be serialised.
    expect(railEntry.data).toEqual(plainEntry.data)
    expect(isEntryFlowNodeType(railEntry.type)).toBe(true)
  })

  it('saves the same graph and YAML whether the entry is the rail or the event node', () => {
    const plain = workflowToFlowElements(workflow)
    const rail = workflowToFlowElements(workflow, { entryNode: 'triggerRail' })

    const savedPlain = save(plain.nodes as Node[], plain.edges)
    const savedRail = save(rail.nodes as Node[], rail.edges)

    expect(savedRail).toEqual(savedPlain)
    expect(savedRail.nodes?.map((n) => n.id)).toEqual(['fetch', 'summarise'])
    expect(savedRail.entry).toEqual(['fetch'])
    expect(serializeWorkflowToYAML(savedRail)).toBe(serializeWorkflowToYAML(savedPlain))
    expect(serializeWorkflowToYAML(savedRail)).not.toMatch(/trigger/i)
  })
})
