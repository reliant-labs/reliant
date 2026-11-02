#!/usr/bin/env node
// Projects control-plane's PUBLIC proto contract OUT of the control-plane repo
// and into `proto-vendor/controlplane/`, the committed generated tree every
// control-plane client in this repo (Go and TypeScript) is generated from.
//
// ## Why this file exists
//
// This repo used to carry a HAND-MAINTAINED copy of the public controlplane.v1
// protos (proto/controlplane/v1/public/, plus a second, smaller copy in
// proto/controlplane/v1/admin.proto for the Go client). Its README claimed
// control-plane read those files through a symlink; it never did. The copies
// drifted in exactly the ways a copy does: fields the server had RESERVED
// (Daemon.last_heartbeat, GetCurrentUserResponse.organizations) were still
// declared here, RPCs the server had deleted (DaemonService.RolloutDaemon)
// still had typed stubs, message names differed from the server's, and
// DeployService was missing entirely.
//
// Now there is ONE source: control-plane's own `proto/`, filtered by its
// `proto/public-api.txt` allowlist. This script runs `buf export` over that
// allowlist and writes the result here byte-for-byte. Nothing under
// proto-vendor/controlplane/ is authored in this repo.
//
// ## Why it is projected into a committed tree rather than read live
//
// This repo is PUBLIC; control-plane is PRIVATE. A reliant build therefore
// cannot read control-plane: generated here, committed, and checked by a CI
// job that reads control-plane with a read-only deploy key.
//
// ## The declared source: SOURCE
//
// Every write also records WHICH control-plane commit the tree was exported
// from, in proto-vendor/controlplane/SOURCE. That is this repo's pin of the
// contract, the way go.mod pins forge. The gate judges the tree against its
// own declaration, never against control-plane's moving main:
//
//   --check   the tree is exactly what control-plane@SOURCE exports (nobody
//             hand-edited it, and it was not exported from a dirty checkout),
//             and SOURCE is on control-plane main (not a PR branch that may
//             never merge, or be squashed away).
//
// It used to compare against control-plane MAIN, so every control-plane proto
// merge turned every reliant pull request that reached the lane red until
// someone re-vendored: a verdict that changed with no change here. Being
// BEHIND control-plane main is a lag, not a defect of the change under review,
// and wire compatibility with the server is control-plane's gate to enforce
// (control-plane scripts/check-vendored-proto.sh fails a control-plane change
// that reliant's vendored client cannot survive). Lag is reported by
//
//   --upstream  compare against CONTROL_PLANE_DIR's checked-out tree (control-
//               plane main, in .github/workflows/cp-contract-drift.yml), print
//               what differs, exit 3 when behind. Never a gate: the drift
//               workflow turns it into one open issue, and the pin bot
//               re-vendors (`upgrade.vendored_contracts` in forge.yaml).
//
// ## Usage
//
//   node .github/scripts/sync-controlplane-proto.mjs                   # write (+ SOURCE)
//   node .github/scripts/sync-controlplane-proto.mjs --check           # verify, fork-safe
//   node .github/scripts/sync-controlplane-proto.mjs --check --require # verify, authoritative
//   node .github/scripts/sync-controlplane-proto.mjs --upstream [--report <file.md>]
//
// After writing, regenerate the clients: `task generate:controlplane`
// (`task contract:controlplane:sync` runs both steps).
//
// The control-plane commit a write records is, in order: $CONTROL_PLANE_COMMIT,
// <CONTROL_PLANE_DIR>/.commit (a `git archive` extraction, as the pin bot
// makes), or the checkout's HEAD, whose exported paths must then be clean.
//
// `--check` without `--require` SKIPS (exit 0) when there is no control-plane
// checkout, no `buf`, or the checkout lacks the SOURCE commit, so a fork does
// not fail on a private repo it cannot see. `--require` turns every skip into
// a failure; CI passes it, because a gate that can silently skip is
// indistinguishable from one that passes.

import { execFileSync } from "node:child_process";
import {
  existsSync,
  mkdtempSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");
const VENDOR_DIR = join(REPO_ROOT, "proto-vendor", "controlplane");
const SOURCE_FILE = join(VENDOR_DIR, "SOURCE");

const CONTROL_PLANE_DIR =
  process.env.CONTROL_PLANE_DIR || resolve(REPO_ROOT, "..", "control-plane");
// The ref SOURCE must be an ancestor of, in the control-plane checkout.
const CONTROL_PLANE_MAIN_REF = process.env.CONTROL_PLANE_MAIN_REF || "origin/main";

// What `buf export proto` reads: the module tree and the configs that resolve it.
const EXPORT_INPUTS = ["proto", "buf.yaml", "buf.lock", "buf.work.yaml"];

const args = process.argv.slice(2);
const checkOnly = args.includes("--check");
const upstream = args.includes("--upstream");
const requireExport = args.includes("--require");
const reportIdx = args.indexOf("--report");
const reportFile = reportIdx >= 0 ? args[reportIdx + 1] : "";
if (reportIdx >= 0 && !reportFile) fail("--report needs a file");
if (checkOnly && upstream) fail("--check and --upstream are different questions; pass one");

// Written alongside the exported protos so the directory is a self-contained
// buf module. Kept here, not copied, because control-plane's buf.yaml carries
// that repo's lint policy, which is not this repo's to enforce.
const BUF_YAML = `# GENERATED by .github/scripts/sync-controlplane-proto.mjs — do not edit.
version: v2
`;

const README = `# proto-vendor/controlplane — GENERATED, do not edit

The public control-plane API, exported verbatim from the control-plane repo's
\`proto/\` by \`.github/scripts/sync-controlplane-proto.mjs\`. Which services are
public is decided by control-plane's \`proto/public-api.txt\`; no operator-only
(\`*Admin\`) or internal-service API is included.

Nothing here is authored in this repo. To change the contract, change the
proto in control-plane, merge it, then run \`task contract:controlplane:sync\`
(the sync script, then the client regeneration) against control-plane main.

\`SOURCE\` names the control-plane commit this tree was exported from: this
repo's pin of the contract. CI (\`task contract:controlplane:check\`) fails if
the tree is not exactly what that commit exports, or if that commit is not on
control-plane main. Being behind control-plane main is not a failure here:
\`.github/workflows/cp-contract-drift.yml\` keeps one issue open while it lasts,
and control-plane's own gate blocks a change this copy could not survive.

Generated clients:

- TypeScript: \`frontends/web/src/gen/controlplane/\` (e.g. \`services/deploy/v1/deploy_pb.ts\`)
- Go:         \`gen/controlplane/\` (e.g. \`services/billing/v1\`)

## Status: EXPERIMENTAL

controlplane.v1 is unstable: field numbers, RPC names and service shapes may
change without notice until the package is declared stable.
`;

function sourceFile(commit) {
  return `# GENERATED by .github/scripts/sync-controlplane-proto.mjs — do not edit.
# The control-plane commit this tree was exported from (on control-plane main).
control-plane ${commit}
`;
}

function fail(message) {
  console.error(`sync-controlplane-proto: ${message}`);
  process.exit(1);
}

function git(dir, ...gitArgs) {
  return execFileSync("git", ["-C", dir, ...gitArgs], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  }).trim();
}

function tryGit(dir, ...gitArgs) {
  try {
    return { out: git(dir, ...gitArgs), ok: true };
  } catch (err) {
    return { out: String(err.stderr || err.message).trim(), ok: false };
  }
}

const isGitCheckout = (dir) => tryGit(dir, "rev-parse", "--git-dir").ok;

/** The commit proto-vendor/controlplane/SOURCE declares, or "" when there is none. */
function declaredSource() {
  if (!existsSync(SOURCE_FILE)) return "";
  for (const raw of readFileSync(SOURCE_FILE, "utf8").split("\n")) {
    const line = raw.replace(/#.*/, "").trim();
    if (!line) continue;
    const m = /^control-plane ([0-9a-f]{40})$/.exec(line);
    if (!m) fail(`${relative(REPO_ROOT, SOURCE_FILE)}: want "control-plane <40-hex commit>", got ${JSON.stringify(raw)}`);
    return m[1];
  }
  return "";
}

/**
 * The control-plane commit CONTROL_PLANE_DIR's exported inputs are, for a
 * write: refuse a dirty checkout, whose export no commit reproduces.
 */
function sourceCommitForWrite() {
  const fromEnv = (process.env.CONTROL_PLANE_COMMIT || "").trim();
  const dotCommit = join(CONTROL_PLANE_DIR, ".commit");
  let commit = fromEnv || (existsSync(dotCommit) ? readFileSync(dotCommit, "utf8").trim() : "");
  if (!commit && isGitCheckout(CONTROL_PLANE_DIR)) {
    const present = EXPORT_INPUTS.filter((p) => existsSync(join(CONTROL_PLANE_DIR, p)));
    const dirty = tryGit(CONTROL_PLANE_DIR, "status", "--porcelain", "--", ...present);
    if (!dirty.ok) fail(`git status failed in ${CONTROL_PLANE_DIR}: ${dirty.out}`);
    if (dirty.out) {
      fail(
        `${CONTROL_PLANE_DIR} has uncommitted changes to what it exports:\n  ` +
          dirty.out.split("\n").join("\n  ") +
          `\nSOURCE must name a commit that reproduces the vendored tree. Commit (and merge)\n` +
          `the control-plane change first, then re-vendor from that commit.`,
      );
    }
    commit = git(CONTROL_PLANE_DIR, "rev-parse", "HEAD");
  }
  if (!/^[0-9a-f]{40}$/.test(commit)) {
    fail(
      `cannot tell which control-plane commit ${CONTROL_PLANE_DIR} is (got ${JSON.stringify(commit)}).\n` +
        `Point CONTROL_PLANE_DIR at a git checkout, or set CONTROL_PLANE_COMMIT / write <dir>/.commit\n` +
        `with the full commit an extracted tree came from.`,
    );
  }
  return commit;
}

/** Parse public-api.txt into the list of exported directories (relative to proto/). */
function readExports(cpDir) {
  const allowlist = join(cpDir, "proto", "public-api.txt");
  const dirs = [];
  for (const [i, raw] of readFileSync(allowlist, "utf8").split("\n").entries()) {
    const line = raw.replace(/#.*/, "").trim();
    if (!line) continue;
    const [verdict, dir, extra] = line.split(/\s+/);
    if (extra !== undefined || !dir || (verdict !== "export" && verdict !== "withhold")) {
      fail(`${allowlist}:${i + 1}: want "export <dir>" or "withhold <dir>", got ${JSON.stringify(raw)}`);
    }
    if (verdict === "export") dirs.push(dir);
  }
  if (dirs.length === 0) fail(`${allowlist} exports nothing`);
  return dirs;
}

/**
 * Export the public contract of the control-plane tree at cpDir into a fresh
 * temp directory and return it, or { unavailable } when this machine cannot
 * produce it.
 */
function exportContract(cpDir, commit) {
  if (!existsSync(join(cpDir, "proto", "public-api.txt"))) {
    return { unavailable: `no control-plane checkout at ${cpDir}` };
  }
  const out = mkdtempSync(join(tmpdir(), "cp-public-proto-"));
  const pathArgs = readExports(cpDir).flatMap((dir) => ["--path", join("proto", dir)]);
  try {
    execFileSync("buf", ["export", "proto", ...pathArgs, "-o", out], {
      cwd: cpDir,
      stdio: ["ignore", "pipe", "pipe"],
    });
  } catch (err) {
    rmSync(out, { recursive: true, force: true });
    if (err.code === "ENOENT") return { unavailable: "buf is not installed" };
    fail(`buf export failed in ${cpDir}:\n${err.stderr || err.message}`);
  }
  writeFileSync(join(out, "buf.yaml"), BUF_YAML);
  writeFileSync(join(out, "README.md"), README);
  if (commit) writeFileSync(join(out, "SOURCE"), sourceFile(commit));
  return { dir: out };
}

/**
 * The control-plane tree at `commit`, to export from: CONTROL_PLANE_DIR itself
 * when it is that commit with clean exported inputs (CI checks it out there),
 * else a `git archive` of the commit out of its object store. Returns
 * { dir, cleanup } or { unavailable }.
 */
function treeAt(commit) {
  if (!existsSync(CONTROL_PLANE_DIR)) return { unavailable: `no control-plane checkout at ${CONTROL_PLANE_DIR}` };
  if (!isGitCheckout(CONTROL_PLANE_DIR)) {
    const dotCommit = join(CONTROL_PLANE_DIR, ".commit");
    if (existsSync(dotCommit) && readFileSync(dotCommit, "utf8").trim() === commit) {
      return { dir: CONTROL_PLANE_DIR, cleanup() {} };
    }
    return { unavailable: `${CONTROL_PLANE_DIR} is not a git checkout, and its .commit is not ${commit}` };
  }
  if (!tryGit(CONTROL_PLANE_DIR, "cat-file", "-e", `${commit}^{commit}`).ok) {
    return {
      unavailable:
        `the control-plane checkout at ${CONTROL_PLANE_DIR} does not have commit ${commit} ` +
        `(git -C ${CONTROL_PLANE_DIR} fetch origin)`,
    };
  }
  const present = EXPORT_INPUTS.filter((p) => tryGit(CONTROL_PLANE_DIR, "cat-file", "-e", `${commit}:${p}`).ok);
  const head = tryGit(CONTROL_PLANE_DIR, "rev-parse", "HEAD").out;
  const dirty = tryGit(CONTROL_PLANE_DIR, "status", "--porcelain", "--", ...present).out;
  if (head === commit && !dirty) return { dir: CONTROL_PLANE_DIR, cleanup() {} };
  const dir = mkdtempSync(join(tmpdir(), "cp-at-source-"));
  try {
    const tar = execFileSync("git", ["-C", CONTROL_PLANE_DIR, "archive", "--format=tar", commit, ...present], {
      stdio: ["ignore", "pipe", "pipe"],
      maxBuffer: 1 << 30,
    });
    execFileSync("tar", ["-x", "-C", dir], { input: tar, stdio: ["pipe", "ignore", "pipe"] });
  } catch (err) {
    rmSync(dir, { recursive: true, force: true });
    fail(`git archive ${commit} failed in ${CONTROL_PLANE_DIR}:\n${err.stderr || err.message}`);
  }
  return { dir, cleanup: () => rmSync(dir, { recursive: true, force: true }) };
}

/** Every file under root as a sorted list of forward-slash relative paths. */
function listTree(root) {
  if (!existsSync(root)) return [];
  const files = [];
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, entry.name);
      if (entry.isDirectory()) walk(full);
      else files.push(relative(root, full).split("\\").join("/"));
    }
  };
  walk(root);
  return files.sort();
}

/** Human-readable differences between two trees; empty when identical. */
function diffTrees(want, have, { ignore = [] } = {}) {
  const wantFiles = listTree(want).filter((f) => !ignore.includes(f));
  const haveFiles = listTree(have).filter((f) => !ignore.includes(f));
  const problems = [];
  for (const f of wantFiles) {
    if (!haveFiles.includes(f)) problems.push(`missing:  ${f}`);
    else if (!readFileSync(join(want, f)).equals(readFileSync(join(have, f)))) {
      problems.push(`differs:  ${f}`);
    }
  }
  for (const f of haveFiles) {
    if (!wantFiles.includes(f)) problems.push(`stale:    ${f}`);
  }
  return problems;
}

/** A skip: exit 0 unless --require, which makes it a failure. */
function unavailableExit(unavailable, what) {
  if (requireExport) {
    fail(
      `--require was passed but ${what}: ${unavailable}.\n` +
        `This invocation is an authoritative gate; skipping would mean it verified\n` +
        `nothing while reporting success. Install buf and point CONTROL_PLANE_DIR at a\n` +
        `control-plane checkout (currently ${CONTROL_PLANE_DIR}).`,
    );
  }
  console.log(`sync-controlplane-proto: skipping — ${unavailable}`);
  process.exit(0);
}

// ── --check: the tree is control-plane@SOURCE's export, and SOURCE is on main ──
if (checkOnly) {
  const source = declaredSource();
  if (!source) {
    fail(
      `${relative(REPO_ROOT, SOURCE_FILE)} is missing: the vendored tree does not say which\n` +
        `control-plane commit it was exported from. Re-vendor from control-plane main:\n` +
        `  task contract:controlplane:sync`,
    );
  }
  const at = treeAt(source);
  if (at.unavailable) unavailableExit(at.unavailable, `control-plane@${source.slice(0, 12)} could not be read`);
  let exported;
  try {
    const res = exportContract(at.dir, source);
    if (res.unavailable) unavailableExit(res.unavailable, "the contract could not be exported");
    exported = res.dir;
  } finally {
    at.cleanup();
  }
  try {
    const problems = diffTrees(exported, VENDOR_DIR);
    if (problems.length) {
      fail(
        `proto-vendor/controlplane/ is NOT what control-plane@${source.slice(0, 12)} (its SOURCE) exports:\n  ` +
          problems.join("\n  ") +
          `\nThis tree is generated — do not hand-edit it, and do not export it from an\n` +
          `uncommitted control-plane tree. Regenerate it from control-plane main with:\n` +
          `  task contract:controlplane:sync`,
      );
    }
  } finally {
    rmSync(exported, { recursive: true, force: true });
  }

  // SOURCE must be on control-plane main: a commit on a PR branch serves a
  // contract the deployed server may never have, and disappears when the PR
  // is squash-merged.
  if (isGitCheckout(CONTROL_PLANE_DIR)) {
    if (!tryGit(CONTROL_PLANE_DIR, "rev-parse", "--verify", "--quiet", `${CONTROL_PLANE_MAIN_REF}^{commit}`).ok) {
      unavailableExit(`the checkout has no ${CONTROL_PLANE_MAIN_REF}`, "SOURCE's ancestry could not be checked");
    }
    const onMain = tryGit(CONTROL_PLANE_DIR, "merge-base", "--is-ancestor", source, CONTROL_PLANE_MAIN_REF);
    if (!onMain.ok) {
      fail(
        `SOURCE names control-plane ${source.slice(0, 12)}, which is not on ${CONTROL_PLANE_MAIN_REF}.\n` +
          `Vendor only a contract control-plane has merged: merge the control-plane change,\n` +
          `then re-vendor from control-plane main (task contract:controlplane:sync).`,
      );
    }
  } else {
    unavailableExit(`${CONTROL_PLANE_DIR} is not a git checkout`, "SOURCE's ancestry could not be checked");
  }
  console.log(
    `sync-controlplane-proto: proto-vendor/controlplane/ is control-plane@${source.slice(0, 12)}'s export, ` +
      `and that commit is on ${CONTROL_PLANE_MAIN_REF}`,
  );
  process.exit(0);
}

// ── --upstream: how far behind CONTROL_PLANE_DIR (control-plane main) is it? ──
if (upstream) {
  const head = isGitCheckout(CONTROL_PLANE_DIR) ? tryGit(CONTROL_PLANE_DIR, "rev-parse", "HEAD").out : "";
  const { dir: exported, unavailable } = exportContract(CONTROL_PLANE_DIR, "");
  if (unavailable) unavailableExit(unavailable, "the contract could not be exported");
  let problems;
  try {
    // SOURCE is ours, not control-plane's: compare the contract itself.
    problems = diffTrees(exported, VENDOR_DIR, { ignore: ["SOURCE"] });
  } finally {
    rmSync(exported, { recursive: true, force: true });
  }
  const source = declaredSource();
  const upstreamName = `control-plane${head ? `@${head.slice(0, 12)}` : ` at ${CONTROL_PLANE_DIR}`}`;
  if (!problems.length) {
    console.log(`sync-controlplane-proto: proto-vendor/controlplane/ matches ${upstreamName}`);
    process.exit(0);
  }
  const summary =
    `proto-vendor/controlplane/ (SOURCE ${source ? source.slice(0, 12) : "missing"}) is behind ${upstreamName}:`;
  console.log(`sync-controlplane-proto: ${summary}\n  ${problems.join("\n  ")}`);
  if (reportFile) {
    writeFileSync(
      reportFile,
      `${summary}\n\n` +
        "```\n" +
        problems.join("\n") +
        "\n```\n\n" +
        "Not a gate: pull requests are judged against `SOURCE`, not control-plane main.\n" +
        "control-plane's own gate (`scripts/check-vendored-proto.sh`) blocks a control-plane\n" +
        "change this copy could not survive, so this is a lag, not a break. Re-vendor:\n\n" +
        "```bash\n" +
        "CONTROL_PLANE_DIR=<a control-plane checkout at main> task contract:controlplane:sync\n" +
        "```\n\n" +
        "(the pin bot does the same when it next runs: `upgrade.vendored_contracts` in `forge.yaml`).\n",
    );
  }
  process.exit(3);
}

// ── write ──
const commit = sourceCommitForWrite();
const { dir: exported, unavailable } = exportContract(CONTROL_PLANE_DIR, commit);
if (unavailable) fail(`cannot write: ${unavailable}`);
try {
  // Replace the tree wholesale so a file dropped upstream disappears here
  // too; merging would leave exactly the kind of stale copy this replaces.
  rmSync(VENDOR_DIR, { recursive: true, force: true });
  for (const f of listTree(exported)) {
    mkdirSync(dirname(join(VENDOR_DIR, f)), { recursive: true });
    writeFileSync(join(VENDOR_DIR, f), readFileSync(join(exported, f)));
  }
  console.log(
    `sync-controlplane-proto: wrote ${listTree(VENDOR_DIR).length} files to proto-vendor/controlplane/ ` +
      `from control-plane@${commit.slice(0, 12)}`,
  );
} finally {
  rmSync(exported, { recursive: true, force: true });
}
