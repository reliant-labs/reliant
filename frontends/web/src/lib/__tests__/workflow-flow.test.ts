import { describe, expect, it } from 'vitest'
import type { Workflow } from '../../types/workflow'
import { convertEdgesToFlowElements, workflowToFlowElements } from '../workflow-flow'

describe('workflow-flow switch behavior', () => {
  it('should create distinct switch nodes for event-scoped sources', () => {
    const workflow: Workflow = {
      name: 'event-scoped-switches',
      nodes: [
        { id: 'run-1', type: 'run', command: 'echo run' },
        { id: 'success', type: 'run', command: 'echo success' },
        { id: 'failure', type: 'run', command: 'echo failure' },
      ],
      edges: [
        {
          from: 'run-1.completed',
          cases: [
            { to: ['success'], condition: 'output.exit_code == 0' },
            { to: ['failure'], condition: 'output.exit_code != 0' },
          ],
        },
        {
          from: 'run-1.failed',
          cases: [
            { to: ['failure'], condition: 'true' },
            { to: ['success'], condition: 'false' },
          ],
        },
      ],
    }

    const elements = workflowToFlowElements(workflow)

    const switchIds = elements.nodes
      .filter((node) => node.type === 'switchNode')
      .map((node) => node.id)
      .sort()

    expect(switchIds).toEqual(['switch-run-1_x2e_completed', 'switch-run-1_x2e_failed'])
  })

  it('should not render orphan switches that only exist in ui.switches metadata', () => {
    const workflow: Workflow = {
      name: 'orphan-switch-metadata',
      nodes: [{ id: 'step-1', type: 'run', command: 'echo 1' }],
      edges: [],
      ui: {
        switches: {
          'switch-orphan': {
            sourceNode: 'step-1',
            position: { x: 300, y: 200 },
            cases: [{ id: 'case-0', condition: { expr: 'true' }, label: 'always' }],
          },
        },
      },
    }

    const elements = workflowToFlowElements(workflow)

    const switchNodes = elements.nodes.filter((node) => node.type === 'switchNode')
    expect(switchNodes).toHaveLength(0)
  })

  it('should preserve sourceEvent on source-to-switch edges for eventful sources', () => {
    const existingNodes = [
      {
        id: 'run-1',
        type: 'runNode',
        position: { x: 100, y: 100 },
        data: { label: 'run-1' },
      },
      {
        id: 'target-a',
        type: 'runNode',
        position: { x: 400, y: 100 },
        data: { label: 'target-a' },
      },
      {
        id: 'target-b',
        type: 'runNode',
        position: { x: 400, y: 250 },
        data: { label: 'target-b' },
      },
    ]

    const workflowEdges: Workflow['edges'] = [
      {
        from: 'run-1.failed',
        cases: [
          { to: ['target-a'], condition: 'true' },
          { to: ['target-b'], condition: 'false' },
        ],
      },
    ]

    const { edges } = convertEdgesToFlowElements(
      workflowEdges || [],
      existingNodes as never,
      undefined,
      undefined,
      true,
    )

    const sourceToSwitchEdge = edges.find(
      (edge) => edge.source === 'run-1' && edge.target.startsWith('switch-run-1_x2e_failed'),
    )

    expect(sourceToSwitchEdge).toBeDefined()
    expect((sourceToSwitchEdge?.data as { sourceEvent?: string } | undefined)?.sourceEvent).toBe('failed')
  })

  it('should normalize legacy scalar targets in convertEdgesToFlowElements', () => {
    const existingNodes = [
      {
        id: 'source',
        type: 'runNode',
        position: { x: 100, y: 100 },
        data: { label: 'source' },
      },
      {
        id: 'target-a',
        type: 'runNode',
        position: { x: 400, y: 100 },
        data: { label: 'target-a' },
      },
      {
        id: 'target-b',
        type: 'runNode',
        position: { x: 400, y: 250 },
        data: { label: 'target-b' },
      },
    ]

    const legacyWorkflowEdges = [
      {
        from: 'source',
        cases: [
          { to: 'target-a', condition: 'inputs.route == "a"' },
          { to: 'target-b', condition: 'inputs.route == "b"' },
        ],
      },
    ] as unknown as Workflow['edges']

    const { switchNodes, edges } = convertEdgesToFlowElements(
      legacyWorkflowEdges || [],
      existingNodes as never,
      undefined,
      undefined,
      true,
    )

    expect(switchNodes).toHaveLength(1)
    expect(edges.some((edge) => edge.target === 'target-a')).toBe(true)
    expect(edges.some((edge) => edge.target === 'target-b')).toBe(true)
  })

  it('should honor workflowStartLabel for the workflow start node', () => {
    const workflow: Workflow = {
      name: 'inline-loop-body',
      nodes: [{ id: 'step-1', type: 'run', command: 'echo 1' }],
      edges: [],
    }

    const defaultElements = workflowToFlowElements(workflow)
    const defaultStart = defaultElements.nodes.find((n) => n.id === 'workflow')
    expect(defaultStart?.data.label).toBe('Workflow Start')

    const loopElements = workflowToFlowElements(workflow, { workflowStartLabel: 'Loop Start' })
    const loopStart = loopElements.nodes.find((n) => n.id === 'workflow')
    expect(loopStart?.data.label).toBe('Loop Start')

    // Empty-workflow path also honors the label
    const emptyElements = workflowToFlowElements(
      { name: 'empty', nodes: [], edges: [] },
      { workflowStartLabel: 'Loop Start' },
    )
    expect(emptyElements.nodes[0]?.data.label).toBe('Loop Start')
  })

  it('should restore event-scoped switch metadata by canonical switch id', () => {
    const existingNodes = [
      {
        id: 'run-1',
        type: 'runNode',
        position: { x: 100, y: 120 },
        data: { label: 'run-1' },
      },
      {
        id: 'target-a',
        type: 'runNode',
        position: { x: 500, y: 120 },
        data: { label: 'target-a' },
      },
      {
        id: 'target-b',
        type: 'runNode',
        position: { x: 500, y: 260 },
        data: { label: 'target-b' },
      },
    ]

    const workflowEdges: Workflow['edges'] = [
      {
        from: 'run-1.failed',
        cases: [
          { to: ['target-a'], condition: 'true' },
          { to: ['target-b'], condition: 'false' },
        ],
      },
    ]

    const savedSwitches = {
      'switch-run-1_x2e_failed': {
        sourceNode: 'run-1',
        position: { x: 777, y: 333 },
        cases: [
          { id: 'case-0', condition: { expr: 'true' }, label: 'yes' },
          { id: 'case-1', condition: { expr: 'false' }, label: 'no' },
        ],
      },
    }

    const { switchNodes } = convertEdgesToFlowElements(
      workflowEdges || [],
      existingNodes as never,
      savedSwitches,
      undefined,
      true,
    )

    expect(switchNodes).toHaveLength(1)
    expect(switchNodes[0].id).toBe('switch-run-1_x2e_failed')
    expect(switchNodes[0].position).toEqual({ x: 777, y: 333 })
  })
})
