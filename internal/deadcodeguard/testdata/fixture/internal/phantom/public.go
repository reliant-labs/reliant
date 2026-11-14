package phantom

// ── Look-alike: embeds a json-tagged struct ──────────────────────────────────
//
// The real MCP tool-input shape (internal/cli benefitsInput): an input struct
// carries no tag of its own and embeds argument structs that do. The decoder
// writes the embedded struct through its promoted, tagged fields.
type wireArgs struct {
	Name string `json:"name"`
}

type ToolInput struct {
	// OK: written by the json decoder through the promoted Name.
	wireArgs
}

// Greeting reads the embedded struct only by promotion.
func (in ToolInput) Greeting() string { return "hello " + in.Name }

// ── Look-alike: re-exported by a public package ──────────────────────────────
//
// The real internal/cli.AccountToolsOptions shape: package pub re-exports
// Options by alias, so its writers are the module's consumers.
type Options struct {
	// OK: pub.Options is public API; a consumer of the module writes it.
	URL string
}

// Dial reads the option a consumer set.
func Dial(o Options) string { return o.URL }
