/**
 * `api.workflows.list` rebuilds each workflow as a field whitelist rather than
 * passing the wire object through. That is fine until a consumer needs a field
 * nobody remembered to copy — and the failure is silent, because the object
 * still looks well-formed.
 *
 * The concrete bug: `step_count` was copied but `nodes`/`edges` were not, so
 * the mobile catalog row said "17 steps" while the detail screen one tap away
 * rendered "This workflow has no steps" for all 23 workflows. Desktop never
 * caught it because it fetches the graph through a different call.
 */

import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  listWorkflows: vi.fn(),
}))

vi.mock('../workflow-grpc', () => ({
  workflowGrpc: { listWorkflows: mocks.listWorkflows },
}))

/** Shaped like a real ListWorkflows entry, trimmed to what this asserts. */
function wireWorkflow() {
  return {
    name: 'gsd',
    filename: 'gsd.yaml',
    description: 'Get stuff done',
    stepCount: 17,
    source: 'builtin',
    isHidden: false,
    isValid: true,
    nodes: Array.from({ length: 17 }, (_, i) => ({ id: `n${i}` })),
    edges: Array.from({ length: 16 }, (_, i) => ({
      from: `n${i}`,
      to: `n${i + 1}`,
    })),
  }
}

describe('api.workflows.list', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.listWorkflows.mockResolvedValue([wireWorkflow()])
  })

  it('carries the graph through, not just the step count', async () => {
    const { api } = await import('../client')
    const { workflows } = await api.workflows.list('project-1')

    expect(workflows[0].nodes).toHaveLength(17)
    expect(workflows[0].edges).toHaveLength(16)
  })

  it('keeps step_count and the graph in agreement', async () => {
    // The whole bug was these two disagreeing. A workflow that reports N steps
    // must carry N nodes, or two screens describe the same thing differently.
    const { api } = await import('../client')
    const { workflows } = await api.workflows.list('project-1')

    const wf = workflows[0]
    expect(wf.nodes?.length).toBe(wf.step_count)
  })

  it('still maps the descriptive fields it always did', async () => {
    const { api } = await import('../client')
    const { workflows } = await api.workflows.list('project-1')

    expect(workflows[0]).toMatchObject({
      name: 'gsd',
      filename: 'gsd.yaml',
      description: 'Get stuff done',
      step_count: 17,
      source: 'builtin',
      is_hidden: false,
      is_valid: true,
    })
  })

  it('tolerates a workflow that genuinely has no graph', async () => {
    // An invalid or unparsed workflow legitimately has no nodes; that must
    // stay distinguishable from the bug, where nodes were dropped in transit.
    mocks.listWorkflows.mockResolvedValue([
      { ...wireWorkflow(), stepCount: 0, nodes: undefined, edges: undefined },
    ])

    const { api } = await import('../client')
    const { workflows } = await api.workflows.list('project-1')

    expect(workflows[0].nodes).toBeUndefined()
    expect(workflows[0].step_count).toBe(0)
  })
})
