package config

import (
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// VulnConfig is the `vuln:` block: how forge keeps the SBOM it records for
// each release artifact, and how it handles the vulnerabilities it finds in
// them. Every key is optional, and an absent block is the default — the SBOM
// goes everywhere it can.
type VulnConfig struct {
	// AttachSBOM says whether the release cut attaches each OCI artifact's
	// SBOM (an image, a hosted static site) to it in its registry, as an OCI
	// 1.1 referrer (with the `sha256-<hex>` tag fallback where the registry
	// has no referrers API). Unset means true.
	//
	// `false` turns attachment off — for a registry that refuses unknown
	// artifact types, or one whose cleanup policy deletes untagged manifests
	// (a referrer is one). The SBOM is still recorded: the release keeps its
	// digest, and the machine ledger (and a control plane, when the env
	// records on one) keeps its bytes.
	//
	//	vuln:
	//	  attach_sbom: false
	AttachSBOM *bool `yaml:"attach_sbom,omitempty"`
	// Patch steers `forge vuln patch`. See [VulnPatchConfig].
	Patch VulnPatchConfig `yaml:"patch,omitempty"`
}

// VulnPatchConfig is `vuln.patch:`.
type VulnPatchConfig struct {
	// Verify is the command `forge vuln patch --verify` runs (with `sh -c`,
	// in the project) to prove a patched tree still builds and passes its
	// tests. Unset, the patcher uses the project's own gate: `task
	// test:short` when the Taskfile has it, else `go build ./... && go test
	// -short ./...` for a Go module, else the package manager's `test`
	// script. `--verify=<cmd>` overrides both.
	//
	//	vuln:
	//	  patch:
	//	    verify: task test:short && task lint
	Verify string `yaml:"verify,omitempty"`
}

// ReadVulnConfig reads only the `vuln:` block of the forge.yaml in
// projectDir, for verbs that hold no loaded project (the release cut,
// `forge vuln patch`). A missing or unreadable file is the zero value (every
// default on); an invalid one is left to the full loader to report.
func ReadVulnConfig(projectDir string) VulnConfig {
	data, err := os.ReadFile(filepath.Join(projectDir, "forge.yaml"))
	if err != nil {
		return VulnConfig{}
	}
	var doc struct {
		Vuln VulnConfig `yaml:"vuln"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return VulnConfig{}
	}
	return doc.Vuln
}

// AttachSBOMEnabled reports whether OCI artifacts get their SBOM attached as
// a referrer: true unless forge.yaml says `vuln.attach_sbom: false`.
func (v VulnConfig) AttachSBOMEnabled() bool {
	return v.AttachSBOM == nil || *v.AttachSBOM
}
