import { describe, expect, it } from 'vitest'
import { normalizeCelBoolean, normalizeCelString } from '../celAdapter'

describe('celAdapter', () => {
  describe('normalizeCelString', () => {
    it('preserves plain string input', () => {
      expect(normalizeCelString('hello')).toBe('hello')
    })

    it('normalizes oneof literal shape', () => {
      expect(
        normalizeCelString({
          value: {
            case: 'literal',
            value: 'literal value',
          },
        })
      ).toBe('literal value')
    })

    it('normalizes oneof expr shape', () => {
      expect(
        normalizeCelString({
          value: {
            case: 'expr',
            value: '{{input.name}}',
          },
        })
      ).toBe('{{input.name}}')
    })

    it('normalizes flat literal/expr fallback shape', () => {
      expect(normalizeCelString({ literal: 'flat literal' })).toBe('flat literal')
      expect(normalizeCelString({ expr: '{{input.role}}' })).toBe('{{input.role}}')
    })

    it('normalizes wrapper scalar value shape', () => {
      expect(normalizeCelString({ value: 'wrapped value' })).toBe('wrapped value')
    })

    it('uses fallback for non-string values', () => {
      expect(normalizeCelString(undefined, 'fallback')).toBe('fallback')
      expect(normalizeCelString({ value: { case: 'literal', value: true } }, 'fallback')).toBe('fallback')
    })
  })

  describe('normalizeCelBoolean', () => {
    it('preserves plain boolean input', () => {
      expect(normalizeCelBoolean(true)).toBe(true)
      expect(normalizeCelBoolean(false)).toBe(false)
    })

    it('normalizes oneof literal shape', () => {
      expect(
        normalizeCelBoolean({
          value: {
            case: 'literal',
            value: true,
          },
        })
      ).toBe(true)
    })

    it('normalizes flat literal fallback shape', () => {
      expect(normalizeCelBoolean({ literal: false }, true)).toBe(false)
    })

    it('normalizes wrapper scalar boolean shape', () => {
      expect(normalizeCelBoolean({ value: true }, false)).toBe(true)
    })

    it('returns expression string for expression variants', () => {
      expect(
        normalizeCelBoolean(
          {
            value: {
              case: 'expr',
              value: '{{input.enabled}}',
            },
          },
          true
        )
      ).toBe('{{input.enabled}}')

      expect(normalizeCelBoolean({ expr: '{{input.enabled}}' }, false)).toBe('{{input.enabled}}')
    })
  })
})
