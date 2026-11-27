#!/usr/bin/env node
// Copyright (c) 2026 Reliant Labs

/**
 * Writes src/fixtures/shop/data/gtmLibrary.gen.ts: the subset of the embedded
 * GTM idea bank (internal/gtm/library/ideas/*.yaml) that the shop fixture pack
 * serves as its strategy library. The entries are copied as the bank holds
 * them, field for field, so the preview shows real bank content and never a
 * hand-typed copy that drifts. Which entries is KEYS below; everything else
 * comes from the YAML.
 *
 *   node scripts/gtm/gen-library-fixture.mjs                 read the working tree's bank
 *   node scripts/gtm/gen-library-fixture.mjs --ref <git-ref> read the bank at a commit
 *   node scripts/gtm/gen-library-fixture.mjs --check [...]   fail if the file is stale
 *
 * Run from frontends/web. A key the bank does not hold fails the run.
 */

import { execFileSync } from "node:child_process";
import { existsSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { load } from "js-yaml";

/**
 * The fixture's subset, in no particular order (the output is in bank order).
 * Every one of the 13 categories; in-person and community entries; founder-led
 * and high-touch outreach; pricing and retention entries tied to one business
 * model (subscription, usage-based, one-time, open source); several with a
 * suggested workflow. None may trip the sample-copy denylist (gtm.test.ts).
 */
const KEYS = [
  // pricing_offers_incentives: one per business model
  "annual-prepay-discount",
  "usage-free-allowance-and-commit-discount",
  "launch-price-with-dated-increase",
  "open-core-paid-tier",
  // referral_viral_growth
  "double-sided-referral-rewards",
  "gift-subscriptions",
  // paid_acquisition
  "sponsor-niche-newsletters",
  // organic_content_seo
  "founder-long-form-blog",
  "own-newsletter",
  // outbound_sales_prospecting: founder-led and high-touch
  "founder-led-sales-first-customers",
  "handwritten-notes-and-gifts",
  // partnerships_distribution
  "co-marketing-webinar",
  // launches_pr
  "launch-directory-day",
  "trade-press-pitch",
  // community_events: in person
  "host-local-meetup-series",
  "conference-speaking",
  "founder-hosted-dinners",
  // product_led_growth
  "activation-checklist-trial",
  // conversion_optimization
  "abandoned-checkout-recovery",
  // retention_expansion: subscription, usage-based, any
  "failed-payment-recovery",
  "usage-milestone-expansion-outreach",
  "win-back-campaign",
  // trust_reputation
  "customer-case-studies",
  "ask-every-customer-for-a-review",
  // market_research_positioning
  "customer-discovery-interviews",
];

/** The bank's category order (internal/gtm/library Categories()), which is also the proto enum's. */
const CATEGORIES = [
  "pricing_offers_incentives", "referral_viral_growth", "paid_acquisition", "organic_content_seo", "outbound_sales_prospecting",
  "partnerships_distribution", "launches_pr", "community_events", "product_led_growth", "conversion_optimization",
  "retention_expansion", "trust_reputation", "market_research_positioning",
];

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");
const REPO = resolve(WEB, "..", "..");
const BANK_DIR = "internal/gtm/library/ideas";
const OUT = join(WEB, "src", "fixtures", "shop", "data", "gtmLibrary.gen.ts");

const args = process.argv.slice(2);
const check = args.includes("--check");
const refAt = args.indexOf("--ref");
const ref = refAt >= 0 ? args[refAt + 1] : null;
if (refAt >= 0 && !ref) throw new Error("--ref needs a git ref");

const git = (...a) => execFileSync("git", a, { cwd: REPO, encoding: "utf8" });

/** The bank's files as [name, text], from the working tree or from `ref`. */
function bankFiles() {
  if (ref) {
    return git("ls-tree", "--name-only", `${ref}:${BANK_DIR}`)
      .split("\n")
      .filter((n) => n.endsWith(".yaml"))
      .map((n) => [n, git("show", `${ref}:${BANK_DIR}/${n}`)]);
  }
  const dir = join(REPO, BANK_DIR);
  if (!existsSync(dir)) throw new Error(`${BANK_DIR} is not in this tree: pass --ref <a commit that has the idea bank>`);
  return readdirSync(dir).filter((n) => n.endsWith(".yaml")).map((n) => [n, readFileSync(join(dir, n), "utf8")]);
}

const source = ref ? git("rev-parse", `${ref}^{commit}`).trim() : `${git("rev-parse", "HEAD").trim()} (working tree)`;

/** Every bank entry, in bank order: category order, then each file's own order. */
const bank = bankFiles()
  .flatMap(([name, text]) => (load(text)?.entries ?? []).map((e) => ({ ...e, file: name })))
  .sort((a, b) => CATEGORIES.indexOf(a.category) - CATEGORIES.indexOf(b.category));

const missing = KEYS.filter((k) => !bank.some((e) => e.key === k));
if (missing.length) throw new Error(`the bank does not hold: ${missing.join(", ")}`);
if (new Set(KEYS).size !== KEYS.length) throw new Error("a key is listed twice in KEYS");

/** One entry as the fixture holds it: every bank field, defaults filled so the shape is fixed. */
const entry = (e) => ({
  key: e.key,
  category: e.category,
  name: e.name,
  summary: e.summary ?? "",
  why_it_works: e.why_it_works ?? "",
  business_types: e.fits?.business_types ?? [],
  business_models: e.fits?.business_models ?? [],
  stages: e.stages ?? [],
  objective: e.objective ?? "",
  effort: e.effort ?? "",
  cost: e.cost ?? "",
  signal_days: e.signal_days ?? 0,
  tailor_questions: e.tailor_questions ?? [],
  suggested_workflow: e.suggested_workflow ?? "",
  channel_types: e.channel_types ?? [],
  kpis: e.kpis ?? [],
});

const chosen = bank.filter((e) => KEYS.includes(e.key)).map(entry);

const out = `// Copyright (c) 2026 Reliant Labs
// GENERATED by scripts/gtm/gen-library-fixture.mjs from ${BANK_DIR}/*.yaml. DO NOT EDIT.
// Change the subset in the script's KEYS and run it again.

/** One idea-bank entry as the bank's YAML holds it (internal/gtm/library Entry), fits flattened. */
export interface GtmBankEntry {
  key: string;
  category: string;
  name: string;
  summary: string;
  why_it_works: string;
  business_types: readonly string[];
  business_models: readonly string[];
  stages: readonly string[];
  objective: string;
  effort: string;
  cost: string;
  signal_days: number;
  tailor_questions: readonly string[];
  suggested_workflow: string;
  channel_types: readonly string[];
  kpis: readonly string[];
}

/** The commit the subset was read from. */
export const GTM_BANK_SOURCE = ${JSON.stringify(source)};

/** ${chosen.length} entries of the bank, in bank order (category, then file order). */
export const GTM_BANK_SUBSET: readonly GtmBankEntry[] = ${JSON.stringify(chosen, null, 2)};
`;

if (check) {
  const current = existsSync(OUT) ? readFileSync(OUT, "utf8") : "";
  // The source line names a commit; a regenerate from the same content at another commit is not drift.
  const body = (s) => s.replace(/^export const GTM_BANK_SOURCE = .*$/m, "");
  if (body(current) !== body(out)) {
    console.error(`${OUT} is stale: run scripts/gtm/gen-library-fixture.mjs${ref ? ` --ref ${ref}` : ""}`);
    process.exit(1);
  }
  console.log(`${OUT} is up to date (${chosen.length} entries)`);
} else {
  writeFileSync(OUT, out);
  console.log(`wrote ${OUT}: ${chosen.length} entries from ${source}`);
}
