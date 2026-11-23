## review:passed

Lane: **MEDIUM** (one pass on gpt-6-luna@codex).

MEDIUM: ui-code (frontends/web/src/ModernApp.tsx, frontends/web/src/components/Projects/SwitchProjectDialog.tsx, …)

Reviewed all seven changed files and their changed hunks. The URL/chat ownership guard, explicit project-selection navigation, org-follow behavior, and project-opening paths are consistent with the route and workspace helpers. The reported fixed-sleep concern in the new test is already reported and is not repeated.

- **risk** `frontends/web/src/store/__tests__/projectStore.chatUrlWins.test.ts:65` — A fixed sleep orders the test (RC-05); under load the ordering it assumes does not hold. (sleep-test) Fix: Wait on the event itself (a channel, a poll with a deadline) instead of a duration.

Passed: entering the native merge queue at this exact head.

Reviewed `ba092a732d9e5a0f3213739d6df99f7f15588dc7`.
<!-- review-passed sha=ba092a732d9e5a0f3213739d6df99f7f15588dc7 -->