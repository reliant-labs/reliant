package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/config"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

func (g *ProjectGenerator) writeProjectConfig() error {
	// Persist `binary:` only when explicitly opted-in (shared) so existing
	// forge.yaml files keep their cleaner shape and the field is omitted by
	// default. EffectiveBinary on the read-side defaults this to per-service.
	binaryYAML := ""
	if g.isBinaryShared() {
		binaryYAML = config.ProjectBinaryShared
	}

	// The scaffolded forge.yaml is MINIMAL and PROJECT-GLOBAL only: identity
	// (name/module), the forge version pin, and the harness. What the project
	// CONTAINS is not written here at all — the scaffold writes the code
	// (protos, handlers, cmd/, db/migrations, frontends/, deploy/kcl) and
	// every later read derives the components, features, frontends, database
	// and kind from that tree. The ci/lint/contracts/deploy/docker/k8s
	// sections are derived from the project shape at load time
	// (config.ApplyDerivedDefaults); they remain valid in forge.yaml as
	// overrides, just not required boilerplate.
	cfg := config.ProjectConfig{
		Name:         g.Name,
		ModulePath:   g.ModulePath,
		Binary:       binaryYAML,
		ForgeVersion: g.recordedForgeVersion(),
		// The harness choice has to survive `forge project new`: every later
		// `forge generate` decides whether to deliver on-disk skills from it,
		// and the flag is gone by then. Written for EVERY harness including
		// the default — an explicit `harness: reliant` is what lets the
		// skills emitter tell "chose the default" apart from "predates the
		// field" (see ProjectConfig.Harness).
		Harness: string(g.Harness.Normalized()),
		// The typed-access guardrail is written explicitly either way:
		// "error" for greenfield, "warn" for a project adopted in place,
		// whose existing env reads would otherwise fail the first lint
		// (scaffoldTypedAccess). An explicit value survives
		// NormalizeForWrite (it is not a section default), so the `config:`
		// block always renders and records the choice where the user flips
		// it.
		Config: config.ConfigGuardConfig{
			EnforceTypedAccess: g.scaffoldTypedAccess(),
		},
		// Only the pins serialize (`project new --disable`); the derived
		// state the scaffold describes itself with is not a yaml field.
		Features: g.Features,
	}

	// Kind sync: cfg.Kind is set from the requested kind so NormalizeForWrite's
	// section defaults (which read kind) drop the right values. It is not
	// serialized — the reader derives kind from the tree this scaffold is
	// writing.
	cfg.Kind = g.effectiveKind()

	// Normalize before marshalling: section values that match the
	// shape-derived defaults are dropped, so what hits disk is only identity
	// + explicit user choices.
	data, err := yaml.Marshal(config.NormalizeForWrite(&cfg))
	if err != nil {
		return fmt.Errorf("marshal project config: %w", err)
	}

	// Prepend a short header. The file is intentionally minimal: everything
	// else is derived from the repo at load time and only needs to appear
	// here when overriding a default. Authentication is OWNED CODE, not
	// config: it is internal/app/auth.go's SetupAuth.
	header := []byte("# Forge project manifest — see https://github.com/reliant-labs/forge.\n" +
		"# This file is minimal on purpose. Features, the database, the frontends and\n" +
		"# the project kind are derived from what exists in the repo (db/migrations,\n" +
		"# frontends/, deploy/kcl, internal/operators, ...), not configured here; a\n" +
		"# `features:` block pins one on or off only when the derived default is wrong.\n" +
		"# Authentication is owned code — internal/app/auth.go's SetupAuth.\n" +
		"# Add a section (ci, lint, contracts, ...) only to override a default\n" +
		"# (`forge skill load architecture` documents the schema;\n" +
		"# `forge skill list` indexes the per-topic skills, e.g. `auth`).\n\n")
	data = append(header, data...)

	destPath := filepath.Join(g.Path, "forge.yaml")
	if err := atomicfile.WriteFile(destPath, data, 0644); err != nil {
		return err
	}

	return nil
}

// recordedForgeVersion is the forge_version a scaffolded forge.yaml records:
// the pin of the module being adopted when there is one (ForgePin), else the
// binary doing the scaffolding.
//
// The adopted pin wins because forge_version is a projection of go.mod's forge
// require (internal/cli/generate_forge_pin.go). An in-place scaffold keeps the
// user's go.mod, so stamping this binary instead wrote a forge.yaml that
// disagreed with the go.mod beside it from birth — reliant got
// v0.1.44-0.20261010080729-… over a go.mod pinning
// v0.1.44-0.20261007193810-e75c74afb46e, and the generate that would have
// converged it refused the project before reaching that step.
func (g *ProjectGenerator) recordedForgeVersion() string {
	if g.ForgePin != "" {
		return g.ForgePin
	}
	return buildinfo.Version()
}

// requiredForgeVersion is the forge version a scaffolded go.mod and
// gen/go.mod require: the adopted pin when there is one, else
// resolveForgeVersion().
func (g *ProjectGenerator) requiredForgeVersion() string {
	if g.ForgePin != "" {
		return g.ForgePin
	}
	return resolveForgeVersion()
}

// resolveBinaryName returns the primary binary name (the cmd/<bin>/ leaf the
// command tree lives under) for the project at projectDir. It is the
// forge.yaml project name VERBATIM (hyphens preserved), falling back to the
// project directory's base name when the config is unreadable — mirroring
// ProjectGenerator.binaryName() so the upgrade / regenerate lane addresses the
// SAME cmd/<bin>/ dir the scaffold wrote (a project named "peptide-platform"
// lives at cmd/peptide-platform/).
func resolveBinaryName(projectDir string) string {
	cfg, err := ReadProjectConfig(filepath.Join(projectDir, "forge.yaml"))
	if err == nil && cfg != nil && cfg.Name != "" {
		return cfg.Name
	}
	return filepath.Base(projectDir)
}

// ReadProjectConfig reads a forge.yaml from the given path with strict
// validation: unknown keys, missing required fields, and type mismatches
// are surfaced together via config.ValidationError rather than failing
// fast on the first issue. forge.yaml is project-global only: what the
// project CONTAINS is read from the code that declares it
// (codegen.DiscoverProjectComponents), and the project kind is derived from
// the tree beside forge.yaml (see config.LoadProject). See config.LoadStrict
// for the full validation semantics.
func ReadProjectConfig(path string) (*config.ProjectConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read project config: %w", err)
	}
	// The path is load-bearing: LoadProject derives the kind from the real
	// sources sitting beside this forge.yaml (see config.LoadProject).
	return config.LoadProject(data, path)
}

// ─────────────────────────────────────────────────────────────────────────────
// WRITING forge.yaml
//
// There used to be a WriteProjectConfigFile(cfg, path) here that marshalled
// the whole config struct over the file. It is GONE, and nothing should
// reintroduce it, because "serialize the struct" is not an available
// operation on this file.
//
// forge.yaml is hand-authored. A config.ProjectConfig has no field for a
// comment, no memory of the author's key order, and — after
// config.NormalizeForWrite — no field for a section whose values happen to
// match what forge would derive anyway. So marshalling one back is a
// REWRITE, not an update, and it silently destroys everything in those three
// categories. Run against forge's own manifest it turned 84 lines carrying
// 40 comment lines into 21 lines carrying none, dropped the ci: and lint:
// blocks whole, and cut features: from ten flags to one. Every one of those
// losses re-derives to the same semantics on load, which is exactly why two
// commands shipped doing it and no test ever went red.
//
// Both callers changed a handful of fields, which is what this file offers
// instead — each writes only the bytes it owns:
//
//	SetProjectConfigScalar      one top-level scalar
//	SetProjectConfigScalarPath  one nested scalar, creating blocks as needed
//
// The one lane that may legitimately write a whole document is the scaffold,
// where the file does not exist yet and there is no user content to lose.
// That lane is ProjectGenerator.writeProjectConfig at the top of this file,
// and it marshals its own freshly-built struct — it never reads from disk,
// so it cannot destroy anything.

// SetProjectConfigScalar sets a single top-level scalar key in the forge.yaml
// at path, rewriting ONLY the bytes that hold that value and leaving the rest
// of the document untouched — comments, key order, blank lines, quoting style
// and any key the Go struct does not model all survive byte-for-byte.
//
// This is the write path for "change one field on a file the user owns". It
// exists because forge.yaml is a hand-editable manifest, not a serialization
// of a Go value. See WriteProjectConfigFile for what the whole-struct
// alternative destroys.
//
// The key is LOCATED through yaml.Node rather than by pattern-matching text,
// so a same-named key nested inside another block, or one mentioned in a
// comment, is not mistaken for the top-level one. Only the located value's
// own bytes are replaced; a trailing end-of-line comment on that line is
// preserved with its original spacing.
//
// If the key is absent it is appended to the document. If the value is
// already correct the file is not written at all.
func SetProjectConfigScalar(path, key string, value any) error {
	return SetProjectConfigScalarPath(path, []string{key}, value)
}

// SetProjectConfigScalarPath is SetProjectConfigScalar for a nested key: it
// sets the scalar at keys (e.g. {"ci","provider"}) in the forge.yaml at path, rewriting only
// that value's own bytes.
//
// Blocks along the path that do not exist are created — appended at the end
// of their parent, so nothing already in the file is shifted or reindented.
// A path whose parent exists but is not a mapping is an error rather than a
// silent overwrite of whatever the user put there.
func SetProjectConfigScalarPath(path string, keys []string, value any) error {
	if len(keys) == 0 {
		return fmt.Errorf("project config %s: no key given", path)
	}
	label := strings.Join(keys, ".")

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read project config: %w", err)
	}

	// Render the value the way YAML wants it, so a version-shaped string
	// stays plain while `yes`, `1.0`, `` and `*star` get the quoting that
	// makes them re-read as the value they are.
	rendered, err := renderScalar(value)
	if err != nil {
		return fmt.Errorf("project config %s: %s: %w", path, label, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse project config: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return fmt.Errorf("project config %s: expected a YAML document", path)
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("project config %s: expected top-level mapping", path)
	}

	// Walk as far down the path as the document already goes.
	node := root
	for depth, k := range keys[:len(keys)-1] {
		child := mappingValue(node, k)
		if child == nil {
			// The rest of the path does not exist — write the whole
			// remaining chain as one new block.
			return insertProjectConfigBlock(path, raw, node, keys[depth:], rendered)
		}
		if child.Kind != yaml.MappingNode {
			return fmt.Errorf("project config %s: %s is not a block",
				path, strings.Join(keys[:depth+1], "."))
		}
		node = child
	}

	leaf := keys[len(keys)-1]
	valueNode := mappingValue(node, leaf)
	if valueNode == nil {
		return insertProjectConfigBlock(path, raw, node, keys[len(keys)-1:], rendered)
	}
	if valueNode.Kind != yaml.ScalarNode {
		return fmt.Errorf("project config %s: %s is not a scalar", path, label)
	}
	if valueNode.Value == strings.TrimSpace(rendered) || valueNode.Value == fmt.Sprint(value) {
		return nil // Already correct — do not touch the file.
	}

	lines := strings.Split(string(raw), "\n")
	idx := valueNode.Line - 1
	if idx < 0 || idx >= len(lines) {
		return fmt.Errorf("project config %s: %s reported line %d, outside the file", path, label, valueNode.Line)
	}
	line := lines[idx]

	// Node columns are 1-based and counted in characters, so index by rune.
	runes := []rune(line)
	start := valueNode.Column - 1
	if start < 0 || start > len(runes) {
		return fmt.Errorf("project config %s: %s reported column %d on a %d-character line",
			path, label, valueNode.Column, len(runes))
	}

	// Everything from the value's first character to end of line is the
	// value's own text, EXCEPT a trailing comment — which is content the
	// user wrote and must come back with its original spacing intact.
	tail := ""
	if valueNode.LineComment != "" {
		if at := strings.LastIndex(line, valueNode.LineComment); at >= 0 {
			commentStart := len([]rune(line[:at]))
			if commentStart >= start {
				// Keep the run of whitespace the author put before the '#'.
				gap := commentStart
				for gap > start && (runes[gap-1] == ' ' || runes[gap-1] == '\t') {
					gap--
				}
				tail = string(runes[gap:])
			}
		}
	}

	lines[idx] = string(runes[:start]) + rendered + tail
	return atomicfile.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)
}

// renderScalar encodes value as a single-line YAML scalar with whatever
// quoting the encoder decides it needs.
func renderScalar(value any) (string, error) {
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode value: %w", err)
	}
	rendered := strings.TrimSuffix(string(encoded), "\n")
	if strings.Contains(rendered, "\n") {
		return "", fmt.Errorf("value does not render as a single-line scalar")
	}
	return rendered, nil
}

// mappingValue returns the value node for key in a mapping node, or nil.
// Mapping nodes store keys and values as alternating children.
func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Kind == yaml.ScalarNode && mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// insertProjectConfigBlock writes the nested chain keys (with rendered as the
// innermost value) into parent, which is a mapping already in the document.
//
// The insertion point is the end of parent's existing entries, found from the
// line/column the parser reported for parent's last child. Appending there
// rather than rewriting parent is what keeps every line above it — comments
// included — exactly as the user left it.
func insertProjectConfigBlock(path string, raw []byte, parent *yaml.Node, keys []string, rendered string) error {
	lines := strings.Split(string(raw), "\n")

	// Indentation and insert position. A parent with no children at all is
	// the document root of an empty file; otherwise indent one step in from
	// the parent's own keys.
	indent := 0
	insertAt := len(lines)
	if len(parent.Content) >= 2 {
		firstKey := parent.Content[0]
		indent = firstKey.Column - 1
		insertAt = blockEndLine(parent)
	}

	// Build the chain: each level indents one 4-space step further, matching
	// the encoder's own default and the shape of every forge.yaml on disk.
	block := make([]string, 0, len(keys))
	for depth, k := range keys[:len(keys)-1] {
		block = append(block, strings.Repeat(" ", indent+depth*4)+k+":")
	}
	block = append(block,
		strings.Repeat(" ", indent+(len(keys)-1)*4)+keys[len(keys)-1]+": "+rendered)

	if insertAt > len(lines) {
		insertAt = len(lines)
	}
	out := make([]string, 0, len(lines)+len(block))
	out = append(out, lines[:insertAt]...)
	out = append(out, block...)
	out = append(out, lines[insertAt:]...)
	return atomicfile.WriteFile(path, []byte(strings.Join(out, "\n")), 0644)
}

// blockEndLine returns the 0-based line index just past the last line any
// descendant of mapping occupies — the point at which a new sibling entry can
// be inserted without landing inside a nested block.
func blockEndLine(mapping *yaml.Node) int {
	last := 0
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil {
			return
		}
		if n.Line > last {
			last = n.Line
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(mapping)
	return last // 1-based last line == 0-based index of the line after it
}
