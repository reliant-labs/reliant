---
name: code-review-orchestration
description: Code review orchestration methodology — triage-based reviewer spawning, finding synthesis, and structured review reporting
---

# Code Review Orchestration

## Process

### Step 1: Triage the Change
Before spawning reviewers, assess the scope and nature of the change:
- What files/changes are being reviewed?
- What is the intent of the change?
- Any specific concerns the requester mentioned?
- **Classify the change**:
  - **Trivial**: CSS, spacing, copy/text, config values, static assets
  - **UI/frontend**: Component changes, styling logic, frontend state, templates
  - **Backend/logic**: API changes, business logic, data access, auth, infrastructure
  - **Full-stack**: Changes spanning both frontend and backend

Use the researcher if you need to gather context about the codebase or the changes.

### Step 2: Spawn Reviewers Based on Triage (IN PARALLEL)
Only spawn reviewers that are relevant to the change. Do NOT spawn all reviewers for every change.

**Trivial changes** (CSS, spacing, copy, config):
- Review directly yourself without spawning sub-reviewers, OR spawn only **code_hygiene_reviewer** at most.
- A one-line CSS fix does not need security, performance, or architecture review.

**UI/frontend changes** (components, styling logic, frontend state):
- Spawn **code_hygiene_reviewer** + **ux_reviewer** (if app URL provided)
- Only add **security_reviewer** if the change handles user input, auth tokens, or sensitive data
- Only add **performance_reviewer** if the change involves data fetching, large lists, or complex state
- Only add **architect** if the change introduces new patterns or significant structural changes

**Backend/logic changes** (API, business logic, data access):
- Spawn **security_reviewer** + **code_hygiene_reviewer** + **performance_reviewer** + **architect**
- Add **ux_reviewer** only if an app URL is provided AND the change affects user-visible behavior

**Full-stack changes**:
- Spawn all relevant reviewers

Available specialized reviewers:
1. **security_reviewer** - Security vulnerabilities, injection, auth, secrets, crypto
2. **architect** - Design, maintainability, patterns, API contracts, type safety
3. **performance_reviewer** - Scalability, concurrency, race conditions, resource leaks
4. **code_hygiene_reviewer** - Correctness bugs, error handling, test quality, LLM antipatterns
5. **ux_reviewer** - (Only if an app URL is provided) Load the running application via Chrome DevTools, check for console errors, test user flows, verify accessibility, and test responsive layouts

For each spawn, provide:
- The files/changes to review
- The intent of the change (if known)
- Any specific context they need
- For ux_reviewer: include the app URL (e.g., http://localhost:PORT)

### Step 3: Synthesize Findings
Once all reviewers complete, synthesize their findings into a unified report:

## Final Report Format

### Executive Summary
- **Overall Verdict**: APPROVE / APPROVE WITH CHANGES / REQUEST CHANGES / BLOCK
- **Risk Level**: LOW / MEDIUM / HIGH / CRITICAL
- 3-5 bullet summary of the most important findings

### Critical Issues (Must Fix)
Issues that should block merge. Include:
- Source reviewer
- Location
- Issue description
- Required action

### Important Issues (Should Fix)
Issues that don't block but should be addressed. Same format.

### Minor Issues & Suggestions
Grouped by category (security, quality, performance, hygiene)

### What's Good
Acknowledge well-written aspects (brief)

### Recommended Test Plan
Based on reviewer findings, what should be tested before merge?

## Guidelines

- **Spawn all reviewers in parallel** - don't wait for one to finish before spawning the next
- **UX reviewer is conditional** - only spawn ux_reviewer if an app URL was provided in your instructions
- **Deduplicate findings** - if multiple reviewers flag the same issue, consolidate
- **Prioritize ruthlessly** - the final report should be actionable, not overwhelming
- **Be decisive** - give a clear verdict, don't hedge with "it depends"
- **Preserve specifics** - keep file:line references and concrete suggestions from reviewers

## When to BLOCK
- Critical security vulnerabilities
- Data loss or corruption risks
- Breaking changes without migration path
- Tests that don't test real code

## When to REQUEST CHANGES
- Medium/high security issues
- Correctness bugs
- Missing error handling on critical paths
- Race conditions or resource leaks

## When to APPROVE WITH CHANGES
- Code quality issues
- Minor hygiene items
- Performance concerns in non-critical paths
- Test coverage gaps

## When to APPROVE
- No significant issues
- Code follows patterns
- Tests are adequate
