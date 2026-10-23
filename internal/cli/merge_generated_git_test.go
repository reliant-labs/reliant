package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/obsdash"
)

// Two branches that each add a service change the same forge files: the
// birth ledger, the JSON outputs' hashes, and a stamped aggregate like the
// service mount list. Before the driver understood them, every pair of
// such branches conflicted in all of them (P3b friction F16). These tests
// run a real `git merge` / `git rebase` with this test binary as the
// driver (TestMain dispatches `merge-generated` to the production
// command), over files rendered by forge's own writers.
//
// The observability outputs are written one file per service (see
// obsdash.ServicePart), so the two branches' are disjoint: the merge must
// produce exactly what a generate over both services writes, with no
// generate in between.

// renderServices writes what a generate over these services writes to the
// shared files, through forge's own writers.
func renderServices(t *testing.T, dir string, services ...string) {
	t.Helper()
	checksums.ResetScaffoldLedgerCache()
	cs, err := checksums.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	in := obsdash.Input{Project: "app"}
	var mounts strings.Builder
	for _, s := range services {
		in.Services = append(in.Services, obsdash.Service{
			Name: s, Package: "app.v1", ProtoService: strings.ToUpper(s[:1]) + s[1:] + "Service",
			Methods: []obsdash.Method{{Name: "Ping"}},
		})
		mounts.WriteString("\t\"" + s + "\",\n")
		checksums.RecordScaffold(dir, "internal/handlers/"+s+"/handlers_scaffold_ping_test.go")
	}
	if _, err := obsdash.Write(dir, in, cs); err != nil {
		t.Fatal(err)
	}
	body := "package app\n\n// Mounts lists the mounted services.\nvar Mounts = []string{\n" + mounts.String() + "}\n"
	if _, err := checksums.WriteGeneratedFile(dir, "internal/app/mounts_gen.go", []byte(body), cs, true); err != nil {
		t.Fatal(err)
	}
	if err := checksums.Save(dir, cs); err != nil {
		t.Fatal(err)
	}
	checksums.ResetScaffoldLedgerCache()
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// twoServiceBranches builds base (system), branch "question" (+question)
// and branch "run" (+run), with the driver wired the way forge wires a
// clone, and leaves "question" checked out.
func twoServiceBranches(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: runs real git merges that exec the test binary as the driver (~10s); runs in task test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeTreeFileT(t, filepath.Join(dir, "forge.yaml"), "name: app\nmodule_path: example.com/app\n")
	writeTreeFileT(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.24\n")
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "config", "merge.forge-generated.driver", "'"+exe+"' merge-generated %O %A %B %L %P")
	if _, ok := writeMergeAttributes(dir); !ok {
		t.Fatal("precondition: the attributes block was written")
	}

	renderServices(t, dir, "system")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "base")
	gitT(t, dir, "checkout", "-qb", "run")
	renderServices(t, dir, "run", "system")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "run")
	gitT(t, dir, "checkout", "-q", "main")
	gitT(t, dir, "checkout", "-qb", "question")
	renderServices(t, dir, "question", "system")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "question")
	return dir
}

// observabilityFiles maps every file under deploy/observability in dir to
// its content.
func observabilityFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := filepath.Join(dir, "deploy", "observability")
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		data, rerr := os.ReadFile(p)
		out[filepath.ToSlash(rel)] = string(data)
		return rerr
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// assertConvergesToBothServices checks a merged tree carries no conflict;
// that its observability outputs ARE what a generate over both services
// writes, certified by the merged hashes.json, with no generate in between;
// and that the next generate renders both services without refusing a
// single file as a hand edit.
func assertConvergesToBothServices(t *testing.T, dir string) {
	t.Helper()
	for _, rel := range []string{".forge/hashes.json", ".forge/scaffolded.json", "internal/app/mounts_gen.go"} {
		data, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "<<<<<<<") {
			t.Errorf("%s carries conflict markers:\n%s", rel, data)
		}
	}
	ledger, _ := os.ReadFile(filepath.Join(dir, checksums.ScaffoldedFile))
	for _, s := range []string{"question", "run"} {
		if !strings.Contains(string(ledger), "internal/handlers/"+s+"/") {
			t.Errorf("the merged ledger lost %s's birth:\n%s", s, ledger)
		}
	}

	fresh := t.TempDir()
	writeTreeFileT(t, filepath.Join(fresh, "go.mod"), "module example.com/app\n\ngo 1.24\n")
	renderServices(t, fresh, "question", "run", "system")

	// The merge alone is the render: reliant's lanes regenerated ~1,500
	// lines of aggregated spec, alerts and saved searches after every
	// rebase, because the merge had kept one side of each.
	merged, want := observabilityFiles(t, dir), observabilityFiles(t, fresh)
	for rel, body := range want {
		if merged[rel] != body {
			t.Errorf("%s after the merge is not what a generate over both services writes; the lane would have to regenerate and amend", rel)
		}
	}
	for rel := range merged {
		if _, ok := want[rel]; !ok {
			t.Errorf("%s survives the merge but a generate over both services does not write it", rel)
		}
	}
	cs, err := checksums.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for rel, body := range merged {
		if cs.Unstampable[rel] != checksums.BodyHash([]byte(body)) {
			t.Errorf("%s is not certified by the merged hashes.json: generate would refuse it as a hand edit", rel)
		}
	}

	// "generate": render the merged inputs. Every shared file must be one
	// forge still certifies, so it is rewritten rather than refused.
	if mounts, _ := os.ReadFile(filepath.Join(dir, "internal/app/mounts_gen.go")); checksums.Verify(mounts) != checksums.Pristine {
		t.Errorf("mounts_gen.go must stay a pristine render:\n%s", mounts)
	}
	renderServices(t, dir, "question", "run", "system")
	got, _ := os.ReadFile(filepath.Join(dir, "internal/app/mounts_gen.go"))
	wantMounts, _ := os.ReadFile(filepath.Join(fresh, "internal/app/mounts_gen.go"))
	if string(got) != string(wantMounts) {
		t.Errorf("mounts_gen.go after regenerating the merge differs from a fresh render of both services")
	}
}

func TestMergeGenerated_TwoServiceBranchesMergeClean(t *testing.T) {
	dir := twoServiceBranches(t)

	// The baseline: git's own merge, without the driver, conflicts.
	attrs := strings.TrimSpace(gitT(t, dir, "rev-parse", "--path-format=absolute", "--git-path", "info/attributes"))
	if err := os.Rename(attrs, attrs+".off"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@t", "merge", "--no-edit", "run")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "CONFLICT") {
		t.Fatalf("precondition: without the driver the two branches conflict, got err=%v\n%s", err, out)
	}
	gitT(t, dir, "merge", "--abort")
	if err := os.Rename(attrs+".off", attrs); err != nil {
		t.Fatal(err)
	}

	gitT(t, dir, "merge", "--no-edit", "run")
	assertConvergesToBothServices(t, dir)
}

func TestMergeGenerated_TwoServiceBranchesRebaseClean(t *testing.T) {
	dir := twoServiceBranches(t)
	gitT(t, dir, "rebase", "run")
	assertConvergesToBothServices(t, dir)
}

// The report that started this: every rebase of a branch that edited one
// proto onto a branch that edited another left gen/forge_descriptor.json
// stamped for protos that no longer existed on disk, so the lane had to
// regenerate and amend before verify-generated would pass.
//
// Real git, real rebase, the test binary as the driver, and the real stamp
// (codegen.DescriptorSources) over a real proto tree: after the rebase the
// descriptor must name exactly the protos the work tree holds, with no
// generate in between.
func TestMergeGenerated_RebaseKeepsDescriptorStampCurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: runs a real git rebase that execs the test binary as the driver; runs in task test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeTreeFileT(t, filepath.Join(dir, "forge.yaml"), "name: app\nmodule_path: example.com/app\n")
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "config", "merge.forge-generated.driver", "'"+exe+"' merge-generated %O %A %B %L %P")
	if _, ok := writeMergeAttributes(dir); !ok {
		t.Fatal("precondition: the attributes block was written")
	}

	stamp := func(services ...string) {
		t.Helper()
		out, err := json.MarshalIndent(codegen.ForgeDescriptor{
			Services: func() (s []codegen.ServiceDef) {
				for _, n := range services {
					s = append(s, codegen.ServiceDef{Name: n})
				}
				return
			}(),
			Sources: codegen.DescriptorSources(dir),
		}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		writeTreeFileT(t, filepath.Join(dir, "gen", "forge_descriptor.json"), string(out)+"\n")
	}
	proto := func(name, body string) {
		t.Helper()
		writeTreeFileT(t, filepath.Join(dir, "proto", name+".proto"), body)
	}
	commit := func(msg string) {
		gitT(t, dir, "add", "-A")
		gitT(t, dir, "commit", "-qm", msg)
	}

	for _, n := range []string{"alpha", "beta", "gamma"} {
		proto(n, "syntax = \"proto3\";\nmessage "+n+" {}\n")
	}
	stamp("alpha", "beta", "gamma")
	commit("base")

	gitT(t, dir, "checkout", "-qb", "feature")
	proto("alpha", "syntax = \"proto3\";\nmessage alpha {}\nmessage AlphaTwo {}\n")
	stamp("alpha", "beta", "gamma")
	commit("feature edits alpha")

	gitT(t, dir, "checkout", "-q", "main")
	proto("beta", "syntax = \"proto3\";\nmessage beta {}\nmessage BetaTwo {}\n")
	proto("delta", "syntax = \"proto3\";\nmessage delta {}\n")
	stamp("alpha", "beta", "gamma", "delta")
	commit("main edits beta and adds delta")

	gitT(t, dir, "checkout", "-q", "feature")
	gitT(t, dir, "rebase", "main")

	data, err := os.ReadFile(filepath.Join(dir, "gen", "forge_descriptor.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got codegen.ForgeDescriptor
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("the rebased descriptor does not parse: %v\n%s", err, data)
	}
	if drift := codegen.DescriptorDrift(dir, got.Sources); drift != "" || len(got.Sources) == 0 {
		t.Errorf("after the rebase the descriptor is stamped for protos the tree does not hold (%q); the lane would have to regenerate and amend:\n%s", drift, data)
	}
	if strings.Contains(string(data), "<<<<<<<") {
		t.Errorf("the descriptor carries conflict markers:\n%s", data)
	}
}
