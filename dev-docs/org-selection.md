# Web organization selection (M1c)

The control-plane contract is `control-plane/docs/orgs.md`. The web's active org is a view preference, not the server's default org. `OrganizationService.ListMyOrganizations` supplies membership and the default; `SetDefaultOrganization` changes that separate default. No client infers an org from ordering or a wallet.

`ActiveOrgBinding` binds the persisted choice to the signed-in user, reconciles it against the membership list (including leaving the last org), and resets org-scoped query families when it changes. Chats and projects remain user-scoped. `orgInterceptor` fills an empty `org_id` field using the message descriptor, on the shared authenticated Connect chain. It leaves an explicitly named org alone, including `SetDefaultOrganization`. New org-scoped messages therefore participate without another ad-hoc client wrapper. Identity/observability's required org parameters resolve through `orgs.currentOrgId`, which shares the membership-list query.

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

## Validation

- `task test:affected LANES=web` and `LANES=go` (this checkout has a Taskfile, not a Makefile).
- Scoped vitest: store, interceptor, switcher, CLI/device approval, pairing, billing and affected chrome.
- `e2e/org-switcher.spec.ts`: mocked backend, two orgs, wire `orgId`, switch, reload, membership removal, single-org chrome, mobile Account and CLI approval; no uncaught JavaScript errors.
- Screenshots: `frontends/web/e2e/screenshots/org-switcher/` (1280×800 desktop and 390×844 mobile).
- Regression proof: interceptor test fails with its transport wiring reverted; ForgeService payload test fails with its implementation reverted. Both pass with the implementation present.
