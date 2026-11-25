---
name: code-review
description: Code review orchestration methodology — triage-based reviewer spawning, finding synthesis, and structured review reporting
---

# Code Review Orchestration

## Process

### Step 1: Triage the Change
Before reviewing, assess the scope and nature of the change:
- What files/changes are being reviewed?
- What is the intent of the change?
- Any specific concerns the requester mentioned?
- **Classify the change**:
  - **Trivial**: CSS, spacing, copy/text, config values, static assets
  - **UI/frontend**: Component changes, styling logic, frontend state, templates
  - **Backend/logic**: API changes, business logic, data access, auth, infrastructure
  - **Full-stack**: Changes spanning both frontend and backend

Use the researcher if you need to gather context about the codebase or the changes.

### Step 2: Load Review Sub-Skills Based on Triage
Only load sub-skills that are relevant to the change. Do NOT load all sub-skills for every change.

Available specialized review sub-skills (load with `skill(action="load", path="code-review/<name>")`):
1. **code-review/security-review** - Security vulnerabilities, injection, auth, secrets, crypto
2. **code-review/architecture-review** - Design, maintainability, patterns, API contracts, type safety
3. **code-review/performance-review** - Scalability, concurrency, race conditions, resource leaks
4. **code-review/code-hygiene-review** - Correctness bugs, error handling, test quality, LLM antipatterns
5. **code-review/ux-review-methodology** - (Only if an app URL is provided) Load the running application via Chrome DevTools, check for console errors, test user flows, verify accessibility, and test responsive layouts

**Trivial changes** (CSS, spacing, copy, config):
- Review directly yourself, OR load only **code-review/code-hygiene-review** at most.
- A one-line CSS fix does not need security, performance, or architecture review.

**UI/frontend changes** (components, styling logic, frontend state):
- Load **code-review/code-hygiene-review** + **code-review/ux-review-methodology** (if app URL provided)
- Only add **code-review/security-review** if the change handles user input, auth tokens, or sensitive data
- Only add **code-review/performance-review** if the change involves data fetching, large lists, or complex state
- Only add **code-review/architecture-review** if the change introduces new patterns or significant structural changes

**Backend/logic changes** (API, business logic, data access):
- Load **code-review/security-review** + **code-review/code-hygiene-review** + **code-review/performance-review** + **code-review/architecture-review**
- Add **code-review/ux-review-methodology** only if an app URL is provided AND the change affects user-visible behavior

**Full-stack changes**:
- Load all relevant sub-skills

For large or complex reviews, consider spawning a **researcher** to gather context in parallel while you review.

### Step 3: Perform the Review
Apply each loaded sub-skill's methodology to the relevant parts of the change. Work systematically through each review dimension.

### Step 4: Synthesize Findings
Compile findings into a unified report:

## Final Report Format

### Executive Summary
- **Overall Verdict**: APPROVE / APPROVE WITH CHANGES / REQUEST CHANGES / BLOCK
- **Risk Level**: LOW / MEDIUM / HIGH / CRITICAL
- 3-5 bullet summary of the most important findings

### Critical Issues (Must Fix)
Issues that should block merge. Include:
- Review dimension (security, architecture, etc.)
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
Based on findings, what should be tested before merge?

## Guidelines

- **UX review is conditional** - only apply ux-review-methodology if an app URL was provided in your instructions
- **Deduplicate findings** - if multiple review dimensions flag the same issue, consolidate
- **Prioritize ruthlessly** - the final report should be actionable, not overwhelming
- **Be decisive** - give a clear verdict, don't hedge with "it depends"
- **Preserve specifics** - keep file:line references and concrete suggestions

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
