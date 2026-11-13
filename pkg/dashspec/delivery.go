package dashspec

// The delivery kinds (KindReleaseHealth, KindPromotions, KindDelivery) draw
// what a team ships rather than what its services emit: the release ledger and
// the release-health verdict. That data is the control plane's (Postgres, not
// ClickHouse), and each kind names one read the console already makes, so a
// panel carries no query at all. Its scope is the dashboard's: the
// environments the viewer selected (every environment of the project when
// none is), and, for promotions and delivery, the selected time range. A
// service selection does not narrow them: a release, a promotion and DORA are
// an environment's, never one service's. A
// release_health panel ignores the range: it judges each environment's
// running release, over the windows the rule itself plans around its
// promotion.
//
// One kind per read, never one kind with a field that picks the read: a
// panel's query is decided by its kind alone (see Display), so a reader that
// knows the kind knows exactly what to fetch.

// DefaultPromotionsLimit is how many promotions a promotions panel with no
// Limit draws: the newest ones in the range.
const DefaultPromotionsLimit = 50

// IsDeliveryKind reports whether k is one of the kinds drawn from the release
// ledger and release health rather than from telemetry.
func IsDeliveryKind(k PanelKind) bool {
	switch k {
	case KindReleaseHealth, KindPromotions, KindDelivery:
		return true
	}
	return false
}

// deliveryPanel validates a delivery-kind panel: it carries no query, so
// every query field is refused, and a promotions panel alone takes a limit.
// Markdown and link are refused by panel itself, as on every other kind.
func (v *validator) deliveryPanel(path string, o map[string]any, kind PanelKind) {
	v.refuse(path, o, kind, "signal", "series", "ratio", "formula", "where", "groupBy", "columns", "unit",
		"funnel", "hasError", "sql", "trendSql", "postgres", "minStep", "display", "browse")
	v.positiveWidthHeight(path, o)
	if kind != KindPromotions {
		v.refuse(path, o, kind, "limit")
		return
	}
	if l, ok := v.number(path, o, "limit"); ok && (l < 1 || l > MaxLimit || l != float64(int(l))) {
		v.add(childPath(path, "limit"), "must be a whole number of promotions from 1 to %d", MaxLimit)
	}
}

// positiveWidthHeight checks a panel's grid size, as panel does for the kinds
// that reach its common checks.
func (v *validator) positiveWidthHeight(path string, o map[string]any) {
	if w, ok := v.number(path, o, "width"); ok && (w < 0 || w > GridColumns || w != float64(int(w))) {
		v.add(childPath(path, "width"), "must be a whole number of grid columns from 1 to %d", GridColumns)
	}
	if h, ok := v.number(path, o, "height"); ok && (h < 0 || h > maxGridHeight || h != float64(int(h))) {
		v.add(childPath(path, "height"), "must be a whole number of grid rows up to %d", maxGridHeight)
	}
}
