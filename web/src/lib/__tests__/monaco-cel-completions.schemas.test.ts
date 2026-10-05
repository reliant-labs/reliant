/**
 * Integration schemas reach CEL completion (research/INTEGRATIONS_V1_BRIEF.md
 * §3a): an action node's output_schema completes `nodes.<id>.data.*`, and a
 * trigger's payload_schema completes `trigger.payload.*`, at any depth.
 */

import { describe, expect, it } from 'vitest'
import { parseCELContext, resolveCompletions, type CELCompletionContext } from '../monaco-cel-completions'

const issueOutput = {
  type: 'object',
  properties: {
    number: { type: 'integer', description: 'The issue number within the repository.' },
    html_url: { type: 'string' },
    user: { type: 'object', properties: { login: { type: 'string' } } },
  },
}

const issuePayload = {
  type: 'object',
  properties: {
    event: { type: 'string' },
    data: {
      type: 'object',
      properties: { issue: { type: 'object', properties: { number: { type: 'integer' }, title: { type: 'string' } } } },
    },
  },
}

function ctx(): CELCompletionContext {
  return {
    nodeIds: ['open_issue'],
    nodeTypeMap: { open_issue: 'action' },
    inputParams: {},
    celContext: 'default',
    pureExpression: true,
    nodeOutputSchemas: { open_issue: issueOutput },
    triggerPayloadSchema: issuePayload,
  }
}

function labelsAt(text: string): string[] {
  const parsed = parseCELContext(text, text.length, true)
  return resolveCompletions(parsed, ctx()).map((entry) => entry.label)
}

describe('schema-driven CEL completions', () => {
  it('offers data beside the action outputs, then the output schema under it', () => {
    expect(labelsAt('nodes.open_issue.')).toContain('data')
    const fields = labelsAt('nodes.open_issue.data.')
    expect(fields).toEqual(expect.arrayContaining(['number', 'html_url', 'user']))
  })

  it('walks nested object properties', () => {
    expect(labelsAt('nodes.open_issue.data.user.')).toContain('login')
  })

  it('completes trigger.payload from the payload schema', () => {
    expect(labelsAt('trigger.payload.')).toEqual(expect.arrayContaining(['event', 'data']))
    expect(labelsAt('trigger.payload.data.issue.')).toEqual(expect.arrayContaining(['number', 'title']))
  })

  it('carries the schema description and type into the entry', () => {
    const parsed = parseCELContext('nodes.open_issue.data.', 'nodes.open_issue.data.'.length, true)
    const number = resolveCompletions(parsed, ctx()).find((entry) => entry.label === 'number')!
    expect(number.detail).toBe('integer')
    expect(number.documentation).toBe('The issue number within the repository.')
  })
})
