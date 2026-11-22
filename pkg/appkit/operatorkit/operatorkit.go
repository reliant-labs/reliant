// Package operatorkit owns the controller-manager runtime behind the
// generated App.RunOperators method in forge projects.
//
// # Pattern
//
// The generated pkg/app/bootstrap.go used to open-code the
// controller-runtime manager setup (kubeconfig resolution, leader
// election, scheme registration, controller setup, manager start).
// Following the "generated files are tables, not programs" rule, the
// generated RunOperators is now a single delegation to [Run] with one
// dumb [Controller] row per operator:
//
//	func (a *App) RunOperators(ctx context.Context, logger *slog.Logger, healthProbeAddr string) error {
//	    return operatorkit.Run(ctx, logger, operatorkit.Options{
//	        LeaderElectionID:       "example.com/myproj-leader",
//	        HealthProbeBindAddress: healthProbeAddr,
//	    }, []operatorkit.Controller{
//	        {Name: "scaler", AddToScheme: scaler.AddToScheme,
//	            SetupWithManager: a.Operators.Scaler.SetupWithManager},
//	    })
//	}
//
// operatorkit lives in its own package (rather than appkit proper) so
// projects without operators never compile controller-runtime and its
// Kubernetes dependency tree — the generated import is conditional on
// the project having operators.
//
// # Behavioural fingerprint
//
// All observable strings from the pre-table generated RunOperators are
// preserved verbatim:
//
//   - warn "operators disabled: no Kubernetes cluster reachable" when
//     kubeconfig resolution fails (vanilla docker-compose dev, fresh
//     laptop, CI without a kind/k3d cluster) — the binary continues
//     without operators rather than crashing, matching how NATS
//     degrades.
//   - "creating controller manager: <wrapped error>".
//   - "adding <name> scheme: <wrapped error>".
//   - "setting up controller %q: <wrapped error>".
//   - info "registered operator controller" / "starting controller
//     manager".
package operatorkit

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
)

// Leader-election timing + client-rate defaults.
//
// controller-runtime's stock leader-election timings (LeaseDuration 15s,
// RenewDeadline 10s, RetryPeriod 2s) are tuned for a fast, dedicated API
// server and an HA controller where a quick failover is worth a hair-trigger
// self-termination: when the acting leader can't renew its lease within
// RenewDeadline, the manager invokes its FailureProcess and the PROCESS EXITS
// ("leader election lost" → "component failed — terminating process"). On a
// contended/single-node API server a brief latency spike (slow networkpolicy
// PUT, an HTTP/2 connection drop, a TLS-handshake timeout) blows past a 10s
// RenewDeadline and kills an otherwise-healthy controller mid-reconcile,
// stalling every CR it owns until the relist completes.
//
// For the common forge shape — a SINGLE-replica controller — fast failover
// buys nothing (there is no standby to fail over to) while the tight deadline
// costs a spurious crash. So we triple the timings to a tolerance band that
// rides out transient API slowness, and let a project that needs different
// numbers set them on [Options]. These are the conventional hardened values
// for single-replica controllers on shared/edge clusters.
const (
	defaultLeaseDuration = 45 * time.Second
	defaultRenewDeadline = 30 * time.Second
	defaultRetryPeriod   = 5 * time.Second

	// Client-go's stock rest.Config limits (QPS 5 / Burst 10 raw; 20 / 30
	// once controller-runtime applies its own defaults) throttle the
	// controller's OWN requests client-side under reconcile fan-out — the
	// "client-side throttling, request waited …" / priority-and-fairness
	// stalls that compound a slow API server into renew-deadline misses.
	// Raise the ceiling so a burst of reconciles isn't self-queued; the
	// server's APF still protects the API server from genuine overload.
	defaultClientQPS   float32 = 50
	defaultClientBurst int     = 100
)

// Controller is one generated operator row: the CRD scheme installer
// and the controller's manager hookup, both referenced straight off the
// generated operator package / constructed instance.
type Controller struct {
	// Name is the operator's forge.yaml name — used in error messages
	// and registration logs.
	Name string
	// AddToScheme registers the operator's CRD types on the manager's
	// scheme. Optional (nil is skipped) for controllers that only watch
	// built-in types.
	AddToScheme func(s *runtime.Scheme) error
	// SetupWithManager registers the controller with the manager.
	SetupWithManager func(mgr ctrl.Manager) error
}

// Options carries the per-project manager configuration the generated
// row table supplies.
type Options struct {
	// LeaderElectionID is the lease name used for leader election — the
	// scaffolded RunOperators passes "<module>-leader". Two processes that
	// both run a manager and share this name contend for the SAME lease, so
	// a project running more than one gives each its own value here.
	LeaderElectionID string

	// LeaderElectionNamespace is the namespace holding the lease.
	//
	// Empty is correct IN-CLUSTER: controller-runtime infers the namespace
	// from the ServiceAccount mount. Out of cluster there is nothing to
	// infer and NewManager hard-errors, so Run treats "reachable cluster,
	// not in-cluster, no namespace" exactly like no-cluster — it warns and
	// continues WITHOUT operators rather than dying. Set this to run a
	// manager from a host process against a dev cluster; the value is the
	// opt-in.
	LeaderElectionNamespace string

	// HealthProbeBindAddress, when non-empty, binds a /healthz +
	// /readyz listener on that address for the controller-runtime
	// manager. The generated RunOperators forwards it from
	// serverkit.Config.OperatorHealthProbeAddr — which is itself projected
	// from the app's typed config, so a deployment that needs a probe port
	// declares one field and it arrives here. Empty leaves the manager
	// without a probe listener (the default — vanilla forge projects don't
	// bind one).
	HealthProbeBindAddress string

	// Leader-election timings. Zero takes the hardened defaults documented
	// at the top of this file (45s/30s/5s), which suit the single-replica
	// controller a forge project scaffolds. Raise or lower them together —
	// LeaseDuration > RenewDeadline > RetryPeriod is a controller-runtime
	// invariant, and a mismatched trio self-terminates a healthy leader.
	LeaseDuration time.Duration
	RenewDeadline time.Duration
	RetryPeriod   time.Duration

	// ClientQPS / ClientBurst raise the manager's own client-side request
	// ceiling. Zero takes the defaults above (50/100). They only apply when
	// the resolved rest.Config does not already carry a value, so an
	// explicit kubeconfig setting still wins.
	ClientQPS   float32
	ClientBurst int

	// ByObjectNamespaces scopes the manager cache PER OBJECT TYPE: each entry
	// maps an object example (e.g. &v1alpha1.Workspace{}) to the ONLY
	// namespaces the manager's informers watch/list for that type
	// (controller-runtime cache.ByObject.Namespaces). Types WITHOUT an entry
	// keep the default cluster-wide watch, so a controller can confine its own
	// CRD to the namespace its stack deploys into while still watching
	// cross-namespace workload objects (Pods/PVCs in per-user namespaces)
	// everywhere.
	//
	// Motivation: co-located stacks on one shared cluster (e.g. dev + e2e on
	// one k3d node) each run their own copy of the same operator. With a
	// cluster-wide CR watch, each copy also reconciles the OTHER stack's CRs —
	// a derelict controller from one stack can then stamp its own config
	// (image, env) onto a sibling stack's workloads. Scoping the CR watch to
	// the stack's own namespace makes cross-stack reconciliation structurally
	// impossible.
	//
	// Entries with no namespaces (or only empty strings) are dropped — that
	// type stays cluster-wide, preserving the legacy behavior when the
	// deployment namespace is unknown. The scoped types' GVKs are resolved
	// against the manager scheme at manager construction, so every scoped
	// type MUST be registered by one of the controllers' AddToScheme hooks
	// (Run registers them all before creating the manager).
	ByObjectNamespaces map[client.Object][]string

	// ByObjectLabels scopes the manager cache PER OBJECT TYPE by LABEL: the
	// informers for that type see ONLY objects matching the selector, in
	// every namespace (controller-runtime cache.ByObject.Label).
	//
	// This is the scoping primitive for CRs whose namespaces are not known up
	// front — one per deploy environment, created at runtime — where a
	// namespace list cannot be written down. A publisher stamps an ownership
	// label (e.g. forge.dev/stack=<id>) and each stack's operator watches only
	// its own value, so two stacks sharing a cluster cannot reconcile each
	// other's CRs.
	//
	// A type may appear in BOTH maps; the cache then intersects them. A nil
	// selector is dropped. UNLIKE ByObjectNamespaces there is no "empty means
	// cluster-wide" fallback to worry about here: the caller passes a real
	// selector or nothing, and refusing an empty scope is the caller's job.
	ByObjectLabels map[client.Object]labels.Selector
}

// Run creates a controller manager, registers every controller's
// scheme and setup, and starts the manager. It blocks until ctx is
// cancelled or an error occurs; the caller runs it in a goroutine.
//
// When no Kubernetes cluster is reachable, kubeconfig resolution fails
// and Run logs a warning and returns nil — the process continues
// without operators instead of crashing.
func Run(ctx context.Context, logger *slog.Logger, opts Options, controllers []Controller) error {
	if logger == nil {
		logger = slog.Default()
	}

	cfg, err := ctrl.GetConfig()
	if err != nil {
		logger.WarnContext(ctx, "operators disabled: no Kubernetes cluster reachable", "error", err)
		return nil
	}

	// Out-of-cluster leader election needs an explicit lease namespace —
	// controller-runtime infers it from the ServiceAccount mount in-cluster
	// and hard-errors otherwise ("unable to find leader election
	// namespace"). A host-mode process with a working kubeconfig (the
	// dev-loop shape: admin binary on the laptop, operators deployed
	// in-cluster) would otherwise get PAST the no-cluster degrade above
	// and then die in NewManager. Treat "reachable cluster, but not
	// in-cluster and no declared namespace" the same way as no-cluster:
	// warn and continue without operators. Options.LeaderElectionNamespace
	// opts a host process back in.
	leaderNS := opts.LeaderElectionNamespace
	if leaderNS == "" && !runningInCluster() {
		logger.WarnContext(ctx, "operators disabled: not running in-cluster and Options.LeaderElectionNamespace is empty; set it to run operators from a host process")
		return nil
	}

	probeAddr := opts.HealthProbeBindAddress

	// Lift the client's own request ceiling before NewManager derives its
	// clients from cfg. Only set when unconfigured so an explicit kubeconfig /
	// caller value still wins. See defaultClientQPS for rationale.
	if cfg.QPS == 0 {
		cfg.QPS = orDefaultFloat32(opts.ClientQPS, defaultClientQPS)
	}
	if cfg.Burst == 0 {
		cfg.Burst = orDefaultInt(opts.ClientBurst, defaultClientBurst)
	}

	leaseDuration := orDefaultDuration(opts.LeaseDuration, defaultLeaseDuration)
	renewDeadline := orDefaultDuration(opts.RenewDeadline, defaultRenewDeadline)
	retryPeriod := orDefaultDuration(opts.RetryPeriod, defaultRetryPeriod)

	// Build the manager scheme BEFORE NewManager: client-go's stock types plus
	// every controller's CRD types. Registration must precede manager creation
	// because the manager constructs its cache eagerly, and a per-object cache
	// scope (Options.ByObjectNamespaces) resolves each scoped object's GVK
	// against this scheme at construction time. A fresh scheme — rather than
	// mutating client-go's global scheme.Scheme, which is what leaving
	// ctrl.Options.Scheme nil did — keeps registrations process-local; the
	// per-controller error string is unchanged.
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return fmt.Errorf("adding client-go scheme: %w", err)
	}
	for _, c := range controllers {
		if c.AddToScheme == nil {
			continue
		}
		if err := c.AddToScheme(scheme); err != nil {
			return fmt.Errorf("adding %s scheme: %w", c.Name, err)
		}
	}

	mgr, err := ctrl.NewManager(cfg, managerOptions(opts, scheme, leaderNS, probeAddr, leaseDuration, renewDeadline, retryPeriod))
	if err != nil {
		return fmt.Errorf("creating controller manager: %w", err)
	}

	// Wire the default /healthz + /readyz checks so the listener
	// configured above actually answers 200. Without these, the manager
	// binds the port but every probe gets 404, defeating the listener's
	// purpose. The "ping" check is the conventional always-ok signal —
	// the manager keeps its own internal state.
	if probeAddr != "" {
		if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
			return fmt.Errorf("adding healthz check: %w", err)
		}
		if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
			return fmt.Errorf("adding readyz check: %w", err)
		}
	}

	// CRD schemes were registered on the manager scheme before NewManager
	// (see above) — every controller's SetupWithManager can rely on a sibling
	// operator's types already being present.
	for _, c := range controllers {
		if err := c.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("setting up controller %q: %w", c.Name, err)
		}
		logger.InfoContext(ctx, "registered operator controller", "controller", c.Name)
	}

	logger.InfoContext(ctx, "starting controller manager")
	return mgr.Start(ctx)
}

// cacheByObject converts Options.ByObjectNamespaces (object type → namespace
// list) into controller-runtime cache.ByObject rows. Entries with no usable
// namespace (empty list, or only empty strings) are dropped — that type keeps
// the default cluster-wide watch. Returns nil when nothing is scoped so Run
// hands the manager a zero-value cache.Options (byte-identical to the
// pre-scoping behavior).
func cacheByObject(scopes map[client.Object][]string) map[client.Object]cache.ByObject {
	if len(scopes) == 0 {
		return nil
	}
	byObject := make(map[client.Object]cache.ByObject, len(scopes))
	for obj, namespaces := range scopes {
		cfgs := make(map[string]cache.Config, len(namespaces))
		for _, ns := range namespaces {
			if ns == "" {
				continue
			}
			cfgs[ns] = cache.Config{}
		}
		if len(cfgs) == 0 {
			continue
		}
		byObject[obj] = cache.ByObject{Namespaces: cfgs}
	}
	if len(byObject) == 0 {
		return nil
	}
	return byObject
}

// withLabelScopes folds Options.ByObjectLabels into the ByObject rows
// cacheByObject produced, adding a row for a type that had no namespace scope.
// Map keys are the caller's object pointers, so a type scoped in both maps must
// be passed the SAME pointer in both to merge into one row; two pointers of one
// type would be two rows for one GVK, which controller-runtime rejects at
// manager construction rather than silently picking one.
func withLabelScopes(rows map[client.Object]cache.ByObject, scopes map[client.Object]labels.Selector) map[client.Object]cache.ByObject {
	for obj, sel := range scopes {
		if sel == nil {
			continue
		}
		if rows == nil {
			rows = make(map[client.Object]cache.ByObject, len(scopes))
		}
		row := rows[obj]
		row.Label = sel
		rows[obj] = row
	}
	return rows
}

// managerOptions builds the ctrl.Options Run hands to NewManager. Split out
// of Run so the resolved options can be asserted without a reachable
// cluster: NewManager needs one, and every setting below is a decision this
// package makes rather than something the environment supplies.
func managerOptions(opts Options, scheme *runtime.Scheme, leaderNS, probeAddr string, leaseDuration, renewDeadline, retryPeriod time.Duration) ctrl.Options {
	return ctrl.Options{
		Scheme:           scheme,
		LeaderElection:   true,
		LeaderElectionID: opts.LeaderElectionID,
		// Empty in-cluster — controller-runtime infers the namespace from
		// the ServiceAccount mount. Non-empty only via the
		// Options.LeaderElectionNamespace opt-in above.
		LeaderElectionNamespace: leaderNS,
		// Give the lease back on the way out, instead of letting it expire.
		//
		// Without this the outgoing leader simply stops renewing, so the
		// successor waits out the FULL LeaseDuration before it may take
		// over — 45s with no controller reconciling anything, on every
		// rollout (measured in prod: "stopped leading" at T, "became
		// leader" at T+45s). Releasing on cancel hands over in about the
		// RetryPeriod instead.
		//
		// THE INVARIANT THIS DEPENDS ON: the process must have stopped
		// reconciling by the time it releases, or two leaders act at once
		// — the exact split-brain the lease exists to prevent.
		// controller-runtime releases the lease as Start unwinds, after it
		// has drained its own controllers, and serverkit.Run then WAITS on
		// operatorWg for RunOperators to return before it continues its
		// teardown (pkg/serverkit/run.go, "Wait for the operator/controller
		// manager goroutine to drain"). So Start has returned — and every
		// reconciler with it — before this process does anything else. A
		// caller that runs the manager WITHOUT waiting for Run to return
		// must not set this.
		LeaderElectionReleaseOnCancel: true,
		// Hardened leader-election timings so a transient API-server latency
		// spike doesn't trip RenewDeadline and self-terminate a healthy
		// single-replica controller. See defaultLeaseDuration for rationale.
		LeaseDuration:          &leaseDuration,
		RenewDeadline:          &renewDeadline,
		RetryPeriod:            &retryPeriod,
		HealthProbeBindAddress: probeAddr,
		// Per-object namespace scoping (nil ByObject leaves every informer
		// cluster-wide — the legacy shape). See Options.ByObjectNamespaces.
		Cache: cache.Options{ByObject: withLabelScopes(cacheByObject(opts.ByObjectNamespaces), opts.ByObjectLabels)},
	}
}

// orDefault* apply a declared value when the caller set one, and the
// hardened default when it left the field zero. A negative value is treated
// as unset for the same reason a typo'd env var used to be: a zero or
// negative timing is not a faster controller, it is controller-runtime's own
// short stock default silently reinstated.
func orDefaultDuration(v, def time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return def
}

func orDefaultFloat32(v, def float32) float32 {
	if v > 0 {
		return v
	}
	return def
}

func orDefaultInt(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// runningInCluster reports whether the process is running inside a
// Kubernetes pod, using the same signal controller-runtime's leader
// election uses to infer the lease namespace: the ServiceAccount
// namespace mount. Checking the mount (rather than KUBERNETES_SERVICE_HOST)
// matches what NewManager will actually succeed or fail on.
func runningInCluster() bool {
	_, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	return err == nil
}
