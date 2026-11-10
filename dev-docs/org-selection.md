# Web organization selection (M1c)

The control-plane contract is `control-plane/docs/orgs.md`. The web's active org is a view preference, not the server's default org. `OrganizationService.ListMyOrganizations` supplies membership and the default; `SetDefaultOrganization` changes that separate default. No client infers an org from ordering or a wallet.

`ActiveOrgBinding` binds the persisted choice to the signed-in user, reconciles it against the membership list (including leaving the last org), and resets org-scoped query families when it changes. Chats and projects belong to an org (below, "Projects and chats belong to an org"). `orgInterceptor` fills an empty `org_id` field using the message descriptor, on the shared authenticated Connect chain. It leaves an explicitly named org alone, including `SetDefaultOrganization`. New org-scoped messages therefore participate without another ad-hoc client wrapper. Identity/observability's required org parameters resolve through `orgs.currentOrgId`, which shares the membership-list query.

The switcher appears only with multiple orgs: the app sidebar, Apps header, Settings header, app/organization consoles, and mobile Account. CLI/device approval has an independent choice: the requested org if it is a membership, otherwise the default. It does not change the web's active org. Approval stays unavailable when membership cannot be loaded; Deny remains available.

## Requests now naming the active org

The field is attached by the shared interceptor, not repeated at each call site. These are all currently used calls in the six M1a services with newly optional `org_id`:

| File under `frontends/web/src/` | Calls |
|---|---|
| `services/forge/cloudEnvs.ts` | `ListEnvironments` (project and org-wide `/apps` list), `GetStatus`, `ListPromotions`, `SetEnvironmentRunState`, `Scale`, `RestartWorkload`, `DeleteEnvironment` |
| `services/forge/live.ts` | `GetLiveView`, `ListConvergences` |
| `services/forge/register.ts` | `EnsureEnvironment` |
| `services/forge/releases.ts` | `ListReleases`, `GetRelease`, `CompareReleases`, `ListPromotions`, `GetRollout`, `GetBundle` |
| `services/forge/domains.ts` | `ListDomains`, `GetDomain`, `CreateDomain`, `DeleteDomain`, `VerifyDomain`, `CreateDomainBinding`, `DeleteDomainBinding` |
| `services/controlPlane/observability/deliveryApi.ts` | `DeployService.ListReleases` |
| `services/controlPlane/billing.ts` | `GetCurrentUserReliantState`, `GetCurrentUserComputeEligibility` |
| `services/controlPlane/reliantAI.ts` | `GetCurrentUserReliantOverview`, `GetCurrentUserWalletOverview`, `RedeemCoupon` |
| `services/controlPlane/memberPermissions.ts` | `ListOrgMemberGrants`, `UpdateOrgMemberGrants` |
| `hooks/useCloudBillingQueries.ts` | `GetCurrentUserWalletOverview`, `GetCurrentUserComputeUsage`, `ListCurrentUserInvoices`, `CreateCurrentUserCheckoutSession`, `CreateCurrentUserWalletTopupSession`, `CreateCurrentUserWalletTopupPaymentIntent`, `CreateCurrentUserComputeSubscriptionIntent`, `GetCurrentUserWalletAutoRecharge`, `SetCurrentUserWalletAutoRecharge`, `CreateCurrentUserWalletPaymentMethodSetup`, `CreateCurrentUserBillingPortalSession` |
| `components/Settings/cloud/usage/useDeployUsage.ts` | `BillingService.GetCurrentUserInfraOverage`, `DeployService.ListUsage`, `ListDeployments`, `ListEnvironments` |

There are no current direct web calls to ClusterService or BrowserIngestService; any added call carrying `org_id` goes through the same interceptor. User-keyed billing operations and the plan catalogue deliberately carry no org field (M1a exemptions). Environment-bound secret-store calls already authorize their resource, not an implicit default org.

Existing required-org identity, observability, dashboards, activity, alerting, RUM, delivery and LLM calls now receive the selected org through the shared resolver or the billing hooks, not the wallet's organization. Their API wrappers keep passing their explicit `orgId`/scope.

The following Reliant `ForgeService` requests also carry `org_id`: `GetTopology`, `VerifyEnv`, `ListSecrets`, `GetEnvStatus`, `GetEnvShape`, `PlanPromote`, `ApplyPromote`, `PlanDeploy`, `StartDeployPlan`, `StartDeploy`. The API forwards it over NATS; only the daemon reads the filesystem and runs forge. That one child's `RELIANT_FORGE_ORG` selects the helper mint's org, without changing the daemon's process-global current org. Detached deploy jobs retain the org. `GetDeployStatus` only polls the already-started job and has no org selection.

The helper passes it in `TokenService.ExchangeToken.org_id`, then `AccessTokenInternalService.MintForUser.org_id`. The control plane verifies membership and clips scopes in the target org. The caller's scope ceiling, acting user, resource binding and expiry remain intact. Omitted org stays in the session credential's own org; a changed default cannot move it. The helper cache is keyed by endpoint, session and org. See control-plane PR #1413 (stacked on #1365).

Plain HTTP approval call sites also send `org_id`: `Auth/CliLogin.tsx` → `/oauth/approve`; `Auth/DevicePairing.tsx` and `Machines/PairMachinePanel.tsx` → `services/controlPlane/pairing.ts` → `/oauth/device/approve`.

## Projects and chats belong to an org

Every project and chat has one organization, fixed at creation (control-plane `docs/design/orgs-and-members.md` §3 and §5): `projects.org_id`, `chats.org_id`, nullable. The api-server asks control-plane (`AccessTokenInternalService.ResolveOrgMembership`, through `internal/orgs`, cached for `orgs.CacheTTL`) and keeps no copy of membership.

- **Create.** `CreateProject`, `CreateProjectFromRepo` and `StartChat` take an optional `org_id`, which the interceptor fills with the active org. A named org must be a live membership (PERMISSION_DENIED, reason `not_org_member`), a machine credential may name only its own org (`org_mismatch`), and an unverifiable named org is UNAVAILABLE. Unnamed is the caller's default org; if that cannot be resolved the row is created with no org rather than failing. A new chat takes its project's org when the project has one (naming another is FAILED_PRECONDITION), a branch takes its source chat's, and `MoveChatToProject` refuses a project of another org.
- **List.** `ListProjects` and `ListChats` take an optional `org_id` and then list only that org's rows; the caller's default org also lists the rows with no org yet. Unset lists everything the caller owns, as old clients expect.
- **Backfill.** Lazy: the first org-scoped list of a user's default org stamps their rows that have no org with it (a chat takes its project's org when it has one), at most once per `orgs.BackfillInterval` per api-server. No migration reads control-plane, and a row is never moved between orgs.
- **Workflows, triggers and runs** (owner decision, 2026-11-10). `workflow_drafts.org_id` (a saved workflow), `triggers.org_id` and `workflows.org_id` (a run) are the same nullable column. A saved workflow takes the request's org (`CreateWorkflowDraft`, `SaveWorkflow`; an import or an update keeps the existing org). A trigger takes its project's org, else the request's, and `UpdateTrigger` refuses a project of another org. Every chat a trigger launches belongs to the trigger's org (`launch.Spec.OrgID`), so a scheduled run bills it. A run inherits its chat's org on insert (the `workflows_inherit_chat_org` database trigger, which also covers older writers). An agent's `start_run` passes its chat's org, so it cannot start a run in another org's project. `ListTriggers`, `ListRuns` and `ListWorkflows` (saved workflows only) filter by `org_id` like the lists above, and the backfill stamps all three.
- **Access needs live membership.** `internal/authz` decides a resource that has an org (chat, project, saved workflow, trigger) for its owner only while they are a live member of that org, so a removed member loses the org's rows even though they created them. The membership is control-plane's (`internal/orgs`, cached `orgs.CacheTTL` = 30s, which bounds how long a removal can lag). Every process puts it on its contexts once: the API and daemon listeners for each request (`grpc.withMembership`), the worker for each activity (`worker.Options.BackgroundActivityContext`). A failed check denies (fail closed), and so does an org resource decided on a context that carries no membership, logged at ERROR. There is no invalidation event yet; control-plane would publish one on a membership change.
- **No control plane** (self-hosted, `AUTH_MODE=apikey`): there are no orgs. Nothing is stamped, `org_id` on requests is ignored, and lists are owner-only, as before.

Follow-ups: the web resets only the `ORG_SCOPED_QUERY_ROOTS` families on a switch, so the chat and project lists refresh on their next fetch, and `StreamUserUpdates` still delivers chats of every org; `Chat.org_id` / `Project.org_id` let it filter.

## Validation

- `task test:affected LANES=web` and `LANES=go` (this checkout has a Taskfile, not a Makefile).
- Scoped vitest: store, interceptor, switcher, CLI/device approval, pairing, billing and affected chrome.
- `e2e/org-switcher.spec.ts`: mocked backend, two orgs, wire `orgId`, switch, reload, membership removal, single-org chrome, mobile Account and CLI approval; no uncaught JavaScript errors.
- Screenshots: `frontends/web/e2e/screenshots/org-switcher/` (1280×800 desktop and 390×844 mobile).
- Regression proof: interceptor test fails with its transport wiring reverted; ForgeService payload test fails with its implementation reverted. Both pass with the implementation present.
