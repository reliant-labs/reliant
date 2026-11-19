package frontenddeps

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The one place forge decides which package manager owns a frontend and how
// to drive it. Every caller that installs, runs a script, audits or tells a
// user what to run asks here, so an npm frontend and a pnpm frontend are
// handled by the same code path rather than by an `npm` literal that is right
// for one of them.

// PackageManager names the package manager that owns dir's install: the
// lockfile on disk (see lockfilePackageManager for how a declared runner
// breaks a tie between two using package.json's packageManager pin, then
// declared), else package.json's "packageManager" field, else declared (a
// KCL dev_runner), else npm.
//
// The packageManager field sits between the lockfile and the declaration
// because a fresh scaffold has no lockfile yet: without it, a frontend whose
// manifest says pnpm would get its first lockfile written by npm.
func PackageManager(dir, declared string) string {
	// An explicit manifest pin breaks competing-lockfile ties as well as
	// choosing the first install's manager. A stale foreign lock must not
	// override the maintained lock belonging to that pin.
	if name, _ := manifestPackageManager(dir); name != "" {
		declared = name
	}
	for _, lock := range lockfiles {
		if fileExists(filepath.Join(dir, lock.file)) {
			return lockfilePackageManager(dir, declared)
		}
	}
	if name, _ := manifestPackageManager(dir); name != "" {
		return name
	}
	return lockfilePackageManager(dir, declared)
}

// ScaffoldDefault is the package manager a frontend forge scaffolds pins in
// its package.json `packageManager` field.
const ScaffoldDefault = "pnpm"

// PackageManagerOrScaffold is PackageManager for a frontend that may not be
// on disk yet: a directory with no package.json is one forge is about to
// scaffold, so it gets ScaffoldDefault rather than npm. For the files forge
// renders at scaffold time (CI workflows) before the frontend itself.
func PackageManagerOrScaffold(dir string) string {
	if !fileExists(filepath.Join(dir, "package.json")) {
		return ScaffoldDefault
	}
	return PackageManager(dir, "")
}

// manifestPackageManager reads package.json's "packageManager" field
// ("pnpm@12.15.0", optionally "+sha512.…") into a name and a version.
func manifestPackageManager(dir string) (name, version string) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return "", ""
	}
	var manifest struct {
		PackageManager string `json:"packageManager"`
	}
	if json.Unmarshal(data, &manifest) != nil || manifest.PackageManager == "" {
		return "", ""
	}
	spec, _, _ := strings.Cut(manifest.PackageManager, "+")
	name, version, _ = strings.Cut(spec, "@")
	switch name {
	case "npm", "pnpm", "yarn":
		return name, version
	}
	return "", ""
}

// InstallArgv is the install forge runs in dir, manager first: what
// `forge env up`, `forge build`, `forge lint` and the CI gate run, and what
// a remediation message tells a user to run.
func InstallArgv(dir, declared string, frozen bool) []string {
	runner := PackageManager(dir, declared)
	return append([]string{runner}, installArgs(runner, dir, frozen)...)
}

// InstallCommand renders InstallArgv for a person to type.
func InstallCommand(dir, declared string, frozen bool) string {
	return strings.Join(InstallArgv(dir, declared, frozen), " ")
}

// InstallStamp is the file the package manager itself rewrites on every
// install it completes, so one older than the lockfile means the lockfile has
// moved on (a rebase, a bump) since the last install. "" when the manager
// keeps none forge knows of.
func InstallStamp(dir, runner string) string {
	switch runner {
	case "npm":
		return filepath.Join(NpmInstallRoot(dir), "node_modules", ".package-lock.json")
	case "pnpm":
		return filepath.Join(dir, "node_modules", ".modules.yaml")
	case "yarn":
		return filepath.Join(dir, "node_modules", ".yarn-integrity")
	}
	return ""
}

// command builds the exec.Cmd for one package-manager invocation.
//
// npm ships with node; pnpm and yarn do not. A frontend that commits a
// pnpm-lock.yaml must still install on a machine or CI runner that has node
// but never had pnpm put on PATH, so a missing pnpm/yarn is run through
// corepack (bundled with node through v24) or, failing that, `npm exec` of the
// version package.json's packageManager pins. Failing the whole command for
// want of a binary that npm can fetch in two seconds helps nobody.
func command(ctx context.Context, dir, runner string, args ...string) *exec.Cmd {
	argv := resolveRunner(dir, runner, exec.LookPath)
	cmd := exec.CommandContext(ctx, argv[0], append(argv[1:], args...)...)
	cmd.Dir = dir
	return cmd
}

// resolveRunner returns the argv prefix that runs runner in dir. lookPath is
// exec.LookPath outside tests.
func resolveRunner(dir, runner string, lookPath func(string) (string, error)) []string {
	if _, err := lookPath(runner); err == nil || runner == "npm" {
		return []string{runner}
	}
	pin := runner
	if name, version := manifestPackageManager(dir); name == runner && version != "" {
		pin = runner + "@" + version
	}
	if _, err := lookPath("corepack"); err == nil {
		return []string{"corepack", pin}
	}
	return []string{"npm", "exec", "--yes", "--package=" + pin, "--", runner}
}

// pnpmCopiedSignature is what pnpm prints when it could not hard-link from
// its store and fell back to copying every package. It always can on one
// filesystem, so this means the store is on another mount: link(2) refuses to
// cross a mount point (EXDEV) even within one filesystem, which is exactly
// what a store placed on a separately bind-mounted cache volume does.
const pnpmCopiedSignature = "Packages are copied from the content-addressable store"

// copiedFromStoreWarning explains a pnpm install that copied rather than
// linked, or returns "" for one that linked.
func copiedFromStoreWarning(output string) string {
	if !strings.Contains(output, pnpmCopiedSignature) {
		return ""
	}
	return "warning: pnpm COPIED every package into node_modules instead of hard-linking it from its store, " +
		"so this install costs its full size on disk again. The store (`pnpm store path`) is on a different " +
		"mount from the project, and hard links cannot cross mounts. Point the store at a directory on the " +
		"project's mount (pnpm_config_store_dir=<dir>, or store-dir in pnpm's global config) and reinstall."
}

// ExecArgv runs runner with args in dir on THIS machine: runner itself when it
// is on PATH, else its corepack / npm exec fallback (see command). For a
// caller that executes argv itself rather than through this package.
func ExecArgv(dir, runner string, args ...string) []string {
	return append(resolveRunner(dir, runner, exec.LookPath), args...)
}

// LockfileName is the lockfile runner writes.
func LockfileName(runner string) string {
	for _, lock := range lockfiles {
		if lock.runner == runner {
			return lock.file
		}
	}
	return ""
}

// Install runs dir's install now, whether or not node_modules looks current,
// streaming the package manager's output: for a caller that has already
// established that something is missing.
func Install(ctx context.Context, dir, declared string, frozen bool) error {
	runner := PackageManager(dir, declared)
	if err := VerifyPinnedRunner(ctx, dir, runner); err != nil {
		return err
	}
	return runInstall(ctx, runner, dir, installArgs(runner, dir, frozen))
}
