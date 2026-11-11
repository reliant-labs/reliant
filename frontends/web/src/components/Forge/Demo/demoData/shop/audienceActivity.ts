// Copyright (c) 2026 Reliant Labs

/**
 * What shop's audience DOES: the product analytics side of the one
 * population audience.ts defines (product-analytics-design.md §4). The
 * accounts, their sign-up and last sign-in times are audience.ts's, so every
 * figure here agrees with the Users page; this module adds:
 *
 *   traits     per account: how it signed up (method, verified), the channel,
 *              source and landing page its sign-up is attributed to (or
 *              Unattributed), its device and country, an engagement class,
 *              and when it activated (its first order), if it did.
 *   activity   one session per active user-day, ending with the account's
 *              last sign-in: events, a credential sign-in or the daily resumed
 *              heartbeat, a sign-out, an order. prod's orders since v1.8.3
 *              fail their charge at the incident's rate (the order is
 *              recorded, the purchase is not).
 *   visitors   the anonymous traffic: visitor-days per day (≈2,400 on a
 *              weekday in prod) arriving on the traffic model's daily curve,
 *              with their channels, countries, devices and pages as shares.
 *              It is analytic, not per visitor: a window's figures are its
 *              days' shares, rounded once (allocate), so a table always sums
 *              to the figure above it.
 *
 * Everything is a pure function of `now` (shopStory.demoNow). The reader is
 * productAnalytics.ts: the Overview's Users card and the Overview, Users,
 * Product and Acquisition dashboards.
 */

import { DAY, DEMO_CUSTOMERS, DEMO_INCIDENT, HOUR, MINUTE, dailyLoad, demoNow, incidentStartMs } from "../shopStory";
import { demoAudience, type DemoAccount } from "./audience";
import { shopDatabase } from "./database";

// ── Determinism ──────────────────────────────────────────────────────────────

export function hash01(...parts: number[]): number {
  let h = 0x811c9dc5;
  for (const p of parts) h = Math.imul(h ^ (p | 0), 0x01000193);
  h ^= h >>> 15;
  h = Math.imul(h, 0x2c1b3c6d);
  h ^= h >>> 12;
  return (h >>> 0) / 0x1_0000_0000;
}

/** The key of `shares` a uniform draw `u` lands on. */
function pick<K extends string>(shares: readonly (readonly [K, number])[], u: number): K {
  const total = shares.reduce((a, [, w]) => a + w, 0);
  let x = u * total;
  for (const [k, w] of shares) {
    if (x < w) return k;
    x -= w;
  }
  return shares[shares.length - 1][0];
}

/**
 * `total` split by `weights` into whole numbers that sum to round(total)
 * (largest remainder, ties to the first): how every table keeps its rows'
 * sum equal to the figure above it.
 */
export function allocate(total: number, weights: readonly number[]): number[] {
  const want = Math.round(total);
  const sum = weights.reduce((a, w) => a + Math.max(0, w), 0);
  if (!sum || want <= 0) return weights.map(() => 0);
  const exact = weights.map((w) => (Math.max(0, w) / sum) * want);
  const out = exact.map(Math.floor);
  let left = want - out.reduce((a, b) => a + b, 0);
  const order = exact.map((e, i) => [e - Math.floor(e), i] as const).sort((a, b) => b[0] - a[0] || a[1] - b[1]);
  for (let k = 0; left > 0; k = (k + 1) % order.length, left--) out[order[k][1]]++;
  return out;
}

export const dayStart = (ms: number) => Math.floor(ms / DAY) * DAY;
const isWeekend = (ms: number) => {
  const d = new Date(ms).getUTCDay();
  return d === 0 || d === 6;
};
/** The Monday 00:00 UTC of `ms`'s ISO week. */
export const weekStart = (ms: number) => {
  const d = dayStart(ms);
  return d - ((new Date(d).getUTCDay() + 6) % 7) * DAY;
};

// The traffic model's daily curve, integrated: the share of a day's arrivals before each quarter hour.
const STEPS = 96;
const CUMULATIVE = (() => {
  const out = [0];
  for (let i = 0; i < STEPS; i++) out.push(out[i] + dailyLoad((i + 0.5) * (DAY / STEPS)));
  return out.map((v) => v / out[STEPS]);
})();
/** The share of a day's arrivals that happen before `ms` (within its own day). */
function arrivedBy(ms: number): number {
  const x = ((ms - dayStart(ms)) / DAY) * STEPS;
  const i = Math.min(STEPS - 1, Math.floor(x));
  return CUMULATIVE[i] + (CUMULATIVE[i + 1] - CUMULATIVE[i]) * (x - i);
}
/** The share of day `day`'s arrivals inside [from, to). */
export function dayShare(day: number, from: number, to: number): number {
  const a = Math.max(from, day);
  const b = Math.min(to, day + DAY);
  if (b <= a) return 0;
  return (b >= day + DAY ? 1 : arrivedBy(b)) - (a <= day ? 0 : arrivedBy(a));
}
/** A time in day `day` drawn from the daily curve by `u`. */
function timeInDay(day: number, u: number): number {
  let lo = 0;
  let hi = STEPS;
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1;
    if (CUMULATIVE[mid] <= u) lo = mid;
    else hi = mid;
  }
  const span = CUMULATIVE[lo + 1] - CUMULATIVE[lo];
  return day + ((lo + (span ? (u - CUMULATIVE[lo]) / span : 0)) / STEPS) * DAY;
}

// ── The vocabularies ─────────────────────────────────────────────────────────

export const CHANNELS = ["Direct", "Organic search", "Paid search", "Email", "Organic social", "Referral", "AI assistants", "Paid social"] as const;
export type Channel = (typeof CHANNELS)[number];
export const UNATTRIBUTED = "Unattributed";
export type SignupChannel = Channel | typeof UNATTRIBUTED;

/** Each channel's share of prod's visitor-days and of its attributed sign-ups (§4). */
export const CHANNEL_VISITORS: Record<Channel, number> = {
  Direct: 31, "Organic search": 27, "Paid search": 10, Email: 7, "Organic social": 8, Referral: 9, "AI assistants": 4, "Paid social": 4,
};
const CHANNEL_SIGNUPS: Record<Channel, number> = {
  Direct: 24, "Organic search": 30, "Paid search": 14, Email: 12, "Organic social": 5, Referral: 6, "AI assistants": 6, "Paid social": 3,
};
/** Of prod's sign-ups, the share matched to a same-day visit; the rest are Unattributed. */
export const ATTRIBUTED_SHARE = 0.88;

/** Where each channel's visits come from: the source (utm_source, else the referrer host), its referrer path, and the campaign. */
export interface Touch {
  source: string;
  ref: string;
  path: string;
  medium: string;
  campaign: string;
  content: string;
  term: string;
  share: number;
}
const touch = (source: string, share: number, t: Partial<Touch> = {}): Touch => ({ source, ref: "", path: "", medium: "", campaign: "", content: "", term: "", share, ...t });
export const CHANNEL_TOUCHES: Record<Channel, Touch[]> = {
  Direct: [touch("(direct)", 1)],
  "Organic search": [
    touch("www.google.com", 22, { ref: "www.google.com", path: "/" }),
    touch("www.bing.com", 3, { ref: "www.bing.com", path: "/search" }),
    touch("duckduckgo.com", 2, { ref: "duckduckgo.com", path: "/" }),
  ],
  "Paid search": [
    touch("google", 8, { medium: "cpc", campaign: "brand-eu" }),
    touch("google", 2, { medium: "cpc", campaign: "brand-eu", term: "linen tote bag" }),
  ],
  Email: [
    touch("newsletter", 5, { medium: "email", campaign: "autumn-sale", content: "hero" }),
    touch("newsletter", 2, { medium: "email", campaign: "autumn-sale", content: "footer" }),
  ],
  "Organic social": [
    touch("www.reddit.com", 4, { ref: "www.reddit.com", path: "/r/BuyItForLife/comments/:id" }),
    touch("news.ycombinator.com", 2.5, { ref: "news.ycombinator.com", path: "/item" }),
    touch("l.instagram.com", 1.5, { ref: "l.instagram.com", path: "/" }),
  ],
  Referral: [
    touch("designbythebook.example", 7, { ref: "designbythebook.example", path: "/2026/best-coffee-kit" }),
    touch("designbythebook.example", 2, { ref: "designbythebook.example", path: "/gift-guides/:id" }),
  ],
  "AI assistants": [
    touch("chatgpt.com", 3, { ref: "chatgpt.com", path: "/" }),
    touch("perplexity.ai", 1, { ref: "perplexity.ai", path: "/search/:id" }),
  ],
  "Paid social": [touch("meta", 4, { medium: "paid_social", campaign: "autumn-sale", content: "carousel" })],
};

export const COUNTRIES: readonly (readonly [string, number])[] = [
  ["US", 24], ["DE", 17], ["GB", 13], ["FR", 9], ["ES", 7], ["NL", 6], ["IT", 6], ["SE", 5], ["JP", 5], ["CA", 3], ["AU", 2], ["PL", 2], ["BR", 1],
];
export const DEVICES: readonly (readonly [string, number])[] = [["desktop", 58], ["mobile", 38], ["tablet", 4]];
export const BROWSERS: readonly (readonly [string, number])[] = [["Chrome", 52], ["Safari", 31], ["Firefox", 7], ["Edge", 6], ["Samsung Internet", 3], ["Opera", 1]];
export const OPERATING_SYSTEMS: readonly (readonly [string, number])[] = [["Windows", 30], ["iOS", 27], ["macOS", 24], ["Android", 15], ["Linux", 3], ["ChromeOS", 1]];

/**
 * shop-web's routes (normalised as page_viewed records them): each one's
 * share of page views and of visits entered on it, the share of its views
 * that end a visit (its exit rate, before the visits' total is shared out), the share
 * of visitor-days that view it, the share of visits entered on it that see
 * one page only, and the share of its visitors who are signed in.
 */
export const ROUTES = [
  { route: "/products/:id", views: 36, entries: 38, exit: 0.34, reach: 0.61, bounce: 0.45, signedIn: 0.12 },
  { route: "/", views: 20, entries: 34, exit: 0.3, reach: 0.46, bounce: 0.38, signedIn: 0.1 },
  { route: "/collections/:id", views: 13, entries: 11, exit: 0.26, reach: 0.24, bounce: 0.36, signedIn: 0.1 },
  { route: "/cart", views: 9, entries: 1, exit: 0.4, reach: 0.17, bounce: 0.2, signedIn: 0.4 },
  { route: "/checkout", views: 5, entries: 0.3, exit: 0.3, reach: 0.08, bounce: 0.15, signedIn: 0.92 },
  { route: "/orders/:id", views: 5, entries: 4, exit: 0.55, reach: 0.07, bounce: 0.55, signedIn: 0.95 },
  { route: "/account", views: 4, entries: 2, exit: 0.35, reach: 0.06, bounce: 0.3, signedIn: 0.9 },
  { route: "/orders", views: 3, entries: 2, exit: 0.3, reach: 0.04, bounce: 0.4, signedIn: 0.95 },
  { route: "/auth/sign-in", views: 3, entries: 3, exit: 0.2, reach: 0.07, bounce: 0.25, signedIn: 0.3 },
  { route: "/auth/sign-up", views: 2, entries: 2, exit: 0.3, reach: 0.04, bounce: 0.3, signedIn: 0.3 },
] as const;

/** Where a sign-up's first visit of the day entered. */
const SIGNUP_LANDINGS: readonly (readonly [string, number])[] = [["/products/:id", 34], ["/", 30], ["/auth/sign-up", 14], ["/collections/:id", 9], ["/cart", 7], ["/checkout", 6]];

export const SIGNUP_METHODS = [
  { method: "password", social: "", share: 46 },
  { method: "social", social: "google", share: 31 },
  { method: "passkey", social: "", share: 9 },
  { method: "social", social: "github", share: 8 },
  { method: "magic_link", social: "", share: 6 },
] as const;

/** Per visit: visits per visitor-day, page views per visit, the mean visit length counting a bounce as 0 (2m 10s). */
export const VISITS_PER_VISITOR = 1.18;
export const VIEWS_PER_VISIT = 2.6;
export const VISIT_SECONDS = 130;

/** The storefront's flag, for the Experiments panel: who saw which variant. */
export const DEMO_FLAG = { flag: "express-checkout", variants: ["control", "on"] as const };

// ── People ───────────────────────────────────────────────────────────────────

export type Engagement = "heavy" | "regular" | "lapsed";

/**
 * What the product analytics know about an account beyond what the identity
 * provider holds (audience.ts DemoAccount): how it signed up and where from,
 * and how engaged it is. Kept here, keyed by the account, so audience.ts stays
 * the identity's shape.
 */
export interface Traits {
  method: string;
  /** The social provider of a social sign-up ("google", "github"); "" otherwise. */
  social: string;
  verified: boolean;
  /** The channel of the same-day visit the sign-up is attributed to, or Unattributed. */
  channel: SignupChannel;
  /** That visit's source and campaign; null when Unattributed. */
  touch: Touch | null;
  landing: string;
  device: string;
  country: string;
  engagement: Engagement;
}

/** An account (audience.ts) with its traits, as the dashboards read it. */
export interface DemoPerson extends Traits {
  /** Its position in demoAudience(env): sessions name it by this. */
  index: number;
  /** The identity subject: the product events' user id. */
  userId: string;
  createdAtMs: number;
  lastSignInAtMs: number | null;
  /** It signed up (an invitation not yet accepted did not: no auth_signed_up). */
  signedUp: boolean;
  /** When it placed its first order (activation), or null: not (yet). */
  activationAtMs: number | null;
}

/** A string's stable hash input. */
function seedOf(text: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < text.length; i++) h = Math.imul(h ^ text.charCodeAt(i), 0x01000193);
  return h | 0;
}

/** The engagement mix of a channel's sign-ups: Email and AI assistants retain best, Paid social worst (§4). */
const CHANNEL_LIFT: Record<string, number> = { Email: 0.08, "AI assistants": 0.07, "Paid search": 0.02, "Paid social": -0.14, Referral: 0.03 };

/**
 * How engaged an account is follows its last sign-in (audience.ts): one
 * signed in within the day comes back daily (heavy), within the week every
 * few days (regular), longer ago it has lapsed. So the days before the last
 * sign-in hold as many daily users as the last day does, and the 30-day
 * curve is flat rather than a cliff at the end.
 */
function engagementOf(now: number, account: DemoAccount): Engagement {
  if (account.lastSignInAtMs === null) return "lapsed";
  const ago = now - account.lastSignInAtMs;
  return ago < DAY ? "heavy" : ago < 7 * DAY ? "regular" : "lapsed";
}

/** The delay from sign-up to the first order: 64% within 7 days, 41% within the hour (§4); null: never. */
function activationDelay(channel: SignupChannel, u: number, v: number): number | null {
  const within7 = 0.64 + (CHANNEL_LIFT[channel] ?? 0);
  if (u < within7) {
    const x = u / within7;
    if (x < 41 / 64) return (2 + v * 50) * MINUTE;
    if (x < 53 / 64) return HOUR + v * 23 * HOUR;
    if (x < 60 / 64) return DAY + v * 2 * DAY;
    return 3 * DAY + v * 4 * DAY;
  }
  return u < within7 + 0.06 ? 8 * DAY + v * 14 * DAY : null;
}

/**
 * How a channel's people stay (§4: Email and AI assistants retain best, Paid
 * social worst): the channel is drawn weighted toward the ones whose people
 * are still signing in, so an account seen this month is likelier an Email
 * sign-up and one gone for months a Paid social one.
 */
function stickiness(channel: Channel, account: DemoAccount, now: number): number {
  const recent = account.lastSignInAtMs !== null && now - account.lastSignInAtMs < 30 * DAY;
  const lift = CHANNEL_LIFT[channel] ?? 0;
  return recent ? 1 + 3 * lift : 1 - 6 * lift;
}

const peopleCache = new Map<string, DemoPerson[]>();

/** `env`'s audience (audience.ts, as of demoNow()) with each account's traits, oldest first. */
export function demoPeople(env: string, now: number = demoNow()): DemoPerson[] {
  const key = `${env}|${now}`;
  const cached = peopleCache.get(key);
  if (cached) return cached;
  const customers = new Map(shopDatabase(env, now).customers.map((row) => [String(row.identity_subject), row]));
  const people = demoAudience(env).map((account, index): DemoPerson => {
    const s = seedOf(account.id);
    const method = pick(SIGNUP_METHODS.map((m, i) => [String(i), m.share] as const), hash01(s, 31));
    const m = SIGNUP_METHODS[Number(method)];
    const channel: SignupChannel =
      env !== "env-prod" ? "Direct" : hash01(s, 32) >= ATTRIBUTED_SHARE ? UNATTRIBUTED : pick(CHANNELS.map((c) => [c, CHANNEL_SIGNUPS[c] * stickiness(c, account, now)] as const), hash01(s, 33));
    const touches = channel === UNATTRIBUTED ? null : CHANNEL_TOUCHES[channel];
    const signedUp = account.lastSignInAtMs !== null;
    // The first order lands in a session, so never long after the account's last sign-in: within the hour it is that session's
    // (the sign-up's, at checkout, for most); one due later is placed in the last session, when that is within the week.
    let activationAtMs: number | null = null;
    const delay = signedUp && account.roles.includes("customer") ? activationDelay(channel, hash01(s, 34), hash01(s, 35)) : null;
    if (delay !== null && account.lastSignInAtMs !== null) {
      const at = account.createdAtMs + delay;
      if (at <= account.lastSignInAtMs + HOUR && at <= now) activationAtMs = at;
      else if (account.lastSignInAtMs - account.createdAtMs < 7 * DAY) activationAtMs = Math.min(now, account.lastSignInAtMs + 3 * MINUTE);
    }
    return {
      index,
      userId: account.id,
      createdAtMs: account.createdAtMs,
      lastSignInAtMs: account.lastSignInAtMs,
      signedUp,
      activationAtMs,
      method: m.method,
      social: m.social,
      verified: m.method !== "password" || hash01(s, 37) < 0.79,
      channel,
      touch: touches ? touches[Math.floor(hash01(s, 38) * touches.length)] : null,
      landing: pick(SIGNUP_LANDINGS, hash01(s, 39)),
      device: pick(DEVICES, hash01(s, 40)),
      country: String(customers.get(account.id)?.country ?? DEMO_CUSTOMERS.find((c) => c.email === account.email)?.country ?? pick(COUNTRIES, hash01(s, 36))),
      engagement: engagementOf(now, account),
    };
  });
  peopleCache.set(key, people);
  return people;
}

// ── Activity ─────────────────────────────────────────────────────────────────

/** One active user-day: one session, its events, and what happened in it. */
export interface Session {
  account: number;
  start: number;
  end: number;
  /** User-attributed product events in the session (page views, funnel steps, auth, CRUD). */
  events: number;
  /** auth_signed_up (the account's sign-up day). */
  signUp: boolean;
  /** auth_signed_in kind=credential (else the resumed heartbeat). */
  credential: boolean;
  signOut: boolean;
  /** When the session placed an order (order_created), or 0. */
  orderAt: number;
  /** Whether that order's charge succeeded (a `purchase` event): false only for prod's charges since v1.8.3 that failed. */
  purchased: boolean;
  /** The account's first order. */
  activation: boolean;
  /** Distinct event names the session sent. */
  distinct: number;
}

/** How far back activity is generated: the 12 cohort weeks, the 90-day new-user window and a 7-day comparison all fit. */
export const HISTORY_DAYS = 100;

/** The chance an account of class `e`, `age` days after signing up, is active on a day before its last sign-in. */
function activeChance(e: Engagement, age: number, weekend: boolean): number {
  const w = weekend ? 0.85 : 1;
  if (e === "heavy") return 0.8 * w;
  if (e === "regular") return (0.2 + 0.25 * Math.exp(-age / 6)) * w;
  return (0.03 + 0.3 * Math.exp(-age / 3)) * w;
}
const ORDER_CHANCE: Record<Engagement, number> = { heavy: 0.03, regular: 0.08, lapsed: 0.12 };

export interface Activity {
  people: DemoPerson[];
  /** Every session, by start time. */
  sessions: Session[];
}

const activityCache = new Map<string, Activity>();

/**
 * `env`'s sessions over the last HISTORY_DAYS days. Each account's LAST one
 * starts at its last sign-in (audience.ts lastSignInAtMs) and none comes
 * after it, so "active in the last N days" here is exactly "signed in within
 * N days" on the Users page. Before it: the sign-up day, the activation day,
 * and days drawn by the account's engagement.
 */
export function demoActivity(env: string, now: number = demoNow()): Activity {
  const key = `${env}|${now}`;
  const cached = activityCache.get(key);
  if (cached) return cached;
  const people = demoPeople(env, now);
  const salt = ({ "env-prod": 1, "env-staging": 2, "env-dev": 3 } as Record<string, number>)[env] ?? 9;
  const first = dayStart(now) - (HISTORY_DAYS - 1) * DAY;
  const incidentAt = env === DEMO_INCIDENT.envId ? incidentStartMs(now) : Infinity;
  // The share of charges the incident fails: it turns 71% of checkouts ending in a purchase into 63%.
  const failShare = 1 - 0.63 / 0.71;
  const sessions: Session[] = [];
  for (const a of people) {
    if (a.lastSignInAtMs === null || a.lastSignInAtMs < first) continue;
    const last = a.lastSignInAtMs;
    const lastDay = dayStart(last);
    const signupDay = dayStart(a.createdAtMs);
    const activationDay = a.activationAtMs === null ? -1 : dayStart(a.activationAtMs);
    for (let day = Math.max(first, signupDay); day <= lastDay; day += DAY) {
      const d = Math.round(day / DAY);
      const final = day === lastDay;
      const signUp = day === signupDay && a.createdAtMs >= first;
      const activation = day === activationDay;
      const age = (day - signupDay) / DAY;
      if (!final && !signUp && !activation && hash01(a.index, d, 51, salt) >= activeChance(a.engagement, age, isWeekend(day))) continue;
      // The last day's session is the last sign-in; on the sign-up day it begins at the sign-up; otherwise drawn from the daily curve, before the last sign-in.
      const start = final ? last : signUp ? a.createdAtMs : activation ? Math.max(a.createdAtMs, a.activationAtMs! - 4 * MINUTE) : Math.min(timeInDay(day, hash01(a.index, d, 52, salt)), last - MINUTE);
      if (start > now || start < a.createdAtMs) continue;
      const long = a.engagement === "heavy" ? 30 : 14;
      let end = start + (2 + hash01(a.index, d, 53) * long) * MINUTE;
      if (activation) end = Math.max(end, a.activationAtMs! + 2 * MINUTE);
      end = Math.min(end, now);
      // An order: the activation, else a repeat order with the class's chance.
      const repeat = !activation && a.activationAtMs !== null && a.activationAtMs < start && hash01(a.index, d, 54) < ORDER_CHANCE[a.engagement];
      const orderAt = activation ? a.activationAtMs! : repeat ? start + (end - start) * 0.8 : 0;
      const events = Math.max(1, Math.round((a.engagement === "heavy" ? 9 : 5) + hash01(a.index, d, 56) * 14));
      sessions.push({
        account: a.index,
        start,
        end,
        events: events + (orderAt ? 4 : 0) + (signUp ? 2 : 0),
        signUp,
        credential: signUp || hash01(a.index, d, 57) < 0.24,
        signOut: hash01(a.index, d, 58) < 0.06,
        orderAt: orderAt && orderAt <= now ? orderAt : 0,
        purchased: orderAt > 0 && orderAt <= now,
        activation,
        distinct: 4 + Math.floor(hash01(a.index, d, 59) * 5) + (orderAt ? 3 : 0),
      });
    }
  }
  sessions.sort((x, y) => x.start - y.start || x.account - y.account);
  // Since v1.8.3 reached prod, charges fail at the incident's share: by quota in time order, not by
  // chance, so the first order after the promotion already shows it and an hour's handful of orders
  // carries the drop (71% → 63% of checkouts end in a purchase) instead of rounding it away.
  let k = 0;
  for (const s of sessions.filter((x) => x.orderAt >= incidentAt).sort((x, y) => x.orderAt - y.orderAt)) {
    if (Math.floor(0.9 + (k + 1) * failShare) > Math.floor(0.9 + k * failShare)) s.purchased = false;
    k++;
  }
  const activity = { people, sessions };
  activityCache.set(key, activity);
  return activity;
}

// ── Visitors ─────────────────────────────────────────────────────────────────

/** Visitor-days a day in `env` holds (§4: ≈2,400 a weekday and 1,700 a weekend day in prod). */
export function visitorsOn(env: string, day: number): number {
  const d = Math.round(day / DAY);
  if (env === "env-prod") return (isWeekend(day) ? 1700 : 2400) * (0.96 + 0.08 * hash01(d, 61));
  if (env === "env-staging") return 40 * (0.85 + 0.3 * hash01(d, 62));
  if (env === "env-dev") return 6 * (0.7 + 0.6 * hash01(d, 63));
  return 0;
}

/** A channel's share of a day's visitor-days: the newsletter goes out on Tuesdays, so Email doubles that day. */
export function channelWeights(env: string, day: number): number[] {
  if (env !== "env-prod") return CHANNELS.map((c) => (c === "Direct" ? 1 : 0));
  const tuesday = new Date(day).getUTCDay() === 2;
  const d = Math.round(day / DAY);
  return CHANNELS.map((c, i) => CHANNEL_VISITORS[c] * (c === "Email" && tuesday ? 2 : 1) * (0.92 + 0.16 * hash01(d, 64, i)));
}

export interface VisitorMix {
  /** Visitor-days that began in the window (fractional; round once). */
  visitors: number;
  /** By CHANNELS order. */
  byChannel: number[];
}

/** The window's visitor-days in `envs`, per channel: each day's, weighted by the share of its arrivals the window holds. */
export function visitorMix(envs: readonly string[], from: number, to: number): VisitorMix {
  const byChannel = CHANNELS.map(() => 0);
  let visitors = 0;
  for (const env of envs) {
    for (let day = dayStart(from); day < to; day += DAY) {
      const share = dayShare(day, from, to);
      if (!share) continue;
      const v = visitorsOn(env, day) * share;
      visitors += v;
      const w = channelWeights(env, day);
      const sum = w.reduce((a, b) => a + b, 0);
      w.forEach((x, i) => (byChannel[i] += (v * x) / sum));
    }
  }
  return { visitors, byChannel };
}
