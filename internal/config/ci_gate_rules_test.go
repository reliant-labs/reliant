package config

import (
	"strings"
	"testing"
)

const gateRulesBase = "name: demo\nmodule_path: github.com/example/demo\nci:\n  gate:\n"

func TestCIGateAffectedRules_Load(t *testing.T) {
	in := gateRulesBase + `    affected_rules:
      - paths: ["db/migrations/**"]
        except: ["db/migrations/README.md"]
        packages: ["./internal/store/...", "./internal/db"]
      - paths: ["frontends/web/**"]
        frontends: [web]
      - paths: ["deploy/kcl/**"]
        full: true
        reason: KCL is read by the render tests
    extra_checks:
      - name: electron
        command: [npm, test]
        paths: ["electron/**"]
        paths_go_closure: ["./tools/docgen/..."]
`
	cfg, err := LoadProject([]byte(in), "forge.yaml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.CI.Gate.ExtraChecks[0].PathsGoClosure; len(got) != 1 || got[0] != "./tools/docgen/..." {
		t.Fatalf("paths_go_closure = %v", got)
	}
	rules := cfg.CI.Gate.AffectedRules
	if len(rules) != 3 || rules[0].Except[0] != "db/migrations/README.md" || rules[1].Frontends[0] != "web" || !rules[2].Full || rules[2].Reason == "" {
		t.Fatalf("rules = %+v", rules)
	}
	if got := cfg.CI.Gate.ExtraChecks[0].Paths; len(got) != 1 || got[0] != "electron/**" {
		t.Fatalf("extra check paths = %v", got)
	}
}

func TestCIGateAffectedRules_NewFieldsLoad(t *testing.T) {
	in := gateRulesBase + `    affected_rules:
      - on: go-source
        packages: [./internal/guard]
      - paths: ["proto/**"]
        packages: ["./gen/{dir}"]
        packages_importing: [./internal/db/testutil]
        full_checks: [test-web]
`
	cfg, err := LoadProject([]byte(in), "forge.yaml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	r := cfg.CI.Gate.AffectedRules
	if len(r) != 2 || r[0].On != CIGateOnGoSource || r[1].PackagesImporting[0] != "./internal/db/testutil" || r[1].FullChecks[0] != "test-web" || r[1].Packages[0] != "./gen/{dir}" {
		t.Fatalf("rules = %+v", r)
	}
}

func TestCIGateAffectedRules_Invalid(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		want       []string
	}{
		{"no paths", "    affected_rules:\n      - frontends: [web]\n", []string{"forge.yaml:6", "ci.gate.affected_rules[0] has no paths"}},
		{"empty paths", "    affected_rules:\n      - paths: []\n        frontends: [web]\n", []string{"ci.gate.affected_rules[0] has no paths"}},
		{"selects nothing", "    affected_rules:\n      - paths: [\"x/**\"]\n", []string{"ci.gate.affected_rules[0] selects nothing", "affected_ignore"}},
		{"full and packages", "    affected_rules:\n      - paths: [\"x/**\"]\n        full: true\n        packages: [./a]\n", []string{"sets full: true and also packages"}},
		{"bad glob", "    affected_rules:\n      - paths: [\"x/[\"]\n        full: true\n", []string{"forge.yaml:6", `ci.gate.affected_rules[0].paths[0] "x/[" is not a valid glob`}},
		{"escaping glob", "    affected_rules:\n      - paths: [\"../other/**\"]\n        full: true\n", []string{"leaves the project"}},
		{"absolute except", "    affected_rules:\n      - paths: [\"x/**\"]\n        except: [/etc/x]\n        full: true\n", []string{"except[0]", "is absolute"}},
		{"empty package", "    affected_rules:\n      - paths: [\"x/**\"]\n        packages: [\"\"]\n", []string{"packages[0] \"\" is not a Go package pattern"}},
		{"empty frontend", "    affected_rules:\n      - paths: [\"x/**\"]\n        frontends: [\"\"]\n", []string{"frontends[0] is empty"}},
		{"unknown key", "    affected_rules:\n      - paths: [\"x/**\"]\n        frontend: [web]\n", []string{"unknown key", "affected_rules[0].frontend"}},
		{"unknown on", "    affected_rules:\n      - on: rust-source\n        packages: [./a]\n", []string{"on \"rust-source\" is not a known match"}},
		{"importing a subtree", "    affected_rules:\n      - paths: [\"x/**\"]\n        packages_importing: [./a/...]\n", []string{"packages_importing[0] \"./a/...\" is not one package"}},
		{"unknown placeholder", "    affected_rules:\n      - paths: [\"x/**\"]\n        packages: [\"./gen/{file}\"]\n", []string{"has an unknown placeholder"}},
		{"empty full check", "    affected_rules:\n      - paths: [\"x/**\"]\n        full_checks: [\"\"]\n", []string{"full_checks[0] is empty"}},
		{"extra check bad paths", "    extra_checks:\n      - name: e\n        command: [x]\n        paths: [\"\"]\n", []string{"ci.gate.extra_checks[0].paths[0] \"\" is empty"}},
		{"extra check glob closure", "    extra_checks:\n      - name: e\n        command: [x]\n        paths_go_closure: [\"tools/*\"]\n", []string{"ci.gate.extra_checks[0].paths_go_closure[0] \"tools/*\" is not a Go package pattern"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadProject([]byte(gateRulesBase+tc.yaml), "forge.yaml")
			ve := requireValidationError(t, err)
			if !containsAll(ve.Error(), tc.want...) {
				t.Errorf("error missing %q:\n%s", tc.want, ve.Error())
			}
		})
	}
}

// Without the new keys a config loads exactly as before: no rules, no paths.
func TestCIGateAffectedRules_AbsentIsEmpty(t *testing.T) {
	cfg, err := LoadProject([]byte(gateRulesBase+"    affected_ignore: [\"*.md\"]\n"), "forge.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CI.Gate.AffectedRules != nil || strings.Join(cfg.CI.Gate.AffectedIgnore, ",") != "*.md" {
		t.Fatalf("gate = %+v", cfg.CI.Gate)
	}
}
