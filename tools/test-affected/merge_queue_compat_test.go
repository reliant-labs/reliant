package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

func TestMergeQueuePublicCompatReadsCompleteOwnerHistory(t *testing.T) {
	testkit.Slow(t, "executes the actual workflow step and check-compat against a small Git fixture")
	requireShellTools(t)
	wf := loadMergeQueue(t)
	var run string
	for _, step := range wf.Jobs["merge-gate"].Steps {
		if step.Name == "task ci:merge -- --queue" {
			run = step.Run
		}
	}
	if run == "" {
		t.Fatal("no actual merge step")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "db", "migrations"), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("migration.sql", "-- forge:breaks-rollback previous binary cannot run\n")
	ok, out := ciShell(t, `set -euo pipefail
cd "$FIXTURE"
git -c init.defaultBranch=main init -q
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
git commit --allow-empty -qm base
git rev-parse HEAD
mv migration.sql db/migrations/001_drop.up.sql
git add db && git commit -qm candidate`, map[string]string{"FIXTURE": root, "GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1"})
	if !ok {
		t.Fatalf("git fixture failed: %s", out)
	}
	base := strings.TrimSpace(out)
	// The workflow really creates COMPAT_GH and sets CI_PR. Stop only at
	// Task; its fake dispatches the real compatibility script in the fixture.
	script := filepath.Join(root, "check-compat.sh")
	if err := os.WriteFile(script, readRepo(t, "scripts/check-compat.sh"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeExec(t, filepath.Join(root, "task"), `#!/usr/bin/env bash
set -euo pipefail
[[ $CI_PR == 7 && -z ${GH_TOKEN:-} && -z ${GITHUB_TOKEN:-} ]]
cd "$FIXTURE"
CI_BASE="$BASE" CI_QUEUE_ENTRY=1 bash "$FIXTURE/check-compat.sh" migrations
`)
	writeExec(t, filepath.Join(root, "curl"), `#!/usr/bin/env bash
set -euo pipefail
[[ -z ${GH_TOKEN:-} && -z ${GITHUB_TOKEN:-} ]]
url=${!#}
printf '%s\n' "$url" >>"$FIXTURE/requests"
case "$url" in
  'https://api.github.com/repos/reliant-labs/reliant/issues/7/labels?per_page=100&page=1')
    if [[ $CASE == missing ]]; then echo '[]'; else echo '[{"name":"migration:breaks-rollback-approved"}]'; fi ;;
  'https://api.github.com/repos/reliant-labs/reliant/issues/7/events?per_page=100&page=1')
    jq -n '[range(99) | {event:"commented"}] + [{event:"labeled",label:{name:"migration:breaks-rollback-approved"},actor:{login:"reliant-01"}}]' ;;
  'https://api.github.com/repos/reliant-labs/reliant/issues/7/events?per_page=100&page=2')
    case "$CASE" in
      http-failure) exit 22 ;;
      reapplied) echo '[{"event":"labeled","label":{"name":"migration:breaks-rollback-approved"},"actor":{"login":"someone"}}]' ;;
      *) echo '[{"event":"labeled","label":{"name":"unrelated"},"actor":{"login":"someone"}}]' ;;
    esac ;;
  *) exit 33 ;;
esac
`)
	for _, c := range []struct {
		name  string
		green bool
		calls int
	}{{"approved", true, 3}, {"reapplied", false, 3}, {"missing", false, 1}, {"http-failure", false, 3}} {
		t.Run(c.name, func(t *testing.T) {
			write("requests", "")
			ok, out := ciShell(t, run, map[string]string{"PATH": root + ":" + os.Getenv("PATH"), "BASE": base,
				"FIXTURE": root, "HEAD_REF": "refs/heads/gh-readonly-queue/main/pr-7-aaaaaaaaa", "RUNNER_TEMP": root,
				"CASE": c.name, "COMPAT_REPO": "reliant-labs/reliant", "GH_TOKEN": "", "GITHUB_TOKEN": ""})
			result, err := os.ReadFile(filepath.Join(root, "requests"))
			if err != nil {
				t.Fatal(err)
			}
			if ok != c.green || strings.Count(string(result), "\n") != c.calls {
				t.Fatalf("ok=%v (want %v), calls=%q (want %d):\n%s", ok, c.green, result, c.calls, out)
			}
		})
	}
}
