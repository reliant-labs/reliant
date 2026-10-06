/**
 * Palette grouping is data-driven (research/WORKFLOW_UI.md §3.4 seam 1): the
 * palette groups by whatever key a node yields, defaulting to its category.
 * Integrations will pass a key that prefers `NodeInfo.integration` once that
 * field exists; this pins that a custom key is honoured end to end.
 */

import { describe, expect, it } from 'vitest'
import {
  ADVANCED_GROUP,
  builderGroupKey,
  getNodeIcon,
  getNodeTheme,
  groupPaletteNodes,
  isAgentBuildingBlock,
  type PaletteNode,
} from '../node-metadata'

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

describe('builderGroupKey: agent building blocks apart, under Advanced', () => {
  const agentic = ['workflow', 'call_llm', 'invoke_tool', 'execute_tools', 'compact', 'save_message'].map((id) => node(id, 'agentic'))

  it('moves the pieces the Agent step runs for you to a trailing Advanced group', () => {
    const groups = groupPaletteNodes([...agentic, node('approval', 'utility')], builderGroupKey)
    expect(groups.map((g) => g.key)).toEqual(['agentic', 'utility', ADVANCED_GROUP])
    expect(groups.find((g) => g.key === ADVANCED_GROUP)?.nodes.map((n) => n.id)).toEqual(['execute_tools', 'compact', 'save_message'])
    // A single model call, and a tool the author picks, are complete steps.
    expect(groups.find((g) => g.key === 'agentic')?.nodes.map((n) => n.id)).toEqual(['workflow', 'call_llm', 'invoke_tool'])
  })

  it('keeps Advanced last even after categories it does not know', () => {
    const groups = groupPaletteNodes([node('execute_tools', 'agentic'), node('zzz', 'zebra'), node('aaa', 'aardvark')], builderGroupKey)
    expect(groups.map((g) => g.key)).toEqual(['aardvark', 'zebra', ADVANCED_GROUP])
  })

  it('names the building blocks by node type', () => {
    expect(isAgentBuildingBlock('execute_tools')).toBe(true)
    expect(isAgentBuildingBlock('invoke_tool')).toBe(false)
    expect(isAgentBuildingBlock('call_llm')).toBe(false)
  })
})

describe('Run Tool (invoke_tool) has its own look', () => {
  it('does not share Call LLM\'s icon or Run LLM Tool Calls\' colour', () => {
    expect(getNodeIcon('invoke_tool')).not.toBe(getNodeIcon('call_llm'))
    expect(getNodeIcon('invoke_tool')).not.toBe(getNodeIcon('execute_tools'))
    expect(getNodeTheme('invoke_tool')).not.toBe(getNodeTheme('execute_tools'))
    expect(getNodeTheme('invoke_tool')).not.toBe(getNodeTheme('call_llm'))
  })
})
