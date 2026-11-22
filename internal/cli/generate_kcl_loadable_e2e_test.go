//go:build e2e

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// TestE2EGenerateFailsOnUnrenderableDeployKCL is the end-to-end half of the
// #322 incident: a deploy tree that does not compile must make `forge generate`
// exit NON-ZERO.
//
// It used to exit 0. The KCL load error was downgraded to a warning, so a
// project whose env declared a field the closed schema had removed got a clean
// generate and discovered the break later, at `forge env render` — an error
// with no connection to the command that had already reported success. The
// silently-no-opping registry migration put projects into exactly that state.
//
// The fault is injected the way the incident produced it: a field the schema
// does not accept, added to a real scaffolded env. The assertion is on the exit
// code and on the error naming the env, because a non-zero exit that does not
// say WHICH env or WHAT kcl said has moved the discovery rather than prevented
// it.
func TestE2EGenerateFailsOnUnrenderableDeployKCL(t *testing.T) {
	t.Parallel()
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "kclbreak", "--mod", "example.com/kclbreak", "--service", "api")
	projectDir := filepath.Join(dir, "kclbreak")
	addCorpusForgePkgReplace(t, projectDir)

	// A clean scaffold must generate cleanly — otherwise the assertion below
	// proves nothing about the fault we are about to inject.
	runCmd(t, projectDir, forgeBin, "generate")

	// Inject the fault: a member a closed forge schema does not have. This
	// is the shape of #322's break (`registry` was removed from
	// ClusterTarget), without depending on that field or that schema.
	//
	// It goes into the first LIVE schema instance — `<name> = forge.<Schema>
	// {` on a line of its own — never into a commented-out one. Since #518
	// prod is hosted and its only `forge.ClusterTarget {` is in the
	// commented example of how to bind a cluster instead; injecting there
	// left the KCL compiling and turned this test into an assertion about a
	// comment.
	mainK := filepath.Join(projectDir, "deploy", "kcl", "prod", "main.k")
	src, err := os.ReadFile(mainK)
	if err != nil {
		t.Fatalf("read prod main.k: %v", err)
	}
	loc := liveSchemaInstance.FindStringSubmatchIndex(string(src))
	if loc == nil {
		t.Fatalf("prod/main.k instantiates no forge schema on a live line; the fault could not be injected:\n%s", src)
	}
	opener := string(src[loc[2]:loc[3]])
	broken := string(src[:loc[3]]) + "\n    no_such_field_on_this_schema = \"x\"" + string(src[loc[3]:])
	t.Logf("fault injected into %q", strings.TrimSpace(opener))
	if err := os.WriteFile(mainK, []byte(broken), 0o644); err != nil {
		t.Fatalf("write prod main.k: %v", err)
	}

	cmd := exec.Command(forgeBin, "generate")
	cmd.Dir = projectDir
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=", "GOPROXY=https://proxy.golang.org,direct"))
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("forge generate EXITED 0 on a deploy tree that does not compile — the break then surfaces at render, with nothing pointing back here:\n%s", out)
	}
	got := string(out)
	for _, want := range []string{"deploy KCL", "prod", "no_such_field_on_this_schema"} {
		if !strings.Contains(got, want) {
			t.Errorf("the failure must name %q so the user knows what to fix:\n%s", want, got)
		}
	}
}

// liveSchemaInstance matches the opening line of a top-level or nested forge
// schema instance, `<name> = forge.<Schema> {`, and captures it. Comment
// lines cannot match (they start with `#`), and neither can a lambda's
// `-> forge.<Schema> {` (there is no `= forge.` before the brace).
var liveSchemaInstance = regexp.MustCompile(`(?m)^(\s*[A-Za-z_]\w*\s*=\s*forge\.[A-Z]\w*\s*\{)\s*$`)
