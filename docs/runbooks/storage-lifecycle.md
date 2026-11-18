# Local storage lifecycle

`forge storage` bounds rebuildable development state. Persistent volumes and
workspaces have separate lifecycles: registry layers, containerd images, BuildKit
cache and application data can all live in Docker volumes, so volume age is not
an indication that a volume is safe to remove. Named volumes are never removed;
anonymous ones only under the rules in
[Dangling images and anonymous volumes](#dangling-images-and-anonymous-volumes).

## Defaults

| Storage                                                          | Policy                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| ---------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Physical host filesystem                                         | Refuse a build below 20 GiB available                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| BuildKit                                                         | Remove cache unused for 7 days toward 20 GiB per registered local builder                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| k3d node images                                                  | Kubelet removes unused images after 7 days; pressure GC at 80%, down to 70%                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| Node container logs                                              | 10 MiB × 3 files per container                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| Registered local registry repositories                           | Keep 14 days and newest 5 distinct digests; protect workloads, release pins and stable aliases                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| Forge dev logs (`forge env up` foreground tee)                   | Rotate at 50 MiB (`FORGE_LOG_ROTATE_BYTES`; `0` disables); the current stream keeps its path                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| Rotated Forge logs                                               | Keep newest 5 per stream; expire after 7 days or toward 1 GiB per project/environment                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| Cross-repo source cache (`<UserCacheDir>/forge/sources`)         | Per repository the newest 2 clones (`source_cache_keep`) are never touched. Beyond them: a clone idle 24h loses its installed `node_modules` but stays a cache hit, since every build reinstalls it (`source_cache_slim_after`); one idle 7 days is evicted (`source_cache_unused`); and past `source_cache_gib` (default 6) the least recently used are evicted down to 80%, after slimming. A clone a running process holds is never touched. A budget the keep floor alone exceeds is reported, not trimmed into                                                                                                                            |
| Temp scratch (`$TMPDIR`)                                         | Remove allowlisted toolchain/test scratch, and orphaned Go `t.TempDir()` roots (`Test…<digits>/` holding only `001`, `002`… dirs), idle 24h and open by no process; never anything with git metadata                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| Host images tagged for a dead local registry                     | Untag `localhost:<port>`, `registry.localhost:<port>` and `k3d-*:<port>` images when no running registry backs that alias and no container (running or stopped) uses the image                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| Shared Go build cache (`go env GOCACHE`) and golangci-lint cache | Remove entries unused for 48h (`go_cache_unused`; the age rule never touches the last 2h). Over its budget — `go_cache_gib` for the build cache, `golangci_cache_gib` (default 4) for the lint cache, each capped at 15% of the volume (`go_cache_volume_percent`) — it also evicts least recently used first down to 80% of the budget. Entries are sized by the blocks they occupy, not their length, never anything used in the last 2h either; a budget the last 2h alone exceeds is reported as `over_budget`, never trimmed into. Executable (`go run`/`go tool`) entries count and are removed whole; top-level files are never touched |
| Worktrees                                                        | Idle ≥ 24h, unlocked, unused, clean, ignored files all rebuildable, HEAD pushed; removed only by an explicit `forge storage gc --apply` under `worktree_reap` (see Worktrees)                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| Every unlink a pass makes                                        | At most 50,000 per pass (`max_deletes_per_pass`), paced to 1,000/s (`delete_rate_per_sec`); the pass backs off at the first EMFILE/ENFILE/ENOTCONN/EIO (see Bounded deletion)                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| Dangling images (untagged)                                       | Remove an image untagged, used by no container (running or stopped) and built over 1h ago; full and automatic passes; at most 100 Docker removals per pass (`max_docker_removals_per_pass`)                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| Anonymous Docker volumes                                         | Remove a labelled, 64-hex-named, plain `local` volume no container uses, seen unused for 14 days (`anonymous_volume_unused`, `off` disables); only by an explicit `forge storage gc --apply`                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| Named Docker volumes, database/PVC/workspace volumes             | Never removed by storage GC                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |

The cache and log budgets are targets: recent/in-use cache and the diagnostic
floor can exceed them. No budget overrides release or workload protection. The
host-space check uses the host filesystem, not the much larger sparse disk that
Docker Desktop exposes to Linux. Build checks also cover output paths, the OS
temporary directory, and explicitly configured `GOTMPDIR`, `GOCACHE` and
`GOMODCACHE` paths. Add a custom Docker data disk or cache mount configured
through a tool-specific config file to `host_paths`.

## Tool cache

Every tool forge installs for itself lives in one machine-wide cache
(`internal/toolcache`): the gate's pinned linters and scanners (`gate/`), the
golangci-lint `forge lint` builds (`golangci-lint/`), and the go.mod-pinned
codegen plugins and air (`pinned/`). Each entry is one tool at one version
(and, for a linter, one Go toolchain), shared by every worktree, and never
rewritten: a bump installs a new entry beside the old one. An install builds
into a staging directory and appears by one rename, so nothing ever runs a
half-written binary; two processes that miss the same entry at once may both
build it, and the loser of the rename uses the winner's. `FORGE_TOOLS_CACHE`
moves the whole cache. Forge does not evict from it: an entry is a few binaries
per pinned version, and what a full disk gives up is the machine steward's call.

## Go caches

Do not set a private `GOCACHE`/`GOMODCACHE` per task and do not run `go clean
-cache` or `go clean -modcache`. Without `-trimpath` Go keys every main-module
package by its absolute directory, so each worktree builds its own copy of the
shared cache; a private cache per task is a cold 5-14 GB copy nobody reuses, and
`go clean -cache` under running builds produces `link: cannot open file
…/go-build/…-d`. Age-based trim is the safe mechanism: Go bumps an entry's mtime
on use once it is an hour stale, so an entry older than 2h is not in use by a
live build. The installed schedule (`forge storage install`) runs the bounded
non-disruptive pass (`forge storage auto-gc`) HOURLY, because agent load adds
~40 GB/h of cache entries and a daily trim cannot hold a 60 GiB budget; the
full pass, with registry downtime, stays daily at 03:30. Both take the
maintenance lock, so they never overlap. `forge env up` additionally starts
auto-gc at most once per 24h. `forge storage gc`/`auto-gc` apply it; `forge storage status` reports the shared cache
size. The module cache (`GOMODCACHE`) is Go's: it is shared by every worktree,
and forge never trims it. The detached auto-gc pass has a 15
minute budget so a first trim of a very large cache can make progress; a pass
cut off by the deadline stops cleanly and resumes next time. Dry-run
(`forge storage gc`) prints each candidate and its bytes.

`forge storage gocache --apply` is the Go-cache layer alone, with the whole of
its budget (`--budget`, default 10m) instead of the slice of a larger pass.
Schedule it every few minutes on a machine that builds a lot: under agent load
entries cross the 2h floor at ~25 GiB/h, so between two hourly passes that much
removable cache sits on the disk (the Reliant daemon runs it every 10 minutes
on cloud machines). It takes the maintenance lock like every other pass and
records `last-gocache-gc.json`. Every pass's record carries `go_caches`: each
cache's estimated size, budget, the bytes inside the floor, the cutoff, what was
removed, and how many shards the walk covered. A Go-cache step stopped by its
deadline is recorded under `cut_off_layers`; until 2026-10-22 it was dropped,
so a trim that ran out of time under load recorded a clean pass while ~46 GiB of
entries past the floor stayed on disk.

Neither rule ever deletes an entry last used in the last 2h, whatever the
budget pressure. Over its budget a cache is trimmed least recently used first
down to a low-water mark (80% of the budget, so the next pass is not trimming
a handful of entries), and that trim stops at the same 2h floor as the age
rule. The floor is not a knob.

Why, from cmd/go's own cache code: Go restamps an entry's mtime on use only
when it is more than an hour stale (`markUsed`, `mtimeInterval`), and its own
trim deletes only entries unused for five days. So an mtime younger than 1h
says nothing about whether the entry is in use; only an entry older than 1h+D
is certainly idle for D. The hazard is an entry a running build has resolved
but not yet consumed — export data a vet or lint run is about to open, an
archive waiting for the link step: deleting it fails that build with `could
not import … could not load export data: open …/go-build/…-d: no such file or
directory` or `cannot open file`. From 2026-10-22 to 2026-10-23 (#1191) the
size rule did reach inside the floor, down to a `go_cache_min_age` of 20m, and
it failed three merge gates in a day on one machine; the key is gone (a
storage.json that still holds it loads, with an unknown-key warning).
`trim.txt` and `README` are top-level files, which the trim never touches, and
cmd/go's own five-day trim is independent.

The 2h floor covers a build younger than about an hour: it can only have looked
up entries whose mtime is at least its start minus 1h. A build that has run
longer can hold an entry that is by then past the floor (`go test -race` across
a repo takes 40-70 minutes on this machine, longer behind a heavy-slot queue),
so the cutoff is also held back to the start of the oldest running Go toolchain
process (`go`, `compile`, `link`, `vet`, `golangci-lint`, `staticcheck`,
`govulncheck`) minus 1h5m. That is a lease read from the process table, not a
bigger number: a build of any length is covered while it runs and nothing is
retained after. A process older than 3h is a server (`go run` of a dev server),
not a build, and does not count, or it would pin the cache for as long as it
lives. A process table that cannot be read counts as a build 3h old. The pass
says when this held the cutoff back, and the record carries the build's age
(`oldest_live_build_ns` in `go_caches`).

A cache that still takes in more than its budget inside the floor cannot be
trimmed to it. The pass then removes everything older, prints
`WARNING … no trim can reach it` with the size used inside that window and the
fill rate, and records the cache under `over_budget` in `last-auto-gc.json` /
`last-full-gc.json` (not a failure). `forge storage status` prints the same
split. The remedy there is a larger `go_cache_gib` or a slower fill.
On 2026-10-21 a 20 GiB budget held a 152 GiB cache filling at ~70 GiB/h: 87% of
its bytes were compiles keyed on a worktree's absolute path, because the repos
pinned `toolchain go1.27.3`, `GOTOOLCHAIN=auto` switched to a downloaded
toolchain, and that toolchain's `$GOROOT/go.env` lacks the `GOFLAGS=-trimpath`
the workspace image appends to its own. Check with `go env GOFLAGS` in a
worktree. Anything forge starts with its own GOFLAGS merges `-trimpath` back in
(`goexec.WithTrimpath`); `internal/goexec`'s `TestGOFLAGSOverridesKeepTrimpath`
keeps it that way.

## Bounded deletion

On 2026-10-09 one `forge storage gc --apply` unlinked 175,324 Go build cache
entries (53.6 GiB) in about 30 seconds on a kata-containers workspace whose home
volume is served over virtiofs, while builds were running. The host's virtiofsd
ran out of file descriptors, the guest saw EMFILE, and the volume was detached
from the container seconds later. The trim had kept unlinking through the
EMFILEs. So every unlink a pass makes — the Go build cache, golangci-lint, the
temp sweep, the source cache and rotated logs — goes through one per-pass
governor:

- **Capped.** One pass unlinks at most `max_deletes_per_pass` entries (default
  50,000) across every layer. The Go caches may spend at most 80% of it (the
  shared build cache 60%), so the temp sweep and source cache always have some.
  What is left is retained, and the next pass continues where this one stopped:
  a budget is reached over several passes, never in one burst. A capped layer is
  recorded as cut off, which is not a failure. The exception is a Go cache over
  its SIZE budget: it may unlink what the sample says it takes to get back under
  the budget, beyond its share and without spending the cap the later layers
  share. Under agent load the cache outgrows any fixed cap (one pass estimated
  62 GiB against a 20 GiB budget and freed 6.1 GiB before stopping), so a cap
  there means it never converges; pacing, back-off and the layer's time slice
  still bound every one of those unlinks.
- **Paced.** Unlinks are granted in batches of 200, and a batch may not finish
  faster than `delete_rate_per_sec` allows (default 1,000/s). Unlinks are
  serial across workers.
- **Backed off.** The first EMFILE, ENFILE, ENOTCONN or EIO, from an unlink or
  from the reads that precede one, stops the pass. Nothing more is unlinked, the
  layers after it (Docker included) do not start, and the rest is retained. The
  pass prints `storage maintenance BACKED OFF: …` and records the layers in
  `backed_off_layers`. It is not a failure, so the command exits 0 and nothing
  is tempted to retry at once; the next scheduled or automatic pass resumes.

A tree stopped partway keeps the marker that identifies it (a build cache's
`README`), so the next pass still recognises it. A source-cache clone is renamed to `.forge-evicting-<name>-<ts>` before its
tree is deleted, so a build never resolves a half-deleted pin, and the next
pass finishes the leftover. Inside a pass, removing a worktree is charged
against the same budget (git makes those unlinks, so they are not paced); a
worktree that does not fit is held as `deferred`. Removing a dangling image or
an anonymous volume is granted as one paced unlink too, and is also counted
against `max_docker_removals_per_pass` (default 100), because the daemon makes
that object's unlinks and one object can be thousands of files. A daemon error
reporting `too many open files`, `input/output error` or `transport endpoint is
not connected` backs the pass off like the errno itself, and the builder and
registry layers do not start.

Direct Go, frontend, shell and Docker build lanes recheck capacity before starting.
`forge env up` checks before startup and host launches. `forge storage check
[path...]` exposes the same check for custom build/watch commands. New scaffolded
Air configs prefix every rebuild with `forge storage check ./tmp &&`; add this
prefix to existing/custom Air configs to guard their internal rebuild loop.
These are admission checks, not disk reservations: concurrent builds and one
large in-progress build can still consume the reserve.

On a CI runner (`CI` or `GITHUB_ACTIONS` set) a shortfall is printed as a
warning and the build proceeds. The reserve guards a persistent machine against
accumulated caches; an ephemeral runner's disk is discarded with the job, and a
stock GitHub runner starts below the 20 GiB default. A disk that cannot be read
still refuses. Set `FORGE_STORAGE_ENFORCE_RESERVE=1` on a persistent self-hosted
runner to keep the refusal.

## Set up a machine

There is nothing to register by hand. forge converges the machine policy from
facts it already holds, every time it touches them:

- `forge env up` / `forge env deploy` reconcile each declared `forge.Cluster`
  and record its context, the local registry its k3d config `registries.use`s,
  every host alias in that config's mirror keys, and the project's
  `.forge/releases` ledger pins. Warm clusters converge too, not only fresh
  creates.
- `forge build` records the local repositories it pushes and the Docker builder
  it actually uses (`BUILDX_BUILDER`, or `default`).

Convergence is additive and idempotent: it never removes an entry another
project contributed, and a registry becomes a cleanup target only once its
container, aliases, contexts and at least one repository are all known.
Projects under the system temp directory are never registered.

Each `forge storage gc` starts by pruning entries that no longer exist. A
project is dropped when its directory is gone. A cluster is dropped only when
both of these hold: its context is missing from kubeconfig, and docker shows no
container (running or stopped) labelled with it. A context that exists but is
unreachable, or a cluster whose nodes are only stopped, stays registered, and
registry cleanup keeps refusing on it. If either source cannot be read, nothing
is pruned. A registry entry is dropped when `docker ps -a` cleanly reports that
its container no longer exists (an e2e cluster's registry dies with the
cluster); an unreadable answer keeps it. A preview reports the pruning but does
not write it.

At the end of a successful `forge env up`, if the opportunistic pass has not
been attempted in 24 hours, forge starts the non-disruptive layers in the
background and returns immediately: rotated logs, the Go caches, builder cache,
the temp sweep and the source cache. The pass runs as a detached `forge storage
auto-gc` process. Its log is `logs/auto-gc.log`, next to the policy. The whole
pass, including each layer's `lsof` snapshot and its walk over entries, is
bounded at 15 minutes. A layer cut off by that limit removes nothing further,
and the rest is picked up by the next pass. It never runs registry GC, restarts
nodes or touches worktrees from that path. Set `FORGE_STORAGE_AUTO=0` to turn it off. It records each attempt,
failures included, in `last-auto-gc.json` next to the policy. That record is only
a rate limit: a machine where the pass cannot succeed is not retried on every
`up`.

Every applied full pass (`forge storage gc --apply`, and the scheduled job)
records its outcome in `last-full-gc.json`: when it ran, whether it succeeded,
and which layers failed. Only the full pass runs registry retention, so this
record is how forge knows whether that retention is working. If registries are
registered and the last full pass failed, is more than 48 hours old, or never
completed, `forge doctor` warns and `forge env up` prints one line saying so.

Registry GC, which briefly stops the registry, runs only from the scheduled job
or an explicit `forge storage gc --apply`. Install the schedule once per machine:

```sh
forge storage gc --dry-run   # review the plan
forge storage install        # daily at 03:30
```

`forge env up` and `forge doctor` say so when registries are registered but no
schedule is installed.

`forge storage register` remains the manual escape hatch: importing an old
project's ledger (`--ledger`), adopting a builder forge never used
(`--builder relbuild`), or pinning an image (`--pin`, a full reference or a
canonical digest). Release cuts pin their digests before the ledger entry is
published. Pins remain until an operator edits the policy; a failed release cut
may leave a harmless extra pin.

The default policy is `forge/storage.json` inside the OS user configuration
directory (`~/Library/Application Support` on macOS, `$XDG_CONFIG_HOME` or
`~/.config` on Linux). `FORGE_STORAGE_POLICY` overrides it for all build hooks;
`--policy` overrides a particular storage command. JSON is strict: unknown fields
are errors. Use `forge storage register --builder relbuild` to adopt another
local builder. An unavailable builder fails its own cleanup; independent healthy
builders and registries still receive maintenance.
Remote Docker contexts/builders are refused. Registration pins the Docker context
so a later context switch cannot redirect scheduled deletion elsewhere.

`install` saves a stable executable and schedules daily maintenance at 03:30 using
launchd or a systemd user timer. It does not depend on a worktree surviving. Run
it again after upgrading Forge. Linux users who need maintenance while logged
out must configure user-service lingering. `storage daemon --interval 24h` is the
portable foreground alternative. Logs are in `~/Library/Logs/forge-storage` on
macOS or the systemd journal on Linux.

Registry maintenance briefly stops pulls and pushes, replans behind a temporary
loopback-only registry, deletes eligible manifests, runs offline GC, then
restarts the public registry. All connected k3d clusters must be registered and
queryable. The protected set is every image reference found in ANY listable
resource in those clusters, custom resources included (a `Workspace` CR's image
is protected exactly like a Deployment's); only Secrets, Events and aggregated
metrics are skipped. Failure to list any resource prevents deletion. Only
explicitly registered repositories are eligible. Stable aliases (`dev`, `e2e`,
`main`, `latest`, `stable`, exact semver) are retained.

### Registries created before `--delete-enabled`

A registry created before forge passed `--delete-enabled` to k3d refuses
manifest DELETE, so retention could never reclaim anything from it. `forge
storage gc` detects this (no `storage.delete.enabled` in its `config.yml`, no
`REGISTRY_STORAGE_DELETE_ENABLED`) and, inside the maintenance window it
already takes for offline GC, rewrites `config.yml` in the stopped container
(`docker cp`). The outage therefore happens once, not on every pass. After the
restart forge sends a DELETE for a manifest that cannot exist and requires a
404 rather than 405. It is idempotent: a delete-enabled registry is never
touched, and a preview only announces that the migration would run. The edit
lives in the container's writable layer, so it survives restarts but not a
`k3d registry delete`; recreate through forge, which enables delete. If the
edit fails the pass still reclaims through its own delete-enabled helper and
the next pass retries. The volume, labels and networks are never touched.

### Stale host images

Images tagged for a local registry that no longer exists (for example
`localhost:5061/workspace-base`) are untagged by both the full and the
non-disruptive passes; `forge storage gc --dry-run` lists them. A `localhost`
or `registry.localhost` alias is live while a running container publishes that
host port or a registered running registry owns the alias; a `k3d-<name>` alias
is live while that container runs. An image any container still uses is kept.

Untagged manifests are reclaimed by forge's own graph walk, never by
distribution's `--delete-untagged` (unsafe with image indexes on 2.x). An
untagged manifest is eligible only when it is older than the retention window
AND unreachable from every retained root: retained tags, pins, protected
digests, and the children of any retained index. That reclaims the history a
re-pushed tag such as `:dev` leaves behind while keeping index children and OCI
artifacts. Any unreadable manifest aborts the whole plan. Registries forge
creates are created with `--delete-enabled`. Arbitrary remote registries,
customized registry containers, and application volumes are outside this local
implementation.

### Dangling images and anonymous volumes

Every rebuild of a tag leaves the image it replaced behind, untagged, and every
container removed without `-v` (including `docker compose down`) leaves its
anonymous volumes behind. On a cloud workspace on 2026-10-09 that was 8.9 GB of
10.35 GB of images, and 4.2 GB of 4.5 GB in 44 volumes. `forge storage gc`
prints each target; `--apply` removes them. Removal is never forced, so the
daemon's own in-use check is the last guard after forge's.

**Dangling images run in every pass, automatic ones included.** A target is an
image with no tag (`docker image ls --filter dangling=true`; an image that kept
only a digest reference counts), that no container, running or stopped, was
created from, built more than an hour ago (a `docker build` with no `-t` leaves
its result dangling until somebody tags or runs it). Its tags are read again
right before `docker rmi <id>`, because `rmi` of an image with one tag deletes
it. An untagged image cannot be selected by name, so nothing but a container or
a child image can be relying on it, and being wrong costs a rebuild of an image
whose tag already points at something newer. Oldest go first.

**Anonymous volumes are removed only by an explicit `forge storage gc
--apply`.** The automatic passes — `forge env up`'s background pass, the hourly
and daily schedule, `forge storage daemon` — only observe. A target must be:

- **Anonymous by both signals:** the `com.docker.volume.anonymous` label the
  daemon sets on every volume it creates without a name (Docker 23+; the
  workspace image installs current `docker-ce`), and the 64-hex name it
  generates. A compose volume, a k3d volume, anything a person named, and an
  anonymous volume from a daemon too old to label it are never candidates.
- **Plain `local`:** no driver options and no plugin driver. Options make even an
  anonymous volume a mount of something outside Docker's data root (`o=bind`,
  NFS), and a plugin driver's volume may be a cloud disk.
- **Unused:** mounted by no container, running or stopped.
- **Unused for `anonymous_volume_unused`** (default `336h`, 14 days; minimum
  `24h`; `off` disables the layer), counted from when forge first saw it unused.
  Docker records no last-use time, so every pass that applies records each
  unused anonymous volume's first sighting in `anonymous-volumes.json` beside the
  policy. One seen in use again, or gone, leaves the record and its clock
  restarts. A preview records nothing. On a machine forge has never observed,
  the first applying pass starts the clocks and the first removal comes one
  window later.

Why explicit only: an orphaned anonymous volume is exactly where a compose
service with no named volume keeps its data after `docker compose down` — the
`postgres` image declares `VOLUME /var/lib/postgresql/data` — and it is
recoverable only by mounting it by hash. Nothing a pass can observe tells that
from scratch. A pass nobody invoked does not make decisions about data, which is
the rule that keeps worktrees out of the automatic passes too. The window is the
recovery period; the explicit run is the consent. To recover one, list
candidates with `forge storage gc` and mount the volume by name:
`docker run --rm -v <hash>:/data alpine ls /data`.

## Existing clusters

New k3d clusters receive a persistent kubelet configuration mount. K3s 1.32+ is
required for kubelet drop-ins. Existing nodes need a deliberate restart:

```sh
forge storage status
forge storage configure-nodes
forge storage configure-nodes --apply
```

Nodes restart sequentially and must report the configured age before proceeding.
This interrupts a single-node development cluster; it does not delete its data.
Kubelet's unused-image clock resets on restart, so frequent restarts can postpone
age-based collection. Pressure GC is complementary, not a substitute for host
space monitoring.

## Worktrees

Agents create worktrees anywhere (`~/src/rl-agent-worktrees`, `/tmp`,
`~/.reliant/worktrees`, `.claude`), so the reaper does not scan the disk. git
knows every worktree of a repository wherever it lives: the layer runs
`git worktree list --porcelain` for each repository in the policy's `repos`
plus the git toplevel of each registered project. `repos` is filled by the
same converge touch points as `projects` (`forge build`, the cluster phase): the
project's own repository, its local `docker.build_contexts` and its `go.mod`
`replace` directories, so a control-plane build also covers `../reliant` and
`../forge`.

A non-main worktree is **removable** only if all hold:

- not `locked` (the ownership contract: an owner that wants its worktrees left
  alone runs `git worktree lock`) and not prunable;
- plainly a linked worktree of the repository (`not a linked worktree`
  otherwise): not the main checkout, not a directory containing it or the home
  directory, and its `.git` is a gitdir file naming one of the repository's
  `worktrees/` admin directories — a stale registration whose directory now
  holds a clone of its own is held;
- not locked by Reliant (`managed by reliant`: a lock whose reason starts
  `reliant: worktree `). Reliant knows which chat each of those is bound to
  and reclaims them itself. Where a worktree lives does not matter: an
  unlocked worktree under `~/.reliant/worktrees`, made with a plain
  `git worktree add`, is judged like any other
  (`worktree_reap_reliant_managed` is ignored);
- idle at least 24h: the newest of the directory mtime and the admin
  directory's `index`, `HEAD` and `logs/HEAD` (activity, not commit age), AND
  nothing anywhere in the tree modified within 24h — a build writing an
  ignored `node_modules` or `bin/` changes neither the admin files nor status;
- not in use: one `lsof` snapshot covers all candidates and the layer **fails
  closed** if it cannot be taken; forge's own working directory is also kept;
- `git status --porcelain --untracked-files=normal` is empty;
- every **ignored** path is on the rebuildable allowlist (`worktree_rebuildable`;
  default `node_modules`, `dist`, `bin`, `.next`, `.turbo`, `coverage`,
  `.gocache`, `*.tsbuildinfo`, `kcl.mod.lock`, `go.work`, `.forge/logs`,
  `.forge/tmp`, `.forge/generate-cache.json`, …). An entry is path segments
  matched anywhere in the path, or, with a leading `/`, only from the
  worktree's root: the defaults `/.tools` (pinned tools) and `/tmp` (scratch)
  are root-anchored, so `web/tmp` is not covered. `data/`, `.env*`, `.forge/hostinfra/`, `*.db`, `*.sqlite` and `secrets/` are
  never rebuildable, even if a policy lists them. An ignored path that is not
  on the allowlist is still not data (so does not hold the worktree) when it is
  a regular file that is a **generated** file (first line `Code generated … DO NOT
EDIT.`, unless the never-list names the path) or a **byte-identical copy of
  the same path in the repository's main checkout** (this applies to the
  never-list too: nothing is lost). A copy that differs, or exists only in the
  worktree, holds, and the hold names the main checkout. A main-checkout path
  that is a symlink back into the worktree or out of the main checkout is not a
  copy. A symlink inside the worktree is that worktree's own entry: it is never
  followed, and its target is never touched;
- no hidden edits: no `--skip-worktree` / `--assume-unchanged` entries
  (`hidden-changes`), which status and `git worktree remove` cannot see;
- no nested repository: no `.git` at any depth but the worktree's own
  (`nested-repository`), since it would be deleted with its commits. The one
  exception is a clone a tool makes again: a git-ignored repository that is a
  direct child of `<worktree>/.tools/pre-commit/` (pre-commit's hook clones).
  Any other repository, including one inside `tmp/`, still holds;
- no orphaned commits: every commit in the worktree's reflog, per-worktree refs
  (`refs/worktree/*`, `refs/bisect/*`) and detached HEAD is reachable from a
  branch, remote or tag (`unreachable-commits`), because `git worktree remove`
  discards the reflog and gc then deletes them;
- pushed: HEAD is reachable from a remote-tracking ref, or is an ancestor of
  `origin/HEAD` (else `origin/main`). A squash-merged branch whose remote branch
  was deleted is therefore held, deliberately.

Each removal first prints `removing worktree <canonical path> (repository …,
HEAD …)`, then goes through a quarantine move: the worktree is `git worktree move`d to
a `.forge-reclaim-<name>-<ts>` sibling (a path-based writer then gets ENOENT),
fully re-classified there with a fresh in-use check, and only then removed. If
anything changed it is moved back and kept. If the move back fails it stays in
quarantine, is never deleted, and `forge storage status` lists it as
`quarantined`; move it back with `git worktree move`.

Removal is `git worktree remove` without `--force`; if git refuses the tree is
kept and reported. Branches are never deleted, and `git worktree prune
--expire 24h` drops only registrations whose directory is gone and whose admin
index has been untouched for a day (a worktree on a briefly unmounted volume is
not pruned while in use).

**Only an explicit `forge storage gc --apply` removes worktrees**, and only when
the policy sets `"worktree_reap": true`. Every pass nobody invoked — the
background auto-gc `forge env up` starts, the hourly and daily installed jobs
(the daily one runs `gc --apply --scheduled`), and `forge storage daemon` —
never removes one: an agent between two commands holds no process in its
worktree and may not write to it for hours, so to an automatic pass it looks
exactly like an abandoned one. The auto-gc pass has no worktree layer at all;
the scheduled full pass previews. Without `lsof` (common in containers) the
in-use check is unavailable and nothing is removed. `forge storage status`
lists held worktrees by reason (locked, recently active, in use, dirty,
unrecognized ignored data, unpushed, not a linked worktree, managed by
reliant) so a person can act on what the reaper will not touch. The explicit
per-repository command runs the same classifier and needs no policy:

```sh
forge storage worktrees --repo /path/to/repo            # preview
forge storage worktrees --repo /path/to/repo --apply
forge storage worktrees --repo /path/to/repo --apply --reliant-managed   # include ~/.reliant/worktrees
```

## Recovery and rollout

Before forge stops a registry for GC, it records a marker beside the policy
(`registry-maintenance/<container>.stopped`). After the pass, forge restarts the
registry and checks that it is running and answering, retrying for up to two
minutes. If it still is not serving, the pass fails and names the registry. A
pass that was killed partway leaves the marker, and the next `forge storage gc`
(a preview is enough) restarts the registry. A registry that someone stopped by
hand has no marker, so forge leaves it alone.

A hard kill or Docker outage can leave a `-forge-retention`/`-forge-gc` container.
The registry is never restarted while one is still running; never run filesystem
GC concurrently with a registry writer. Normal cancellation gets a separate
cleanup timeout. A stale helper blocks the next pass instead of overlapping GC.
Maintenance containers mount the registry's data volume by name, so removing one
can never remove the volume.

Disable any older ad hoc registry-retention launch agent before installing the
Forge job, and migrate its repository/context allowlist and pins. Two independent
maintenance programs do not share Forge's lock. Persistent workspace, database,
and unmerged worktree cleanup requires its own explicit teardown decision.
