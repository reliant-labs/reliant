package codegen

import (
	"regexp"
	"strings"
	"testing"
)

// roots_gen.go carries, per root, every variable ROOTS projects — with its
// config, path and type — the families, and each config its process loads
// with the reach its command demands.
func TestRootsGoSource_EachRootsReachAsData(t *testing.T) {
	views := binConfigViews()
	if err := markBinConfig(t, binConfigCompose, views); err != nil {
		t.Fatal(err)
	}
	roots := graphRoots(views)
	src, ok, err := rootsGoSource(roots, "AppConfig")
	if err != nil || !ok {
		t.Fatalf("rootsGoSource = ok %v, %v", ok, err)
	}
	gen := strings.Join(strings.Fields(string(src)), " ")
	for _, want := range []string{
		`Name: "api", Args: []string{"server", "api"},`,
		`{Name: "DATABASE_URL", Config: "AppConfig", Path: "database_url", Type: "string", Required: true, Sensitive: true},`,
		`{Name: "API_SIGNING_KEY", Config: "ApiConfig", Path: "signing_key", Type: "string", Required: true, Sensitive: true},`,
		`{Name: "AUDIT_SINK", Config: "ServerConfig", Path: "audit_sink", Type: "string", Required: true},`,
		`{Message: &Config{}, Reach: []string{"port", "log_level", "database_url"}},`,
		`{Message: &ApiConfig{}, Reach: []string{"signing_key", "greeting"}},`,
		`Name: "worker", Args: []string{"server", "worker"},`,
		`func ValidateRootEnv(name string, env map[string]string) []error {`,
	} {
		if !strings.Contains(gen, want) {
			t.Errorf("roots_gen.go lacks %q:\n%s", want, src)
		}
	}
	// The worker never reaches the api's credential.
	worker := gen[strings.Index(gen, `Name: "worker"`):]
	if strings.Contains(worker, "API_SIGNING_KEY") || strings.Contains(worker, "DATABASE_URL") {
		t.Errorf("the worker root reaches only its own config:\n%s", worker)
	}

	// The variables are ROOTS' exactly: every env/secret name ROOTS lists
	// for a root is a Var of it, and no other.
	kcl := RootsKCLFor(roots, RootsEnv{ConfigType: "RbcProcess", EnvMap: "rbcProcessEnvMap", Argv: true})
	for _, name := range []string{"api", "worker"} {
		block := kcl[strings.Index(kcl, "    \""+name+"\" = Root {"):]
		block = block[:strings.Index(block, "    }")]
		var kclVars []string
		for _, list := range regexp.MustCompile(`(?m)^\s+(env|secrets) = \[(.*)\]$`).FindAllStringSubmatch(block, -1) {
			for _, v := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(list[2], -1) {
				kclVars = append(kclVars, v[1])
			}
		}
		goBlock := gen[strings.Index(gen, `Name: "`+name+`"`):]
		goBlock = goBlock[:strings.Index(goBlock, "Configs:")]
		goVars := regexp.MustCompile(`\{Name: "([A-Z_]+)"`).FindAllStringSubmatch(goBlock, -1)
		if len(goVars) != len(kclVars) {
			t.Errorf("%s: roots_gen.go has %d vars, ROOTS %d (%v)", name, len(goVars), len(kclVars), kclVars)
		}
		for _, v := range kclVars {
			if !strings.Contains(goBlock, `{Name: "`+v+`"`) {
				t.Errorf("%s: ROOTS lists %s, roots_gen.go does not", name, v)
			}
		}
	}

	// No reach to read: no ROOTS, and no roots_gen.go.
	if _, ok, _ := rootsGoSource([]GraphRoot{{Name: "server"}}, "AppConfig"); ok {
		t.Error("a root with unknown reach projects no roots_gen.go, as it projects no ROOTS")
	}
}
