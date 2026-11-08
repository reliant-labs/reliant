// Copyright (c) 2025 Reliant Labs
/** Shared plain-language explanation beside the recording and in its tour step. */
export function BuildSummary() {
  return <div className="space-y-3" data-testid="build-summary">
    <h2 className="text-xl font-semibold text-ink sm:text-2xl">Tell your agent what to build. Forge gives it the foundation.</h2>
    <p className="max-w-3xl text-base leading-relaxed text-ink">You choose what the app should do. Your agent defines the data and API and writes your app&apos;s custom behavior. Forge generates supporting code and production setup, with checks to guide the work.</p>
  </div>;
}
