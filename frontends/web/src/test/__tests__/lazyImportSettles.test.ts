// Copyright (c) 2025 Reliant Labs

/**
 * Pins "Nothing loads after teardown" in src/test/setup.ts: a module a test
 * starts loading and never awaits has finished loading before the next test
 * runs, so it can never still be loading when the environment is torn down.
 */

import { describe, expect, it } from 'vitest'

declare global {
  var __lazyImportSettlesLoaded: boolean | undefined
}

describe('a lazy import a test never awaits', () => {
  it('starts loading a module that takes a while to evaluate', () => {
    globalThis.__lazyImportSettlesLoaded = false
    void import('./fixtures/slowToEvaluate')
    expect(globalThis.__lazyImportSettlesLoaded).toBe(false)
  })

  it('has finished loading before the next test starts', () => {
    expect(globalThis.__lazyImportSettlesLoaded).toBe(true)
  })
})
