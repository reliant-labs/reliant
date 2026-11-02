# Releasing forge and propagating the bump

forge ships **one** Go module, `github.com/reliant-labs/forge`, tagged `vX.Y.Z`.
It carries both the CLI (reliant embeds it) and the `pkg/*` runtime libraries
generated projects import.

Consumers: **reliant** and **control-plane** each require that one module. The
managed workspace **daemon image** is the reliant binary cross-compiled
(`control-plane/docker/Dockerfile.reliant.dev` COPYs it) — so its forge version
flows transitively from reliant's `go.mod`; there is **no forge pin in any
Dockerfile** to bump.

See `docs/versioning.md` for the dev-vs-release dependency model. This file is
the operational checklist for cutting a version.

## 0. Who releases: the RC train

**Maintainers and agents do not cut forge releases by hand.** The Reliant workflow
`rc-train` (control-plane `.reliant/workflows/rc-train.yaml`, driving
`scripts/rc/rc.sh`; design in control-plane `docs/design/release-train.md`) does.
Every 10 minutes it reads the three mains; when a main moved and 2h passed, or a PR
labelled `release:asap` merged, it cuts a release-candidate set — a forge commit
`F` and matching reliant and control-plane commits — gates it, and promotes the
first green set. Promotion pushes the final tags, forge's with
`scripts/release-forge.sh --commit F` (the release `NEXT_VERSION` names at `F`; see
below), then opens one pull request per consumer moving its pin to the promoted
set. It replaced the `release`, `release-forge` and `pin-bot` workflows.

**web-runtime** is released separately, when `web-runtime/package.json` is ahead of
its newest `web-runtime/v*` tag: `scripts/release-web-runtime.sh vX.Y.Z`, then the
tag push that publishes it. The version bump itself is a reviewed PR; the release
never invents one.

Need a forge change released sooner: label its PR **`release:asap`**. One release of
the repository runs at a time (`scripts/release-lock.sh`, a lock ref on origin), and
`release-forge.sh` refuses a tag that already exists, locally or on origin, before it
builds anything. The steps below are what the train runs, for whoever fixes it, and
what an owner-authorized hand release runs.

## 1. Tag forge — ONE command, and nothing is pushed to `main`

```sh
cd forge
task release:forge -- --dry-run   # optional: every validation, no side effects
task release:forge                # cuts the version NEXT_VERSION names
task release:forge -- --commit <sha>   # a specific green commit of main instead
```

**A release never pushes to `main`.** `main` merges only through the merge queue
(the `main` ruleset), and the release does not need an exception to it:

- the tag goes on a commit `main` **already has** — by default the newest commit of
  `main`'s first-parent history whose full CI is green (the proof
  `scripts/check-release-tests.sh` reads; `FORGE_RELEASE_GREEN_DEPTH` commits are
  searched, default 40), or the one `--commit` names. `main` keeps merging while a
  release is cut, and the tag stays reachable from it;
- the **bookkeeping** — `VERSION`, `internal/buildinfo/VERSION`, `NEXT_VERSION`,
  `CHANGELOG.md`, `npm/package.json` — is committed on top of that commit, pushed to
  `release/vX.Y.Z` together with the tag (one atomic push), and lands as the pull
  request `chore: release vX.Y.Z`, set to auto-merge, through review and the merge
  queue like any other change.

So the tagged tree still names the previous version in `VERSION`. Nothing reads that
file for a release: the archives are stamped with `-X main.Version=vX.Y.Z`
(`scripts/build-release-assets.sh`), `go install …@vX.Y.Z` reports the module
version, and `release-npm.yml` takes the version from the tag.

The release is cut in a private worktree at that commit, never in your checkout:
uncommitted work there cannot block a release or be swept into one, and the
worktree is removed when the script exits.

If origin refuses the push, the script deletes the local tag it made and says
which refusal it was: origin already has the tag (another cut released it: fetch
it, do not re-cut), a `release/vX.Y.Z` branch is left from an earlier attempt, or
the push itself was refused (a ruleset, a permission, the network).

**`NEXT_VERSION` declares the next release.** It is not always the last tag's
patch+1: v0.1.46 was followed by v1.0.0. Lint rollout rows (`IntroducedIn`),
codemods (`Version`) and migration playbooks (`migrations/<version>/`) for
unreleased work are keyed to it, and their tests fail a key that is neither
shipped (at or before `VERSION`) nor `NEXT_VERSION`. The script refuses a version
that is not `NEXT_VERSION` or a pre-release of it (`vX.Y.Z-rc.1`); to cut a
minor or major release, change `NEXT_VERSION` (and those keys) in a pull request
first. A final release writes `NEXT_VERSION`'s next patch in the bookkeeping
commit. `NEXT_VERSION` is read at the commit being tagged.

What it does, in order:

1. picks the commit to tag (above) and checks it out in a private worktree;
   validates the version shape and that the tag does not already exist;
2. confirms against the module proxy that the version was never published at a
   **different** commit (see below), then requires the latest full `ci.yml` run
   at the exact commit being tagged to have succeeded. PR/merge-queue runs do not
   count: every shard of `Test (unit I/N)` and `Test (internal/cli I/N)`, and
   `Test (-short whole tier, then guards)`, must each succeed, even if `CI gate`
   waived an existing red. Missing, queued, failed, or unreadable proof blocks
   the release before files change; wait for main or dispatch full CI at that
   commit. `gh` authentication is required for GitHub publication;
3. writes the release's section of `CHANGELOG.md` (see below) and formats it
   with the prettier the pre-commit hook pins;
4. syncs `VERSION` and `internal/buildinfo/VERSION` — which must stay
   byte-identical, since `buildinfo` embeds a copy;
5. builds the module and the release archives;
6. commits the bookkeeping, tags `vX.Y.Z` at the commit `main` already has, and
   pushes the tag and `release/vX.Y.Z` in a single atomic `git push` — never
   `main`;
7. opens the `chore: release vX.Y.Z` pull request from `release/vX.Y.Z` and sets
   it to auto-merge;
8. creates the GitHub release (`gh release create`) with that section as its notes.

`--dry-run` runs every validation and every file edit in the private worktree,
prints the plan, then removes it — no commit, no tag, no push, no pull request.

### CHANGELOG.md and the GitHub release notes

`CHANGELOG.md` is written **only here**, never in a pull request —
`scripts/check-changelog-untouched.sh` fails a PR that edits it. The script
builds the new `## [vX.Y.Z] - <date>` section (`scripts/changelog-release.sh`)
from the squash commits `(#N)` between the previous `v*` tag and `HEAD` — the
merged PR titles, read from git so it works offline — and groups them by
conventional-commit type: `feat` → Added, `fix` → Fixed, everything else →
Changed. A commit with `!` after its type, a `BREAKING CHANGE:` footer, or a PR
labelled `breaking` (looked up with `gh pr list` when `gh` is available) goes
under **Breaking changes** instead. Whatever `## [Unreleased]` held is folded
into the section once and `[Unreleased]` is left empty, so **the PR title is the
changelog entry: write a good one.**

After the push, `gh release create vX.Y.Z --verify-tag --notes-file <section>`
publishes the same section. If that step fails the tag is already pushed and
immutable; the script says so, prints the retry command, and exits non-zero. A
section over GitHub's 125000-character body limit (the first release after the
old per-PR habit folds in a very long `[Unreleased]`) is published as the
generated bullets plus a link to `CHANGELOG.md`.

### The immutable-version gate

`proxy.golang.org` is immutable. Once it has served a version, that content is
permanent: deleting the tag and re-cutting it elsewhere does **not** change what
consumers download, and the version is burned forever. `pkg/v0.1.12` was burned
exactly this way — tagged, published, deleted, re-cut — and the symptom is
maddening from inside the repo, because `git show <tag>` plainly contains your
code while every consumer gets the old bytes.

So the script asks the proxy before a tag exists. A network failure is a hard
stop, not a pass; `--skip-proxy-check` is the only way past it, and only when
you have verified by hand that the version was never published.

It asks the proxy's cache (`/cached-only/<module>/@v/<version>.info`), never
the proxy itself: proxy.golang.org caches a 404 for an unrequested version for
up to 30 minutes, and a fetching `.info` request before the tag is pushed would
make the release uninstallable for that long. The cache is also the
authoritative answer: a version the proxy ever served stays in it, even after
its tag is deleted. `@v/list` is not consulted; a version missing from it is not
proof that it was never published.

### What this step used to involve

forge shipped a second module — `github.com/reliant-labs/forge/pkg`, tagged
`pkg/vX.Y.Z` — which the root module **required**. That require could not
resolve until the pkg tag was pushed, so releasing carried three extra
mechanisms, all of which are now gone:

- **a two-tag atomic push**, so a pkg tag could never ship without its root
  release (v0.0.4 shipped requiring `pkg v0.0.3` while `pkg/v0.0.4` existed, and
  no in-repo build could have noticed);
- **the `go.sum` trap** — an in-repo `go build ./...` passed with no `forge/pkg`
  hashes in `go.sum`, because `go.work` resolved `pkg` from disk, so the script
  made a temporary bare clone, tagged it locally, and resolved the not-yet-public
  version through `GOPROXY=direct` to record real hashes;
- **a bump of `defaultPublishedForgePkgVersion`**, the constant a dev build wrote
  into scaffolds as the forge/pkg pin.

A single module cannot require itself, so there is no unpushed version to
resolve, no second tag to order, and no hand-maintained pin to drift.
`scripts/release-pkg.sh` and `task release:pkg` are deleted.

### Installers (what the release script ships beyond the tag)

`release-forge.sh` also publishes what lets someone with no Go toolchain install
forge. All of it hangs off one naming contract, owned by
`scripts/build-release-assets.sh`: `forge_<os>_<arch>.tar.gz` for
linux/darwin/windows × amd64/arm64 (the binary, `LICENSE`, `NOTICE`) plus
`checksums.txt`, attached to the GitHub release. Change a name there and you must
change `install.sh`, `install.ps1`, `npm/lib/install.js` and
`scripts/render-homebrew-formula.sh` in the same pull request;
`internal/installers` fails if they drift.

| Channel                                                   | What the release does                                                                                                                                                                                                                                                      | Needs a human                                                                                                                                                   |
| --------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `curl \| sh` (`install.sh`), `irm \| iex` (`install.ps1`) | Nothing: they read `main` and download the release's assets.                                                                                                                                                                                                               | No                                                                                                                                                              |
| GitHub release assets                                     | `build-release-assets.sh` runs before the bookkeeping commit; `gh release create` attaches the archives and `checksums.txt`.                                                                                                                                               | No                                                                                                                                                              |
| Homebrew `reliant-labs/reliant/forge`                     | Dispatches `forge-release-published` to `reliant-labs/homebrew-reliant`; its `update-forge-formula.yml` renders `Formula/forge.rb` (`scripts/render-homebrew-formula.sh`), installs and tests it, and opens a PR.                                                          | Yes: the tap's ruleset requires an approving review, same as the cask. The workflow lives in the tap; `docs/installers/update-forge-formula.yml` is its source. |
| npm `@reliantlabs/forge`                                  | `npm/package.json` is synced in the bookkeeping commit. The GitHub release's `release: published` event runs `.github/workflows/release-npm.yml`, which publishes the shim at the tag's version with npm trusted publishing (OIDC) and waits for the registry to serve it. | No, once the trusted publisher is registered (below).                                                                                                           |

Re-run either channel for an existing release by hand:

```sh
gh workflow run update-forge-formula.yml --repo reliant-labs/homebrew-reliant -f version=X.Y.Z
gh workflow run release-npm.yml --repo reliant-labs/forge -f version=X.Y.Z -f dry_run=false
```

`release-npm.yml` takes the version from the tag, not from `npm/package.json`
at the tagged commit (every release tags a commit whose manifest still names
the previous version; the bump is in the bookkeeping pull request), smoke-tests the packed tarball against the release's
archives, and is a no-op for a version the registry already serves.

**The npm trusted publisher.** There is no npm token anywhere: npm accepts a
publish of `@reliantlabs/forge` only from `reliant-labs/forge`'s
`release-npm.yml`, the same arrangement as `release-web-runtime.yml`. An npm
owner of the `@reliantlabs` scope registers it once, on the package's
**Settings → Trusted publishing** page (GitHub Actions; organization
`reliant-labs`, repository `forge`, workflow filename `release-npm.yml`, no
environment; allow `npm publish`), or from the command line:

```sh
npx npm@11 trust github @reliantlabs/forge --repo reliant-labs/forge --file release-npm.yml --allow-publish
```

npm only offers that page for a package that exists, so the very first version
is published by that owner from a checkout of `main`
(`cd npm && npm publish --access public`, with `npm/package.json` at the latest
release's version). A new registration expires unless a publish goes through
it within two days; the next release does that, so register it when one is due.
Renaming the workflow file breaks publishing until the registration is updated.

A prerelease tag gets assets but is never sent to Homebrew or npm.

If the GitHub release step fails after the tag is pushed, the script prints the
retry command; build the archives again with
`scripts/build-release-assets.sh vX.Y.Z dist/` and attach `dist/*` to it.

## 2. Bump the consumers

Pre-launch, reliant and control-plane pin forge by **commit**, not by tag
(reliant `dev-docs/pinning.md` records why, and the switch back at launch). A
release tag is still a valid pin: both pin scripts take any ref.

```sh
cd reliant
make pin-forge REF=vX.Y.Z          # go.mod, go.sum, forge.yaml forge_version, regenerated output
go build ./...                     # then open the PR

cd ../control-plane                # AFTER reliant: control-plane depends on it
task pin:forge REF=vX.Y.Z          # go.mod, gen/go.mod and forge.yaml forge_version together
```

Each script resolves the ref against forge's remote rather than the local
`../forge` checkout, edits every pin together (a partial bump is the failure
they exist to prevent), and stops without committing. Commit and open the PR yourself.

`forge_version` in `forge.yaml` and the `go.mod` require are necessarily the
**same version** — one module, one version — and both repos' CI installs the
forge CLI from `forge_version` and fails when the two disagree, so there are no
per-workflow `go install ...@vX.Y.Z` pins to chase.

### The KCL module needs no tag

There is deliberately no `kcl-vX.Y.Z` step here. The forge KCL module is
embedded in the binary, and every render supplies it to KCL from the binary
doing the render, so a release publishes it automatically by shipping the
binary — a project pinned to the release renders against exactly its module.
Forge once scaffolded a published KCL git tag on release builds; the tag was
never pushed, and every project a released forge created could not resolve its
deploy manifests.

## 3. Forge's own CI needs no pin

Nothing to do here — this step is listed only because it used to exist.

`forge/.github/workflows/ci.yml` runs `go install ./cmd/forge`, building the
forge under test from the working tree. That is deliberate: installing a tag
would validate every PR against the LAST release, so no change to a template or
an emitter could ever go green until after it shipped.

## 4. Rebuild the daemon image

Once reliant's `go.mod` is on the new forge, the next daemon-image build (the
reliant binary → `Dockerfile.reliant.dev`) picks it up automatically. No manual
version edit; just rebuild/deploy per the normal flow.

## Note on history rewrites

If forge history is ever rewritten (e.g. redaction via `git filter-repo`), the
existing version tags move to new commit hashes. Force-push the moved tags, and
bump consumers to a **fresh** tag on the rewritten history — anything pinning the
moved tag will otherwise hit a go.sum/module-hash mismatch.
