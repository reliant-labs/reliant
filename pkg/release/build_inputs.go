package release

// BuildInputs is a stable recipe/input identity, independent of consumer
// selection. Maps contain non-secret normalized identities, never credentials.
// Dynamic shell/external inputs must set Cacheable=false.
type BuildInputs struct {
	Recipe        string            `json:"recipe"`
	Sources       []Source          `json:"sources"`
	ContextDigest string            `json:"context_digest"`
	RecipeDigest  string            `json:"recipe_digest"`
	ConfigDigest  string            `json:"config_digest"`
	Tools         map[string]string `json:"tools"`
	Platforms     []string          `json:"platforms"`
	Args          map[string]string `json:"args,omitempty"`
	External      map[string]string `json:"external,omitempty"`
	Outputs       []string          `json:"outputs"`
	Cacheable     bool              `json:"cacheable"`
}

// BuildReceipt is produced by the actual builder after exporting pinned inputs.
// WorkingDirectory is attribution only and never substitutes for Inputs.Sources.
type BuildReceipt struct {
	Inputs           BuildInputs       `json:"inputs"`
	InputDigest      string            `json:"input_digest"`
	WorkingDirectory *BuildSource      `json:"working_directory,omitempty"`
	Outputs          map[string]string `json:"outputs"`
}
