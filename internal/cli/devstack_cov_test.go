package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// covCGitProject is a git repository holding forge.yaml and the given block
// registry, with cwd moved into it: prune and unattributed-block reporting ask
// git which worktrees are alive.
func covCGitProject(t *testing.T, registry string) string {
	t.Helper()
	dir := seedRegistry(t, registry)
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(dir)
	return dir
}

const covCRegistry = `{
  "": {"block": 0},
  "prod": {"block": 1},
  "wt-gone": {"block": 2, "stack": true},
  "prod-wt-gone": {"block": 3, "origin": "wt-gone"},
  "mystery-key": {"block": 4}
}`

func TestCovC_DevStackKeyAndPort(t *testing.T) {
	testkit.Slow(t, "execs git")
	covCGitProject(t, `{"": {"block": 0}}`)

	var out bytes.Buffer
	key := newDevStackKeyCmd()
	key.SetOut(&out)
	key.SetArgs(nil)
	if err := key.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Errorf("the primary checkout's key is empty, got %q", out.String())
	}

	out.Reset()
	port := newDevStackPortCmd()
	port.SetOut(&out)
	port.SetArgs([]string{"18080"})
	if err := port.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "18080" {
		t.Errorf("block 0 must return the base unchanged, got %q", out.String())
	}

	bad := newDevStackPortCmd()
	bad.SilenceUsage, bad.SilenceErrors = true, true
	bad.SetArgs([]string{"http"})
	if err := bad.Execute(); err == nil || !strings.Contains(err.Error(), `base port "http" is not an integer`) {
		t.Errorf("err = %v", err)
	}
}

func TestCovC_DevStackPrune(t *testing.T) {
	testkit.Slow(t, "execs git")
	dir := covCGitProject(t, covCRegistry)
	run := func(args ...string) (string, error) {
		cmd := newDevStackPruneCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	registry := func() string {
		b, err := os.ReadFile(filepath.Join(dir, ".forge", "blocks.json"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	out, err := run()
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	for _, want := range []string{"would reclaim: wt-gone (block 2)", "would reclaim: prod-wt-gone (block 3)", "2 block(s) would be freed. Re-run with --apply"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "would reclaim: prod ") || strings.Contains(out, "would reclaim: mystery-key") {
		t.Errorf("a standalone or unattributed key must never be reclaimed:\n%s", out)
	}
	if !strings.Contains(out, "block 4: mystery-key") || !strings.Contains(out, "forge env devstack release <key> --apply") {
		t.Errorf("the unclassifiable block is reported with the way out:\n%s", out)
	}
	if !strings.Contains(registry(), "wt-gone") {
		t.Fatal("a dry run must not rewrite the registry")
	}

	out, err = run("--apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if !strings.Contains(out, "reclaimed: wt-gone (block 2)") || !strings.Contains(out, "2 block(s) freed.") {
		t.Errorf("apply output:\n%s", out)
	}
	reg := registry()
	if strings.Contains(reg, `"wt-gone"`) || strings.Contains(reg, "prod-wt-gone") {
		t.Errorf("reclaimed keys must leave the registry:\n%s", reg)
	}
	for _, keep := range []string{`"prod"`, "mystery-key"} {
		if !strings.Contains(reg, keep) {
			t.Errorf("%s must survive a prune:\n%s", keep, reg)
		}
	}

	out, err = run("--apply")
	if err != nil || !strings.Contains(out, "nothing to prune") {
		t.Errorf("second prune: err %v out %q", err, out)
	}
}

func TestCovC_DevStackPruneRefusesWhenWorktreesCannotBeListed(t *testing.T) {
	testkit.Slow(t, "execs git")
	// Not a git checkout: with no list of live worktrees nothing may be reclaimed.
	dir := seedRegistry(t, covCRegistry)
	t.Chdir(dir)
	cmd := newDevStackPruneCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetArgs([]string{"--apply"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "prune block registry") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".forge", "blocks.json")); !strings.Contains(string(b), "wt-gone") {
		t.Errorf("a failed prune must reclaim nothing:\n%s", b)
	}
}

func TestCovC_DevStackRelease(t *testing.T) {
	testkit.Slow(t, "execs git")
	dir := covCGitProject(t, covCRegistry)
	run := func(args ...string) (string, error) {
		cmd := newDevStackReleaseCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	if _, err := run(); err == nil {
		t.Error("release needs a key")
	}
	out, err := run("mystery-key")
	if err != nil || !strings.Contains(out, "would release: mystery-key (block 4)") || !strings.Contains(out, "Re-run with --apply") {
		t.Fatalf("dry run: err %v out %q", err, out)
	}
	if _, err := run("ghost"); err == nil || !strings.Contains(err.Error(), `no block is registered for key "ghost"`) {
		t.Fatalf("unknown key err = %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".forge", "blocks.json")); !strings.Contains(string(b), "mystery-key") {
		t.Fatal("a dry run must not release anything")
	}
	out, err = run("mystery-key", "--apply")
	if err != nil || !strings.Contains(out, "released: mystery-key (block 4)") {
		t.Fatalf("apply: err %v out %q", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".forge", "blocks.json")); strings.Contains(string(b), "mystery-key") {
		t.Errorf("released key still registered:\n%s", b)
	}
	if _, err := run("mystery-key", "--apply"); err == nil || !strings.Contains(err.Error(), "release block") {
		t.Errorf("releasing twice must fail: %v", err)
	}
}

func TestCovC_ReportUnattributedFlagsALikelyLiveBlock(t *testing.T) {
	testkit.Slow(t, "execs git")
	dir := covCGitProject(t, `{"": {"block": 0}}`)
	// The checkout's own worktree key is its directory name.
	live := filepath.Base(dir)
	if err := os.WriteFile(filepath.Join(dir, ".forge", "blocks.json"),
		[]byte(`{"": {"block": 0}, "prod-`+live+`": {"block": 1}, "orphan": {"block": 2}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := reportUnattributed(&out, dir); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "2 block(s) forge cannot classify") || !strings.Contains(s, "no live worktree matches this key") {
		t.Errorf("report:\n%s", s)
	}
	var empty bytes.Buffer
	if err := os.WriteFile(filepath.Join(dir, ".forge", "blocks.json"), []byte(`{"": {"block": 0}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := reportUnattributed(&empty, dir); err != nil || empty.Len() != 0 {
		t.Errorf("nothing unclassified prints nothing: %v %q", err, empty.String())
	}
}
