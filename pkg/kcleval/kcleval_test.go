package kcleval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeForge writes an executable `forge` into a fresh directory. The script
// records its arguments (one per line) in args.txt, then prints stdout.txt to
// stdout and stderr.txt to stderr and exits with the code in exit.txt. The
// real binary is driven by the e2e test; these tests pin what this package
// does with whatever the binary says, which does not need a real KCL runtime.
type fakeForge struct {
	dir string
	bin string
}

func newFakeForge(t *testing.T, stdout, stderr string, exit int) *fakeForge {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake forge is a POSIX shell script")
	}
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("stdout.txt", stdout)
	write("stderr.txt", stderr)
	write("exit.txt", strconv.Itoa(exit))
	script := `#!/bin/sh
d="$(dirname "$0")"
: > "$d/args.txt"
for a in "$@"; do printf '%s\n' "$a" >> "$d/args.txt"; done
cat "$d/stdout.txt"
cat "$d/stderr.txt" >&2
exit "$(cat "$d/exit.txt")"
`
	bin := filepath.Join(dir, "forge")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &fakeForge{dir: dir, bin: bin}
}

func (f *fakeForge) args(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "args.txt"))
	if err != nil {
		t.Fatalf("the fake forge was never run: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

func TestSelectDecodesTheSelectedValueAndPassesTheRightArguments(t *testing.T) {
	f := newFakeForge(t, `{"client_id":"abc","context":"prod"}`, "", 0)

	var got struct {
		ClientID string `json:"client_id"`
		Context  string `json:"context"`
	}
	err := Select(context.Background(), "deploy/kcl/lib/placement.k", "placement.0", &got, Options{
		Binary:     f.bin,
		ProjectDir: "/work/proj",
		Options:    []string{"env=prod", "region=eu"},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.ClientID != "abc" || got.Context != "prod" {
		t.Errorf("decoded %+v", got)
	}
	want := []string{
		"kcl", "eval", "deploy/kcl/lib/placement.k", "--format", "json",
		"-S", "placement.0",
		"-D", "env=prod", "-D", "region=eu",
		"-C", "/work/proj",
	}
	if args := f.args(t); !reflect.DeepEqual(args, want) {
		t.Errorf("forge was run with\n  %v\nwant\n  %v", args, want)
	}
}

func TestEvalPassesNoSelectorAndDecodesTheWholeDocument(t *testing.T) {
	f := newFakeForge(t, `{"a":1,"b":[true,false]}`, "", 0)

	var doc map[string]any
	if err := Eval(context.Background(), "main.k", &doc, Options{Binary: f.bin}); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if doc["a"] != float64(1) || len(doc["b"].([]any)) != 2 {
		t.Errorf("decoded %v", doc)
	}
	want := []string{"kcl", "eval", "main.k", "--format", "json"}
	if args := f.args(t); !reflect.DeepEqual(args, want) {
		t.Errorf("forge was run with %v, want %v (no -S, -D or -C without options)", args, want)
	}
}

func TestSelectScalars(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		out    any
		want   any
	}{
		{"string", `"hello"`, new(string), "hello"},
		{"int", `42`, new(int), 42},
		{"bool", `true`, new(bool), true},
		{"list", `["a","b"]`, new([]string), []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeForge(t, tt.stdout, "", 0)
			if err := Select(context.Background(), "f.k", "x", tt.out, Options{Binary: f.bin}); err != nil {
				t.Fatal(err)
			}
			got := reflect.ValueOf(tt.out).Elem().Interface()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("decoded %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSelectAndEvalNameTheValueThatDidNotDecode(t *testing.T) {
	f := newFakeForge(t, `"not a number"`, "", 0)

	var n int
	err := Select(context.Background(), "f.k", "count", &n, Options{Binary: f.bin})
	if err == nil {
		t.Fatal("Select decoded a string into an int")
	}
	for _, want := range []string{"f.k", "-S count", "*int", `value was: "not a number"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}

	err = Eval(context.Background(), "f.k", &n, Options{Binary: f.bin})
	if err == nil || !strings.Contains(err.Error(), "decode f.k into *int") {
		t.Errorf("Eval error = %v, want a decode error naming the file and target type", err)
	}
}

func TestForgeFailureSurfacesItsOwnMessageVerbatim(t *testing.T) {
	const diag = "path \"nope\" not found in f.k; the document has fields: a, b"
	f := newFakeForge(t, "", "  "+diag+"\n\n", 1)

	err := Select(context.Background(), "f.k", "nope", new(string), Options{Binary: f.bin})
	if err == nil {
		t.Fatal("Select succeeded although forge exited 1")
	}
	if want := "`forge kcl eval f.k`: " + diag; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestForgeFailureWithNoStderrFallsBackToTheExitError(t *testing.T) {
	f := newFakeForge(t, "", "", 3)

	err := Eval(context.Background(), "f.k", new(any), Options{Binary: f.bin})
	if err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("error = %v, want one carrying 'exit status 3'", err)
	}
}

func TestStdoutIsNotPollutedByStderr(t *testing.T) {
	// Diagnostics go to stderr and must never reach the JSON decoder.
	f := newFakeForge(t, `{"ok":true}`, "warning: lock notice\n", 0)

	var doc map[string]bool
	if err := Eval(context.Background(), "f.k", &doc, Options{Binary: f.bin}); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if !doc["ok"] {
		t.Errorf("decoded %v", doc)
	}
}

func TestMissingBinaryIsErrForgeNotFound(t *testing.T) {
	err := Eval(context.Background(), "f.k", new(any), Options{Binary: filepath.Join(t.TempDir(), "no-such-forge")})
	if !errors.Is(err, ErrForgeNotFound) {
		t.Fatalf("error = %v, want ErrForgeNotFound", err)
	}
	if !strings.Contains(err.Error(), "go install") {
		t.Errorf("error %q does not say how to install forge", err)
	}
}

func TestDefaultBinaryIsForgeOnPath(t *testing.T) {
	f := newFakeForge(t, `"from-path"`, "", 0)
	t.Setenv("PATH", f.dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")

	var got string
	if err := Select(context.Background(), "f.k", "x", &got, Options{}); err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got != "from-path" {
		t.Errorf("got %q; the default binary must be `forge` on PATH", got)
	}

	t.Setenv("PATH", t.TempDir()) // an empty directory: no forge anywhere
	if err := Select(context.Background(), "f.k", "x", &got, Options{}); !errors.Is(err, ErrForgeNotFound) {
		t.Errorf("with no forge on PATH: error = %v, want ErrForgeNotFound", err)
	}
}

func TestTimeoutIsReportedAsATimeoutNamingTheCall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake forge is a POSIX shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "forge")
	// exec so the sleep is the process the timeout kills, not a shell
	// that leaves a child holding the output pipe open.
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	began := time.Now()
	err := Eval(context.Background(), "slow.k", new(any), Options{Binary: bin, Timeout: 100 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "`forge kcl eval slow.k` timed out after 100ms") {
		t.Fatalf("error = %v, want a timeout naming the call", err)
	}
	if elapsed := time.Since(began); elapsed > 20*time.Second {
		t.Errorf("took %v; the timeout did not stop the process", elapsed)
	}
}

func TestACancelledContextStopsTheEvaluation(t *testing.T) {
	f := newFakeForge(t, `{}`, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Eval(ctx, "f.k", new(any), Options{Binary: f.bin})
	if err == nil {
		t.Fatal("Eval succeeded on a cancelled context")
	}
	if !strings.Contains(err.Error(), "timed out") && !strings.Contains(err.Error(), "killed") && !strings.Contains(err.Error(), "canceled") {
		t.Errorf("error = %q, want it to say the call was cut short", err)
	}
}

// fakeT records what MustSelect does to a test without failing the real one.
type fakeT struct {
	testing.TB
	fatal string
}

func (f *fakeT) Helper() {}
func (f *fakeT) Fatalf(format string, args ...any) {
	f.fatal = fmt.Sprintf(format, args...)
	panic(f) // stop the caller the way t.Fatalf's runtime.Goexit does
}

func callMust(f func(tb testing.TB)) (ft *fakeT) {
	ft = &fakeT{}
	defer func() {
		if r := recover(); r != nil && r != ft {
			panic(r)
		}
	}()
	f(ft)
	return ft
}

func TestMustSelectAndMustSelectString(t *testing.T) {
	f := newFakeForge(t, `"prod"`, "", 0)
	t.Setenv("PATH", f.dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")

	var env string
	if ft := callMust(func(tb testing.TB) { MustSelect(tb, "f.k", "env", &env) }); ft.fatal != "" {
		t.Fatalf("MustSelect failed the test: %s", ft.fatal)
	}
	if env != "prod" {
		t.Errorf("MustSelect decoded %q", env)
	}

	var got string
	if ft := callMust(func(tb testing.TB) { got = MustSelectString(tb, "f.k", "env") }); ft.fatal != "" {
		t.Fatalf("MustSelectString failed the test: %s", ft.fatal)
	}
	if got != "prod" {
		t.Errorf("MustSelectString = %q, want prod", got)
	}
}

func TestMustSelectFailsTheTestWithContext(t *testing.T) {
	f := newFakeForge(t, "", "no such field", 1)
	t.Setenv("PATH", f.dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")

	ft := callMust(func(tb testing.TB) { MustSelectString(tb, "deploy/x.k", "missing.path") })
	for _, want := range []string{"deploy/x.k", "-S missing.path", "no such field"} {
		if !strings.Contains(ft.fatal, want) {
			t.Errorf("failure message %q lacks %q", ft.fatal, want)
		}
	}
}
