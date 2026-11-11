// Copyright (c) 2026 Reliant Labs

/**
 * The built-in product analytics dashboards' answers: the Overview's Traffic
 * and Users sections, and the Users, Product and Acquisition dashboards
 * (forge #2075, pkg/obsbuiltin; their specs are in forge-dashboards.json).
 * Every panel there is a sql panel over product_events; the demo answers each
 * BY ITS ID, from the one audience (audience.ts), with the definitions its
 * statement computes, and in the shape control-plane folds a ClickHouse
 * result into (rpc_query_panel_sql.go foldSQLRows):
 *
 *   figures      one row, no text column: `columns` name the figures.
 *   tables       text columns are each row's `group`, numbers its `values`
 *                (NULL is null, as a nullIf or an empty side of a FULL JOIN
 *                leaves it).
 *   time series  a first Date/DateTime column: one series per numeric column
 *                and group of the text columns, bucketed by day (UTC) or by
 *                the statement's own interval.
 *
 * So the dashboards agree with each other as the real ones do (the design's
 * consistency rules, pinned by __tests__/productAnalytics.test.ts): a user is
 * an account active in the window, a new user is a sign-up in it, returning
 * is the rest; sign-ups split by channel, by method and by country all sum to
 * the new users; a table's rows sum to the figure above it.
 */

import type { PanelResult, QueryPanelRequest, Series } from "@/services/controlPlane/observability/types";

import { DAY, DEMO_ENV_IDS, HOUR, MINUTE, type DemoEnvKey } from "../shopStory";
import {
  allocate,
  BROWSERS,
  CHANNEL_TOUCHES,
  CHANNELS,
  channelWeights,
  COUNTRIES,
  dayShare,
  dayStart,
  DEMO_FLAG,
  demoActivity,
  DEVICES,
  hash01,
  OPERATING_SYSTEMS,
  ROUTES,
  UNATTRIBUTED,
  VIEWS_PER_VISIT,
  VISIT_SECONDS,
  visitorMix,
  visitorsOn,
  VISITS_PER_VISITOR,
  weekStart,
  type DemoPerson as DemoAccount,
  type Session,
  type Touch,
} from "./audienceActivity";

/** The dashboards whose sql panels this module answers. */
export const PRODUCT_ANALYTICS_DASHBOARDS: ReadonlySet<string> = new Set(["overview", "users", "product", "acquisition"]);

type Row = { group: Record<string, string>; values: (number | null)[] };
interface Answer {
  columns: string[];
  rows?: Row[];
  series?: Series[];
  bucketSeconds?: number;
  bucketTimezone?: string;
}

interface Q {
  envs: DemoEnvKey[];
  from: number;
  to: number;
  now: number;
  stepSeconds: number;
}

const ENV_NAMES: Record<string, DemoEnvKey> = { dev: "env-dev", staging: "env-staging", prod: "env-prod" };
const round = (v: number, digits: number) => Math.round(v * 10 ** digits) / 10 ** digits;
const pct = (n: number, d: number, digits: number) => (d > 0 ? round((100 * n) / d, digits) : null);
const share = (n: number, d: number) => (d > 0 ? round(n / d, 4) : null);
const sec = (ms: number) => Math.floor(ms / 1000);
const utc = (ms: number) => new Date(ms).toISOString().slice(0, 16).replace("T", " ");

const figures = (...cols: [string, number | null][]): Answer => ({ columns: cols.map(([c]) => c), rows: [{ group: {}, values: cols.map(([, v]) => v) }] });

/** A time series: one series per (value column × group), its points in time order, as the fold builds them. */
function series(points: { at: number; group?: Record<string, string>; values: Record<string, number> }[], bucketSeconds: number, timezone = ""): Answer {
  const byKey = new Map<string, Series>();
  const columns: string[] = [];
  for (const p of points) {
    for (const [label, value] of Object.entries(p.values)) {
      if (!columns.includes(label)) columns.push(label);
      const group = p.group ?? {};
      const key = `${label}|${JSON.stringify(group)}`;
      let s = byKey.get(key);
      if (!s) byKey.set(key, (s = { label, group, timestamps: [], values: [], value: null }));
      s.timestamps.push(sec(p.at));
      s.values.push(value);
    }
  }
  return { columns, series: [...byKey.values()], bucketSeconds, bucketTimezone: timezone || undefined };
}

/** The days (UTC midnights) whose dates fall in [from, to). */
function daysOf(from: number, to: number): number[] {
  const out: number[] = [];
  for (let d = dayStart(from); d < to; d += DAY) out.push(d);
  return out;
}

// ── Users ────────────────────────────────────────────────────────────────────

interface EnvUsers {
  env: DemoEnvKey;
  accounts: DemoAccount[];
  sessions: Session[];
}
const usersOf = (q: Pick<Q, "envs" | "now">): EnvUsers[] =>
  q.envs.map((env) => {
    const { people, sessions } = demoActivity(env, q.now);
    return { env, accounts: people, sessions };
  });

/** Sessions that began (signed in, or sent the day's heartbeat) in [from, to): what makes a user active there. */
const within = (s: Session, from: number, to: number) => s.start >= from && s.start < to;

/** Distinct (env, user) active in [from, to): any signed-in product event. */
function active(us: EnvUsers[], from: number, to: number): number {
  let n = 0;
  for (const u of us) n += new Set(u.sessions.filter((s) => within(s, from, to)).map((s) => s.account)).size;
  return n;
}
/** The accounts that signed up in [from, to) (an invitation not yet accepted has not). */
const signups = (us: EnvUsers[], from: number, to: number) => us.flatMap((u) => u.accounts.filter((a) => a.signedUp && a.createdAtMs >= from && a.createdAtMs < to));
const accountsAt = (us: EnvUsers[], at: number) => us.reduce((n, u) => n + u.accounts.filter((a) => a.createdAtMs < at).length, 0);
/** Whether a sign-up placed its first order within 7 days (and before `to`). */
const activated = (a: DemoAccount, to: number) => a.activationAtMs !== null && a.activationAtMs < Math.min(to + 7 * DAY, a.createdAtMs + 7 * DAY) && a.activationAtMs - a.createdAtMs < 7 * DAY;
/** Each env's users active on each day, as day → accounts. Sessions before `to` only. */
function userDays(u: EnvUsers, from: number, to: number): Map<number, Set<number>> {
  const out = new Map<number, Set<number>>();
  for (const s of u.sessions) {
    if (!within(s, from, to)) continue;
    const d = dayStart(s.start);
    if (!out.has(d)) out.set(d, new Set());
    out.get(d)!.add(s.account);
  }
  return out;
}
const unionSize = (sets: (Set<number> | undefined)[]) => {
  const all = new Set<number>();
  for (const s of sets) s?.forEach((x) => all.add(x));
  return all.size;
};

function returning(us: EnvUsers[], from: number, to: number): number {
  let n = 0;
  for (const u of us) {
    const fresh = new Set(u.accounts.filter((a) => a.signedUp && a.createdAtMs >= from && a.createdAtMs < to).map((a) => a.index));
    n += new Set(u.sessions.filter((s) => within(s, from, to) && !fresh.has(s.account)).map((s) => s.account)).size;
  }
  return n;
}

function dauWauMau(us: EnvUsers[], from: number, to: number) {
  const maps = us.map((u) => userDays(u, dayStart(from) - 29 * DAY, to));
  return daysOf(from, to).map((day) => {
    const range = (n: number) => Array.from({ length: n }, (_, k) => day - k * DAY);
    const sum = (n: number) => maps.reduce((acc, m) => acc + unionSize(range(n).map((d) => m.get(d))), 0);
    return { day, dau: sum(1), wau: sum(7), mau: sum(30) };
  });
}

const BUCKETS = ["< 1 hour", "1-24 hours", "1-3 days", "3-7 days", "> 7 days", "Not yet"] as const;
function activationBucket(a: DemoAccount, now: number): (typeof BUCKETS)[number] {
  if (a.activationAtMs === null || a.activationAtMs > now) return "Not yet";
  const s = (a.activationAtMs - a.createdAtMs) / 1000;
  return s < 3600 ? "< 1 hour" : s < 86400 ? "1-24 hours" : s < 3 * 86400 ? "1-3 days" : s < 7 * 86400 ? "3-7 days" : "> 7 days";
}

const PROVIDER = (a: DemoAccount) => (a.social ? `forge/${a.social}` : "forge");
const countBy = <T,>(items: T[], key: (t: T) => string) => {
  const m = new Map<string, T[]>();
  for (const t of items) {
    const k = key(t);
    if (!m.has(k)) m.set(k, []);
    m.get(k)!.push(t);
  }
  return m;
};

// ── Traffic ──────────────────────────────────────────────────────────────────

interface Traffic {
  visitors: number;
  visits: number;
  views: number;
  /** Visitor-days by CHANNELS order (fractional). */
  byChannel: number[];
  fractional: number;
}
function traffic(q: Pick<Q, "envs" | "from" | "to">): Traffic {
  const mix = visitorMix(q.envs, q.from, q.to);
  const visits = Math.round(mix.visitors * VISITS_PER_VISITOR);
  return { visitors: Math.round(mix.visitors), visits, views: Math.round(visits * VIEWS_PER_VISIT), byChannel: mix.byChannel, fractional: mix.visitors };
}

/** Bounce rate: the visits entered on each page, at that page's bounce rate. */
function bounces(visits: number): { entries: number[]; bounced: number } {
  const entries = allocate(visits, ROUTES.map((r) => r.entries));
  return { entries, bounced: entries.reduce((a, n, i) => a + n * ROUTES[i].bounce, 0) };
}

/** The window's orders, purchases and the checkout funnel they imply (begin_checkout → purchase is 71% when every charge succeeds). */
function commerce(q: Q, us = usersOf(q)) {
  const orders: Session[] = [];
  let purchasers = 0;
  let orderers = 0;
  for (const u of us) {
    const mine = u.sessions.filter((s) => s.orderAt >= q.from && s.orderAt < q.to);
    orders.push(...mine);
    orderers += new Set(mine.map((s) => s.account)).size;
    purchasers += new Set(mine.filter((s) => s.purchased).map((s) => s.account)).size;
  }
  const checkout = Math.round(orderers / 0.71);
  // Anyone adds to a cart; only a signed-in shopper checks out: ≈1 in 5.5 carts reaches checkout.
  const cart = Math.round(checkout / 0.18);
  return { orders: orders.length, purchases: orders.filter((s) => s.purchased).length, orderers, purchasers, checkout, cart, lastOrder: Math.max(0, ...orders.map((s) => s.orderAt)) };
}

/** Per route: views, visitors, signed-in users, entries and exits over the window. */
function pages(q: Q) {
  const t = traffic(q);
  const us = usersOf(q);
  const c = commerce(q, us);
  const users = active(us, q.from, q.to);
  const fresh = signups(us, q.from, q.to).length;
  const reach: Record<string, number> = {};
  for (const r of ROUTES) reach[r.route] = Math.round(t.fractional * r.reach);
  reach["/cart"] = Math.max(Math.round(c.cart * 1.15), Math.min(reach["/cart"], c.cart));
  reach["/checkout"] = Math.round(c.checkout * 1.05);
  const signedShare: Record<string, number> = { "/products/:id": 0.55, "/": 0.48, "/collections/:id": 0.22, "/orders/:id": 0.3, "/account": 0.28, "/orders": 0.18, "/auth/sign-in": 0.22 };
  const signed = (route: string) => {
    if (route === "/checkout") return Math.min(users, Math.round(c.checkout * 0.92));
    if (route === "/cart") return Math.min(users, Math.round(reach["/cart"] * 0.45));
    if (route === "/auth/sign-up") return Math.round(fresh * 0.62);
    return Math.round(users * (signedShare[route] ?? 0.1));
  };
  // Views follow each route's visitors, several views each.
  const perVisitor: Record<string, number> = { "/products/:id": 1.9, "/": 1.3, "/collections/:id": 1.6, "/cart": 1.5, "/checkout": 1.4, "/orders/:id": 1.3, "/account": 1.4, "/orders": 1.5, "/auth/sign-in": 1.2, "/auth/sign-up": 1.3 };
  const views = allocate(t.views, ROUTES.map((r) => reach[r.route] * perVisitor[r.route]));
  const { entries } = bounces(t.visits);
  // Every visit ends on one page: the visits shared out by each route's views at its exit rate.
  const exits = allocate(t.visits, ROUTES.map((r, i) => views[i] * r.exit));
  return {
    t,
    c,
    rows: ROUTES.map((r, i) => {
      const visitors = Math.min(reach[r.route], views[i]);
      return { route: r.route, views: views[i], visitors, signed: Math.min(visitors, signed(r.route)), entries: entries[i], exits: exits[i], bounce: r.bounce };
    }),
  };
}

/** Channel → the window's visitors (whole numbers summing to the visitors figure). */
function channelVisitors(t: Traffic): Map<string, number> {
  const n = allocate(t.visitors, t.byChannel);
  return new Map(CHANNELS.map((c, i) => [c, n[i]]));
}

const CHANNEL_BOUNCE: Record<string, number> = { Direct: 0.37, "Organic search": 0.42, "Paid search": 0.45, Email: 0.33, "Organic social": 0.58, Referral: 0.4, "AI assistants": 0.3, "Paid social": 0.62 };

/**
 * A breakdown of the window's visitors and its sign-ups by one dimension
 * (the Acquisition tables): visitors split by `weights`, sign-ups counted
 * from the accounts by `keyOf`. A sign-up with no same-day visit is
 * "Unattributed", a row with no visitors.
 */
function breakdown(q: Q, weights: readonly (readonly [string, number])[], keyOf: (a: DemoAccount) => string, limit = 50) {
  const t = traffic(q);
  const v = allocate(t.visitors, weights.map(([, w]) => w));
  const us = usersOf(q);
  const su = signups(us, q.from, q.to);
  const byKey = countBy(su, (a) => (a.channel === UNATTRIBUTED ? UNATTRIBUTED : keyOf(a)));
  const keys = [...weights.map(([k]) => k), ...[...byKey.keys()].filter((k) => !weights.some(([w]) => w === k))];
  return keys
    .map((k) => {
      const visitors = v[weights.findIndex(([w]) => w === k)] ?? 0;
      const s = byKey.get(k) ?? [];
      return { key: k, visitors, signups: s.length, activated: s.filter((a) => activated(a, q.now)).length };
    })
    .filter((r) => r.visitors > 0 || r.signups > 0)
    .sort((a, b) => b.visitors - a.visitors || a.key.localeCompare(b.key))
    .slice(0, limit);
}

/** The touches (source, medium, campaign …) of the window's visitors, each channel's split by its touches' shares. */
function touches(t: Traffic): { touch: Touch; channel: string; visitors: number }[] {
  const byChannel = channelVisitors(t);
  return CHANNELS.flatMap((channel) => {
    const list = CHANNEL_TOUCHES[channel];
    const n = allocate(byChannel.get(channel) ?? 0, list.map((x) => x.share));
    return list.map((touch, i) => ({ touch, channel, visitors: n[i] }));
  });
}

// ── Events ───────────────────────────────────────────────────────────────────

/** Every product event name the window holds: count, persons, last seen. The `events` figure is their sum. */
function eventsByName(q: Q) {
  const p = pages(q);
  const us = usersOf(q);
  const c = p.c;
  const sessions = us.flatMap((u) => u.sessions.filter((s) => within(s, q.from, q.to)));
  const users = active(us, q.from, q.to);
  const su = signups(us, q.from, q.to);
  const productViewers = p.rows.find((r) => r.route === "/products/:id")!.visitors;
  const last = (at: number) => utc(Math.min(at, q.to - 1));
  const lastSession = Math.max(q.from, ...sessions.map((s) => Math.min(s.end, q.to - 1)));
  const flagPersons = Math.round(c.checkout * 1.0);
  const rows = [
    { name: "page_viewed", events: p.t.views, persons: p.t.visitors, at: q.to - 4_000 },
    { name: "view_product", events: Math.round(productViewers * 2.1), persons: productViewers, at: q.to - 9_000 },
    { name: "auth_signed_in", events: sessions.length, persons: users, at: lastSession },
    { name: "add_to_cart", events: Math.round(c.cart * 1.4), persons: c.cart, at: q.to - 4 * MINUTE },
    { name: "flag_evaluated", events: flagPersons * 2, persons: flagPersons, at: q.to - 7 * MINUTE },
    { name: "begin_checkout", events: Math.round(c.checkout * 1.15), persons: c.checkout, at: q.to - 11 * MINUTE },
    { name: "order_created", events: c.orders, persons: c.orderers, at: c.lastOrder },
    { name: "purchase", events: c.purchases, persons: c.purchasers, at: c.lastOrder },
    { name: "auth_signed_up", events: su.length, persons: su.length, at: Math.max(0, ...su.map((a) => a.createdAtMs)) },
    { name: "customer_created", events: su.length, persons: su.length, at: Math.max(0, ...su.map((a) => a.createdAtMs)) },
    { name: "customer_updated", events: Math.round(users * 0.05), persons: Math.round(users * 0.05), at: lastSession - 25 * MINUTE },
    { name: "auth_signed_out", events: sessions.filter((s) => s.signOut).length, persons: new Set(sessions.filter((s) => s.signOut).map((s) => s.account)).size, at: lastSession - 3 * MINUTE },
  ];
  return rows.filter((r) => r.events > 0).map((r) => ({ ...r, lastSeen: last(r.at || q.from) })).sort((a, b) => b.events - a.events || a.name.localeCompare(b.name));
}

/** The CRUD writes per entity: orders are created at checkout and updated twice (shipped, delivered); customers at sign-up. */
function crud(q: Q) {
  const us = usersOf(q);
  const out: { entity: string; kind: "created" | "updated" | "deleted"; at: number }[] = [];
  for (const u of us) {
    for (const s of u.sessions) {
      if (s.orderAt) {
        for (const [kind, at] of [["created", s.orderAt], ["updated", s.orderAt + 2 * HOUR], ["updated", s.orderAt + 1.6 * DAY]] as const) {
          if (kind === "updated" && !s.purchased) continue;
          if (at >= q.from && at < q.to) out.push({ entity: "order", kind, at });
        }
      }
      if (within(s, q.from, q.to) && hash01(s.account, Math.round(s.start / DAY), 71) < 0.05) out.push({ entity: "customer", kind: "updated", at: Math.max(q.from, s.start) });
    }
    for (const a of u.accounts) if (a.createdAtMs >= q.from && a.createdAtMs < q.to) out.push({ entity: "customer", kind: "created", at: a.createdAtMs });
  }
  return out;
}

// ── Answers ──────────────────────────────────────────────────────────────────

/**
 * A weekly series' bucket (toMonday): 7 days, the panel's minStep. The
 * console captions a chart's bucket from its minStep (SeriesChart
 * panelBucketSeconds), so it reads "7-day buckets" whatever the server infers
 * from the Date column; the demo answers the bucket the statement grouped by.
 */
const WEEK_BUCKET = 7 * 86400;

const TRAFFIC_ROUTE = ["/products/:id", "/", "/collections/:id", "/cart", "/checkout", "/orders/:id", "/account", "/orders", "/auth/sign-in", "/auth/sign-up"];

function visitorsTrend(q: Q, hourly: boolean): Answer {
  const step = hourly ? HOUR : DAY;
  const points = [];
  for (let at = Math.floor(q.from / step) * step; at < q.to; at += step) {
    const v = visitorMix(q.envs, Math.max(at, q.from), Math.min(at + step, q.to)).visitors;
    if (v > 0) points.push({ at, values: { Visitors: Math.round(v) } });
  }
  return series(points, hourly ? 3600 : 86400, hourly ? "" : "UTC");
}

function perDay(q: Q, label: string, value: (from: number, to: number) => number): Answer {
  return series(
    daysOf(q.from, q.to).map((day) => ({ at: day, values: { [label]: value(Math.max(day, q.from), Math.min(day + DAY, q.to)) } })),
    86400,
    "UTC"
  );
}

function sevenDays(q: Q, label: string, f: (us: EnvUsers[], from: number, to: number) => number): Answer {
  const us = usersOf(q);
  return figures([label, f(us, q.to - 7 * DAY, q.to)], ["Previous 7 days", f(us, q.to - 14 * DAY, q.to - 7 * DAY)]);
}

function trafficFigures(q: Q) {
  const t = traffic(q);
  const { bounced } = bounces(t.visits);
  return { t, bounce: share(bounced, t.visits) };
}

const ANSWERS: Record<string, (q: Q) => Answer> = {
  // Overview: Traffic.
  "overview/live-visitors": (q) => figures(["Visitors now", Math.round(visitorMix(q.envs, q.to - 7 * MINUTE, q.to).visitors)]),
  "overview/visitors": (q) => figures(["Visitors", traffic(q).visitors]),
  "overview/visitors.trend": (q) => visitorsTrend(q, true),
  "overview/pageviews": (q) => {
    const t = traffic(q);
    return figures(["Page views", t.views], ["Views per visit", t.visits ? round(t.views / t.visits, 2) : null]);
  },
  "overview/bounce": (q) => figures(["Bounce rate", trafficFigures(q).bounce]),
  "overview/top-pages-5": (q) => ({
    columns: ["Visitors"],
    rows: pages(q).rows.sort((a, b) => b.visitors - a.visitors || a.route.localeCompare(b.route)).slice(0, 5).map((r) => ({ group: { route: r.route }, values: [r.visitors] })),
  }),
  "overview/top-channels-5": (q) => ({
    columns: ["Visitors"],
    rows: [...channelVisitors(traffic(q))].filter(([, n]) => n > 0).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])).slice(0, 5).map(([channel, n]) => ({ group: { channel }, values: [n] })),
  }),
  "overview/js-errors-per-1k": (q) => {
    const views = traffic(q).views;
    // Uncaught browser errors: steady at ≈2 per 1,000 views. The incident is server-side, and checkout shows it as a toast.
    const errors = Math.round(views * (0.0018 + 0.0004 * hash01(Math.round(q.to / HOUR), 81)));
    return figures(["Errors per 1,000 views", views ? round((1000 * errors) / views, 2) : null]);
  },
  // Overview: Users (always the 7 days before the end of the range, beside the 7 before them).
  "overview/active-7d": (q) => sevenDays(q, "Active users (7 days)", active),
  "overview/new-7d": (q) => sevenDays(q, "New users (7 days)", (us, f, t) => signups(us, f, t).length),
  "overview/returning-7d": (q) => sevenDays(q, "Returning users (7 days)", returning),
  "overview/accounts": (q) => figures(["Accounts", accountsAt(usersOf(q), q.to)]),
  "overview/dau-30d": (q) => {
    const us = usersOf(q);
    return series(
      daysOf(dayStart(q.to) - 29 * DAY, q.to).map((day) => ({ at: day, values: { "Daily active users": active(us, day, Math.min(day + DAY, q.to)) } })),
      86400,
      "UTC"
    );
  },

  // Users: at a glance.
  "users/accounts": (q) => figures(["Accounts", accountsAt(usersOf(q), q.to)]),
  "users/accounts.trend": (q) => {
    const us = usersOf(q);
    return perDay(q, "Accounts", (_, to) => accountsAt(us, Math.min(dayStart(to - 1) + DAY, q.now)));
  },
  "users/active": (q) => figures(["Active users", active(usersOf(q), q.from, q.to)]),
  "users/active.trend": (q) => {
    const us = usersOf(q);
    return perDay(q, "Active users", (from, to) => active(us, from, to));
  },
  "users/new": (q) => figures(["New users", signups(usersOf(q), q.from, q.to).length]),
  "users/new.trend": (q) => {
    const us = usersOf(q);
    return perDay(q, "New users", (from, to) => signups(us, from, to).length);
  },
  "users/returning": (q) => figures(["Returning users", returning(usersOf(q), q.from, q.to)]),
  "users/dau-wau-mau": (q) =>
    series(
      dauWauMau(usersOf(q), q.from, q.to).map((d) => ({ at: d.day, values: { "Daily active": d.dau, "Weekly active": d.wau, "Monthly active": d.mau } })),
      86400,
      "UTC"
    ),
  "users/stickiness": (q) => {
    const days = dauWauMau(usersOf(q), q.from, q.to);
    const avg = (k: "dau" | "mau") => days.reduce((a, d) => a + d[k], 0) / Math.max(1, days.length);
    return figures(["DAU / MAU", share(avg("dau"), avg("mau"))]);
  },
  // Users: engagement.
  "users/lifecycle-weekly": (q) => {
    const us = usersOf(q);
    const first = weekStart(q.from);
    const current = weekStart(q.to);
    const counts = new Map<number, { New: number; Retained: number; Resurrected: number; Churned: number }>();
    const at = (w: number) => {
      if (!counts.has(w)) counts.set(w, { New: 0, Retained: 0, Resurrected: 0, Churned: 0 });
      return counts.get(w)!;
    };
    for (const u of us) {
      const weeks = new Map<number, Set<number>>();
      for (const s of u.sessions) {
        if (s.start >= q.to || s.start < first - 7 * DAY) continue;
        if (!weeks.has(s.account)) weeks.set(s.account, new Set());
        weeks.get(s.account)!.add(weekStart(s.start));
      }
      for (const [account, ws] of weeks) {
        const signupWeek = weekStart(u.accounts[account].createdAtMs);
        for (const w of ws) {
          if (w >= first) at(w)[signupWeek === w ? "New" : ws.has(w - 7 * DAY) ? "Retained" : "Resurrected"]++;
          if (!ws.has(w + 7 * DAY) && w + 7 * DAY < current && w + 7 * DAY >= first) at(w + 7 * DAY).Churned++;
        }
      }
    }
    return series(
      [...counts].filter(([w]) => w < q.to).sort((a, b) => a[0] - b[0]).map(([w, c]) => ({ at: w, values: { New: c.New, Retained: c.Retained, Resurrected: c.Resurrected, Churned: -c.Churned } })),
      WEEK_BUCKET,
      "UTC"
    );
  },
  "users/power-users-l28": (q) => {
    const us = usersOf(q);
    const hist = new Map<number, number>();
    for (const u of us) {
      const days = new Map<number, Set<number>>();
      for (const s of u.sessions) {
        if (!within(s, q.to - 28 * DAY, q.to)) continue;
        if (!days.has(s.account)) days.set(s.account, new Set());
        days.get(s.account)!.add(dayStart(Math.max(s.start, q.to - 28 * DAY)));
      }
      for (const set of days.values()) hist.set(set.size, (hist.get(set.size) ?? 0) + 1);
    }
    return { columns: ["Users"], rows: [...hist].sort((a, b) => a[0] - b[0]).map(([n, users]) => ({ group: { "Days active": String(n) }, values: [users] })) };
  },
  "users/time-to-activation": (q) => {
    const by = countBy(signups(usersOf(q), q.from, q.to), (a) => activationBucket(a, Math.min(q.now, q.to + 7 * DAY)));
    return { columns: ["Sign-ups"], rows: BUCKETS.filter((b) => by.has(b)).map((b) => ({ group: { "Time to activation": b }, values: [by.get(b)!.length] })) };
  },
  "users/retention-cohorts": (q) => {
    const us = usersOf(q);
    const current = weekStart(q.to);
    const firstCohort = weekStart(q.to - 11 * 7 * DAY);
    const rows: Row[] = [];
    const cohorts = new Map<number, { size: number; weeks: number[] }>();
    for (const u of us) {
      const weeksOf = new Map<number, Set<number>>();
      for (const s of u.sessions) {
        if (s.start >= q.to || s.start < firstCohort) continue;
        if (!weeksOf.has(s.account)) weeksOf.set(s.account, new Set());
        weeksOf.get(s.account)!.add(weekStart(s.start));
      }
      for (const a of u.accounts) {
        if (!a.signedUp || a.createdAtMs < firstCohort || a.createdAtMs >= q.to) continue;
        const c = weekStart(a.createdAtMs);
        if (!cohorts.has(c)) cohorts.set(c, { size: 0, weeks: Array(12).fill(0) });
        const cohort = cohorts.get(c)!;
        cohort.size++;
        for (let k = 0; k < 12; k++) if (weeksOf.get(a.index)?.has(c + k * 7 * DAY)) cohort.weeks[k]++;
      }
    }
    for (const [c, { size, weeks }] of [...cohorts].sort((a, b) => a[0] - b[0])) {
      rows.push({
        group: { "Cohort (week of)": `${new Date(c).toISOString().slice(0, 10)} · ${size} ${size === 1 ? "user" : "users"}` },
        values: weeks.map((n, k) => (c + k * 7 * DAY <= current ? round(n / size, 4) : null)),
      });
    }
    return { columns: Array.from({ length: 12 }, (_, k) => `W${k}`), rows };
  },
  // Users: authentication.
  "users/signups-by-method": (q) => {
    const su = signups(usersOf(q), q.from, q.to);
    const by = countBy(su, (a) => `${dayStart(a.createdAtMs)}|${a.method}`);
    return series(
      [...by].sort((a, b) => a[0].localeCompare(b[0])).map(([k, list]) => {
        const [day, method] = k.split("|");
        return { at: Number(day), group: { method }, values: { "Sign-ups": list.length } };
      }),
      86400,
      "UTC"
    );
  },
  "users/signins-by-method": (q) => {
    const us = usersOf(q);
    const points = new Map<string, number>();
    for (const u of us) {
      for (const s of u.sessions) {
        if (!s.credential || s.start < q.from || s.start >= q.to) continue;
        const k = `${dayStart(s.start)}|${u.accounts[s.account].method}`;
        points.set(k, (points.get(k) ?? 0) + 1);
      }
    }
    return series(
      [...points].sort((a, b) => a[0].localeCompare(b[0])).map(([k, n]) => {
        const [day, method] = k.split("|");
        return { at: Number(day), group: { method }, values: { "Sign-ins": n } };
      }),
      86400,
      "UTC"
    );
  },
  "users/verified-share": (q) => {
    const su = signups(usersOf(q), q.from, q.to);
    return figures(["Verified", share(su.filter((a) => a.verified).length, su.length)]);
  },
  "users/signups-by-provider": (q) => {
    const su = signups(usersOf(q), q.from, q.to);
    return {
      columns: ["Sign-ups", "Share %"],
      rows: [...countBy(su, PROVIDER)]
        .sort((a, b) => b[1].length - a[1].length || a[0].localeCompare(b[0]))
        .map(([provider, list]) => ({ group: { Provider: provider }, values: [list.length, pct(list.length, su.length, 1)] })),
    };
  },
  "users/auth-sources": (q) => {
    const us = usersOf(q);
    const sessions = us.flatMap((u) => u.sessions.filter((s) => s.start >= q.from && s.start < q.to));
    const credential = sessions.filter((s) => s.credential).length;
    const signOuts = sessions.filter((s) => s.signOut).length;
    const rows: Row[] = [
      { group: { Client: "backend" }, values: [signups(us, q.from, q.to).length, credential, sessions.length - credential, 0] },
      { group: { Client: "web" }, values: [0, 0, 0, signOuts] },
    ];
    return { columns: ["Sign-ups", "Sign-ins (credential)", "Sign-ins (resumed)", "Sign-outs"], rows: rows.filter((r) => r.values.some((v) => v)) };
  },
  "users/most-active-users": (q) => {
    const us = usersOf(q);
    const rows = us.flatMap((u) => {
      const by = countBy(u.sessions.filter((s) => within(s, q.from, q.to)), (s) => String(s.account));
      return [...by].map(([account, list]) => ({
        user: u.accounts[Number(account)].userId,
        events: list.reduce((a, s) => a + s.events, 0),
        days: new Set(list.map((s) => dayStart(Math.max(s.start, q.from)))).size,
        distinct: Math.max(...list.map((s) => s.distinct)) + Math.min(3, list.length - 1),
        last: Math.max(...list.map((s) => Math.min(s.end, q.to - 1))),
      }));
    });
    rows.sort((a, b) => b.events - a.events || a.user.localeCompare(b.user));
    return {
      columns: ["Events", "Active days", "Distinct events"],
      rows: rows.slice(0, 25).map((r) => ({ group: { User: r.user, "Last seen (UTC)": utc(r.last) }, values: [r.events, r.days, r.distinct] })),
    };
  },

  // Product: at a glance.
  "product/pageviews": (q) => figures(["Page views", traffic(q).views]),
  "product/pageviews.trend": (q) => perDay(q, "Page views", (from, to) => traffic({ ...q, from, to }).views),
  "product/visits": (q) => figures(["Visits", traffic(q).visits]),
  "product/visits.trend": (q) => perDay(q, "Visits", (from, to) => traffic({ ...q, from, to }).visits),
  "product/views-per-visit": (q) => {
    const t = traffic(q);
    return figures(["Views per visit", t.visits ? round(t.views / t.visits, 2) : null]);
  },
  "product/bounce": (q) => figures(["Bounce rate", trafficFigures(q).bounce]),
  "product/visit-duration": (q) => figures(["Visit duration", traffic(q).visits ? Math.round(VISIT_SECONDS + 6 * (hash01(Math.round(q.to / DAY), 82) - 0.5)) : null]),
  "product/events": (q) => figures(["Product events", eventsByName(q).reduce((a, r) => a + r.events, 0)]),
  "product/events.trend": (q) => perDay(q, "Product events", (from, to) => eventsByName({ ...q, from, to }).reduce((a, r) => a + r.events, 0)),
  // Product: pages.
  "product/top-pages": (q) => ({
    columns: ["Views", "Visitors", "Signed-in users", "Entries", "Exits"],
    rows: pages(q).rows.filter((r) => r.views > 0).sort((a, b) => b.views - a.views || a.route.localeCompare(b.route)).map((r) => ({ group: { Route: r.route }, values: [r.views, r.visitors, r.signed, r.entries, r.exits] })),
  }),
  "product/entry-pages": (q) => {
    const p = pages(q);
    const su = countBy(signups(usersOf(q), q.from, q.to).filter((a) => a.channel !== UNATTRIBUTED), (a) => a.landing);
    return {
      columns: ["Visits", "Bounce %", "Sign-up %"],
      rows: p.rows.filter((r) => r.entries > 0).sort((a, b) => b.entries - a.entries || a.route.localeCompare(b.route)).map((r) => ({ group: { "Entry page": r.route }, values: [r.entries, round(100 * r.bounce, 1), pct(su.get(r.route)?.length ?? 0, r.entries, 1)] })),
    };
  },
  "product/exit-pages": (q) => ({
    columns: ["Exits", "Exit rate %"],
    rows: pages(q).rows.filter((r) => r.exits > 0).sort((a, b) => b.exits - a.exits || a.route.localeCompare(b.route)).map((r) => ({ group: { "Exit page": r.route }, values: [r.exits, pct(r.exits, r.views, 1)] })),
  }),
  // Product: goals and events.
  "product/goals": (q) => {
    const p = pages(q);
    const cart = p.rows.find((r) => r.route === "/cart")!;
    return {
      columns: ["Persons", "Total", "Conversion %"],
      rows: [
        { group: { Goal: "Checkout" }, values: [p.c.purchasers, p.c.purchases, pct(p.c.purchasers, p.t.visitors, 2)] },
        { group: { Goal: "Viewed the cart" }, values: [cart.visitors, cart.views, pct(cart.visitors, p.t.visitors, 2)] },
      ],
    };
  },
  "product/events-by-name": (q) => ({
    columns: ["Events", "Persons"],
    rows: eventsByName(q).map((r) => ({ group: { Event: r.name, "Last seen (UTC)": r.lastSeen }, values: [r.events, r.persons] })),
  }),
  "product/records-by-entity": (q) => {
    const step = q.stepSeconds > 0 ? q.stepSeconds * 1000 : Math.max(HOUR, Math.ceil((q.to - q.from) / 120 / HOUR) * HOUR);
    const counts = new Map<string, number>();
    for (const w of crud(q)) {
      const k = `${Math.floor(w.at / step) * step}|${w.entity}`;
      counts.set(k, (counts.get(k) ?? 0) + 1);
    }
    return series(
      [...counts].sort((a, b) => a[0].localeCompare(b[0], undefined, { numeric: true })).map(([k, n]) => {
        const [at, entity] = k.split("|");
        return { at: Number(at), group: { entity }, values: { records: n } };
      }),
      Math.round(step / 1000)
    );
  },
  "product/actions-per-entity": (q) => ({
    columns: ["Created", "Updated", "Deleted"],
    rows: [...countBy(crud(q), (w) => w.entity)]
      .sort((a, b) => b[1].length - a[1].length || a[0].localeCompare(b[0]))
      .map(([entity, list]) => ({ group: { entity }, values: (["created", "updated", "deleted"] as const).map((k) => list.filter((w) => w.kind === k).length) })),
  }),
  // Product: funnels.
  "product/funnel-activation": (q) => {
    const su = signups(usersOf(q), q.from, q.to);
    return {
      columns: ["Count"],
      rows: [
        { group: { Step: "Visited" }, values: [traffic(q).visitors] },
        { group: { Step: "Signed up" }, values: [su.length] },
        { group: { Step: "Activated" }, values: [su.filter((a) => activated(a, q.now) && a.activationAtMs! <= q.now).length] },
      ],
    };
  },
  "product/signups-from-visit": (q) => {
    const su = signups(usersOf(q), q.from, q.to);
    return figures(["From a visit", share(su.filter((a) => a.channel !== UNATTRIBUTED).length, su.length)]);
  },
  "product/funnel-buy": (q) => {
    const p = pages(q);
    const viewers = p.rows.find((r) => r.route === "/products/:id")!.visitors;
    const steps: [string, number][] = [["view_product", viewers], ["add_to_cart", p.c.cart], ["begin_checkout", p.c.checkout], ["purchase", p.c.purchasers]];
    return { columns: ["Persons"], rows: steps.map(([step, n]) => ({ group: { Step: step }, values: [n] })) };
  },
  "product/flag-exposure": (q) => {
    const c = commerce(q);
    if (!c.checkout) return { columns: ["Exposed persons", "Activated %"], rows: [] };
    const control = Math.round(c.checkout * 0.51);
    const base = (100 * c.purchasers) / c.checkout;
    return {
      columns: ["Exposed persons", "Activated %"],
      rows: [
        { group: { Flag: DEMO_FLAG.flag, Variant: DEMO_FLAG.variants[0] }, values: [control, round(base * 0.95, 1)] },
        { group: { Flag: DEMO_FLAG.flag, Variant: DEMO_FLAG.variants[1] }, values: [c.checkout - control, round(Math.min(100, base * 1.06), 1)] },
      ],
    };
  },

  // Acquisition: at a glance.
  "acquisition/visitors": (q) => figures(["Visitors", traffic(q).visitors]),
  "acquisition/visitors.trend": (q) => visitorsTrend(q, false),
  "acquisition/visits": (q) => figures(["Visits", traffic(q).visits]),
  "acquisition/visits.trend": (q) => perDay(q, "Visits", (from, to) => traffic({ ...q, from, to }).visits),
  "acquisition/signups": (q) => figures(["Sign-ups", signups(usersOf(q), q.from, q.to).length]),
  "acquisition/signups.trend": (q) => {
    const us = usersOf(q);
    return perDay(q, "Sign-ups", (from, to) => signups(us, from, to).length);
  },
  "acquisition/visit-to-signup": (q) => {
    const attributed = signups(usersOf(q), q.from, q.to).filter((a) => a.channel !== UNATTRIBUTED).length;
    return figures(["Visit to sign-up", share(attributed, traffic(q).visitors)]);
  },
  // Acquisition: channels.
  "acquisition/visitors-by-channel": (q) => {
    const points = daysOf(q.from, q.to).flatMap((day) => {
      const v = q.envs.reduce((a, env) => a + visitorsOn(env, day) * dayShare(day, q.from, q.to), 0);
      const w = q.envs.reduce((acc, env) => {
        const ws = channelWeights(env, day);
        const sum = ws.reduce((x, y) => x + y, 0);
        return acc.map((x, i) => x + (visitorsOn(env, day) * ws[i]) / sum);
      }, CHANNELS.map(() => 0));
      const n = allocate(v, w);
      return CHANNELS.map((channel, i) => ({ at: day, group: { channel }, values: { Visitors: n[i] } })).filter((p) => p.values.Visitors > 0);
    });
    return series(points.sort((a, b) => a.at - b.at || a.group.channel.localeCompare(b.group.channel)), 86400, "UTC");
  },
  "acquisition/channels": (q) => {
    const t = traffic(q);
    const visitors = channelVisitors(t);
    const visits = allocate(t.visits, CHANNELS.map((c) => visitors.get(c) ?? 0));
    const su = countBy(signups(usersOf(q), q.from, q.to), (a) => a.channel);
    const rows = [...CHANNELS, UNATTRIBUTED].map((channel, i) => {
      const v = visitors.get(channel) ?? 0;
      const n = visits[i] ?? 0;
      const s = su.get(channel) ?? [];
      return { channel, v, row: { group: { Channel: channel }, values: [v, n, pct(n * CHANNEL_BOUNCE[channel], n, 1), s.length, pct(s.length, v, 2), pct(s.filter((a) => activated(a, q.now)).length, s.length, 1)] } };
    });
    return {
      columns: ["Visitors", "Visits", "Bounce %", "Sign-ups", "Conversion %", "Activated %"],
      rows: rows.filter((r) => r.v > 0 || (r.row.values[3] ?? 0) > 0).sort((a, b) => b.v - a.v || a.channel.localeCompare(b.channel)).map((r) => r.row),
    };
  },
  "acquisition/signups-by-channel-weekly": (q) => {
    const by = countBy(signups(usersOf(q), q.from, q.to), (a) => `${weekStart(a.createdAtMs)}|${a.channel}`);
    return series(
      [...by].sort((a, b) => a[0].localeCompare(b[0])).map(([k, list]) => {
        const [week, channel] = k.split("|");
        return { at: Number(week), group: { channel }, values: { "Sign-ups": list.length } };
      }),
      WEEK_BUCKET,
      "UTC"
    );
  },
  // Acquisition: sources.
  "acquisition/sources": (q) => {
    const t = traffic(q);
    const visitors = new Map<string, number>();
    for (const x of touches(t)) visitors.set(x.touch.source, (visitors.get(x.touch.source) ?? 0) + x.visitors);
    const rows = breakdown(q, [...visitors], (a) => a.touch?.source ?? "(direct)");
    return { columns: ["Visitors", "Sign-ups", "Conversion %"], rows: rows.map((r) => ({ group: { Source: r.key }, values: [r.visitors, r.signups, pct(r.signups, r.visitors, 2)] })) };
  },
  "acquisition/referrers": (q) => {
    const t = traffic(q);
    const rows = touches(t)
      .filter((x) => x.touch.ref && x.visitors > 0)
      .map((x) => ({ group: { Referrer: x.touch.ref, Path: x.touch.path }, values: [x.visitors, Math.round(x.visitors * VISITS_PER_VISITOR)] as number[] }));
    return { columns: ["Visitors", "Visits"], rows: rows.sort((a, b) => b.values[1] - a.values[1] || a.group.Referrer.localeCompare(b.group.Referrer)).slice(0, 50) };
  },
  "acquisition/campaigns": (q) => {
    const t = traffic(q);
    const su = signups(usersOf(q), q.from, q.to);
    const keyOf = (x: Touch) => [x.source, x.medium, x.campaign, x.content, x.term].join("|");
    const visitors = new Map<string, number>();
    for (const x of touches(t)) if (x.touch.medium || x.touch.campaign) visitors.set(keyOf(x.touch), (visitors.get(keyOf(x.touch)) ?? 0) + x.visitors);
    const bySu = countBy(su.filter((a) => a.touch && (a.touch.medium || a.touch.campaign)), (a) => keyOf(a.touch!));
    const keys = [...new Set([...visitors.keys(), ...bySu.keys()])];
    const rows = keys.map((k) => {
      const [source, medium, campaign, content, term] = k.split("|");
      const v = visitors.get(k) ?? 0;
      const s = bySu.get(k) ?? [];
      return { v, row: { group: { Source: source, Medium: medium, Campaign: campaign, Content: content, Term: term }, values: [v, s.length, pct(s.length, v, 2), pct(s.filter((a) => activated(a, q.now)).length, s.length, 1)] } };
    });
    return { columns: ["Visitors", "Sign-ups", "Conversion %", "Activated %"], rows: rows.sort((a, b) => b.v - a.v).map((r) => r.row) };
  },
  "acquisition/landing-pages": (q) => {
    const t = traffic(q);
    const visitors = channelVisitors(t);
    const pairs = TRAFFIC_ROUTE.flatMap((route) => CHANNELS.map((channel) => ({ route, channel, w: (ROUTES.find((r) => r.route === route)!.entries * (visitors.get(channel) ?? 0)) })));
    const visits = allocate(t.visits, pairs.map((p) => p.w));
    const su = countBy(signups(usersOf(q), q.from, q.to), (a) => (a.channel === UNATTRIBUTED ? `${UNATTRIBUTED}|${UNATTRIBUTED}` : `${a.landing}|${a.channel}`));
    const rows: Row[] = pairs.map((p, i) => ({ group: { "Landing page": p.route, Channel: p.channel as string }, values: [visits[i], su.get(`${p.route}|${p.channel}`)?.length ?? 0] as (number | null)[] }));
    const unattributed = su.get(`${UNATTRIBUTED}|${UNATTRIBUTED}`)?.length ?? 0;
    if (unattributed) rows.push({ group: { "Landing page": UNATTRIBUTED, Channel: UNATTRIBUTED }, values: [0, unattributed] });
    return {
      columns: ["Visits", "Sign-ups"],
      rows: rows.filter((r) => (r.values[0] ?? 0) > 0 || (r.values[1] ?? 0) > 0).sort((a, b) => (b.values[0] ?? 0) - (a.values[0] ?? 0) || a.group["Landing page"].localeCompare(b.group["Landing page"])).slice(0, 50),
    };
  },
  // Acquisition: audience.
  "acquisition/countries": (q) => ({
    columns: ["Visitors", "Sign-ups", "Conversion %"],
    rows: breakdown(q, COUNTRIES, (a) => a.country).map((r) => ({ group: { Country: r.key }, values: [r.visitors, r.signups, pct(r.signups, r.visitors, 2)] })),
  }),
  "acquisition/devices": (q) => ({
    columns: ["Visitors", "Sign-ups", "Conversion %"],
    rows: breakdown(q, DEVICES, (a) => a.device, 10).map((r) => ({ group: { Device: r.key }, values: [r.visitors, r.signups, pct(r.signups, r.visitors, 2)] })),
  }),
  "acquisition/browsers": (q) => {
    const n = allocate(traffic(q).visitors, BROWSERS.map(([, w]) => w));
    return { columns: ["Visitors"], rows: BROWSERS.map(([browser], i) => ({ group: { browser }, values: [n[i]] })).filter((r) => r.values[0] > 0) };
  },
  "acquisition/os": (q) => {
    const n = allocate(traffic(q).visitors, OPERATING_SYSTEMS.map(([, w]) => w));
    return { columns: ["Visitors"], rows: OPERATING_SYSTEMS.map(([os], i) => ({ group: { os }, values: [n[i]] })).filter((r) => r.values[0] > 0) };
  },
  // Acquisition: quality. The sign-ups of 90 to 35 days before the end of the range, each having lived its fourth week.
  "acquisition/retention-by-channel": (q) => {
    const us = usersOf(q);
    const list = us.flatMap((u) =>
      u.accounts
        .filter((a) => a.createdAtMs >= q.to - 90 * DAY && a.createdAtMs < q.to - 35 * DAY)
        .map((a) => ({ a, w4: u.sessions.some((s) => s.account === a.index && s.start < q.to && s.end >= a.createdAtMs + 28 * DAY && s.start < a.createdAtMs + 35 * DAY) }))
    );
    return {
      columns: ["Sign-ups", "Active in week 4 %"],
      rows: [...countBy(list, (x) => x.a.channel)]
        .sort((a, b) => b[1].length - a[1].length || a[0].localeCompare(b[0]))
        .map(([channel, xs]) => ({ group: { Channel: channel }, values: [xs.length, xs.length >= 20 ? pct(xs.filter((x) => x.w4).length, xs.length, 1) : null] })),
    };
  },
};

/** Environments a read is scoped to: the scope's, narrowed by a deployment.environment.name filter or predicate. */
function envsOf(req: QueryPanelRequest): DemoEnvKey[] {
  let envs = (req.envIds.length ? DEMO_ENV_IDS.filter((e) => req.envIds.includes(e)) : [...DEMO_ENV_IDS]) as DemoEnvKey[];
  const preds = [...(req.panel.where ?? []), ...(req.filters ?? []).map((f) => ({ field: f.key, op: f.op, value: f.value, values: f.values }))];
  for (const p of preds) {
    if (p.field.replace(/^(resource|span|log)\.attr\./, "") !== "deployment.environment.name") continue;
    const names = p.op === "in" ? (p.values ?? []) : [p.value ?? ""];
    envs = envs.filter((e) => names.some((n) => ENV_NAMES[n] === e || n === e));
  }
  return envs;
}

/** Whether `req` reads a built-in product analytics panel this module answers. */
export function isProductAnalyticsPanel(req: Pick<QueryPanelRequest, "dashboardId" | "panel">): boolean {
  return req.panel.kind === "sql" && !!req.dashboardId && PRODUCT_ANALYTICS_DASHBOARDS.has(req.dashboardId) && `${req.dashboardId}/${req.panel.id}` in ANSWERS;
}

const BLANK: Omit<PanelResult, "kind" | "unit"> = {
  series: [], columns: [], rows: [], stepSeconds: 0, truncated: false, funnelSteps: [], issues: [], sessions: [], seriesMeta: [], seriesTotal: 0, warnings: [], compiledSql: [],
};

/** A built-in product analytics panel's answer, or null when `req` is not one. */
export function productAnalyticsPanel(req: QueryPanelRequest, now: number): PanelResult | null {
  if (!isProductAnalyticsPanel(req)) return null;
  const to = Math.min(req.window.endMs, now);
  const q: Q = { envs: envsOf(req), from: Math.min(req.window.startMs, to), to, now, stepSeconds: req.stepSeconds };
  const answer = ANSWERS[`${req.dashboardId}/${req.panel.id}`](q);
  return {
    ...BLANK,
    kind: "sql",
    unit: req.panel.unit ?? "",
    window: req.window,
    columns: answer.columns,
    rows: answer.rows ?? [],
    series: answer.series ?? [],
    bucketSeconds: answer.bucketSeconds,
    bucketTimezone: answer.bucketTimezone,
  };
}

/** Every answer's key (`<dashboard>/<panel>`, `.trend` for a trendSql): the test checks each built-in panel has one. */
export const PRODUCT_ANALYTICS_ANSWERS: readonly string[] = Object.keys(ANSWERS);

/** For the Checkout funnel dashboard (a session funnel over the same steps): the commerce of the window, so its funnel and the Product dashboard's agree. */
export function shopCommerce(envs: DemoEnvKey[], from: number, to: number, now: number) {
  const q: Q = { envs, from, to: Math.min(to, now), now, stepSeconds: 0 };
  const p = pages(q);
  return { viewers: p.rows.find((r) => r.route === "/products/:id")!.visitors, cart: p.c.cart, checkout: p.c.checkout, purchasers: p.c.purchasers, purchases: p.c.purchases, orders: p.c.orders };
}
