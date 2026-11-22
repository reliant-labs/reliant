// Copyright (c) 2025 Reliant Labs
//
// Keyboard shortcuts generator - generates TypeScript and Markdown from YAML source of truth.
//
// SOURCE OF TRUTH: config/shortcuts.yaml
//
// GENERATED FILES:
//   - frontends/web/src/store/shortcutsData.generated.ts
//   - generated/docs-source/settings/keyboard-shortcuts.generated.md
//
// Usage: go run ./tools/docgen/shortcuts <yaml_file> <ts_output> <md_output>

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ShortcutsConfig represents the YAML structure
type ShortcutsConfig struct {
	Categories []string   `yaml:"categories"`
	Shortcuts  []Shortcut `yaml:"shortcuts"`
}

// Shortcut represents a single keyboard shortcut.
//
// Binding is the shortcut on every surface, desktop app and browser tab alike.
// WebBinding overrides it in a browser tab, and is discouraged — see
// config/shortcuts.yaml for the rules. When WebBinding is empty the same chord
// is used on both. DesktopAliases are older chords that also fire in the
// desktop app, so a shortcut can leave a chord the browser reserves without
// breaking desktop muscle memory.
type Shortcut struct {
	ID             string   `yaml:"id"`
	Name           string   `yaml:"name"`
	Description    string   `yaml:"description"`
	Category       string   `yaml:"category"`
	Binding        string   `yaml:"binding"`
	WebBinding     string   `yaml:"web_binding"`
	DesktopAliases []string `yaml:"desktop_aliases"`
	Handler        string   `yaml:"handler"`
	// Context names the focus surface this shortcut belongs to. Defaults to
	// "global" when omitted.
	Context string `yaml:"context"`
	// AllowInInput lets an unmodified binding fire while the user is typing.
	AllowInInput bool `yaml:"allow_in_input"`
	// Passthrough handles the chord without consuming it.
	Passthrough bool `yaml:"passthrough"`
	// DesktopOnly means the shortcut does not exist in a browser tab: it has
	// no web binding, and a user's remap does not apply there either.
	DesktopOnly bool `yaml:"desktop_only"`
}

// Context returns the declared context, defaulting to "global".
func (s Shortcut) EffectiveContext() string {
	if s.Context == "" {
		return "global"
	}
	return s.Context
}

// EffectiveWebBinding returns the browser binding, falling back to the desktop
// one when no override is declared. A desktop-only shortcut has none.
func (s Shortcut) EffectiveWebBinding() string {
	if s.DesktopOnly {
		return ""
	}
	if s.WebBinding == "" {
		return s.Binding
	}
	return s.WebBinding
}

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintf(os.Stderr, "Usage: %s <yaml_file> <ts_output> <md_output>\n", os.Args[0])
		os.Exit(1)
	}

	yamlFile := os.Args[1]
	tsOutput := os.Args[2]
	mdOutput := os.Args[3]

	// Read and parse YAML
	data, err := os.ReadFile(yamlFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading YAML: %v\n", err)
		os.Exit(1)
	}

	var config ShortcutsConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing YAML: %v\n", err)
		os.Exit(1)
	}
	if err := validate(config); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(filepath.Dir(tsOutput), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating TypeScript output directory: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(mdOutput), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating Markdown output directory: %v\n", err)
		os.Exit(1)
	}

	// Generate TypeScript
	ts := generateTypeScript(config)
	if err := os.MkdirAll(filepath.Dir(tsOutput), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating directory for TypeScript output: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(tsOutput, []byte(ts), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing TypeScript: %v\n", err)
		os.Exit(1)
	}

	// Generate Markdown
	md := generateMarkdown(config)
	if err := os.MkdirAll(filepath.Dir(mdOutput), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating directory for Markdown output: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(mdOutput, []byte(md), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing Markdown: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Generated shortcuts: %s (%d shortcuts), %s\n", tsOutput, len(config.Shortcuts), mdOutput)
}

// validate rejects declarations the TypeScript side cannot represent
// faithfully. The chord rules themselves (no hard-reserved binding, no
// conflicts) are enforced by frontends/web/src/lib/keyboard/__tests__/bindings.test.ts,
// which knows the browser's reserved set.
func validate(config ShortcutsConfig) error {
	for _, s := range config.Shortcuts {
		if s.DesktopOnly && s.WebBinding != "" {
			return fmt.Errorf("shortcut %q is desktop_only and also declares a web_binding", s.ID)
		}
		seen := map[string]bool{s.Binding: true}
		for _, alias := range s.DesktopAliases {
			if strings.TrimSpace(alias) == "" {
				return fmt.Errorf("shortcut %q declares an empty desktop alias", s.ID)
			}
			if seen[alias] {
				return fmt.Errorf("shortcut %q repeats %q in desktop_aliases", s.ID, alias)
			}
			seen[alias] = true
		}
	}
	return nil
}

func generateTypeScript(config ShortcutsConfig) string {
	var sb strings.Builder

	sb.WriteString(`// =============================================================================
// GENERATED FILE - DO NOT EDIT DIRECTLY
// =============================================================================
//
// Source of truth: config/shortcuts.yaml
// Generated by: go run ./tools/docgen/shortcuts
// Regenerate with: task generate:docs:shortcuts
//
// This file contains the default keyboard shortcut definitions.
// =============================================================================

import type { ShortcutDefinition } from './shortcutsStore';

/**
 * Default keyboard shortcuts.
 *
 * Bindings are authored strings ("Cmd+Shift+P", "Cmd+K C") rather than
 * pre-resolved modifier objects. Platform folding (Cmd to Meta on macOS, to
 * Control elsewhere) happens at load time in parseBinding(), so one definition
 * serves every platform and sequences stay readable.
 *
 * Categories:
`)

	for _, cat := range config.Categories {
		fmt.Fprintf(&sb, " *   - %s\n", cat)
	}

	sb.WriteString(` */
export const defaultShortcuts: Record<string, Omit<ShortcutDefinition, 'currentBinding'>> = {
`)

	for i, s := range config.Shortcuts {
		fmt.Fprintf(&sb, `  %s: {
    id: '%s',
    name: '%s',
    description: '%s',
    category: '%s',
    defaultBinding: '%s',
    defaultWebBinding: '%s',
    context: '%s',
    handler: '%s'`,
			s.ID, s.ID, escapeJS(s.Name), escapeJS(s.Description), s.Category,
			escapeJS(s.Binding), escapeJS(s.EffectiveWebBinding()),
			s.EffectiveContext(), s.Handler)

		if len(s.DesktopAliases) > 0 {
			quoted := make([]string, len(s.DesktopAliases))
			for i, alias := range s.DesktopAliases {
				quoted[i] = "'" + escapeJS(alias) + "'"
			}
			fmt.Fprintf(&sb, ",\n    desktopAliases: [%s]", strings.Join(quoted, ", "))
		}
		if s.AllowInInput {
			sb.WriteString(",\n    allowInInput: true")
		}
		if s.Passthrough {
			sb.WriteString(",\n    passthrough: true")
		}
		if s.DesktopOnly {
			sb.WriteString(",\n    desktopOnly: true")
		}

		sb.WriteString("\n  }")

		if i < len(config.Shortcuts)-1 {
			sb.WriteString(",")
		}
		sb.WriteString("\n")
	}

	sb.WriteString(`};
`)

	return sb.String()
}

func escapeJS(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "\\'")
	return s
}

func generateMarkdown(config ShortcutsConfig) string {
	var sb strings.Builder

	sb.WriteString(`---
title: "Keyboard Shortcuts"
description: "Default keyboard shortcuts for Reliant"
weight: 60
---

<!-- =============================================================================
     GENERATED FILE - DO NOT EDIT DIRECTLY
     
     Source of truth: config/shortcuts.yaml
     Generated by: go run ./tools/docgen/shortcuts
     Regenerate with: task generate:docs:shortcuts
     ============================================================================= -->

Reliant includes keyboard shortcuts for common actions. All shortcuts can be customized in Settings → Keyboard Shortcuts.

The shortcuts are the same in the desktop app and in a browser tab. ` + "`Cmd`" + ` is
` + "`Ctrl`" + ` on Windows and Linux.

Where a browser has its own use for a chord that a web page may take, like
` + "`Cmd+P`" + ` (print) or ` + "`Cmd+L`" + ` (the address bar), Reliant takes it. Chords a
browser never releases to a web page, like ` + "`Cmd+T`" + ` and ` + "`Cmd+W`" + `, are not
used; those actions have a chord that works everywhere — often a ` + "`Cmd+K`" + `
sequence — and the desktop app also keeps the familiar chord, listed under
**Also in the desktop app**.

A ` + "`Cmd+K`" + ` sequence's second key can be typed with ` + "`Cmd`" + ` released or still held.

`)

	// Group by category
	byCategory := make(map[string][]Shortcut)
	for _, s := range config.Shortcuts {
		byCategory[s.Category] = append(byCategory[s.Category], s)
	}

	// Output in category order
	for _, cat := range config.Categories {
		shortcuts := byCategory[cat]
		if len(shortcuts) == 0 {
			continue
		}

		fmt.Fprintf(&sb, "## %s\n\n", cat)
		sb.WriteString("| Shortcut | Also in the desktop app | Action |\n")
		sb.WriteString("|----------|-------------------------|--------|\n")

		// Sort by name within category
		sort.Slice(shortcuts, func(i, j int) bool {
			return shortcuts[i].Name < shortcuts[j].Name
		})

		for _, s := range shortcuts {
			binding := fmt.Sprintf("`%s`", formatBindingForDocs(s.Binding))
			if s.DesktopOnly {
				binding += " (desktop app only)"
			} else if s.WebBinding != "" && s.WebBinding != s.Binding {
				binding += fmt.Sprintf(" (browser: `%s`)", formatBindingForDocs(s.WebBinding))
			}
			aliases := make([]string, len(s.DesktopAliases))
			for i, alias := range s.DesktopAliases {
				aliases[i] = fmt.Sprintf("`%s`", formatBindingForDocs(alias))
			}
			fmt.Fprintf(&sb, "| %s | %s | %s |\n", binding, strings.Join(aliases, ", "), s.Name)
		}
		sb.WriteString("\n")
	}

	sb.WriteString(`## Customizing Shortcuts

To customize keyboard shortcuts:

1. Open Settings (` + "`Cmd+,`" + `)
2. Navigate to Keyboard Shortcuts
3. Click any shortcut to rebind it
4. Press your desired key combination
5. Click Save

Use the Reset button to restore default shortcuts.
`)

	return sb.String()
}

func formatBindingForDocs(binding string) string {
	// Convert to display format (e.g., "Cmd+Shift+T")
	// Already in correct format from YAML
	return binding
}
