package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/reliant-labs/forge/internal/devstack"
)

// The default kubeconfig is one file every k3d and kubectl invocation on the
// machine rewrites whole: read it, change one context, write it back. Neither
// tool locks it against the other, so two forge processes that create a
// cluster at once lose one of the two contexts — the second write was
// computed from a file that did not have the first one yet. The loser then
// fails a moment later on `context "k3d-<name>" does not exist`, for a cluster
// that is running fine (TestE2EDirectApplySecretsLandAndStayOutOfTheBundle hit
// it with three parallel `forge cluster up`s; a shared box running several
// agents hits it the same way).
//
// So every kubeconfig write forge makes goes through withKubeconfigLock, and
// `k3d cluster create` is told not to write at all: forge merges the new
// context itself, under the lock, right after (createK3dCluster).

// kubeconfigLockSuffix names the lock beside the kubeconfig it guards. It is
// deliberately not kubectl's own `<file>.lock`: that one is an O_EXCL marker a
// crashed kubectl leaves behind, while this is an flock the OS releases.
const kubeconfigLockSuffix = ".forge.lock"

// defaultKubeconfigPath is the file k3d and kubectl write by default: the
// first entry of $KUBECONFIG, else ~/.kube/config. "" when neither resolves.
func defaultKubeconfigPath() string {
	for _, p := range filepath.SplitList(os.Getenv("KUBECONFIG")) {
		if strings.TrimSpace(p) != "" {
			return p
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".kube", "config")
}

// withKubeconfigLock runs fn holding the cross-process lock on the default
// kubeconfig. With no resolvable kubeconfig there is nothing to guard, and fn
// runs unlocked.
func withKubeconfigLock(fn func() error) error {
	p := defaultKubeconfigPath()
	if p == "" {
		return fn()
	}
	return devstack.WithFileLock(p+kubeconfigLockSuffix, fn)
}

// withoutKubeconfigWrites returns a `k3d cluster create` argv that leaves
// the kubeconfig alone — neither merging the new context nor switching the
// current one — so the merge can happen under withKubeconfigLock instead.
// It also stops a create from silently moving the caller's current context
// onto the new cluster; the paths that want that pin it explicitly
// (pinKubectlContext).
func withoutKubeconfigWrites(args []string) []string {
	flags := []string{"--kubeconfig-update-default", "--kubeconfig-switch-context"}
	out := slices.DeleteFunc(slices.Clone(args), func(a string) bool {
		return slices.ContainsFunc(flags, func(f string) bool { return a == f || strings.HasPrefix(a, f+"=") })
	})
	for _, f := range flags {
		out = append(out, f+"=false")
	}
	return out
}
