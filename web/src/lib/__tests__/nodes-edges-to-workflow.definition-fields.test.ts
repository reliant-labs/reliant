/**
 * The builder rebuilds the saved definition from the canvas plus the
 * workflow-level fields it holds. Every top-level field the canvas does not
 * draw has to come back out unchanged, or a save from the builder silently
 * deletes it — a workflow's declared `triggers:` (its WHEN, see
 * research/INTEGRATIONS_V1_BRIEF.md §3a) being the costly one.
 */

import { describe, expect, it } from 'vitest'
import type { Node } from '@xyflow/react'
import type { Workflow } from '../../types/workflow'
import { workflowToFlowElements } from '../workflow-flow'
import { nodesEdgesToWorkflow } from '../nodes-edges-to-workflow'

const workflow: Workflow = {
  name: 'triage-new-issues',
  title: 'Triage new issues',
  hidden: true,
  resumeNode: 'triage',
  transitionTo: 'builtin://agent',
  daemon: { value: { case: 'expr', value: '{{ inputs.machine }}' } },
  description: 'Triage each new issue',
  entry: ['triage'],
  nodes: [{ id: 'triage', type: 'call_llm' }],
  triggers: [
    {
      name: 'new-issue',
      description: 'A new issue',
      filter: "!('wontfix' in trigger.payload.data.issue.labels)",
      inputs: { issue_number: '{{ trigger.payload.data.issue.number }}' },
      source: {
        case: 'integration',
        value: { integration: 'github', events: ['issues.opened'], match: { repository: 'acme/app' }, pollInterval: '' },
      },
    },
  ] as unknown as Workflow['triggers'],
}

function save(source: Workflow): Workflow {
  const { nodes, edges } = workflowToFlowElements(source, { entryNode: 'triggerRail' })
  return nodesEdgesToWorkflow(nodes as Node[], edges, {
    name: source.name ?? '',
    description: source.description ?? '',
    inputs: {},
    outputs: {},
    entry: source.entry,
    tag: undefined,
    presetDefault: undefined,
    apiVersion: undefined,
    isLocked: false,
    definition: source,
  })
}

describe('nodesEdgesToWorkflow keeps definition fields the canvas does not draw', () => {
  it('keeps declared triggers', () => {
    expect(save(workflow).triggers).toEqual(workflow.triggers)
  })

  it('keeps title, hidden, daemon, resume_node and transition_to', () => {
    const saved = save(workflow)
    expect(saved.title).toBe('Triage new issues')
    expect(saved.hidden).toBe(true)
    expect(saved.resumeNode).toBe('triage')
    expect(saved.transitionTo).toBe('builtin://agent')
    expect(saved.daemon).toEqual(workflow.daemon)
  })

  it('omits them when the workflow has none', () => {
    const saved = save({ name: 'plain', nodes: [{ id: 'a', type: 'call_llm' }], entry: ['a'] })
    expect(saved.triggers).toBeUndefined()
    expect(saved.title).toBeUndefined()
    expect(saved.hidden).toBeUndefined()
  })
})
