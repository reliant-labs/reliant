//go:build e2e

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EParallelServiceBranchesMerge is the end-to-end proof for P3b
// friction F16: two branches cut from one base, each adopting a different
// service through `forge generate`, then merged with `git merge`.
//
// Every file forge generates or keeps state in merges without a person —
// the birth ledger, the JSON outputs' hashes, the descriptor, the
// observability outputs, the generated service mounts. The one file left
// conflicting is internal/app/compose.go, which is the PROJECT's (forge
// reconciles its component set but never owns it): taking either side and
// running `forge generate` re-injects the other side's component. The test
// pins that set exactly, so a new generated file that starts conflicting
// fails here.
//
// Alert inputs are one file per RPC service and merge disjointly. A shared
// process part necessarily changes when both branches add mounted RPCs; a
// generate reconciles that bounded inventory and the next run is a fixed point.
// No obsolete per-RPC-service dashboard or saved search is resurrected.
func TestE2EParallelServiceBranchesMerge(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()
	runCmd(t, dir, forgeBin, "project", "new", "mergeapp", "--mod", "example.com/mergeapp", "--service", "api")
	projectDir := filepath.Join(dir, "mergeapp")
	addCorpusForgePkgReplace(t, projectDir)

	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = projectDir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	mustGit := func(args ...string) string {
		t.Helper()
		out, err := git(args...)
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	commit := func(msg string) {
		t.Helper()
		mustGit("add", "-A")
		mustGit("commit", "-q", "--no-verify", "-m", msg)
	}
	// forge routes its files to the driver in this clone by itself
	// (info/attributes). The COMMAND it will not write here: this test's
	// binary lives under the temp directory and is gone by the next merge,
	// which is exactly the binary forge refuses to name. An installed forge
	// names itself; this one is named by hand.
	mustGit("config", "merge.forge-generated.driver", "'"+forgeBin+"' merge-generated %O %A %B %L %P")

	// Base: two more services, declared and not yet adopted — reliant's
	// shape before each P3b lane.
	for _, s := range []string{"alpha", "beta"} {
		title := strings.ToUpper(s[:1]) + s[1:]
		proto := fmt.Sprintf(`syntax = "proto3";

package services.%[1]s.v1;

option go_package = "example.com/mergeapp/gen/services/%[1]s/v1;%[1]sv1";

// forge:unmanaged — adopted by its own branch.

service %[2]sService {
  rpc Ping%[2]s(Ping%[2]sRequest) returns (Ping%[2]sResponse);
}

message Ping%[2]sRequest {
  string name = 1;
}

message Ping%[2]sResponse {
  string reply = 1;
}
`, s, title)
		writeTreeFileT(t, filepath.Join(projectDir, "proto", "services", s, "v1", s+".proto"), proto)
	}
	runCmd(t, projectDir, forgeBin, "generate")
	if _, err := git("rev-parse", "--verify", "HEAD"); err != nil {
		mustGit("checkout", "-q", "-b", "main")
	}
	commit("base")
	baseBranch := strings.TrimSpace(mustGit("rev-parse", "--abbrev-ref", "HEAD"))

	adopt := func(branch, svc string) {
		t.Helper()
		mustGit("checkout", "-q", baseBranch)
		mustGit("checkout", "-q", "-b", branch)
		p := filepath.Join(projectDir, "proto", "services", svc, "v1", svc+".proto")
		body := readFileE2E(t, p)
		body = strings.Replace(body, "// forge:unmanaged — adopted by its own branch.\n\n", "", 1)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		runCmd(t, projectDir, forgeBin, "generate")
		commit("adopt " + svc)
	}
	adopt("lane-alpha", "alpha")
	adopt("lane-beta", "beta")

	out, err := git("merge", "--no-edit", "lane-alpha")
	conflicted := strings.Fields(mustGit("diff", "--name-only", "--diff-filter=U"))
	if err == nil {
		t.Logf("merge was clean:\n%s", out)
	}
	if strings.Join(conflicted, ",") != "internal/app/compose.go" && len(conflicted) != 0 {
		t.Fatalf("only the project-owned internal/app/compose.go may conflict; conflicted %v\n%s", conflicted, out)
	}
	if len(conflicted) == 1 {
		mustGit("checkout", "--ours", "internal/app/compose.go")
	}
	mustGit("add", "-A")
	mustGit("commit", "-q", "--no-verify", "--no-edit")

	// Adding an RPC changes the process that mounts it. Its part is rebuilt
	// from both branches, while independent alert inputs merge unchanged.
	runCmd(t, projectDir, forgeBin, "generate")
	// Reusable fragments merge/reconcile without claiming process ownership.
	// The selected environment's rendered workload supplies that at bundle time.
	if matches, _ := filepath.Glob(filepath.Join(projectDir, "deploy/observability/spec/process-*_gen.json")); len(matches) != 0 {
		t.Fatalf("ordinary generate emitted environment process parts: %v", matches)
	}
	compose := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "compose.go"))
	for _, want := range []string{"alpha.New(", "beta.New("} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose.go after generate lacks %s", want)
		}
	}
	body := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "routes_gen.go"))
	for _, svc := range []string{"alpha", "beta"} {
		if !strings.Contains(strings.ToLower(body), svc) {
			t.Errorf("routes_gen.go after generate does not carry %s", svc)
		}
		readFileE2E(t, filepath.Join(projectDir, "deploy", "observability", "spec", "alert-service-"+svc+"_gen.json"))
	}
	commit("regenerate")

	// A fixed point: one more generate changes nothing.
	runCmd(t, projectDir, forgeBin, "generate")
	if st := strings.TrimSpace(mustGit("status", "--porcelain")); st != "" {
		t.Errorf("generate after the merge is not a fixed point:\n%s", st)
	}
}
