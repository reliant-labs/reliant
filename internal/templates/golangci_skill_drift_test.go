package templates

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The ci/golangci skill quotes the scaffolded .golangci.yml's thresholds,
// because agents learned them only from a failing lint round-trip (ranger's
// balancer hit funlen at 87 statements). A skill that quotes a number drifts
// the day someone tunes the template, so this test reads each number from the
// rendered template and requires the skill row that states it.
func TestGolangciSkillQuotesTheTemplatesThresholds(t *testing.T) {
	t.Parallel()
	rendered, err := ProjectTemplates().Render("golangci.yml.tmpl", struct {
		Module, TypedAccessGuard, LoaderPackage string
	}{"example.com/fx", "", ""})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Linters struct {
			Settings struct {
				Funlen struct {
					Lines      int `yaml:"lines"`
					Statements int `yaml:"statements"`
				} `yaml:"funlen"`
				Gocognit struct {
					MinComplexity int `yaml:"min-complexity"`
				} `yaml:"gocognit"`
				Nestif struct {
					MinComplexity int `yaml:"min-complexity"`
				} `yaml:"nestif"`
				Interfacebloat struct {
					Max int `yaml:"max"`
				} `yaml:"interfacebloat"`
				Revive struct {
					Rules []struct {
						Name      string `yaml:"name"`
						Arguments []int  `yaml:"arguments"`
					} `yaml:"rules"`
				} `yaml:"revive"`
			} `yaml:"settings"`
		} `yaml:"linters"`
		Formatters struct {
			Enable []string `yaml:"enable"`
		} `yaml:"formatters"`
	}
	if err := yaml.Unmarshal(rendered, &cfg); err != nil {
		t.Fatalf("parse the rendered .golangci.yml: %v", err)
	}
	s := cfg.Linters.Settings
	if s.Funlen.Statements == 0 || s.Gocognit.MinComplexity == 0 || s.Nestif.MinComplexity == 0 || s.Interfacebloat.Max == 0 {
		t.Fatalf("a threshold did not parse (the template's shape moved?): %+v", s)
	}
	revive := map[string]int{}
	for _, r := range s.Revive.Rules {
		if len(r.Arguments) == 1 {
			revive[r.Name] = r.Arguments[0]
		}
	}

	skill, err := templateFS.ReadFile("project/skills/forge/ci/golangci/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	body := string(skill)
	want := []string{
		fmt.Sprintf("| `funlen` | more than %d statements or %d lines |", s.Funlen.Statements, s.Funlen.Lines),
		fmt.Sprintf("| `gocognit` | cognitive complexity over %d |", s.Gocognit.MinComplexity),
		fmt.Sprintf("| `nestif` | an `if` nest of complexity %d or more |", s.Nestif.MinComplexity),
		fmt.Sprintf("| `interfacebloat` | an interface with more than %d methods |", s.Interfacebloat.Max),
		fmt.Sprintf("| revive `max-control-nesting` | control flow nested deeper than %d |", revive["max-control-nesting"]),
		fmt.Sprintf("| revive `argument-limit` | more than %d parameters |", revive["argument-limit"]),
		fmt.Sprintf("| revive `function-result-limit` | more than %d results |", revive["function-result-limit"]),
		fmt.Sprintf("| revive `max-public-structs` | more than %d exported structs in a package |", revive["max-public-structs"]),
	}
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("ci/golangci skill does not state the template's value; want a row starting %q", w)
		}
	}
	if len(cfg.Formatters.Enable) != 1 || cfg.Formatters.Enable[0] != "goimports" || !strings.Contains(body, "local-prefixes") {
		t.Errorf("the template's formatters are %v; the skill documents goimports with local-prefixes", cfg.Formatters.Enable)
	}
}
