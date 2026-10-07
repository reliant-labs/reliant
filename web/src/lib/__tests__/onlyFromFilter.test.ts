import { describe, expect, it } from 'vitest'

import { normalizeSenderId, onlyFromClause, parseOnlyFrom, withOnlyFrom } from '../onlyFromFilter'

describe('Only from: the sender clause of a trigger filter', () => {
  it('writes the clause the server compiles (internal/triggers filter_test.go)', () => {
    expect(withOnlyFrom('', ['U123'])).toBe('trigger.sender.verified && trigger.sender.id in ["U123"]')
    expect(onlyFromClause(['octocat', 'hubot'])).toBe('trigger.sender.verified && trigger.sender.id in ["octocat", "hubot"]')
  })

  it('ANDs the clause after an existing filter and keeps that filter', () => {
    const existing = "trigger.payload.data.issue.number > 1 || trigger.payload.data.action == 'opened'"
    const written = withOnlyFrom(existing, ['octocat'])
    expect(written).toBe(`(${existing}) && trigger.sender.verified && trigger.sender.id in ["octocat"]`)
    expect(parseOnlyFrom(written!)).toEqual({ kind: 'list', ids: ['octocat'], rest: existing })
  })

  it('round-trips: what it writes, it reads back exactly', () => {
    const ids = ['U123', 'with "quotes" and \\ backslash', 'ünïcode@example.com']
    for (const rest of ['', 'true', "trigger.payload.data.channel == 'C0GEN'", "(a) || ('b)' == trigger.name)"]) {
      const written = withOnlyFrom(rest, ids)!
      expect(parseOnlyFrom(written)).toEqual({ kind: 'list', ids, rest })
      expect(withOnlyFrom(written, ids)).toBe(written)
    }
  })

  it('removing every sender removes the clause and restores the filter', () => {
    const written = withOnlyFrom('trigger.kind == "integration"', ['U1', 'U2'])
    expect(withOnlyFrom(written, ['U2'])).toBe('(trigger.kind == "integration") && trigger.sender.verified && trigger.sender.id in ["U2"]')
    expect(withOnlyFrom(written, [])).toBe('trigger.kind == "integration"')
    expect(withOnlyFrom(withOnlyFrom('', ['U1']), [])).toBe('')
  })

  it('drops duplicates and blanks', () => {
    expect(withOnlyFrom('', ['U1', '', 'U1', 'U2'])).toBe(onlyFromClause(['U1', 'U2']))
  })

  it('reads a filter with no sender clause as an empty list over the whole filter', () => {
    expect(parseOnlyFrom('')).toEqual({ kind: 'none', rest: '' })
    expect(parseOnlyFrom(undefined)).toEqual({ kind: 'none', rest: '' })
    expect(parseOnlyFrom('  trigger.payload.data.x == 1  ')).toEqual({ kind: 'none', rest: 'trigger.payload.data.x == 1' })
  })

  it('calls any other use of trigger.sender custom, and never rewrites it', () => {
    for (const custom of [
      'trigger.sender.id == "octocat"',
      'trigger.sender.id in ["U1"]',
      'trigger.sender.verified && trigger.sender.id in ["U1"] || true',
      '(trigger.sender.kind == "slack") && trigger.sender.verified && trigger.sender.id in ["U1"]',
      // Parens that do not wrap all of the rest: not the written shape.
      '(a) || (b) && trigger.sender.verified && trigger.sender.id in ["U1"]',
      'x && trigger.sender.verified && trigger.sender.id in ["U1"]',
      'trigger.sender.verified && trigger.sender.id in [inputs.allowed]',
    ]) {
      expect(parseOnlyFrom(custom), custom).toEqual({ kind: 'custom' })
      expect(withOnlyFrom(custom, ['U9']), custom).toBeNull()
    }
  })

  it('normalizes ids the way trigger.sender carries them', () => {
    expect(normalizeSenderId('github', ' OctoCat ')).toBe('octocat')
    expect(normalizeSenderId('gmail', 'Boss@Example.com')).toBe('boss@example.com')
    expect(normalizeSenderId('slack', ' U0ABC ')).toBe('U0ABC')
  })
})
