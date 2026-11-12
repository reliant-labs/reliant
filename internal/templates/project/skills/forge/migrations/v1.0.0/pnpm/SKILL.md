---
name: migration-v1.0.0-pnpm
description: Optional pnpm adoption for existing frontends. New forge frontends pin pnpm 12.15.0; npm projects remain supported. Import the npm lockfile, move overrides and build-script approvals to pnpm-workspace.yaml, declare phantom dependencies exposed by isolated node_modules, and update owned Dockerfiles, CI and Taskfile commands. Keep pnpm's shared store on the same mount as worktrees for hard links.
relevance: migration
---

# v1.0.0 — pnpm for new frontends; optional adoption for existing ones

**Use when** you want an npm frontend to use pnpm's shared, hard-linked
content-addressable store. Existing npm and yarn projects keep working: forge
selects their package manager from their lockfile, then `packageManager` when
there is no lockfile. This playbook changes owned files; `forge generate`
does not migrate them for you. Apply one frontend at a time. Do not import an
old, unrelated pnpm lockfile left in the repository.

## 1. Import, don't re-resolve the world

Start from a clean branch and keep `package-lock.json` until the import is
verified. Install pnpm 12.15.0 (`corepack enable pnpm`, if corepack is installed;
otherwise `npm install -g pnpm@12.15.0`). In the frontend's directory:

1. Set `"packageManager": "pnpm@12.15.0"` in `package.json`.
2. Move npm's `overrides` out of package.json into `pnpm-workspace.yaml`.
   pnpm 11+ ignores npm's top-level overrides. A nested npm override
   `{"parent": {"child": "^1.2.3"}}` becomes `"parent>child": ^1.2.3`.
   Preserve the security justification and parent scope; don't turn a
   subtree-specific patch into a global pin.
3. Write the build-script policy below, then run `pnpm import`.
4. Compare resolved versions with the old lockfile. Platform-only optional
   dependencies can differ; unexplained application-version changes need
   investigation, not acceptance as part of a package-manager switch.
5. Remove **this frontend's** old `node_modules` (never someone else's), then
   run `pnpm install --frozen-lockfile` and all its checks before deleting
   `package-lock.json`. Commit the manifest, settings and pnpm-lock.yaml
   together; do not leave competing lockfiles.

pnpm 12 may write two YAML documents to pnpm-lock.yaml when `packageManager`
is set: one for pnpm itself, one for the app. That is valid; do not strip the
first. Forge reads both.

For a standalone frontend, its own `pnpm-workspace.yaml` is its settings root
(it does **not** opt the project into the shared API/hooks layout). Example:

```yaml
# Forge installs explicitly before dev/build/lint. Scripts must not reinstall
# a standalone registry lock over an active dev bridge's linked runtime.
verifyDepsBeforeRun: false

# GHSA-px8p-9vwx-vf98: recorder still pins fflate 0.7.4.
# Remove once its own dependency range reaches the patched release.
overrides:
  "@hyperdx/otel-web-session-recorder>fflate": ^0.7.5

# Review every script, don't approve a wildcard or all builds.
allowBuilds:
  esbuild: true        # fetches/verifies its platform binary
  unrs-resolver: true # if present: fetches/verifies its platform binary
  protobufjs: false   # if present: postinstall only prints a version warning
```

**pnpm 12 calls the policy `allowBuilds`.** It replaces the older
`onlyBuiltDependencies` / `ignoredBuiltDependencies` lists; do not configure
both. An unlisted build script fails with `ERR_PNPM_IGNORED_BUILDS`. Inspect
what it does before recording `true` or `false`; rerun the frozen install.
Do not silence the failure with `dangerouslyAllowAllBuilds` or `ignoreScripts`.

For the opt-in shared `packages/*` + `frontends/*` workspace, settings go in
the project's **root** pnpm-workspace.yaml, not in each member. Keep every
`workspace:*` dependency in that workspace. Expo SDKs before 54 need
`nodeLinker: hoisted`; don't change an existing mobile packager's layout
without validating its native bundle. Electron packaging is likewise a
separate validation, not a reason to flatten every web frontend.

## 2. Fix the undeclared dependencies npm hid

pnpm's default isolated layout lets a package import only what it declares.
Run lint, typecheck, tests and a production build — not just install. Add
missing direct dependencies to the package that imports them, with a
reviewed version range. Include imports in Vite manualChunks, build plugins,
ESLint configuration and test helpers. npm's hoisting often hid these phantom
dependencies (one real frontend migration exposed four).

Do **not** set `shamefullyHoist` to make the errors disappear. A broken
third-party package may need a narrow `packageExtensions` entry with a reason;
your own missing declarations belong in package.json.

## 3. Update owned build and test entrypoints

- Replace `npm ci` with `pnpm install --frozen-lockfile`; replace `npm run`
  with `pnpm run`, `npm install <pkg>` with `pnpm add <pkg>`, and
  `npx --no-install <tool>` with `pnpm exec <tool>`.
- Taskfile/shell scripts: retain your existing lane semantics and flags.
  New scaffolds' frontend task chooses `$PM` per frontend; mixed-manager
  projects must not force the unmigrated frontends through pnpm.
- GitHub Actions: install `pnpm/action-setup@v4` **before** setup-node's pnpm
  cache lookup. Give it `package_json_file: frontends/web/package.json` (not
  a duplicated version), then use `cache: pnpm` and
  `cache-dependency-path: frontends/web/pnpm-lock.yaml` on setup-node.
  Dependabot still calls its ecosystem `npm` for pnpm projects.
- Dockerfile (Node 22): `ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0`, then
  `RUN corepack enable pnpm`. Copy package.json, pnpm-lock.yaml **and**
  pnpm-workspace.yaml before installing. Use `pnpm fetch --frozen-lockfile`
  and `pnpm install --offline --frozen-lockfile` with the same store cache
  mount on both RUNs. Build with `pnpm run build`. Node 25+ no longer bundles
  corepack: install a pinned corepack explicitly if upgrading the base.
- Update KCL's explicit `dev_runner = "npm"` to `"pnpm"`. Forge detects the
  lockfile for installs, but the declared dev command must use pnpm too.
- Review postinstall scripts, release scripts and OS-specific wrappers. A
  web frontend migration does not automatically migrate Electron or Expo.

The dev Go bridge has a pnpm twin: a gitignored root pnpm-workspace.yaml
links local web-runtime through `.forge-link/`. Run `pnpm install` at that
root after it is written, or let forge's own install do so. Never commit
that machine-local root or its lockfile. A user's tracked root workspace is
not overwritten by forge. Independent frontends share the dev overlay only
when their `allowBuilds` and overrides maps are identical (absent approvals
count too). Otherwise forge reports both sources and removes any older owned
overlay, leaving standalone installs; it never unions approvals or picks a
later security override. Relative `file:`/`link:` overrides are rejected by
the bridge because moving their settings root would change their meaning.

## 4. Put the store on the worktrees' mount

A shared store saves disk only when pnpm can hard-link into node_modules.
Linux rejects links across **mount points**, even two bind mounts of the
same ext4 disk. A separately mounted `/var/cache` store silently copies every
package into every worktree: correct installs, but no disk savings.

```bash
pnpm store path
# Example on a cloud machine whose worktrees live on /home/workspace:
export pnpm_config_store_dir="$HOME/.local/share/pnpm/store"
pnpm install --frozen-lockfile
```

Set the store in the machine's config/environment, never a home-specific path
in a project's tracked file. Verify pnpm says packages are hard linked, not
copied, and inspect link counts (`stat` reports >1) on package files. A
container's BuildKit cache mount is a different case: copy fallback into its
image layer is expected; the mount avoids network downloads on rebuilds.

## Verify

From a fresh install, run the frontend's lint, typecheck, unit tests and
production build, plus its Docker build if it ships one. Then run
`forge generate` and the scoped forge lint checks: the lockfile must remain
in sync. Measure install time and **unique allocated** node_modules bytes,
not just `du` of the tree (which counts the hard links again). Keep the old
lockfile only until the comparison is complete, not in the final commit.
