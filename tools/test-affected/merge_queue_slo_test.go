package main

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestMergeQueueSLORejectsUntrustedArtifacts(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the workflow shell against fake API and archive responses; runs in task test")
	}
	requireShellTools(t)
	var workflow workflowPermissions
	if err := yaml.Unmarshal(readRepo(t, ".github/workflows/merge-queue-slo.yml"), &workflow); err != nil {
		t.Fatal(err)
	}
	runner := workflow.Jobs["publish"].Steps[0].Run
	for _, tc := range []struct {
		name, member, metaMutation string
		wrongHead, publish         bool
	}{
		{"matching proof", "merge-queue-slo.json", "", false, true},
		{"wrong run head", "merge-queue-slo.json", "", true, false},
		{"zip path", "../merge-queue-slo.json", "", false, false},
		{"wrong producer", "merge-queue-slo.json", "wrong-producer", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			head := strings.Repeat("a", 40)
			created := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Minute)
			data := map[string]any{"schema": "merge-queue-slo/v1", "repo": "reliant-labs/reliant", "head": head,
				"run_id": 123, "attempt": 2, "created": created.Unix(), "started": created.Unix() + 40, "measured": created.Unix() + 400}
			if tc.wrongHead {
				data["head"] = strings.Repeat("b", 40)
			}
			archivePath := filepath.Join(dir, "timing.zip")
			archive, err := os.Create(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			writer := zip.NewWriter(archive)
			member, err := writer.Create(tc.member)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.NewEncoder(member).Encode(data); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := archive.Close(); err != nil {
				t.Fatal(err)
			}
			event := filepath.Join(dir, "event.json")
			if err := os.WriteFile(event, []byte(`{"workflow_run":{"id":123,"run_attempt":2,"head_sha":"`+head+`"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			writeExec(t, filepath.Join(dir, "gh"), `#!/usr/bin/env bash
set -euo pipefail
case "$*" in
  *'/actions/workflows/merge-queue.yml'*) echo 44 ;;
  *'/actions/runs/123/attempts/2'*)
    path=.github/workflows/merge-queue.yml
    [[ -z $MUTATION ]] || path=.github/workflows/other.yml
    jq -n --arg created "$CREATED" --arg head "$HEAD" --arg path "$path" '{id:123,workflow_id:44,run_attempt:2,event:"merge_group",status:"completed",repository:{full_name:"reliant-labs/reliant"},head_repository:{full_name:"reliant-labs/reliant"},head_sha:$head,head_branch:"gh-readonly-queue/main/pr-4-aaaaaaaa",path:$path,created_at:$created}' ;;
  *'/actions/runs/123/artifacts'*)
    jq -n --arg head "$HEAD" '{artifacts:[{name:"merge-queue-slo-2",id:99,expired:false,size_in_bytes:256,workflow_run:{id:123,head_sha:$head}}]}' ;;
  *'/actions/artifacts/99/zip'*) cat "$ARCHIVE" ;;
  'issue list '*) echo '[]' ;;
  'label create '*) : ;;
  'issue create '*) printf 'published\n' ;;
  *) exit 12 ;;
esac
`)
			ok, out := ciShell(t, runner, map[string]string{"PATH": dir + ":" + os.Getenv("PATH"), "GITHUB_EVENT_PATH": event,
				"ARCHIVE": archivePath, "HEAD": head, "CREATED": created.Format(time.RFC3339), "MUTATION": tc.metaMutation})
			if tc.publish != strings.Contains(out, "published") || ok != tc.publish {
				t.Fatalf("wanted publish=%v, got ok=%v: %s", tc.publish, ok, out)
			}
		})
	}
}
