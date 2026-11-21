package toolfetch

import "github.com/reliant-labs/forge/pkg/toolpins"

// Syft is the SBOM generator forge pins (toolpins.Syft).
var Syft = fromPin(toolpins.Syft())

// Grype is the vulnerability scanner forge pins (toolpins.Grype). grype
// publishes no windows/arm64 or linux/riscv64 build: there forge falls back
// to `go install` (see Fetcher.Ensure).
var Grype = fromPin(toolpins.Grype())

// Tools is every tool this package pins, in the order `forge tools pins` and
// `forge doctor` list them.
func Tools() []Tool { return []Tool{Syft, Grype} }

// fromPin is the Tool for a public pin: pkg/toolpins is the one copy of the
// versions, the archive sums and the executable sums (and records how they
// were made); this package only installs them.
func fromPin(p toolpins.Tool) Tool {
	t := Tool{
		Name: p.Name, Version: p.Version, Repo: p.Repo, GoPackage: p.GoPackage,
		Checksums: map[string]string{}, BinaryChecksums: map[string]string{},
	}
	for _, a := range p.Archives {
		t.Checksums[a.Name] = a.SHA256
		t.BinaryChecksums[a.Name] = a.BinarySHA256
	}
	return t
}
