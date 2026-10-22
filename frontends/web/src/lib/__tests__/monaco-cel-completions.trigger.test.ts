/**
 * `trigger.*` autocompletes (research/WORKFLOW_UI.md §3.2). The backend's
 * catalog lists `trigger` as a dynamic namespace with no fields, so without a
 * client-side field list `trigger.` would complete nothing at all.
 */

import { describe, expect, it } from 'vitest'
import { parseCELContext, resolveCompletions, type CELCompletionContext } from '../monaco-cel-completions'

function ctx(celContext: CELCompletionContext['celContext']): CELCompletionContext {
  return { nodeIds: [], nodeTypeMap: {}, inputParams: {}, celContext, pureExpression: true }
}

function labelsAt(text: string, celContext: CELCompletionContext['celContext'] = 'default'): string[] {
  const parsed = parseCELContext(text, text.length, true)
  return resolveCompletions(parsed, ctx(celContext)).map((entry) => entry.label)
}

describe('trigger.* completions', () => {
  it('offers every trigger field after "trigger."', () => {
    const labels = labelsAt('trigger.')
    for (const field of ['kind', 'name', 'scheduled_for', 'trigger_id', 'event_id', 'occurred_at', 'payload']) {
      expect(labels).toContain(field)
    }
  })

  it('offers trigger.sender and its keys, for an "Only from" filter', () => {
    expect(labelsAt('trigger.')).toContain('sender')
    const labels = labelsAt('trigger.sender.')
    for (const field of ['kind', 'id', 'display_name', 'verified']) {
      expect(labels).toContain(field)
    }
  })

  it('offers the trigger namespace at the top level wherever the runtime binds it', () => {
    for (const celContext of ['default', 'loop_while', 'edge_condition', 'save_message'] as const) {
      expect(labelsAt('', celContext)).toContain('trigger')
    }
  })

  it('does not offer trigger where the runtime does not bind it', () => {
    expect(labelsAt('', 'thread')).not.toContain('trigger')
  })
})
