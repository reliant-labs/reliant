/**
 * Palette grouping is data-driven (research/WORKFLOW_UI.md §3.4 seam 1): the
 * palette groups by whatever key a node yields, defaulting to its category.
 * Integrations will pass a key that prefers `NodeInfo.integration` once that
 * field exists; this pins that a custom key is honoured end to end.
 */

import { describe, expect, it } from 'vitest'
import { groupPaletteNodes, type PaletteNode } from '../node-metadata'

const node = (id: string, category: string): PaletteNode => ({ id, category })

describe('groupPaletteNodes', () => {
  const nodes = [
    node('call_llm', 'agentic'),
    node('git_commit', 'git'),
    node('github_create_issue', 'utility'),
    node('read_file', 'utility'),
    node('mystery', ''),
  ]

  it('groups by category by default, in the preferred order, with blanks as utility', () => {
    const groups = groupPaletteNodes(nodes)
    expect(groups.map((g) => g.key)).toEqual(['agentic', 'utility', 'git'])
    expect(groups.find((g) => g.key === 'utility')?.nodes.map((n) => n.id)).toEqual([
      'github_create_issue',
      'read_file',
      'mystery',
    ])
  })

  it('groups by a custom key when one is given', () => {
    const integrationOf: Record<string, string> = { github_create_issue: 'github' }
    const groups = groupPaletteNodes(nodes, (n) => integrationOf[n.id] ?? n.category ?? '')

    expect(groups.map((g) => g.key)).toEqual(['agentic', 'utility', 'git', 'github'])
    expect(groups.find((g) => g.key === 'github')?.nodes.map((n) => n.id)).toEqual([
      'github_create_issue',
    ])
    expect(groups.find((g) => g.key === 'utility')?.nodes.map((n) => n.id)).toEqual([
      'read_file',
      'mystery',
    ])
  })
})
