package buildtarget

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// The production runner interprets the ShellBuild in-process: pipeline, &&,
// env expansion and a non-zero exit all behave without any `sh` binary.
func TestExecRunner_RunShell(t *testing.T) {
	dir := t.TempDir()
	r := Runner{}
	res := r.Build(context.Background(), Spec{
		Service: "s", ProjectDir: dir,
		BuildCmd: `echo "v=$FOO" | { read x; echo "$x" > out.txt; } && test "$(cat out.txt)" = "v=bar"`,
		BuildEnv: map[string]string{"FOO": "bar"},
	})
	if res.Err != nil {
		t.Fatalf("want success, got %v", res.Err)
	}
	if _, err := filepath.Abs(filepath.Join(dir, "out.txt")); err != nil {
		t.Fatal(err)
	}

	res = r.Build(context.Background(), Spec{Service: "s", ProjectDir: dir, BuildCmd: "true && exit 3"})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "exit status 3") {
		t.Fatalf("want exit status 3 error, got %v", res.Err)
	}
}
