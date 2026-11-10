// Copyright (c) 2025 Reliant Labs

/**
 * How an activity event reads: "sean resolved TypeError: x is undefined in the
 * next release", "Reliant saw a new issue …", "sean queried prod's database:
 * investigating #4412".
 *
 * The server stores the verb as written in the rpc's declaration (a token:
 * "promote", "resolve", "query_database"); phrasing it is the client's job.
 * A verb this table does not know is shown as its words, not guessed into a
 * past tense — "set run state" is clumsy, "setted run state" is wrong.
 *
 * Facts are the small declared extras worth a glance (rows, duration, the
 * statement's hash). A query's SQL is never among them: the event does not
 * carry it, only its sha256, shown short.
 */

import type { ActivityEvent } from "@/services/controlPlane/observability/activityApi";

import { formatSize } from "../Storage/storageObject-model";

export interface ActivityText {
  /** Who: a person's name, a credential's, or "Reliant" for the platform's own observations. */
  actor: string;
  /** The verb, phrased: "resolved", "queried". */
  action: string;
  /** What it was done to. */
  object: string;
  /** What follows the object: ": <reason>", "in the next release". */
  tail: string;
  /** Small facts, each one short: "12 rows", "48 ms", "sql 3f9a1c2b". */
  facts: string[];
  /** The channel, when it is worth saying (not the console, which is where the reader is). */
  via: string;
}

/** A statement's sha256 the way a commit is read: the first 8 hex characters. */
export function shortSha(sha: string): string {
  return /^[0-9a-f]{12,}$/i.test(sha) ? sha.slice(0, 8) : sha;
}

const VIA_LABEL: Record<string, string> = { cli: "CLI", ci: "CI", api: "API" };

function when(iso: string | undefined): string {
  if (!iso) return "";
  const ms = Date.parse(iso);
  if (!Number.isFinite(ms)) return iso;
  return new Date(ms).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false });
}

function plural(n: number, one: string, many = `${one}s`): string {
  return `${n.toLocaleString()} ${n === 1 ? one : many}`;
}

/** Verbs whose phrasing is fixed regardless of detail. */
const SIMPLE: Record<string, string> = {
  promote: "promoted",
  rollback: "rolled back",
  deploy: "deployed",
  restart: "restarted",
  rotate: "rotated",
  create: "created",
  update: "updated",
  delete: "deleted",
  archive: "archived",
  silence: "silenced",
  triage: "triaged",
};

function words(token: string): string {
  return token.replace(/_/g, " ");
}

export function activityText(e: ActivityEvent): ActivityText {
  const d = e.detail;
  const actor = e.actor.kind === "system" ? (e.actor.name && e.actor.name !== "reliant" ? e.actor.name : "Reliant") : e.actor.name || "Someone";
  const via = e.via && e.via !== "console" && e.via !== "reliant" ? `via ${VIA_LABEL[e.via] ?? e.via}` : "";
  const object = e.target.label || e.target.id || words(e.target.kind);
  const base = { actor, object, via };

  switch (e.verb) {
    case "resolve": {
      const tail =
        d.resolve_in === "next_release" ? "in the next release" : d.resolve_in === "version" && d.version ? `in ${d.version}` : "";
      return { ...base, action: d.from === "regressed" ? "resolved the regression" : "resolved", tail, facts: issueFacts(d) };
    }
    case "ignore":
      return { ...base, action: "ignored", tail: d.ignored_until ? `until ${when(d.ignored_until)}` : "until unignored", facts: issueFacts(d) };
    case "unresolve":
      return { ...base, action: "reopened", tail: d.from ? `(was ${d.from})` : "", facts: issueFacts(d) };
    case "issue_opened":
      return { ...base, action: "saw a new issue:", tail: "", facts: [...issueFacts(d), ...(d.version ? [`first in ${d.version}`] : [])] };
    case "issue_regressed":
      return {
        ...base,
        action: "reopened",
        tail: "as regressed",
        facts: [...issueFacts(d), ...(d.version ? [`seen again in ${d.version}`] : []), ...(d.resolved_at ? [`resolved ${when(d.resolved_at)}`] : [])],
      };
    case "query_database": {
      const env = e.env?.name;
      const db = e.target.id || e.target.label;
      const facts: string[] = [];
      const rows = Number(d.row_count);
      if (d.row_count !== undefined && Number.isFinite(rows)) facts.push(plural(rows, "row"));
      const ms = Number(d.duration_ms);
      if (d.duration_ms !== undefined && Number.isFinite(ms)) facts.push(`${ms.toLocaleString()} ms`);
      if (d.sql_sha256) facts.push(`sql ${shortSha(d.sql_sha256)}`);
      return {
        ...base,
        action: "queried",
        object: env ? `${env} DB` : db ? `the ${db} database` : "a database",
        tail: d.reason ? `: ${d.reason}` : "",
        facts: env && db ? [db, ...facts] : facts,
      };
    }
    // The storage browser (StorageBrowserService): "Ada opened the uploads
    // bucket on prod: <reason>", "Ada downloaded a file from the uploads
    // bucket on prod: <reason>" with its size. The detail carries the reason
    // and the size, never the key: the feed's audience is wider than
    // storage:read, and a key can name a person.
    case "open_bucket":
      return { ...base, action: "opened", object: `${bucketPhrase(e)}${onEnv(e)}`, tail: d.reason ? `: ${d.reason}` : "", facts: [] };
    case "download_object": {
      const size = Number(d.size);
      return {
        ...base,
        action: "downloaded",
        object: `a file from ${bucketPhrase(e)}${onEnv(e)}`,
        tail: d.reason ? `: ${d.reason}` : "",
        facts: d.size !== undefined && d.size !== "" && Number.isFinite(size) && size >= 0 ? [formatSize(size)] : [],
      };
    }
    // The data browser (AppDatabaseService): "Ada opened the shop database on
    // prod: <reason>". The environment is part of the sentence, as for a query.
    // browse_table is phrased for the events a control plane recorded while
    // BrowseTable declared it; the current one records none (a page view is an
    // audit row, not a feed line).
    case "describe_database": {
      const db = e.target.id || e.target.label;
      return {
        ...base,
        action: "opened",
        object: `${db ? `the ${db} database` : "a database"}${onEnv(e)}`,
        tail: d.reason ? `: ${d.reason}` : "",
        facts: durationFacts(d),
      };
    }
    case "browse_table": {
      const table = e.target.id || e.target.label || "a table";
      return {
        ...base,
        action: "browsed",
        object: `${table}${d.database ? ` in the ${d.database} database` : ""}${onEnv(e)}`,
        tail: d.reason ? `: ${d.reason}` : "",
        facts: durationFacts(d),
      };
    }
    default:
      return { ...base, action: SIMPLE[e.verb] ?? words(e.verb), tail: "", facts: [] };
  }
}

/**
 * Verbs whose sentence already names the environment ("queried prod DB",
 * database and bucket actions), so the row does not add "in prod".
 */
export const ENV_IN_SENTENCE: ReadonlySet<string> = new Set(["query_database", "describe_database", "browse_table", "open_bucket", "download_object"]);

function onEnv(e: ActivityEvent): string {
  return e.env?.name ? ` on ${e.env.name}` : "";
}

function bucketPhrase(e: ActivityEvent): string {
  const bucket = e.target.id || e.target.label;
  return bucket ? `the ${bucket} bucket` : "a bucket";
}

function durationFacts(d: Record<string, string>): string[] {
  const ms = Number(d.duration_ms);
  return d.duration_ms !== undefined && Number.isFinite(ms) ? [`${ms.toLocaleString()} ms`] : [];
}

/** The facts a triage or an issue observation carries: which service, which platform. */
function issueFacts(d: Record<string, string>): string[] {
  const out: string[] = [];
  if (d.service) out.push(d.service);
  if (d.platform) out.push(d.platform);
  return out;
}

/** The whole sentence, for a title attribute or a screen reader. */
export function activitySentence(e: ActivityEvent): string {
  const t = activityText(e);
  const tail = t.tail ? (t.tail.startsWith(":") ? t.tail : ` ${t.tail}`) : "";
  const where = e.env?.name && !ENV_IN_SENTENCE.has(e.verb) ? ` in ${e.env.name}` : "";
  return `${t.actor} ${t.action} ${t.object}${tail}${where}${t.via ? ` ${t.via}` : ""}`;
}
