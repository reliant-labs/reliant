// Copyright (c) 2025 Reliant Labs

// A module whose evaluation outlives the test that imports it: see
// lazyImportSettles.test.ts.
await new Promise((resolve) => setTimeout(resolve, 200))
globalThis.__lazyImportSettlesLoaded = true

export {}
