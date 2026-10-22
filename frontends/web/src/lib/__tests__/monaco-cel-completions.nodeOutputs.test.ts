/**
 * `{{ nodes.<id>. }}` completion offers exactly what the step's Outputs tab
 * and Insert data offer: one source, the catalog's ListNodes output fields
 * (lib/nodeOutputFields). It used to read GetCELCompletions' node output
 * schemas instead, which listed debug plumbing (last_stream_seq) and missed
 * fields the other two showed (tool_calls[].name) — so typing and picking gave
 * different answers about the same step.
 */

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { create } from '@bufbuild/protobuf'

import { NodeInfoSchema, NodeInputFieldSchema, CELFieldInfoSchema } from '../../gen/reliant/v1/catalog_pb'

const nodes = vi.hoisted(() => ({ list: [] as unknown[] }))
vi.mock('../node-metadata', () => ({ getCachedNodes: () => nodes.list }))

// The other source, deliberately different: if completion read it, the
// assertions below would see `stale_field` and `last_stream_seq`.
vi.mock('../cel-completion-service', () => ({
  getCELNamespaces: () => [],
  getCELGlobalFunctions: () => [],
  getCELMemberFunctions: () => [],
  getNamespaceFields: () => [],
  getNodeOutputSchema: (nodeType: string) =>
    nodeType === 'call_llm'
      ? [
          create(CELFieldInfoSchema, { name: 'stale_field', type: 'string' }),
          create(CELFieldInfoSchema, { name: 'last_stream_seq', type: 'int' }),
        ]
      : [],
}))

import { parseCELContext, resolveCompletions, type CELCompletionContext } from '../monaco-cel-completions'
import { insertableData } from '../insertableData'
import { outputFieldsForNodeType } from '../nodeOutputFields'

const field = (name: string, type: string, extra: { children?: ReturnType<typeof field>[]; advanced?: boolean } = {}) =>
  create(NodeInputFieldSchema, {
    name,
    type,
    description: `${name} description`,
    children: extra.children ?? [],
    visibilityContexts: extra.advanced ? ['advanced'] : [],
  })

const context: CELCompletionContext = {
  nodeIds: ['llm', 'next'],
  nodeTypeMap: { llm: 'call_llm', next: 'call_llm' },
  inputParams: {},
  celContext: 'default',
  pureExpression: false,
  nodeDeclaredOutputs: {},
}
// `llm` runs before `next`, so Insert data in `next` offers llm's outputs.
const insertContext = { ...context, edges: [{ source: 'llm', target: 'next' }] }

function labelsAt(text: string): string[] {
  return resolveCompletions(parseCELContext(text, text.length, false), context)
    .filter((entry) => entry.kind === 'field')
    .map((entry) => entry.label)
}

beforeEach(() => {
  nodes.list = [
    create(NodeInfoSchema, {
      id: 'call_llm',
      displayName: 'Call LLM',
      outputFields: [
        field('response_text', 'string'),
        field('tool_calls', 'array', { children: [field('name', 'string'), field('arguments', 'string')] }),
        field('message', 'message', { children: [field('text', 'string'), field('seq', 'int', { advanced: true })] }),
        field('last_stream_seq', 'int', { advanced: true }),
      ],
    }),
  ]
})

describe('{{ nodes.<id>. }} completion reads the Outputs tab source', () => {
  it('offers the step type output fields, hiding the advanced ones, never the CEL-completion schema', () => {
    expect(labelsAt('Hi {{ nodes.llm.')).toEqual(['response_text', 'tool_calls', 'message'])
  })

  it('offers the same top-level fields as Insert data', () => {
    const groups = insertableData({ context: insertContext, currentNodeId: 'next', nodeOutputFields: outputFieldsForNodeType })
    const llm = groups.find((group) => group.id === 'node:llm')!
    const insertable = llm.fields.map((f) => f.path.slice('nodes.llm.'.length)).filter((path) => !/[.[]/.test(path))
    expect(labelsAt('{{ nodes.llm.')).toEqual(insertable)
  })

  it('completes a message field and a list item through the paths Insert data inserts', () => {
    expect(labelsAt('{{ nodes.llm.message.')).toEqual(['text'])
    expect(labelsAt('{{ nodes.llm.tool_calls[0].')).toEqual(['name', 'arguments'])
    const groups = insertableData({ context: insertContext, currentNodeId: 'next', nodeOutputFields: outputFieldsForNodeType })
    const paths = groups.find((group) => group.id === 'node:llm')!.fields.map((f) => f.path)
    expect(paths).toEqual(expect.arrayContaining(['nodes.llm.message.text', 'nodes.llm.tool_calls[0].name']))
  })
})
