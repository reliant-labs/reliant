package openbao

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// RetiredAuthMount is an auth mount the store must no longer carry: one left
// by an install that predates this component (a bespoke `kubernetes` or
// `approle` mount). The component converges only the `jwt-*` mounts it
// writes, so nothing else would ever remove it; declaring it here does.
//
// The converge Job disables the mount if it is present (a mount that is
// already gone is a no-op), after checking it holds no roles. Disabling a
// mount revokes every token it issued and deletes its roles, so a mount that
// still has roles is refused unless Force says the roles are known to be
// dead. Reason is required and records why that is safe (the evidence the
// mount is unused); it is written to the Job's log with the disable.
type RetiredAuthMount struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
	Force  bool   `json:"force,omitempty"`
}

var retiredMountNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]{0,62}[a-z0-9])?$`)

// validateRetired is the part of the retire rules that needs only the spec.
// It also guards the policy: a name is rendered into an ACL path, so one that
// could carry a glob never reaches [retiredRules].
func (s Spec) validateRetired() []error {
	var errs []error
	seen := map[string]bool{}
	for _, m := range s.RetiredAuthMounts {
		switch {
		case !retiredMountNameRe.MatchString(m.Name):
			errs = append(errs, fmt.Errorf("retiredAuthMounts: %q is not an auth mount name (lowercase letters, digits, '-' and '_'; no slash or glob): it is rendered into an ACL path", m.Name))
			continue
		case m.Name == "token":
			errs = append(errs, fmt.Errorf("retiredAuthMounts: %q is OpenBao's built-in token auth and cannot be disabled. Drop it", m.Name))
		case seen[m.Name]:
			errs = append(errs, fmt.Errorf("retiredAuthMounts: %q is declared twice", m.Name))
		}
		seen[m.Name] = true
		if strings.TrimSpace(m.Reason) == "" {
			errs = append(errs, fmt.Errorf("retiredAuthMounts: %q states no reason. Disabling a mount revokes every token it issued: say why it is unused (the evidence), so the declaration can be audited", m.Name))
		}
	}
	return errs
}

// retiredRules are the converge policy's grants for retiring: for each listed
// name, exactly the delete on its mount (OpenBao requires sudo to disable an
// auth mount) and the listing of its roles that the in-use check reads. Never
// a glob, and never on the access-admin policy that delegated principals hold.
func retiredRules(retired []RetiredAuthMount) []Rule {
	var r []Rule
	for _, m := range retired {
		r = append(r,
			Rule{Path: "sys/auth/" + m.Name, Capabilities: []string{"delete", "sudo"}},
			Rule{Path: "auth/" + m.Name + "/role", Capabilities: []string{"list"}},
		)
	}
	return r
}

// sortedRetired is the declaration in a canonical order, so the same spec in
// another order gives the same document.
func sortedRetired(in []RetiredAuthMount) []RetiredAuthMount {
	out := slices.Clone(in)
	for i := range out {
		out[i].Reason = strings.Join(strings.Fields(out[i].Reason), " ")
	}
	slices.SortFunc(out, func(a, b RetiredAuthMount) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// checkRetiredNotManaged refuses retiring a mount the access document itself
// writes: converge would enable it and then disable it on every run.
func checkRetiredNotManaged(retired []RetiredAuthMount, managed []Mount, selfMount string) error {
	var errs []error
	for _, m := range retired {
		if m.Name == selfMount {
			errs = append(errs, fmt.Errorf("retiredAuthMounts: %q is this store's own mount: it holds the converge, consumer and writer roles. It can never be retired", m.Name))
			continue
		}
		if slices.ContainsFunc(managed, func(x Mount) bool { return x.Name == m.Name }) {
			errs = append(errs, fmt.Errorf("retiredAuthMounts: %q is the JWT mount this component manages for cluster %q: converge would enable it and then disable it. To drop a cluster's mount, remove the cluster from access; retiredAuthMounts is for mounts the component never wrote", m.Name, mountCluster(managed, m.Name)))
		}
	}
	return errors.Join(errs...)
}

func mountCluster(managed []Mount, name string) string {
	for _, m := range managed {
		if m.Name == name {
			return m.Cluster
		}
	}
	return ""
}
