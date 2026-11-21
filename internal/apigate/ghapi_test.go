package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// The real ghAPI, through an APIGATE_GH client the way CI runs it
// (scripts/ci-public-gh.sh): the client's exit status and stderr reach the
// classification, and a client that cannot start is named.
func TestGHAPIClientFailureIsClassified(t *testing.T) {
	testkit.Slow(t, "execs a fake APIGATE_GH client script")
	dir := t.TempDir()
	client := filepath.Join(dir, "gh")
	script := "#!/bin/sh\necho 'ci-public-gh: infrastructure: GitHub API rate limit (HTTP 429) reading repos/o/r/pulls/5 after 4 attempt(s)' >&2\nexit 75\n"
	if err := os.WriteFile(client, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APIGATE_GH", client)
	t.Setenv("GITHUB_REPOSITORY", "o/r")
	_, _, _, err := prDeclaration("5")
	var gh *githubReadError
	if !errors.As(err, &gh) || gh.cause != "GitHub API rate limit (HTTP 429)" {
		t.Fatalf("err = %v, want a rate-limit githubReadError", err)
	}

	t.Setenv("APIGATE_GH", filepath.Join(dir, "missing"))
	_, _, _, err = prDeclaration("5")
	if !errors.As(err, &gh) || !strings.HasPrefix(gh.cause, "cannot run the GitHub client") {
		t.Fatalf("err = %v, want one naming the client that cannot run", err)
	}
}
