## review:passed

Lane: **HIGH** (Sol pass + second pass in this run).

HIGH: billing (electron/package.json)

Reviewed all supplied changed hunks and the latest push. All six earlier blocker IDs are fixed: retained-shell markers reset, draft mirroring waits for application, and thread navigation preserves validated view/panel state. Regression tests cover these cases, including deferred and superseded applications. HIGH second pass: no new authorization, tenant-isolation, secrets, or rollout defect found; billing and migrations are unchanged. RC-04/05/06/10 checks found no new violation. No code or tests executed.

**Second pass:** The billing classifier matched the retained Stripe test name in electron/package.json; this change only adds deep-link test discovery, with no price, quantity or payment change. All supplied text hunks were read. The three routing defects are fixed with focused regression tests. RC-04/05/06/10 revealed no new violation. No migrations or server contracts change. Tests were inspected, not executed; binary visual baselines remain uninspected.

Tests covering the risk: 
- Classifier's billing match is test-list wiring only; no money behavior changes.: `electron/test/test-discovery.test.js: every test file is either in the npm test script or explicitly quarantined`
- Untrusted desktop deep links must reject external destinations while admitting ordinary chat links.: `electron/test/deep-link.test.js: refuses anything that is not a page of this app on the allowlist; opens a chat, a thread and the new-chat view, in either URL form`
- Chat routing must deny non-member and signed-out reads and avoid displaying a chat under another org.: `frontends/web/src/__tests__/chatRoutes.production.test.ts: asks nothing about a chat under an org the person is not in; keeps a chat URL in place for a visitor who is not signed in; never shows a chat under an org it is not in`

Migration safety: **not_applicable**

**Earlier blockers, at this head:**
- R1.B1 fixed — frontends/web/src/hooks/useChatRoute.ts:125 resets openedFor on leaving chat routes; the retained-shell Back regression covers reopening.
- R1.B2 fixed — frontends/web/src/hooks/useChatRoute.ts:154-197 gates mirroring on completed application for the current location and fences superseded completions.
- R1.B3 fixed — frontends/web/src/lib/chatRoute.ts:108-125 preserves validated view/panel values for same-chat thread navigation; hook and library regressions cover it.
- R2.B1 fixed — frontends/web/src/hooks/useChatRoute.ts:125-132,161-167 resets retained-shell open/apply markers when leaving their routes.
- R2.B2 fixed — frontends/web/src/store/openChat.ts:151-165 records application completion; useChatRoute.ts:173-197 waits before mirroring, including externally initiated applications.
- R2.B3 fixed — frontends/web/src/lib/chatRoute.ts:108-125 retains validated chat view state when changing threads.
- R1.B1 fixed — useChatRoute.ts resets route markers on leaving their page; retained-shell Back tests cover chat and draft returns.
- R1.B2 fixed — useChatRoute.ts gates the draft mirror on shownFor, fences superseded completions, and waits for externally initiated applies; held-apply regression tests cover these paths.
- R1.B3 fixed — chatRoute.ts preserves validated view/panel search for same-chat thread navigation; library and hook tests assert both directions.
- R2.B1 fixed — frontends/web/src/hooks/__tests__/useChatRoute.test.tsx: retained-shell chat→draft→Back and draft→chat→Back tests assert reopening with one shell mount.
- R2.B2 fixed — frontends/web/src/hooks/__tests__/useChatRoute.test.tsx: held project resolution, externally initiated apply, and superseded apply tests pin the new mirror fence.
- R2.B3 fixed — frontends/web/src/lib/__tests__/chatRoute.test.ts and hooks/__tests__/useChatRoute.test.tsx assert view/panel preservation on opening and closing a thread.

**Not verified (non-blocking):**
- Visual changes within the five binary screenshot baselines could not be inspected through the available text-only GitHub tools.

**Post-merge verification** (only a live environment shows these; not review blockers, each verified by the plan named):
- Production web navigation and retained chat URLs. — After release, open a sidebar chat, reload, use Back, open /new, and verify legacy home/project links as specified in the PR.; rollback: Revert this PR and ship the web release.
- Packaged Electron deep-link delivery. — On the release candidate, verify open?url brings the window forward on the chat and open?path still opens its project.; rollback: Revert this PR and ship the next desktop release.
- Production web navigation, reload, Back, draft route and legacy-link behavior. — After release reaches prod, open a sidebar chat, inspect its URL, reload, use Back, open /new, and check / and /project/<id> links.; rollback: Revert the PR and ship the web release.
- Released Electron deep-link delivery and existing project-link compatibility. — On the next Electron release candidate, manually open reliant://open?url=/c/<chatId> and reliant://open?path=<dir>; confirm window surfacing and correct destination.; rollback: Revert the PR and ship the next desktop release if the change has already shipped.

Passed: entering the native merge queue at this exact head.

Reviewed `aa3c932cbe0733e5e66856d7159df64686359065`.
<!-- review-passed sha=aa3c932cbe0733e5e66856d7159df64686359065 -->