package assets

import (
	"embed"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/templates"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// EmbeddedFiles bundles the proto annotation definitions shipped
// with the forge binary so they can be vendored into scaffolded
// projects without a network fetch.
//
// buf/validate/validate.proto is protovalidate's option-definition file
// (buf.build/bufbuild/protovalidate). Vendoring it locally — exactly as
// forge/v1/forge.proto is vendored — keeps the no-BSR-dep property: a
// project can use `[(buf.validate.field)...]` field rules and still
// `forge generate` offline, with no `buf registry login`. It is a buf
// COMPILE input only; buf.gen excludes it from Go/TS output and the
// runtime library (buf.build/go/protovalidate, pulled in by forge/pkg)
// provides the extension types + the validation interceptor.
//
//go:embed proto/forge/v1/forge.proto
//go:embed proto/buf/validate/validate.proto
var EmbeddedFiles embed.FS

// WriteTemplate writes a project template to the specified path.
func WriteTemplate(templateName, destPath string) error {
	content, err := templates.ProjectTemplates().Get(templateName)
	if err != nil {
		return err
	}
	return writeFile(destPath, content)
}

// WriteTemplateWithData writes a project template with data substitution.
func WriteTemplateWithData(templateName, destPath string, data any) error {
	content, err := templates.ProjectTemplates().Render(templateName, data)
	if err != nil {
		return err
	}
	return writeFile(destPath, content)
}

// GetForgeV1Proto returns the unified forge/v1/forge.proto file.
func GetForgeV1Proto() ([]byte, error) {
	return EmbeddedFiles.ReadFile("proto/forge/v1/forge.proto")
}

// ForgeProtoImportPath is the import path a project's protos use to pull in
// forge's annotations: `import "forge/v1/forge.proto";`.
const ForgeProtoImportPath = "forge/v1/forge.proto"

// ForgeProtoVendorRelPath is where the vendored forge.proto lives inside
// a scaffolded project, relative to the project root.
const ForgeProtoVendorRelPath = "proto/forge/v1/forge.proto"

// VendoredProtoRelPaths is the complete set of files forge COPIES into a
// project verbatim, rather than rendering from a template — the two
// embedded protos above, at the project-relative paths scaffold writes
// them to.
//
// It is a declared list because these copies are otherwise UNTRACKED:
// they are not templates, not Tier-1 (a forge:hash marker is a comment,
// and a comment here would be vendored into every project along with the
// file), and absent from .forge/hashes.json — so forge's upgrade path is
// blind to them and a stale copy diverges silently. Two features depend
// on knowing exactly which files those are, and MUST agree:
//
//   - `forge lint --vendored-protos` reports drift against the embedded
//     copy (internal/cli/lint/lint_vendored_protos.go).
//   - `forge project disown` accepts these paths despite the missing
//     marker, so the lint's escape hatch actually opens.
//
// Adding a go:embed'd proto above without adding it here reintroduces the
// blind spot; a test in internal/cli/lint pins the list against the
// embedded FS so that cannot happen quietly.
var VendoredProtoRelPaths = []string{
	ForgeProtoVendorRelPath,
	ValidateProtoVendorRelPath,
}

// IsVendoredProtoRelPath reports whether relPath (slash-separated,
// project-relative) is one of forge's vendored copies.
func IsVendoredProtoRelPath(relPath string) bool {
	for _, p := range VendoredProtoRelPaths {
		if p == relPath {
			return true
		}
	}
	return false
}

// ValidateProtoImportPath is the import path a project's protos use to
// pull in protovalidate's field rules: `import "buf/validate/validate.proto";`.
const ValidateProtoImportPath = "buf/validate/validate.proto"

// ValidateProtoVendorRelPath is where the vendored validate.proto lives
// inside a scaffolded project, relative to the project root. buf resolves
// the import from here because it sits under the `proto` module path.
const ValidateProtoVendorRelPath = "proto/buf/validate/validate.proto"

// GetValidateProto returns the vendored buf/validate/validate.proto.
func GetValidateProto() ([]byte, error) {
	return EmbeddedFiles.ReadFile("proto/buf/validate/validate.proto")
}

// WriteValidateProto writes the vendored buf/validate/validate.proto into
// destDir/validate.proto (destDir being <project>/proto/buf/validate).
// Unlike forge.proto, the go_package is left untouched: buf.gen excludes
// this path from every language's output, so no Go/TS is ever emitted
// from it — the protovalidate runtime library supplies the types.
func WriteValidateProto(destDir string) error {
	content, err := GetValidateProto()
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(destDir, "validate.proto"), content)
}

// goPackageOptionRE matches the file-level go_package option line of the
// embedded forge.proto regardless of what path it currently declares.
// WriteForgeV1Proto must not depend on the exact embedded value: a stale
// literal-match here is exactly how scaffolds historically shipped a
// forge.proto pointing at `github.com/reliant-labs/forge/gen/...` — a
// module that does not exist — leaving every generated *.pb.go in the
// project with an unresolvable import.
var goPackageOptionRE = regexp.MustCompile(`(?m)^option go_package = "[^"]*";$`)

// forgePBGoPackage is the FIXED go_package that scaffolded projects' vendored
// forge.proto must declare. It points at forge's shared, pre-generated
// forgepb package rather than a project-local gen/forge/v1 copy.
//
// "Path A" proto unification: forge.proto registers the descriptor file
// "forge/v1/forge.proto" at init. If a project generated its OWN copy into
// gen/forge/v1 AND linked forge/pkg/forgepb (which generates the identical
// descriptor), both copies register the same file and every binary panics:
//
//	proto: file "forge/v1/forge.proto" is already registered
//
// Pointing go_package at forgepb means the project's buf pipeline emits no
// local copy (the path is excluded from Go output in buf.gen.yaml) and every
// other generated *.pb.go blank-imports the shared forgepb — single
// registration, both binaries boot.
const forgePBGoPackage = `option go_package = "github.com/reliant-labs/forge/pkg/forgepb;forgepb";`

// WriteForgeV1Proto writes the unified forge.proto into destDir/forge.proto,
// rewriting the go_package option to point at forge's shared forgepb package.
//
// Rationale: scaffolded projects vendor forge.proto into proto/forge/v1/ as a
// buf COMPILE input (other protos import its annotations), but they do NOT
// generate a local Go copy — buf.gen.yaml excludes forge/v1 from output and
// the project links forge/pkg/forgepb instead. The fixed go_package below is
// what makes the cross-file blank-imports in the generated *.pb.go resolve to
// that shared package.
func WriteForgeV1Proto(destDir string) error {
	content, err := GetForgeV1Proto()
	if err != nil {
		return err
	}

	adjusted := goPackageOptionRE.ReplaceAllString(string(content), forgePBGoPackage)

	destPath := filepath.Join(destDir, "forge.proto")
	return writeFile(destPath, []byte(adjusted))
}

func writeFile(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicfile.WriteFile(path, content, 0o644)
}

// NormalizeVendoredProto canonicalizes a vendored proto for comparison with
// the copy embedded in this binary — the one comparison `forge generate`
// (which refreshes a stale copy) and `forge lint --vendored-protos` (which
// reports one) both make, so the two cannot disagree about what "stale" is.
//
// The go_package line is dropped from both sides: WriteForgeV1Proto rewrites
// it on purpose, so it differs in every project, and the value forge writes
// has itself changed across versions. CRLF and trailing whitespace are
// normalized too, and trailing blank lines dropped: a checkout artifact is not
// a divergence in what the file declares.
func NormalizeVendoredProto(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if strings.HasPrefix(strings.TrimSpace(trimmed), "option go_package") {
			continue
		}
		out = append(out, trimmed)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}
