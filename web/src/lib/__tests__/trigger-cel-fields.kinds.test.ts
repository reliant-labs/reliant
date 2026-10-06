/**
 * `trigger.kind` is documented in the builder (the Workflow start panel and
 * Monaco completion) from TRIGGER_KINDS, which mirrors core.TriggerEventKind.
 * The mirror once listed three of seven kinds, so authors writing a filter
 * for a webhook run never learned "webhook" was a value.
 *
 * This reads the Go declarations rather than restating them, so adding a
 * kind in Go without describing it here fails, and so does the reverse. Same
 * idea as the isUnattendedLaunchKind mirror in runStatus.test.ts, with the
 * other side read from source instead of typed out again.
 */

import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

import { TRIGGER_CEL_FIELDS, TRIGGER_KINDS } from '../trigger-cel-fields'

const GO_TRIGGER_KINDS = resolve(process.cwd(), '../internal/db/core/trigger.go')

/** Every `TriggerEventKindX TriggerEventKind = "x"` constant, in order. */
function goTriggerEventKinds(): string[] {
  const source = readFileSync(GO_TRIGGER_KINDS, 'utf8')
  return [...source.matchAll(/^\s*TriggerEventKind\w+\s+TriggerEventKind\s*=\s*"([^"]+)"/gm)].map((match) => match[1]!)
}

describe('trigger.kind: core.TriggerEventKind, mirrored', () => {
  it('lists exactly the kinds Go declares, in the same order', () => {
    const goKinds = goTriggerEventKinds()
    // A parse that found nothing would make the comparison vacuous.
    expect(goKinds.length).toBeGreaterThanOrEqual(7)
    expect([...TRIGGER_KINDS]).toEqual(goKinds)
  })

  it('names every kind in the description the builder shows', () => {
    const description = TRIGGER_CEL_FIELDS.find((field) => field.name === 'kind')?.description ?? ''
    for (const kind of TRIGGER_KINDS) expect(description).toContain(`"${kind}"`)
    expect(description).toBe(
      'What started the run: "chat.start", "schedule", "agent.start_run", "builder.test", "webhook", "integration" or "workflow_event".',
    )
  })
})
