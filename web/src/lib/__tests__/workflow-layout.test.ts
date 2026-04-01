import { describe, it, expect } from 'vitest'
import { autoLayoutWorkflow } from '../workflow-layout'
import type { Workflow } from '../../types/workflow'

describe('Workflow Auto-Layout', () => {
  it('should auto-layout a simple linear workflow', () => {
    const workflow: Workflow = {
      name: 'linear-workflow',
      nodes: [
        { id: 'step1', type: 'run', command: 'echo "1"' },
        { id: 'step2', type: 'run', command: 'echo "2"' },
        { id: 'step3', type: 'run', command: 'echo "3"' },
      ],
      edges: [
        { from: 'step1', default: ['step2'] },
        { from: 'step2', default: ['step3'] },
      ],
    }

    const layouted = autoLayoutWorkflow(workflow)

    // All nodes should have positions in ui.positions
    expect(layouted.ui?.positions?.['step1']).toBeDefined()
    expect(layouted.ui?.positions?.['step2']).toBeDefined()
    expect(layouted.ui?.positions?.['step3']).toBeDefined()

    // Nodes should be arranged left-to-right (increasing X)
    const pos1 = layouted.ui!.positions!['step1']!
    const pos2 = layouted.ui!.positions!['step2']!
    const pos3 = layouted.ui!.positions!['step3']!

    expect(pos2.x).toBeGreaterThanOrEqual(pos1.x)
    expect(pos3.x).toBeGreaterThanOrEqual(pos2.x)
  })

  it('should handle branching workflows', () => {
    const workflow: Workflow = {
      name: 'branching-workflow',
      nodes: [
        { id: 'start', type: 'run', command: 'echo "start"' },
        { id: 'branch-a', type: 'run', command: 'echo "A"' },
        { id: 'branch-b', type: 'run', command: 'echo "B"' },
        { id: 'merge', type: 'run', command: 'echo "merge"' },
      ],
      edges: [
        { 
          from: 'start', 
          cases: [
            { to: ['branch-a'], condition: 'a' },
            { to: ['branch-b'], condition: 'b' },
          ],
        },
        { from: 'branch-a', default: ['merge'] },
        { from: 'branch-b', default: ['merge'] },
      ],
    }

    const layouted = autoLayoutWorkflow(workflow)

    // All nodes should have positions in ui.positions
    layouted.nodes.forEach(step => {
      expect(layouted.ui?.positions?.[step.id!]).toBeDefined()
    })

    // Start should be leftmost
    const startPos = layouted.ui!.positions!['start']!
    const mergePos = layouted.ui!.positions!['merge']!

    // Merge should be to the right of or at same X as start (workflows flow left-to-right)
    expect(mergePos.x).toBeGreaterThanOrEqual(startPos.x)
  })

  it('should re-layout workflows even if they have existing positions', () => {
    const workflow: Workflow = {
      name: 'positioned-workflow',
      nodes: [
        { id: 'step1', type: 'run', command: 'echo "1"' },
        { id: 'step2', type: 'run', command: 'echo "2"' },
      ],
      edges: [
        { from: 'step1', default: ['step2'] },
      ],
      ui: {
        positions: {
          step1: { x: 100, y: 100 },
          step2: { x: 200, y: 100 },
        },
      },
    }

    const layouted = autoLayoutWorkflow(workflow)

    // Positions should be re-laid out (autoLayoutWorkflow clears and re-calculates positions)
    expect(layouted.ui?.positions?.['step1']).toBeDefined()
    expect(layouted.ui?.positions?.['step2']).toBeDefined()
    // Nodes should be arranged left-to-right
    expect(layouted.ui!.positions!['step2']!.x).toBeGreaterThanOrEqual(layouted.ui!.positions!['step1']!.x)
  })

  it('should layout workflows with workflow and run steps', () => {
    const workflow: Workflow = {
      name: 'workflow-with-run-steps',
      nodes: [
        { id: 'workflow-1', type: 'workflow', ref: 'builtin://agent' },
        { id: 'safety-check', type: 'run', command: 'check' },
        { id: 'success', type: 'run', command: 'echo success' },
      ],
      edges: [
        { from: 'workflow-1', default: ['safety-check'] },
        { from: 'safety-check', default: ['workflow-1'] },
        { from: 'workflow-1', cases: [{ to: ['success'], condition: 'output.approved == true' }] },
      ],
    }

    const layouted = autoLayoutWorkflow(workflow)

    // All nodes should have positions in ui.positions
    layouted.nodes.forEach(step => {
      expect(layouted.ui?.positions?.[step.id!]).toBeDefined()
    })
  })

  it('should handle fan-out targets in case and default edges', () => {
    const workflow: Workflow = {
      name: 'fanout-edge-layout',
      nodes: [
        { id: 'source', type: 'run', command: 'echo source' },
        { id: 'target-a', type: 'run', command: 'echo a' },
        { id: 'target-b', type: 'run', command: 'echo b' },
        { id: 'target-c', type: 'run', command: 'echo c' },
      ],
      edges: [
        {
          from: 'source',
          cases: [
            {
              to: ['target-a', 'target-b'],
              condition: 'inputs.route == "case"',
            },
          ],
          default: ['target-c'],
        } as Workflow['edges'][number],
      ],
    }

    const layouted = autoLayoutWorkflow(workflow)

    expect(layouted.ui?.positions?.['source']).toBeDefined()
    expect(layouted.ui?.positions?.['target-a']).toBeDefined()
    expect(layouted.ui?.positions?.['target-b']).toBeDefined()
    expect(layouted.ui?.positions?.['target-c']).toBeDefined()
    expect(layouted.ui?.positions?.['switch-source']).toBeDefined()
  })

  it('should create distinct switch nodes for different source events', () => {
    const workflow: Workflow = {
      name: 'event-scoped-switches',
      nodes: [
        { id: 'run-1', type: 'run', command: 'echo start' },
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

    const layouted = autoLayoutWorkflow(workflow)

    expect(layouted.ui?.positions?.['switch-run-1_x2e_completed']).toBeDefined()
    expect(layouted.ui?.positions?.['switch-run-1_x2e_failed']).toBeDefined()
  })

  it('should normalize scalar edge targets for backward-compatible parsing', () => {
    const workflow = {
      name: 'legacy-scalar-targets',
      nodes: [
        { id: 'start', type: 'run', command: 'echo start' },
        { id: 'branch-a', type: 'run', command: 'echo a' },
        { id: 'branch-b', type: 'run', command: 'echo b' },
      ],
      edges: [
        {
          from: 'start',
          cases: [
            { to: 'branch-a', condition: 'inputs.route == "a"' },
            { to: 'branch-b', condition: 'inputs.route == "b"' },
          ],
        },
      ],
    } as unknown as Workflow

    const layouted = autoLayoutWorkflow(workflow)

    expect(layouted.ui?.positions?.['switch-start']).toBeDefined()
  })
})
