package codegen

import (
	"strings"
	"testing"
)

// assertFormatterClean fails when content would be rewritten by the
// whitespace fixers every scaffolded project runs in pre-commit
// (trailing-whitespace, end-of-file-fixer): a line with trailing blanks, or
// anything but exactly one newline at the end.
//
// A generated file those hooks rewrite is a file `forge ci verify-generated`
// then reports as drifted — the two gates fight on every commit. houndersclub
// hit both: config_gen.k (a trailing space) and frontend_config_gen.k (a
// trailing blank line).
func assertFormatterClean(t *testing.T, name, content string) {
	t.Helper()
	for i, line := range strings.Split(content, "\n") {
		if strings.TrimRight(line, " \t") != line {
			t.Errorf("%s:%d has trailing whitespace: %q", name, i+1, line)
		}
	}
	if !strings.HasSuffix(content, "\n") || strings.HasSuffix(content, "\n\n") {
		tail := content
		if len(tail) > 20 {
			tail = tail[len(tail)-20:]
		}
		t.Errorf("%s must end with exactly one newline; ends %q", name, tail)
	}
}

func TestGeneratedKCLModulesAreFormatterClean(t *testing.T) {
	cfg, err := GenerateConfigKCL(representativeConfigFields(), "myproj")
	if err != nil {
		t.Fatalf("GenerateConfigKCL: %v", err)
	}
	assertFormatterClean(t, "config_gen.k", cfg)

	fe, err := GenerateFrontendConfigKCL([]FrontendConfig{oidcFrontendConfig(), webFrontendConfig()}, "myproj")
	if err != nil {
		t.Fatalf("GenerateFrontendConfigKCL: %v", err)
	}
	assertFormatterClean(t, "frontend_config_gen.k", fe)
}
