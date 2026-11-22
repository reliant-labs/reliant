package selfupdate

import "os"

// This file is the package's whole contact with the process environment,
// exempted from pkg/'s no-ambient-environment rule (pkg/.golangci.yml) for
// two reasons, neither of which is app configuration:
//
//   - FORGE_MANAGED_BY is a contract between a SUPERVISOR and the binary it
//     runs. The app cannot pass it in: the whole point is that a supervisor
//     marks the install as its own without the app's cooperation, and the
//     marker can only refuse a swap, never grant one.
//   - KUBERNETES_SERVICE_HOST is the kubelet's mark on every container it
//     starts. Like FORGE_MANAGED_BY it only ever refuses a swap: in a pod the
//     image is the unit of upgrade, never the binary.
//   - exec(2) replaces this process with the new binary, which must start
//     with the same environment, as forgedelegate.Exec does. It is passed
//     through, never consulted.

func lookupManagedByEnv() (string, bool) { return os.LookupEnv(ManagedByEnv) }

// KubernetesServiceHostEnv is set by the kubelet in every container of a
// pod; [Detect] refuses to replace a binary that sees it.
const KubernetesServiceHostEnv = "KUBERNETES_SERVICE_HOST"

func lookupKubernetesEnv() (string, bool) { return os.LookupEnv(KubernetesServiceHostEnv) }

func processEnv() []string { return os.Environ() }
