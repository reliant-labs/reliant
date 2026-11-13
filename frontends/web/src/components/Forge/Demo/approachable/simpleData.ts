// Copyright (c) 2026 Reliant Labs

/**
 * The simple view's figures, in plain words, from the SAME story every other
 * demo page reads (demoData/shopStory.ts and shop/*): so "1 thing broke" here
 * is the one open issue on Errors, and "checkout takes 3s for 1 in 20" is the
 * p95 the RPC explorer shows. Nothing here invents a number.
 */

import { useQuery } from "@tanstack/react-query";

import type { Issue } from "@/services/controlPlane/observability/issuesTypes";

import { demoActivity, CHANNELS } from "../demoData/shop/audienceActivity";
import { shopIssues } from "../demoData/shop/issues";
import {
  DAY,
  DEMO_INCIDENT,
  DEMO_ORG,
  DEMO_RELEASES,
  DEMO_ROUTES,
  demoNow,
  demoStats,
  HISTORY,
  HOUR,
  RUNNING,
  type DemoEnvKey,
  type DemoRelease,
} from "../demoData/shopStory";

export const PROD: DemoEnvKey = "env-prod";
export const ENV_LABEL: Record<DemoEnvKey, string> = { "env-dev": "Development", "env-staging": "Testing (staging)", "env-prod": "Customers (production)" };
export const ENV_SHORT: Record<DemoEnvKey, string> = { "env-dev": "Development", "env-staging": "Testing", "env-prod": "Production" };

// ── Things that broke ───────────────────────────────────────────────────────

/** What each of the shop's issues means, for someone who never read a stack trace. */
const PLAIN_ISSUES: Record<string, { title: string; why: string }> = {
  [DEMO_INCIDENT.issueId]: {
    title: "Some checkouts fail: the payment provider answers too slowly",
    why: "Since the latest release, a slow payment is retried, and the retry runs past checkout's 3-second time limit.",
  },
  "iss-inventory-lock": {
    title: "Reserving stock sometimes failed when two orders hit the same item",
    why: "Two orders tried to reserve the same product at the same moment.",
  },
  "iss-confirmation-status": {
    title: "The order confirmation page crashed for some customers",
    why: "The page expected an order status that older orders did not have.",
  },
  "iss-dev-deprecated": {
    title: "An old field is still used in development",
    why: "A warning only developers see; it does not affect customers.",
  },
};

export const plainIssue = (issue: Pick<Issue, "id" | "title">) => PLAIN_ISSUES[issue.id] ?? { title: issue.title, why: "" };

async function listIssues(statuses: Issue["status"][]) {
  const now = demoNow();
  const api = shopIssues(now);
  const res = await api.listIssues({
    orgId: DEMO_ORG,
    envIds: [],
    statuses: statuses.filter((s): s is NonNullable<typeof s> => !!s),
    window: { startMs: now - 14 * DAY, endMs: now },
    sort: "last",
  } as never);
  return res.issues;
}

export function useShopIssues() {
  const open = useQuery({ queryKey: ["simple-demo", "issues", "open"], queryFn: () => listIssues(["new", "unresolved", "regressed"]) });
  const fixed = useQuery({ queryKey: ["simple-demo", "issues", "resolved"], queryFn: () => listIssues(["resolved"]) });
  return { open: open.data ?? null, fixed: fixed.data ?? null };
}

// ── Slow requests ───────────────────────────────────────────────────────────

/** Every request the shop serves, in the words a shopper would use. */
const PLAIN_ROUTES: Record<string, string> = {
  "orders.v1.OrdersService/CreateOrder": "Placing an order",
  "checkout.v1.CheckoutService/Checkout": "Checking out",
  "orders.v1.OrdersService/ListOrders": "The \"Your orders\" page",
  "orders.v1.OrdersService/GetOrder": "Opening one order",
  "orders.v1.OrdersService/CancelOrder": "Cancelling an order",
  "inventory.v1.InventoryService/GetStock": "Checking what's in stock",
  "inventory.v1.InventoryService/Reserve": "Setting stock aside for an order",
  "GET /products/{slug}": "A product page",
  "fulfilment.tick": "Shipping orders (in the background)",
};

export interface RouteSpeed {
  op: string;
  name: string;
  service: string;
  typicalMs: number;
  slowMs: number;
  slowYesterdayMs: number;
  requests: number;
  errorRate: number;
}

export function routeSpeeds(now: number = demoNow()): RouteSpeed[] {
  const window = { startMs: now - HOUR, endMs: now };
  const yesterday = { startMs: now - DAY - HOUR, endMs: now - DAY };
  return DEMO_ROUTES.map((route) => {
    const s = demoStats({ ...window, envIds: [PROD], op: route.op, now });
    const y = demoStats({ ...yesterday, envIds: [PROD], op: route.op, now });
    return { op: route.op, name: PLAIN_ROUTES[route.op] ?? route.op, service: route.service, typicalMs: s.p50Ms, slowMs: s.p95Ms, slowYesterdayMs: y.p95Ms, requests: s.requests, errorRate: s.errorRate };
  }).sort((a, b) => b.slowMs - a.slowMs);
}

export function overallSpeed(now: number = demoNow()) {
  const s = demoStats({ startMs: now - HOUR, endMs: now, envIds: [PROD], now });
  const y = demoStats({ startMs: now - DAY - HOUR, endMs: now - DAY, envIds: [PROD], now });
  return { typicalMs: s.p50Ms, slowMs: s.p95Ms, slowYesterdayMs: y.p95Ms, requests: s.requests, errorRate: s.errorRate };
}

/** The shop's production telemetry, as the console's own pages read it (Observability/ObservabilityPanel). */
export const SHOP_PROD_SCOPE = { orgId: DEMO_ORG, projectId: "shop", envIds: [PROD], filters: [] };

/**
 * One step of a request (a span), in words: "SELECT customers" → "Look up the
 * customer". The raw name always shows beside it; this only says what it is.
 */
export function plainStep(span: { name: string; service: string; kind: string }, isRoot: boolean): string {
  const name = span.name;
  if (isRoot && PLAIN_ROUTES[name]) return PLAIN_ROUTES[name];
  if (name.startsWith("HTTP ") || (span.kind === "client" && span.service === "shop-web")) return "The customer's browser waits for an answer";
  if (name.startsWith("POST https://") && name.includes("charges")) return "Ask the payment provider to take the money";
  const known: Record<string, string> = {
    "SELECT customers": "Look up the customer",
    "INSERT orders": "Save the order",
    "SELECT orders": "Read the customer's orders",
    "SELECT products": "Look up the product",
    "SELECT stock": "Check what's in stock",
    "UPDATE stock": "Set the stock aside",
    "payments.Charge": "Charge the card",
    "nats publish orders.created": "Hand the order to the background worker",
    "orders.created process": "The background worker picks up the order",
    "orders.CreateOrder": "Run the order code",
    "inventory.Reserve": "Run the stock code",
    "inventory.v1.InventoryService/Reserve": "Reserve the items in stock",
  };
  return known[name] ?? PLAIN_ROUTES[name] ?? name;
}

/** A shop page, as a person would name it: "/checkout" → "the checkout page". */
export function plainPath(path: string): string {
  if (!path || path === "/") return "the home page";
  if (path.startsWith("/products/")) return `a product page (${path.slice("/products/".length).replace(/-/g, " ")})`;
  const known: Record<string, string> = { "/checkout": "the checkout page", "/cart": "their cart", "/orders": "“Your orders”", "/account": "their account", "/search": "search" };
  return known[path] ?? path;
}

/** "2:17" for a duration in ms. */
export function clock(ms: number): string {
  const total = Math.max(0, Math.round(ms / 1000));
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, "0")}`;
}

// ── Alerts ──────────────────────────────────────────────────────────────────

/** What each of the shop's alert rules watches, in a sentence, and who it tells. */
export const PLAIN_ALERTS: Record<string, { watches: string; tells: string }> = {
  OrdersErrorRateHigh: { watches: "More than 1 in 20 orders fail for 5 minutes in a row", tells: "Wakes the on-call person (Slack)" },
  ListOrdersSlow: { watches: "The “Your orders” page takes over half a second for 1 in 20 visits, for 10 minutes", tells: "Opens a ticket on GitHub" },
  WorkerBacklog: { watches: "More than 500 orders are waiting for the background worker, for 10 minutes", tells: "Opens a ticket on GitHub" },
};

// ── Releases ────────────────────────────────────────────────────────────────

/** "feat(orders): retry the payment charge once on a timeout" → "Retry the payment charge once on a timeout". */
export function plainSubject(subject: string): string {
  const text = subject.replace(/^[a-z]+(\([^)]*\))?!?:\s*/i, "");
  return text.charAt(0).toUpperCase() + text.slice(1);
}

export interface ReleaseStop {
  env: DemoEnvKey;
  version: string;
  ageMs: number;
  by: string;
  check: "passed" | "failed" | "none";
  checkSummary: string;
}

export function runningNow(): ReleaseStop[] {
  return (["env-dev", "env-staging", "env-prod"] as DemoEnvKey[]).map((env) => {
    const p = RUNNING[env];
    const health = p.health ?? (env === "env-dev" ? "none" : { status: "passed" as const, summary: "" });
    return {
      env,
      version: p.version,
      ageMs: p.age,
      by: p.by,
      check: health === "none" ? "none" : health.status,
      checkSummary: health === "none" ? "" : health.summary,
    };
  });
}

/** Where each release runs now, and which environments it has passed through. */
export function releaseRows(): { release: DemoRelease; runningIn: DemoEnvKey[]; reached: DemoEnvKey[] }[] {
  const envs = ["env-dev", "env-staging", "env-prod"] as DemoEnvKey[];
  return DEMO_RELEASES.map((release) => ({
    release,
    runningIn: envs.filter((env) => RUNNING[env].version === release.version),
    reached: envs.filter((env) => RUNNING[env].version === release.version || HISTORY[env]?.some((p) => p.version === release.version)),
  }));
}

/** The incident's check, in plain numbers: errors and slow checkouts before → after. */
export const INCIDENT_CHECK = { errorsBefore: "0%", errorsAfter: `${(DEMO_INCIDENT.failShare * 100).toFixed(1)}%`, slowBefore: "0.1s", slowAfter: "3.0s" };

// ── People ──────────────────────────────────────────────────────────────────

export interface PeopleWeek {
  active: number;
  fresh: number;
  returning: number;
  activeLastWeek: number;
  /** Where this week's new people came from, by sign-ups, largest first. */
  sources: { channel: string; people: number }[];
}

export function peopleThisWeek(now: number = demoNow()): PeopleWeek {
  const from = now - 7 * DAY;
  const { people, sessions } = demoActivity(PROD, now, from - 8 * DAY);
  const within = (start: number, a: number, b: number) => start >= a && start < b;
  const freshPeople = people.filter((p) => p.signedUp && within(p.createdAtMs, from, now));
  const fresh = new Set(freshPeople.map((p) => p.index));
  const active = new Set(sessions.filter((s) => within(s.start, from, now)).map((s) => s.account));
  const activeLastWeek = new Set(sessions.filter((s) => within(s.start, from - 7 * DAY, from)).map((s) => s.account)).size;
  const returning = [...active].filter((a) => !fresh.has(a)).length;
  const byChannel = new Map<string, number>();
  for (const p of freshPeople) byChannel.set(p.channel, (byChannel.get(p.channel) ?? 0) + 1);
  const sources = [...byChannel.entries()]
    .map(([channel, n]) => ({ channel: CHANNELS.includes(channel as never) ? channel : "Unknown", people: n }))
    .sort((a, b) => b.people - a.people);
  return { active: active.size, fresh: fresh.size, returning, activeLastWeek, sources };
}

// ── Checkout (one dashboard, simplified) ────────────────────────────────────

export interface CheckoutHour {
  startMs: number;
  orders: number;
  failed: number;
}

const ORDER_OPS = ["orders.v1.OrdersService/CreateOrder"];

export function checkoutByHour(now: number = demoNow(), hours = 24): CheckoutHour[] {
  const end = Math.floor(now / HOUR) * HOUR + HOUR;
  return Array.from({ length: hours }, (_, i) => {
    const startMs = end - (hours - i) * HOUR;
    const endMs = Math.min(startMs + HOUR, now);
    let orders = 0;
    let failed = 0;
    for (const op of ORDER_OPS) {
      const s = demoStats({ startMs, endMs, envIds: [PROD], op, now });
      orders += s.requests;
      failed += s.errors;
    }
    return { startMs, orders, failed };
  });
}
