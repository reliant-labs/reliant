package serverkit

// probe_paths.go — WHERE the probes are answered.
//
// Every process forge serves answers two probes: liveness (a static 200 — the
// process is up) and readiness (/readyz's live answer — the process can take
// traffic). Their paths default to /healthz and /readyz and are configurable,
// because a platform that probes a fixed path — a GKE HealthCheckPolicy, a
// docker-compose healthcheck, an ingress health route — must keep working
// when the process moves under serverkit. Each probe may answer on several
// paths (an old one and forge's), and the FIRST path of each is the one a
// deploy probes: the KCL lowering reads the same config fields
// (LIVENESS_PATHS, READINESS_PATHS) for the default probe it writes, so the
// pod's probe and the process's route cannot drift apart.

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// The default probe paths.
const (
	DefaultLivenessPath  = "/healthz"
	DefaultReadinessPath = "/readyz"
)

// The other routes forge serves on the probe surface.
const (
	metricsPath    = "/metrics"
	flowHealthPath = "/flow-health"
)

// ProbePaths are the paths a process answers its probes on. Each list's
// first path is the canonical one, the one a deploy probes; the rest are
// aliases served alike.
type ProbePaths struct {
	// Live answers liveness: 200 "ok" while the process runs, booting
	// included. Empty is [DefaultLivenessPath].
	Live []string
	// Ready answers readiness: the boot probes' 503 until the server is
	// composed, then /readyz's drain gate and dependency checks. Empty is
	// [DefaultReadinessPath].
	Ready []string
}

// WithDefaults fills an empty list with its default path.
func (p ProbePaths) WithDefaults() ProbePaths {
	if len(p.Live) == 0 {
		p.Live = []string{DefaultLivenessPath}
	}
	if len(p.Ready) == 0 {
		p.Ready = []string{DefaultReadinessPath}
	}
	return p
}

// Validate rejects paths a listener cannot serve as an exact probe route: one
// not starting with "/", "/" itself (the routes' pattern), a path with a
// space or a "{" (a ServeMux method or wildcard), and a path given twice.
func (p ProbePaths) Validate() error {
	var errs []error
	seen := map[string]string{}
	check := func(kind string, paths []string) {
		for _, path := range paths {
			if err := checkRoutePath(path); err != nil {
				errs = append(errs, fmt.Errorf("%s probe path %w", kind, err))
				continue
			}
			switch {
			case path == metricsPath || path == flowHealthPath:
				errs = append(errs, fmt.Errorf("%s probe path %q is forge's own %s route", kind, path, path))
			default:
				if prev, dup := seen[path]; dup {
					errs = append(errs, fmt.Errorf("probe path %q is both a %s and a %s path", path, prev, kind))
				}
				seen[path] = kind
			}
		}
	}
	p = p.WithDefaults()
	check("liveness", p.Live)
	check("readiness", p.Ready)
	return errors.Join(errs...)
}

// checkRoutePath rejects a path a listener cannot serve as an exact front
// route: one not starting with "/", "/" itself (the routes' pattern), and one
// with a space or a brace (a ServeMux method or wildcard).
func checkRoutePath(path string) error {
	switch {
	case !strings.HasPrefix(path, "/"):
		return fmt.Errorf("%q does not start with \"/\"", path)
	case path == "/":
		return errors.New(`"/" would claim every route`)
	case strings.ContainsAny(path, " {}\t"):
		return fmt.Errorf("%q is not a plain path", path)
	}
	return nil
}

// ProbePathList parses a comma-separated path list — the LIVENESS_PATHS /
// READINESS_PATHS config value — trimming each entry and dropping empty ones.
// An empty string is an empty list, which is the default path.
func ProbePathList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// routes maps every probe path to its handler.
func (p ProbePaths) routes(ready http.Handler) map[string]http.Handler {
	p = p.WithDefaults()
	out := make(map[string]http.Handler, len(p.Live)+len(p.Ready))
	for _, path := range p.Live {
		out[path] = http.HandlerFunc(serveLiveness)
	}
	for _, path := range p.Ready {
		out[path] = ready
	}
	return out
}

// serveLiveness is the liveness probe: the process is up.
func serveLiveness(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}
