package depsurface

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// golangciInterfaceBloatDefault is interfacebloat's `max` when a config does
// not set one.
const golangciInterfaceBloatDefault = 10

var golangciConfigNames = []string{".golangci.yml", ".golangci.yaml"}

// lintConfig is what narrow-deps reads of the project's golangci-lint
// configuration: the goimports local prefixes its output must be grouped by
// and the interfacebloat limit its output is judged against.
type lintConfig struct {
	found  bool
	source string // project-relative config file; empty when none was found

	localPrefixes []string

	interfaceBloatOn  bool
	interfaceBloatMax int
}

// readLintConfig reads dir's .golangci.yml / .golangci.yaml (v1 or v2 layout).
// A project without one gets golangci-lint's own defaults, and the bloat check
// stays advisory (on): the interface is as wide as it is either way.
func readLintConfig(dir string) lintConfig {
	cfg := lintConfig{interfaceBloatOn: true, interfaceBloatMax: golangciInterfaceBloatDefault}
	for _, name := range golangciConfigNames {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var raw struct {
			Linters struct {
				Default   string         `yaml:"default"`
				EnableAll bool           `yaml:"enable-all"`
				DisableAl bool           `yaml:"disable-all"`
				Enable    []string       `yaml:"enable"`
				Disable   []string       `yaml:"disable"`
				Settings  linterSettings `yaml:"settings"`
			} `yaml:"linters"`
			LintersSettings linterSettings `yaml:"linters-settings"`
			Formatters      struct {
				Settings linterSettings `yaml:"settings"`
			} `yaml:"formatters"`
		}
		if yaml.Unmarshal(data, &raw) != nil {
			return cfg
		}
		cfg.found, cfg.source = true, name
		l := raw.Linters
		all := l.Default == "all" || l.EnableAll
		cfg.interfaceBloatOn = (all || slices.Contains(l.Enable, "interfacebloat")) && !slices.Contains(l.Disable, "interfacebloat")
		for _, s := range []linterSettings{l.Settings, raw.LintersSettings} {
			if s.InterfaceBloat.Max > 0 {
				cfg.interfaceBloatMax = s.InterfaceBloat.Max
			}
		}
		for _, s := range []linterSettings{raw.Formatters.Settings, l.Settings, raw.LintersSettings} {
			cfg.localPrefixes = append(cfg.localPrefixes, s.Goimports.LocalPrefixes...)
		}
		return cfg
	}
	return cfg
}

type linterSettings struct {
	InterfaceBloat struct {
		Max int `yaml:"max"`
	} `yaml:"interfacebloat"`
	Goimports struct {
		// golangci v2 writes a list, v1 a comma-separated string.
		LocalPrefixes prefixList `yaml:"local-prefixes"`
	} `yaml:"goimports"`
}

type prefixList []string

func (p *prefixList) UnmarshalYAML(n *yaml.Node) error {
	var list []string
	if n.Kind == yaml.SequenceNode {
		if err := n.Decode(&list); err != nil {
			return err
		}
	} else {
		var s string
		if err := n.Decode(&s); err != nil {
			return err
		}
		list = strings.Split(s, ",")
	}
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" {
			*p = append(*p, s)
		}
	}
	return nil
}

// importPrefix is the `goimports -local` argument that groups imports the way
// the project's lint does: its configured prefixes, else its module.
func (c lintConfig) importPrefix(module string) string {
	if len(c.localPrefixes) > 0 {
		return strings.Join(c.localPrefixes, ",")
	}
	return module
}

// limitSource says where the interfacebloat limit came from.
func (c lintConfig) limitSource() string {
	if c.found {
		return c.source
	}
	return "golangci-lint's default"
}

// overBloat reports whether an interface of entries entries (what
// interfacebloat counts) is wider than the limit of a lint that is on.
func (c lintConfig) overBloat(entries int) bool {
	return c.interfaceBloatOn && entries > c.interfaceBloatMax
}

// suppressesBloat reports whether narrow-deps writes a reasoned
// //nolint:interfacebloat above an interface of entries entries: only when
// the project's own config runs interfacebloat. Without a config the check is
// advisory — a directive for a linter nobody runs is noise, and nolintlint
// flags it as unused.
func (c lintConfig) suppressesBloat(entries int) bool {
	return c.found && c.overBloat(entries)
}

// Bloated is a narrowed interface wider than the project's interfacebloat
// limit.
type Bloated struct {
	Interface string
	// Methods counts what interfacebloat counts: every method written out and
	// every embedded interface.
	Methods     int
	Limit       int
	LimitSource string
	// Suppressed: narrow-deps wrote a reasoned //nolint:interfacebloat above
	// the type, because the project's lint runs interfacebloat.
	Suppressed bool
}

func (b Bloated) String() string {
	if b.Suppressed {
		return fmt.Sprintf("%s has %d methods, over interfacebloat's limit of %d (%s): written with a reasoned `//nolint:interfacebloat` — it is exactly what the package calls, so the width is the package's. To shrink it, split the package (the interface follows), not the interface.",
			b.Interface, b.Methods, b.Limit, b.LimitSource)
	}
	return fmt.Sprintf("%s has %d methods, over interfacebloat's limit of %d (%s). Remedy: split the dependency so each half stays under the limit, or keep it and add `//nolint:interfacebloat // <why it is one dependency>` above the type.",
		b.Interface, b.Methods, b.Limit, b.LimitSource)
}
