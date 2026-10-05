// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY: the real Forge pane — ForgeLayout, the Overview and the env page —
 * against canned control-plane and daemon answers, with no login.
 *
 * WHY THE FAKE IS AT THE NETWORK, NOT IN THE QUERY CACHE. Seeding React
 * Query would bypass the code most worth looking at: the transport, the
 * classification of forge's answers, the routing of each read to the backend
 * or the daemon. So this harness answers the Connect JSON requests themselves
 * and lets every hook, service and component run as it does in the product.
 * Each request is logged to the console with which side it would have gone
 * to, which is how a screenshot can be checked against the source model.
 *
 * The fixtures MIRROR control-plane's state on 2026-10-04 (cluster names
 * scrubbed — this file ships in a public repo): four declared
 * environments, prod on v1.7.15 by forge's file ledger, dev running locally
 * via `forge env up` — and, in the `unregistered` scenario, no rows in the
 * control plane for any of them, which is what produced "Not built yet" on
 * every tab.
 *
 *   /forge-pane-preview?scenario=registered|unregistered|daemon-offline&path=/forge/env/prod
 */

import { useEffect, useMemo, useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";

import { useProjectStore, type Project } from "@/store/projectStore";

import { ForgeEnvPage } from "../EnvPage/ForgeEnvPage";
import { ForgeLayout } from "../ForgeLayout";
import { ForgeOverviewPage } from "../Overview/ForgeOverviewPage";
import { forgeEnvPageSearchSchema, forgeOverviewSearchSchema } from "@/routeSchemas";

type Scenario = "registered" | "unregistered" | "daemon-offline";

const PROJECT: Project = {
  id: "cp-project",
  name: "control-plane",
  path: "/src/control-plane",
  is_git_repo: true,
  worktree_count: 0,
  last_active: "",
  created_at: "",
  updated_at: "",
  is_forge: true,
  forge_project_name: "control-plane",
} as Project;

const OTHER: Project = { ...PROJECT, id: "reliant-project", name: "reliant", forge_project_name: undefined } as Project;

// ── forge's answers (the daemon) ────────────────────────────────────────────

const TOPOLOGY = {
  project: "control-plane",
  latest_release: "v1.7.15",
  releases: ["v1.7.15", "v1.7.14", "v1.7.13"],
  environments: [
    { env: "dev", declared: true, bound: false, destination: "mixed", note: "never promoted" },
    { env: "dev-k8s", declared: true, bound: false, destination: "cluster" },
    {
      env: "e2e",
      declared: true,
      bound: true,
      destination: "cluster",
      release: "20261004.090833-65c4c4ea711d",
      promoted_at: "2026-10-04T09:10:29Z",
      kube_context: "k3d-control-plane-v2",
      namespace: "control-plane-e2e",
      images: [{ image: "control-plane", state: "not_verified" }],
    },
    {
      env: "prod",
      declared: true,
      bound: true,
      destination: "mixed",
      release: "v1.7.15",
      promoted_at: "2026-10-04T21:18:54Z",
      kube_context: "gke-prod-context",
      namespace: "control-plane-prod",
      images: [{ image: "control-plane", state: "not_verified" }],
    },
  ],
};

/** `forge env status dev --json` as forge v0.1.44 nests it — runtime under `runtime`. */
const DEV_STATUS = {
  ok: true,
  env: "dev",
  bound: false,
  images: [],
  runtime: {
    env: "dev",
    lifecycle: "local",
    destination: "mixed",
    head_commit_at: "2026-10-04T21:10:02Z",
    services: [
      { name: "admin-server", kind: "host", url: "http://localhost:8090", port: 8090, listening: true, pid: 57674, owned: true },
      { name: "admin-api", kind: "host", url: "http://localhost:8091", port: 8091, listening: true, pid: 57666, owned: true },
      { name: "control-plane-workers", kind: "host", port: 8092, listening: true, pid: 57677, owned: true },
      { name: "reliant-api-server", kind: "host", port: 3091, listening: true, pid: 57578, owned: true },
      { name: "reliant-web", kind: "frontend", url: "http://localhost:3000", port: 3000, listening: true, pid: 57428, owned: true },
    ],
    checks: [
      { name: "Compose Infra", status: "pass", message: "9/9 services healthy/running" },
      { name: "App Health", status: "pass", message: "admin-server localhost:8090: healthz=ok readyz=ok" },
      { name: "Metrics", status: "pass", message: "prometheus scraping 6 targets" },
      { name: "Traces", status: "unknown", message: "no traces in the last 5m" },
    ],
  },
};

const PROD_STATUS = {
  ok: true,
  env: "prod",
  bound: true,
  release: "v1.7.15",
  runtime: { env: "prod", destination: "mixed", services: [], checks: [] },
};

const CHECKOUTS = {
  project: "control-plane",
  main_ref: "origin/main",
  checkouts: [
    { label: "main", kind: "worktree", path: "/src/control-plane", branch: "main", dirty: false, ahead_of_main: 0, behind_main: 0, selected: true },
    { label: "feat/deploy-hub", kind: "worktree", path: "/src/wt/deploy-hub", branch: "feat/deploy-hub", dirty: true, ahead_of_main: 4, behind_main: 1 },
    { label: "fix/egress-policy", kind: "worktree", path: "/src/wt/egress", branch: "fix/egress-policy", dirty: false, ahead_of_main: 1, behind_main: 0 },
    { label: "spike/flux-local", kind: "worktree", path: "/src/wt/flux", branch: "spike/flux-local", dirty: false },
  ],
};

function diffFor(env: string) {
  return {
    environments: [
      {
        env,
        status: "ok",
        live_source: "bundle",
        diff: {
          changed: [
            { kind: "Deployment", name: "admin-server", namespace: `control-plane-${env}`, fields: ["spec.template.spec.containers[0].image"] },
            { kind: "ConfigMap", name: "admin-server-env", namespace: `control-plane-${env}`, fields: ["data.LOG_LEVEL"] },
          ],
          added: [{ kind: "NetworkPolicy", name: "egress-allow-hub", namespace: `control-plane-${env}` }],
          removed: [],
          images: [{ image: "control-plane", live: "sha256:3f2a…", candidate: "sha256:9c71…" }],
        },
      },
    ],
  };
}

// ── the control plane's answers (the backend) ───────────────────────────────

const ts = (iso: string) => iso;

function liveEnvironments(scenario: Scenario) {
  if (scenario === "unregistered") return [];
  return [
    {
      environment: { id: "env-dev", name: "dev", project: "control-plane", kind: "DEPLOY_ENVIRONMENT_KIND_LOCAL", createdAt: ts("2026-09-01T10:00:00Z") },
    },
    {
      environment: {
        id: "env-prod",
        name: "prod",
        project: "control-plane",
        kind: "DEPLOY_ENVIRONMENT_KIND_SELF_MANAGED",
        currentPromotionId: "promo-15",
        declaredAt: ts("2026-10-04T21:18:40Z"),
        declaredShape: {
          kind: "self_managed",
          workloads: [
            { name: "admin-server", runtime: "cluster", cluster: "gke-prod" },
            { name: "control-plane-workers", runtime: "cluster", cluster: "gke-prod" },
            { name: "daemon-gateway", runtime: "cluster", cluster: "gke-prod" },
            { name: "reliant-api-server", runtime: "cluster", cluster: "gke-prod" },
          ],
          secrets: [{ name: "STRIPE_WEBHOOK_SECRET", provider: "hosted", declared_by: ["admin-server"] }],
          domains: ["api.example.com"],
          clusters: ["gke-prod"],
        },
        declaredBy: { repo: "github.com/reliant-labs/control-plane", commit: "47d0d1525ccc", branch: "main", forgeVersion: "v0.1.44" },
      },
      currentPromotion: {
        id: "promo-15",
        releaseVersion: "v1.7.15",
        promotedByActor: "sean",
        createdAt: ts("2026-10-04T21:18:54Z"),
        releaseProvenance: { repo: "github.com/reliant-labs/control-plane", commit: "47d0d1525ccc", branch: "main" },
      },
      currentRelease: { id: "rel-15", version: "v1.7.15", gitCommit: "47d0d1525ccc", provenance: { commit: "47d0d1525ccc", branch: "main" } },
      phase: "DEPLOY_ROLLOUT_PHASE_SUCCEEDED",
      drift: { state: "in_sync", observedAt: ts("2026-10-04T21:22:10Z") },
    },
  ];
}

const PROMOTIONS = [
  { id: "promo-15", releaseVersion: "v1.7.15", kind: "promote", promotedByActor: "sean", createdAt: ts("2026-10-04T21:18:54Z"), artifacts: [{ name: "control-plane", digest: "sha256:47d0d1525ccc4673aa" }] },
  { id: "promo-14", releaseVersion: "v1.7.14", kind: "promote", promotedByActor: "ci", createdAt: ts("2026-10-03T16:02:11Z"), artifacts: [{ name: "control-plane", digest: "sha256:8a54465b8d718276bb" }] },
  { id: "promo-13", releaseVersion: "v1.7.13", kind: "promote", promotedByActor: "ci", createdAt: ts("2026-10-02T11:40:03Z"), artifacts: [] },
];

const CONVERGENCES = [
  { id: "conv-15", state: "converged", reason: "ReconciliationSucceeded", cluster: "gke-prod", observedAt: ts("2026-10-04T21:22:10Z") },
];

// ── the fake network ────────────────────────────────────────────────────────

const META_OK = { isForgeProject: true, supported: true, forgeVersion: "v0.1.44", exitCode: 0 };

function forgeReply(report: unknown) {
  return { meta: META_OK, reportJson: JSON.stringify(report) };
}

function answer(scenario: Scenario, path: string, body: Record<string, unknown>): { status: number; json: unknown } | null {
  const method = path.split("/").pop() ?? "";
  if (path.includes("reliant.v1.ForgeService/")) {
    if (scenario === "daemon-offline") {
      return { status: 503, json: { code: "unavailable", message: "no daemon connected for user" } };
    }
    switch (method) {
      case "GetTopology":
        return { status: 200, json: forgeReply(TOPOLOGY) };
      case "GetEnvStatus":
        return { status: 200, json: forgeReply(body.env === "dev" ? DEV_STATUS : { ...PROD_STATUS, env: body.env }) };
      case "GetEnvShape":
        return {
          status: 200,
          json: forgeReply({
            project: "control-plane",
            env: body.env,
            kind: body.env === "dev" ? "local" : "self_managed",
            lifecycle: body.env === "dev" ? "local" : undefined,
            shape: { kind: body.env === "dev" ? "local" : "self_managed", workloads: [], secrets: [] },
            provenance: { commit: "47d0d1525ccc", branch: "main", forge_version: "v0.1.44" },
          }),
        };
      case "ListCheckouts":
        return { status: 200, json: forgeReply(CHECKOUTS) };
      case "DiffEnv":
        return { status: 200, json: forgeReply(diffFor(String(body.env ?? "prod"))) };
      case "GetAudit":
        return { status: 200, json: forgeReply({ project: "control-plane", ok: true, findings: [], summary: { total: 0 } }) };
      default:
        return { status: 200, json: forgeReply({}) };
    }
  }
  if (path.includes("controlplane.v1.DeployService/")) {
    switch (method) {
      case "GetLiveView":
        return { status: 200, json: { environments: liveEnvironments(scenario) } };
      case "ListEnvironments":
        return { status: 200, json: { environments: liveEnvironments(scenario).map((env) => env.environment) } };
      case "ListPromotions":
        return { status: 200, json: { promotions: body.environmentId === "env-prod" ? PROMOTIONS : [] } };
      case "ListConvergences":
        return { status: 200, json: { convergences: body.environmentId === "env-prod" ? CONVERGENCES : [] } };
      default:
        return { status: 200, json: {} };
    }
  }
  if (path.includes("SecretStoreService/ListSecrets")) {
    return {
      status: 200,
      json: { secrets: [{ name: "STRIPE_WEBHOOK_SECRET", currentVersion: 3, oldestVersion: 1 }, { name: "DATABASE_URL", currentVersion: 1, oldestVersion: 1 }] },
    };
  }
  return null;
}

function installFakeNetwork(scenario: Scenario): () => void {
  const realFetch = window.fetch.bind(window);
  window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const path = new URL(url, window.location.origin).pathname;
    let body: Record<string, unknown> = {};
    try {
      // connect-web hands fetch the JSON as bytes, not a string.
      const raw = init?.body;
      const text =
        typeof raw === "string" ? raw : raw instanceof Uint8Array ? new TextDecoder().decode(raw) : "";
      body = text ? (JSON.parse(text) as Record<string, unknown>) : {};
    } catch {
      body = {};
    }
    const reply = answer(scenario, path, body);
    if (!reply) return realFetch(input, init);
    const side = path.includes("ForgeService") ? "DAEMON" : "BACKEND";
    console.info(`[forge-pane-preview] ${side} ${path} ${JSON.stringify(body)}`);
    // A beat of latency so loading states are visible in the harness.
    await new Promise((resolve) => setTimeout(resolve, 250));
    return new Response(JSON.stringify(reply.json), {
      status: reply.status,
      headers: { "content-type": "application/json" },
    });
  };
  return () => {
    window.fetch = realFetch;
  };
}

// ── the harness ─────────────────────────────────────────────────────────────

function buildRouter(path: string) {
  const root = createRootRoute({ component: () => <Outlet /> });
  const authenticated = createRoute({ getParentRoute: () => root, id: "_authenticated", component: () => <Outlet /> });
  const forgeLayout = createRoute({ getParentRoute: () => authenticated, id: "_forge", component: ForgeLayout });
  const overview = createRoute({
    getParentRoute: () => forgeLayout,
    path: "/forge",
    validateSearch: forgeOverviewSearchSchema,
    component: ForgeOverviewPage,
  });
  const env = createRoute({
    getParentRoute: () => forgeLayout,
    path: "/forge/env/$env",
    validateSearch: forgeEnvPageSearchSchema,
    component: ForgeEnvPage,
  });
  const home = createRoute({ getParentRoute: () => root, path: "/", component: () => <p className="p-6">Closed.</p> });
  return createRouter({
    routeTree: root.addChildren([authenticated.addChildren([forgeLayout.addChildren([overview, env])]), home]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
}

export function ForgePanePreview() {
  // Keyed on the query string: the app router treats a change of `?scenario`
  // as the same route, so without a remount the inner router and its cache
  // would carry the previous case's state.
  return <ForgePanePreviewCase key={window.location.search} />;
}

function ForgePanePreviewCase() {
  const params = new URLSearchParams(window.location.search);
  const scenario = (params.get("scenario") as Scenario) || "registered";
  const path = params.get("path") || `/forge?project=${PROJECT.id}`;
  const [ready, setReady] = useState(false);

  useEffect(() => {
    const restore = installFakeNetwork(scenario);
    useProjectStore.setState({ projects: [PROJECT, OTHER], currentProject: PROJECT });
    setReady(true);
    return restore;
  }, [scenario]);

  const queryClient = useMemo(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }), []);
  const router = useMemo(() => buildRouter(path), [path]);

  if (!ready) return null;
  return (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  );
}
