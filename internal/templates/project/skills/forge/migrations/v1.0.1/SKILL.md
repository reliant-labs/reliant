---
name: v1.0.1
description: Upgrade a project to forge v1.0.1. (1) The anonymous (claimable) plan's caps come from the control plane's GetClaimableAvailability limits: pkg/deploy's Claimable* constants and ClaimableResources are a ClaimableLimits value (ClaimableLimitsFallback is the offline copy, 125m / 128 MiB), and CheckHostedResources(plan, r) is CheckHostedResourcesWithin(plan, limits, r). Use when Go code names deploy.CheckHostedResources, deploy.ClaimableCPUMillicores, deploy.ClaimableMemoryBytes, deploy.ClaimableMax* or deploy.ClaimableResources.
version: v1.0.1
detection: grep -rqsE --include='*.go' --exclude-dir=gen --exclude-dir=vendor --exclude-dir=node_modules 'deploy\.(CheckHostedResources\(|ClaimableCPUMillicores|ClaimableMemoryBytes|ClaimableMax|ClaimableResources)' . && exit 0; exit 1
---

# Upgrading to v1.0.1

Each section below is independent: apply the ones whose "Use when" matches.

## 1. The claimable plan's caps come from the control plane

Load `migrations/v1.0.1/claimable-limits` for the edit.
