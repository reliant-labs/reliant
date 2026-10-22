import { describe, it, expect } from 'vitest'
import { getCollapsedLoopNodeIds } from './useExpandedLoops'

describe('getCollapsedLoopNodeIds', () => {
  it('removes collapsed loop and all descendants', () => {
    const ids = [
      'outer',
      'outer:inner',
      'outer:inner:deep',
      'other',
      'other:child',
    ]

    const remaining = getCollapsedLoopNodeIds('outer', ids)

    expect(remaining).toEqual(['other', 'other:child'])
  })

  it('keeps unrelated siblings when collapsing a nested loop', () => {
    const ids = [
      'outer',
      'outer:inner-a',
      'outer:inner-a:deep',
      'outer:inner-b',
      'outer:inner-b:deep',
    ]

    const remaining = getCollapsedLoopNodeIds('outer:inner-a', ids)

    expect(remaining).toEqual(['outer', 'outer:inner-b', 'outer:inner-b:deep'])
  })
})
