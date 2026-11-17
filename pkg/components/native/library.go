// Copyright (c) 2025 Reliant Labs
//
// Package native embeds the React Native component primitives that ship
// in `@<scope>/ui-native`. Distinct from the sibling `components`
// package which embeds the web (Tailwind/DOM) library — RN primitives
// are useless on the web (Tailwind class names mean nothing to a
// <View>) and DOM components are useless on RN, so the two libraries
// stay in separate packages with no overlap in registry.
//
// Surface is deliberately small (~10 primitives). For anything bigger
// (data tables, sidebars, complex layouts) the answer on native is a
// real design system — Tamagui or Unistyles — not more primitives
// hand-rolled here. See the `ui-native-package` skill for the
// reasoning.
package native

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed components/*.tsx tokens.ts
var componentsFS embed.FS

// Primitive describes a single React Native primitive shipped by the
// library. Mirrors the shape of components.Entry but with a flatter
// schema — there are no categories yet, only primitives.
type Primitive struct {
	// Name is the kebab-cased file stem (e.g. "button", "safe-area-view").
	Name string `json:"name"`
	// Description is a one-line summary for documentation generation.
	Description string `json:"description"`
	// FilePath is the embed-relative path to the .tsx source.
	FilePath string `json:"-"`
}

// primitives is the canonical list of RN primitives. Order is the
// order they appear in the documentation and the order they are written
// to packages/ui-native/src/components/ during scaffold.
var primitives = []Primitive{
	{Name: "button", Description: "Pressable wrapped with variant/size/loading props mirroring the web Button."},
	{Name: "input", Description: "TextInput wrapper with optional label + error row."},
	{Name: "label", Description: "Standalone Text label primitive — pair with non-Input controls."},
	{Name: "card", Description: "Bordered, rounded surface with iOS shadow + Android elevation."},
	{Name: "stack", Description: "Row/column layout with consistent gap from the spacing scale (also exports HStack/VStack)."},
	{Name: "text", Description: "Text wrapper with size/weight/tone tokens — mirrors the web Text taxonomy."},
	{Name: "spinner", Description: "ActivityIndicator wrapper that picks up the active palette's primary color."},
	{Name: "switch", Description: "RN Switch wrapper with palette-aware track + thumb colors."},
	{Name: "pressable", Description: "Pressable wrapper with built-in pressed-state opacity feedback."},
	{Name: "safe-area-view", Description: "react-native-safe-area-context SafeAreaView with palette-aware defaults."},
}

// byName provides O(1) lookup.
var byName map[string]*Primitive

func init() {
	byName = make(map[string]*Primitive, len(primitives))
	for i := range primitives {
		p := &primitives[i]
		p.FilePath = fmt.Sprintf("components/%s.tsx", p.Name)
		byName[p.Name] = p
	}
}

// Library is the RN counterpart to components.Library. Kept as a
// distinct type so callers that statically depend on "the native
// library" don't accidentally pick up DOM components.
type Library struct{}

// NewLibrary returns a Library handle. Stateless — the underlying
// registry lives in package globals.
func NewLibrary() *Library {
	return &Library{}
}

// Primitives returns the full list of RN primitives.
func (l *Library) Primitives() []Primitive {
	return primitives
}

// Get returns the source code for a primitive by name.
func (l *Library) Get(name string) (string, error) {
	p, ok := byName[name]
	if !ok {
		return "", fmt.Errorf("native primitive %q not found", name)
	}
	content, err := componentsFS.ReadFile(p.FilePath)
	if err != nil {
		return "", fmt.Errorf("read native primitive %q: %w", name, err)
	}
	return string(content), nil
}

// GetEntry returns the metadata for a primitive by name.
func (l *Library) GetEntry(name string) (*Primitive, bool) {
	p, ok := byName[name]
	return p, ok
}

// Tokens returns the source of `tokens.ts`. Lives alongside the
// primitives in the same embed.FS but isn't a "primitive" in the
// catalogue sense — it's a single sibling file with no variants.
func (l *Library) Tokens() (string, error) {
	content, err := componentsFS.ReadFile("tokens.ts")
	if err != nil {
		return "", fmt.Errorf("read tokens.ts: %w", err)
	}
	return string(content), nil
}

// IndexBarrel renders the canonical `src/index.ts` re-export barrel
// for the package. Generated rather than embedded so the export list
// always stays in lockstep with the primitive registry — adding a new
// primitive to `primitives` automatically picks it up in the barrel.
func (l *Library) IndexBarrel() string {
	var sb strings.Builder
	sb.WriteString("// @<scope>/ui-native — barrel re-export.\n")
	sb.WriteString("//\n")
	sb.WriteString("// AUTO-GENERATED at scaffold time. Components live as one .tsx file\n")
	sb.WriteString("// each under ./components/ and the design tokens under ./tokens.\n")
	sb.WriteString("// Edit the individual files; this barrel is intentionally tiny.\n\n")
	for _, p := range primitives {
		exportName := defaultExportName(p.Name)
		path := fmt.Sprintf("./components/%s", p.Name)
		fmt.Fprintf(&sb, "export { default as %s } from \"%s\";\n", exportName, path)
	}
	// Stack also exports HStack / VStack named exports — surface them
	// alongside the default.
	sb.WriteString("export { HStack, VStack } from \"./components/stack\";\n")
	sb.WriteString("\nexport * from \"./tokens\";\n")
	return sb.String()
}

// defaultExportName turns a kebab-case primitive name into the
// PascalCase symbol the .tsx file default-exports.
func defaultExportName(name string) string {
	parts := strings.Split(name, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "")
}
