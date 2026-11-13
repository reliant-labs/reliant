package obsbuiltin

import "github.com/reliant-labs/forge/pkg/dashspec"

// The product-analytics declarations the built-in dashboards read
// (forge.Analytics in KCL, merged across the project's frontends by forge
// generate). The dashboards draw the defaults below when nothing is declared,
// so a nil *Analytics is a complete answer, not a missing one.

// Funnel identities that count over the hosted product_events table rather
// than over browser spans. A funnel counted by one of them is drawn on the
// built-in Product dashboard, never as a `funnel` panel on the frontend
// dashboard (dashspec's funnel panel counts sessions and users only).
const (
	// FunnelByVisitor counts the cookieless daily visitor (DayVisitor): one
	// visitor per UTC day, so its window is at most a day.
	FunnelByVisitor dashspec.FunnelBy = "visitor"
	// FunnelByPerson counts the person key: the signed-in user, else the
	// same-day visitor one sign-in links to them, else the visitor-day.
	FunnelByPerson dashspec.FunnelBy = "person"
)

// IsProductFunnel reports whether a funnel counted by `by` belongs to the
// built-in Product dashboard rather than the frontend dashboard.
func IsProductFunnel(by dashspec.FunnelBy) bool {
	return by == FunnelByVisitor || by == FunnelByPerson
}

// DefaultActivationWindow is how long after signing up a user has to activate
// when no frontend declares Analytics.activation_window.
const DefaultActivationWindow = "7d"

// Analytics is the project's declared product analytics.
type Analytics struct {
	// Goals are the declared conversions, sorted by id.
	Goals []Goal `json:",omitempty"`
	// Activation is the product event that marks a user activated. Empty: the
	// user's first <entity>_created event.
	Activation string `json:",omitempty"`
	// ActivationWindow is how long after signing up a user has to activate
	// ("7d"). Empty: DefaultActivationWindow.
	ActivationWindow string `json:",omitempty"`
	// Channels are the custom channel rules, in the order they are tried,
	// ahead of the built-in channel table.
	Channels []ChannelRule `json:",omitempty"`
	// NoDashboards drops the built-in Overview sections and the Users,
	// Product and Acquisition dashboards (a frontend declared
	// Analytics.dashboards = False).
	NoDashboards bool `json:",omitempty"`
}

// Goal is one declared conversion (forge.Goal): exactly one of Event and
// Route is set. A Route goal matches page_viewed's Props['route'].
type Goal struct {
	ID    string
	Title string `json:",omitempty"`
	Event string `json:",omitempty"`
	Route string `json:",omitempty"`
}

// ChannelRule is one custom channel rule (forge.Channel). A visit is credited
// to Channel when its first page view matches every condition set: Host is
// the referrer host or any subdomain of it, Source and Medium the utm_source
// and utm_medium. Conditions are lowercase and match case-insensitively.
type ChannelRule struct {
	Host    string `json:",omitempty"`
	Source  string `json:",omitempty"`
	Medium  string `json:",omitempty"`
	Channel string
}

// Funnel is one declared funnel (forge.Funnel in KCL). The Product dashboard
// draws the ones counted by FunnelByVisitor or FunnelByPerson.
type Funnel struct {
	ID    string
	Title string
	dashspec.Funnel
}

// EffectiveActivationWindow is the declared window, else the default.
func (a *Analytics) EffectiveActivationWindow() string {
	if a == nil || a.ActivationWindow == "" {
		return DefaultActivationWindow
	}
	return a.ActivationWindow
}

// DashboardsEnabled reports whether the built-in product dashboards are drawn.
func (a *Analytics) DashboardsEnabled() bool {
	return a == nil || !a.NoDashboards
}
