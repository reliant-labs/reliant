package sbom

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// MaxProvidedSize bounds an SBOM a pipeline hands forge: the size the
// control plane accepts for one uploaded SBOM. A real one is 300 KB to a few
// MB; anything near this is not an SBOM of one artifact.
const MaxProvidedSize = 32 << 20

// Document is what [Identify] reads from an SBOM it did not generate.
type Document struct {
	// Format is the recorded format: `cyclonedx-json@<specVersion>` or
	// `spdx-json@<version>` (`spdx-json@2.3`).
	Format string
	// Generator is the tool the document says produced it (`syft@1.57.2`,
	// `syft-1.57.2`), "" when it names none.
	Generator string
}

var (
	cycloneDXSpec = regexp.MustCompile(`^1\.[0-9]+$`)
	spdxVersion   = regexp.MustCompile(`^SPDX-(2\.[0-9]+)$`)
)

// Identify checks that data is an SBOM forge can record and scan, a
// CycloneDX JSON or SPDX 2.x JSON document, and names its format. It reads
// only the header: whether the components inside are right is the
// producer's claim, which the record keeps verbatim (its digest is of the
// bytes as given).
func Identify(data []byte) (Document, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return Document{}, fmt.Errorf("the file is empty")
	}
	if len(data) > MaxProvidedSize {
		return Document{}, fmt.Errorf("the file is %d bytes, over the %d MiB an SBOM may be", len(data), MaxProvidedSize>>20)
	}
	var head struct {
		BOMFormat    string          `json:"bomFormat"`
		SpecVersion  string          `json:"specVersion"`
		SPDXVersion  string          `json:"spdxVersion"`
		Metadata     json.RawMessage `json:"metadata"`
		CreationInfo struct {
			Creators []string `json:"creators"`
		} `json:"creationInfo"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return Document{}, fmt.Errorf("it is not a JSON document (%v); pass the JSON form, e.g. `syft -o cyclonedx-json` or `-o spdx-json`", err)
	}
	switch {
	case head.BOMFormat == "CycloneDX":
		if !cycloneDXSpec.MatchString(head.SpecVersion) {
			return Document{}, fmt.Errorf("it is CycloneDX with specVersion %q, which is not a CycloneDX 1.x version", head.SpecVersion)
		}
		return Document{Format: "cyclonedx-json@" + head.SpecVersion, Generator: cycloneDXTool(head.Metadata)}, nil
	case head.SPDXVersion != "":
		m := spdxVersion.FindStringSubmatch(head.SPDXVersion)
		if m == nil {
			return Document{}, fmt.Errorf("it is SPDX %q; forge records SPDX 2.x JSON", head.SPDXVersion)
		}
		var gen string
		for _, c := range head.CreationInfo.Creators {
			if tool, ok := strings.CutPrefix(strings.TrimSpace(c), "Tool:"); ok {
				gen = strings.TrimSpace(tool)
				break
			}
		}
		return Document{Format: "spdx-json@" + m[1], Generator: gen}, nil
	}
	return Document{}, fmt.Errorf("it is neither a CycloneDX JSON document (bomFormat) nor an SPDX JSON one (spdxVersion)")
}

// cycloneDXTool is the first tool a CycloneDX document's metadata names, as
// `name@version`. CycloneDX 1.5 moved tools from a list to
// {components, services}; both spellings are read.
func cycloneDXTool(metadata json.RawMessage) string {
	type tool struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	var meta struct {
		Tools json.RawMessage `json:"tools"`
	}
	if len(metadata) == 0 || json.Unmarshal(metadata, &meta) != nil || len(meta.Tools) == 0 {
		return ""
	}
	var list []tool
	if json.Unmarshal(meta.Tools, &list) != nil {
		var obj struct {
			Components []tool `json:"components"`
			Services   []tool `json:"services"`
		}
		if json.Unmarshal(meta.Tools, &obj) != nil {
			return ""
		}
		list = append(list, obj.Components...)
		list = append(list, obj.Services...)
	}
	for _, t := range list {
		if name := strings.TrimSpace(t.Name); name != "" {
			if v := strings.TrimSpace(t.Version); v != "" {
				return name + "@" + v
			}
			return name
		}
	}
	return ""
}
