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
// # Windows
//
// A running .exe can be renamed but neither replaced nor removed, and there
// is no exec(2). So the swap renames the running binary aside to
// <name>.old-<unixnano>.exe in its own directory, moves the staged one into
// place, starts it as a new process with the same argv, environment,
// working directory and stdio, and exits: under [ExecInPlace] the pid
// changes and this process's children are orphaned. A revert (the watchdog,
// the boot-loop guard) goes through the same rename. [Boot] and
// [Pending.Confirm] remove the images set aside once no process runs them.
//
// The window that remains: the instant between the two renames (each
// written through to disk), when the install path is missing. Windows has
// no call that closes it: ReplaceFile performs the same two renames, reports
// the same half-done state (ERROR_UNABLE_TO_MOVE_REPLACEMENT_2) and does not
// support write-through. So the move is first recorded in StateDir/move.json,
// and a process killed inside the window leaves that journal behind. The
// binary itself cannot start from a missing path; the next [Boot] that does
// run from the image set aside, <name>.old-<ns>.exe, started by hand with
// the same state directory, renames its own image back. Recovery rejects
// journal paths outside that install directory or unrelated to that binary.
// Renaming that file back by hand does the same.
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
// manager, the app's own managed predicate, a Kubernetes pod or a container
// (the image is the unit), or a directory this user cannot write. Heuristics
// only ever refuse; they never grant.
//
// # Signatures
//
// [New] refuses a [Config] with no [Verifier] unless
// [Config.InsecureAllowUnsigned] is set, and logs that opt-out at every start.
// The keyless Sigstore verifier is in the sigstore subpackage. [GitHubSource]
// and [ManifestSource] (a signed channel manifest such as reliant's
// daemon-channels.json) find the target release.
//
// Targets that are neither unix nor Windows stage but do not swap
// ([ErrUnsupported]).
//
// # Opting in
//
// The autoupdate subpackage is the default-off wiring for a long-lived
// binary: Boot, a periodic Check/Prepare/Request loop, confirmation on the
// app's readiness, and ExecPending after its shutdown. A pkg/serverkit
// server hands one to Server.SelfUpdate.
package selfupdate
