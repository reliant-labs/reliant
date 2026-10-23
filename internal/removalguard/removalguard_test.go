package removalguard

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/commitpolicy"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE TABLE
//
// One entry per feature that has been REMOVED from forge. Recording a new
// removal is exactly one new `removal` entry — name it, list the patterns that
// constitute a reference to it, and (only if a legitimate look-alike exists)
// add a narrow allowance with the reason spelled out.
//
// Rules for writing an entry:
//
//   - Patterns should be IDENTIFIER-shaped, not English-prose-shaped. Prose is
//     unbounded ("a test asserting the dev-auth bypass is gone" legitimately
//     names it); identifiers are the thing that actually compiles, ships and
//     misleads.
//   - Allowances are suppressed SPANS, not suppressed files: a finding is
//     excused only when the exact text that matched sits inside a match of the
//     allowance's Token. A file-wide allowance (nil Token) needs a very good
//     reason, and Paths to keep it contained.
//   - A too-permissive allowance silently defeats the guard. If an allowance
//     needs more than a sentence to justify, the pattern is probably wrong.
//
// ─────────────────────────────────────────────────────────────────────────────

var removals = []removal{
	{
		Name: "tenancy",
		Why: "Multi-tenancy was removed from forge. Tenant scoping is application-owned " +
			"domain policy now; forge ships no tenant column, header, context key or helper.",
		Patterns: []*regexp.Regexp{
			// tenant, tenants, tenancy, TenantID, tenant_id, X-Tenant-Id,
			// multi-tenant, --tenant. The leading \b keeps "lieutenant" and
			// "maintenance" out.
			regexp.MustCompile(`(?i)\btenan[tc]`),
			// camelCase-embedded forms the \b above cannot see:
			// WithTestTenant, testTenant, scopeTenant. Case-SENSITIVE, so the
			// lowercase-t English words can never reach it.
			regexp.MustCompile(`[a-z0-9]Tenant`),
			// multitenant / multitenancy written without a separator.
			regexp.MustCompile(`(?i)multitenan`),
		},
		Allowances: []allowance{
			{
				Name: "the OAuth issuer's own multi-tenant URL shape",
				Reason: "An identity provider's issuer URL is frequently per-tenant " +
					"(`https://your-tenant.auth0.com`), and a provider MAY publish a query " +
					"parameter of its own on the authorization endpoint that the flow has to " +
					"preserve. Both the Go client (pkg/oauth2) and the scaffolded browser flow " +
					"name that shape in example URLs and in the test that proves the endpoint's " +
					"query survives.\n" +
					"This is the IdP's tenancy, not forge's: it is a substring of a URL the " +
					"application configures, and forge ships no tenant column, header, context " +
					"key or helper on the strength of it. Scoped to the word inside a hostname " +
					"or query parameter, so a `TenantID` field or an `X-Tenant-Id` header in " +
					"these same files still fails.",
				// Three shapes, all of them "tenant as a URL component":
				//   - inside a hostname:            your-tenant.auth0.com
				//   - as a query parameter, set:    ?tenant=acme
				//   - as a query parameter, read:   Get("tenant"), get("tenant")
				// plus the hyphenated adjective in prose about such issuers.
				Token: regexp.MustCompile(`(?i)[-.a-z0-9]*tenant[-.a-z0-9]*\.[a-z]|tenant=|"tenant"|tenant = %q|multi-tenant`),
				Paths: []string{
					"pkg/oauth2/",
					"internal/templates/frontend/shared-web/src/lib/auth/",
					"internal/templates/frontend_auth_flow_test.go",
					// Zitadel's OWN multi-tenancy: the dev IdP resolves which
					// instance a request belongs to from the Host header. That
					// is a property of the product forge runs as a container,
					// not a tenant concept forge ships. Scoped by the same
					// "multi-tenant" token, so a TenantID field here still fails.
					"pkg/devidp/zitadel.go",
				},
			},
			{
				Name: "Flux's own multitenancy chart values and the tenants it isolates",
				Reason: "forge installs Flux from the community flux2 chart, whose values key is " +
					"literally `multitenancy` (`multitenancy.enabled`, `.privileged`, " +
					"`.defaultServiceAccount`), and whose isolation model is described in terms of " +
					"tenants. The key is Flux's identifier and cannot be renamed.\n" +
					"This is Flux's tenancy, not a forge tenant concept: forge ships no tenant " +
					"column, header, context key or helper on the strength of it. Scoped to the " +
					"Flux component's own files, so a `TenantID` anywhere else still fails.",
				Token: regexp.MustCompile(`(?i)multitenancy|hosted tenants?|tenants?`),
				Paths: []string{
					"kcl/lib/flux.k",
					"kcl/render.k",
					"kcl/tests/positive_flux_chart.k",
					"kcl/tests/positive_flux_chart_*.k",
					"kcl/tests/negative_flux_chart_*.k",
					"internal/flux/",
					"internal/cluster/flux_chart_hardening_test.go",
					"internal/cli/flux_reconcile_e2e_test.go",
					"internal/templates/project/skills/forge/deploy/flux/SKILL.md",
					"internal/templates/project/skills/forge/migrations/v0.1.46/SKILL.md",
					"docs/runbooks/flux-patch-and-upgrade.md",
				},
			},
			{
				Name: "Flux's per-namespace controller ClusterRoles in the changelog",
				Reason: "The changelog entry for the hardened flux_chart() names the two ClusterRoles \n" +
					"(`flux-tenant-reconciler`, `flux-tenant-source`) and Flux's own `multitenancy` chart \n" +
					"value. Those are Flux/control-plane identifiers shared with the control plane's Go \n" +
					"writer and cannot be renamed here; they are not a tenant column, header, context key \n" +
					"or helper in forge. Scoped to those exact literals in CHANGELOG.md, so any other \n" +
					"`tenant` there still fails.",
				Token: regexp.MustCompile(`flux-tenant-(reconciler|source)|multitenancy`),
				Paths: []string{"CHANGELOG.md"},
			},
			{
				Name: "the control plane's own organization-lookup RPC, DeployService/GetTenant",
				Reason: "forge learns which organization a credential acts for by calling the control " +
					"plane's DeployService.GetTenant, whose response nests the caller's org under a " +
					"`tenant` key. Both names are the control plane's wire identifiers and cannot be " +
					"renamed here; they are a lookup of the caller's ORGANIZATION, not a tenant column, " +
					"header, context key or helper in forge. Scoped to those exact literals and to the one " +
					"file that makes the call and its test, so any other `tenant` there (a TenantID, a " +
					"tenant header) still fails.",
				Token: regexp.MustCompile(`DeployService/GetTenant|json:"tenant"|\{"tenant":`),
				Paths: []string{
					"internal/cloud/organization.go",
					"internal/cloud/organization_test.go",
				},
			},
			{
				Name: "the secret store's existing `tenants/` KV path layout",
				Reason: "The platform's OpenBao keeps delivered values at `secret/data/tenants/<org>/<env>/<NAME>` " +
					"(control-plane's tenantsecret.Scope.Path), and pkg/secretaccess derives exact-path " +
					"policies over that layout so the hosted store and a self-run one share one derivation.\n" +
					"This is the store's on-disk path segment, a wire identifier that cannot be renamed " +
					"here without orphaning every stored value; forge ships no tenant column, header, " +
					"context key or helper on the strength of it. Scoped to the literal `tenants/` path " +
					"segment and the `\"tenants\"` root constant in that one package, so a TenantID or " +
					"an X-Tenant-Id header there still fails.",
				Token: regexp.MustCompile(`tenants/|"tenants"|\btenants\b`),
				Paths: []string{
					"pkg/secretaccess/",
					// The same layout, read by ESO: the SecretStore renderer
					// composes the remoteRef key, SF3's shape tests and this
					// renderer's fixtures spell it out.
					"kcl/lib/external_secret.k",
					"kcl/schema.k",
					"kcl/tests/positive_secrets_store_*.k",
					"internal/cli/testdata/render_contract/secrets-components.json",
					"pkg/release/store_secrets_test.go",
					// The component that serves that layout: its tests and
					// docker smokes read and write the same paths.
					"pkg/deploy/openbao/openbao_test.go",
					"pkg/deploy/openbao/smoke_test.go",
					"pkg/deploy/openbao/public_test.go",
					"pkg/deploy/openbao/public_smoke_test.go",
					"pkg/deploy/openbao/testdata/public.golden.yaml",
					"internal/templates/project/skills/forge/deploy/openbao-public/SKILL.md",
					"kcl/tests/positive_openbao_render.k",
					"internal/templates/project/skills/forge/secrets/openbao/SKILL.md",
					// The CLI's store source writes and reads that layout; only
					// its fakes and tests spell the literal path.
					"internal/cli/secret_store_source_fakes_test.go",
					"internal/cli/secret_store_source_smoke_test.go",
					"internal/cli/secret_store_source_test.go",
					"internal/cli/secret_store_source_tls_test.go",
				},
			},
			{
				Name: "the vendored ESO SecretStore CRD's provider fields",
				Reason: "pkg/deploy/eso/crds/<version>/secretstores.yaml is External Secrets Operator's own CRD, \n" +
					"embedded byte-for-byte and never edited here. Its provider schemas name other products' \n" +
					"tenancy: Azure's `tenantId`, Oracle Cloud's `tenancy` OCID, Vault's namespace described as \n" +
					"multi-tenancy, a `tenant` hostname. They are upstream identifiers that cannot be renamed, \n" +
					"and none is a forge tenant column, header, context key or helper. Scoped to the \n" +
					"`tenant`/`tenancy` words (with an `Id`/`Name` suffix) in that one vendored file, so a \n" +
					"`TenantID` field or an `X-Tenant-Id` header anywhere else, or `tenants/` in it, still fails.",
				Token: regexp.MustCompile(`(?i)\b(tenant|tenancy)(id|name)?\b`),
				Paths: []string{"pkg/deploy/eso/crds/*/secretstores.yaml"},
			},
			{
				Name: "Azure's Entra ID tenant, which a Trusted Signing credential belongs to",
				Reason: "Azure Trusted Signing authenticates as an Entra ID service principal, identified by " +
					"a tenant id (the Azure directory) and a client id. The desktop-release proposal names " +
					"that credential's fields: `tenant_id` on its AzureTrustedSigning schema, the `<tenant>` " +
					"placeholder in its example, and the \"Azure tenant id\" in its list of release secrets.\n" +
					"This is Microsoft's tenancy, not forge's: an identifier the author supplies for their own " +
					"signing account. forge ships no tenant column, header, context key or helper on the " +
					"strength of it. Scoped to the `tenant_id` / `tenant id` spellings and the `<tenant>` " +
					"placeholder in that one file, so the removed annotation (`tenant: true`), a `TenantID` " +
					"field or an `X-Tenant-Id` header there still fails; TestTenancyAzureAllowanceStaysNarrow " +
					"pins both sides. When the AzureTrustedSigning schema lands in code, add that file to " +
					"Paths rather than widening the Token.",
				Token: regexp.MustCompile(`(?i)\btenant[_ ]id\b|<tenant>`),
				Paths: []string{
					"docs/proposals/desktop-release-target.md",
				},
			},
			{
				Name: "prose teaching that forge has no tenancy",
				Reason: "The db/write-policy skill's \"If rows belong to someone, that is a column\" section and " +
					"seedplan's diamond-disambiguation comment both name tenancy in order to say " +
					"forge does NOT have it: the skill tells the author that ownership must be a " +
					"column they write because forge stores none, and the seedplan comment records " +
					"that the rule is deliberately STRUCTURAL and must not start recognizing " +
					"`tenant_id`/`org_id`, because that would put back the domain concept the " +
					"package removed on purpose.\n" +
					"This is documentation OF the removal, and it is the text most likely to stop " +
					"someone reintroducing the feature — deleting it to satisfy the guard would " +
					"delete the explanation for why the guard exists. Scoped to the negating " +
					"phrasings, so a line in these same files that actually reintroduced a tenant " +
					"column or helper still fails.",
				Token: regexp.MustCompile(`no implicit tenant|nothing about tenancy, ownership or scope|` +
					"`company_id`/`tenant_id`/`org_id`"),
				Paths: []string{
					"internal/templates/project/skills/forge/db/SKILL.md",
					"internal/templates/project/skills/forge/db/write-policy/SKILL.md",
					// The rendered copies of the same templates, tracked in-repo.
					".claude/skills/db/SKILL.md",
					".claude/skills/db-write-policy/SKILL.md",
					"pkg/seedplan/diamond.go",
				},
			},
		},
	},
	{
		Name: "authorization",
		Why: "Application-level authorization (RBAC, role checks, the pkg/crud Authorize " +
			"hook, the frontend route-guard role machinery) was removed. forge still does " +
			"AUTHENTICATION — proving who the caller is — and stops there; deciding what a " +
			"caller may do is the application's job.",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)\bauthz\b`),
			// authorizer, Authorizer, authorizer_gen.go, NewGeneratedAuthorizer.
			// No trailing \b: '_' is a word character, so `authorizer\b` would
			// miss "authorizer_gen.go" — the exact straggler this guard was
			// written for.
			regexp.MustCompile(`(?i)authorizer`),
			// IsAuthorized, isAuthorized, useIsAuthorized. Contiguous, so the
			// prose "not authorized" cannot reach it.
			regexp.MustCompile(`(?i)isauthoriz`),
			// AuthorizeOptions, AuthorizeFunc, AuthorizeHook, AuthorizeRequest.
			regexp.MustCompile(`Authorize[A-Z]`),
			// RequireRole, RequiresRole, required_roles, requiredRoles.
			regexp.MustCompile(`(?i)\brequire[sd]?[_-]?roles?\b`),
			// HasRole, hasRole, has_role, hasAnyRole, has_any_role.
			regexp.MustCompile(`(?i)\bhas[_-]?(any[_-]?)?roles?\b`),
			// RoleCheck, role_guard, roleGate, role_claim.
			regexp.MustCompile(`(?i)\brole[_-]?(check|claim|gate|guard)s?\b`),
			// The JSX prop <RouteGuard roles={["admin"]}>. Lowercase and
			// unspaced, so Go's `claims.Roles = []string{...}` (an identity
			// claim from the IdP, which is authentication) cannot reach it.
			regexp.MustCompile(`\broles=\{`),
			// The word itself. forge no longer has application RBAC of any
			// kind, so any surviving "RBAC" outside the Kubernetes deploy
			// surface is a straggler — see the k8s allowances below.
			regexp.MustCompile(`(?i)\brbac\b`),
		},
		Allowances: []allowance{
			{
				Name:   "HTTP Authorization header",
				Reason: "`Authorization: Bearer …` is AUTHENTICATION, which forge still supports and generates. Never confuse the header with the removed authorization feature.",
				Token:  regexp.MustCompile(`(?i)\bauthorizations?\b`),
			},
			{
				Name: "the OAuth 2.0 authorization endpoint and request",
				Reason: "OAuth 2.0's own vocabulary collides head-on with the removed feature's. " +
					"`AuthorizeRequest`, `buildAuthorizeUrl` and `AuthorizeUrl` name the " +
					"AUTHORIZATION ENDPOINT of RFC 6749 §3.1 — the URL a user agent is " +
					"redirected to in order to AUTHENTICATE and consent. That is token " +
					"acquisition, which is the authentication half forge kept; it has nothing to " +
					"do with the removed `Authorize` policy hook, role checks or RBAC.\n" +
					"The `Authorize[A-Z]` pattern above was written for AuthorizeHook / " +
					"AuthorizeFunc / AuthorizeOptions — a POLICY callback deciding what a caller " +
					"may do. Scoped by path to the OAuth clients (the Go pkg/oauth2, forge " +
					"login's driver of it in internal/cloud/login.go, and the scaffolded " +
					"browser flow) and by token to the endpoint spellings, so an " +
					"`AuthorizeHook` or a role check appearing in these same files still fails.",
				Token: regexp.MustCompile(`Authorize(?:Request|Url|URL|Endpoint|Params?)\b`),
				Paths: []string{
					"pkg/oauth2/",
					"internal/cloud/login.go",
					"internal/templates/frontend/shared-web/src/lib/auth/",
				},
			},
			{
				Name:   "AuthN/AuthZ as security-review vocabulary",
				Reason: "The generic code-review skill teaches reviewers to look for MISSING authorization in the user's application — which owns that policy now — under the standard AuthN/AuthZ shorthand. The removed forge feature was always spelled `authz` (package and identifier), never `AuthZ`.",
				Token:  regexp.MustCompile(`AuthZ`),
				Paths:  []string{"**/code-review/security-review/SKILL.md", "**/code-review-security-review/SKILL.md"},
			},
			{
				Name: "the Moby AuthZ-plugin advisory this repo accepts as unreachable",
				Reason: "GO-2026-4887's upstream title is \"Moby has AuthZ plugin bypass …\", and the vuln gate's " +
					"exemption entry has to name the advisory it accepts and argue WHY it cannot bite — an " +
					"argument that is precisely \"forge runs no Docker daemon and no authz plugin\". This is " +
					"Docker's AuthZ plugin mechanism (a daemon-side HTTP callout), not the removed forge " +
					"feature: nothing here grants or denies anything to a caller of a forge-generated API.\n" +
					"Scoped to the advisory ID's own vocabulary and to the two files that carry the " +
					"acceptance — forge.yaml's ci.vuln_scan.exemptions block and the filter's tests, whose " +
					"fixtures quote the advisory summary verbatim. An `Authorize` policy hook or a role " +
					"check re-appearing in either file still fails.",
				Token:   regexp.MustCompile(`(?i)authz`),
				Context: regexp.MustCompile(`(?i)moby|docker|GO-2026-4887|plugin`),
				Paths:   []string{"forge.yaml", "internal/cli/ci_vuln_exempt_test.go"},
			},
			{
				Name:   "HTTP 401 Unauthorized",
				Reason: "`unauthorized` / `Unauthorized` / `http.StatusUnauthorized` / the `network:unauthorized` event are the HTTP 401 status — a wire-level authentication outcome, unrelated to the removed feature.",
				Token:  regexp.MustCompile(`(?i)unauthoriz(ed|ation)?`),
			},
			{
				Name:   "Kubernetes RBAC identifiers",
				Reason: "Kubernetes RBAC grants the POD's ServiceAccount access to the API server. It is pod identity, not application authorization, and deleting it breaks `forge env deploy` (operators lose CRD/Lease access). These spellings are unambiguously Kubernetes anywhere they appear.",
				Token: regexp.MustCompile(`(?i)` + strings.Join([]string{
					`rbac\.authorization\.k8s\.io`, // ClusterRole/RoleBinding apiVersion + apiGroup
					`\+kubebuilder:rbac`,           // controller-gen RBAC markers
					`[a-z0-9]*_rbac\b`,             // cluster_rbac, namespaced_rbac, operator_cluster_rbac
					`rbac_[a-z0-9_]+`,              // rbac_lib (the KCL RBAC module)
					`(cluster|namespaced)rbac`,     // ClusterRBAC, forge.ClusterRBAC
					`rbacspec`,                     // RBACSpec
					`rbac\.k\b`,                    // kcl/lib/rbac.k
				}, `|`)),
			},
			{
				Name:    "Kubernetes RBAC named in prose next to a Kubernetes noun",
				Reason:  "Docs, skills and comments say bare \"RBAC\" where no identifier spelling applies (\"Deployment / Job / RBAC\", \"RBAC denied, malformed kubeconfig\"). Requiring an unambiguous Kubernetes noun on the SAME line keeps the carve-out tied to pod identity: an application-RBAC claim (\"Connect RPC, JWT auth, RBAC, PostgreSQL\") has no such neighbour and still fails.",
				Token:   regexp.MustCompile(`(?i)\brbac\b`),
				Context: kubernetesNoun,
			},
			{
				Name: "Kubernetes RBAC prose on the deploy surface",
				Reason: "The KCL module, the cluster code, serverkit's manager wiring and the deploy command discuss Kubernetes RBAC across paragraphs, so the Kubernetes noun is often on a neighbouring line — and the manifest-render tests bind it to a local variable. These files render or apply Kubernetes manifests and nothing else. Application RBAC never lived in any of them; a re-introduction lands in handlers, middleware, frontend or skills, all of which stay guarded.\n" +
					"`env_render*.go` and `clusterhealth*.go` join this list for the same reason, not a weaker one. `forge env render` prints the manifests an environment would apply — one of which IS a ClusterRoleBinding — and the Cluster Workloads check reads pod status from the API server, where `RBAC denying the list` is one of the ways it must answer UNDETERMINED rather than pass. Both talk to Kubernetes and nothing else.\n" +
					"`pkg/deploy/` joins it because it is now THE Kubernetes renderer (ADR 0002 §4): RenderWorkloads turns a Workload into Kubernetes manifests, the Role/ClusterRole/bindings among them, and the capability profiles in pkg/deploy/v1alpha1 decide which destinations may carry that RBAC. It renders Kubernetes manifests and nothing else; application RBAC never lived there.",
				Token: regexp.MustCompile(`(?i)\brbac\b`),
				Paths: []string{
					"kcl/",
					"pkg/deploy/",
					// The template copy of the same deploy surface. The
					// KCL module under kcl/ and the workload template that
					// scaffolds a project's deploy/kcl/ are the same
					// Kubernetes-only prose; scoping to one and not the
					// other made the guard fail on forge's own scaffold.
					"internal/templates/deploy/kcl/",
					"internal/cluster/",
					"internal/kclrender/",
					"internal/kclvendor/",
					"internal/kclplugin/",
					"pkg/serverkit/",
					"internal/cli/deploy*.go",
					"internal/cli/env_render*.go",
					// `forge cluster connect` applies a ClusterRole/ClusterRoleBinding to the
					// owner's cluster, and its skill documents it. Kubernetes manifests only;
					// the files carry an `rbac` local naming that bootstrap-manifest struct.
					"internal/cli/cluster_connect*.go",
					"internal/templates/project/skills/forge/deploy/cluster-connect/SKILL.md",
					// The External Secrets Operator chart's `rbac:` values (aggregation,
					// token-create) and the invariants its skill documents: the
					// operator's own Kubernetes RBAC, in the render golden and the skill.
					"internal/cli/testdata/render_contract/secrets-components.json",
					"internal/templates/project/skills/forge/deploy/external-secrets/SKILL.md",
					// The Flux chart's controller RBAC (rbac = "namespaced") is Kubernetes
					// ClusterRoles/RoleBindings for the controllers' ServiceAccounts: pod
					// identity on the deploy surface, in its skill, runbook and changelog.
					"internal/templates/project/skills/forge/deploy/flux/SKILL.md",
					"internal/templates/project/skills/forge/migrations/v0.1.46/SKILL.md",
					"docs/runbooks/flux-patch-and-upgrade.md",
					"CHANGELOG.md",
					"internal/doctor/clusterhealth*.go",
					"internal/templates/deploy_build_test.go",
				},
			},
			{
				Name:   "Connect PermissionDenied wire code",
				Reason: "`svcerr.PermissionDenied` / `connect.CodePermissionDenied` is the standard Connect/gRPC status code an application returns from ITS OWN policy check. forge transports the code; it does not decide it.",
				Token:  regexp.MustCompile(`(?i)permission[_ ]?denied`),
			},
			{
				Name: "the lint rules that DETECT the removed annotations",
				Reason: "proto-options and vendored-protos exist precisely because a project's vendored " +
					"forge.proto kept declaring `authz_public` / `required_roles` / `authz_custom` / " +
					"`default_roles` on field numbers upstream had reserved: buf compiled them, forge's " +
					"own descriptor had no such fields, and 104 annotations across 14 service protos " +
					"declared an authorization posture enforced by nothing, silently. These rules name " +
					"the dead spellings in order to FIND them in user projects — the reference is the " +
					"feature working, and deleting it would delete the detection along with the record " +
					"of what it is for. Scoped to the lint rules' own files and to the removed option " +
					"spellings, so an actual role check or Authorize hook in these files still fails.",
				Token: regexp.MustCompile(`(?i)\bauthz\b|\brequire[sd]?[_-]?roles?\b|\bdefault_roles\b`),
				Paths: []string{
					"internal/cli/lint/lint_proto_options.go",
					"internal/cli/lint/lint_proto_options_test.go",
					"internal/cli/lint/lint_vendored_protos.go",
					"internal/cli/lint/help_surface_test.go",
				},
			},
			{
				Name: "Kubernetes RBAC prose where the Kubernetes noun is a line away",
				Reason: "Three places describe POD-identity RBAC in prose whose Kubernetes noun sits on a " +
					"neighbouring line, so the kubernetesNoun Context above cannot see it: README's KCL " +
					"model list (\"Application, Environment, ConfigMap, Ingress, RBAC\"), the " +
					"idp-provision workload comment explaining why that job needs a Role to PATCH a " +
					"ConfigMap in its own namespace, and the auth command-tree template's WHY IT NEEDS " +
					"RBAC paragraph about the ServiceAccount token every pod is projected. All three " +
					"are the ServiceAccount permissions that make `forge env deploy` work, not " +
					"application authorization — which none of these files has ever contained. Scoped " +
					"to the bare word in these three paths; every other RBAC spelling stays guarded.",
				Token: regexp.MustCompile(`(?i)\brbac\b`),
				Paths: []string{
					"README.md",
					"internal/codegen/workloads_kcl.go",
					"internal/templates/project/cmd-tree-auth.go.tmpl",
				},
			},
		},
	},
	{
		Name: "ambient-environment-knobs",
		Why: "forge/pkg — the library that compiles into every generated binary — no longer " +
			"changes its behaviour from the process environment. The authentication opt-out, the " +
			"operator gate, the leader-election knobs and the HMAC secret-by-env-var name were all " +
			"removed: a library reads what its caller passed, and a value the app declares nowhere " +
			"cannot be reviewed, deployed, or explained after the fact. Each has a field on the " +
			"options/config struct the package already had. Naming one of these variables in " +
			"guidance tells a reader to set something nothing reads — and in the auth case, to " +
			"believe a server can be run unauthenticated from a shell.",
		Patterns: []*regexp.Regexp{
			// The authentication opt-out. Word-bounded so an app's own,
			// unrelated AUTH_MODE (reliant has one) is not what this matches —
			// this repo simply must not have the spelling at all.
			regexp.MustCompile(`\bAUTH_MODE\b`),
			// The second operator gate serverkit consulted.
			regexp.MustCompile(`\bRUN_OPERATORS\b`),
			// The operatorkit knobs: lease identity/namespace, probe address,
			// client rate, and the leader-election timing trio.
			regexp.MustCompile(`\bLEADER_ELECTION_(?:ID|NAMESPACE)\b`),
			regexp.MustCompile(`\bHEALTH_PROBE_BIND_ADDRESS\b`),
			regexp.MustCompile(`\bOPERATOR_(?:CLIENT_QPS|CLIENT_BURST|LEASE_DURATION|RENEW_DEADLINE|RETRY_PERIOD)\b`),
			// The HMAC validator's "read the secret from the variable this
			// field names" indirection, in its Go and config-file spellings.
			regexp.MustCompile(`\bSecretEnv\b`),
			regexp.MustCompile(`\bsecret_env\b`),
			// cmdkit's flag-then-env resolver: the same laundering, one layer
			// down — the command stayed lint-clean because the read happened
			// inside the library.
			regexp.MustCompile(`\bcmdkit\.(?:Resolve|FirstNonEmpty)\b`),
		},
		Allowances: []allowance{
			{
				Name: "the tests that prove the variables are inert",
				Reason: "Each removal is pinned by a test that EXPORTS the variable and asserts " +
					"nothing changes — which is the only way \"you cannot disable auth from a shell\" " +
					"stays true. Those tests must name the spellings to set them. Scoped to the " +
					"assertion files so the names cannot drift back into shipped code or guidance.",
				Paths: []string{
					"pkg/authn/authn_test.go",
					"pkg/serverkit/operator_gate_test.go",
					"pkg/appkit/operatorkit/operatorkit_test.go",
					"internal/templates/project/middleware_test.go",
				},
			},
		},
	},
	{
		Name: "dev-auth-bypass",
		Why: "The dev-auth bypass — a synthetic bearer token the server accepted in dev mode — " +
			"was deleted server-side. Every client that still mints it is sending a credential " +
			"no backend honors, which fails as a confusing 401 instead of a missing feature.",
		Patterns: []*regexp.Regexp{
			// DEV_BYPASS_TOKEN, DevBypassToken, isDevBypass, devBypass,
			// dev-bypass-do-not-use-in-prod, NEXT_PUBLIC_AUTH_DEV_BYPASS.
			// The separator is optional but may not be a space, so prose that
			// merely NAMES the removed feature ("the dev-auth bypass is gone",
			// which is what a regression test must say) stays legal.
			regexp.MustCompile(`(?i)dev[_-]?bypass`),
			// The sentinel value itself, spelled out so a plain grep for the
			// literal lands here.
			regexp.MustCompile(`dev-bypass-do-not-use-in-prod`),
		},
	},
	{
		Name: "down-migrations",
		Why: "Forge rolls forward only. `forge db migrate down`, the scaffolded binary's " +
			"`db migrate down`, migratekit.Steps, pkg/orm's Rollback/Migration.Down, and every " +
			"writer of a `.down.sql` (migration new, squash, entity birth, goose import) were " +
			"removed: a down script cannot account for what a release already wrote, so the " +
			"recovery is a new forward migration. `forge lint` (no-down-migration) enforces it " +
			"in projects; this entry keeps the capability from creeping back into forge itself.",
		Patterns: []*regexp.Regexp{
			// The command: `migrate down`, `db migrate down`, "migrate", "down".
			regexp.MustCompile(`migrate down\b`),
			regexp.MustCompile(`"migrate",\s*"down"`),
			// Stepping backwards through golang-migrate.
			regexp.MustCompile(`\.Steps\(-`),
			// Writers: a Go expression that BUILDS a down filename.
			regexp.MustCompile(`\+\s*"\.down\.sql"`),
			regexp.MustCompile(`\bDownSQL\b|\bDownPath\b|\bDownBody\b`),
			// Tolerating them: the grandfather knob that turned pre-policy down
			// files into a warning, and the upgrade step that stamped it.
			regexp.MustCompile(`\bDownFilesAllowedUntil\b|\bDownFilesBaseline\b|\brecordDownFilesBaseline\b`),
		},
		Allowances: []allowance{
			{
				Name: "the lint rule and tests that DETECT down migrations",
				Reason: "no-down-migration exists to find these spellings in a project, so " +
					"its rule, its tests, and the tests pinning their absence must name them.",
				Paths: []string{
					"internal/linter/migrationlint/",
					"internal/templates/db_migrate_test.go",
					"internal/cli/db_test.go",
					"pkg/migratekit/",
				},
			},
		},
	},
	{
		Name: "the server-advertised image push base and its on-disk cache",
		Why: "forge used to LEARN where to push hosted artifacts: EnsureEnvironment returned an " +
			"`imagePushBase`, forge wrote it to .forge/state/push-base-<env>.json, and the offline " +
			"consumers (`forge env render`, `forge lint`, the deploy's resolution) read it back. The " +
			"base is now COMPOSED from the env's own declaration — " +
			"`<registry_host>/<organization>/<project>`, internal/hostedimage.PushBase — so the whole " +
			"mechanism is gone: rememberHostedPushBase, cachedHostedPushBase, hostedimage.CachedBase/" +
			"AnyCachedBase/CachePath/CacheRecord/CacheDirRel, the per-env record, " +
			"EnsureHostedEnvironmentFull/EnsuredHostedEnvironment, and wireEnvironment.ImagePushBase.\n" +
			"Three structural costs, which is why this must not come back as a convenience. The base " +
			"was unknowable before the first successful AUTHENTICATED call, so a fresh checkout could " +
			"not render. A stale or absent record silently WEAKENED the off-base check rather than " +
			"failing it, so the rule was quietly inert on exactly the projects that had never deployed. " +
			"And \"the server told us\" plus \"we wrote it down\" are two sources of one fact, which " +
			"drift by construction.\n" +
			"The server did not lose a job here, and that distinction is the one to preserve: it " +
			"ENFORCES the subtree, at publish (ociregistry.Admit) and at the registry realm, which " +
			"mints a token scoped to the authenticated row's org and refuses anything outside it. A " +
			"mis-declared org therefore fails loudly at the first push. Re-adding an advertised base " +
			"would mean forge trusting a value for an address it can compose, which is strictly worse " +
			"than composing it.",
		Patterns: []*regexp.Regexp{
			// The cache's read and write sides, and the Go API that held
			// them. All case-SENSITIVE identifiers with no live homonym.
			regexp.MustCompile(`\brememberHostedPushBase\b|\bcachedHostedPushBase\b`),
			// The cache's own API. NOT CacheDirRel or CacheRecord alone:
			// internal/bundle has an unrelated bundle cache that uses both
			// spellings legitimately, so these are anchored to the package
			// that held the push-base cache.
			regexp.MustCompile(`\bAnyCachedBase\b|hostedimage\.Cache(?:dBase|Path|Record|DirRel)\b`),
			// The wider-return ensure that existed ONLY to carry the base.
			regexp.MustCompile(`\bEnsureHostedEnvironmentFull\b|\bEnsuredHostedEnvironment\b`),
			// The WIRE field, in Go and in a JSON fixture — a fake that
			// served it would let a test pass against a field the real
			// server does not send. Scoped to the field spelling (a
			// selector, a struct field, or a JSON key), so
			// bundle.Repository's `imagePushBase` PARAMETER — a local name
			// for a base the caller composed — stays clear.
			regexp.MustCompile(`\.ImagePushBase\b|ImagePushBase\s+string|"imagePushBase"`),
			// The state file itself, by name.
			regexp.MustCompile(`push-base-[a-z<]`),
			// PROSE that sends a reader to the server for the base. Each of
			// these was a real message or docstring; a stale message is a
			// worse failure than a stale field, because the field fails at
			// load and the message is believed.
			regexp.MustCompile(`(?i)the control plane reports no image push base`),
			regexp.MustCompile(`(?i)the base the (?:platform|control plane) reports`),
			regexp.MustCompile(`(?i)image push base, as (?:last )?stated by the control plane`),
		},
		Allowances: []allowance{
			{
				Name: "the control-plane proto field this replaced, named in the follow-on that deletes it",
				Reason: "control-plane's DeployEnvironment carried `image_push_base` as tag 14, and the cp " +
					"PR that removes it must reserve the number and say what it was. That is the record of " +
					"the removal, not a resurrection of it — and forge does not vendor cp's protos, so " +
					"nothing here can read it either way.",
				Token: regexp.MustCompile(`image_push_base`),
				Paths: []string{"CHANGELOG.md"},
			},
		},
	},
	{
		Name: "the env-level image registry",
		Why: "An environment does not have a registry; a WORKLOAD does, as part of its image. " +
			"ClusterTarget.registry, ControlPlane.registry and DockerBuild.registry are gone, along " +
			"with every Go field that carried one env-wide value (buildOptions.envRegistry/pushRegistry, " +
			"pushRegistryChoice, declaredRegistry, BuildState.Registry, buildtarget.State.Registry, " +
			"ServiceGroup/RawK8sCluster.Registry) and the `registry` key the render used to project onto " +
			"the cluster runtime. forge contributes only the tag and the digest, and composes them onto " +
			"the reference the author wrote — so the image the build pushes and the image the cluster " +
			"pulls cannot disagree, and two workloads in one env can ride two registries. " +
			"This entry keeps the env-wide field from being reintroduced as a convenience — and, " +
			"because the field outlived itself in PROSE, it also guards the sentences that told a " +
			"user to look for it. `forge build --push` printing \"pushes to the registry " +
			"deploy/kcl/<env>/main.k declares on forge.ControlPlane\" named a field that no longer " +
			"exists: a user who followed it went looking in the env file, found nothing, and had no " +
			"way to learn the reference is on the frontend. A stale message is a worse failure than a " +
			"stale field, because the field fails loudly at load and the message is believed.",
		Patterns: []*regexp.Regexp{
			// The Go fields that held the one env-wide value.
			regexp.MustCompile(`\benvRegistry\b|\bpushRegistry\b|\bpushRegistryChoice\b`),
			regexp.MustCompile(`\bdeclaredRegistry\b|\bdeclaredRegistryForEnv\b|\bundeclaredRegistryError\b`),
			regexp.MustCompile(`\bexpandPushRegistries\b|\bresolvePushRegistry\b`),
			// A KCL `registry` field on either env schema, in the module or a
			// scaffold template. Scoped to an assignment so prose, and
			// `registry_inherit` / `registry_mirror` (k3d plumbing, unrelated),
			// stay clear.
			regexp.MustCompile(`(?m)^\s*registry\s*=\s*"`),
			// The removed field, by qualified name. The trailing boundary is
			// load-bearing: `ControlPlane.registry_host` is a DIFFERENT
			// field and a different concept — the platform subtree forge
			// composes a bare hosted image under, which no author chooses —
			// whereas the field removed here was the env-wide registry for
			// every workload's image, which each workload now declares
			// itself. Without the boundary this entry forbids the
			// replacement it was never about.
			regexp.MustCompile(`(?:ClusterTarget|ControlPlane|DockerBuild)\.registry\b`),
			// USER-FACING PROSE that sends someone to an env for a registry.
			// Each of these was a real message or scaffold comment after the
			// field was deleted. They are phrases rather than identifiers
			// because prose is exactly where this survived the removal.
			regexp.MustCompile(`(?i)the registry (?:the |this )?env(?:ironment)?(?:'s(?: KCL)?)? declares`),
			regexp.MustCompile(`(?i)the registry deploy/kcl/`),
			regexp.MustCompile(`(?i)declares on forge\.ControlPlane`),
			// "the env's registry" / "the environment's registry" — the field
			// as a possessive. An env HAS no registry to possess.
			regexp.MustCompile(`(?i)\bthe env(?:ironment)?'s registry\b`),
		},
		Allowances: []allowance{
			{
				Name: "the MIGRATION that removes the field, and the tests that prove it does",
				Reason: "internal/kclmigrate exists to find `registry = \"…\"` in a project's KCL and move it " +
					"onto the images, so its regexes, its fixtures and its tests must write the exact " +
					"spelling being removed. The generate step that runs it, and the closed-schema " +
					"fixtures that assert the field is REFUSED, name it for the same reason: they are the " +
					"code that catches it coming back, so deleting the name to satisfy the guard would " +
					"delete the enforcement.",
				Paths: []string{
					"internal/kclmigrate/",
					"internal/cli/generate_pipeline.go",
					"kcl/tests/closedschema_cluster_target_registry.k",
					"kcl/tests/closedschema_control_plane_registry.k",
					"CHANGELOG.md",
				},
			},
			{
				Name: "the pre-#322 ledger explanation, which must describe the field to say why keys are NOT rewritten",
				Reason: "release_ledger_prerelease.go exists to explain an unverifiable pre-#322 release: its " +
					"OCI keys were bare because the registry lived on the env. Saying so requires naming " +
					"what the env used to carry, in the PAST tense, and it is the argument for not " +
					"rewriting the keys — a text substitution would make the ledger verify green on a " +
					"claim nobody checked. Deleting the phrase to satisfy the guard would delete the " +
					"reasoning that keeps someone from 'fixing' it.",
				Token: regexp.MustCompile(`the env's registry was AT THE TIME|the registry the env used to declare`),
				Paths: []string{"internal/cli/release_ledger_prerelease.go"},
			},
			{
				Name: "prose and helpers about the registry an IMAGE names",
				Reason: "The registry did not stop existing — it moved onto the image. So the words " +
					"`registry` and `registryHost` are everywhere they should be: reading the host off a " +
					"reference (registryHost, is_local_registry, image_registry_host), the " +
					"`forge registry login` / `ref` commands, and the docstrings " +
					"that teach where a registry IS declared. Only the spellings above — an env-wide " +
					"field and the Go plumbing that read one — are forbidden.",
				Token: regexp.MustCompile(`\benvRegistry\b|\bpushRegistryChoice\b|(?m)^\s*registry\s*=\s*"`),
				Paths: []string{
					"internal/cli/registry_cmd.go",
					"internal/cli/build_push_registry.go",
					"kcl/base.k",
					"kcl/lib/images.k",
					"kcl/schema.k",
					"kcl/render.k",
				},
			},
		},
	},
	{
		Name: "ShellBuild ${TOKEN} substitution",
		Why: "A ShellBuild `cmd` is a plain KCL string that forge runs byte-for-byte. " +
			"deploytarget.ExpandVars, buildtarget.Vars/Expand, the Spec fields that fed them " +
			"(TargetArch/Registry/Env), resolveExternalBuildTargetArch, doctor's substituted-command " +
			"preview and audit's conflict_tokens are gone: forge substituted a fixed ${IMAGE}/${TAG}/" +
			"${REGISTRY}/${TARGETARCH}/… vocabulary into a shell program written in a language forge " +
			"does not parse. The values are read in KCL instead, where they already live " +
			"(forge.image_tag(), forge.target_arch(), forge.env(), file.workdir()). " +
			"`forge lint` (shellbuild-tokens) and `forge generate` refuse a leftover token in a " +
			"project; this entry keeps the substitution itself from creeping back into forge.",
		Patterns: []*regexp.Regexp{
			// The substitution functions and the token map.
			regexp.MustCompile(`\bExpandVars\b|\bisShellIdentByte\b`),
			regexp.MustCompile(`\bbuildtarget\.(Vars|Expand)\b`),
			// The arch helper that existed only to feed ${TARGETARCH}.
			regexp.MustCompile(`\bresolveExternalBuildTargetArch\b`),
			// Audit's built-in-token collision surface.
			regexp.MustCompile(`\bexternalBuildBuiltinTokens\b|\bconflictingBuildEnvKeys\b`),
			regexp.MustCompile(`\bconflict_tokens\b|\bconflict_count\b`),
		},
		Allowances: []allowance{
			{
				Name: "the check that DETECTS a leftover token, and the docs that teach the replacement",
				Reason: "internal/shellbuildtokens exists to find these spellings in a project, so its " +
					"token table, its tests and its lint/generate arms must name every one of them. " +
					"The KCL schema docstring, the arch accessor and the fixture that pins the contract " +
					"name them in order to say forge NO LONGER substitutes them — that is the text most " +
					"likely to stop someone reintroducing the pass, so deleting it to satisfy the guard " +
					"would delete the explanation for why the guard exists.",
				Paths: []string{
					"internal/shellbuildtokens/",
					"internal/cli/lint/lint_shellbuild_tokens.go",
					"internal/cli/lint/lint_steps.go",
					"internal/cli/generate_pipeline.go",
					"kcl/schema.k",
					"kcl/lib/build.k",
					"kcl/render.k",
					"internal/buildtarget/buildtarget.go",
					"kcl/tests/positive_shellbuild_plain_kcl.k",
					"CHANGELOG.md",
				},
			},
			{
				Name: "the audit test asserting conflict_count is GONE from the details map",
				Reason: "TestAuditExternalBuilds_TokenNamedEnvKeyIsNotAConflict names the key in order to " +
					"assert it is absent — it reads cat.Details[\"conflict_count\"] and fails if the key is " +
					"present. That is the test that would catch the collision surface coming back, so the " +
					"name has to appear in it. Scoped to the details-map lookup, so a line in this same " +
					"file that actually re-populated conflict_count still fails.",
				Token: regexp.MustCompile(`Details\["conflict_count"\]|"conflict_count"\]`),
				Paths: []string{"internal/cli/audit_external_builds_test.go"},
			},
		},
	},
	{
		Name: "the raw-curl cut-release CI job and `forge reconcile`",
		Why: "build-images.yml's opt-in `cut-release` job cut and promoted by raw curl to " +
			"DeployService, read DEPLOY_TOKEN where forge reads FORGE_CONTROL_PLANE_TOKEN, promoted " +
			"by a hand-copied DEPLOY_ENVIRONMENT_ID, and was broken against the server (no artifact " +
			"kind/mode). It is gone: a hosted project gets release.yml + the vendored forge-deploy " +
			"action, which go through forge. `forge reconcile` was never a verb; reconcile.yml now " +
			"runs `forge env status`. A surviving reference scaffolds a job that cannot work.",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`\bcut-release\b`),
			regexp.MustCompile(`\bDEPLOY_TOKEN\b|\bDEPLOY_ENVIRONMENT_ID\b`),
			regexp.MustCompile("`forge reconcile`|run: forge reconcile\\b"),
		},
		Allowances: []allowance{
			{
				Name: "the tests that pin the removal",
				Reason: "They assert the rendered workflows do NOT contain these spellings, so " +
					"they must name them.",
				Paths: []string{
					"internal/templates/ci_release_test.go",
					"internal/generator/project_ci_reconcile_test.go",
				},
			},
			{
				Name:   "the changelog entries that record the job and its removal",
				Reason: "The CHANGELOG has to name what it removed, or the entry cannot tell a reader upgrading what stopped existing.",
				Paths:  []string{"CHANGELOG.md"},
			},
		},
	},
	{
		Name: "the vendored action's promote/probe/wait steps and its `deploy:` input",
		Why: "The composite action used to run FOUR forge commands to deploy one env: promote, " +
			"a `forge env rollout` probe to decide whether this env converges its own promotions, " +
			"a `forge env deploy` when it does not, and a `forge env wait` on the promotion the " +
			"promote returned. `forge env deploy <env>` is all four (ADR docs/adr/env-verbs.md, V3): " +
			"it records, applies and waits, and it decides who applies from what the env DECLARES " +
			"rather than from a probe or a pipeline flag. So the action's `deploy: auto|true|false` " +
			"input is gone too — there is nothing left for a caller to choose, and a project that " +
			"still passed `deploy: \"true\"` would be configuring a decision forge now makes " +
			"correctly from the env's own render (including the mixed case, which the probe got " +
			"wrong whenever the rollout read exited 5). A surviving promote/rollout/wait step is a " +
			"scaffolded command that exits non-zero, and a surviving `deploy:` input is a lie about " +
			"where the decision lives.",
		// SCOPED TO THE ACTION'S OWN SPELLINGS, deliberately narrowly.
		// `converges_promotions` is still a real field forge reads, and
		// `deploy: true` is an ordinary KCL/config key in a dozen unrelated
		// places — so neither is a pattern here. What is retired is the
		// PROBE that read the field from a shell pipeline, and the files it
		// wrote.
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`jq -r '\.converges_promotions`),
			regexp.MustCompile(`\$\{\{ inputs\.deploy \}\}|inputs\.deploy\b`),
			regexp.MustCompile(`> rollout\.json|> promote\.json`),
		},
		Allowances: []allowance{
			{
				Name: "the tests that pin the removal",
				Reason: "They assert the rendered action does NOT contain these spellings, so " +
					"they must name them.",
				Paths: []string{"internal/templates/ci_release_test.go"},
			},
			{
				Name:   "the changelog entry that records what the action stopped doing",
				Reason: "The CHANGELOG has to name the retired input, or a project upgrading cannot tell why its `deploy:` line is now ignored.",
				Paths:  []string{"CHANGELOG.md"},
			},
		},
	},
	{
		Name: "the vendored KCL-module downgrade guard",
		Why: "`forge generate --allow-kcl-downgrade` and kclvendor.DowngradeError are gone. They " +
			"guarded a committed project-local .forge-kcl/ copy against an older forge rewriting it; " +
			"the module now comes from the rendering binary as a KCL external package, so there is " +
			"no shared copy to protect (docs/adr/0003-kcl-module-from-the-binary.md). A surviving " +
			"reference hands the user a flag that no longer exists.",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`--allow-kcl-downgrade\b`),
			regexp.MustCompile(`\bAllowKCLDowngrade\b|\bDowngradeError\b`),
		},
	},
	{
		Name: "packs",
		Why: "The pack subsystem was retired wholesale — there is no pack root, no pack " +
			"manifest and no `packs` / `pack_overrides` / `features.packs` config key. What " +
			"packs installed is now owned scaffold plus forge/pkg libraries. Naming a pack " +
			"tells a reader (or an agent) to install something that cannot be installed.",
		// A pack reference is only a straggler when the pack it names is not on
		// disk — see packOnDisk. That keeps this entry correct without a pattern
		// edit if packs ever return, and makes a FUTURE pack deletion fail here
		// automatically.
		Resolve: packOnDisk,
		// SCOPE — read before widening. This entry forbids references that NAME
		// a pack or a pack ARTIFACT: things with an on-disk referent Resolve can
		// check. It deliberately does NOT police the word "pack" in prose.
		// forge still ships `crud.Pack` (the live response projection, ~50 uses)
		// and the English verb ("the read path never packs it"), so a pattern
		// broad enough to catch stale prose also catches those — and a pattern
		// that cries wolf gets weakened, which would defeat this guard for all
		// four removals. Naming a pack that cannot be installed is the harm;
		// that is what is caught here.
		Patterns: []*regexp.Regexp{
			// "the jwt-auth pack", "an auth pack", "the audit-log pack's".
			// Two things keep this off `crud.Pack`: the article + name make it a
			// noun phrase the English verb never forms, and `pack` must be
			// LOWERCASE, because the live Go identifier is always `Pack`.
			regexp.MustCompile("\\b(?:[Aa]n?|[Tt]he)\\s+`?([a-z][a-z0-9-]*)`?\\s+packs?\\b"),
			// "jwt-auth pack", "audit-log pack's" — a hyphenated pack name needs
			// no article to be unambiguous.
			regexp.MustCompile("`?\\b([a-z0-9]+(?:-[a-z0-9]+)+)`?\\s+packs?\\b"),
			// A pack PATH: packs/<name>, internal/packs.
			regexp.MustCompile(`\bpacks/([a-z0-9][a-z0-9._-]*)`),
			regexp.MustCompile(`\binternal/packs\b`),
			// The pack MANIFEST format.
			regexp.MustCompile(`(?i)\bpack\.yaml\b`),
			// The retired forge.yaml keys, in YAML and in Go/JSON literals. The
			// trailing colon is what separates the key from the English verb.
			regexp.MustCompile(`\bfeatures\.packs\b`),
			regexp.MustCompile(`\bpack_overrides\b`),
			regexp.MustCompile(`"?\bpacks"?\s*:\s*[\[{a-z"]`),
		},
		Allowances: []allowance{
			{
				Name:   "the removed-key registry",
				Reason: "This table is the MECHANISM that tells a user a forge.yaml key is gone: it must name `packs`, `pack_overrides` and `features.packs` to match them and print the migration message. Deleting these names would silently accept a retired key instead of rejecting it. File-wide because the registry's prose and its keys are inseparable.",
				Paths:  []string{"internal/config/validate.go"},
			},
		},
	},
	{
		Name: "root-component-manifest",
		Why: "The project-root `components.json` manifest was retired. What a project " +
			"CONTAINS is discovered from the code that declares it — the proto descriptor, " +
			"internal/workers/, internal/operators/, cmd/ — via codegen.DiscoverProjectComponents, " +
			"and the project KIND is read off the same tree (config.deriveProjectKindFromSources). " +
			"forge.yaml is project-global only and per-env config is KCL. A second file claiming " +
			"to say what a project contains is a source of truth that is wrong exactly when " +
			"nobody remembered to update it.",
		// READ BEFORE EDITING — the live look-alike.
		//
		// forge still SCAFFOLDS `deploy/kcl/components.k`
		// (codegen.ComponentsKCLRelPath): the project's own, TRACKED,
		// hand-editable declaration of what it is made of. It is written once
		// and never regenerated, which is exactly what separates it from the
		// retired root manifest — it is not a second source of truth forge
		// keeps in sync, it IS the source of truth, and it is KCL rather than
		// JSON. It must keep working; deleting it breaks every render.
		//
		// It is separated from the patterns below by SPELLING: the retired
		// manifest is `components.json` at the project ROOT, and this is
		// `components.k` under deploy/kcl/. See the components.k entry in
		// TestLegitimateLookalikesAreStillPresent, which fails if a widened
		// pattern ever swallows the live file.
		//
		// SCOPE: this entry forbids the retired FILE and the retired Go API
		// that read and wrote it. It deliberately does not police the word
		// "components" or a bare `components:` key — the KCL module, the KCL
		// schemas and the generated projection all use them legitimately, and
		// a pattern that cries wolf gets weakened, which would defeat this
		// guard for every removal in the table. (If forge ever ships a
		// shadcn/ui CLI config — also literally named components.json — that
		// is a real look-alike and earns a narrow Paths-scoped allowance, not
		// a pattern edit.)
		Patterns: []*regexp.Regexp{
			// The manifest by name, on any surface. The escaped dot is the
			// whole separation: `components_gen.json` cannot match it.
			regexp.MustCompile(`(?i)\bcomponents\.json\b`),
			// The kind derivation that read the manifest. Case-SENSITIVE and
			// \b-terminated, so the live `deriveProjectKindFromSources` (kind
			// from the real sources) and `EffectiveProjectKind` cannot reach
			// it.
			regexp.MustCompile(`\bDeriveProjectKind\b`),
			// The manifest reader/writers. All three name a components FILE,
			// which is the retired mechanism.
			regexp.MustCompile(`(?i)\b(?:hasComponentsFile|WriteComponentsFile|AppendComponentToFile)\b`),
		},
	},
	{
		Name: "the forge.components KCL package and its Server/Binary schemas",
		Why: "`Server` meant two different things: a PROTO SERVICE (a set of RPCs " +
			"mounted on a shared mux) and a DEPLOYABLE (an image + args + placement). " +
			"They are not the same object — one binary serving twelve Connect services " +
			"is ONE deployable — and conflating them invited a declaration per proto " +
			"service. A real project declared 14 `servers` that rendered 10 Deployments " +
			"from 2 binaries. What replaced it is `forge.workloads`, whose `Workload` " +
			"schema is exactly one deployable unit, with a `kind` discriminator " +
			"(service/worker/cron/job/operator/tool) instead of six subschemas that " +
			"shared every field and differed only in which expansion ran. " +
			"`Binary` is gone outright: it named a property EVERY workload has (they " +
			"are all executables with args) and its expansion was byte-identical to " +
			"`Worker`'s. What it MEANT — built into the image, never scheduled — is " +
			"`kind = \"tool\"`, which renders no manifest at all rather than an " +
			"unscheduled Deployment nobody addressed.",
		Patterns: []*regexp.Regexp{
			// The package, on any surface (import path or prose).
			regexp.MustCompile(`\bforge\.components\b`),
			// The retired schema names, as a project would write them. Anchored
			// on the `fc.` alias the old scaffold used so the English words
			// "server" and "binary" cannot match.
			regexp.MustCompile(`\bfc\.(?:Server|Worker|Cron|Job|Operator|Binary|Component|ComponentPort|ComponentEnv)\b`),
			// The retired render entry point + env schema.
			regexp.MustCompile(`\brender_components\b|\bComponentEnv\b|\bComponentPort\b`),
			// The retired Go emitters. Their replacements are Workload*.
			regexp.MustCompile(`\b(?:ComponentsKCLRelPath|ComponentsKCLExists|ComponentStanza|ComponentStanzaHint|AppendComponentStanza|ScaffoldComponentsKCL)\b`),
		},
		Allowances: []allowance{
			{
				Name: "the changelog entry announcing the rename",
				Reason: "The CHANGELOG has to name what it removed, or the entry cannot " +
					"tell a reader upgrading what stopped existing.",
				Token: regexp.MustCompile(`forge\.components|fc\.(?:Server|Worker|Cron|Job|Operator|Binary|Component|ComponentPort|ComponentEnv)|render_components|ComponentEnv|ComponentPort|ComponentsKCLRelPath|ComponentsKCLExists|ComponentStanza|ComponentStanzaHint|AppendComponentStanza|ScaffoldComponentsKCL`),
				Paths: []string{"CHANGELOG.md"},
			},
		},
	},
	{
		Name: "generated components_gen.json",
		Why: "Deploy has ONE source of truth and it is KCL. forge used to also emit " +
			"`deploy/kcl/components_gen.json` — a gitignored, regenerated-every-run " +
			"projection of the discovered Inventory that each per-env main.k read via " +
			"`fc.load_components(fc.COMPONENTS_GEN)`. That split one concept across two " +
			"files, one of them untracked, so a FRESH CLONE rendered ZERO manifests " +
			"SILENTLY until someone ran `forge generate`. It also made forge decide " +
			"things it has no business deciding: which components exist in which " +
			"environment, and whether a migration step runs. Those are per-env choices " +
			"a project must be able to hand-edit — bring in NATS, use a hosted database " +
			"in prod and a container in dev, change a port — and a file forge rewrites " +
			"every run cannot hold them. What replaced it is `deploy/kcl/components.k`: " +
			"scaffolded ONCE, tracked, appended to by `forge scaffold`, never " +
			"regenerated. Drift is reported by `forge lint`, not repaired.",
		Patterns: []*regexp.Regexp{
			// The file, on any surface. Underscore-anchored, so the live
			// `components.k` cannot match it.
			regexp.MustCompile(`\bcomponents_gen\.json\b`),
			// The Go API that wrote it.
			regexp.MustCompile(`\b(?:ComponentsJSONRelPath|GenerateComponentsJSON|ComponentsToJSON)\b`),
			// The KCL loaders that read it. `load_components`/`load_migrate`
			// were the only readers; COMPONENTS_GEN was the path constant.
			regexp.MustCompile(`\b(?:load_components|load_migrate|COMPONENTS_GEN)\b`),
		},
		Allowances: []allowance{
			{
				Name: "the changelog entry announcing the removal",
				Reason: "The CHANGELOG has to name what it removed, or the entry cannot " +
					"tell a reader upgrading what stopped existing.",
				Token: regexp.MustCompile(`components_gen\.json|ComponentsJSONRelPath|GenerateComponentsJSON|ComponentsToJSON|load_components|load_migrate|COMPONENTS_GEN`),
				Paths: []string{"CHANGELOG.md"},
			},
		},
	},
	{
		Name: "project add / project scaffold",
		Why: "Scaffolding collapsed onto ONE verb where arity picks the granularity: " +
			"`forge scaffold` births everything the protos imply, `forge scaffold <noun> …` " +
			"scaffolds exactly one thing. The two-word spellings `forge project add <noun>` " +
			"and `forge project scaffold` are gone — no alias, no hidden name, no shim. " +
			"`add` was the wrong verb (these commands write code, they do not add to a list) " +
			"and `project` carried no information (every forge command operates on a project).",
		Patterns: []*regexp.Regexp{
			// The fully-spelled invocation, in help text, docs, skills,
			// comments and error hints.
			regexp.MustCompile(`forge\s+project\s+(?:add|scaffold)\b`),
			// The same command with the binary name substituted at runtime
			// ("%s project add entity", "reliant forge project add worker").
			// Anchored on a real noun so the English "project scaffold"
			// (as in "a project scaffolded by an older forge") cannot reach
			// it.
			regexp.MustCompile(`\bproject\s+add\s+(?:service|worker|operator|crd|frontend|scenario|webhook|package|adapter|binary|library|handler-file|rpc|entity)\b`),
			// The Go ARGV form: exec/test invocations pass the command as
			// separate string args, so the tokens are never adjacent in the
			// source and neither pattern above can see them. 25 files —
			// almost the whole `-tags e2e` corpus — carried the old spelling
			// past the rename precisely this way, and the lane compiles
			// without running, so nothing failed until one was executed.
			regexp.MustCompile(`"project",\s*"(?:add|scaffold)"`),
		},
		Allowances: []allowance{
			{
				Name: "the tests that prove the old spellings are gone",
				Reason: "These files assert against the assembled cobra tree that `forge project add …` " +
					"and `forge project scaffold` resolve to nothing — they must name the removed " +
					"spellings to test for their absence. Scoped by both path and token, so any OTHER " +
					"reference in the same files still fails.\n" +
					"group_strict_test.go earned its entry the hard way: it was NOT listed, the argv " +
					"pattern flagged its `{\"project\", \"add\", \"entity\"}` case, and the case was " +
					"swept to `{\"scaffold\", \"entity\"}` — which is a VALID command, so the " +
					"assertion silently stopped testing anything and started failing for an unrelated " +
					"reason. A guard that forces a rewrite of the one test asserting a thing is absent " +
					"destroys the assertion it was protecting.",
				// Both spellings: the user-typed form, and the Go ARGV form these
				// assertions actually use (cobra Find takes a []string).
				Token: regexp.MustCompile(`(?:forge\s+)?project\s+(?:add|scaffold)(?:\s+[a-z-]+)?|"project",\s*"(?:add|scaffold)"`),
				Paths: []string{
					"internal/cli/scaffold_surface_test.go",
					"internal/cli/new_next_steps_test.go",
					"internal/cli/group_strict_test.go",
				},
			},
			{
				Name: "the changelog entry announcing the rename",
				Reason: "A Keep-a-Changelog `### Changed` entry has to name the spelling it replaced, " +
					"or readers cannot tell which of their invocations broke. The changelog is prose " +
					"about history, not a shipped surface — no tool reads it, and nothing scaffolds from it.",
				Token: regexp.MustCompile(`(?:forge\s+)?project\s+(?:add|scaffold)(?:\s+[a-z-]+)?`),
				Paths: []string{"CHANGELOG.md"},
			},
		},
	},
	{
		Name: "per-component port",
		Why: "A component never carries a port. Every service in a binary mounts onto the SAME " +
			"Connect mux and the process listens ONCE, on AppConfig.port (env PORT, default " +
			"8080) — config.DefaultServePort. Any other port is a DEPLOY fact declared per " +
			"environment in deploy/kcl/<env>/main.k on forge.workloads.Workload.ports, so " +
			"nothing forge introspects (the proto descriptor, owned worker/operator files, " +
			"cmd/) can state one. The Go-side carrier — ComponentConfig.Ports, PortSpec, " +
			"HTTPPortName, PrimaryPort() and the components_gen.json `ports` key — is gone. " +
			"It came back once already: e3b7fa39 deleted the port HEURISTIC but left the " +
			"FIELD, and the always-zero it returned then flowed into six consumers, an audit " +
			"finding that could never fire, a generated architecture doc that printed `0`, " +
			"and a scaffolded frontend baked to http://localhost:0.",
		Patterns: []*regexp.Regexp{
			// The accessor whose answer was always zero, and the two types
			// that carried it. All three are case-SENSITIVE Go identifiers
			// with no live homonym: the KCL schema is spelled ComponentPort
			// (no "Spec"), and a k8s/compose port list is `ports`/`Ports`,
			// which none of these can reach.
			regexp.MustCompile(`\bPrimaryPort\b`),
			regexp.MustCompile(`\bPortSpec\b`),
			regexp.MustCompile(`\bHTTPPortName\b`),
			// The JSON projection of the field, and the key it wrote into
			// components_gen.json. Anchored on `ports` as a JSON key with the
			// component doc's exact spelling, so `listen_ports`, `egress_ports`,
			// `host_ports` and K8sCluster's own `ports` stay untouched.
			regexp.MustCompile(`\bcomponentPortJSON\b`),
			// SCOPE: this entry forbids the per-COMPONENT port carrier, not
			// the word "port". Ports are real and everywhere — the KCL
			// ComponentPort schema, HostDeploy.listen_ports, K8sCluster.ports,
			// the frontend dev-server port, PORT itself. A pattern that
			// policed those would cry wolf, and a guard that cries wolf gets
			// weakened until it guards nothing.
		},
		Allowances: []allowance{
			{
				Name: "the dead-code guard's record of this exact defect",
				Reason: "deadcodeguard's doc, quarantine ledger and testdata fixture reproduce the " +
					"historical defect BY NAME — a planted Component.Ports/PrimaryPort pair and the " +
					"false-green test over it — because that is the shape its phantom-field rule " +
					"exists to catch. That is the guard's evidence, not a live carrier. Scoped to " +
					"that one package by path and to these identifiers by token.",
				Token: regexp.MustCompile(`\bPrimaryPort\b|\bPortSpec\b|\bHTTPPortName\b|\bcomponentPortJSON\b`),
				Paths: []string{"internal/deadcodeguard/"},
			},
			{
				Name: "the changelog entry announcing the removal",
				Reason: "A Keep-a-Changelog `### Removed` entry has to name the identifiers it removed, " +
					"or a reader whose build just broke cannot tell which symbol went away. The " +
					"changelog is prose about history, not a shipped surface — no tool reads it, and " +
					"nothing scaffolds from it.",
				Token: regexp.MustCompile(`\bPrimaryPort\b|\bPortSpec\b|\bHTTPPortName\b|\bcomponentPortJSON\b`),
				Paths: []string{"CHANGELOG.md"},
			},
		},
	},
	{
		Name: "the entity-annotation convention rules",
		Why: "`forgeconv-pk-annotation` and `forgeconv-timestamps` fired only on messages " +
			"carrying `(forge.v1.entity)`, and told the author to add `pk: true` / " +
			"`(forge.v1.field)`. Those annotations are retired — `forge generate` prints a " +
			"notice when it sees one, and the migration skill says to delete them. So the " +
			"rules could only ever teach the shape forge had just deleted, and only to " +
			"someone who had not migrated yet. Entities come from SQL now: db/migrations " +
			"drive the ORM projections, and the primary key and timestamp columns are the " +
			"migration's business. Two rules, and the whole annotation-tracking half of the " +
			"proto mini-parser that existed to feed them, are gone.",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`forgeconv-pk-annotation`),
			regexp.MustCompile(`forgeconv-timestamps`),
			// The Go identifiers behind them, so a resurrection under the old
			// name is caught even before it acquires a rule string.
			regexp.MustCompile(`\bcheckPKAnnotation\b|\bcheckTimestampAnnotation\b|\bisTimestampShapedFieldName\b`),
			// The parser fields that existed only to feed the two rules. A
			// re-introduced HasEntityAnnotation is the same removal coming
			// back through the parser rather than through a rule.
			regexp.MustCompile(`\bHasEntityAnnotation\b|\bHasTimestampsTrue\b|\bHasSoftDeleteTrue\b|\bHasFieldAnnotation\b|\bHasPKTrue\b|\btrackEntityOpts\b`),
		},
	},
	{
		Name: "the marker-scaffold CRUD test pair",
		Why: "GenerateCRUDTests used to emit two files per service: `handlers_crud_gen_test.go` " +
			"(per-RPC AnyOutcome frames) and `handlers_crud_integration_test.go` (a build-tag-gated " +
			"suite). Both were retired for ONE user-owned lifecycle test, `handlers_crud_test.go`, " +
			"and GenerateCRUDTests now DELETES them from projects that still carry them. The " +
			"references survived anyway, and the worst one was on the success path: `forge generate` " +
			"printed \"Generated handlers/<svc>/handlers_crud_gen_test.go (unit) + " +
			"handlers_crud_integration_test.go (-tags integration)\" — announcing, by name, two files " +
			"it had just deleted and never writes. A `_gen` suffix also states forge OWNS the file; " +
			"the file it really writes is scaffold-once and user-owned, so the message inverted " +
			"ownership on the one line the author reads.",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`\bhandlers_crud_gen_test\.go\b`),
			regexp.MustCompile(`\bhandlers_crud_integration_test\.go\b`),
		},
		Allowances: []allowance{
			{
				Name: "the retirement sweep that deletes them",
				Reason: "removeRetiredScaffoldTest has to NAME both files to delete them from projects " +
					"scaffolded before the retirement. Naming a file in order to remove it is the " +
					"removal, not a reference to it. Scoped to the two call lines and their comment " +
					"in the generator that performs the sweep.",
				Token: regexp.MustCompile(`\bhandlers_crud_gen_test\.go\b|\bhandlers_crud_integration_test\.go\b`),
				Paths: []string{"internal/codegen/crud_gen.go"},
			},
			{
				Name: "the ignore rule keeping a legacy project's copy committed",
				Reason: "`handlers/**/*_gen.go` is ignored, so a legacy project that still carries the " +
					"retired handlers_crud_gen_test.go would have it swept up by the glob. The " +
					".gitignore comment names the file to explain why *_gen_test.go is deliberately " +
					"NOT ignored — committing it is what keeps `go test ./...` green on a fresh " +
					"clone of such a project. The reference exists to protect a file forge no " +
					"longer writes, not to keep writing it. Scoped to .gitignore.",
				Token: regexp.MustCompile(`\bhandlers_crud_gen_test\.go\b`),
				Paths: []string{".gitignore"},
			},
			{
				Name: "the tests that assert both files are ABSENT",
				Reason: "The retirement sweep is covered by tests that build a project carrying the old " +
					"pair and assert it is gone afterwards. They must name the files to look for them. " +
					"Forcing a rewrite of the one test proving a thing is absent is how a guard " +
					"deletes its own evidence.",
				Token: regexp.MustCompile(`\bhandlers_crud_gen_test\.go\b|\bhandlers_crud_integration_test\.go\b`),
				Paths: []string{
					"internal/codegen/crud_gen_test.go",
					"internal/codegen/generator_test.go",
					"internal/linter/scaffolds/scaffolds_test.go",
				},
			},
			{
				Name: "migration skills addressing projects that already OWN the file",
				Reason: "What was removed is forge EMITTING the pair. A project scaffolded before the " +
					"retirement that then cleared every FORGE_SCAFFOLD marker owns its " +
					"handlers_crud_gen_test.go outright — the sweep leaves those alone — so a " +
					"migration skill still has to name the file to tell that reader what to do with " +
					"it. Same carve-out the sibling `handlers_crud_gen.go` pre-split name already " +
					"earns in knownGeneratedHandlerFiles.",
				Token: regexp.MustCompile(`\bhandlers_crud_gen_test\.go\b|\bhandlers_crud_integration_test\.go\b`),
				Paths: []string{
					// Any per-release migration skill under migrations/
					// earns this carve-out: naming the retired file is how
					// it tells a reader who still owns one what to do.
					"**/migrations/*/SKILL.md",
					"**/migration-upgrade/SKILL.md",
					"internal/templates/skills_validation_test.go",
				},
			},
			{
				Name: "the .gitignore note explaining the retirement to legacy projects",
				Reason: "The scaffolded .gitignore deliberately documents that a LEGACY project may " +
					"still carry the retired file and that it must be committed either way. Naming " +
					"a retired file in order to say it is retired is the removal, not a reference.",
				Token: regexp.MustCompile(`\bhandlers_crud_gen_test\.go\b|\bhandlers_crud_integration_test\.go\b`),
				Paths: []string{"internal/templates/project/.gitignore"},
			},
		},
	},
	{
		Name: "the root spellings of the `forge env` verbs",
		Why: "Every environment-REQUIRED lifecycle verb moved under the `env` noun and now takes " +
			"the environment as a POSITIONAL argument: `forge env up dev`, `forge env deploy prod`. " +
			"The root spellings — `forge up`, `forge down`, `forge deploy`, `forge smoke`, " +
			"`forge devstack`, `forge promote`, `forge secrets` — are gone, no alias and no hidden " +
			"name, and so is the `--env=<env>` flag that used to carry the environment on up/down. " +
			"An env is what these commands act ON, not a modifier, and `cobra.ExactArgs(1)` turns " +
			"forgetting it into an error instead of a silent default. Commands where the env really " +
			"IS an optional modifier (`forge build [environment]`) kept their " +
			"flag and stayed at the root. A surviving `forge up --env=dev` is a copy-pasteable " +
			"command — in a doc, a skill, a KCL comment or a scaffolded script — that now dies on " +
			"\"unknown command\".",
		Patterns: []*regexp.Regexp{
			// The removed root verbs, on any surface. The trailing \b is what
			// keeps `forge upgrade` out (and `forge deploys`, the English verb);
			// requiring `forge` IMMEDIATELY before the verb is what keeps both
			// the live `forge env deploy` and the live siblings `forge cluster
			// up` / `forge db migrate up` out.
			regexp.MustCompile(`\bforge\s+up\b`),
			regexp.MustCompile(`\bforge\s+down\b`),
			regexp.MustCompile(`\bforge\s+(?:deploy|smoke|devstack|promote|secrets)\b`),
			// The removed FLAG, anchored on the verb it belonged to — which
			// also catches the half-renamed `forge env up --env=dev`, the shape
			// a mechanical sweep produces and the one that reads as correct.
			// Anchoring on up/down is what leaves the live `--env` flags
			// alone — `forge release where --env`, `forge secret set --env`,
			// `forge domain bind --env`, where the env really is a modifier;
			// requiring `=` or a space after the flag is what leaves
			// `docker compose up --env-file` alone.
			regexp.MustCompile(`\b(?:up|down)\s+--env[= ]`),
			// The Go ARGV form: exec/test invocations pass the command as
			// separate string args, so the tokens are never adjacent in the
			// source and neither pattern above can see them.
			regexp.MustCompile(`"(?:up|down)",\s*"--env`),
		},
	},
	{
		Name: "the top-level `forge run` dev runner",
		Why: "`forge run` was a thin alias over the SAME runUp that `forge env up <env>` calls. " +
			"Its one distinct feature — forwarding tokens after `--` to the frontend dev servers " +
			"— moved onto the env verb, so the local lifecycle is `forge env up <env> " +
			"[-- <dev-server flags>]` and nothing is lost. Two spellings of one lifecycle had " +
			"already drifted: the alias carried the environment as an `--env` FLAG defaulting to " +
			"dev, where `env up` takes it as a REQUIRED positional, so \"which env is running?\" " +
			"had two different answers depending on which spelling you typed. A surviving " +
			"`forge run` is a copy-pasteable command — in a doc, a skill, a KCL comment or a " +
			"scaffolded script — that now dies on \"unknown command\".",
		Patterns: []*regexp.Regexp{
			// SCOPE — read before widening. This forbids `forge run` as an
			// INVOCATION: the command name, optionally with its old flag or
			// the `--` terminator after it, and the `forge run show`
			// non-command the ci-run entry used as a foil.
			//
			// It deliberately requires `forge` IMMEDIATELY before `run`,
			// which is what leaves the LIVE `forge ci run` alone — the only
			// `run` forge still has.
			//
			// It also deliberately does NOT police the English verb. "forge
			// runs the migration", "forge run-time", "what forge runs" are
			// ordinary prose all over the tree, and a pattern broad enough
			// to catch stale commands also catches those — a pattern that
			// cries wolf gets weakened, which would defeat this guard for
			// every removal in the table. Requiring a word boundary and
			// then either end-of-token punctuation, a flag, or the
			// terminator is what separates the invocation from the verb.
			regexp.MustCompile("`forge run`"),
			regexp.MustCompile(`\bforge run\s+--`),
			regexp.MustCompile(`\bforge run\s+(?:show|$)`),
			// The removed flag, anchored on the command it belonged to.
			regexp.MustCompile(`\bforge run --env`),
			// The deleted Go constructor and its passthrough helper, so a
			// resurrection is caught before it acquires any help text.
			regexp.MustCompile(`\bnewRunCmd\b`),
			regexp.MustCompile(`\brunPassthroughArgs\b`),
		},
		Allowances: []allowance{
			{
				Name: "the record of the collision that named `forge ci run`",
				Reason: "An earlier design spec called the run timeline `forge run show`, and that name was " +
					"rejected because it collided with the then-live dev-server `forge run`. Both the " +
					"CHANGELOG entry and the doc comment on the command that shipped record this, because " +
					"it is the rationale for a name forge STILL ships — and `ci_run.go` says in the same " +
					"breath that the dev-server command has since been deleted and the name does not move " +
					"back. Rewriting the history to name a command that did not exist at the time would " +
					"falsify it. Scoped to those two phrasings, so a doc that TELLS someone to run the " +
					"deleted command still fails.",
				Token: regexp.MustCompile("`forge run show`|dev-server `forge run`|`forge run` is the dev-server runner"),
				Paths: []string{"CHANGELOG.md", "internal/cli/ci_run.go"},
			},
			{
				Name: "the test asserting no top-level `run` is registered again",
				Reason: "TestCIRun_IsUnderCIAndNotUnderRun walks the root command list and fails if a " +
					"top-level `run` reappears. Naming the deleted spelling is how the assertion says what " +
					"it forbids — this is the Go-level guard that catches a resurrection before any doc or " +
					"help text mentions it, so deleting the text to satisfy this sweep would remove the " +
					"check. Scoped to that one failure message.",
				Token: regexp.MustCompile("a top-level `forge run` is registered again"),
				Paths: []string{"internal/cli/ci_run_test.go"},
			},
		},
	},
	{
		Name: "`forge release cut` and the publishing flags on top-level `forge build`",
		Why: "Publishing is an ENVIRONMENT act, so it moved onto the env noun as " +
			"`forge env build <env> [--push] [--release vX]`. Both flags always NEEDED an env to " +
			"resolve — a push destination is declared per workload in the env's render, and a " +
			"release's artifact set (including the per-env external build_cmd images that exist " +
			"nowhere else) is discovered from deploy/kcl/<env>/main.k — so on a command whose env " +
			"argument is OPTIONAL they were a combination that could only be rejected at runtime. " +
			"Top-level `forge build` is now compile-only: a local check that the tree builds, which " +
			"is exactly why its env argument can stay optional.\n" +
			"`forge release cut` was DELETED in the same move. It was the cut WITHOUT the build, for " +
			"a pipeline whose build and release are separate jobs — that is " +
			"`forge env build <env> --release vX --no-build`, the same code path with the build " +
			"phase off. Two spellings of one cut meant two places for the release's completeness " +
			"gate to drift, and that gate is the only thing standing between a release with a hole " +
			"in it and a promotion that ships one. A surviving `forge build --push`, " +
			"`forge build --release` or `forge release cut` is a copy-pasteable command — in a doc, " +
			"a skill, a CI workflow or a scaffolded script — that now exits non-zero.",
		Patterns: []*regexp.Regexp{
			// The deleted subcommand, in any invocation shape.
			regexp.MustCompile(`\bforge release cut\b`),
			regexp.MustCompile("`release cut`"),
			// The publishing flags ON `forge build` specifically. Anchored on
			// `forge build` so the LIVE `forge env build <env> --push` and
			// `--release` are untouched — `forge env build` does not match
			// `forge build` (the word `env` sits between), which is the whole
			// reason this pattern can be this narrow.
			regexp.MustCompile(`\bforge build\b[^\n]{0,60}--push\b`),
			regexp.MustCompile(`\bforge build\b[^\n]{0,60}--release\b`),
			// The Go ARGV form: exec/test invocations pass the command and
			// flag as separate string args, so the tokens are never adjacent
			// in the source and the patterns above cannot see them.
			regexp.MustCompile(`"release",\s*"cut"`),
			// The deleted Go constructor, so a resurrection is caught before
			// it acquires any help text.
			regexp.MustCompile(`\bnewReleaseCutCmd\b`),
		},
		Allowances: []allowance{
			{
				Name: "the text that documents where `release cut` went",
				Reason: "Three places name the deleted command in order to say it is deleted and where it " +
					"went: env_build.go's doc comment (the command that absorbed it, explaining that " +
					"--no-build IS the cut-only half), release_cmd.go's note on why the release noun " +
					"holds no cut verb, and the tests that assert `cut` is no longer registered and " +
					"that the run flags moved with it. That last one is the Go-level guard which " +
					"catches a resurrection before any doc mentions it, so deleting the text to " +
					"satisfy this sweep would remove the check. Scoped to those files.",
				Token: regexp.MustCompile("`forge release cut`|`release cut`"),
				Paths: []string{
					"internal/cli/env_build.go",
					"internal/cli/env_build_test.go",
					"internal/cli/release_cmd.go",
					"internal/cli/run_identity_test.go",
				},
			},
			{
				Name: "the English noun \"release cut\"",
				Reason: "\"The release cut\" is the ordinary name for the EVENT of cutting a release, and " +
					"forge still cuts releases — pkg/release's StageCut is literally that stage, and " +
					"the ledger/verify/deploy code describes a release's own provenance in those " +
					"words (\"a release cut on a different machine\", \"an idempotent release cut\"). " +
					"Only the COMMAND went away. A pattern broad enough to catch stale prose also " +
					"catches the noun, and a pattern that cries wolf gets weakened — which would " +
					"defeat this guard for every removal in the table. The command-shaped patterns " +
					"above require either `forge ` immediately before it or the backticked code " +
					"span, so this allowance only needs to cover the bare English phrase.",
				Token: regexp.MustCompile(`(?i)\b(?:the|a|an|every|idempotent|same) release cut\b|release cut (?:is|was|on|with|resolves|keys|today)\b`),
			},
		},
	},
	{
		Name: "the `forge dev` command group",
		Why: "The k3d lifecycle and dev-state introspection verbs were promoted flat out of the " +
			"`dev` namespace onto `forge cluster`: `forge cluster up|down|reset|reload|status|" +
			"logs|info|urls|instances`. `forge dev` itself is not a command — nothing resolves it. " +
			"`forge dev port-forward` did not move at all: host port-forwarding was RETIRED in " +
			"favour of the Gateway API ingress model, where a service is reachable through a " +
			"declared route or not at all, so a doc that still names it promises a way out that " +
			"no longer exists.",
		Patterns: []*regexp.Regexp{
			// SCOPE — read before widening. This entry forbids `forge dev`
			// followed by a SUBCOMMAND. It deliberately does NOT police a bare
			// "forge dev", because that is a live English adjective all over the
			// tree: "a forge dev server", "every forge dev namespace", "the
			// forge dev loop", "a forge dev capability", "the forge dev
			// controller", "the forge dev-stack convention". A pattern broad
			// enough to catch stale prose also catches those — and a pattern
			// that cries wolf gets weakened, which would defeat this guard for
			// every removal in the table. Naming a subcommand that cannot be
			// invoked is the harm; that is what is caught here.
			regexp.MustCompile(`\bforge\s+dev\s+(?:cluster|status|info|logs|urls|instances|port-forward|up|down|reset|reload)\b`),
		},
	},
	{
		Name: "the retired package kinds",
		Why: "forge named six internal-package \"kinds\"; four bought nothing a rule could read " +
			"and are gone. `//forge:strategy` folded into `//forge:exclude-contract` (which says " +
			"the same thing and has real users). `//forge:interactor` and its " +
			"`forge scaffold package --type interactor` scaffold are gone — an orchestrator is a " +
			"service whose deps are other services' interfaces, so `--type service` already " +
			"births it, and the three-file interactor template tree emitted the service scaffold " +
			"with different comments. The \"utility\" kind was never a kind: it was " +
			"`interfaceCount == 0`, a rule predicate that needed no name. And the rule the " +
			"interactor marker gated is now `forgeconv-deps-are-interfaces`, un-gated and " +
			"applying wherever a `type Deps struct` exists — the gate is exactly why it fired on " +
			"zero packages while control-plane carried 28 concrete-typed Deps fields.",
		Patterns: []*regexp.Regexp{
			// SCOPE — read before widening. This entry forbids the MARKERS, the
			// FLAG VALUE, the retired rule id and the deleted Go API. It
			// deliberately does NOT police the words "interactor", "utility" or
			// "strategy". All three survive as ordinary design vocabulary that
			// forge still ships on purpose: the `interactor` skill (which now
			// opens by saying an interactor is NOT a forge package kind),
			// `forge scaffold package`'s own help explaining why an orchestrator
			// needs no flag, "pure-utility packages" in the contracts skill, and
			// "multiple implementations / strategy pattern" in service-layer.
			// A pattern broad enough to catch stale prose also catches those —
			// and a pattern that cries wolf gets weakened, which would defeat
			// this guard for every removal in the table. Naming a marker no
			// parser reads, or a flag value that now exits non-zero, is the
			// harm; that is what is caught here.
			regexp.MustCompile(`forge:strategy\b`),
			regexp.MustCompile(`forge:interactor\b`),
			// The flag value, in every spelling a doc, a skill, a charter or a
			// help string writes it: `--type interactor`, `--type=interactor`,
			// and the half-swept `--type=adapter|interactor`.
			regexp.MustCompile(`--type[= ]"?[a-z|]*interactor`),
			// The Go ARGV form: exec/test invocations pass the flag and its
			// value as separate string args, so the tokens are never adjacent
			// in the source and the pattern above cannot see them.
			regexp.MustCompile(`"--type",\s*"interactor"`),
			// Re-adding it to the flag map — the resurrection that happens
			// before any help text or doc mentions it.
			regexp.MustCompile(`"interactor":\s*true`),
			// The deleted three-file template tree.
			regexp.MustCompile(`internal-package/interactor\b`),
			// The retired rule id. The live successor is spelled
			// `forgeconv-deps-are-interfaces`; requiring the `interactor-`
			// infix is what keeps this off it.
			regexp.MustCompile(`forgeconv-interactor-deps-are-interfaces`),
			// The Go API behind all of it, so a resurrection is caught before
			// it acquires a marker, a flag or a rule string. All are
			// case-SENSITIVE identifiers with no live homonym.
			regexp.MustCompile(`\bHasInteractorDirective\b|\bInternalPackageInteractor\b|\bRuleInteractorDepsAreInterfaces\b`),
			// The "utility kind" predicate and the file that held it, plus the
			// strategy-marker reader.
			regexp.MustCompile(`\bisUtilityPackage\b|\bfileHasStrategyDirective\b|\butility_skip\.go\b`),
		},
	},
	{
		Name: "the seeder's demo identity and its table-name classifiers",
		Why: "pkg/seedplan used to read a table's NAME for domain meaning. `detectIdentity` " +
			"elected any table called `users`/`user` with a single string `id` key to be THE user " +
			"table, and stamped row 0 with DemoUserID/DemoUserEmail/DemoUserName — literals it " +
			"copied off the JWT scaffold and declared forge-wide canonical. `isOrgTableName` " +
			"carried a list of organization-shaped English spellings that OVERRODE the column " +
			"evidence beside it, so a table named `agencies` with first_name/last_name/" +
			"date_of_birth was still classified as a company. Both are gone, along with " +
			"identityKind and the hasSingleStringIDPK shape test that existed only to feed the " +
			"first one.\n" +
			"The demo identity was not cosmetic: `dev-user-001` is not a UUID, so a `users` table " +
			"with a `uuid` primary key — an entirely ordinary schema — made postgres reject the " +
			"INSERT, and because `forge db seed` is one transaction, that ONE table took the whole " +
			"dataset down with it. Which row is the authenticated principal is domain knowledge " +
			"the app declares; forge does not invent a user for an app that has none. The " +
			"COLUMN-name half of the same disease is the entry below.",
		Patterns: []*regexp.Regexp{
			// The three exported literals, and the constant block that held them.
			regexp.MustCompile(`\bDemoUser(?:ID|Email|Name)\b`),
			// The classifiers and the enum they produced. All case-SENSITIVE Go
			// identifiers with no live homonym anywhere in the tree.
			regexp.MustCompile(`\bdetectIdentity\b`),
			regexp.MustCompile(`\bisOrgTableName\b`),
			regexp.MustCompile(`\bhasSingleStringIDPK\b`),
			regexp.MustCompile(`\bidentity(?:Kind|None|User)\b`),
			// SCOPE — read before widening. This entry forbids the seeder's demo
			// IDENTITY and its table-name classifiers. It deliberately does NOT
			// police the literals `dev-user-001` / `dev@localhost` / `Dev User`:
			// those are the FRONTEND dev session and the JWT test minter's claims,
			// which are live, separately owned, and unrelated to what any row of a
			// seeded database contains. The seeder's sin was ADOPTING them as a
			// forge-wide truth, and that is what the identifiers above catch.
			// It also does not police the word "identity" — pod identity, an
			// identity mapping and an IdP identity claim are all live vocabulary.
		},
	},
	{
		Name: "the seeder's column-name domain heuristics",
		Why: "pkg/seedplan used to decide what a column MEANS from what it was CALLED. " +
			"`price`/`amount`/`*_cents` were money, `currency`/`*_currency` pinned to the constant " +
			"USD (even overriding a CHECK vocabulary that offered EUR and GBP), `*color*`/`*_hex` " +
			"and the design tokens primary/secondary/accent/background drew hex colors, `email`/" +
			"`*_email` drew addresses, `phone` drew numbers, `uri`/`link`/`*_url` drew URLs, " +
			"`avatar_url`/`image_url` drew a data: URI, `last4` drew four digits, `date_of_birth`/" +
			"`dob`/`born_on` drew birthdates, `date`/`*_date`/`*_on` drew ISO dates, `role` drew " +
			"{admin, member, viewer, editor, owner}, and `name`/`full_name` drew from a company " +
			"pool or — via detectPersonish, which read the SIBLING columns — a person pool. " +
			"Alongside them lived a dozen sample* vocabularies (names, titles, descriptions, " +
			"addresses, cities, states, countries, statuses, roles, types, locales, timezones) and " +
			"the placeholderPalette that fed the color and image branches.\n" +
			"All of it is gone. What noun belongs in a column is a DECISION — it is not in the " +
			"schema, forge cannot derive it, and a guess is right for the vocabulary it was " +
			"written against and silently wrong everywhere else: the heuristics satisfied an " +
			"email-format CHECK on a column spelled `email` and violated the identical CHECK on " +
			"one spelled `contact`, which aborts the whole transactional seed. The declaration " +
			"surface is db/seeds/vocab.yaml. What replaced them derives from what the author " +
			"DECLARED — the canonical type, the CHECK vocabulary, a regex CHECK (seedplan." +
			"SynthString builds a value from the pattern itself), length and range bounds, NOT " +
			"NULL, UNIQUE, the foreign keys — and an undescribed column gets the emitter's " +
			"self-labelling placeholder, seedplan.SyntheticStringPrefix + column + row.",
		Patterns: []*regexp.Regexp{
			// The dispatchers. Case-SENSITIVE Go identifiers; `isDateColumn`
			// and friends have no live homonym in the tree.
			regexp.MustCompile(`\bisCurrencyColumn\b|\bisColorColumn\b|\bisBirthDateColumn\b|\bisDateColumn\b`),
			regexp.MustCompile(`\bdetectPersonish\b|\bpersonFullName\b|\bpersonish\b`),
			regexp.MustCompile(`\bstringValue\b|\bintegerLiteral\b|\bSynthStringValue\b`),
			regexp.MustCompile(`\bisoDate\b|\bplaceholderImageURI\b|\bsvgURLEscape\b|\bplaceholderPalette\b`),
			// The vocabularies themselves. Case-SENSITIVE and \b-anchored, so
			// the lowercase `sample` never reaches a live camelCase-embedded
			// identifier.
			regexp.MustCompile(`\bsample(?:Names|FirstNames|LastNames|Titles|Descriptions|Addresses|Cities|States|Countries|Statuses|Roles|Types|Locales|Timezones)\b`),
			// SCOPE — read before widening. This entry forbids the SEEDER's
			// column-NAME dispatch and the pools it fed. Two things it
			// deliberately does not police:
			//
			//  1. The column names themselves. `email`, `price_cents`,
			//     `deleted_at` and the rest are ordinary schema vocabulary that
			//     every fixture, migration and skill legitimately spells.
			//  2. `SyntheticStringPrefix` and the `sample_` value it holds —
			//     that is the LIVE stamp the replacement puts on what it
			//     invents, spelled `sample_` on purpose.
			//
			// The frontend mock generator's second copy of the same heuristics
			// has its own entry below — it was a separate decision, made
			// separately.
		},
		Allowances: []allowance{
			{
				Name: "the frontend-config projector's own stringValue helper",
				Reason: "internal/cli/generate_frontend_config.go has an unrelated four-line " +
					"`stringValue(v any) (string, bool)` — a type assertion that reads a KCL-projected " +
					"config value as a string, reporting a non-string as \"not a string here\" rather " +
					"than coercing it. It reads OIDC issuer/client-id out of a rendered config map; " +
					"it draws nothing, knows no column names, and predates none of the seeder's " +
					"heuristics.\n" +
					"The generic lowercase spelling in the pattern above is what collides: no " +
					"`stringValue` — nor `integerLiteral`, nor `SynthStringValue` — appears anywhere " +
					"in pkg/seedplan's history; seedplan's real names are SynthString and " +
					"SyntheticStringPrefix. Scoped to this one file, so a genuine seeder heuristic " +
					"landing here (isCurrencyColumn, detectPersonish, samplesNames …) still fails.",
				Token: regexp.MustCompile(`\bstringValue\b`),
				Paths: []string{"internal/cli/generate_frontend_config.go"},
			},
			{
				Name: "seedplan's DECLARED-type date check",
				Reason: "pkg/seedplan/synth.go has an `isDateColumn(col)` that reads col.DeclType and " +
					"reports whether the column was DECLARED `DATE` — the one time type with no " +
					"time-of-day to render. keyTimeLiteral uses it to format a TIME key member " +
					"date-only rather than as a full instant.\n" +
					"It is the exact inverse of the removed heuristic, which is why the name " +
					"collides: the heuristic guessed a column's MEANING from what it was CALLED " +
					"(`date`/`*_date`/`*_on` drew an ISO date). This reads what the author " +
					"DECLARED in the schema, which is precisely the replacement's rule. Scoped to " +
					"the one file and the one identifier, so isCurrencyColumn, isColorColumn and " +
					"isBirthDateColumn still fail anywhere, and a name-sniffing isDateColumn " +
					"landing in any other file still fails too.",
				Token: regexp.MustCompile(`\bisDateColumn\b`),
				// vocabscalar.go calls it to pick a relative-time range's default step
				// (a day for a column DECLARED DATE, a minute otherwise) — the same
				// declared-type read, in the one other file that needs it.
				Paths: []string{"pkg/seedplan/synth.go", "pkg/seedplan/vocabscalar.go"},
			},
		},
	},
	{
		Name: "the hand-written unauthenticated allow-list",
		Why: "The scaffolded pkg/middleware carried a hand-maintained map of procedure strings that " +
			"the auth interceptor read, alongside `(forge.v1.method).auth_required` in the proto — " +
			"two declaration surfaces for one fact. They could disagree, and only the map did " +
			"anything: an RPC could declare auth_required: true, be reported as authenticated by " +
			"`forge project graph` and the MCP manifest, and still serve anonymous callers. One " +
			"measured app shipped 17 of 20 CRUD RPCs open that way.\n" +
			"The proto is the declaration now. forge projects `auth_required: false` into the " +
			"Tier-1 pkg/middleware/procedures_gen.go as connect's own …Procedure constants, and " +
			"the scaffolded serve wiring runs the interceptor FAIL-CLOSED (AnonymousOK: false) " +
			"against it. Publishing an endpoint is one edit, on the rpc that declares it.",
		Patterns: []*regexp.Regexp{
			// The removed identifier. Case-SENSITIVE and \\b-anchored: the LIVE
			// generated symbol is the exported UnauthenticatedProcedures, and
			// `Unauthenticated` (the authn.Policy field) is untouched.
			regexp.MustCompile(`\bunauthenticatedProcedures\b`),
			// The non-gating posture the scaffold used to ship. A project may
			// still set it deliberately in its own tree; what may not come
			// back is forge GENERATING it — the serve template is the only
			// place in this repo that writes the literal, and
			// internal/templates/auth_test.go pins it to false.
			regexp.MustCompile(`AuthDeps\{AnonymousOK: true\}`),
		},
	},
	{
		Name: "the frontend mock generator's own demo vocabulary",
		Why: "internal/codegen/frontend_mocks.go carried a SECOND copy of the column-name " +
			"heuristics the seeder shed: pools for name/first_name/last_name/title/description/" +
			"status/role/type, integer ranges for age/price/quantity, float ranges keyed on the " +
			"substrings probability/ratio/percent, and a foreign-key target guessed by " +
			"pluralizing an `_id` stem with an \"s\".\n" +
			"It shipped one application with TWO demo vocabularies — the database said " +
			"`sample_name_1` where the frontend said \"Acme Corp\" — and, reading no constraints " +
			"at all, it mocked a `sku` column whose CHECK is `^[A-Z]{3}-[0-9]{4}$` as " +
			"`sample_sku_3`: data the very API the mock stands in for would reject. The pluralize " +
			"guess named tables that do not exist (`categorys`), so mock foreign keys referenced " +
			"ids no fixture carried.\n" +
			"There is now ONE dataset. codegen.SeedProjection resolves the project's own " +
			"seedplan.Plan and the fixtures carry what that plan writes at each (table, column, " +
			"row) — vocabulary from db/seeds/vocab.yaml, values from the CHECK constraints, keys " +
			"and references from the real foreign keys — and a column nothing describes gets the " +
			"same self-labelling placeholder in both places.",
		Patterns: []*regexp.Regexp{
			// The pools. Case-SENSITIVE and anchored on the `mock` prefix, so
			// the seeder entry's own `sample*` patterns and the live
			// `sample_` stamp are untouched. `[A-Z]` catches any pool name,
			// including one added later.
			regexp.MustCompile(`\bmockSample[A-Z]`),
			// SCOPE — read before widening. This entry forbids the mock
			// generator's own VOCABULARY. It deliberately does not police
			// `mockGenerateStringValue` / `mockGenerateIntegerValue`, which
			// are live: they are what a cell falls back to when the project
			// has no dataset to agree with, and they now emit only the
			// synthetic placeholder and the row number.
		},
	},
	{
		Name: "the `//forge:adapter` marker spelling",
		Why: "The marker was RENAMED to `//forge:outbound-io`, named for what it asserts — this " +
			"package calls OUT to a third-party system and serves nothing inbound — so knowing " +
			"when to stamp it requires reading the package, not learning a taxonomy. Nothing " +
			"parses `forge:adapter` any more, so a package still carrying it silently loses the " +
			"outbound-io-no-rpc lint and the observe heuristic's I/O signal: it does not fail, " +
			"it stops checking. The rule id `forgeconv-adapter-no-rpc` moved with it.",
		Patterns: []*regexp.Regexp{
			// SCOPE — read before widening. This entry forbids the MARKER
			// spelling and the retired rule id and Go API. It deliberately does
			// NOT police the word "adapter", which forge keeps everywhere ON
			// PURPOSE: `forge scaffold package --type adapter`, the `adapter`
			// skill, internal/templates/internal-package/adapter/, and the
			// prose calling a package an outbound adapter. "Adapter" is the
			// design pattern; outbound-io is the invariant a linter can check,
			// and only the second one was renamed. The `forge:` prefix is the
			// whole separation — see the adapter entries in
			// TestLegitimateLookalikesAreStillPresent, which fail if a widened
			// pattern ever swallows the live verb or skill.
			regexp.MustCompile(`forge:adapter\b`),
			regexp.MustCompile(`forgeconv-adapter-no-rpc`),
			// The deleted Go API. Case-SENSITIVE, \b-anchored identifiers; the
			// live successors are spelled HasOutboundIODirective /
			// InternalPackageOutboundIO / RuleOutboundIONoRPC and share no
			// substring with these.
			regexp.MustCompile(`\bHasAdapterDirective\b|\bInternalPackageAdapter\b|\bRuleAdapterNoRPC\b`),
			regexp.MustCompile(`\blintAdapterNoRPC\b|\blintAdapterPkg\b|\bhasAdapterMarker\b`),
		},
	},
	{
		Name: "the `forge env promote` verb",
		Why: "`forge env promote <version> --to <env>` was absorbed into " +
			"`forge env deploy <env> [vX | --from <src-env>]` (docs/adr/env-verbs.md, task V3) and " +
			"DELETED — pre-1.0, no alias and no hidden name. Recording a binding ships nothing, so a " +
			"pipeline step that only promoted reported success before any byte had moved and the " +
			"release's real failure surfaced minutes later with nothing connecting the two. Every " +
			"pipeline therefore spelled it `promote --deploy --wait`; the spellings that omitted " +
			"either half were bugs waiting for an incident. So `deploy` means record + apply + wait, " +
			"the health gate is ON by default (--no-wait opts out), and `promote --wait` / " +
			"`--deploy` / `--to` are gone with the verb. A surviving `forge env promote` is a " +
			"copy-pasteable command — in a doc, a skill, a KCL comment or a scaffolded CI step — " +
			"that now dies on \"unknown command\"; worse, a surviving `--wait`/`--deploy` reads as " +
			"if waiting and applying were still opt-in.",
		Patterns: []*regexp.Regexp{
			// The deleted verb, on any surface. Requiring `env`
			// IMMEDIATELY before it is what leaves the live English verb
			// ("forge promotes good practice", "cut and promoted like any
			// other") alone; the root `forge promote` spelling is already
			// policed by "the root spellings of the `forge env` verbs".
			regexp.MustCompile(`\bforge\s+env\s+promote\b`),
			// The Go ARGV form: exec/test invocations pass the command as
			// separate string args, so the tokens are never adjacent in
			// the source and the pattern above cannot see them.
			regexp.MustCompile(`"env",\s*"promote"`),
			// The command constructor and its file, so a revert that
			// restores the Go surface without the doc surface is caught
			// too.
			regexp.MustCompile(`\bnewPromoteCmd\b`),
			// The follow-through flags that became the default. `--to` is
			// NOT policed: it is a live flag elsewhere (e.g. a range end),
			// and the verb patterns above already catch every spelling
			// that carried it.
			regexp.MustCompile(`\bpromote\s+--(?:wait|deploy)\b`),
		},
		Allowances: []allowance{
			{
				Name: "the changelog entry announcing the removal",
				Reason: "A Keep-a-Changelog `### Removed` entry has to name what was removed, or " +
					"readers cannot tell which of their invocations broke. The older entries that " +
					"describe `forge env promote`'s own past behaviour (always-CAS, the run flags) " +
					"are history of a verb that existed at the time and must stay readable.",
				Token: regexp.MustCompile("`forge env promote`|`forge env promote --rollback`|" +
					"`forge env promote --run-id / --run-url / --no-run`|" +
					"`forge env promote --wait`|forge env promote|" +
					// The removed follow-through flags: the entry has to
					// name them to say they were absorbed rather than
					// renamed, which is the question a reader of a broken
					// pipeline actually has.
					"promote --deploy --wait|`promote --wait`|`promote --deploy`"),
				Paths: []string{"CHANGELOG.md"},
			},
			{
				Name: "the test that proves the verb no longer resolves",
				Reason: "TestEnvCmd_HasNoPromoteVerb walks `forge env`'s subcommands asserting none " +
					"is named (or aliased) promote, and the tests beside it record which spelling " +
					"moved where. A test that what it checks is absent must name it. Scoped by " +
					"path and token.",
				Token: regexp.MustCompile("`forge env promote`|forge env promote"),
				Paths: []string{"internal/cli/deploy_promote_test.go"},
			},
			{
				Name: "the absorbed code saying which spelling it used to be reached by",
				Reason: "deploy_promote_follow.go and its test are the MOVED machinery, and their " +
					"header comments say so: \"was promote_wait.go, where the same machinery was " +
					"reached by `promote --wait` / `--deploy`\". That sentence is why waiting is " +
					"now the default — a reader who finds an unconditional wait and no record of " +
					"the flag it replaced cannot tell deliberate from accidental. " +
					"TestDeployCmd_DeclaresEveryReleaseFlag names the pair in order to assert both " +
					"flags are ABSENT. Scoped to the flag spellings, so a line in these files that " +
					"re-registered either flag still fails.",
				Token: regexp.MustCompile(`promote\s+--(?:wait|deploy)`),
				Paths: []string{
					"internal/cli/deploy_promote_follow.go",
					"internal/cli/deploy_promote_follow_test.go",
					"internal/cli/deploy_promote_test.go",
				},
			},
			{
				Name: "the rollback entry's own prose, which names the retired flag pair",
				Reason: "The `rollback` removal above says `forge env deploy --rollback` and " +
					"`forge env promote --rollback` are both gone — it was written while promote " +
					"existed, and its Why/allowances are the record of THAT removal. Rewriting it " +
					"to drop the promote half would make it read as though only deploy ever had " +
					"the flag. Scoped to the flag pairing, so a line here that revived the verb " +
					"still fails.",
				Token:   regexp.MustCompile("`forge env promote --rollback`|forge env promote --rollback|backwards `forge env promote`"),
				Context: regexp.MustCompile(`rollback`),
				Paths:   []string{"internal/removalguard/removalguard_test.go"},
			},
		},
	},
	{
		Name: "rollback — recovery is roll forward",
		Why: "forge has no rollback. `forge env deploy --rollback`, `forge env promote --rollback`, " +
			"`forge.External.rollback_cmd`, the Provider.Rollback verb (kubectl rollout undo, compose " +
			"override pinning, static-site re-sync, the hosted DeployService/Rollback call) and the " +
			"deploy's skip-the-migrate-Job-on-a-rollback path are all gone. A rollback claims to undo a " +
			"release it cannot undo: the release already ran its migrations and wrote rows in the new " +
			"shape. Recovery is a new release that rolls forward. A backwards `forge env promote` is " +
			"still an ordinary promote, labelled `direction BEHIND`. Ledgers keep reading legacy " +
			"`\"kind\":\"rollback\"` entries as promotes (release.legacyKindRollback, " +
			"wireKindLegacyRollback) — that decode path is the one sanctioned mention.",
		Patterns: []*regexp.Regexp{
			// The removed flags, on any surface, in the typed and the ARGV form.
			regexp.MustCompile(`--rollback\b`),
			regexp.MustCompile(`"--rollback"`),
			// The removed KCL field and its Go projection.
			regexp.MustCompile(`\brollback_cmd\b`),
			regexp.MustCompile(`\bRollbackCmd\b`),
			// The removed provider verb and its dispatcher.
			regexp.MustCompile(`\) Rollback\(ctx context\.Context, group ServiceGroup`),
			regexp.MustCompile(`\brollbackDeployGroups\b|\brunDeployRollback\b`),
			// The hosted RPC, the writable kind, and the migrate-skip seam.
			regexp.MustCompile(`DeployService/Rollback\b`),
			regexp.MustCompile(`\bKindRollback\b|\bPromotionRollback\b|\bOnSkippedJobs\b|\bskipPreRolloutForRollback\b`),
			regexp.MustCompile(`\brollout\s+undo\b`),
		},
		Allowances: []allowance{
			{
				Name: "the tests that prove the flags are rejected",
				Reason: "These assert `forge env deploy --rollback` and `forge env promote --rollback` are " +
					"unknown flags, and that no call reaches the retired Rollback RPC. They must name what " +
					"they test is absent. Scoped by path and token.",
				Token: regexp.MustCompile(`"--rollback"|--rollback\b|DeployService/Rollback\b`),
				Paths: []string{
					"internal/cli/deploy_dispatch_test.go",
					"internal/cli/promote_plan_test.go",
					"internal/cli/hosted_ledger_test.go",
				},
			},
			{
				Name: "prose stating what forge does not do",
				Reason: "The deploy skill and the External provider say, in so many words, that there is " +
					"no rollback_cmd and no rollout-undo path — that sentence is the removal, and deleting " +
					"it would delete the explanation. Scoped to the negating phrasings.",
				Token: regexp.MustCompile("no `--rollback`,\\s*|no `rollback_cmd`|no `kubectl rollout undo` path|There is no rollback_cmd|There is no `rollback_cmd`"),
				Paths: []string{
					"internal/templates/project/skills/forge/deploy/SKILL.md",
					"internal/deploytarget/external.go",
					".claude/skills/",
				},
			},
			{
				Name: "the changelog entry announcing the removal",
				Reason: "A Keep-a-Changelog `### Removed` entry has to name what was removed, or readers " +
					"cannot tell which of their invocations broke.",
				Token: regexp.MustCompile("`forge env (?:deploy|promote) --rollback`|`?forge\\.External\\.rollback_cmd`?|`rollback_cmd`|`kubectl rollout undo`|`DeployService/Rollback`"),
				Paths: []string{"CHANGELOG.md"},
			},
			{
				Name: "Helm's own hook vocabulary",
				Reason: "`post-rollback` is a Helm hook name forge must recognise to strip hooks from a " +
					"rendered chart. It is Helm's API, not a forge rollback. None of the patterns above " +
					"match it today; recorded so nobody widens a pattern into it.",
				Token: regexp.MustCompile(`post-rollback`),
				Paths: []string{"internal/cluster/helm.go"},
			},
		},
	},
	{
		Name: "the generated per-API MCP manifest and its bridge",
		Why: "forge generated gen/mcp/manifest.json — one Model Context Protocol tool per " +
			"Connect RPC — plus a stdio bridge (internal/mcpbridge) and two hosts for it " +
			"(`forge mcp serve` and the cmd/forge-mcp binary) whose only job was serving that " +
			"manifest. An agent can drive the project's own CLI and `forge api curl` directly, " +
			"so the manifest bought a second, generated description of the API that had to be " +
			"kept true to the protos forever. The RPC inventory an agent actually needs is " +
			"still in `forge project audit --json` (shape.services[].rpcs, with streaming " +
			"mode) and `forge project map`/`graph`.\n" +
			"This removal does NOT touch MCP as a CLIENT: .mcp.json / .mcp.json.example and " +
			"their templates configure chrome-devtools and reliant-docs MCP servers that " +
			"agents consume, and those stay.",
		Patterns: []*regexp.Regexp{
			// SCOPE — read before widening. This entry forbids the generated
			// MANIFEST, the bridge package, and the hosts. It deliberately does
			// NOT police the bare word "MCP", which forge keeps ON PURPOSE for
			// the chrome-devtools / reliant-docs client config, the `MCP` Go
			// initialism in internal/naming, and internal packages a user may
			// legitimately name `mcp/...`.
			//
			// The manifest path. Slash-separated, so a project's own
			// internal/mcp/database package cannot reach it.
			regexp.MustCompile(`gen/mcp/manifest\.json`),
			// The emitter's Go API and its call site in the pipeline.
			regexp.MustCompile(`\bGenerateMCPManifest\b|\bMCPGenInput\b|\bstepMCPManifest\b`),
			// The bridge package and the standalone binary. \b-anchored:
			// "forge-mcp" and "mcpbridge" share no substring with the live
			// chrome-devtools config.
			regexp.MustCompile(`\bmcpbridge\b|\bforge-mcp\b`),
			// The CLI host: the `forge mcp` command and its token env var.
			// The space is what keeps this off ".mcp.json" and "internal/mcp/".
			regexp.MustCompile(`\bforge mcp\b|\bFORGE_MCP_TOKEN\b|\bnewMCPCmd\b|\bnewMCPServeCmd\b`),
			// The audit field that existed only to tell an agent what the
			// bridge could dispatch. Streaming mode survives it.
			regexp.MustCompile(`\bmcp_callable\b|\bMCPCallable\b`),
		},
	},
	{
		Name: "passing an image registry to forge instead of declaring it",
		Why: "An image registry is DECLARED in the env's KCL (forge.ClusterTarget.registry, or " +
			"forge.ControlPlane.registry for a hosted env, in deploy/kcl/<env>/main.k) and nowhere " +
			"else. `forge build --push` became a boolean that pushes to that declaration; the " +
			"`--push <registry>` / `--push=<registry>` value forms are gone, as is the " +
			"forge.registry(default) KCL helper (an `option(\"registry\")` forge owned so " +
			"`-D registry=` could outrank the declaration). `forge generate` rewrites " +
			"forge.registry(\"X\") to \"X\". A project that reads option(\"registry\") in its OWN " +
			"KCL has written an ordinary project option — forge neither owns nor blocks it — so " +
			"that spelling is not policed here. What is policed is forge TELLING anyone to pass a " +
			"registry, or calling the helper that no longer exists.",
		Patterns: []*regexp.Regexp{
			// The retired KCL helper. `\(` keeps prose about "the forge.registry
			// helper" out; a call is what fails to render.
			regexp.MustCompile(`\bforge\.registry\(`),
			// --push carrying a value: `--push=ghcr.io/x`, `--push "$REGISTRY"`,
			// `--push <registry>`, `--push localhost:5050`. A value is a word
			// starting with a quote, `$`, `<`, or containing a registry's `.`
			// / `:`. A following flag (`--push --plan`) is not a value.
			regexp.MustCompile(`--push=\S`),
			regexp.MustCompile(`--push\s+(?:["'$<]|[A-Za-z0-9-]+[.:/])`),
			// forge.yaml's registry keys and the Go that read them. The
			// dotted key path is how docs and messages name them; the loader
			// refuses them (config.refusedSchemaKeys) with the runbook.
			regexp.MustCompile(`\b(?:docker|deploy)\.registry\b`),
			regexp.MustCompile(`\b(?:Docker|Deploy)\.Registry\b|\bEffectiveRegistry\b`),
		},
		Allowances: []allowance{
			{
				Name: "the migration that removes the helper, and its tests",
				Reason: "MigrateRegistryHelper / CheckRegistryHelper exist to find and rewrite " +
					"forge.registry( calls in existing projects, so they must spell the call.",
				Paths: []string{
					"internal/kclvendor/registry_helper.go",
					"internal/kclvendor/registry_helper_test.go",
					"internal/cli/kcl_vendor.go",
					"internal/cli/kcl_vendor_test.go",
				},
			},
			{
				Name: "env new recognising a template env's retired helper call",
				Reason: "`forge env new` derives from an existing env that may predate the " +
					"migration; its registry regexp names the call so it can neutralise it.",
				Token: regexp.MustCompile(`forge\.registry\("…"\)|forge\.registry\("ghcr\.io/acme"\)|forge\.registry\(\\"ghcr\.io/acme\\"\)`),
				Paths: []string{"internal/cli/new_env.go", "internal/cli/new_env_test.go"},
			},
			{
				Name: "the loader's refusal of the retired forge.yaml keys, and its tests",
				Reason: "refusedSchemaKeys must name the keys it refuses, and its tests " +
					"must write them to prove each one fails the load.",
				Paths: []string{"internal/config/validate.go", "internal/config/validate_test.go"},
			},
			{
				Name:   "the tests that pin --push refusing a value",
				Reason: "They must write the refused spellings to prove each one fails.",
				Paths:  []string{"internal/cli/build_push_registry_test.go"},
			},
		},
	},
	{
		Name: "the image_on_registry string helper",
		Why: "`forge.image_on_registry(image, host)` (and the lib/images.k `on_registry` " +
			"behind it) swapped the registry host on an image reference so one env could " +
			"re-point another env's declaration. It was string manipulation KCL already " +
			"does, dressed up as forge API: a helper forge had to keep, document and teach " +
			"forever in order to save an author one interpolation.\n" +
			"The model now is the plain one: an image is a literal string on each workload, " +
			"and an env that needs a different image sets `image` on that workload in its " +
			"own KCL. A project that wants one source of truth writes its own constant — " +
			"forge prescribes no pattern for it and ships no helper.",
		Patterns: []*regexp.Regexp{
			// The forge-namespaced export and the lib binding behind it.
			// `\b`-anchored on both sides so a project's own identifier that
			// merely ends in these words is not policed.
			regexp.MustCompile(`\bimage_on_registry\b`),
			regexp.MustCompile(`\bimg_lib\.on_registry\b|(?m)^on_registry\s*=`),
		},
	},
	{
		Name: "the five absorbed `forge env` read verbs",
		Why: "`forge env verify`, `forge env wait`, `forge env rollout`, `forge env topology` and " +
			"`forge env history` were six views of one question (with `env status`), split by which " +
			"half of the answer each happened to own — a reader had to know, before they could ask, " +
			"that the bound release lived in `verify`, the rollout phase in `rollout`, the runtime " +
			"ports in `status` and the promotion that caused all of it in `history`. Two were " +
			"literal duplicates: `env rollout` WAS `env wait --timeout 0`.\n" +
			"They are modes of `forge env status [environment...]` now: `--wait` blocks on the " +
			"rollout (exit codes 0/1/2/5/6 unchanged), `--wait --timeout 0` is the old rollout " +
			"snapshot, `--history` pages the promotion ledger, no environment is the old topology, " +
			"and the default one-env view carries the release half verify owned. Pre-1.0, so the " +
			"five spellings are DELETED, with no alias and no hidden name.\n" +
			"A surviving `forge env verify prod` is a copy-pasteable command — in a doc, a skill, a " +
			"KCL comment, a scaffolded CI job or a help string — that now dies on \"unknown command\". " +
			"The internal helpers stay (runEnvWait, runEnvHistory, runEnvTopology, " +
			"runEnvStatusRelease); only the command surface went.",
		Patterns: []*regexp.Regexp{
			// The removed spellings, on any surface. Requiring `forge env`
			// IMMEDIATELY before the verb is what keeps the live siblings
			// out: `forge release verify` (a RELEASE's artifacts, not an
			// env — explicitly kept by the ADR), `forge release where`,
			// `forge ci run`, and `kubectl rollout status`. The trailing
			// \b keeps `forge env verifying` and the English "history" out.
			regexp.MustCompile(`\bforge\s+env\s+(?:verify|wait|rollout|topology|history)\b`),
			// The Go ARGV form: exec/test invocations pass the command as
			// separate string args, so the tokens are never adjacent in
			// the source and the pattern above cannot see them. Anchored
			// on the "env" element so `runForge(t, "release", "verify")`
			// and a bare `"wait"` kubectl arg are untouched.
			regexp.MustCompile(`"env",\s*"(?:verify|wait|rollout|topology|history)"`),
			// The constructors. A command that is still BUILT but no
			// longer registered is worse than one that is registered: it
			// compiles, it is covered by no test, and the next person to
			// read env.go sees a verb that looks merely forgotten.
			regexp.MustCompile(`\bnewEnv(?:Verify|Wait|Rollout|Topology|History)Cmd\b`),
		},
		Allowances: []allowance{
			{
				Name: "the CHANGELOG's record of the releases that SHIPPED these verbs",
				Reason: "The CHANGELOG is an append-only history of what each release contained, and " +
					"these verbs genuinely shipped under these names. Rewriting those entries to the " +
					"new spelling would make the file assert that a past release shipped a command it " +
					"did not, which is the one thing a changelog must never do — and it would erase " +
					"the only record a reader has of why their pinned older forge has a verb this one " +
					"does not.\n" +
					"The removal itself gets its own entry, in the new release's section, naming the " +
					"replacement. Scoped to the one file.",
				Token: regexp.MustCompile(`\bforge\s+env\s+(?:verify|wait|rollout|topology|history)\b`),
				Paths: []string{"CHANGELOG.md"},
			},
			{
				Name: "the test that proves the merge happened",
				Reason: "env_status_cmd_test.go names the six verbs twice: once to assert each no " +
					"longer RESOLVES under `forge env`, and once to assert the merged help does not " +
					"still point at them. That is documentation OF the removal, and the check most " +
					"likely to stop someone reintroducing a verb. Deleting it to satisfy the guard " +
					"would delete the proof the removal is complete. Scoped to the one file, so a " +
					"line in it that actually registered one of these commands still fails — the " +
					"constructor pattern is not allowed here.",
				Token: regexp.MustCompile(`\bforge\s+env\s+(?:verify|wait|rollout|topology|history)\b`),
				Paths: []string{"internal/cli/env_status_cmd_test.go"},
			},
		},
	},
}

// packOnDisk implements the "a referenced pack must exist" rule for the packs
// removal. name is empty for an unnamed reference ("a pack"), which resolves
// against the pack root itself.
func packOnDisk(root, name string) (bool, []string) {
	looked := filepath.Join(root, "internal", "packs")
	if name != "" {
		looked = filepath.Join(looked, name)
	}
	fi, err := os.Stat(looked)
	rel, _ := filepath.Rel(root, looked)
	return err == nil && fi.IsDir(), []string{filepath.ToSlash(rel)}
}

// kubernetesNoun matches vocabulary that only appears when the subject is
// Kubernetes itself. It scopes the bare-"RBAC" allowance: an application-level
// RBAC claim never shares a line with a kubeconfig, a ClusterRole or a KCL
// deploy path.
var kubernetesNoun = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`\bk8s\b`, `kubernetes`, `kubectl`, `kubeconfig`, `kubebuilder`,
	`clusterrole`, `rolebinding`, `serviceaccount`, `\bcrd\b`,
	`cluster[- _]scoped`, `cluster[- _]rbac`, `namespaced`,
	`\bkcl\b`, `\bmanifests?\b`, `\bdeployments?\b`, `\bhelm\b`,
}, `|`))

// commonAllowances apply to every removal in the table.
var commonAllowances = []allowance{
	{
		Name:   "the guard's own source",
		Reason: "This package necessarily spells out every forbidden pattern; matching itself would make the guard permanently red.",
		Paths:  []string{"internal/removalguard/"},
	},
}

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// removal is one feature that has been deleted from forge.
type removal struct {
	// Name identifies the removal in failure output.
	Name string
	// Why records what was removed and what replaced it, so a future reader
	// who trips the guard can tell a straggler from a legitimate look-alike.
	Why string
	// Patterns are the spellings that constitute a reference to the feature.
	Patterns []*regexp.Regexp
	// Allowances excuse legitimate look-alikes. See allowance.
	Allowances []allowance

	// Resolve, when set, makes the Patterns CONDITIONAL: a match is a
	// straggler only when the artifact it names is missing from disk. Submatch
	// 1 of the matching pattern, when the pattern has one, is the name; it is
	// empty for an unnamed reference. It returns whether the artifact exists
	// and where it looked, which goes into the failure message.
	//
	// This is how to express "a referenced X must exist" instead of a literal
	// list of dead X names: adding or deleting an X later needs no edit here,
	// and a FUTURE deletion is caught the moment it lands.
	Resolve func(root, name string) (exists bool, searched []string)
}

// allowance excuses text that a removal's Patterns match but that is not a
// reference to the removed feature.
type allowance struct {
	// Name and Reason appear in nothing but the source; they exist so the
	// next reader can judge whether the carve-out is still earned.
	Name   string
	Reason string

	// Token scopes the allowance to the exact substrings it matches: a finding
	// is excused only when its matched span sits INSIDE a Token match on the
	// same line. A nil Token excuses the whole file and REQUIRES Paths.
	Token *regexp.Regexp

	// Context, when set, limits the allowance to lines that also match it. It
	// is how an ambiguous word is disambiguated by its neighbours rather than
	// by blanket-excusing the word.
	Context *regexp.Regexp

	// Paths limits the allowance to repo-relative, slash-separated paths. An
	// entry ending in "/" covers that directory and everything under it; a
	// leading "**/" matches at any depth; otherwise it is matched with
	// path.Match. Empty means every file.
	Paths []string
}

type finding struct {
	feature string
	path    string
	line    int
	pattern string
	text    string
	snippet string
	// note carries a Resolve verdict ("no such pack; looked in …").
	note string
}

// ─────────────────────────────────────────────────────────────────────────────
// Scan surface
// ─────────────────────────────────────────────────────────────────────────────

// skipDirs are never scanned. Everything excluded here is either not source
// (build output, dependency trees, per-developer runtime state) or would drown
// the guard in generated noise. Note what is NOT here: skills, docs, kcl,
// proto, internal/templates and dotfiles are all scanned, because that is
// exactly where the misses happened.
//
// These apply on top of git's file list (scannedFiles), which already drops
// every gitignored entry. They still matter for TRACKED files: .forge/ holds
// forge's committed state files, which are machine-written, not a surface.
var skipDirs = map[string]bool{
	".git":         true, // VCS internals
	"node_modules": true, // npm dependency tree (vendored third-party code)
	"vendor":       true, // Go vendor tree (vendored third-party code)
	"dist":         true, // frontend build output
	"bin":          true, // compiled binaries
	".next":        true, // Next.js build cache
	".turbo":       true, // turborepo cache
	".vercel":      true, // Vercel build state
	"coverage":     true, // coverage report output
	"tmp":          true, // scratch output
	".forge":       true, // per-developer forge runtime state (gitignored)
	".scratch":     true, // per-agent working notes (gitignored, never shipped)
	".kilo":        true, // nested git worktrees (excluded via .git/info/exclude)
}

// skipExts are binary formats. Matching bytes inside them would be noise, and
// reading them wastes the scan.
var skipExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".ico": true,
	".webp": true, ".pdf": true, ".zip": true, ".gz": true, ".tgz": true,
	".tar": true, ".wasm": true, ".woff": true, ".woff2": true, ".ttf": true,
	".otf": true, ".eot": true, ".mp4": true, ".mov": true, ".jar": true,
	".so": true, ".dylib": true, ".dll": true, ".exe": true, ".test": true,
}

// skipFiles are machine-generated dependency manifests: their contents are the
// names and hashes of third-party modules, which forge does not control.
var skipFiles = map[string]bool{
	"go.sum":            true,
	"go.work.sum":       true,
	"package-lock.json": true,
	"pnpm-lock.yaml":    true,
	"yarn.lock":         true,
	"kcl.mod.lock":      true,
}

// skipFilePrefixes are basename prefixes that are never a forge surface.
//
// PR_BODY*.md is an agent-authored PR description, written into the worktree
// while a branch is in flight. It is prose ABOUT a change, and a PR body for a
// REMOVAL names the removed spelling dozens of times by design — that is what
// the description is for. Scanning one makes every mention read as a surviving
// reference, so the guard would fail on exactly the branches that are doing the
// removing properly. (Observed: V1 tracked PR_BODY_V1.md and turned main red.)
//
// The alternative — one allowance per PR body per removal — would mean a
// standing carve-out in the table for text no release ever reads, which is
// precisely the "too-permissive allowance" this file warns against.
var skipFilePrefixes = []string{"PR_BODY"}

// skipScannedFile reports whether a basename is outside forge's surfaces.
func skipScannedFile(name string) bool {
	if skipFiles[name] {
		return true
	}
	for _, p := range skipFilePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// maxFileSize caps a single scanned file. Anything larger is generated data,
// not a surface a human wrote a feature reference into.
const maxFileSize = 4 << 20

// ─────────────────────────────────────────────────────────────────────────────
// The guard
// ─────────────────────────────────────────────────────────────────────────────

func TestRemovedFeaturesLeaveNoReferences(t *testing.T) {
	if testing.Short() {
		t.Skip("scans every file in the forge repository; runs in task test")
	}
	root := repoRoot(t)
	byFeature := survivingReferences(t, root)

	for _, rm := range removals {
		hits := byFeature[rm.Name]
		if len(hits) == 0 {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d surviving reference(s) to the removed %q feature.\n", len(hits), rm.Name)
		fmt.Fprintf(&b, "\n  %s\n", rm.Why)
		b.WriteString("\nSurviving references:\n")
		for _, h := range hits {
			fmt.Fprintf(&b, "  %s:%d: matched %q via pattern `%s`\n", h.path, h.line, h.text, h.pattern)
			if h.note != "" {
				fmt.Fprintf(&b, "      %s\n", h.note)
			}
			fmt.Fprintf(&b, "      | %s\n", truncate(h.snippet, 160))
		}
		b.WriteString("\nDelete the reference. If it is a legitimate look-alike and not a\n")
		b.WriteString("straggler, add a narrow allowance (with its reason) to the\n")
		b.WriteString(`"` + rm.Name + `" entry in internal/removalguard/removalguard_test.go.` + "\n")
		b.WriteString("Do NOT widen a pattern to make this green.\n")
		t.Errorf("%s", b.String())
	}
}

// survivingReferences scans every file under root that the guard reads and
// returns the unexcused hits, keyed by removal name, in sorted file order.
func survivingReferences(t *testing.T, root string) map[string][]finding {
	t.Helper()

	// Match each file against every removal on a worker pool.
	//
	// This is the expensive half of the guard — thousands of files times 49
	// removals times their patterns, times every line. Serially it ran ~31s
	// here, and under `-race` (how CI runs the suite: `go test -race -count=1
	// ./...` with no -timeout, so Go's 10-minute default applies) it blew
	// past 600s and PANICKED. A timeout panic fails the whole `Test` job and
	// masks every other package's result behind what reads as a hang, which
	// is how this went unexamined while the job stayed red.
	//
	// Findings are collected per file and merged in sorted file order after
	// the pool drains, so the output is byte-identical to the serial version
	// — the failure message diffs against golden expectations and must not
	// reorder.
	type fileHits struct {
		idx       int
		byFeature map[string][]finding
	}

	var scanned []struct {
		rel     string
		content []byte
	}
	forEachScannedFile(t, root, func(rel string, content []byte) {
		scanned = append(scanned, struct {
			rel     string
			content []byte
		}{rel, content})
	})

	// Precompute each removal's allowance slice once rather than rebuilding
	// it per file — it is identical for every file and the append pair
	// allocated twice per file per removal.
	allowedFor := make([][]allowance, len(removals))
	for ri, rm := range removals {
		allowedFor[ri] = append(append([]allowance{}, commonAllowances...), rm.Allowances...)
	}

	hitsCh := make(chan fileHits, len(scanned))
	forEachIndex(len(scanned), func(idx int) {
		rel, content := scanned[idx].rel, scanned[idx].content
		local := map[string][]finding{}
		lines := strings.Split(string(content), "\n")
		for ri, rm := range removals {
			for i, line := range lines {
				for _, hit := range matchLine(root, line, rm, allowedFor[ri], rel) {
					hit.feature, hit.path, hit.line = rm.Name, rel, i+1
					hit.snippet = strings.TrimSpace(line)
					local[rm.Name] = append(local[rm.Name], hit)
				}
			}
		}
		if len(local) > 0 {
			hitsCh <- fileHits{idx: idx, byFeature: local}
		}
	})
	close(hitsCh)

	perFile := make([]map[string][]finding, len(scanned))
	for fh := range hitsCh {
		perFile[fh.idx] = fh.byFeature
	}

	byFeature := map[string][]finding{}
	for _, m := range perFile {
		for name, hits := range m {
			byFeature[name] = append(byFeature[name], hits...)
		}
	}
	return byFeature
}

// TestGuardScansOnlyWhatGitWouldCommit runs the guard over a scratch
// repository holding the same removed reference in three files. The gitignored
// one must pass: .claude/skills/ is where `forge generate` renders every skill,
// and the guard used to fail there after any local generate. The tracked one
// and the untracked-but-not-ignored one must both still fail. Skipping
// everything outside the index would hide a straggler until its first commit.
func TestGuardScansOnlyWhatGitWouldCommit(t *testing.T) {
	// A developer's global excludes or config must not decide what this
	// test's repository ignores.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// The removed tenancy annotation (#94), as a proto field option.
	const ref = "string org_id = 2 [(forge.v1.field) = { tenant: true }];\n"
	const (
		ignored   = ".claude/skills/deploy-flux/SKILL.md"
		tracked   = "docs/tracked.md"
		untracked = "docs/untracked.md"
	)
	git("init", "-q")
	write(".gitignore", ".claude/\n")
	write(ignored, ref)
	write(tracked, ref)
	write(untracked, ref)
	git("add", ".gitignore", tracked)

	hit := map[string]bool{}
	for _, hits := range survivingReferences(t, root) {
		for _, h := range hits {
			hit[h.path] = true
		}
	}
	if hit[ignored] {
		t.Errorf("%s is gitignored, yet the guard reported it. Rendered output that git "+
			"will never commit is not a surface forge ships.", ignored)
	}
	for _, rel := range []string{tracked, untracked} {
		if !hit[rel] {
			t.Errorf("%s carries a removed reference and git would commit it, yet the guard "+
				"passed it. The scan has stopped reading files it must read.", rel)
		}
	}
}

// matchLine returns every pattern hit on line that no allowance excuses and,
// when the removal has a Resolve, that names something missing from disk.
func matchLine(root, line string, rm removal, allowances []allowance, rel string) []finding {
	var out []finding
	for _, p := range rm.Patterns {
		for _, m := range p.FindAllStringSubmatchIndex(line, -1) {
			span := m[0:2]
			if excused(line, span, allowances, rel) {
				continue
			}
			hit := finding{pattern: p.String(), text: line[span[0]:span[1]]}
			if rm.Resolve != nil {
				name := ""
				if len(m) >= 4 && m[2] >= 0 {
					name = line[m[2]:m[3]]
				}
				exists, searched := rm.Resolve(root, name)
				if exists {
					continue
				}
				hit.note = fmt.Sprintf("→ names %q, which does not exist (looked in %s)",
					name, strings.Join(searched, ", "))
			}
			out = append(out, hit)
		}
	}
	return out
}

// excused reports whether span on line is covered by an allowance that applies
// to rel. A Token-scoped allowance excuses the span only when the span sits
// entirely inside one of the Token's matches on the same line — so excusing
// `rbac.authorization.k8s.io` never excuses a `RequireRole` sharing the line.
func excused(line string, span []int, allowances []allowance, rel string) bool {
	for _, a := range allowances {
		if len(a.Paths) > 0 && !pathCovered(rel, a.Paths) {
			continue
		}
		if a.Context != nil && !a.Context.MatchString(line) {
			continue
		}
		if a.Token == nil {
			return true // file-wide allowance
		}
		for _, ok := range a.Token.FindAllStringIndex(line, -1) {
			if ok[0] <= span[0] && span[1] <= ok[1] {
				return true
			}
		}
	}
	return false
}

// pathCovered matches rel (repo-relative, slash-separated) against the
// allowance patterns: a trailing "/" is a directory prefix, a leading "**/"
// matches at any depth, anything else goes through path.Match.
func pathCovered(rel string, patterns []string) bool {
	for _, p := range patterns {
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(rel, p) {
				return true
			}
			continue
		}
		if tail, ok := strings.CutPrefix(p, "**/"); ok {
			for _, suffix := range pathSuffixes(rel) {
				if match, _ := path.Match(tail, suffix); match {
					return true
				}
			}
			continue
		}
		if ok, _ := path.Match(p, rel); ok {
			return true
		}
	}
	return false
}

// pathSuffixes yields rel and every sub-path of it starting at a "/" boundary,
// so a "**/"-anchored pattern can match at any depth.
func pathSuffixes(rel string) []string {
	out := []string{rel}
	for i, c := range rel {
		if c == '/' {
			out = append(out, rel[i+1:])
		}
	}
	return out
}

// TestTenancyAzureAllowanceStaysNarrow pins the tenancy entry's Azure carve-out
// from both sides, on synthetic lines. The Azure Trusted Signing lines in the
// desktop-release proposal must pass. Every shape the removed feature shipped
// in must still fail, both in that same file and elsewhere. The tree-wide scan
// cannot prove the second half: main has no straggler for it to catch, so a
// carve-out that swallowed the removed annotation would leave it green.
func TestTenancyAzureAllowanceStaysNarrow(t *testing.T) {
	rm := removalNamed(t, "tenancy")
	allowances := append(append([]allowance{}, commonAllowances...), rm.Allowances...)
	const proposal = "docs/proposals/desktop-release-target.md"

	cases := []struct {
		name    string
		path    string
		line    string
		wantHit bool
	}{
		// Azure's tenant, verbatim from the proposal: excused.
		{"Azure tenant id in the release-secrets list", proposal,
			"  tenant id and client id, the four Trusted Signing", false},
		{"the AzureTrustedSigning schema field", proposal,
			"    tenant_id: str", false},
		{"the AzureTrustedSigning example", proposal,
			`signing = forge.AzureTrustedSigning {endpoint = "<endpoint>", tenant_id = "<tenant>", client_id = "<client>"}`, false},

		// The removed annotation (FieldOptions.tenant, deleted in #94), in
		// the forms it shipped in. Each must fail.
		{"the annotation on a proto field", "proto/services/users/v1/users.proto",
			`string org_id = 2 [(forge.v1.field) = { tenant: true, ref: "orgs.id" }];`, true},
		{"the annotation on a tenant_id field, inside the proposal", proposal,
			`string tenant_id = 2 [(forge.v1.field) = { tenant: true }];`, true},
		{"the option's definition in forge.proto", "proto/forge/v1/forge.proto",
			"  bool tenant = 2;", true},
		{"the annotation taught by a shipped skill", "internal/templates/project/skills/forge/proto/SKILL.md",
			"- Always annotate the tenant column (`tenant: true`).", true},

		// The rest of the removed feature, inside the proposal. The
		// carve-out excuses spans, not the file.
		{"a tenant context key", proposal, "TenantID string", true},
		{"a tenant header", proposal, "X-Tenant-Id: acme", true},
		{"the generated CRUD tenant hook", proposal, "Tenant: middleware.RequireTenantID,", true},

		// Azure's spelling outside the proposal is not excused.
		{"the Azure field in a file the carve-out does not name", "kcl/lib/desktop.k",
			"    tenant_id: str", true},
	}

	root := repoRoot(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits := matchLine(root, tc.line, rm, allowances, tc.path)
			switch {
			case tc.wantHit && len(hits) == 0:
				t.Errorf("%s: %q passed the tenancy guard; it is a reference to the removed feature "+
					"and must fail. An allowance has grown wide enough to excuse it.", tc.path, tc.line)
			case !tc.wantHit && len(hits) > 0:
				t.Errorf("%s: %q failed the tenancy guard (matched %q via `%s`); it is Azure's "+
					"Entra ID tenant, not forge tenancy, and must pass.", tc.path, tc.line, hits[0].text, hits[0].pattern)
			}
		})
	}
}

// removalNamed returns the table entry called name.
func removalNamed(t *testing.T, name string) removal {
	t.Helper()
	for _, rm := range removals {
		if rm.Name == name {
			return rm
		}
	}
	t.Fatalf("no removal named %q in the table", name)
	return removal{}
}

// ─────────────────────────────────────────────────────────────────────────────
// The scan surface
// ─────────────────────────────────────────────────────────────────────────────

// TestScanSurfaceReachesEveryForgeSurface makes "we grepped the Go tree and
// declared victory" structurally impossible. Every removal so far was declared
// done while a non-Go surface survived; if a future skipDirs/skipExts edit
// silently drops one of these, this fails instead of the guard quietly
// scanning less.
func TestScanSurfaceReachesEveryForgeSurface(t *testing.T) {
	surfaces := map[string]struct {
		why   string
		match func(rel string) bool
	}{
		"skills that ship into downstream projects": {
			"a stale skill taught agents an API that does not compile",
			func(r string) bool {
				return strings.HasPrefix(r, "internal/templates/project/skills/") && strings.HasSuffix(r, ".md")
			},
		},
		"template TypeScript": {
			"three frontend templates kept minting a bearer token no backend honors",
			func(r string) bool {
				return strings.HasPrefix(r, "internal/templates/") && strings.HasSuffix(r, ".ts")
			},
		},
		"template text/template sources": {
			"scaffolded Go/YAML lives in .tmpl, invisible to a Go-only grep",
			func(r string) bool { return strings.HasSuffix(r, ".tmpl") },
		},
		"docs": {"docs outlive the code they describe", func(r string) bool {
			return strings.HasPrefix(r, "docs/") && strings.HasSuffix(r, ".md")
		}},
		"kcl": {"KCL examples kept a removed field alive for multiple rounds", func(r string) bool {
			return strings.HasSuffix(r, ".k")
		}},
		"proto": {"proto is the API contract; a dead field there is a shipped dead field", func(r string) bool {
			return strings.HasSuffix(r, ".proto")
		}},
		"dotfiles": {"a .gitignore breadcrumb survived a removal", func(r string) bool {
			return strings.HasPrefix(path.Base(r), ".")
		}},
		"Go": {"the one surface that never gets missed — assert it anyway", func(r string) bool {
			return strings.HasSuffix(r, ".go")
		}},
	}

	counts := map[string]int{}
	total := 0
	forEachScannedFile(t, repoRoot(t), func(rel string, _ []byte) {
		total++
		for name, s := range surfaces {
			if s.match(rel) {
				counts[name]++
			}
		}
	})

	if total < 500 {
		t.Fatalf("the guard scanned only %d files — the repo root or the skip lists are wrong; "+
			"a guard that scans nothing passes everything", total)
	}
	for name, s := range surfaces {
		if counts[name] == 0 {
			t.Errorf("the %s surface is not being scanned (%s)", name, s.why)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// The look-alikes the guard must NEVER kill
// ─────────────────────────────────────────────────────────────────────────────

// TestLegitimateLookalikesAreStillPresent asserts that each thing the guard
// must NOT kill — whether an allowance protects it or a pattern's spelling
// steers around it — is really in the tree. Without this, the carve-outs rot
// into unfalsifiable config: someone broadens a pattern, this test goes red
// naming the exact construct they broke, instead of the guard quietly
// "passing" after the construct was deleted.
func TestLegitimateLookalikesAreStillPresent(t *testing.T) {
	if testing.Short() {
		t.Skip("scans every file in the forge repository; runs in task test")
	}
	// Each entry is a construct forge legitimately keeps. The guard above must
	// stay green while these are present.
	lookalikes := []struct {
		what  string
		why   string
		token *regexp.Regexp
	}{
		{`"Authorization" header`, "authentication — forge still reads Bearer tokens off it", regexp.MustCompile(`"Authorization"`)},
		{"http.StatusUnauthorized", "HTTP 401", regexp.MustCompile(`StatusUnauthorized`)},
		{"network:unauthorized", "frontend 401 event", regexp.MustCompile(`network:unauthorized`)},
		{"rbac.authorization.k8s.io", "Kubernetes ClusterRole/RoleBinding apiVersion", regexp.MustCompile(`rbac\.authorization\.k8s\.io`)},
		{"+kubebuilder:rbac", "controller-gen RBAC marker on generated controllers", regexp.MustCompile(`\+kubebuilder:rbac`)},
		{"fw.Workload.clusterRBAC", "the KCL field granting an operator cluster-scoped API access (kcl/workload.k, generated into kcl/tiers/tiers_gen.k)", regexp.MustCompile(`\bclusterRBAC\b`)},
		{"WorkloadSpec.ClusterRBAC", "the Go field (pkg/deploy/v1alpha1) behind clusterRBAC — what the Go renderer turns into a ClusterRole and ClusterRoleBinding", regexp.MustCompile(`\bClusterRBAC\b`)},
		{"WorkloadSpec.NamespacedRBAC", "the Go field (pkg/deploy/v1alpha1) granting a workload namespaced Kubernetes API access — what the Go renderer turns into a Role and RoleBinding", regexp.MustCompile(`\bNamespacedRBAC\b`)},
		{"v1alpha1.PolicyRule", "the Go mirror of an rbac/v1 PolicyRule that NamespacedRBAC / ClusterRBAC carry", regexp.MustCompile(`\bPolicyRule\b`)},
		{"fw.Workload.namespacedRBAC", "the KCL field granting a workload namespaced API access (kcl/workload.k, generated into kcl/tiers/tiers_gen.k)", regexp.MustCompile(`\bnamespacedRBAC\b`)},
		{"rbacv1", "the k8s.io/api/rbac/v1 import pkg/deploy's renderer builds every Role/ClusterRole/binding with — the Go successor to the deleted KCL rbac lib", regexp.MustCompile(`\brbacv1\b`)},
		{"crud.Pack", "the live response-projection seam in forge/pkg/crud — not the retired pack subsystem", regexp.MustCompile(`\bPack:\s+func\(`)},
		{"the English verb \"packs\"", "\"the read path never packs it\" — prose the packs patterns must not reach", regexp.MustCompile(`never packs `)},
		{"svcerr.PermissionDenied", "Connect wire code an application returns from its own policy check", regexp.MustCompile(`svcerr\.PermissionDenied`)},
		{"connect.CodePermissionDenied", "the Connect status code it maps to", regexp.MustCompile(`CodePermissionDenied`)},
		{"deploy/kcl/workloads.k", "the LIVE, tracked, user-owned workload declaration KCL expands into k8s resources — not the retired root manifest; the extension and the directory are what separate them", regexp.MustCompile(`deploy/kcl/workloads\.k`)},
		{"WorkloadsKCLRelPath", "the const naming that scaffolded file", regexp.MustCompile(`WorkloadsKCLRelPath`)},
		{"WorkloadStanza", "the formatter that writes one workload into it, shared by the scaffold and the drift lint", regexp.MustCompile(`WorkloadStanza`)},
		{"forge.workloads.Port", "the LIVE KCL schema a project declares a real port on — the home a port moved TO, not the Go carrier it moved off", regexp.MustCompile(`\bfw\.Port\b`)},
		{"config.DefaultServePort", "the one port fact forge itself knows: the single mux every service in the binary mounts onto", regexp.MustCompile(`DefaultServePort`)},
		{"HostDeploy.listen_ports", "the host TCP ports a dev-mode service binds — a KCL deploy fact, unrelated to the removed per-component carrier", regexp.MustCompile(`listen_ports`)},
		{"Workload.ports", "the container/Service port list a workload declares (fw.Workload, tiers.Workload, WorkloadSpec.Ports) — the home the cluster deploy block's ports moved TO", regexp.MustCompile(`ports\?: \[(tiers\.)?Port\]|Ports\s+\[\]Port\b`)},
		{"forge cluster up", "the LIVE k3d-lifecycle verb — a `forge up` pattern widened to drop the word between `forge` and `up` swallows it", regexp.MustCompile(`forge cluster up`)},
		{"a live `--env` flag", "the LIVE flag on commands where the environment really IS an optional modifier rather than the subject — `forge release where --env` narrows which ledger to ask, `forge secret set --env` and `forge domain bind --env` name which env's resource to act on. This is the counter-example that keeps the `--env` pattern anchored on the up/down verbs it was written for. The spelling has moved twice as its host commands were absorbed (`forge run --env`, then `forge build --push --env`), which is itself the argument for matching the FLAG rather than one command: the rule being protected is \"--env is legal where the env is a modifier\", and that rule outlives any particular command.", regexp.MustCompile(`--env[ =]`)},
		{"forge env deploy", "the LIVE spelling the env-noun verbs moved TO — a root-verb pattern widened to ignore what sits between `forge` and the verb swallows it", regexp.MustCompile(`forge env deploy`)},
		{`the English "forge dev" adjective`, "\"every forge dev namespace\", \"the forge dev loop\", \"a forge dev capability\" — prose the `forge dev` pattern must not reach, which is why that pattern requires a subcommand after it. Matched as a family rather than one fixed sentence: any single phrasing can legitimately leave the tree with the file that held it (\"a forge dev server\" did), and the assertion worth keeping is that the adjective still has SOME live use the pattern spares", regexp.MustCompile(`(?i)\bforge dev (?:server|namespace|loop|capability|controller)\b`)},
		{"`--type adapter`", "the LIVE scaffold flag value — the marker was renamed, the verb was NOT; a `forge:adapter` pattern widened to drop the `forge:` prefix swallows it", regexp.MustCompile(`--type[= ]adapter\b`)},
		{"the `adapter` skill", "forge still ships it — \"adapter\" is the design pattern, `outbound-io` is only the invariant a linter checks", regexp.MustCompile(`forge skill load adapter`)},
		{"`// forge:outbound-io`", "the LIVE marker the adapter scaffold stamps — the spelling `forge:adapter` was renamed TO", regexp.MustCompile(`forge:outbound-io`)},
		{"HasOutboundIODirective", "the live reader that replaced HasAdapterDirective", regexp.MustCompile(`\bHasOutboundIODirective\b`)},
		{"`forgeconv-deps-are-interfaces`", "the LIVE rule id. The retired spelling carried an `interactor-` infix; a pattern widened to drop it deletes the rule that catches `Deps: *db.PostgresRepository`", regexp.MustCompile(`forgeconv-deps-are-interfaces`)},
		{"the design-pattern noun \"interactor\"", "the `interactor` skill, and `forge scaffold package`'s help explaining why an orchestrator needs no flag, both keep the word — only the MARKER, the FLAG VALUE and the template tree went away", regexp.MustCompile(`(?i)\binteractors?\b`)},
	}

	root := repoRoot(t)
	seen := make([]int, len(lookalikes))
	forEachScannedFile(t, root, func(rel string, content []byte) {
		if strings.HasPrefix(rel, "internal/removalguard/") {
			return // this file names them all; that proves nothing
		}
		for i, l := range lookalikes {
			seen[i] += len(l.token.FindAllIndex(content, -1))
		}
	})

	for i, l := range lookalikes {
		if seen[i] == 0 {
			t.Errorf("%s has vanished from the repo (%s).\n"+
				"Either it was deleted — which is a real regression, not a lint fix — or a\n"+
				"removalguard pattern was widened until it swallowed it. Do not \"fix\" this\n"+
				"by deleting the allowance.", l.what, l.why)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Plumbing
// ─────────────────────────────────────────────────────────────────────────────

// repoRoot walks up from the working directory, which `go test` sets to this
// package's source directory whatever directory it was invoked from.
//
// Not runtime.Caller: under -trimpath (the workspace image's GOFLAGS default)
// that reports "github.com/reliant-labs/forge/internal/...", a module-relative
// path that is not a file on disk. internal/trimpathguard holds the tree to it.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	root, err := ascendToRoot(wd)
	if err != nil {
		t.Fatalf("locate repo root from %s: %v", wd, err)
	}
	return root
}

// ascendToRoot walks up from dir to the directory holding forge's go.mod.
func ascendToRoot(dir string) (string, error) {
	for {
		b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && bytes.Contains(b, []byte("module github.com/reliant-labs/forge\n")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod declaring module github.com/reliant-labs/forge found above the search start")
		}
		dir = parent
	}
}

// forEachScannedFile calls fn with each scannable file's repo-relative slash
// path and contents, in a stable order.
func forEachScannedFile(t *testing.T, root string, fn func(rel string, content []byte)) {
	t.Helper()

	files := scannedFiles(t, root)

	// Read the files concurrently. The scan is thousands of files against
	// every removal pattern, and under `-race` (which is how CI runs the
	// suite) the serial version took the package past `go test`'s 10-minute
	// default and panicked the whole job — masking every other package's
	// result behind a timeout that looked like a hang.
	//
	// Only the READ is parallel. fn is still invoked serially, in sorted
	// order, on the calling goroutine: the callers accumulate into shared
	// maps and their output is diffed against golden expectations, so
	// concurrent calls would both race and reorder findings.
	type readResult struct {
		rel     string
		content []byte
		err     error
	}
	results := make([]readResult, len(files))
	forEachIndex(len(files), func(i int) {
		rel := files[i]
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		results[i] = readResult{rel: rel, content: content, err: err}
	})

	for _, res := range results {
		rel, content := res.rel, res.content
		if res.err != nil {
			t.Fatalf("read %s: %v", rel, res.err)
		}
		if isBinary(content) {
			continue
		}
		fn(rel, content)
	}
}

// scannedFiles lists the files the guard reads, as sorted repo-relative slash
// paths: every file git would commit under root (tracked, plus untracked but
// not ignored), minus the skip lists.
//
// The list comes from git rather than a directory walk so that gitignored,
// per-developer output stays out of the scan. `forge generate` renders every
// shipped skill into .claude/skills/, which is gitignored. A walk read those
// rendered copies, e.g. .claude/skills/deploy-flux/SKILL.md, as surviving
// references, while the allowances name only the template sources they were
// rendered from. So the guard failed after any local generate and passed in
// CI, which has no .claude/. A walk learns ignored directories one at a time
// (skipDirs grew .forge, .scratch and .kilo that way); git already knows
// every one. An untracked file that is NOT ignored is still scanned, because
// it is one `git add` away from shipping.
func scannedFiles(t *testing.T, root string) []string {
	t.Helper()

	out, err := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		var stderr []byte
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr = exitErr.Stderr
		}
		t.Fatalf("git ls-files in %s: %v\n%s\nThe guard scans exactly the files git would commit, so it needs a git checkout.",
			root, err, bytes.TrimSpace(stderr))
	}

	// A Go build cache is not source. Its entries are verbatim copies of
	// compiler input, so a cached `rbac.pb.go` reads as a live reference to
	// a removed feature. Keyed on Go's own sentinel because GOCACHE is
	// configurable and a project-local one is not guaranteed to be ignored:
	// agents here point it at $WORKTREE/.gocache, but the name is a
	// convention. Memoized per directory, since every file's ancestors are
	// checked.
	isCache := map[string]bool{}
	inGoBuildCache := func(rel string) bool {
		for dir := path.Dir(rel); dir != "."; dir = path.Dir(dir) {
			cache, seen := isCache[dir]
			if !seen {
				cache = commitpolicy.IsGoBuildCacheDir(filepath.Join(root, filepath.FromSlash(dir)))
				isCache[dir] = cache
			}
			if cache {
				return true
			}
		}
		return false
	}
	inSkippedDir := func(rel string) bool {
		for _, part := range strings.Split(path.Dir(rel), "/") {
			if skipDirs[part] {
				return true
			}
		}
		return false
	}

	seen := map[string]bool{}
	var files []string
	for _, rel := range strings.Split(string(out), "\x00") {
		// An unmerged path is listed once per stage, and an untracked
		// nested repository as a single "dir/" entry; Lstat below drops
		// the directory.
		if rel == "" || seen[rel] {
			continue
		}
		seen[rel] = true
		if inSkippedDir(rel) || skipScannedFile(path.Base(rel)) || skipExts[strings.ToLower(path.Ext(rel))] {
			continue
		}
		info, statErr := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		if statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				continue // tracked, but deleted in the working tree
			}
			t.Fatalf("stat %s: %v", rel, statErr)
		}
		if !info.Mode().IsRegular() || info.Size() > maxFileSize {
			continue
		}
		if inGoBuildCache(rel) {
			continue
		}
		files = append(files, rel)
	}
	sort.Strings(files)
	return files
}

// forEachIndex calls fn(i) for every i in [0, n) on a FIXED pool of
// GOMAXPROCS workers, and returns when all calls have.
//
// A fixed pool, not a goroutine per item behind a semaphore: the latter
// creates every goroutine up front and only bounds how many RUN, so the live
// goroutine count — and the stacks, closures and captured slices they hold —
// grows with the size of the repository rather than with the machine. The
// work is CPU-bound regex matching, so GOMAXPROCS is the useful width.
func forEachIndex(n int, fn func(i int)) {
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), n) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				fn(i)
			}
		}()
	}
	for i := range n {
		next <- i
	}
	close(next)
	wg.Wait()
}

// isBinary reports whether content looks like a binary file. A NUL byte in the
// first 8 KiB is the same heuristic git uses.
func isBinary(content []byte) bool {
	head := content
	if len(head) > 8192 {
		head = head[:8192]
	}
	return bytes.IndexByte(head, 0) >= 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
