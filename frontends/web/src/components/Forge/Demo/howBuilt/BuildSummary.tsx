// Copyright (c) 2025 Reliant Labs
/** Shared plain-language explanation beside the recording and in its tour step. */
export function BuildSummary() {
  return <div className="space-y-3" data-testid="build-summary">
    <h2 className="text-xl font-semibold text-ink sm:text-2xl">You define the app. Forge builds the foundation.</h2>
    <p className="max-w-3xl text-base leading-relaxed text-ink">One Forge command created the project. A person described each service&apos;s data and API; Forge generated the supporting code and production setup. Your app&apos;s custom behavior is still yours or your coding agent&apos;s to write.</p>
  </div>;
}
