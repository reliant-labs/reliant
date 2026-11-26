---
name: selfupdate
description: Opt a long-lived binary your users run (agent, daemon, self-hosted server) in to replacing itself with a newer signed release — forge/pkg/selfupdate/autoupdate, off by default; serverkit's Server.SelfUpdate, a standalone binary's Serve/ExecPending, the Settings and where they come from, Sigstore signing, the unsigned escape hatch, and the installs it refuses (package managers, app bundles, pods, containers).
---

# Self-updating binaries

A binary your users run on their own machines — a daemon, an agent, a
self-hosted server — can replace itself with a newer signed release, without a
package manager: download, verify the signature, stage and probe the new
binary, drain, swap it in, exec it with the same pid, and revert it if it does
not come up healthy. `forge/pkg/selfupdate` is the mechanism;
`forge/pkg/selfupdate/autoupdate` is the opt-in wiring. **It is off by
default**, and nothing forge scaffolds turns it on.

It is NOT for workloads forge deploys. A pod, a compose container or a hosted
workload is upgraded by rolling its image (`forge env deploy`), so the
updater refuses to swap there: it logs `selfupdate: off: something else owns
this install` with the reason and does nothing.

## Turn it on in a serverkit server

```go
import "github.com/reliant-labs/forge/pkg/selfupdate/autoupdate"

up, err := autoupdate.New(autoupdate.Options{
    Binary:  "acme",
    Current: selfupdate.Build{Version: version, CommitTime: commitTime}, // the build's stamp
    Logger:  logger,
    Settings: autoupdate.Settings{
        Enabled:          cfg.GetSelfUpdate().GetEnabled(), // the user's opt-in
        Channel:          cfg.GetSelfUpdate().GetChannel(),
        Interval:         cfg.GetSelfUpdate().GetInterval().AsDuration(),
        Source:           "acme-labs/acme",                 // GitHub releases, or an https manifest URL
        SignerWorkflow:   "release.yml",                    // the workflow that signs them
    },
})
if err != nil {
    return err
}
srv.SelfUpdate = up // serverkit.Server
return serverkit.Run(ctx, skCfg, srv)
```

Call `autoupdate.New` as early as the settings are known, before slow
composition (database connects): it runs `selfupdate.Boot`, which starts the
revert watchdog of a binary that was just swapped in.

serverkit then:

1. starts the updater once the listener is bound;
2. confirms a just-swapped binary only when its readiness passes — the same
   verdict `/readyz` gives (listener bound, not draining, every `ReadyCheck`
   passing). A build that never gets ready is reverted by the watchdog
   (default 60s), and a build that crashes twice before confirming is
   reverted on the next start;
3. checks for a newer release two minutes after start, then every interval
   (default 6h, jittered by a tenth);
4. applies one through its own graceful shutdown — readiness flips to
   draining, in-flight requests finish, workers stop, `OnShutdown` runs,
   telemetry flushes — exactly as a rollout drains;
5. calls `ExecPending` as `Run`'s last act. On success the process is the new
   binary. If the exec fails the old binary is put back and `Run` returns the
   error, so the process exits non-zero for its supervisor.

A Settings with `Enabled: false` still confirms a binary an earlier run
swapped in, so turning self-update off never strands a pending revert.

## Turn it on in any other binary

A `forge scaffold binary` (or any `main`) runs the updater beside its loop and
execs after its own teardown:

```go
ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
defer stop()
// ready is the binary's own health check (connected, serving); nil confirms
// a swapped-in binary as soon as Serve starts.
go func() { _ = up.Serve(ctx, ready, stop) }()
if err := svc.Run(ctx); err != nil {
    return err
}
return up.ExecPending() // last: returns only when the swap failed
```

Without serverkit's graceful shutdown, give `Options.Drainer` a
`selfupdate.Drainer` that stops new work and finishes in-flight work by the
deadline; a `Drain` error declines the upgrade, with nothing swapped.

## Settings

| Field | Meaning |
|---|---|
| `Enabled` | The opt-in. Default false. |
| `Source` | An https URL of a channel manifest (the schema reliant's `daemon-channels.json` uses), or a GitHub repository `owner/name` whose releases are read. |
| `Channel` | The manifest's channel, or GitHub's `latest` (default) / `prerelease`. |
| `Interval` | Between checks. Default 6h, at least 1m. |
| `SignerRepository` | Whose release workflow signs, and the repository the signed predicate must name. Default: `Source` when it is a GitHub repository; required with a manifest. |
| `SignerWorkflow` | The signing workflow's file under `.github/workflows/`. The certificate must be that workflow at a semver release tag. |
| `SignerIdentity` | Overrides the identity `SignerWorkflow` derives: an anchored `^…$` regexp. |
| `PredicateType` | Default `https://github.com/<SignerRepository>/attestations/release-assets/v1`. |
| `Bundle` | A GitHub release's signature asset. Default `<binary>-release.sigstore.json`. |
| `InsecureAllowUnsigned` | See below. Never together with a signer. |

`Options.Policy` replaces the default (apply every newer release at routine
urgency), and `Options.Tune` edits the underlying `selfupdate.Config` —
archive naming (`ArchiveName`, default `<binary>_<goos>_<goarch>.tar.gz`),
`SwapMode: selfupdate.ExitForSupervisor` (exit 75 and let systemd or launchd
start the new binary), `ConfirmWithin`, the probe.

### Where the settings come from

`pkg/` never reads the environment, so the app decides. Split them by who
owns the value:

- **The user's choice** — `enabled`, `channel`, `interval` — is the binary's
  typed config, so it is a flag, an env var and a config-file key at once.
  Declare a block in your config proto and compose it on the binary's config
  (`binaries`):

  ```protobuf
  message SelfUpdateConfig {
    bool enabled = 1 [(forge.v1.config) = {env_var: "ACME_SELF_UPDATE", flag: "self-update", default_value: "false",
      description: "Replace this binary with newer signed releases"}];
    string channel = 2 [(forge.v1.config) = {env_var: "ACME_UPDATE_CHANNEL", flag: "update-channel", default_value: "latest"}];
    google.protobuf.Duration interval = 3 [(forge.v1.config) = {env_var: "ACME_UPDATE_INTERVAL", flag: "update-interval", default_value: "6h"}];
  }
  ```

- **The publisher's facts** — `Source` and the signer — are where releases
  come from and who signs them. They belong to the build, not the user: write
  them as constants, or stamp them per release with the config's build-time
  defaults (`binaries/config-files`).

There is no `forge.yaml` or KCL block for it on purpose. forge renders KCL env
only into the environments it runs — host, compose, cluster, hosted — and
those are exactly where a binary must not replace itself. The machines that
self-update are your users', which only the binary's own config reaches.

## Signing

Releases are verified in process with keyless Sigstore
(`forge/pkg/selfupdate/sigstore`): no cosign or gh on the user's machine. A
release installs only when its bundle chains to the Sigstore trusted root, the
certificate is the configured workflow at a release tag, every downloaded file
is a signed subject with its sha256, and the signed predicate names the
repository and the very tag being installed, so an older signed set replayed
under a newer tag is refused. Sign the release assets in the release workflow
with `actions/attest`, a predicate `{repository, release, commit}`, and attach
the bundle as `<binary>-release.sigstore.json` (forge's own `release-attest.yml`
is the worked example).

`InsecureAllowUnsigned: true` installs on the sha256 of an UNSIGNED
`checksums.txt` alone. That catches a truncated download and nothing else:
whoever can replace one release asset can replace both. It is logged at every
start. Use it for a private test channel, never for users.

## Installs it refuses

The updater replaces only an install nothing else owns (`selfupdate.Detect`).
Every other kind is refused, and the log names the owner and, when known, the
command that updates it:

| Install | Why refused |
|---|---|
| Homebrew, snap, nix, or a file dpkg/rpm/pacman owns | the package manager owns it: `brew upgrade acme`, `sudo apt-get install --only-upgrade acme` |
| Built with `-X github.com/reliant-labs/forge/pkg/selfupdate.managedBy=<owner>`, an `<binary>.managed-by` file beside it, or `FORGE_MANAGED_BY` set | a packager or supervisor said so; authoritative |
| Inside a macOS `.app`, an Electron app's resources, an AppImage | the app's own updater replaces the bundle |
| A Kubernetes pod (`KUBERNETES_SERVICE_HOST`), a Docker or Podman container | the image is the unit of upgrade |
| `Options.Managed` reports true | the app's own "something else runs me" |
| A directory this user cannot write | the swap renames there |

Heuristics only refuse; nothing grants a swap the table refuses. Packagers
should stamp their builds with `managedBy` so the refusal does not depend on
the install path.

Windows stages and probes releases but cannot swap a running `.exe` yet: the
updater logs `apply declined` once and stops checking.
