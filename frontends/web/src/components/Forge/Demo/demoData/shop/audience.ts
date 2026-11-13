// Copyright (c) 2025 Reliant Labs

/**
 * shop's AUDIENCE: every account that can sign in to shop, per environment.
 * ONE population, read by every surface that counts people: the identity
 * provider's users (the console's Users page, through inventoryScenario.ts's
 * IdentityService) and the product-analytics views (accounts, new, returning,
 * active, locked). So the Users page's total and a dashboard's "accounts"
 * cannot disagree.
 *
 *   the team      jon, maya and priya (shopStory DEMO_CAST): admin, admin,
 *                 support. jon's admin role is declared in KCL. The oldest
 *                 accounts, in every environment.
 *   customers     Data's `customers` table (shop/database.ts) is a subset: each
 *                 row's `identity_subject` is an account's id, its email, name
 *                 and created_at the account's.
 *   the rest      generated deterministically to DEMO_AUDIENCE_SIZE. In prod,
 *                 people who signed up over the shop's ~14 months, faster
 *                 lately; in staging and dev, test shoppers.
 *   prod also     one support hire invited two days ago (not signed in yet),
 *                 two customers locked out, one account disabled on request.
 *   staging also  one test shopper locked by the lockout test.
 *
 * Accounts are ordered oldest first, as the real ListUsers sorts them (by
 * creation date). Every time is an offset from demoNow(), the page load.
 */

import { DAY, DEMO_CAST, HOUR, MINUTE, demoNow } from "../shopStory";
import { shopDatabase } from "./database";

export type DemoAccountState = "active" | "invited" | "locked" | "disabled";

export interface DemoAccount {
  /** The identity subject; a customer's `customers.identity_subject`. */
  id: string;
  email: string;
  name: string;
  state: DemoAccountState;
  roles: string[];
  createdAtMs: number;
  /** The last successful sign-in; null: never signed in (an invitation not yet accepted). */
  lastSignInAtMs: number | null;
}

/** How many accounts each environment has: ListUsers' total_size. */
export const DEMO_AUDIENCE_SIZE: Record<string, number> = { "env-prod": 1284, "env-staging": 48, "env-dev": 19 };

/** The roles shop's identity declares. `admin` is `members_from = declared` (jon), so it is read-only in the console. */
export const DEMO_IDENTITY_ROLES = [
  { key: "admin", displayName: "Administrators", declared: true },
  { key: "support", displayName: "Customer support", declared: false },
  { key: "customer", displayName: "Customers", declared: false },
] as const;

const TEAM: Record<string, { name: string; roles: string[]; createdDaysAgo: number; lastSignInAgo: number }> = {
  jon: { name: "Jon", roles: ["admin"], createdDaysAgo: 430, lastSignInAgo: 2 * HOUR },
  // maya is on the incident (DEMO_INCIDENT): she signed in minutes ago.
  maya: { name: "Maya", roles: ["admin"], createdDaysAgo: 428, lastSignInAgo: 12 * MINUTE },
  priya: { name: "Priya", roles: ["support"], createdDaysAgo: 401, lastSignInAgo: 26 * HOUR },
};

const FIRST = ["Ada", "Bruno", "Camille", "Dev", "Elif", "Finn", "Gita", "Hugo", "Isla", "Jae", "Kai", "Lea", "Milan", "Nadia", "Otto", "Paula", "Quinn", "Rosa", "Stefan", "Tara", "Umar", "Vera", "Wim", "Xenia", "Yara", "Zeno", "Alma", "Boris", "Clara", "Dario", "Emil", "Fatima", "Gus", "Hana", "Ivo", "Jule", "Karim", "Luz", "Mads", "Nina", "Anya", "Ben", "Cora", "Dmitri", "Esme", "Farid", "Greta", "Hiro", "Ilse", "Joel", "Kira", "Leon", "Maren", "Nico", "Olga", "Pablo", "Rhea", "Sven", "Thea", "Ugo", "Viktor", "Wren", "Yusuf", "Zoe", "Aria", "Bilal", "Cleo", "Dina", "Erik", "Flora", "Gael", "Hedda", "Imran", "Jana", "Kenji", "Lior", "Mila", "Nils", "Odette", "Pia"];
const LAST = ["Abbott", "Brennan", "Castillo", "Dubois", "Eriksen", "Ferreira", "Gallo", "Holm", "Ibrahim", "Jansen", "Kowalski", "Lambert", "Molina", "Novak", "Oyelaran", "Petrov", "Quintero", "Reyes", "Schmidt", "Takahashi", "Uddin", "Varga", "Whitfield", "Yilmaz", "Zimmer", "Almeida", "Bauer", "Chen", "Dahl", "Esposito", "Andersson", "Becker", "Coelho", "Duarte", "Engel", "Fontaine", "Gutierrez", "Hartmann", "Ivanova", "Jovanovic", "Keller", "Lindgren", "Marchetti", "Nakamura", "Olsen", "Park", "Richter", "Silva", "Torres", "Vogel", "Wolff", "Young", "Zhou", "Arslan", "Brandt", "Carvalho", "Dimitrov", "Eze", "Fischer", "Greco"];

function hash01(...parts: number[]): number {
  let h = 0x9e3779b9;
  for (const p of parts) h = Math.imul(h ^ (p | 0), 0x01000193);
  h ^= h >>> 15;
  h = Math.imul(h, 0x2c1b3c6d);
  h ^= h >>> 12;
  return (h >>> 0) / 0x1_0000_0000;
}

const ENV_SEED: Record<string, number> = { "env-prod": 1, "env-staging": 2, "env-dev": 3 };
const pgMs = (value: unknown) => Date.parse(String(value).replace(" ", "T").replace(/\+00$/, "Z"));
const slug = (text: string) => text.toLowerCase().normalize("NFD").replace(/[^a-z]/g, "");

/**
 * When someone who joined at `createdAtMs` last signed in: about a third within a
 * day, a quarter within the week, a fifth within the month, the rest longer
 * ago — never before they joined.
 */
function lastSignIn(now: number, createdAtMs: number, roll: number, jitter: number): number {
  const recency =
    roll < 0.32 ? jitter * DAY : roll < 0.57 ? DAY + jitter * 6 * DAY : roll < 0.77 ? 7 * DAY + jitter * 23 * DAY : 30 * DAY + jitter * 300 * DAY;
  return Math.max(createdAtMs, Math.floor(now - recency));
}

const cache = new Map<string, DemoAccount[]>();

/** `env`'s accounts, oldest first, as of the page load. An environment that is not shop's has none. */
export function demoAudience(env: string): DemoAccount[] {
  const size = DEMO_AUDIENCE_SIZE[env];
  if (size === undefined) return [];
  const now = demoNow();
  const key = `${env}@${now}`;
  const hit = cache.get(key);
  if (hit) return hit;

  const seed = ENV_SEED[env];
  const accounts: DemoAccount[] = [];
  const emails = new Set<string>();
  const ids = new Set<string>();
  const add = (account: DemoAccount) => {
    accounts.push(account);
    emails.add(account.email);
    ids.add(account.id);
  };

  for (const person of DEMO_CAST) {
    const team = TEAM[person.handle];
    add({
      id: `sub_${person.id.replace(/^user-/, "")}${seed}`,
      email: person.email,
      name: team.name,
      state: "active",
      roles: team.roles,
      createdAtMs: now - team.createdDaysAgo * DAY,
      lastSignInAtMs: now - team.lastSignInAgo - seed * 7 * MINUTE,
    });
  }

  const customers = shopDatabase(env, now).customers;
  customers.forEach((row, i) => {
    const createdAtMs = pgMs(row.created_at);
    add({
      id: String(row.identity_subject),
      email: String(row.email),
      name: String(row.name),
      state: "active",
      roles: ["customer"],
      createdAtMs,
      lastSignInAtMs: lastSignIn(now, createdAtMs, hash01(seed, i, 11), hash01(seed, i, 12)),
    });
  });

  if (env === "env-prod") {
    add({
      id: "sub_7c1e04a9",
      email: "ada.brennan@example.com",
      name: "Ada Brennan",
      state: "invited",
      roles: ["support"],
      createdAtMs: now - 2 * DAY - 3 * HOUR,
      lastSignInAtMs: null,
    });
  }

  const generated = size - accounts.length;
  for (let i = 0; i < generated; i++) {
    let email: string;
    let name: string;
    let createdAtMs: number;
    if (env === "env-prod") {
      const first = FIRST[Math.floor(hash01(seed, i, 1) * FIRST.length)];
      const last = LAST[Math.floor(hash01(seed, i, 2) * LAST.length)];
      name = `${first} ${last}`;
      // The address shapes people pick, so 1,284 of them read as people rather than a pattern.
      const f = slug(first);
      const l = slug(last);
      const shapes = [`${f}.${l}`, `${f}${l}`, `${f[0]}${l}`, `${f}.${l[0]}`, `${f}_${l}`, `${l}.${f}`];
      const shape = Math.floor(hash01(seed, i, 7) * shapes.length);
      email = `${shapes[shape]}@example.com`;
      for (let k = 1; emails.has(email) && k < shapes.length; k++) email = `${shapes[(shape + k) % shapes.length]}@example.com`;
      for (let n = 0; emails.has(email); n++) email = `${f}.${l}${70 + Math.floor(hash01(seed, i, 8, n) * 30)}@example.com`;
      // ~14 months of sign-ups, more of them lately: about 50 in the last week.
      createdAtMs = Math.floor(now - 420 * DAY * hash01(seed, i, 3) ** 1.3);
    } else {
      const n = String(i + 1).padStart(2, "0");
      name = `Test Shopper ${n}`;
      email = `shopper+${env === "env-staging" ? "stg" : "dev"}${n}@example.com`;
      createdAtMs = Math.floor(now - (40 - i) * DAY - hash01(seed, i, 3) * DAY);
    }
    let id = `sub_${Math.floor(hash01(seed, i, 4) * 0xffffffff).toString(16).padStart(8, "0")}`;
    while (ids.has(id)) id = `${id}0`;
    add({
      id,
      email,
      name,
      state: "active",
      roles: ["customer"],
      createdAtMs,
      lastSignInAtMs: lastSignIn(now, createdAtMs, hash01(seed, i, 5), hash01(seed, i, 6)),
    });
  }

  accounts.sort((a, b) => a.createdAtMs - b.createdAtMs);

  // The few who are not active, picked deterministically from the generated customers.
  const generatedCustomers = accounts.filter((a) => a.roles[0] === "customer" && !customers.some((c) => c.identity_subject === a.id));
  const mark = (state: DemoAccountState, count: number, salt: number) => {
    for (let k = 0, picked = 0; picked < count && k < 64; k++) {
      const account = generatedCustomers[Math.floor(hash01(seed, salt, k) * generatedCustomers.length)];
      if (!account || account.state !== "active") continue;
      account.state = state;
      picked++;
    }
  };
  if (env === "env-prod") {
    mark("locked", 2, 21);
    mark("disabled", 1, 22);
  } else if (env === "env-staging") {
    mark("locked", 1, 21);
  }

  cache.set(key, accounts);
  return accounts;
}

/** `env`'s head counts by state: what the Users page's total and every analytics "accounts" figure agree on. */
export function demoAudienceTotals(env: string): Record<DemoAccountState, number> & { accounts: number } {
  const totals = { accounts: 0, active: 0, invited: 0, locked: 0, disabled: 0 };
  for (const account of demoAudience(env)) {
    totals.accounts++;
    totals[account.state]++;
  }
  return totals;
}
