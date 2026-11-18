package phantom

// ── Look-alike: embedded, written only through its promoted fields ───────────
//
// The real obsMetricsQueryOpts.metricsPanelFlags shape. A command's options
// embed a flag struct; the flag set writes `&o.agg` (a promoted field) and the
// command hands `o.panelFlags` to the code that builds the panel. A promoted
// selector passes through the embedded field implicitly, so a write of `o.agg`
// is a write of `o.panelFlags`. Recording only the leaf reported a field that
// every flag writes.
type panelFlags struct {
	agg string
}

type queryOpts struct {
	// OK: written through `&o.agg` in bindQuery.
	panelFlags
	// OK: written by `o.register()`, a pointer-receiver method promoted from it.
	common
}

type common struct {
	// OK: written by register.
	env string
}

func (c *common) register() { c.env = "dev" }

// bindQuery is the flag set: it writes the promoted field by address.
func bindQuery(o *queryOpts, bind func(*string)) {
	bind(&o.agg)
	o.register()
}

// Query reads the embedded struct wholesale, and common through its field.
func Query(bind func(*string)) (string, string) {
	var o queryOpts
	bindQuery(&o, bind)
	return buildPanel(o.panelFlags), o.env
}

func buildPanel(f panelFlags) string { return f.agg }

// ── Planted: embedded, read through its promoted field, written nowhere ──────
type label struct {
	// WANT phantom-field: read through Badge's promotion, written nowhere.
	text string
}

type Badge struct {
	// WANT phantom-field: the promoted read of text reads it; nothing writes it.
	label
}

// Text reads label only by promotion.
func (b Badge) Text() string { return b.text }
