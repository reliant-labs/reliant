import { describe, it, expect } from 'vitest'
import type { Step, Workflow } from '../../types/workflow'
import { getLoopInline, getLoopRef, getLoopWhile, getSubWorkflowInline, getSubWorkflowRef } from '../workflow-step-accessors'

describe('workflow-step-accessors', () => {
  const inlineWorkflow: Workflow = {
    name: 'inline-loop',
    nodes: [{ id: 'run-inner', type: 'run', command: 'echo hi' }],
    edges: [],
    entry: ['run-inner'],
  }

  it('reads loop fields from flattened step properties', () => {
    const step: Step = {
      id: 'loop-1',
      type: 'loop',
      ref: 'builtin://agent',
      inline: inlineWorkflow,
      while: 'iter.iteration < 5',
    }

    expect(getLoopRef(step)).toBe('builtin://agent')
    expect(getLoopInline(step)).toEqual(inlineWorkflow)
    expect(getLoopWhile(step)).toBe('iter.iteration < 5')
  })

  it('falls back to loop args oneof when flattened properties are absent', () => {
    const step = {
      id: 'loop-2',
      type: 'loop',
      args: {
        case: 'loop',
        value: {
          ref: { value: { case: 'literal', value: 'builtin://agent' } },
          inline: inlineWorkflow,
          while: { expr: 'iter.iteration < 3' },
        },
      },
    } as unknown as Step

    expect(getLoopRef(step)).toBe('builtin://agent')
    expect(getLoopInline(step)).toEqual(inlineWorkflow)
    expect(getLoopWhile(step)).toBe('iter.iteration < 3')
  })

  it('reads inline workflow for workflow nodes from workflow args oneof', () => {
    const step = {
      id: 'workflow-1',
      type: 'workflow',
      args: {
        case: 'workflow',
        value: {
          ref: { value: { case: 'literal', value: 'builtin://agent' } },
          inline: inlineWorkflow,
        },
      },
    } as unknown as Step

    expect(getSubWorkflowRef(step)).toBe('builtin://agent')
    expect(getSubWorkflowInline(step)).toEqual(inlineWorkflow)
  })
})
