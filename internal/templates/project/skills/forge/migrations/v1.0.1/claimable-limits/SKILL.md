---
name: migration-v1.0.1-claimable-limits
description: The anonymous (claimable) plan's caps come from the control plane's GetClaimableAvailability limits; pkg/deploy's Claimable* constants and ClaimableResources are a ClaimableLimits value (ClaimableLimitsFallback is the offline copy, 125m / 128 MiB), and CheckHostedResources(plan, r) is CheckHostedResourcesWithin(plan, limits, r). Use when Go code names deploy.CheckHostedResources, deploy.ClaimableCPUMillicores, deploy.ClaimableMemoryBytes, deploy.ClaimableMax* or deploy.ClaimableResources.
relevance: migration
---

# The claimable plan's caps come from the control plane

Use when Go code calls `deploy.CheckHostedResources`, or names
`deploy.ClaimableCPUMillicores`, `deploy.ClaimableMemoryBytes`,
`deploy.ClaimableMax*` or `deploy.ClaimableResources`.

### What changed

The control plane publishes the anonymous plan's caps in
`GetClaimableAvailability`'s `limits`. `forge env shape --claimable`,
`forge env deploy --claimable --plan-only` and `forge env deploy --claimable`
judge by that answer and fall back to a compiled-in copy only when the control
plane cannot be asked, saying so. In `pkg/deploy` the `Claimable*` constants
and `ClaimableResources()` are a `ClaimableLimits` value
(`ClaimableLimitsFallback()` is the compiled-in copy: 125m / 128 MiB per
workload), and `CheckHostedResources(plan, r)` is
`CheckHostedResourcesWithin(plan, limits, r)`.

### What to do

Pass the limits you resolved, or `deploy.ClaimableLimitsFallback()` offline:
`deploy.CheckHostedResourcesWithin(plan, limits, r)`, `limits.Resources()`,
`limits.WorkloadCPUMillicores`, `limits.MaxServices`.
