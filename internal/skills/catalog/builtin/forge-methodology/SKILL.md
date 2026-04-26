---
name: forge-methodology
description: Forge app generation methodology — clean-slate project rules, no backwards compatibility, fix root causes over workarounds
---

# Forge Methodology

## Forge Project Rules

This is a new Forge project that has never been shipped. There are no users, no backwards compatibility requirements, and no legacy code paths to maintain.

- Do NOT add fallbacks that mask bugs — fix the underlying issue instead
- Do NOT maintain backwards compatibility — opt for clean new implementations
- Do NOT add defensive code for scenarios that can't happen
- Do NOT keep old code paths "just in case" — delete what's not needed
- When something breaks, patch the root cause, don't paper over it
