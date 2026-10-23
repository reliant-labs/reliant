package v1alpha1

import (
	"errors"
	"fmt"
)

// Rendered-name limits. metadata.name becomes the name of every object the
// renderer emits, so its bounds live with the type rather than being
// discovered at render time.
const (
	// MaxWorkloadNameLength is the RFC-1123 label limit every Kubernetes
	// object name, Service DNS name and container name shares.
	MaxWorkloadNameLength = 63
	// StandaloneJobHashLength is the length of the spec hash a standalone
	// job's Job name carries (<name>-<hash>). A Job's pod template is
	// immutable, so a changed spec must be a NEW Job, and the hash is what
	// makes it one.
	StandaloneJobHashLength = 10
	// MaxStandaloneJobNameLength leaves room for "-" plus the hash inside
	// the 63-character label.
	MaxStandaloneJobNameLength = MaxWorkloadNameLength - 1 - StandaloneJobHashLength
)

// Validate checks a Workload OBJECT under profile p: the rules that need
// metadata.name, then every WorkloadSpec rule (Spec.Validate). All errors
// are collected together.
//
// THIS IS THE ADMISSION ENTRYPOINT. The control plane admits Workload CRs,
// so it calls this rather than Spec.Validate. A spec cannot see its own
// name, and three rules are about that name:
//
//   - the name is an RFC-1123 label of at most 63 characters, because it is
//     every rendered object's name (Deployment, Service, ServiceAccount,
//     main container);
//   - a STANDALONE job's name is at most MaxStandaloneJobNameLength (52),
//     because its Job is named <name>-<10-char spec hash>. A gating job
//     renders no Job of its own, so the 63-character limit is its limit;
//   - no sidecar shares the workload's name, which the main container uses.
//     Kubernetes refuses two containers of one name, but only server-side,
//     so the deploy would die against the real cluster.
//
// Cross-WORKLOAD rules (duplicate names in one env, what `before` names, job
// cycles) need the whole set and stay with pkg/deploy.RenderWorkloads.
// Metadata other than the name (labels, namespace) is the platform's to
// stamp and is not checked here.
func (w Workload) Validate(p Profile) error {
	var errs []error
	name := w.Name
	if !dnsLabelRE.MatchString(name) || len(name) > MaxWorkloadNameLength {
		errs = append(errs, fmt.Errorf("name %q must be an RFC-1123 label of at most %d characters: it is every rendered object's name", name, MaxWorkloadNameLength))
	}
	if w.Spec.EffectiveKind() == KindJob && len(w.Spec.Before) == 0 && len(name) > MaxStandaloneJobNameLength {
		errs = append(errs, fmt.Errorf("a standalone job's name is at most %d characters: its Job is named <name>-<%d-char spec hash> (got %d characters)", MaxStandaloneJobNameLength, StandaloneJobHashLength, len(name)))
	}
	for _, c := range w.Spec.Sidecars {
		if c.Name == name {
			errs = append(errs, fmt.Errorf("sidecars[%s]: a sidecar may not be named %q, the workload's own name, which its main container uses", c.Name, name))
		}
	}
	if err := w.Spec.Validate(p); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
