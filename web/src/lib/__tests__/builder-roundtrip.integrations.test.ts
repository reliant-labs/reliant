/**
 * The builder round-trips integration workflows losslessly.
 *
 * The canonical YAML is the server's: MarshalWorkflow writes it from the
 * Workflow proto the builder sends (internal/workflow/yaml, goldens under
 * testdata/triggers). So "the UI writes the same YAML the agent tools do"
 * holds exactly when the builder's load → edit → save sends back the same
 * proto it was given, plus the edit. This drives the real conversion path:
 * proto JSON → Workflow → canvas (workflowToFlowElements) → step edits
 * (actionNodeArgs, the WorkflowMutationContext's payload) → nodesEdgesToWorkflow
 * → the SaveWorkflow request proto (toWorkflowInit + create).
 */

import { describe, expect, it } from 'vitest'
import type { Node } from '@xyflow/react'
import { create, fromJson, toJson } from '@bufbuild/protobuf'

import { WorkflowSchema } from '../../gen/reliant/v1/workflow_v2_pb'
import type { Workflow, Step } from '../../types/workflow'
import { workflowToFlowElements, type FlowNodeData } from '../workflow-flow'
import { nodesEdgesToWorkflow } from '../nodes-edges-to-workflow'
import { newActionStep, withActionConnection, withActionParam } from '../actionNodeArgs'

/**
 * internal/workflow/yaml/testdata/triggers/integration.golden.yaml, plus an
 * action node, as the server's GetWorkflow returns it (proto JSON).
 */
const canonical = {
  name: 'triage-new-issues',
  description: 'Triage each new issue in the repo.',
  inputs: { issue_number: { type: 'integer' }, title: { type: 'string' } },
  entry: ['triage'],
  triggers: [
    {
      name: 'new-issue',
      description: 'A new or reopened issue',
      integration: { integration: 'github', events: ['issues.opened', 'issues.labeled'], match: { repository: 'reliant-labs/reliant' } },
      filter: "!('wontfix' in trigger.payload.data.issue.labels)",
      inputs: {
        issue_number: '{{ trigger.payload.data.issue.number }}',
        title: '#{{ trigger.payload.data.issue.number }}: {{ trigger.payload.data.issue.title }}',
      },
    },
    { name: 'polled', integration: { integration: 'test-poll', events: ['*'], pollInterval: '10m' } },
  ],
  nodes: [
    { id: 'triage', type: 'call_llm' },
    {
      id: 'open_issue',
      type: 'action',
      action: {
        uses: { literal: 'github/issue.create@1' },
        with: { owner: 'reliant-labs', repo: 'reliant', title: '{{ inputs.title }}', labels: ['triage'] },
        connection: { literal: 'conn-1' },
      },
    },
  ],
  edges: [{ from: 'triage', default: ['open_issue'] }],
  ui: { positions: { workflow: { x: 40, y: 200 }, triage: { x: 400, y: 200 }, open_issue: { x: 700, y: 200 } } },
}

/** What the builder holds after GetWorkflow: the decoded proto. */
function loaded(): Workflow {
  return fromJson(WorkflowSchema, canonical as never) as unknown as Workflow
}

/** The builder's save path, from a held definition and its canvas. */
function save(definition: Workflow, nodes: Node[], edges: ReturnType<typeof workflowToFlowElements>['edges']) {
  const workflow = nodesEdgesToWorkflow(nodes, edges, {
    name: definition.name ?? '',
    description: definition.description ?? '',
    inputs: definition.inputs ?? {},
    outputs: definition.outputs ?? {},
    entry: definition.entry,
    tag: definition.presets?.tag,
    presetDefault: definition.presets?.default,
    apiVersion: definition.apiVersion,
    isLocked: false,
    definition,
  })
  // toWorkflowInit's contract: strip proto metadata and build the request message.
  const plain = JSON.parse(JSON.stringify(workflow, (k, v) => (k === '$typeName' || k === '$unknown' ? undefined : v)))
  return toJson(WorkflowSchema, create(WorkflowSchema, plain))
}

function canvas(definition: Workflow) {
  const { nodes, edges } = workflowToFlowElements(definition, { entryNode: 'triggerRail' })
  return { nodes: nodes as Node[], edges }
}

describe('builder round-trip of an integration workflow', () => {
  it('saves exactly the proto it loaded: declared triggers and the action node intact', () => {
    const definition = loaded()
    const { nodes, edges } = canvas(definition)
    const saved = save(definition, nodes, edges) as typeof canonical
    const expected = structuredClone(toJson(WorkflowSchema, fromJson(WorkflowSchema, canonical as never))) as typeof canonical
    // The start node's on-canvas position is re-laid-out on load (pre-existing,
    // and UI-only); everything that is part of the definition is byte-equal.
    delete (saved.ui.positions as Record<string, unknown>).workflow
    delete (expected.ui.positions as Record<string, unknown>).workflow
    expect(saved).toEqual(expected)
  })

  it('carries an edit of the action node and nothing else', () => {
    const definition = loaded()
    const { nodes, edges } = canvas(definition)
    const edited = nodes.map((node) => {
      if (node.id !== 'open_issue') return node
      let step = (node.data as FlowNodeData).step as Step
      step = withActionParam(step, 'body', 'Reported by {{ trigger.payload.data.issue.user.login }}')
      step = withActionConnection(step, '')
      return { ...node, data: { ...node.data, step } }
    })

    const saved = save(definition, edited, edges) as typeof canonical
    const action = saved.nodes.find((n) => n.id === 'open_issue')!.action!
    expect(action.with).toEqual({
      owner: 'reliant-labs',
      repo: 'reliant',
      title: '{{ inputs.title }}',
      labels: ['triage'],
      body: 'Reported by {{ trigger.payload.data.issue.user.login }}',
    })
    expect(action).not.toHaveProperty('connection')
    expect(saved.triggers).toEqual(canonical.triggers)
  })

  it('saves a palette-added action node in the canonical shape', () => {
    const definition = loaded()
    const { nodes, edges } = canvas(definition)
    const step = newActionStep('post_message', 'slack/message.post@1', { unfurl_links: true })
    const added: Node = { id: 'post_message', type: 'actionNode', position: { x: 1000, y: 200 }, data: { step, label: 'post_message' } }

    const saved = save(definition, [...nodes, added], edges) as typeof canonical
    expect(saved.nodes.find((n) => n.id === 'post_message')).toEqual({
      id: 'post_message',
      type: 'action',
      action: { uses: { literal: 'slack/message.post@1' }, with: { unfurl_links: true } },
    })
  })
})
