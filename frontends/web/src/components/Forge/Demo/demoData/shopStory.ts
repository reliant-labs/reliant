// Copyright (c) 2025 Reliant Labs

/**
 * The ONE story the public demo tells. Every surface of the demo — the
 * console's Releases/Environments (consoleScenario.ts), the observability
 * views (shop/*.ts, busyApp.ts), the Data browser (inventoryScenario.ts) —
 * reads its people, its releases, its services and its incident from here,
 * so no two pages can disagree about them.
 *
 *   shop      an online shop: an `api` (orders + checkout, Connect and a few
 *             HTTP routes), a `worker` (order fulfilment off NATS) and an
 *             `inventory` service, plus a static storefront `shop-web`, in
 *             dev, staging and prod. Outside it: Postgres, NATS and a payment
 *             provider (an external dependency, not a service of shop's).
 *   the team  jon, maya and priya (DEMO_CAST), and CI (github-actions).
 *   releases  v1.8.1 → v1.8.4 (DEMO_RELEASES), each with one commit, one
 *             author, one cut time; every promotion of each (RUNNING +
 *             HISTORY) with one promoter and one time (demoLedger).
 *   incident  ONE, in prod, on api: v1.8.3 ("retry the payment charge once
 *             on a timeout") reached prod 40 minutes ago, and since then the
 *             payment provider's slow answers plus the retry overrun
 *             CreateOrder's and Checkout's 3s deadline (DEMO_INCIDENT). The
 *             post-promotion `forge obs check` failed, OrdersErrorRateHigh
 *             fires, the issue is assigned to maya, and v1.8.4 (the fix) is
 *             in dev. Everything else is healthy: staging runs v1.8.3 too, but
 *             against the provider's sandbox, which never times out.
 *
 * The traffic model (DEMO_ROUTES, demoRouteRate, demoStats) is analytic: the
 * per-request generator (busyApp.ts) samples it, and every aggregate — the
 * overview, panels, the service graph, the RPC explorer, release health,
 * metrics — is computed from it, so a headline, its sparkline and a table
 * agree.
 *
 * Times are offsets from "now" (the page load), never absolute dates.
 */

let loadedAt: number | null = null;
/**
 * "Now" for the whole demo: the page load. Every surface measures its times
 * from this one instant, so "40 minutes ago" on one page is the same moment
 * as on every other, however long the tab stays open.
 */
export const demoNow = (): number => (loadedAt ??= Date.now());
/** Test seam: forget the page load (the next read is a new one). */
export const resetDemoNowForTests = (at: number | null = null) => {
  loadedAt = at;
};

export const MINUTE = 60_000;
export const HOUR = 60 * MINUTE;
export const DAY = 24 * HOUR;

// ── People ───────────────────────────────────────────────────────────────────

/** The team that builds shop. Nobody else ever acts on it (CI aside). */
export const DEMO_CAST = [
  { handle: "jon", id: "user-jon", email: "jon@example.com", role: "admin" as const },
  { handle: "maya", id: "user-maya", email: "maya@example.com", role: "member" as const },
  { handle: "priya", id: "user-priya", email: "priya@example.com", role: "member" as const },
] as const;
export type DemoPerson = (typeof DEMO_CAST)[number]["handle"];
/** The automation actor: CI promotes to dev and re-applies, nothing else. */
export const DEMO_CI_ACTOR = "github-actions";
/** The reader of the demo: the console treats the visitor as jon (read-only). */
export const DEMO_READER: DemoPerson = "jon";

export const demoPerson = (handle: string) => DEMO_CAST.find((p) => p.handle === handle);

/**
 * shop's customers: the people who BUY from it (Data's `customers` table,
 * the identity provider's users, the browser sessions and traces that carry a
 * user id). Never the team.
 */
export const DEMO_CUSTOMERS = [
  { id: "cus_4f1a9c", name: "Lena Fischer", email: "lena.fischer@example.com", country: "DE", city: "Berlin" },
  { id: "cus_7b20e1", name: "Tomás Ortega", email: "tomas.ortega@example.com", country: "ES", city: "Madrid" },
  { id: "cus_2c88d4", name: "Aiko Tanaka", email: "aiko.tanaka@example.com", country: "JP", city: "Osaka" },
  { id: "cus_9e5b37", name: "Sam Okafor", email: "sam.okafor@example.com", country: "GB", city: "London" },
  { id: "cus_51d0fa", name: "Chloé Martin", email: "chloe.martin@example.com", country: "FR", city: "Lyon" },
  { id: "cus_0a6e72", name: "Ravi Iyer", email: "ravi.iyer@example.com", country: "US", city: "Austin" },
  { id: "cus_c3f915", name: "Noor Haddad", email: "noor.haddad@example.com", country: "NL", city: "Utrecht" },
  { id: "cus_e81b06", name: "Mateo Rossi", email: "mateo.rossi@example.com", country: "IT", city: "Turin" },
  { id: "cus_6d4c2b", name: "Hannah Berg", email: "hannah.berg@example.com", country: "SE", city: "Malmö" },
  { id: "cus_b07f58", name: "Kofi Mensah", email: "kofi.mensah@example.com", country: "US", city: "Seattle" },
] as const;

// ── Services ─────────────────────────────────────────────────────────────────

/** shop's backend services: each is a workload, emits traces/logs/metrics/profiles. */
export const DEMO_BACKENDS = ["api", "worker", "inventory"] as const;
/** shop's storefront: a static site (a workload of tier STATIC) and the browser service name. */
export const DEMO_FRONTEND = "shop-web";
/** Every workload, in the order the console lists them. */
export const DEMO_SERVICE_NAMES = [...DEMO_BACKENDS, DEMO_FRONTEND] as const;
export type DemoService = (typeof DEMO_SERVICE_NAMES)[number];
/** Dependencies that are NOT shop's services: graph nodes only, never workloads or filter values. */
export const DEMO_DEPENDENCIES = { database: "postgres", broker: "nats", payments: "payments" } as const;
/** The payment provider's host (the api's client span to it). */
export const DEMO_PAYMENTS_HOST = "payments.example";

// ── Releases ─────────────────────────────────────────────────────────────────

export const DEMO_REPO = "github.com/acme/shop";

/** The releases, newest first: what each changed, and when it was cut. A commit's time is `age + (i + 1) × 40m` (commitAgeMs). */
export const DEMO_RELEASES = [
  {
    version: "v1.8.4",
    commit: "9c2e41d07a3f",
    subject: "fix(orders): give the payment call its own deadline and retry budget",
    author: "maya",
    age: 30 * MINUTE,
    commits: [
      { sha: "9c2e41d07a3f", subject: "fix(orders): give the payment call its own deadline and retry budget", author: "maya" },
      { sha: "51ab0e9c11d2", subject: "test(orders): CreateOrder survives a slow payment provider", author: "maya" },
    ],
    /** The workloads whose artifact changed against the release before it. */
    changed: ["api"],
  },
  {
    version: "v1.8.3",
    commit: "e7d93a2b5c18",
    subject: "feat(orders): retry the payment charge once on a timeout",
    author: "jon",
    age: 26 * HOUR,
    commits: [
      { sha: "e7d93a2b5c18", subject: "feat(orders): retry the payment charge once on a timeout", author: "jon" },
      { sha: "a40f6c2d9e77", subject: "feat(inventory): reserve stock from the order event", author: "priya" },
      { sha: "3b81d5f0e2c4", subject: "chore(deps): bump connect-go to v1.19", author: "jon" },
    ],
    changed: ["api", "worker", "inventory"],
  },
  {
    version: "v1.8.2",
    commit: "c51f08e3a96b",
    subject: "feat(web): show the order's status on the confirmation page",
    author: "priya",
    age: 6 * DAY,
    commits: [
      { sha: "c51f08e3a96b", subject: "feat(web): show the order's status on the confirmation page", author: "priya" },
      { sha: "8d3e17b42a90", subject: "feat(inventory): index orders by customer", author: "priya" },
    ],
    changed: ["inventory", "shop-web"],
  },
  {
    version: "v1.8.1",
    commit: "07e2b9d4f1a0",
    subject: "fix(worker): ack the order event only after inventory answers",
    author: "maya",
    age: 9 * DAY + 2 * HOUR,
    commits: [{ sha: "07e2b9d4f1a0", subject: "fix(worker): ack the order event only after inventory answers", author: "maya" }],
    changed: ["worker"],
  },
] as const;
export type DemoRelease = (typeof DEMO_RELEASES)[number];

/** How long before now the i-th commit (0 = the release's head) of `release` was made. */
export const commitAgeMs = (release: DemoRelease, i: number) => release.age + (i + 1) * 40 * MINUTE;

export const demoRelease = (version: string): DemoRelease | undefined => DEMO_RELEASES.find((r) => r.version === version);

export const DEMO_ENV_IDS = ["env-dev", "env-staging", "env-prod"] as const;
export type DemoEnvKey = (typeof DEMO_ENV_IDS)[number];
const ENV_NAME: Record<DemoEnvKey, string> = { "env-dev": "dev", "env-staging": "staging", "env-prod": "prod" };

/** A health gate's outcome: the `forge obs check` the promotion recorded ("none": it recorded none). */
export type DemoGate = { status: "passed" | "failed"; summary: string } | "none";

/**
 * One promotion in an environment's ledger. Beyond the release, who and when:
 * `seq` tells apart two promotions of one release in one env (it suffixes the
 * id), `targets` makes it a scoped deploy, `from: null` a direct deploy (a
 * rollback is one), and `health` is the `forge obs check` gate it recorded.
 */
export interface DemoPromotion {
  version: string;
  age: number;
  by: string;
  seq?: string;
  targets?: readonly string[];
  note?: string;
  from?: string | null;
  health?: DemoGate;
}

/** The default gate: the error rate and p95 held. */
export const PASSED_GATE: DemoGate = { status: "passed", summary: "error rate and p95 within the previous release's window" };

/** The incident's gate, on prod's v1.8.3 (see DEMO_INCIDENT). */
export const PROD_V183_GATE: DemoGate = {
  status: "failed",
  summary: "release health FAIL: CreateOrder error rate 0.0% → 12.4%, p95 128ms → 3.0s",
};

/** Which release each environment runs, and when it got it. */
export const RUNNING: Record<DemoEnvKey, DemoPromotion> = {
  "env-dev": { version: "v1.8.4", age: 20 * MINUTE, by: "maya", note: "The payment call gets its own 2s deadline and retry budget." },
  "env-staging": { version: "v1.8.3", age: 3 * HOUR, by: "jon", note: "Retry the payment charge once on a timeout; inventory reserves from the order event." },
  "env-prod": { version: "v1.8.3", age: 40 * MINUTE, by: "jon", health: PROD_V183_GATE, note: "Retry the payment charge once on a timeout; inventory reserves from the order event." },
};

/**
 * Earlier promotions, newest first (beyond the one running).
 *
 * staging's is the long one, so the environment page's Promotions card shows
 * every kind of row a real ledger holds (oldest first):
 *
 *   9d    v1.8.1 everywhere; then the worker alone re-applied (a scoped redeploy)
 *   5.8d  v1.8.2 everywhere, rolled back to v1.8.1 four hours later (a whole-
 *         release rollback), and promoted again once the migration was split
 *   4.5d  v1.8.2 re-applied by CI after a secret rotation (a redeploy with no
 *         health check recorded); inventory re-applied alone a day later
 *   22h   v1.8.3 to the api alone (a canary); the storefront re-shipped on
 *         v1.8.2; then api went BACK to v1.8.2 with the storefront in one
 *         scoped deploy while inventory's stock migration ran: api rolled
 *         back, shop-web redeployed (a mixed row)
 *   3h    v1.8.3 everywhere (RUNNING), then on to prod
 *
 * Eleven rows with the running one, so the card lists ten and offers the rest.
 */
export const HISTORY: Record<DemoEnvKey, DemoPromotion[]> = {
  "env-dev": [
    { version: "v1.8.3", age: 26 * HOUR - 10 * MINUTE, by: DEMO_CI_ACTOR },
    { version: "v1.8.2", age: 6 * DAY - 10 * MINUTE, by: DEMO_CI_ACTOR },
    { version: "v1.8.1", age: 9 * DAY + HOUR, by: DEMO_CI_ACTOR },
  ],
  "env-staging": [
    {
      version: "v1.8.2",
      seq: "api-web",
      age: 20.5 * HOUR,
      by: "jon",
      targets: ["api", "shop-web"],
      from: null,
      note: "api back on v1.8.2 until inventory's stock migration had run; shop-web re-shipped with it.",
    },
    {
      version: "v1.8.2",
      seq: "web",
      age: 21 * HOUR,
      by: "priya",
      targets: ["shop-web"],
      from: null,
      health: "none",
      note: "Storefront re-shipped for the /checkout CDN cache rule.",
    },
    { version: "v1.8.3", seq: "api", age: 22 * HOUR, by: "jon", targets: ["api"], note: "Canary: the api alone first." },
    {
      version: "v1.8.2",
      seq: "inventory",
      age: 3.5 * DAY,
      by: "priya",
      targets: ["inventory"],
      from: null,
      note: "inventory re-applied once the orders index finished building.",
    },
    {
      version: "v1.8.2",
      seq: "reapply",
      age: 4.5 * DAY,
      by: DEMO_CI_ACTOR,
      from: null,
      health: "none",
      note: "Re-applied after PAYMENTS_WEBHOOK_SECRET was rotated.",
    },
    { version: "v1.8.2", seq: "again", age: 5.2 * DAY, by: "priya", note: "Promoted again, with the orders index built concurrently." },
    {
      version: "v1.8.1",
      seq: "rollback",
      age: 5.6 * DAY,
      by: "priya",
      from: null,
      note: "v1.8.2's inventory migration held a lock on orders for 40s; back to v1.8.1.",
    },
    { version: "v1.8.2", age: 5.8 * DAY, by: "priya" },
    {
      version: "v1.8.1",
      seq: "worker",
      age: 8.7 * DAY,
      by: "maya",
      targets: ["worker"],
      from: null,
      health: "none",
      note: "worker re-applied with a longer ack timeout.",
    },
    { version: "v1.8.1", age: 9 * DAY, by: "maya" },
  ],
  "env-prod": [
    { version: "v1.8.2", age: 5 * DAY, by: "priya" },
    { version: "v1.8.1", age: 8.5 * DAY, by: "maya" },
  ],
};

/** Which environment each promotes from. */
export const PROMOTED_FROM: Record<DemoEnvKey, string | undefined> = { "env-dev": undefined, "env-staging": "dev", "env-prod": "staging" };

/** One promotion's id, everywhere (DeployService, DeliveryInsights, release health): `promo-prod-v183`. */
export const promotionId = (env: string, version: string, seq?: string) =>
  `promo-${env.replace("env-", "")}-${version.replace(/\./g, "")}${seq ? `-${seq}` : ""}`;

/** A release's recorded bundle in one environment (config, artifacts, promotions): synthetic, stable. */
export const demoBundleId = (env: string, version: string) => `demo-bundle-${env}-${version}`;
export function demoBundleDigest(env: string, version: string): string {
  // A clearly synthetic but stable per-environment/release identity.
  const envIndex = DEMO_ENV_IDS.findIndex((e) => e === env) + 1;
  const releaseIndex = DEMO_RELEASES.findIndex((r) => r.version === version) + 1;
  return `sha256:${`${envIndex}${releaseIndex}`.repeat(32)}`;
}

/** The release whose cut last changed `workload`'s artifact, as of `version`: unchanged artifacts keep their digest. */
export function artifactVersion(workload: string, version: string): string {
  const from = DEMO_RELEASES.findIndex((r) => r.version === version);
  for (let i = Math.max(0, from); i < DEMO_RELEASES.length; i++) {
    if ((DEMO_RELEASES[i].changed as readonly string[]).includes(workload)) return DEMO_RELEASES[i].version;
  }
  return DEMO_RELEASES[DEMO_RELEASES.length - 1].version;
}

/** One row of the flat ledger: what every surface says about one promotion. */
export interface DemoLedgerEntry {
  promotionId: string;
  envId: DemoEnvKey;
  envName: string;
  version: string;
  commit: string;
  by: string;
  /** The person's user id; "" for CI. */
  byUserId: string;
  atMs: number;
  /** "rollback" for a direct deploy of a release older than the one it replaced; else "promote". */
  kind: "promote" | "rollback";
  targets: readonly string[];
  gate: DemoGate;
  /** Whether this is the env's current promotion. */
  current: boolean;
  note: string;
  seq?: string;
}

const order = (v: string) => DEMO_RELEASES.length - DEMO_RELEASES.findIndex((r) => r.version === v);

/** Every promotion, oldest first, as of `now`. */
export function demoLedger(now: number = demoNow()): DemoLedgerEntry[] {
  const out: DemoLedgerEntry[] = [];
  for (const envId of DEMO_ENV_IDS) {
    const rows = [RUNNING[envId], ...HISTORY[envId]].slice().sort((a, b) => b.age - a.age);
    let previous: string | null = null;
    for (const p of rows) {
      const release = demoRelease(p.version)!;
      const rollback = p.from === null && previous !== null && order(p.version) < order(previous) && !p.targets?.length;
      out.push({
        promotionId: promotionId(envId, p.version, p.seq),
        envId,
        envName: ENV_NAME[envId],
        version: p.version,
        commit: release.commit,
        by: p.by,
        byUserId: demoPerson(p.by)?.id ?? "",
        atMs: now - p.age,
        kind: rollback ? "rollback" : "promote",
        targets: p.targets ?? [],
        gate: p.health ?? PASSED_GATE,
        current: p === RUNNING[envId],
        note: p.note ?? "",
        seq: p.seq,
      });
      if (!p.targets?.length) previous = p.version;
    }
  }
  return out.sort((a, b) => a.atMs - b.atMs);
}

/** The release running in `envId` at `atMs` (whole-environment promotions only). */
export function demoVersionAt(envId: string, atMs: number, now: number = demoNow()): string | null {
  return demoLedger(now).filter((p) => p.envId === envId && !p.targets.length && p.atMs <= atMs).at(-1)?.version ?? null;
}

// ── Traffic ──────────────────────────────────────────────────────────────────

/** prod's traffic: one potential request per SLOT_MS, kept with probability dailyLoad (≈ 2,400 an hour at peak, ≈ 1,000 at night). */
export const SLOT_MS = 1_500;
/** Each environment's share of prod's traffic. Only prod has real users. */
export const ENV_TRAFFIC: Record<DemoEnvKey, number> = { "env-prod": 1, "env-staging": 0.04, "env-dev": 0.015 };

/**
 * The quietest hour's share of the busiest's. shop sells worldwide, so it is
 * never idle: whatever the hour a visitor opens the demo, prod has enough
 * traffic for the incident to read clearly (dozens of failed charges since
 * v1.8.3, not a handful).
 */
export const LOAD_FLOOR = 0.4;

/** Load over the day: busiest in the (UTC) afternoon, quietest at night, never below LOAD_FLOOR. Mean ≈ 0.7. */
export function dailyLoad(timeMs: number): number {
  const hour = (timeMs / 3_600_000) % 24;
  const wave = (1 + Math.sin(((hour - 9) / 24) * 2 * Math.PI)) / 2;
  return LOAD_FLOOR + (1 - LOAD_FLOOR) * wave;
}

/**
 * shop's routes: the root operation, the service that serves it, its share of
 * requests (weight), its median latency (ms), whether it charges the payment
 * provider (the incident's path), and the downstream server spans one request
 * makes (`share`: the fraction of requests that make that hop).
 */
export const DEMO_ROUTES = [
  { op: "orders.v1.OrdersService/CreateOrder", service: "api", weight: 14, base: 48, charges: true, browser: false, hops: [{ service: "worker", op: "orders.created process", base: 28, share: 0.6 }, { service: "inventory", op: "inventory.v1.InventoryService/Reserve", base: 22, share: 0.6 }] },
  { op: "orders.v1.OrdersService/GetOrder", service: "api", weight: 22, base: 9, charges: false, browser: false, hops: [] },
  { op: "orders.v1.OrdersService/ListOrders", service: "api", weight: 16, base: 31, charges: false, browser: false, hops: [] },
  { op: "orders.v1.OrdersService/CancelOrder", service: "api", weight: 3, base: 22, charges: false, browser: false, hops: [] },
  { op: "checkout.v1.CheckoutService/Checkout", service: "api", weight: 8, base: 95, charges: true, browser: true, hops: [{ service: "worker", op: "orders.created process", base: 28, share: 0.6 }, { service: "inventory", op: "inventory.v1.InventoryService/Reserve", base: 22, share: 0.6 }] },
  { op: "inventory.v1.InventoryService/GetStock", service: "inventory", weight: 18, base: 6, charges: false, browser: false, hops: [] },
  { op: "inventory.v1.InventoryService/Reserve", service: "inventory", weight: 6, base: 14, charges: false, browser: false, hops: [] },
  { op: "GET /products/{slug}", service: "api", weight: 10, base: 18, charges: false, browser: true, hops: [] },
  { op: "fulfilment.tick", service: "worker", weight: 3, base: 210, charges: false, browser: false, hops: [] },
] as const;
export type DemoRoute = (typeof DEMO_ROUTES)[number];
export const DEMO_ROUTE_WEIGHT = DEMO_ROUTES.reduce((a, r) => a + r.weight, 0);

/** The rpc methods shop serves (the RPC explorer, the catalog): every Connect route, by service. */
export const DEMO_RPC_METHODS = DEMO_ROUTES.filter((r) => r.op.includes("/") && !r.op.startsWith("GET ")).map((r) => {
  const [svc, method] = r.op.split("/");
  return { id: r.op, service: svc, method, process: r.service };
});

/** Log-normal latency spread (sigma of ln ms) and the outlier share every route has. */
export const LATENCY_SIGMA = 0.55;
export const OUTLIER_SHARE = 1 / 150;
export const OUTLIER_FACTOR = 22;

/** The one incident. */
export const DEMO_INCIDENT = {
  envId: "env-prod" as DemoEnvKey,
  service: "api" as const,
  release: "v1.8.3",
  /** The issue the tour opens (journeys/forge.tsx DEMO_ISSUE_ID). */
  issueId: "iss-orders-deadline",
  /** The issue's title and every log line / span status the failure writes. */
  error: "payments: charge failed: provider timeout after 3s",
  /** The retry's warning, before the deadline. */
  retryWarning: "payments: charge attempt 1 timed out after 1.5s, retrying",
  errorType: "PaymentTimeoutError",
  /** The one alert that fires (consoleScenario's shape declares it). */
  alert: "OrdersErrorRateHigh",
  /** Share of charging requests that fail, after v1.8.3 reached prod (before: none). */
  failShare: 0.124,
  /** Share of charging requests that retried and then succeeded (slow, not failed). */
  slowShare: 0.06,
  /** A failed charge's duration: the 3s deadline. */
  deadlineMs: 3_000,
  /** A retried-then-successful charge's extra latency. */
  retryMs: 1_500,
} as const;

/**
 * The trace the tour opens (journeys/forge.tsx repeats these ids; a test pins
 * them): a CreateOrder in prod on v1.8.3 whose charge timed out, then the
 * worker's hop into inventory.
 */
export const DEMO_TOURED_TRACE = {
  traceId: "4bf92f3577b34da6a3ce929d0e0e4736",
  /** inventory's server span (fg-hops); the api's method span, one SELECT, the worker's consumer (fg-inside). */
  spans: { inventory: "e1", method: "a2", query: "b1", worker: "d1" },
} as const;

/** The org the demo's reads are scoped by. */
export const DEMO_ORG = "org-acme";

/** When the incident started: prod's v1.8.3 promotion. */
export const incidentStartMs = (now: number = demoNow()) => now - RUNNING["env-prod"].age;

/** Whether a request on `route` in `envId` at `atMs` is in the incident's path. */
export const inIncident = (route: Pick<DemoRoute, "charges">, envId: string, atMs: number, now: number = demoNow()) =>
  route.charges && envId === DEMO_INCIDENT.envId && atMs >= incidentStartMs(now);

/** Expected requests per second on `route` in `envId` at `atMs`. */
export function demoRouteRate(route: DemoRoute, envId: DemoEnvKey, atMs: number): number {
  return (1000 / SLOT_MS) * dailyLoad(atMs) * ENV_TRAFFIC[envId] * (route.weight / DEMO_ROUTE_WEIGHT);
}

export interface DemoStats {
  /** Unrounded, for callers that split a window and must keep its total (demoTrend). */
  requestsExact?: number;
  errorsExact?: number;
  requests: number;
  errors: number;
  errorRate: number;
  ratePerSecond: number;
  p50Ms: number;
  p95Ms: number;
  p99Ms: number;
}

// The standard normal CDF (Abramowitz–Stegun 7.1.26 via erf).
function normCdf(x: number): number {
  const t = 1 / (1 + 0.3275911 * Math.abs(x / Math.SQRT2));
  const y = 1 - ((((1.061405429 * t - 1.453152027) * t + 1.421413741) * t - 0.284496736) * t + 0.254829592) * t * Math.exp(-(x * x) / 2);
  return x >= 0 ? (1 + y) / 2 : (1 - y) / 2;
}

/** A latency mixture: (weight, median ms, sigma) log-normal components. */
type Mixture = { w: number; median: number; sigma: number }[];

function quantileOf(mix: Mixture, q: number): number {
  const total = mix.reduce((a, c) => a + c.w, 0);
  if (total <= 0) return 0;
  const cdf = (x: number) => mix.reduce((a, c) => a + c.w * normCdf((Math.log(x) - Math.log(c.median)) / c.sigma), 0) / total;
  let lo = 0.01;
  let hi = 600_000;
  for (let i = 0; i < 60; i++) {
    const mid = Math.sqrt(lo * hi);
    if (cdf(mid) < q) lo = mid;
    else hi = mid;
  }
  return Math.round(Math.sqrt(lo * hi) * 10) / 10;
}

/** A request's latency components on `route` (its root span) with the incident on or off. */
function routeMixture(route: { base: number; charges?: boolean }, incident: boolean, w: number): Mixture {
  if (w <= 0) return [];
  const fail = incident ? DEMO_INCIDENT.failShare : 0;
  const slow = incident ? DEMO_INCIDENT.slowShare : 0;
  const ok = 1 - fail - slow;
  return [
    { w: w * ok * (1 - OUTLIER_SHARE), median: route.base, sigma: LATENCY_SIGMA },
    { w: w * ok * OUTLIER_SHARE, median: route.base * OUTLIER_FACTOR, sigma: 0.3 },
    ...(slow ? [{ w: w * slow, median: route.base + DEMO_INCIDENT.retryMs + 120, sigma: 0.08 }] : []),
    ...(fail ? [{ w: w * fail, median: DEMO_INCIDENT.deadlineMs + 20, sigma: 0.01 }] : []),
  ];
}

/** Integration steps for a window (every 5 minutes, at least 12). */
const stepsOf = (startMs: number, endMs: number) => Math.max(12, Math.ceil((endMs - startMs) / (5 * MINUTE)));

export interface DemoStatsQuery {
  startMs: number;
  endMs: number;
  /** Environments in scope; empty: all. */
  envIds?: readonly string[];
  /** A service's SERVER spans (api, worker, inventory); empty: every route's root. */
  service?: string;
  /** One root operation / rpc id (e.g. `orders.v1.OrdersService/CreateOrder`). */
  op?: string;
  now?: number;
}

/**
 * The expected RED figures for a window: requests, errors and latency
 * quantiles, analytically from the routes. Every aggregate in the demo is
 * this function's answer for its window.
 */
export function demoStats(q: DemoStatsQuery): DemoStats {
  return statsOf(q).stats;
}

/**
 * The window's latency as a histogram over `boundsSeconds` (the last bucket
 * is everything above the last bound): the service graph's and RPC
 * explorer's duration, from the same model as demoStats.
 */
export function demoHistogram(q: DemoStatsQuery, boundsSeconds: readonly number[]): { buckets: number[]; count: number; sumSeconds: number } {
  const { stats, mix } = statsOf(q);
  const total = mix.reduce((a, c) => a + c.w, 0);
  const cdf = (ms: number) => (total ? mix.reduce((a, c) => a + c.w * normCdf((Math.log(ms) - Math.log(c.median)) / c.sigma), 0) / total : 0);
  const edges = [0, ...boundsSeconds.map((b) => cdf(b * 1000)), 1];
  const buckets = edges.slice(1).map((e, i) => Math.round(stats.requests * (e - edges[i])));
  const meanMs = total ? mix.reduce((a, c) => a + c.w * c.median * Math.exp((c.sigma * c.sigma) / 2), 0) / total : 0;
  return { buckets, count: buckets.reduce((a, b) => a + b, 0), sumSeconds: (stats.requests * meanMs) / 1000 };
}

function statsOf(q: DemoStatsQuery): { stats: DemoStats; mix: Mixture } {
  const now = q.now ?? demoNow();
  const envs = (q.envIds?.length ? DEMO_ENV_IDS.filter((e) => q.envIds!.includes(e)) : [...DEMO_ENV_IDS]) as DemoEnvKey[];
  let requests = 0;
  let errors = 0;
  // Components with the same shape are merged, so a quantile stays cheap over any window.
  const merged = new Map<string, Mixture[number]>();
  const add = (parts: Mixture) => {
    for (const c of parts) {
      const key = `${c.median}|${c.sigma}`;
      const prev = merged.get(key);
      if (prev) prev.w += c.w;
      else merged.set(key, { ...c });
    }
  };
  // The window, split at the incident's start so neither side borrows the other's shape.
  const split = incidentStartMs(now);
  const ranges = q.startMs < split && split < q.endMs ? [[q.startMs, split], [split, q.endMs]] : [[q.startMs, q.endMs]];
  for (const [from, to] of ranges) {
    const steps = stepsOf(from, to);
    const dt = (to - from) / steps;
    for (let i = 0; i < steps; i++) {
      const at = from + (i + 0.5) * dt;
      for (const env of envs) {
        for (const route of DEMO_ROUTES) {
          const n = demoRouteRate(route, env, at) * (dt / 1000);
          const incident = inIncident(route, env, at, now);
          // The root span, when it is the service's (or no service was named).
          if ((!q.service || route.service === q.service) && (!q.op || route.op === q.op)) {
            requests += n;
            if (incident) errors += n * DEMO_INCIDENT.failShare;
            add(routeMixture(route, incident, n));
          }
          // The downstream hops it makes into other services.
          for (const hop of route.hops) {
            if (q.service && hop.service === q.service && (!q.op || hop.op === q.op)) {
              // A failed order is still recorded (payment_failed) and its event published: the hop happens either way.
              const h = n * hop.share;
              requests += h;
              add(routeMixture(hop, false, h));
            }
          }
        }
      }
    }
  }
  const mix = [...merged.values()];
  const seconds = (q.endMs - q.startMs) / 1000;
  const stats = {
    requests: Math.round(requests),
    errors: Math.round(errors),
    requestsExact: requests,
    errorsExact: errors,
    errorRate: requests ? errors / requests : 0,
    ratePerSecond: seconds > 0 ? requests / seconds : 0,
    p50Ms: quantileOf(mix, 0.5),
    p95Ms: quantileOf(mix, 0.95),
    p99Ms: quantileOf(mix, 0.99),
  };
  return { stats, mix };
}

/**
 * `demoStats` per bucket: a trend of `buckets` points over the window. Counts
 * are rounded cumulatively, so the buckets always sum to the window's own
 * (rounded) total, however few requests each holds.
 */
export function demoTrend(q: DemoStatsQuery, buckets = 30): { startMs: number; stats: DemoStats }[] {
  const step = (q.endMs - q.startMs) / buckets;
  let requests = 0;
  let errors = 0;
  let shownRequests = 0;
  let shownErrors = 0;
  return Array.from({ length: buckets }, (_, i) => {
    const startMs = q.startMs + i * step;
    const stats = demoStats({ ...q, startMs, endMs: startMs + step });
    requests += stats.requestsExact ?? stats.requests;
    errors += stats.errorsExact ?? stats.errors;
    const r = Math.round(requests) - shownRequests;
    const e = Math.round(errors) - shownErrors;
    shownRequests += r;
    shownErrors += e;
    return { startMs, stats: { ...stats, requests: r, errors: e, errorRate: r ? e / r : 0 } };
  });
}

// ── The shop's database ──────────────────────────────────────────────────────

/** The `shop` database's tables (Data browser, dashboard Postgres panels). Columns: name, Postgres type. */
export const DEMO_DB_TABLES = [
  { name: "customers", columns: [["id", "text"], ["email", "text"], ["name", "text"], ["country", "text"], ["identity_subject", "text"], ["created_at", "timestamp with time zone"]] },
  { name: "products", columns: [["id", "text"], ["sku", "text"], ["name", "text"], ["price_cents", "integer"], ["active", "boolean"], ["created_at", "timestamp with time zone"]] },
  { name: "stock", columns: [["sku", "text"], ["on_hand", "integer"], ["reserved", "integer"], ["updated_at", "timestamp with time zone"]] },
  { name: "orders", columns: [["id", "text"], ["customer_id", "text"], ["state", "text"], ["total_cents", "integer"], ["currency", "text"], ["created_at", "timestamp with time zone"], ["paid_at", "timestamp with time zone"]] },
  { name: "order_items", columns: [["id", "bigint"], ["order_id", "text"], ["sku", "text"], ["quantity", "integer"], ["unit_price_cents", "integer"]] },
] as const;

/** An order's states, in the order an order moves through them. `payment_failed` is the incident's. */
export const DEMO_ORDER_STATES = ["pending", "paid", "payment_failed", "shipped", "delivered", "cancelled"] as const;

/** shop's catalogue: real-looking products, one SKU each. */
export const DEMO_PRODUCTS = [
  { sku: "TOTE-LIN-NAT", name: "Linen tote bag, natural", price: 3400 },
  { sku: "MUG-CER-SAGE", name: "Stoneware mug, sage", price: 2200 },
  { sku: "NB-A5-DOT", name: "A5 dot-grid notebook", price: 1600 },
  { sku: "TEE-ORG-M-INK", name: "Organic cotton tee, ink, M", price: 2900 },
  { sku: "TEE-ORG-L-INK", name: "Organic cotton tee, ink, L", price: 2900 },
  { sku: "GRND-HAND-01", name: "Hand coffee grinder", price: 8900 },
  { sku: "JUG-MILK-350", name: "Milk jug, 350 ml", price: 2400 },
  { sku: "KETL-GOOSE-BLK", name: "Gooseneck kettle, black", price: 6900 },
  { sku: "CAN-SOY-FIG", name: "Soy candle, fig", price: 1800 },
  { sku: "BLNK-WOOL-GRY", name: "Wool throw blanket, grey", price: 11900 },
  { sku: "PEN-BRS-01", name: "Brass ballpoint pen", price: 4500 },
  { sku: "TRAY-OAK-SM", name: "Oak serving tray, small", price: 3900 },
] as const;
