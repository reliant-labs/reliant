# Proposal: self-upgrading long-lived binaries (`forge/pkg/selfupdate`)

**Status:** approved by the owner 2026-11-19. F1 (`forge/pkg/selfupdate` core)
is implemented, and so is F2's verification half (the Sigstore verifier and the
channel-manifest source); where their API differs from the sketch in §3, §10
and §11 say how and why. F2's Windows swap, F3 and the reliant phases have not
landed.
**Date:** 2026-11-19
**Scope:** forge (new package `pkg/selfupdate`, opt-in hook in `pkg/serverkit`),
reliant (first consumer, release pipeline), control-plane (signal source only).
**Extends:** "Forced daemon updates: a minimum version, rolled out in waves"
(Oct 2026, "the earlier design"). It is not in any repository; the parts that
shipped are documented in control-plane `docs/daemon-floor.md`. Its owner
decisions (2026-10-24) stand: the policy is release KCL plus a `DaemonRollout`
CR (D2); every prod release carries a `normal` floor with a 7-day deadline
(D3); a cloud machine rolls at a quiet point of 10 minutes with no tool call in
flight, and background processes do not block it (D4); a 10-minute drain
timeout (D5); user-owned machines are never blocked (D6); Electron
auto-downloads and prompts, and self-run auto-update is opt-in (D7); a halted
roll resumes only by a person or a new release (D8); the floor includes a
minimum forge version (D10). This proposal changes none of them, except for one
reversal it asks the owner to confirm (§3, §9).

## Summary

The owner asked (2026-11-19) for long-lived daemons that upgrade or replace
themselves, possibly on a signal from the control plane. Today nothing in
forge, reliant or control-plane does this, and there is no server-to-daemon
upgrade message. The earlier design covers the control-plane half (a version
floor, waves, a drain protocol, a fleet roller for cloud pods). It deliberately
left the daemon half to `reliant update` and deferred hot-swap. This proposal
fills that half, and makes it a forge primitive instead of reliant code.

Recommendation:

1. **Add `forge/pkg/selfupdate`**, generic for any forge-built long-lived
   binary: find a release, verify it, stage it, refuse to touch installs that
   something else owns, drain, swap, and revert if the new binary does not
   confirm itself. Policy (when to upgrade, how urgent) stays in the app.
2. **Verify with the scheme forge's `install.sh` already uses**: a keyless
   Sigstore attestation pinned to the release workflow's identity and the tag.
   Unsigned artifacts are refused unless the app says otherwise in so many words.
3. **Never overwrite an install something else owns.** The Electron bundle,
   Homebrew, dpkg/rpm/pacman, managed pods and unwritable paths are detected
   (a build-time `ManagedBy` stamp is authoritative) and the primitive
   returns "delegate to the owner" instead of acting.
4. **Control plane decides, the owner of each runtime acts.** Cloud pods: the
   image is the version and the FleetRoller (earlier design D1) rolls the pod,
   unchanged. Electron: it runs its own updater when told. Self-run CLI: the
   primitive, opt-in per D7.
5. **One new backward-compatible signal**: the earlier design's
   `ServerMessage.restart_requested` (its phase B2), carrying target, urgency and
   deadline, plus daemon-reported upgrade state.
6. **Reverse the earlier design's deferral of hot-swap, for user-owned self-run
   daemons only.** Cloud pods never exec in place. Reason: §3, "The reversal".
7. **Prerequisite, and the real critical path: reliant publishes no standalone,
   signed CLI artifact today** (§6). Nothing can self-replace until it does.
8. **Background processes survive an exec only after a later reliant change**
   (output to files, not daemon-held pipes). Until then an upgrade exit kills
   them as a stop does, and the drain waits for them or the policy skips the
   upgrade (§5).

---

## 1. What exists

The earlier design's §1 describes the daemon's version identity, the cloud
rollout paths and the restart semantics; it is not repeated here. What matters
for this proposal:

- **Shipped on main** (reliant, control-plane): version, commit,
  `commit_time_unix` and `forge_version` on the wire and in `DaemonInfo`
  (phases A1-A2, `proto/reliant/v1/tools_daemon.proto` `DaemonRegister` 13-16,
  `daemon_registry.proto:476-481`); the Workspace CR's `Status.DaemonVersion`
  (A3, `internal/operators/workspace/daemon_build.go`); the fsynced result
  outbox (B1, `internal/toolexec/daemonruntime/result_outbox_store.go`); the
  floor policy plumbing and `DaemonRollout` CR (C1, `docs/daemon-floor.md`).
- **Not built:** the drain protocol and `RestartRequested` (B2), the user-owned
  signal (C2), the FleetRoller (D1), the health gate (D2), Electron's check (E1)
  and `reliant update` (E2). Reliant does not yet read `DAEMON_FLOOR_*`.
- **No self-update anywhere.** No code in the three repos downloads and swaps a
  binary. The one in-place exec precedent is forge's own delegation:
  `internal/forgedelegate/exec_unix.go:17-23` (`syscall.Exec`, same argv and
  env), with Windows running a child and relaying its exit
  (`exec_windows.go`).
- **Signing precedent.** forge signs release archives and `checksums.txt` with a
  keyless Sigstore attestation (`release-attest.yml`, predicate
  `https://github.com/reliant-labs/forge/attestations/release-assets/v1`), and
  `install.sh:92-119` verifies it with `gh attestation verify --bundle` or
  `cosign verify-blob-attestation`, pinned to the workflow identity and the
  release tag (`docs/releasing.md`, "Signed release assets"; the constants are
  held equal by `internal/installers/signature_test.go`).
- **Daemon shutdown today** (reliant `cmd/reliant/cmd/daemon.go`): SIGINT/SIGTERM
  cancel the context and arm a 10s force-exit (`daemonShutdownGrace`, `:759`,
  `watchShutdownSignals :780`); a deferred `KillAllRunning()` kills background
  processes (`:985`); `daemonruntime.Start` clears the daemonstate record on
  exit (`runtime.go:~406`). A drain longer than 10s, or an exit that keeps
  children, needs all three changed (§4).

## 2. Goals and non-goals

Goals: a forge-built long-lived binary can opt in to upgrading itself from a
signed release without a package manager; it never damages an install it does
not own; a bad new binary reverts itself; the app keeps full control of when.

Non-goals: a policy engine (urgency, deadlines, waves and floors come from the
app and, for Reliant, the control plane); updating cloud pods (the image is the
unit, per the earlier design §3); blocking user-owned machines (D6, never);
updating forge-the-CLI (it delegates through the `forge.yaml` pin and is not
long-lived).

## 3. The forge primitive: `forge/pkg/selfupdate`

### The reversal (for the owner to confirm)

The earlier design (§3, "Hot-swap") deferred `syscall.Exec` with state handoff,
for two reasons: it cannot update a cloud pod (the image is the unit), and every
piece of in-memory state would have to survive a version change. This proposal
re-opens hot-swap **only for user-owned self-run daemons**, as a forge
primitive, because the owner asked on 2026-11-19 for long-lived daemons that
replace themselves. The earlier design's own revisit clause ("if disruption on
desktop or self-run becomes the complaint, revisit just for the binary") covers
that scope. Both original objections are answered by construction:

- Cloud pods keep the image and the FleetRoller. `selfupdate` refuses managed
  installs, so a pod cannot reach the swap path even if opted in by mistake.
- There is **no in-memory handoff**. The sequence is drain, tear down fully,
  _then_ exec, so the new image starts from durable state only (the outbox and
  background journal already are; §5 covers the rest). Exec is a restart that
  keeps the pid, not a live migration.

One correction to the earlier design: it assumed exec keeps children and PTYs.
In practice Go marks every descriptor close-on-exec, so output pipes and PTY
masters close and children die of EPIPE or SIGHUP (§5).

### Shape of the package

Two phases, because `syscall.Exec` skips deferred functions: **prepare** (any
time, no disruption) and **apply** (drain, tear down, then exec as the last act
of `main`).

```go
package selfupdate

// What is running, in the same ordering the release carries.
type Build struct{ Version string; CommitTime time.Time }

type Release struct {
    Version, Tag string
    CommitTime   time.Time
    Channel      string
    Assets       []Asset // os/arch archives + checksums.txt
    Bundle       Asset   // Sigstore attestation bundle
}

// Source answers "what is the target release for this channel".
type Source interface {
    Latest(ctx context.Context, channel string) (Release, error)
}
func ManifestSource(url string) Source        // signed channel manifest (§6)
func GitHubSource(repo string) Source         // releases API
func LedgerSource(ref release.Ref) Source     // forge release ledger

// Verifier checks the downloaded files against the release's attestation.
type Verifier interface {
    Verify(ctx context.Context, rel Release, files map[string]string) error
}
// sigstore subpackage: keyless, no external tool needed on the user's machine.
type SigstoreConfig struct {
    Repo          string         // "reliant-labs/reliant"
    WorkflowID    *regexp.Regexp // release-attest.yml at a release tag
    Issuer        string
    PredicateType string
    // The signed predicate {repository, release, commit} must name Repo and
    // the tag being installed: an older signed set replayed under a newer tag
    // is refused, exactly as install.sh does.
}

type Policy interface { // the only policy in the primitive
    Decide(ctx context.Context, cur Build, rel Release) Decision
}
type Decision struct {
    Action   Action // Skip | Stage | Apply
    Urgency  Urgency
    Deadline time.Time
}

// Drainer is the app's: stop new work, finish in-flight until the deadline.
type Drainer interface {
    Drain(ctx context.Context, deadline time.Time) (DrainReport, error)
}

type Config struct {
    Binary    string // name, for state paths and logs
    Current   Build
    Source    Source
    Verifier  Verifier   // nil is an error unless AllowUnsigned is set
    Policy    Policy     // default: Stage only, never Apply
    Drainer   Drainer
    Shutdown  func()     // asks the app to tear down (cancel its root context)
    Probe     []string   // staged-binary self-test argv, default {"version"}
    StateDir  string     // machine-level, default ~/.<binary>/upgrade
    ConfirmWithin time.Duration // default 60s
    SwapMode  SwapMode   // ExecInPlace (default, unix) | ExitForSupervisor
    AllowUnsigned bool   // explicit, logged at every start
}

func New(cfg Config) (*Updater, error)
func (u *Updater) Check(ctx context.Context) (Release, Decision, error)
func (u *Updater) Prepare(ctx context.Context, rel Release) (Staged, error)
func (u *Updater) Request(ctx context.Context, s Staged, d Decision) error
func ExecPending() error // call after the app's teardown; returns only on failure
func Boot() Pending      // first line of main: pending-swap + revert watchdog
func (p Pending) Confirm() error
```

### Stage

`Prepare` downloads the archive and `checksums.txt` into
`StateDir/staged/<version>/`, verifies the Sigstore bundle, checks the archive's
sha256 against the _verified_ `checksums.txt` (never one fetched unsigned from
the same place), extracts the binary, fsyncs and renames it into place. The
state directory is machine-level because several instances can share one
binary (reliant's data directories are per instance,
`~/.reliant/instances/<origin>/<sub>/<workspace>/`), so a file lock in
`StateDir` serialises staging and swapping across instances. Before the swap
the running binary is copied (or hard-linked) to `StateDir/previous/<binary>`.
The **probe** runs the staged binary with a short timeout (`version`) so a
wrong-architecture, missing-library or truncated binary is caught while the old
process is still the one serving.

### Owner and install kind

`Detect()` resolves the executable (`os.Executable` plus `EvalSymlinks`, as
reliant's `daemonstate.binaryIdentity` already does,
`internal/toolexec/daemonstate/state.go:399-414`) and returns an `Install` with
a kind and, for anything but `SelfOwned`, who owns it and the command to run:

| Kind            | Signal                                                                                                                      | Result                                                                |
| --------------- | --------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------- |
| Stamped         | build-time `-X …selfupdate.ManagedBy=<owner>`, or `<binary>.managed-by` beside it, or `FORGE_MANAGED_BY` in the environment | refuse, delegate. **Authoritative**: packagers and supervisors set it |
| Bundle          | path inside an app bundle or AppImage (`*.app/Contents/Resources/…`, `resources/server/`, `/tmp/.mount_*`)                  | refuse, delegate to the app's updater                                 |
| Package manager | `/Cellar/`, `/Caskroom/`, `/home/linuxbrew/`; `/usr/bin`/`/usr/lib` confirmed by `dpkg -S`/`rpm -qf`/`pacman -Qo`           | refuse; hint `brew upgrade …`                                         |
| Managed         | the app's own predicate (Reliant: `host.Managed()`, a cloud pod)                                                            | refuse                                                                |
| Read-only       | the directory is not writable by this user (rename needs the directory, not just the file)                                  | refuse                                                                |
| SelfOwned       | everything else (`~/.local/bin`, a tarball, `install.sh`)                                                                   | may self-replace                                                      |

Heuristics only ever refuse; they never grant. An explicit stamp refuses even
where the path looks owned. There is no override for the bundle and managed
kinds: the 20% who need one build their own binary without the updater.

### Swap

- **Unix.** Copy the verified binary next to the running one
  (`.<name>.new-<pid>`, same filesystem, fsync), rename over it (atomic; the
  running image keeps its old inode), write `state.json`, and
  `syscall.Exec(path, os.Args, env)` as `forgedelegate.Exec` does, from
  `ExecPending()` after the app has torn down. The same pid means
  systemd, launchd and Electron see no restart. With `ExitForSupervisor`, it
  exits with a documented code and lets the supervisor restart it instead (for
  hosts where the supervisor expects that, and for the unsupervised-crash case
  below).
- **Windows.** A running `.exe` can be renamed but not replaced: rename it to
  `<name>.exe.old`, move the new one in, start the new process detached, exit;
  the new process deletes `.old` on its next start. A new pid, children
  orphaned. Best-effort and unsupervised installs only (Electron owns Windows
  desktops anyway).

### Confirm and revert

`Boot()` runs first in `main`. When it sees `state.json` in phase `swapped` it
starts a watchdog (`ConfirmWithin`). The app calls `Pending.Confirm()` after its
own health check (for the daemon: registered with the gateway and an
authenticated stream). If the watchdog fires, or `Boot()` finds the pending
marker with attempts already at 2 (the process crashed before confirming), it
swaps `previous/` back and execs it, and records the failed version so the
policy does not retry it until a newer release appears. That is the boot-loop
guard. One gap, stated plainly: a binary that dies _before_ `Boot()` runs, on an
install with no supervisor, leaves the daemon down. The probe step exists to
make that rare, and `ExitForSupervisor` is the answer for supervised hosts.

### Opt-in and `serverkit`

Default off; no scaffold changes until a consumer asks. A server built on
`pkg/serverkit` gets an optional field (`Server.SelfUpdate *selfupdate.Updater`)
that wires `Drainer` to serverkit's existing `Drainer`
(`pkg/serverkit/drain.go`: `Begin`, `ShutdownContext`) and calls `ExecPending`
after `Run`'s teardown returns. Daemons that do not use serverkit (Reliant's)
use the package directly. A `forge.yaml` per-binary `self_update` block
(`source`, `channel`, `verify`) is proposed for scaffolded binaries, still
default off.

## 4. Reliant as the first consumer

### Signal path

New backward-compatible fields; old daemons and old gateways ignore them:

```proto
// ServerMessage oneof (fields 1-21 are taken; GatewayDrain = 20, ToolResultAck = 21)
RestartRequested restart_requested = 22;

message RestartRequested {
  string target_version = 1;  string target_commit = 2;
  int64  commit_time_unix = 3;
  Urgency urgency = 4;        // routine | normal | security | emergency
  int64  deadline_unix_ms = 5;
  string rollout_id = 6;      string reason = 7;
}
```

The daemon reports its state on the heartbeat (`CURRENT`, `UPDATING`,
`DRAINED`, `AWAITING_OWNER`, `FAILED`) and a capability bool in
`DaemonRegister` (like `moves_on_drain = 12`) so the gateway knows whether to
expect the handshake. The gateway computes urgency from the floor it already
receives as `DAEMON_FLOOR_*` config (C1; no new transport), and the target from
the channel manifest (§6). A daemon with no control plane still learns "update
available" from the manifest alone: the signal is a nudge, never the
authority, because the daemon verifies the release itself.

### Who acts, by runtime

| Runtime          | Detected by                                                                          | Action on `restart_requested`                                                                                                                                                                                                                                                                                                         |
| ---------------- | ------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Cloud pod        | `host.Managed()`                                                                     | Drain, report `DRAINED`, wait. The FleetRoller rolls the pod (D1, unchanged). No download, no exec                                                                                                                                                                                                                                    |
| Electron         | `RELIANT_SUPERVISOR=electron`, set by `backend-manager.js` when it spawns the daemon | Print `@@RELIANT_UPGRADE {json}` on stdout, mirror it into daemonstate, report `AWAITING_OWNER`. Electron runs `runBackgroundUpdateCheck("daemon")` (`electron/src/main.js:101-160`) and, for `required`, auto-downloads and prompts (D7). The daemon is stopped by Electron's normal path. Never overwrites inside the signed bundle |
| Self-run CLI     | `Detect()` is `SelfOwned`                                                            | The primitive, **opt-in** (`--auto-update` or `reliant update`, D7)                                                                                                                                                                                                                                                                   |
| Brew/deb/rpm/AUR | `Detect()`                                                                           | Surface the owner's command in the UI banner (C2); do nothing                                                                                                                                                                                                                                                                         |

Electron's only channels from the daemon are stdout and the daemonstate file
(`backend-manager.js:365-433`, `:466`, `:596-616`); an old Electron logs an
unknown line as `[Daemon]:` (`backend-manager.js:958`) and ignores it, so the
notice is backward compatible.

### Drain (reliant side)

This is the earlier design's §2.4 made concrete, with the three differences an
upgrade exit needs from a stop:

1. **Admission gate.** New: nothing refuses requests today
   (`registerCancel`/`requestDone`, `runtime.go:1679-1692`). While draining,
   new tool requests are refused with the _undelivered_ class, so senders
   re-send safely (earlier design §1.3).
2. **Suspend the 10s watchdog** during the drain (`daemon.go:780-799`); a drain
   is bounded by its own deadline.
3. **An upgrade exit differs from a stop:** it skips `KillAllRunning`
   (`daemon.go:985`) once background survival exists (§5), and writes
   `upgrading` to daemonstate instead of clearing it (`runtime.go:406`), since
   Electron polls that file every 250ms and would see the daemon vanish.

Results need no flush: a finished result is fsynced to the outbox before it is
sent, and a call interrupted by the exec becomes "interrupted by daemon restart"
at the next start (`result_outbox_store.go`). Past the drain deadline, the
daemon backgrounds long shell calls with the existing server-initiated
`ToolExecutionBackground` (`tools_daemon.proto:918-925`) once §5 lands, and
otherwise follows the deadline rule in §9.

## 5. Background processes across exec (a later phase)

Today they do not survive, whatever the earlier design assumed. Each is spawned
by `StartProcess` (`internal/llm/tools/shell/background_manager.go:410-530`)
with in-process `outputLineWriter` pipes, so the pipe read ends close at exec
and the child takes EPIPE on its next write. Terminal sessions (go-pty, Setsid +
Setctty) get SIGHUP when the master closes. Only the background journal at
`<dataDir>/background-processes/<id>.json` and `.out` is on disk, and a record
still marked `running` is demoted to `stale` on restore
(`background_journal.go`).

The plan, in order:

1. **Children write to files, not pipes** (`<id>.stdout`, `<id>.stderr` in the
   journal directory) and the daemon tails them. This survives exec _and_ a
   daemon crash, which passing pipe fds with close-on-exec cleared does not.
2. **The journal records pid and process start time** (`procledger` already
   keeps start ticks).
3. **On restore, re-adopt** a `running` record whose pid is alive, whose parent is
   this process and whose start time matches, via `AdoptRunningProcess`
   (`background_manager.go:1513`) with a `wait4` goroutine; rebind the cgroup by
   path; subscribers resume from `GetCombinedOutputWithSeq(afterSeq)`.
4. **PTYs die in v1**; the UI reopens them. Passing masters as inherited fds is
   possible on Unix later.
5. **Sandboxed commands die with the daemon.** The credsandbox init sets
   `PR_SET_PDEATHSIG` by owner decision (2026-11-10,
   `credsandbox_linux.go:195-198`) and watches a liveness pipe, so an exec kills
   them. This stays as decided.
6. **Windows:** no exec, new pid; adopt by pid with `OpenProcess` best-effort,
   else only upgrade when none are running (the earlier defaults).

Until step 3 ships, the policy prefers the quiet case: do not self-apply while
background processes run, unless the deadline forces it (§9). A pod roll still
kills them, as the earlier design's D4 already accepts.

## 6. Prerequisite: reliant publishes nothing to self-update from

Verified on `origin/main` of reliant:

- `.goreleaser.yml` defines archives (`reliant-{mac,linux,windows}-{x86_64,arm64}`
  plus `checksums.txt`, Homebrew, AUR, deb, rpm), but nothing in `.github`,
  `Taskfile.yml` or `scripts` invokes goreleaser.
- `release.yml` builds `reliant-backend[.exe]` with plain `go build` (~`:554`),
  bundles it only into the Electron installers on
  `downloads.reliantlabs.io`, and runs `gh release create` with **no assets**
  (`:1551`). Workflow permissions are `contents: write`, `packages: write`,
  `actions: read`: no `id-token` (`:29-32`).
- The `reliant` on PATH is a symlink to or copy of the bundled binary
  (`electron/src/cli-installer.js`), or a launcher from `scripts/install.sh`.

So there is no standalone CLI artifact, no checksum and no signature to
verify. The pieces to add:

1. Publish the CLI archives and `checksums.txt` to the GitHub release and to R2.
2. A job with `id-token: write` and `attestations: write` running
   `actions/attest` over them with a reliant predicate shaped like forge's
   (`{repository, release, commit}`), uploaded as `reliant-release.sigstore.json`.
   Same caveat forge documents: attesting archives built in the release job says
   "these are the assets the release published", and building them inside the
   signing workflow is what closes the rest.
3. A **public signed channel manifest**, `daemon-channels.json` on
   `downloads.reliantlabs.io`, written by the same tag workflow: per channel
   (`alpha`, `latest`) the version, `commit_time`, archive URLs, checksums and
   the bundle URL. It makes the target known without our control plane, so
   self-run daemons and standalone reliant servers work, and it is the "target"
   the control plane's floor (urgency) pairs with. Electron's `latest*.yml`
   feeds stay as they are.

This is the longest pole: phases F1-F3 below can be built and tested against a
forge release (which is already signed) while R0 lands.

## 7. Phased plan

Letters follow the earlier design's table; new phases are prefixed `F` (forge)
or `R` (reliant). Anything already shipped is omitted.

| #      | Repo               | PR                                                                                                                                                                                                                                     | Depends on       |
| ------ | ------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------- |
| F1     | forge              | `pkg/selfupdate` core: `Source`, `Stage` (lock, checksum, probe, `.prev`), `Detect`, unix `Swap`/`ExecPending`, `Boot`/`Confirm`/revert and boot-loop guard; tested with a fake release server and a re-exec test binary. No consumers | –                |
| F2     | forge              | `selfupdate/sigstore` verifier (keyless, in process), conformance test pinning identity, issuer, predicate and replay rule equal to `install.sh` (extend `signature_test.go`); Windows swap                                            | F1               |
| F3     | forge              | `serverkit` opt-in hook, `forge.yaml` `self_update` block for scaffolded binaries, a skill page                                                                                                                                        | F1               |
| R0     | reliant            | **Prerequisite (§6):** CLI archives + `checksums.txt`, `actions/attest`, public `daemon-channels.json`                                                                                                                                 | –                |
| B2     | reliant            | Drain protocol: `RestartRequested` (field 22), `UPDATING`/`DRAINED` state, admission gate, watchdog suspend, daemonstate `upgrading`. Gateway sends it from the floor config (earlier design)                                          | A2 (shipped)     |
| C2     | reliant            | `update_state` on `DaemonInfo`; web and desktop banner, including "update with brew/dpkg" hints from `Detect()`                                                                                                                        | B2, C1 (shipped) |
| E1     | reliant (electron) | `@@RELIANT_UPGRADE` handling; periodic check; auto-download below the floor, prompt                                                                                                                                                    | B2               |
| D1, D2 | control-plane      | FleetRoller and health gate, **unchanged** from the earlier design                                                                                                                                                                     | B2, C1           |
| E2     | reliant            | **Becomes: adopt `forge/pkg/selfupdate`.** `reliant update`, `--auto-update` (opt-in, D7), drain integration, `ManagedBy` stamps for brew/AUR/deb/rpm builds. Gateway blocking stays out of scope (D6)                                 | F1, F2, R0, B2   |
| R5     | reliant            | Background-process survival: file output, journal pid + start time, re-adopt on restore (§5). Then a self-apply no longer waits for them                                                                                               | E2               |

Parallelism: F1-F3, R0 and B2 are independent and can proceed at once; E2 is the
join. R5 is deliberately last; E2 ships without it under the quiet-case policy.

## 8. Alternatives and devil's advocate

**Use a library** (`minio/selfupdate`, `inconshreveable/go-update`,
`creativeprojects/go-selfupdate`). They cover download and atomic replace, and
some add checksums or key-based signature checks. As far as I know none does
keyless Sigstore identity pinning, a drain hook, exec-in-place or
confirm-and-revert, but I did not re-check their current feature sets for this
proposal. The replace step itself is about thirty lines; the value is in the
parts around it. Worth re-evaluating when F1 is built, as a possible inner
dependency.

**Package managers only.** Right for Homebrew, deb, rpm and AUR, and the
primitive delegates to them. It does not reach `install.sh` or tarball users,
the Electron-less CLI population this is for.

**A supervisor sidecar** (a small updater service under systemd or launchd, as
Docker Desktop and Tailscale ship helpers). It solves the died-before-`Boot()`
gap cleanly and needs no exec. It costs a privileged component to install and
maintain per OS, and a second signing and distribution surface. `ExitForSupervisor`
leaves room to adopt it later on managed hosts.

**Exit and let the supervisor restart, instead of exec.** Simpler, and it is
what `ExitForSupervisor` does. Exec is kept as the default only for unsupervised
daemons and because the pid survives for children the new image can `wait4`;
after a supervisor restart, surviving children are orphans of init and can only
be polled by pid.

**Against this proposal itself.**

- _It is a remote-code-replacement mechanism in a process that holds the user's
  credentials._ The exposure is the release workflow's identity: whoever can
  make that workflow attest a malicious asset gets code on every opted-in
  machine. Mitigations: identity and tag pinning, the replay rule, refusing
  unsigned by default, opt-in, and `Policy` defaulting to stage-only. Electron's
  updater carries the same class of risk today, over a feed with weaker
  guarantees.
- _Self-run CLI daemons may be few._ The population is unmeasured. Mitigation:
  the primitive is generic and small (F1-F3), the Reliant adoption (E2) is the
  only phase that costs reliant effort, and the earlier design's A3 fleet
  panel can size it first.
- _Exec-in-place is a sharper tool than a restart._ Close-on-exec, held locks and
  inherited signal masks all cross it. The drain-then-teardown-then-exec order
  and a test that re-execs a real binary in F1 keep it a restart-with-same-pid.
- _The revert watchdog cannot cover a binary that never reaches `Boot()`._ True
  and stated in §3; the probe and `ExitForSupervisor` are the mitigations.
- _Two upgrade paths per daemon type confuse operators._ `Detect()` makes the
  choice mechanical and the UI states which owner will act.

## 9. Open decisions for the owner

The owner said to use the recommendations, so each is a default being
proceeded on, listed so it can be overturned.

1. **Hot-swap for user-owned self-run daemons** (reverses the earlier design's
   deferral, §3). _Recommended default: yes, as the forge primitive, for
   self-run only; cloud pods keep the image and the FleetRoller (proceeding on
   this)._
2. **Drain deadline before backgrounding or interrupting a long call.** Shell
   calls run for minutes and `shell_wait` can block up to 20 minutes. _Recommended
   default: 5 minutes for a self-apply (cloud keeps D5's 10). At the deadline:
   background long shell calls once §5 lands; before then, for `routine` and
   `normal` urgency abort the upgrade and retry at the next quiet window, and
   interrupt (clear lost-request error) only at a `security` or `emergency`
   deadline (proceeding on this)._ It never blocks the user, consistent with D6.
3. **Where the public channel manifest lives.** _Recommended default:
   `daemon-channels.json` beside the CLI archives on `downloads.reliantlabs.io`,
   signed by the same attestation (proceeding on this)._
4. **Package name.** _Recommended default: `forge/pkg/selfupdate` (proceeding on
   this)._

## 10. F1 implementation notes

What `pkg/selfupdate` ended up as, where it differs from the §3 sketch. The
design is unchanged; these are the API details building it settled.

- **`Boot(BootOptions{Binary, StateDir})`, not `Boot()`.** Boot runs before the
  app builds its `Config`, so it needs to be told where `state.json` lives.
  `ConfirmWithin` is written into `state.json` at the swap, so the location is
  all it needs.
- **`(*Updater).ExecPending()`, not a package function.** The armed swap is
  in-process state that `Request` hands to `ExecPending`; a package-level
  global would be shared mutable state.
- **`Detect(DetectOptions{Path, Managed})`.** The managed predicate (Reliant's
  `host.Managed()`) is the app's, so it is passed in; `Config.Managed` feeds it.
- **The build stamp is `-X github.com/reliant-labs/forge/pkg/selfupdate.managedBy=<owner>`**
  (unexported). forge's `exportedvars` contract rule rejects a mutable exported
  variable, and the linker flag works on an unexported one: the flag is the
  contract, and no Go code can change it.
- **`InsecureAllowUnsigned`, not `AllowUnsigned`.** Named loudly; `New` refuses
  a `Config` with neither a `Verifier` nor it (`ErrNoVerifier`), refuses both
  together, and logs the opt-out at every start. The sha256 check against
  `checksums.txt` still runs without a verifier.
- **`Config` grew what the sketch left implicit:** `Channel` (passed to
  `Source.Latest`), `ArchiveName`/`BinaryName`/`ChecksumsName` (reliant's
  goreleaser names its archives `reliant-<os>-<arch>`, not forge's
  `<binary>_<os>_<arch>.tar.gz`, the default), `DrainTimeout` (default 5 min,
  §9.2), `ProbeTimeout`, `HTTPClient` (https only, redirects included),
  `MaxDownloadBytes`, `Managed` and `Logger`.
- **Sources: `GitHubSource` and `SourceFunc` only.** `ManifestSource` waits for
  R0, which defines `daemon-channels.json` (§6.3), and F2, which verifies its
  signature: shipping a manifest schema first would freeze R0's format inside
  forge's semver. `LedgerSource(release.Ref)` is dropped: `pkg/release` has no
  `Ref`, and its ledger records deploy releases, not CLI archives.
- **Ordering.** The GitHub releases API carries no commit time, so a release is
  newer by semver first and by commit time only when the versions are equal or
  not semver. A build with neither (`dev`) is never upgraded. The `Policy` is
  asked only about a release that is newer and has not failed here.
- **The boot-loop guard counts dead boots, not starts.** Several instances can
  share one binary, so "attempts at 2" would revert a healthy binary when two
  instances restart inside one confirm window. `state.json` records the pid of
  each unconfirmed start; the next start reverts when two of them are no longer
  alive. A confirmation by any instance counts, and a watchdog that fires after
  another instance confirmed does not revert.
- **Rollback images are copies under new names, never overwritten.** Each
  swap copies the running binary to `StateDir/previous/<binary>.<version>.<ns>`
  (a copy, not the §3 hard link: a link shares the install's inode, so a `cp`
  over the install would rewrite the backup too). Older images are pruned only
  after the new swap is recorded in `state.json`, an unconfirmed earlier
  swap's image is kept (`retained`) until a later swap confirms, and a
  confirmation leaves only its own rollback image.
- **`GitHubSource` sends its `Token` only to the API's origin.** `APIBaseURL`
  must be https with no userinfo, query or fragment; `Repo` must be exactly
  `owner/name`; the token is stripped from any redirect that leaves the API
  origin, and asset downloads never carry it.
- **`ExitForSupervisor` exits 75** (`EX_TEMPFAIL`, `ExitCodeRestart`).
- **A `Drainer` error declines the upgrade** with nothing swapped: that is how
  an app aborts a routine upgrade whose work did not drain (§9.2).
- **exec of a just-written binary retries `ETXTBSY`** (golang/go#22315: a child
  forked elsewhere in the process can briefly hold the file open).
- **Windows** stages and probes; `Request` and `ExecPending` return
  `ErrUnsupported` until F2.
- **Environment.** `pkg/` forbids reading the environment
  (`pkg/.golangci.yml`); `FORGE_MANAGED_BY` (a supervisor-to-binary contract)
  and the `os.Environ()` handed to exec are exempted in one file,
  `selfupdate/env.go`.

## 11. F2 implementation notes (verification half)

- **`pkg/selfupdate/sigstore`**: `New(Config{Repo, Identity, Issuer,
PredicateType, TrustedRoot, TUFCachePath})` returns a `selfupdate.Verifier`
  built on `github.com/sigstore/sigstore-go` (in process; no cosign or gh on the
  machine). It requires a Fulcio certificate chaining to the trusted root, one
  transparency-log entry, one certificate timestamp and one observer timestamp,
  the certificate identity (`Identity` must be an anchored `^…$` regexp; an
  unanchored one is refused) and issuer (default GitHub Actions), the predicate
  type, **every** downloaded file but the bundle among the statement's sha256
  subjects, and the predicate's `repository == Repo` and `release == rel.Tag`
  (the replay rule). A release with no bundle is refused.
- **Trusted root.** Nil `TrustedRoot` fetches the public-good root through TUF
  at each verification (keys rotate; an upgrade check is rare, so the fetch is
  cheap), cached on disk only under `TUFCachePath`. A pinned root is for tests
  and air-gapped machines. The package's test verifies forge v1.8.0's real
  signed `checksums.txt` against a pinned snapshot of the public-good root, and
  that each rule above refuses.
- **`ManifestSource{URL, Repository, Client}`** requires the manifest's repository to match the caller's expected `owner/repo` and reads schema 1 of the channel manifest,
  the format reliant's R0 publishes as `daemon-channels.json`
  (`dev-docs/cli-release-artifacts.md`): one channel's version, tag, commit
  time, `checksums`, `bundle` and per-`goos/goarch` archives, each named by the
  last element of its https URL, on an allowed origin (`AllowedOrigins`, default the
  manifest's own; a redirect of the manifest off its origin is refused). The
  manifest's own signature is not checked:
  it only names the target. The files are verified against their release's
  bundle (a manifest pointing at another release's files fails the replay
  rule), and `Updater.Check` never moves backwards, so a held-back manifest can
  delay an upgrade but never install anything. An unknown schema is refused.
- **Windows swap** is still `ErrUnsupported`.
