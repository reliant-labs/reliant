// Copyright (c) 2025 Reliant Labs
package machinepool

import (
	"fmt"
	"regexp"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// What an owner may write on a machine and a pool, held in one place: the
// DaemonRegistryService rpcs (SetMachineLabels, PutMachinePool) and the agent
// tools that raise an approval card before calling them check the same rules,
// so a card is never approved for a change the write then refuses.

// labelSlug is what a label key or value, and a pool name, may be: a short
// slug, so a label reads the same in a selector, a URL and a log line.
var labelSlug = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?$`)

// MaxMachineLabels bounds how many labels one machine carries.
const MaxMachineLabels = 32

// ValidateLabels refuses labels that are not short slugs, or too many of
// them. what names the field in the message ("labels", "selector").
func ValidateLabels(labels map[string]string, what string) error {
	if len(labels) > MaxMachineLabels {
		return svcerr.InvalidArgument(fmt.Sprintf("%s: at most %d labels", what, MaxMachineLabels))
	}
	for k, v := range labels {
		if !labelSlug.MatchString(k) || !labelSlug.MatchString(v) {
			return svcerr.InvalidArgument(fmt.Sprintf("%s: %q=%q: keys and values are lowercase slugs (a-z, 0-9, '.', '_', '-'), at most 63 characters", what, k, v))
		}
	}
	return nil
}

// NormalizeSpec fills a pool definition's defaults (an empty selector is
// {"pool": name}; an empty member_idle_timeout is
// core.DefaultMemberIdleTimeout) and refuses one the registry would not
// store. It changes p in place.
func NormalizeSpec(p *core.MachinePool) error {
	if !labelSlug.MatchString(p.Name) {
		return svcerr.InvalidArgument("pool name: a lowercase slug (a-z, 0-9, '.', '_', '-'), at most 63 characters")
	}
	if len(p.Selector) == 0 {
		p.Selector = DefaultSelector(p.Name)
	}
	if err := ValidateLabels(p.Selector, "selector"); err != nil {
		return err
	}
	if p.MinMembers < 0 || p.MaxMembers < 0 || p.MaxRunsPerMember < 0 {
		return svcerr.InvalidArgument("min_members, max_members and max_runs_per_member cannot be negative")
	}
	if p.MaxMembers > 0 && p.MinMembers > p.MaxMembers {
		return svcerr.InvalidArgument(fmt.Sprintf("min_members (%d) exceeds max_members (%d)", p.MinMembers, p.MaxMembers))
	}
	if p.MemberIdleTimeout == "" {
		p.MemberIdleTimeout = core.DefaultMemberIdleTimeout
	}
	if d, err := time.ParseDuration(p.MemberIdleTimeout); err != nil || d < time.Minute || d > 4*time.Hour {
		return svcerr.InvalidArgument("member_idle_timeout: a duration between 1m and 4h, e.g. \"10m\"")
	}
	return nil
}
