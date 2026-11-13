package debug

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeDlvOnPath puts a `dlv` on PATH that prints msg to stderr and exits 1,
// the way a real dlv does when the kernel refuses its attach.
func fakeDlvOnPath(t *testing.T, msg string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake dlv is a shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\necho '" + msg + "' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "dlv"), []byte(script), 0o755); err != nil { // #nosec G306 -- an executable test fixture
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestAttachReportsWhyDlvExited pins that a dlv which exits before it listens
// fails the attach at once, quoting dlv's own error and, on a Yama host that
// forbids it, how to allow the attach. It used to wait out the whole ready
// timeout and print only "timeout waiting for dlv", which is all GitHub's
// runners (ptrace_scope=1) ever said about forge #1990's attach e2e test.
func TestAttachReportsWhyDlvExited(t *testing.T) {
	const refusal = "could not attach to pid 4242: this could be caused by a kernel security setting, try writing \"0\" to /proc/sys/kernel/yama/ptrace_scope"
	fakeDlvOnPath(t, refusal)
	scope := filepath.Join(t.TempDir(), "ptrace_scope")
	if err := os.WriteFile(scope, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldScope, oldTimeout := ptraceScopePath, dlvReadyTimeout
	ptraceScopePath, dlvReadyTimeout = scope, time.Minute
	t.Cleanup(func() { ptraceScopePath, dlvReadyTimeout = oldScope, oldTimeout })

	out := filepath.Join(t.TempDir(), "dlv-attach-4242.log")
	start := time.Now()
	err := NewDelveDebugger().Attach(context.Background(), AttachOptions{PID: 4242, OutputPath: out})
	if err == nil {
		t.Fatal("Attach succeeded with a dlv that exits at once")
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("Attach took %s: it waited out the ready timeout instead of noticing dlv exit", took)
	}
	for _, want := range []string{refusal, "kernel.yama.ptrace_scope is 1", out} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Attach error lacks %q:\n%v", want, err)
		}
	}
}

func TestPtraceScopeHint(t *testing.T) {
	dir := t.TempDir()
	old := ptraceScopePath
	t.Cleanup(func() { ptraceScopePath = old })
	for _, tc := range []struct {
		name, content string
		missing       bool
		wantHint      bool
	}{
		{name: "no yama", missing: true},
		{name: "classic", content: "0\n"},
		{name: "restricted", content: "1\n", wantHint: true},
		{name: "admin only", content: "2\n", wantHint: true},
	} {
		ptraceScopePath = filepath.Join(dir, tc.name)
		if !tc.missing {
			if err := os.WriteFile(ptraceScopePath, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if got := ptraceScopeHint(); (got != "") != tc.wantHint {
			t.Errorf("%s: ptraceScopeHint() = %q, want hint=%v", tc.name, got, tc.wantHint)
		}
	}
}
