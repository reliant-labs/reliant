---
name: pivot-on-friction
description: >-
  Recognize when iterative fixes aren't working and pivot strategy.
  Load this skill when the same bug has been "fixed" multiple times but persists,
  when the user expresses frustration, when you've made changes that you
  can't verify actually work, or when you realize you've been guessing
  instead of investigating. Provides a structured approach to break out of
  fix-attempt loops.
---

# Pivot on Friction

## Triggers — recognize the pattern

You need this skill when:

- You've attempted the same class of fix more than twice
- The user has said something is still broken after your fix
- You're making changes without being able to verify them
- You're spawning agents to "fix" things without reading the code yourself
- Your fixes are in the wrong files/worktree/branch
- You're guessing at root causes instead of tracing the actual code path

If any of these are true, **stop what you're doing right now.**

## Diagnose why you're looping

Before attempting another fix, answer these honestly:

1. **Did you actually read the code**, or did you make assumptions about what it does?
2. **Did you verify the fix was applied to the RIGHT location?** Correct worktree, correct branch, correct file. Check with `git diff` or `pwd`.
3. **Did you verify the fix was applied at all?** Check `git diff`, run the code, look at the output.
4. **Are you fixing symptoms instead of root causes?** Adding a nil check instead of understanding why it's nil?
5. **Are you delegating investigation to agents who then also guess?** Spawning a chain of agents who each make assumptions doesn't converge on truth.

## Break the loop — in priority order

### 1. Verify your environment first

```
- Which worktree/branch am I editing? (pwd, git branch)
- Was the binary/app rebuilt after my source change?
- Am I testing the right instance? (right port, right namespace, right pod)
```

This catches a shocking number of "the fix didn't work" situations.

### 2. Read the actual code path, end-to-end

- Don't spawn an agent to summarize — read it yourself
- Trace from the user action to the final effect, line by line
- Find the EXACT line that breaks, not the general area
- If the path crosses multiple files, read all of them

### 3. Build a validation harness

If you can't manually verify, write an automated test:

- Write the test FIRST
- Confirm it reproduces the bug (test fails)
- Then fix the code (test passes)
- **This is mandatory after 2+ failed fix attempts**

Load the `validation-harness` skill for guidance on building one.

### 4. Simplify the scope

- Fix ONE bug at a time, verify it, move on
- Don't batch 7 fixes into parallel agents — cross-contamination risk is high
- Each fix gets its own verify step

### 5. Ask the user for specific facts

Don't ask vague questions. Ask for specific observable facts:

- What exactly do they see? (screenshot, error message, browser console)
- What exactly did they do? (click sequence, URL, browser state)
- What changed between when it worked and when it broke?

## Anti-patterns

Do not do these:

- **Spawn fix agents without reading the relevant code yourself first.** You can't delegate understanding.
- **Declare something "fixed" without running a test or checking output.** "Should work now" is not verification.
- **Make changes to the wrong worktree/repo/branch.** Always confirm where you are.
- **Fix the code but forget to rebuild the binary/restart the server.** Your source change means nothing until it's running.
- **Add timeouts and fallbacks to mask bugs instead of fixing root causes.** A retry loop around a broken function is not a fix.
- **Apologize and immediately try the same approach again.** If the approach failed, the approach is wrong.

## The right mindset

- **Every failed fix attempt is information.** What did it tell you? If you can't answer that, you didn't learn from the failure.
- **Friction = signal.** If it's hard to fix, you probably don't understand the problem yet.
- **Slow down to go faster.** Reading 200 lines of code is faster than 3 wrong fix attempts.
- **Your goal is a WORKING fix, not a plausible-sounding fix.** The user doesn't care about your explanation — they care that it works.
