#!/usr/bin/env bash
#
# Fails when an npm dependency carries a license that is not on the ALLOWLIST.
#
# The npm twin of check-licenses.sh, and it exists because that script is
# Go-only: it walks `go list -deps`, so a frontend's entire dependency tree —
# routinely larger than the Go one — went ungated. Measured when this was
# written: 104 Go modules in control-plane against 729 npm packages in one of
# its two frontends.
#
# Same bar as the Go gate, deliberately: allowlist not blocklist, an
# unrecognized license FAILS rather than passing silently, and an exception is
# per-package with a stated reason. See check-licenses.sh for the full
# rationale; the differences below are the ones npm forces.
#
# ── SCOPE: the PRODUCTION tree, resolved from the lockfile ──────────────
#
# `npm ls --omit=dev --all` is the closest npm gets to `go list -deps`: it
# resolves what actually ships, not everything in node_modules. Build tooling
# is excluded on purpose. lightningcss (Tailwind's compiler) is MPL-2.0 and
# runs at build time — it emits CSS, it does not link into the bundle, and
# gating on it produces a false positive that teaches people to add
# exceptions until the gate means nothing.
#
# ── WHY NOT WALK node_modules ───────────────────────────────────────────
#
# Reading every package.json under node_modules is the obvious approach and
# it is wrong twice over. It reports packages that are merely present (dev
# tooling, an unlinked pnpm store, a stale install), and it descends into
# `example/` and `playground/` FIXTURE directories that ship inside real
# packages. Measured: that approach reported beep-boop@1.2.3 and
# monaco-loader@0.0.1 as unlicensed dependencies. Neither is a dependency;
# both are demo fixtures inside github-from-package and @monaco-editor/loader.
# The lockfile-resolved tree has no such ambiguity.
#
# ── OPTIONAL DEPENDENCIES ARE STILL CHECKED ─────────────────────────────
#
# npm marks a package `optional` when installation may fail without breaking
# the install — NOT when it is unused. An optional package that does install
# is linked and shipped like any other. sharp's libvips binaries are optional
# AND LGPL, and they land in node_modules on every platform we build on, so
# treating optional as "not our problem" would miss the single most
# consequential finding this gate has produced. Exclude such a package
# deliberately, via package.json, not by pretending the gate cannot see it.
#
# Usage:
#   scripts/check-npm-licenses.sh              # every frontend in this repo
#   scripts/check-npm-licenses.sh --json       # machine-readable, for CI
#   scripts/check-npm-licenses.sh --list       # print every dep + license
#   scripts/check-npm-licenses.sh <dir> ...    # only these package dirs
set -uo pipefail

repo_root="$(git rev-parse --show-toplevel 2>/dev/null)" || repo_root="$(pwd)"
allowlist_file="${repo_root}/.license-allowlist-npm"

mode="check"
dirs=()
while [ $# -gt 0 ]; do
  case "$1" in
  --json) mode="json" ;;
  --list) mode="list" ;;
  -h | --help)
    echo "usage: $0 [--json|--list] [package-dir ...]" >&2
    exit 2
    ;;
  -*)
    echo "check-npm-licenses: unknown flag $1" >&2
    exit 2
    ;;
  *) dirs+=("$1") ;;
  esac
  shift
done

if ! command -v node >/dev/null 2>&1; then
  echo "check-npm-licenses: node is required" >&2
  exit 2
fi

# Discover frontends when none were named: any directory with a package.json
# that is not itself inside a node_modules tree. `-prune` keeps this from
# descending into tens of thousands of vendored package.json files.
if [ ${#dirs[@]} -eq 0 ]; then
  while IFS= read -r pj; do
    dirs+=("$(dirname "${pj}")")
  done < <(
    find "${repo_root}" \
      -name node_modules -prune -o \
      -name .git -prune -o \
      -name package.json -print 2>/dev/null | sort
  )
fi

if [ ${#dirs[@]} -eq 0 ]; then
  echo "✅ no npm packages in this repo — nothing to check."
  exit 0
fi

# The classifier and the report both live in node: npm's own JSON is the
# authoritative view of the resolved tree, and parsing it in bash would mean
# hand-rolling a JSON reader in a script whose entire point is being trusted.
run_one() {
  local dir="$1"
  NPM_LICENSE_DIR="${dir}" \
    NPM_LICENSE_MODE="${mode}" \
    NPM_LICENSE_ALLOWLIST="${allowlist_file}" \
    node -e '
const { execFileSync } = require("child_process");
const fs = require("fs");
const path = require("path");

const dir = process.env.NPM_LICENSE_DIR;
const mode = process.env.NPM_LICENSE_MODE;
const allowlistFile = process.env.NPM_LICENSE_ALLOWLIST;

// Same bar as the Go gate: no source-disclosure obligation, no
// "provide as a service" restriction. Kept as a literal list rather than
// derived from the sibling script so each gate is readable on its own.
const ALLOWED = new Set([
  "MIT", "MIT-0", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "BSD",
  "ISC", "0BSD", "Unlicense", "Zlib", "PostgreSQL", "Python-2.0",
  "CC0-1.0", "WTFPL", "BlueOak-1.0.0", "CC-BY-4.0", "Artistic-2.0",
]);

// Copyleft and source-available. LGPL is included on purpose: npm ships
// native addons (sharp/libvips) whose LGPL obligations are murkier than the
// classic dynamic-linking carve-out, and the decision here is to avoid the
// family rather than reason case by case.
const DENY = [
  "AGPL", "GPL", "LGPL", "SSPL", "BSL", "BUSL", "ELASTIC",
  "CC-BY-SA", "CC-BY-NC", "OSL", "EUPL", "CPAL", "RPL", "MPL",
];

const allowlist = new Map();
if (fs.existsSync(allowlistFile)) {
  for (const line of fs.readFileSync(allowlistFile, "utf8").split("\n")) {
    const stripped = line.replace(/#.*$/, "").trim();
    if (stripped) allowlist.set(stripped, true);
  }
}

if (!fs.existsSync(path.join(dir, "package.json"))) process.exit(0);

// --omit=dev is the whole point: build tooling is not shipped. --all walks
// the tree transitively. npm ls exits non-zero on peer-dep warnings while
// still emitting valid JSON, so the exit code is deliberately ignored and
// only unparseable output is treated as failure.
let raw;
try {
  raw = execFileSync("npm", ["ls", "--omit=dev", "--all", "--json", "--long"], {
    cwd: dir, encoding: "utf8", maxBuffer: 256 * 1024 * 1024, stdio: ["ignore", "pipe", "ignore"],
  });
} catch (e) {
  raw = e.stdout;
}

// The resolved tree is necessary but NOT sufficient, and this is the part
// that took a real finding to discover. `npm ls --omit=dev` omits an
// optionalDependency it did not itself resolve — but another installer may
// have put it on disk anyway. Measured: next declares sharp as an
// optionalDependency; npm ls reported a tree with no sharp in it, while
// @img/sharp-libvips (LGPL-3.0-or-later) sat installed in node_modules.
// Gating on the tree alone therefore passes a repo that ships LGPL binaries.
//
// So the installed tree is cross-checked below: anything physically present
// under node_modules that the resolved tree did not account for is examined
// too. Fixture directories are excluded by requiring the package.json to sit
// at a real package root (…/node_modules/<name>/package.json), which is what
// keeps example/ and playground/ demos from being reported as dependencies.
if (!raw || !raw.trim()) {
  console.error(`check-npm-licenses: ${dir}: npm ls produced no output (are dependencies installed?)`);
  process.exit(2);
}

let tree;
try { tree = JSON.parse(raw); } catch (e) {
  console.error(`check-npm-licenses: ${dir}: could not parse npm ls output: ${e.message}`);
  process.exit(2);
}

// An SPDX expression is not a license: "(MPL-2.0 OR Apache-2.0)" is a CHOICE,
// and we take the permissive side. Splitting on OR and accepting if ANY
// branch is allowed is what keeps dompurify (MPL or Apache) from failing a
// gate it has no business failing. AND is the opposite — every term binds —
// so an AND expression is allowed only if every term is.
function verdict(expr) {
  if (!expr) return { ok: false, why: "UNKNOWN" };
  const raw = String(expr).trim();
  const norm = raw.replace(/[()]/g, " ").trim();

  if (/\bOR\b/i.test(norm)) {
    const parts = norm.split(/\bOR\b/i).map((s) => s.trim()).filter(Boolean);
    if (parts.some((p) => verdict(p).ok)) return { ok: true, why: raw };
    return { ok: false, why: raw };
  }
  if (/\bAND\b/i.test(norm)) {
    const parts = norm.split(/\bAND\b/i).map((s) => s.trim()).filter(Boolean);
    if (parts.every((p) => verdict(p).ok)) return { ok: true, why: raw };
    return { ok: false, why: raw };
  }

  const id = norm.replace(/\+$/, "");
  if (ALLOWED.has(id)) return { ok: true, why: raw };
  const upper = id.toUpperCase();
  if (DENY.some((d) => upper.includes(d))) return { ok: false, why: raw };
  return { ok: false, why: raw === "" ? "UNKNOWN" : raw };
}

// Collect unique name@version across the resolved tree. The root itself is
// first-party and skipped; so is any @reliantlabs/@reliant-labs package.
const seen = new Map();
function walk(node) {
  const deps = node.dependencies || {};
  for (const [name, info] of Object.entries(deps)) {
    if (!info || typeof info !== "object") continue;

    // An entry with no version was never installed: npm lists unresolved
    // optional and peer dependencies in the tree, carrying no version, no
    // path and no license. Nothing that is absent from node_modules can ship,
    // so reporting it as an unlicensed dependency is a false positive —
    // measured on reliant/web, this is exactly bufferutil, utf-8-validate and
    // immer, none of which are installed. A package that IS installed always
    // carries a version, so this cannot hide a real one.
    if (!info.version) continue;

    const version = info.version;
    const key = `${name}@${version}`;
    if (!seen.has(key)) {
      seen.set(key, { name, version, license: info.license, optional: !!info.optional });
      walk(info);
    }
  }
}
walk(tree);

// Cross-check what is physically installed (see the note above npm ls), but
// ONLY for packages that a production dependency declares as an
// optionalDependency. That is the exact hole: an optional dep is part of the
// shipped tree when it installs, yet `npm ls --omit=dev` omits the ones it
// did not resolve itself.
//
// Scoping the cross-check this way is deliberate. Walking all of
// node_modules instead re-admits every dev tool on disk — measured, that
// reported axe-core and lightningcss (build-time, MPL-2.0) as production
// findings, which is the false-positive class this gate exists to avoid.
//
// A package.json counts only when its directory is the package root a
// resolver would load — .../node_modules/<name>/package.json, allowing one
// @scope/ segment. That is what excludes the example/ and playground/
// fixtures shipping inside real packages, which a naive walk reports as
// unlicensed dependencies.
const optionalWanted = new Set();
(function collectOptional() {
  const lock = path.join(dir, "package-lock.json");
  if (!fs.existsSync(lock)) return;
  let d;
  try { d = JSON.parse(fs.readFileSync(lock, "utf8")); } catch { return; }
  for (const [, meta] of Object.entries(d.packages || {})) {
    if (meta && meta.dev) continue; // dev-tree optionals do not ship
    for (const n of Object.keys((meta && meta.optionalDependencies) || {})) optionalWanted.add(n);
  }
})();
function scanInstalled(nmDir, depth) {
  if (depth > 6 || !fs.existsSync(nmDir)) return;
  let entries;
  try { entries = fs.readdirSync(nmDir, { withFileTypes: true }); } catch { return; }
  for (const ent of entries) {
    if (!ent.isDirectory() && !ent.isSymbolicLink()) continue;
    const name = ent.name;
    if (name === ".bin") continue;

    // pnpm hides the real packages under .pnpm/<pkg>@<ver>/node_modules/.
    if (name === ".pnpm") {
      let stores;
      try { stores = fs.readdirSync(path.join(nmDir, name), { withFileTypes: true }); } catch { continue; }
      for (const s of stores) {
        if (s.isDirectory()) scanInstalled(path.join(nmDir, name, s.name, "node_modules"), depth + 1);
      }
      continue;
    }
    if (name.startsWith(".")) continue;

    if (name.startsWith("@")) { scanInstalled(path.join(nmDir, name), depth + 1); continue; }

    const pkgDir = path.join(nmDir, name);
    const pj = path.join(pkgDir, "package.json");
    if (fs.existsSync(pj)) {
      try {
        const d = JSON.parse(fs.readFileSync(pj, "utf8"));
        // Only optionals a production package asked for, plus the platform
        // siblings they pull in (@img/sharp-linux-x64 brings @img/sharp-libvips-*),
        // which share the requested name leading scope+stem.
        const wanted = d.name && (optionalWanted.has(d.name) ||
          [...optionalWanted].some((w) => {
            const stem = w.includes("/") ? w.split("/")[1].split("-")[0] : w.split("-")[0];
            return stem.length > 3 && d.name.includes(stem);
          }));
        if (d.name && d.version && wanted) {
          const key = `${d.name}@${d.version}`;
          if (!seen.has(key)) {
            seen.set(key, { name: d.name, version: d.version, license: d.license, installedOnly: true });
          }
        }
      } catch { /* unreadable package.json is not a license signal */ }
    }
    scanInstalled(path.join(pkgDir, "node_modules"), depth + 1);
  }
}
scanInstalled(path.join(dir, "node_modules"), 0);

const rows = [], violations = [], unknowns = [];
let exempt = 0;
for (const [, info] of [...seen.entries()].sort()) {
  const { name, version, license } = info;
  if (name.startsWith("@reliantlabs/") || name.startsWith("@reliant-labs/")) continue;

  const v = verdict(license);
  const shown = v.why || "UNKNOWN";
  rows.push({ name, version, license: shown, ok: v.ok });
  if (v.ok) continue;

  // The strong-copyleft family is NOT exemptable, by policy. An allowlist
  // entry is a judgement call someone makes under deadline; these licenses
  // are the ones where being wrong is expensive and irreversible, so the
  // answer is to remove the dependency, not to record a reason for keeping
  // it. LGPL is in this set deliberately: npm ships native addons whose
  // obligations are murkier than the classic dynamic-linking carve-out, and
  // the decision here is to avoid the family rather than argue it case by
  // case. Everything else (an unusual permissive variant, an undeclared
  // license) remains exemptable.
  const HARD = ["AGPL", "GPL", "LGPL", "SSPL", "BSL", "BUSL", "ELASTIC"];
  const isHard = HARD.some((d) => shown.toUpperCase().includes(d));

  if (!isHard && allowlist.has(name)) { exempt++; continue; }
  if (isHard && allowlist.has(name)) {
    console.error(`check-npm-licenses: ${name} is allowlisted, but ${shown} cannot be exempted — remove the dependency instead.`);
  }
  (shown === "UNKNOWN" ? unknowns : violations).push({ name, version, license: shown });
}

if (mode === "list") {
  for (const r of rows) console.log(`${r.name.padEnd(52)} ${String(r.version).padEnd(16)} ${r.license}`);
  process.exit(0);
}
if (mode === "json") {
  console.log(JSON.stringify({ dir, checked: rows.length, exempt, violations, unknowns }, null, 2));
  process.exit(violations.length || unknowns.length ? 1 : 0);
}

const label = path.relative(process.cwd(), dir) || ".";
if (!violations.length && !unknowns.length) {
  console.log(`✅ ${label}: ${rows.length} production dependencies checked, all permissive.` +
    (exempt ? ` ${exempt} allowlisted.` : ""));
  process.exit(0);
}

console.log(`❌ ${label}: ${violations.length + unknowns.length} of ${rows.length} production dependencies need a decision.`);
if (violations.length) {
  console.log("\n  Non-permissive license:");
  for (const v of violations) console.log(`    ${v.license.padEnd(30)} ${v.name}@${v.version}`);
}
if (unknowns.length) {
  console.log("\n  No license declared (cannot verify — treat as unresolved):");
  for (const u of unknowns) console.log(`    ${"UNKNOWN".padEnd(30)} ${u.name}@${u.version}`);
}
console.log(`
  Each of these is a decision, not a formality:
    - Not actually needed?  Remove it, or exclude it in package.json.
    - Fine on inspection?   Add "<package>  # why" to .license-allowlist-npm.
                            Per-package, and say WHY, not just what.
`);
process.exit(1);
'
}

status=0
for dir in "${dirs[@]}"; do
  run_one "${dir}" || status=1
done

if [ "${mode}" = "check" ] && [ "${status}" -ne 0 ]; then
  echo "check-npm-licenses: gate failed." >&2
fi
exit "${status}"
