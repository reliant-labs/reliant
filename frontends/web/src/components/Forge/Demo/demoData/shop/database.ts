// Copyright (c) 2025 Reliant Labs

/**
 * The rows of shop's Postgres database, `shop`, in one environment: its
 * customers, products, stock, orders and order items (shopStory.ts
 * DEMO_DB_TABLES). The Data browser (inventoryScenario.ts) browses them and
 * the dashboards' Postgres panels (panels.ts) aggregate them, so the two
 * agree. Deterministic and relative to `now`: orders arrive at the traffic
 * model's rate shape, and only prod has `payment_failed` orders, all since
 * v1.8.3's promotion (the incident).
 */

import { DAY, DEMO_CUSTOMERS, DEMO_ORDER_STATES, DEMO_PRODUCTS, HOUR, incidentStartMs, MINUTE } from "../shopStory";

export type Cell = string | number | boolean | null;
export type Row = Record<string, Cell>;

function hash01(...parts: number[]): number {
  let h = 0x811c9dc5;
  for (const p of parts) h = Math.imul(h ^ (p | 0), 0x01000193);
  h ^= h >>> 15;
  h = Math.imul(h, 0x2c1b3c6d);
  h ^= h >>> 12;
  return (h >>> 0) / 0x1_0000_0000;
}

/** A timestamptz as Postgres prints it. */
export const pgTime = (ms: number) => new Date(Math.floor(ms / 1000) * 1000).toISOString().replace("T", " ").replace(".000Z", "+00");

const FIRST = ["Lena", "Tomás", "Aiko", "Sam", "Chloé", "Ravi", "Noor", "Mateo", "Hannah", "Kofi", "Ines", "Jonas", "Mei", "Oscar", "Priyanka", "Lucas", "Amara", "Felix", "Sofia", "Daniel", "Yuki", "Elena", "Omar", "Grace", "Nils", "Zara", "Marco", "Freya", "Diego", "Leila"];
const LAST = ["Fischer", "Ortega", "Tanaka", "Okafor", "Martin", "Iyer", "Haddad", "Rossi", "Berg", "Mensah", "Costa", "Weber", "Lin", "Nilsson", "Rao", "Moreau", "Diallo", "Wagner", "Bianchi", "Kim", "Sato", "Popescu", "Farah", "Walsh", "Larsen", "Khan", "Ricci", "Hughes", "Vargas", "Aziz"];
const COUNTRIES = ["DE", "ES", "JP", "GB", "FR", "US", "NL", "IT", "SE", "US"];

export interface ShopDatabase {
  customers: Row[];
  products: Row[];
  stock: Row[];
  orders: Row[];
  order_items: Row[];
}

const ENV_SCALE: Record<string, number> = { "env-prod": 1, "env-staging": 0.15, "env-dev": 0.06 };

/** `env`'s database as of `now`. */
export function shopDatabase(env: string, now: number): ShopDatabase {
  const scale = ENV_SCALE[env] ?? 0.06;
  const customerCount = Math.max(12, Math.round(60 * scale));
  const customers: Row[] = Array.from({ length: customerCount }, (_, i) => {
    const story = DEMO_CUSTOMERS[i];
    const first = FIRST[i % FIRST.length];
    // Shifted once per pass through FIRST, so prod's 60 customers are 60 different people (and emails).
    const last = LAST[(i * 7 + Math.floor(i / FIRST.length)) % LAST.length];
    return {
      id: story?.id ?? `cus_${Math.floor(hash01(i, 1) * 0xffffff).toString(16).padStart(6, "0")}`,
      email: story?.email ?? `${first.toLowerCase().normalize("NFD").replace(/[^a-z]/g, "")}.${last.toLowerCase()}@example.com`,
      name: story?.name ?? `${first} ${last}`,
      country: story?.country ?? COUNTRIES[i % COUNTRIES.length],
      identity_subject: `sub_${Math.floor(hash01(i, 2) * 0xffffffff).toString(16).padStart(8, "0")}`,
      created_at: pgTime(now - (120 - i) * DAY - Math.floor(hash01(i, 3) * DAY)),
    };
  });
  const products: Row[] = DEMO_PRODUCTS.map((p, i) => ({
    id: `prd_${(i + 1).toString().padStart(3, "0")}`,
    sku: p.sku,
    name: p.name,
    price_cents: p.price,
    active: p.sku !== "PEN-BRS-01",
    created_at: pgTime(now - (200 - i * 9) * DAY),
  }));
  // Orders: the last week, one every ~25 minutes at prod's scale; the last six hours in full (one every ~90s).
  const orders: Row[] = [];
  const order_items: Row[] = [];
  const incidentAt = incidentStartMs(now);
  const span = 7 * DAY;
  const every = 25 * MINUTE / scale;
  let itemId = 1;
  for (let k = 0, t = now - span; t < now; k++, t += (now - t > 6 * HOUR ? every : (90_000 / scale)) * (0.5 + hash01(k, 4))) {
    const customer = customers[Math.floor(hash01(k, 5) * customers.length)];
    const age = now - t;
    const lines = 1 + Math.floor(hash01(k, 6) * 3);
    let total = 0;
    const id = `ord_${Math.floor(hash01(k, 7) * 0xffffff).toString(16).padStart(6, "0")}`;
    for (let l = 0; l < lines; l++) {
      const product = DEMO_PRODUCTS[Math.floor(hash01(k, 8, l) * DEMO_PRODUCTS.length)];
      const quantity = 1 + Math.floor(hash01(k, 9, l) * 2);
      total += product.price * quantity;
      order_items.push({ id: itemId++, order_id: id, sku: product.sku, quantity, unit_price_cents: product.price });
    }
    const failed = env === "env-prod" && t >= incidentAt && hash01(k, 10) < 0.124;
    const roll = hash01(k, 11);
    const state: (typeof DEMO_ORDER_STATES)[number] = failed
      ? "payment_failed"
      : roll < 0.02
        ? "cancelled"
        : age < 2 * HOUR
          ? "paid"
          : age < 2 * DAY
            ? "shipped"
            : "delivered";
    orders.push({
      id,
      customer_id: customer.id,
      state,
      total_cents: total,
      currency: "EUR",
      created_at: pgTime(t),
      paid_at: state === "payment_failed" || state === "cancelled" ? null : pgTime(t + 4_000 + hash01(k, 12) * 2_000),
    });
  }
  orders.reverse();
  const reservedBySku = new Map<string, number>();
  const stateOf = new Map(orders.map((o) => [o.id, o.state]));
  for (const item of order_items) {
    if (stateOf.get(item.order_id) === "paid") reservedBySku.set(String(item.sku), (reservedBySku.get(String(item.sku)) ?? 0) + Number(item.quantity));
  }
  const stock: Row[] = DEMO_PRODUCTS.map((p, i) => ({
    sku: p.sku,
    on_hand: p.sku === "KETL-GOOSE-BLK" ? 3 : 12 + Math.floor(hash01(i, 13) * 140),
    reserved: reservedBySku.get(p.sku) ?? 0,
    updated_at: pgTime(now - Math.floor(hash01(i, 14) * 6 * HOUR)),
  }));
  return { customers, products, stock, orders, order_items };
}
