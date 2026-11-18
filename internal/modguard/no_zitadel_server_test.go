// Copyright (c) 2025 Reliant Labs
package modguard_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// forge must never require the Zitadel SERVER module. pkg/identity drives a
// Zitadel we run unmodified (AGPL-3.0) strictly over its HTTP/gRPC APIs; AGPL
// permits running unmodified copyleft software as a service, but importing it
// would pull the server's licence into every binary built from this module and
// into every consumer of forge/pkg.
//
// Denied: github.com/zitadel/zitadel and github.com/zitadel/zitadel/vN
// (AGPL-3.0). Not denied, because they are separate modules under their own
// licences (verified against each repository's LICENSE): github.com/zitadel/oidc/v3
// and github.com/zitadel/zitadel-go/v3 (Apache-2.0, neither requires the server
// module), github.com/zitadel/logging (Apache-2.0), github.com/zitadel/passwap
// and github.com/zitadel/schema (BSD-3-Clause). If one of them ever drags the
// server in, the go.sum / module-graph checks below fail on the server module.
//
// go.mod and go.sum are read here, so the test cache notices an edit; the
// module-graph check execs `go list` (invisible to the cache) and so runs in
// full mode only.
var zitadelServerModule = regexp.MustCompile(`^github\.com/zitadel/zitadel(/v[0-9]+)?$`)

// zitadelServerRefs returns every denied module path mentioned in a go.mod
// (require or replace, either side of =>) or go.sum, with its 1-based line.
func zitadelServerRefs(file, content string) []string {
	var hits []string
	for i, line := range strings.Split(content, "\n") {
		if before, _, ok := strings.Cut(line, "//"); ok {
			line = before
		}
		for _, f := range strings.Fields(line) {
			if zitadelServerModule.MatchString(f) {
				hits = append(hits, file+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
				break
			}
		}
	}
	return hits
}

func TestZitadelServerGuardMatcher(t *testing.T) {
	t.Parallel()
	denied := map[string]string{
		"require":       "module x\n\nrequire github.com/zitadel/zitadel v1.2.3\n",
		"require /v2":   "module x\n\nrequire (\n\tgithub.com/zitadel/zitadel/v2 v2.71.0 // indirect\n)\n",
		"replace lhs":   "module x\n\nreplace github.com/zitadel/zitadel/v3 => ../z\n",
		"replace rhs":   "module x\n\nreplace example.com/a => github.com/zitadel/zitadel v1.0.0\n",
		"go.sum h1":     "github.com/zitadel/zitadel/v2 v2.0.0 h1:abc=\n",
		"go.sum go.mod": "github.com/zitadel/zitadel v1.0.0/go.mod h1:abc=\n",
	}
	for name, content := range denied {
		if hits := zitadelServerRefs("go.mod", content); len(hits) == 0 {
			t.Errorf("%s: matcher missed the server module in:\n%s", name, content)
		}
	}
	allowed := "module x\n\nrequire (\n" +
		"\tgithub.com/zitadel/oidc/v3 v3.49.2\n" +
		"\tgithub.com/zitadel/zitadel-go/v3 v3.0.0\n" +
		"\tgithub.com/zitadel/logging v0.6.0\n" +
		"\tgithub.com/zitadel/passwap v0.6.0\n" +
		"\tgithub.com/zitadel/schema v1.3.2\n" +
		"\texample.com/zitadel/zitadel v1.0.0\n" +
		")\n// github.com/zitadel/zitadel is mentioned only in a comment\n"
	if hits := zitadelServerRefs("go.mod", allowed); len(hits) != 0 {
		t.Errorf("matcher flagged allowed modules: %v", hits)
	}
}

func TestGoModAndSumDoNotMentionZitadelServer(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, hit := range zitadelServerRefs(name, string(data)) {
			t.Errorf("%s — the AGPL Zitadel server module must never enter forge's module graph; "+
				"drive Zitadel over its API (see pkg/identity)", hit)
		}
	}
}

func TestModuleGraphDoesNotContainZitadelServer(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list -m all over the module cache (invisible to the test cache); full mode only")
	}
	t.Parallel()
	cmd := exec.Command("go", "list", "-m", "all")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -m all: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		for _, f := range strings.Fields(line) {
			if zitadelServerModule.MatchString(f) {
				why, _ := exec.Command("go", "mod", "why", "-m", f).CombinedOutput()
				t.Errorf("module graph contains the AGPL Zitadel server module %s (%q)\ngo mod why -m %s:\n%s", f, line, f, why)
				break
			}
		}
	}
}
