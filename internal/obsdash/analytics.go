package obsdash

import "github.com/reliant-labs/forge/pkg/obsbuiltin"

// The product-analytics declarations the built-in product dashboards read
// (forge.Analytics in KCL, merged across the project's frontends by forge
// generate) live in pkg/obsbuiltin, beside the builders, so the control plane
// can synthesise the same dashboards for an environment with no bundle. These
// aliases keep obsdash's code, and its callers, unqualified.

type (
	// ProductAnalytics is obsbuiltin.Analytics.
	ProductAnalytics = obsbuiltin.Analytics
	// Goal is obsbuiltin.Goal.
	Goal = obsbuiltin.Goal
	// ChannelRule is obsbuiltin.ChannelRule.
	ChannelRule = obsbuiltin.ChannelRule
	// FrontendFunnel is one declared funnel (forge.Funnel in KCL):
	// obsbuiltin.Funnel.
	FrontendFunnel = obsbuiltin.Funnel
)

// Funnel identities that count over the hosted product_events table
// (obsbuiltin.FunnelByVisitor, obsbuiltin.FunnelByPerson).
const (
	FunnelByVisitor = obsbuiltin.FunnelByVisitor
	FunnelByPerson  = obsbuiltin.FunnelByPerson
)

// DefaultActivationWindow is obsbuiltin.DefaultActivationWindow.
const DefaultActivationWindow = obsbuiltin.DefaultActivationWindow

// IsProductFunnel is obsbuiltin.IsProductFunnel.
func IsProductFunnel(by FunnelBy) bool { return obsbuiltin.IsProductFunnel(by) }
