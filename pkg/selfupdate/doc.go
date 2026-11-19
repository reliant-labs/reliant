// Package selfupdate lets a long-lived forge-built binary replace itself with a newer signed release: find the release, verify it, stage it, refuse to touch an install something else owns, drain, swap, and revert if the new binary does not confirm itself.
//
// The design is docs/proposals/self-upgrade.md, §3. Policy (when to upgrade,
// how urgent) stays in the app; this package is the mechanism.
//
// # Two phases
//
// syscall.Exec skips deferred functions, so an upgrade is split in two:
//
//   - Prepare (any time, no disruption): download the release archive and
//     checksums.txt, verify them with the [Verifier], check the archive's
//     sha256 against the VERIFIED checksums.txt, extract the binary and run
//     the probe (default `<binary> version`) so a wrong-architecture or
//     truncated binary is caught while the old process still serves.
//   - Apply: [Updater.Request] drains through the app's [Drainer] and asks
//     the app to tear down ([Config.Shutdown]); the app's main then calls
//     [Updater.ExecPending] as its last act. That saves the running binary to
//     StateDir/previous/, renames the staged binary over the running one
//     (atomic; the running image keeps its old inode), records the swap in
//     StateDir/state.json and execs it with the same argv, environment and
//     pid.
//
// # Confirm and revert
//
// [Boot] runs first in main. After a swap it starts a watchdog: the app calls
// [Pending.Confirm] once its own health check passes, and if it does not
// within [Config.ConfirmWithin] the previous binary is put back and exec'd.
// A new binary that crashes before confirming twice is reverted on the next
// start (the boot-loop guard), and a reverted version is never offered again
// by [Updater.Check] until a newer release appears.
//
// # Owners
//
// [Detect] refuses installs something else owns: a managed-by stamp, at
// build time (-X …/selfupdate.managedBy=<owner>), in a <binary>.managed-by
// file beside it or in FORGE_MANAGED_BY (authoritative), an app bundle or AppImage, a package
// manager, the app's own managed predicate (a cloud pod), or a directory this
// user cannot write. Heuristics only ever refuse; they never grant.
//
// # Signatures
//
// [New] refuses a [Config] with no [Verifier] unless
// [Config.InsecureAllowUnsigned] is set, and logs that opt-out at every start.
//
// Windows: staging works; the swap does not yet ([ErrUnsupported]).
package selfupdate
