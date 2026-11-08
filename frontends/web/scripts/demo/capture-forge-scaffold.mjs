#!/usr/bin/env node
// Copyright (c) 2025 Reliant Labs

/**
 * The recorder behind capture-forge-scaffold.sh: runs the steps, keeps their
 * real output, and counts what forge wrote. Run the .sh, not this; it builds
 * the pinned forge and puts it on PATH first.
 *
 * Writes two fixtures (schemas in README.md, parsed by howBuilt/fixtures.ts):
 *   forge-scaffold-cast.json      every step: the command, its output with
 *                                 timings (waits over IDLE_LIMIT shortened to
 *                                 it), its exit code and how long it really took
 *   forge-scaffold-manifest.json  every file in the finished app by category,
 *                                 with line counts; what you wrote vs what forge
 *                                 wrote; and excerpts for one entity, Order
 */

import { spawn, execFileSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync, realpathSync } from "node:fs";
import { join } from "node:path";
import { homedir } from "node:os";

export const CAST_SCHEMA = "forge-demo.scaffold-cast/v1";
export const MANIFEST_SCHEMA = "forge-demo.scaffold-manifest/v1";
const PRODUCER = "frontends/web/scripts/demo/capture-forge-scaffold.sh";

/** A pause longer than this plays as this (asciinema's idle_time_limit). The real time is kept per step. */
const IDLE_LIMIT = 1.0;
/** Output chunks closer together than this (after shortening) play as one. */
const COALESCE = 0.05;

const PROJECT = "shop";
const NEW_COMMAND = "forge project new shop --service orders --service inventory --frontend web";

// What a person writes: one entity and one custom rpc per service. Each replaces
// the "TODO: Add your RPC methods here." line forge leaves in the service.
const TODO_LINE = "  // TODO: Add your RPC methods here.\n}\n";
const HAND_WRITTEN = [
  {
    path: "proto/services/orders/v1/orders.proto",
    text: `  // CancelOrder cancels an order that has not shipped yet.
  rpc CancelOrder(CancelOrderRequest) returns (CancelOrderResponse) {
    option (forge.v1.method) = {
      activity: { verb: "cancel" target: { kind: "order" id: "id" } }
    };
  }
}

// forge:entity
message Order {
  string customer_id = 2;
  int64 total_cents = 3;
  string status = 4;
}

message CancelOrderRequest {
  string id = 1;
}

message CancelOrderResponse {
  Order order = 1;
}
`,
  },
  {
    path: "proto/services/inventory/v1/inventory.proto",
    text: `  // Reserve holds stock for an order.
  rpc Reserve(ReserveRequest) returns (ReserveResponse) {
    option (forge.v1.method) = {
      activity: { verb: "reserve" target: { kind: "stock" id: "sku" } }
    };
  }
}

// forge:entity
message Stock {
  string sku = 2;
  int64 available = 3;
  int64 reserved = 4;
}

message ReserveRequest {
  string sku = 1;
  int64 quantity = 2;
  string order_id = 3;
}

message ReserveResponse {
  Stock stock = 1;
}
`,
  },
];

const AFTER = ["forge scaffold", "forge lint --quiet", "go build ./..."];

/**
 * Categories, first match wins (so tests and mocks are picked out before the
 * directory they sit in). Every file lands in exactly one; `project` takes the rest.
 */
const CATEGORIES = [
  ["tests", (p) => /(_test\.go|\.test\.tsx?|\.spec\.tsx?)$/.test(p) || /^e2e\//.test(p) || /\/testdata\//.test(p)],
  ["mocks", (p) => /\/mocks\//.test(p) || /mock-transport/.test(p) || /(^|\/)mock[^/]*_gen\.go$/.test(p)],
  ["observability", (p) => /^deploy\/observability\//.test(p) || /^internal\/metrics\//.test(p) || /(^|\/)(otel|observe|telemetry)[^/]*$/.test(p)],
  ["contract", (p) => /^proto\//.test(p) || /^gen\//.test(p) || /^frontends\/[^/]+\/src\/gen\//.test(p) || /(^|\/)buf(\.gen)?(\.[a-z]+)?\.yaml$/.test(p)],
  ["migrations", (p) => /^db\/.*\.sql$/.test(p) || /^db\/seeds\//.test(p)],
  ["store", (p) => /^internal\/db\//.test(p) || /^db\/[^/]+\.go$/.test(p)],
  ["handlers", (p) => /^internal\/handlers\//.test(p)],
  ["deploy", (p) => /^deploy\//.test(p) || /(^|\/)Dockerfile[^/]*$/.test(p) || /(^|\/)\.dockerignore$/.test(p) || /^docker-compose[^/]*\.ya?ml$/.test(p) || /^secrets\//.test(p) || /^idp-steps\.yaml$/.test(p)],
  ["ci", (p) => /^\.github\//.test(p) || /^\.githooks\//.test(p) || /^\.pre-commit-config\.yaml$/.test(p) || /^\.golangci\.ya?ml$/.test(p) || /^Taskfile\.ya?ml$/.test(p) || /^scripts\//.test(p)],
  ["frontend", (p) => /^frontends\//.test(p)],
  ["wiring", (p) => /^(cmd|pkg|internal)\//.test(p)],
  ["project", () => true],
];

/** Files left out of every count: dependency lockfiles, forge's own bookkeeping, binaries. */
const LOCKFILES = new Set(["go.sum", "go.work.sum", "pnpm-lock.yaml", "package-lock.json", "yarn.lock", "kcl.mod.lock"]);

/**
 * The entity chain the panel clicks through, for Order. Each excerpt starts at
 * `anchor` (whose following line must match `next`, when given; with the
 * comment lines just above it when `comments`), and ends at
 * the anchor's closing brace (`brace`), at the first line matching `at` after
 * one matching `after`, or at the end of the file (`whole`). If forge changes a
 * file's shape so an anchor no longer matches, the run fails rather than
 * committing a wrong excerpt.
 */
const CHAIN = [
  { id: "proto", file: "proto/services/orders/v1/orders.proto", lang: "protobuf", anchor: /^\/\/ forge:entity$/, next: /^message Order \{$/, end: "brace" },
  { id: "migration", file: /^db\/migrations\/\d+_create_orders\.up\.sql$/, lang: "sql", whole: true },
  { id: "orm", file: "internal/db/order_orm_gen.go", lang: "go", anchor: /^type Order struct \{$/, comments: true, end: "brace" },
  { id: "handler", file: "internal/handlers/orders/handlers_crud.go", lang: "go", anchor: /^func \(s \*Service\) CreateOrder\($/, comments: true, end: "brace" },
  { id: "hook", file: "frontends/web/src/hooks/orders-service-hooks_gen.ts", lang: "typescript", anchor: /^export const useCreateOrder /, end: { after: /^export const useListOrders /, at: /^\);$/ } },
  { id: "test", file: "internal/handlers/orders/handlers_crud_test.go", lang: "go", anchor: /^func TestCRUD_Order_Lifecycle\(/, comments: true, end: "brace", max: 30 },
];

// ── arguments ───────────────────────────────────────────────────────────────

function args() {
  const out = {};
  const argv = process.argv.slice(2);
  for (let i = 0; i < argv.length; i += 2) out[argv[i].replace(/^--/, "")] = argv[i + 1];
  for (const key of ["work", "out", "forge-version"]) {
    if (!out[key]) throw new Error(`capture: --${key} is required (run capture-forge-scaffold.sh)`);
  }
  return out;
}

// ── running and recording ──────────────────────────────────────────────────

/** Paths that would only describe this machine, longest first, and what a reader sees instead. */
function scrubber(work) {
  const pairs = [];
  const add = (from, to) => {
    if (!from) return;
    pairs.push([from, to]);
    try {
      const real = realpathSync(from);
      if (real !== from) pairs.push([real, to]);
    } catch {
      // not a path on this machine
    }
  };
  add(work, "~");
  add(process.env.GOMODCACHE || safe(() => execFileSync("go", ["env", "GOMODCACHE"]).toString().trim()), "~/go/pkg/mod");
  add(process.env.XDG_CACHE_HOME, "~/.cache");
  add(process.env.HOME || homedir(), "~");
  pairs.sort((a, b) => b[0].length - a[0].length);
  const ansi = /\u001b\[[0-9;?]*[ -/]*[@-~]/g;
  return (text) => {
    let out = text.replace(ansi, "");
    for (const [from, to] of pairs) out = out.split(from).join(to);
    return out;
  };
}

function safe(fn) {
  try {
    return fn();
  } catch {
    return "";
  }
}

/**
 * The recorded commands' environment: a reader's plain shell. Only the
 * toolchains' own settings pass through, so nothing about the machine running
 * the capture (an agent harness, a CI's variables) changes what forge does.
 */
const ENV_ALLOW = /^(PATH|HOME|USER|LOGNAME|SHELL|LANG|LC_[A-Z]+|TZ|TMPDIR|GO[A-Z0-9_]*|XDG_[A-Z_]+|GIT_(AUTHOR|COMMITTER)_[A-Z]+|SSL_CERT_(FILE|DIR)|DOCKER_HOST|PNPM_HOME|(npm|NPM|pnpm|PNPM)_config_[A-Za-z_]+|COREPACK_[A-Z_]+)$/;

function readerEnv() {
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => ENV_ALLOW.test(key)));
  return { ...env, NO_COLOR: "1", COLUMNS: "100" };
}

/** Runs one command in bash, keeping each output chunk with the second it arrived. */
function record(command, cwd, scrub) {
  return new Promise((resolve, reject) => {
    const started = performance.now();
    const chunks = [];
    const child = spawn("bash", ["-c", command], {
      cwd,
      env: readerEnv(),
      stdio: ["ignore", "pipe", "pipe"],
    });
    const take = (buf) => {
      chunks.push([(performance.now() - started) / 1000, buf.toString("utf8")]);
      process.stdout.write(buf);
    };
    child.stdout.on("data", take);
    child.stderr.on("data", take);
    child.on("error", reject);
    child.on("close", (code) => {
      const real = (performance.now() - started) / 1000;
      resolve({ exitCode: code ?? 1, real, events: compress(chunks, real, scrub) });
    });
  });
}

/** Shortens every wait to IDLE_LIMIT, merges near chunks, and scrubs paths. Returns events and the played length. */
function compress(chunks, real, scrub) {
  const events = [];
  let last = 0;
  let played = 0;
  for (const [at, data] of chunks) {
    played += Math.min(at - last, IDLE_LIMIT);
    last = at;
    const text = scrub(data);
    const prev = events[events.length - 1];
    if (prev && played - prev[0] < COALESCE) prev[1] += text;
    else events.push([round(played), text]);
  }
  played += Math.min(real - last, IDLE_LIMIT);
  return { events, duration: round(played) };
}

const round = (n) => Math.round(n * 1000) / 1000;

function git(cwd, ...argv) {
  return execFileSync("git", argv, { cwd, maxBuffer: 64 << 20 }).toString();
}

function commitAll(cwd, message) {
  git(cwd, "add", "-A");
  if (git(cwd, "status", "--porcelain").trim() === "") return;
  git(cwd, "commit", "-q", "--no-verify", "-m", message);
}

/** `git diff --name-status` of the staged tree against HEAD, as {added, changed, removed} path lists. */
function stagedChanges(cwd) {
  git(cwd, "add", "-A");
  const out = { added: [], changed: [], removed: [] };
  for (const line of git(cwd, "diff", "--cached", "--name-status", "--no-renames", "HEAD").split("\n")) {
    const [status, path] = line.split("\t");
    if (!path) continue;
    if (status === "A") out.added.push(path);
    else if (status === "D") out.removed.push(path);
    else out.changed.push(path);
  }
  return out;
}

// ── counting ────────────────────────────────────────────────────────────────

function countLines(buf) {
  if (buf.length === 0) return 0;
  let n = 0;
  for (const byte of buf) if (byte === 10) n++;
  return buf[buf.length - 1] === 10 ? n : n + 1;
}

function categoryOf(path) {
  return CATEGORIES.find(([, match]) => match(path))[0];
}

function exclusionOf(path, buf) {
  if (path.startsWith(".forge/")) return "forge_state";
  if (LOCKFILES.has(path.split("/").pop())) return "lockfile";
  if (buf.includes(0)) return "binary";
  return null;
}

/** "regen": forge rewrites it on every generate. "yours": written once, then the project's. */
function ownershipOf(project, path, text) {
  if (isMachine(path) || existsSync(join(project, ".forge/hashes", `${path}.sha256`))) return "regen";
  const head = text.split("\n", 8).join("\n");
  return /Code generated .*DO NOT EDIT|forge-owned: regenerated/.test(head) ? "regen" : "yours";
}

/** protoc / buf output (Go and TypeScript stubs, descriptors): regenerated from the protos, never edited. */
function isMachine(path) {
  return /^gen\//.test(path) || /^frontends\/[^/]+\/src\/gen\//.test(path);
}

// ── excerpts ────────────────────────────────────────────────────────────────

function excerpt(project, files, spec, yourAdded) {
  const yourLines = new Set(yourAdded.get(typeof spec.file === "string" ? spec.file : "") ?? []);
  const path = typeof spec.file === "string" ? spec.file : files.find((f) => spec.file.test(f));
  if (!path || !files.includes(path)) throw new Error(`capture: excerpt ${spec.id}: no file ${spec.file}`);
  const lines = readFileSync(join(project, path), "utf8").replace(/\n$/, "").split("\n");
  let start = 0;
  let end = lines.length - 1;
  if (!spec.whole) {
    start = lines.findIndex((line, i) => spec.anchor.test(line) && (!spec.next || spec.next.test(lines[i + 1] ?? "")));
    if (start < 0) throw new Error(`capture: excerpt ${spec.id}: ${spec.anchor} not found in ${path}`);
    end = endOf(lines, start + (spec.next ? 1 : 0), spec.end);
    if (end < 0) throw new Error(`capture: excerpt ${spec.id}: no end for ${spec.anchor} in ${path}`);
    if (spec.comments) while (start > 0 && /^\s*\/\//.test(lines[start - 1])) start--;
  }
  const truncated = spec.max !== undefined && end - start + 1 > spec.max;
  if (truncated) end = start + spec.max - 1;
  const code = lines.slice(start, end + 1);
  return {
    id: spec.id,
    path,
    lang: spec.lang,
    start_line: start + 1,
    end_line: end + 1,
    file_lines: lines.length,
    truncated,
    // 1-based lines of the excerpt that are word for word what you wrote.
    yours: code.flatMap((line, i) => (line.trim() !== "" && yourLines.has(line) ? [i + 1] : [])),
    code: code.join("\n"),
  };
}

function endOf(lines, from, end) {
  if (end === "brace") {
    let depth = 0;
    let opened = false;
    for (let i = from; i < lines.length; i++) {
      for (const ch of lines[i].replace(/\/\/.*$/, "").replace(/"(?:[^"\\]|\\.)*"|`[^`]*`/g, "")) {
        if (ch === "{") {
          depth++;
          opened = true;
        } else if (ch === "}") depth--;
      }
      if (opened && depth <= 0) return i;
    }
    return -1;
  }
  const after = lines.findIndex((line, i) => i >= from && end.after.test(line));
  if (after < 0) return -1;
  return lines.findIndex((line, i) => i >= after && end.at.test(line));
}

// ── output ──────────────────────────────────────────────────────────────────

/** JSON with objects indented and each row of a list on one line: small, and readable in a diff. */
function pretty(value, indent = "") {
  const next = indent + "  ";
  if (Array.isArray(value)) {
    if (value.length === 0) return "[]";
    if (value.every((v) => v === null || typeof v !== "object")) return JSON.stringify(value);
    return `[\n${value.map((v) => next + (Array.isArray(v) ? JSON.stringify(v) : pretty(v, next))).join(",\n")}\n${indent}]`;
  }
  if (value && typeof value === "object") {
    const keys = Object.keys(value);
    if (keys.length === 0) return "{}";
    return `{\n${keys.map((k) => `${next}${JSON.stringify(k)}: ${pretty(value[k], next)}`).join(",\n")}\n${indent}}`;
  }
  return JSON.stringify(value);
}

// ── the run ─────────────────────────────────────────────────────────────────

async function main() {
  const { work, out, "forge-version": forgeVersion } = args();
  const scrub = scrubber(work);
  const project = join(work, PROJECT);
  const capturedAt = new Date().toISOString();
  const steps = [];

  const run = async (command, cwd) => {
    const where = cwd === work ? "~" : `~/${PROJECT}`;
    console.log(`\n$ ${command}   (in ${where})`);
    const { exitCode, real, events } = await record(command, cwd, scrub);
    steps.push({ kind: "command", cwd: where, command, exit_code: exitCode, real_seconds: round(real), duration: events.duration, events: events.events });
    if (exitCode !== 0) throw new Error(`capture: \`${command}\` exited ${exitCode}; nothing written`);
  };

  // 1. forge makes the project. Its own first commit is the baseline; make one if it could not.
  await run(NEW_COMMAND, work);
  if (!safe(() => git(project, "rev-parse", "--verify", "HEAD"))) commitAll(project, "forge project new");
  const fromNew = new Set(git(project, "ls-tree", "-r", "--name-only", "HEAD").split("\n").filter(Boolean));

  // 2. You write the two protos.
  const yourAdded = new Map(); // path → added lines
  for (const edit of HAND_WRITTEN) {
    const file = join(project, edit.path);
    const before = readFileSync(file, "utf8");
    if (!before.includes(TODO_LINE)) throw new Error(`capture: ${edit.path} no longer has forge's TODO line; update HAND_WRITTEN`);
    writeFileSync(file, before.replace(TODO_LINE, edit.text));
    steps.push({ kind: "edit", cwd: `~/${PROJECT}`, path: edit.path, replaces: TODO_LINE.trimEnd(), text: edit.text, lines: countLines(Buffer.from(edit.text)) });
  }
  commitAll(project, "the protos you write");
  for (const line of git(project, "diff", "--unified=0", "--no-color", "HEAD~1", "HEAD").split("\n")) {
    const m = /^\+\+\+ b\/(.*)$/.exec(line);
    if (m) yourAdded.set(m[1], []);
    else if (line.startsWith("+") && !line.startsWith("+++")) [...yourAdded.values()].at(-1).push(line.slice(1));
  }

  // 3. forge does the rest; lint and the build prove it holds together.
  const changes = {};
  for (const command of AFTER) {
    await run(command, project);
    changes[command] = stagedChanges(project);
    commitAll(project, command);
  }

  // ── count every file in the finished app ──
  const files = git(project, "ls-files", "-z").split("\0").filter(Boolean).sort();
  const categories = new Map(CATEGORIES.map(([id]) => [id, { id, files: 0, lines: 0, you_lines: 0, regen_lines: 0, entries: [] }]));
  const excluded = new Map();
  const youWrote = [];
  let machineLines = 0;
  for (const path of files) {
    const buf = readFileSync(join(project, path));
    const reason = exclusionOf(path, buf);
    const lines = countLines(buf);
    if (reason) {
      const row = excluded.get(reason) ?? { reason, files: 0, lines: 0 };
      row.files++;
      row.lines += lines;
      excluded.set(reason, row);
      continue;
    }
    const text = buf.toString("utf8");
    // Your lines still in the file, word for word (forge adds to your protos; it does not rewrite your lines).
    const finalLines = new Set(text.split("\n"));
    const yours = (yourAdded.get(path) ?? []).filter((line) => finalLines.has(line)).length;
    if (yours) youWrote.push({ path, lines: yours });
    const own = ownershipOf(project, path, text);
    const cat = categories.get(categoryOf(path));
    cat.files++;
    cat.lines += lines;
    cat.you_lines += yours;
    if (own === "regen") cat.regen_lines += lines - yours;
    cat.entries.push([path, lines, yours, own]);
    if (isMachine(path)) machineLines += lines;
  }
  const cats = [...categories.values()].filter((c) => c.files > 0);
  const totalLines = cats.reduce((n, c) => n + c.lines, 0);
  const youLines = youWrote.reduce((n, f) => n + f.lines, 0);
  const included = new Set(cats.flatMap((c) => c.entries.map((e) => e[0])));
  const stepCounts = (paths) => paths.filter((p) => included.has(p)).length;

  const manifest = {
    schema: MANIFEST_SCHEMA,
    producer: PRODUCER,
    forge_version: forgeVersion,
    captured_at: capturedAt,
    project: { name: PROJECT, command: NEW_COMMAND, services: ["orders", "inventory"], frontend: "web", entities: ["Order", "Stock"], custom_rpcs: ["OrdersService/CancelOrder", "InventoryService/Reserve"] },
    totals: {
      files: included.size,
      lines: totalLines,
      you_wrote_lines: youLines,
      you_wrote_files: youWrote.length,
      forge_lines: totalLines - youLines,
      machine_lines: machineLines,
      regen_lines: cats.reduce((n, c) => n + c.regen_lines, 0),
    },
    by_step: {
      project_new: { files: [...fromNew].filter((p) => included.has(p)).length },
      ...Object.fromEntries(
        Object.entries(changes).map(([command, c]) => [command, { added: stepCounts(c.added), changed: stepCounts(c.changed), removed: c.removed.length }])
      ),
    },
    you_wrote: youWrote,
    categories: cats,
    excluded: [...excluded.values()],
    chain: CHAIN.map((spec) => excerpt(project, files, spec, yourAdded)),
  };

  const cast = {
    schema: CAST_SCHEMA,
    producer: PRODUCER,
    forge_version: forgeVersion,
    captured_at: capturedAt,
    asciicast: { version: 2, width: 100, height: 30, idle_time_limit: IDLE_LIMIT, title: `forge ${forgeVersion}: shop, from nothing to a build` },
    steps,
  };

  writeFileSync(join(out, "forge-scaffold-cast.json"), `${pretty(cast)}\n`);
  writeFileSync(join(out, "forge-scaffold-manifest.json"), `${pretty(manifest)}\n`);
  const t = manifest.totals;
  console.log(`\ncapture: you wrote ${t.you_wrote_lines} lines in ${t.you_wrote_files} files; forge wrote ${t.forge_lines} across ${t.files} files.`);
  console.log(`capture: wrote ${out}/forge-scaffold-{cast,manifest}.json`);
}

main().catch((err) => {
  console.error(err instanceof Error ? err.message : err);
  process.exit(1);
});
