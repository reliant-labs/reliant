package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// forge.yaml's brownfield switches: `envs:` and `monitoring:`.
//
// A team with an existing app — any language, its own CI and infrastructure —
// adopts forge one piece at a time, and the first piece is usually monitoring
// (`forge obs init`). Those projects have no deploy/kcl tree, so the facts KCL
// would declare about an environment (that it exists, where its telemetry
// goes) need a home that is just switches and simple values. That home is
// here. KCL stays the long-term home of an environment's topology: an env with
// a deploy/kcl/<env>/ directory is KCL's, and declaring the same env's
// monitoring in both places is a load error (internal/cli/env_declaration.go),
// never a silent precedence.
//
// Both lower into the same model the KCL render fills (obsbackend.Facts), so
// every `forge obs` verb reads one thing whichever file declared it.
//
// DEPRECATED (legacy shorthand): an env document, deploy/envs/<env>.yaml
// (`forge project schema env`), declares the same env without KCL and without
// making forge.yaml a list of resources. These keys keep working (semver 1);
// declaring one env in both is a load error. docs/design/env-document.md.

// EnvNameMax bounds an env name: a DNS label, because it names a namespace
// and a control-plane environment.
const EnvNameMax = 63

var envNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// ValidEnvName reports whether name can name an environment: a lowercase DNS
// label (letters, digits and '-', not starting or ending with '-').
func ValidEnvName(name string) bool {
	return len(name) <= EnvNameMax && envNameRE.MatchString(name)
}

// MonitoringConfig is forge.yaml `monitoring:`. It switches forge's
// observability on for the envs `envs:` names, for an app forge does not
// deploy. Written either as a bare boolean (`monitoring: true`) or as a
// mapping:
//
//	monitoring:
//	  enabled: true                                 # default true when the block is present
//	  endpoint: https://otel.example.com:4318       # a self-hosted OTLP backend instead of Reliant
//	  control_plane: https://admin.reliantapi.com   # the Reliant control plane (default: Reliant's)
//	  console_url: https://forge.reliantlabs.io     # where dashboard links point (default: Reliant's console)
type MonitoringConfig struct {
	// Enabled is the switch. A present block with no `enabled:` is on.
	Enabled bool `yaml:"enabled"`
	// Endpoint is a self-hosted OTLP/HTTP backend the app exports to instead
	// of Reliant. forge prints it as OTEL_EXPORTER_OTLP_ENDPOINT and mints
	// nothing; the `forge obs` read verbs cannot query it.
	Endpoint string `yaml:"endpoint,omitempty"`
	// ControlPlane is the Reliant control plane the envs live on. "" is
	// Reliant's production control plane (cloud.DefaultEndpoint).
	ControlPlane string `yaml:"control_plane,omitempty"`
	// ConsoleURL is the base of the dashboard links forge prints. "" is
	// Reliant's console.
	ConsoleURL string `yaml:"console_url,omitempty"`

	// bare records that the block was written as a boolean, so a rewrite of
	// forge.yaml keeps the shorthand the author chose.
	bare bool
}

// UnmarshalYAML accepts `monitoring: <bool>` or the mapping.
func (m *MonitoringConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var on bool
		if err := node.Decode(&on); err != nil {
			return fmt.Errorf("monitoring: want true, false or a mapping (enabled, endpoint, control_plane, console_url), got %q", node.Value)
		}
		*m = MonitoringConfig{Enabled: on, bare: true}
		return nil
	}
	type plain MonitoringConfig
	p := plain{Enabled: true}
	if err := node.Decode(&p); err != nil {
		return err
	}
	*m = MonitoringConfig(p)
	return nil
}

// MarshalYAML writes the shorthand back when the block holds nothing but the
// switch and was written that way.
func (m MonitoringConfig) MarshalYAML() (any, error) {
	if m.bare && m.Endpoint == "" && m.ControlPlane == "" && m.ConsoleURL == "" {
		return m.Enabled, nil
	}
	type plain MonitoringConfig
	return plain(m), nil
}

// MonitoringOn reports whether forge.yaml switches monitoring on.
func (c *ProjectConfig) MonitoringOn() bool {
	return c != nil && c.Monitoring != nil && c.Monitoring.Enabled
}

// DeclaresEnv reports whether forge.yaml `envs:` names env.
func (c *ProjectConfig) DeclaresEnv(env string) bool {
	if c == nil {
		return false
	}
	for _, e := range c.Envs {
		if e == env {
			return true
		}
	}
	return false
}

// validateBrownfield checks `envs:` and `monitoring:`.
func validateBrownfield(cfg *ProjectConfig, root *yaml.Node) []validationIssue {
	var out []validationIssue
	seen := map[string]bool{}
	for i, e := range cfg.Envs {
		line, col := findNodePos(root, []string{"envs"})
		switch {
		case !ValidEnvName(e):
			out = append(out, validationIssue{line: line, column: col,
				msg: fmt.Sprintf("envs[%d] %q is not an env name", i, e),
				fix: "use a lowercase DNS label: letters, digits and '-', at most 63 characters (prod, staging, eu-west)."})
		case seen[e]:
			out = append(out, validationIssue{line: line, column: col,
				msg: fmt.Sprintf("envs names %q twice", e), fix: "list each env once."})
		}
		seen[e] = true
	}
	if m := cfg.Monitoring; m != nil {
		for _, f := range []struct{ key, val string }{
			{"endpoint", m.Endpoint}, {"control_plane", m.ControlPlane}, {"console_url", m.ConsoleURL},
		} {
			if f.val == "" {
				continue
			}
			if err := checkAbsoluteURL(f.val); err != nil {
				line, col := findNodePos(root, []string{"monitoring", f.key})
				out = append(out, validationIssue{line: line, column: col,
					msg: fmt.Sprintf("monitoring.%s %q: %v", f.key, f.val, err),
					fix: "write it as an absolute URL, e.g. https://otel.example.com:4318."})
			}
		}
	}
	return out
}

// checkAbsoluteURL accepts an http(s) URL with a host and no query or
// fragment.
func checkAbsoluteURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("not an absolute http(s) URL")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("a query or fragment is not allowed")
	}
	return nil
}
