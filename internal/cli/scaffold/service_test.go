package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestRunAddService_RollsBackOnPipelineFailure is the F3 revert regression:
// when the post-scaffold generate pipeline (buf validate / lint
// lint / build) fails on a FRESH `forge scaffold service`, the files the scaffold
// just wrote — the handler dir and proto/services/<pkg> — must be removed, so
// a failed validation doesn't strand a half-created service that confuses the
// next run.
func TestRunAddService_RollsBackOnPipelineFailure(t *testing.T) {
	dir := withTempProject(t, "name: testproj\nmodule_path: example.com/testproj\n")
	markServiceProject(t, dir) // stamp pkg/app so the project derives to service kind

	f := testFactory()
	f.Gen.RunPipeline = func(string) error { return fmt.Errorf("simulated validation failure") }

	err := runServices(f, []string{"orders"}, false, false)
	if err == nil {
		t.Fatal("expected runService to surface the pipeline failure")
	}

	// Both freshly-scaffolded trees must be gone after rollback.
	for _, p := range []string{
		filepath.Join(dir, "internal", "handlers", "orders"),
		filepath.Join(dir, "proto", "services", "orders"),
	} {
		if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
			t.Errorf("rollback did not remove %s (stat err=%v)", p, statErr)
		}
	}
}

// TestRunAddService_RollbackPreservesPreexistingDirs proves the rollback only
// removes what THIS run created: a handler dir present before the add (a
// --resume-style recovery, or a manual edit) must survive a pipeline failure.
func TestRunAddService_RollbackPreservesPreexistingDirs(t *testing.T) {
	dir := withTempProject(t, "name: testproj\nmodule_path: example.com/testproj\n")
	markServiceProject(t, dir)

	// Pre-create the handler dir with a sentinel file (simulating a prior
	// partial scaffold the user is recovering).
	handlerDir := filepath.Join(dir, "internal", "handlers", "orders")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(handlerDir, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("preexisting"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := testFactory()
	f.Gen.RunPipeline = func(string) error { return fmt.Errorf("simulated validation failure") }

	// --resume so an existing dir is tolerated by the conflict check.
	if err := runServices(f, []string{"orders"}, true, false); err == nil {
		t.Fatal("expected runService to surface the pipeline failure")
	}

	// The pre-existing handler dir (and its sentinel) must be preserved,
	// because --resume is not a fresh add — rollback must not fire.
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("rollback removed a pre-existing handler dir on a --resume run: %v", err)
	}
}

// TestRollbackServiceScaffold_OnlyRemovesFreshDirs unit-tests the rollback
// helper directly: a dir flagged pre-existing is preserved; a fresh one is
// removed.
func TestRollbackServiceScaffold_OnlyRemovesFreshDirs(t *testing.T) {
	root := t.TempDir()
	handlerDir := filepath.Join(root, "internal", "handlers", "svc")
	protoDir := filepath.Join(root, "proto", "services", "svc")
	for _, d := range []string{handlerDir, protoDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// handler pre-existed (preserve), proto is fresh (remove).
	rollbackServiceScaffold(root, "svc", true, false, nil)

	if _, err := os.Stat(handlerDir); err != nil {
		t.Errorf("pre-existing handler dir must be preserved: %v", err)
	}
	if _, err := os.Stat(protoDir); !os.IsNotExist(err) {
		t.Errorf("fresh proto dir must be removed (stat err=%v)", err)
	}
}

// TestRunAddService_RollbackRemovesGeneratedStubs: the pipeline runs `buf
// generate` BEFORE it validates, so a failed scaffold has already written the
// service's stubs into every buf.gen.yaml `out:` — gen/services/<pkg>, and a
// frontend's src/gen/services/<pkg>. Those are usually gitignored, so a
// rollback that removed only the handler and proto dirs left a failed service
// invisible to `git status` yet still compiled into the next build. Stubs of
// services that existed before the scaffold must survive.
func TestRunAddService_RollbackRemovesGeneratedStubs(t *testing.T) {
	dir := withTempProject(t, "name: testproj\nmodule_path: example.com/testproj\n")
	markServiceProject(t, dir)
	bufGen := "version: v2\nplugins:\n  - local: protoc-gen-go\n    out: gen\n  - local: protoc-gen-es\n    out: frontends/web/src/gen\n"
	if err := os.WriteFile(filepath.Join(dir, "buf.gen.yaml"), []byte(bufGen), 0o644); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(dir, "gen", "services", "item", "v1")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}

	f := testFactory()
	f.Gen.RunPipeline = func(root string) error {
		for _, out := range []string{"gen", filepath.Join("frontends", "web", "src", "gen")} {
			stub := filepath.Join(root, out, "services", "orders", "v1")
			if err := os.MkdirAll(stub, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(stub, "orders.pb.go"), []byte("package ordersv1\n"), 0o644); err != nil {
				return err
			}
		}
		return fmt.Errorf("simulated validation failure")
	}

	if err := runServices(f, []string{"orders"}, false, false); err == nil {
		t.Fatal("expected runService to surface the pipeline failure")
	}
	for _, p := range []string{
		filepath.Join(dir, "gen", "services", "orders"),
		filepath.Join(dir, "frontends", "web", "src", "gen", "services", "orders"),
	} {
		if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
			t.Errorf("rollback left generated stubs behind at %s (stat err=%v)", p, statErr)
		}
	}
	if _, err := os.Stat(existing); err != nil {
		t.Errorf("rollback removed a pre-existing service's stubs: %v", err)
	}
}
