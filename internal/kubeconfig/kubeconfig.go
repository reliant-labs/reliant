// Package kubeconfig answers one question: where on THIS machine does the
// default kubeconfig live.
//
// forge needs the answer because a HOST process it launches (forge.HostDeploy
// runs services on the developer's machine in dev) may have to reach a
// cluster, and a host process resolves a kubeconfig from a real filesystem
// path — unlike a pod, which gets one from the Volume that mounts the minted
// Secret.
//
// The answer is not a forge convention. It is where forge's own cluster
// creation puts it: `k3d cluster create` followed by `k3d kubeconfig merge
// <name> --kubeconfig-merge-default` (internal/cli/dev_cluster.go,
// createK3dCluster → mergeK3dKubeconfig) writes the cluster's entry into the
// DEFAULT kubeconfig. So "the default kubeconfig" is, by construction, the
// file that will contain the context for any cluster forge created.
//
// kubectl's precedence is reproduced deliberately rather than approximated
// with `~/.kube/config`: a machine that sets KUBECONFIG has a different
// default, and forge writing to one file while telling a host process to read
// another is a failure that surfaces as client-go's "context does not exist"
// — a message that names neither file.
package kubeconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultPath returns the path to the default kubeconfig, using kubectl's own
// precedence:
//
//  1. the FIRST entry of $KUBECONFIG, when set. KUBECONFIG is a path LIST, and
//     the first entry is where a merge writes — which is the question being
//     asked here, so an empty leading entry is skipped rather than returned.
//  2. otherwise $HOME/.kube/config.
//
// Returns "" when neither is determinable (no KUBECONFIG and no home
// directory). Callers must treat "" as "unknown" and not substitute a guess:
// a wrong path is harder to diagnose than a missing one, because it fails as a
// missing context inside a file that exists.
func DefaultPath() string {
	if fromEnv := firstKubeconfigEntry(os.Getenv("KUBECONFIG")); fromEnv != "" {
		return fromEnv
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".kube", "config")
}

// firstKubeconfigEntry splits a KUBECONFIG value on the platform's path-list
// separator and returns the first non-empty entry, or "" when there is none.
//
// The separator is ':' everywhere except Windows (';'), matching client-go's
// own parsing — splitting on ':' on Windows would cut "C:\Users\..." in half
// at the drive letter and yield "C" as the kubeconfig path.
func firstKubeconfigEntry(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	sep := ":"
	if runtime.GOOS == "windows" {
		sep = ";"
	}
	for _, entry := range strings.Split(value, sep) {
		if entry = strings.TrimSpace(entry); entry != "" {
			return entry
		}
	}
	return ""
}
