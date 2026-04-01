import { describe, it, expect } from 'vitest'

/**
 * Test helper function for normalizing workflow names
 * (Mimics the normalizeName function in WorkflowBuilder.tsx)
 */
function normalizeName(value: string): string {
  return value.trim().replace(/\s+/g, '-')
}

describe('WorkflowBuilder - Name Normalization', () => {
  it('should replace single spaces with hyphens', () => {
    expect(normalizeName('My Workflow')).toBe('My-Workflow')
    expect(normalizeName('Simple Test')).toBe('Simple-Test')
  })

  it('should replace multiple consecutive spaces with a single hyphen', () => {
    expect(normalizeName('Too    Many    Spaces')).toBe('Too-Many-Spaces')
    expect(normalizeName('Inconsistent  Spacing   Here')).toBe('Inconsistent-Spacing-Here')
  })

  it('should trim leading and trailing spaces', () => {
    expect(normalizeName('  Leading Spaces')).toBe('Leading-Spaces')
    expect(normalizeName('Trailing Spaces  ')).toBe('Trailing-Spaces')
    expect(normalizeName('  Both Sides  ')).toBe('Both-Sides')
  })

  it('should handle already normalized names', () => {
    expect(normalizeName('already-normalized')).toBe('already-normalized')
    expect(normalizeName('kebab-case-name')).toBe('kebab-case-name')
  })

  it('should handle empty strings', () => {
    expect(normalizeName('')).toBe('')
    expect(normalizeName('   ')).toBe('')
  })

  it('should handle names with mixed spacing', () => {
    expect(normalizeName('  Complex   Name With   Weird    Spacing  ')).toBe('Complex-Name-With-Weird-Spacing')
  })

  it('should handle single word names', () => {
    expect(normalizeName('Workflow')).toBe('Workflow')
    expect(normalizeName('  Workflow  ')).toBe('Workflow')
  })

  it('should preserve existing hyphens', () => {
    expect(normalizeName('pre-existing-hyphens with spaces')).toBe('pre-existing-hyphens-with-spaces')
  })
})
